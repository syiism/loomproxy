# -*- coding: utf-8 -*-
"""quota 模块集成测试（Python 版）：数据源访问控制/计费/限流中间件主路径。
对应 Go 版 test/quota_integration_test.go。
中间件链：monitor → baseURLCheck → DataSourceAccess → Billing → RateLimit → handler。

打的三个源来自假源夹具（testkit/fakesource，经 cmd/fakegateway 挂载）：fake_a 为主用例源，
fake_b / fake_c 用于需要隔离限流与计费状态的用例。
"""
import time
from urllib.parse import urlencode


FAKE_A = "fake_a"
FAKE_B = "fake_b"
FAKE_C = "fake_c"

# 与 testkit/fakesource.Actions 一致：seed 按声明播种，/endpoints 按声明对账
FAKE_ACTIONS = ["search", "detail", "chapter", "content", "explore"]


def _q(params: dict) -> str:
    return "?" + urlencode(params)


def _search(source=FAKE_A):
    return "/%s/search" % source + _q({"query": "测试"})


def test_access_disabled_source(server):
    """数据源被管理员禁用（status=0）后全员 403。"""
    api, db = server.api, server.db
    token = api.register("q_user1", "q_user1@example.com", "pass1234")

    db.disable_source(FAKE_A)

    status, env = api.get(_search(), token=token, expected=403)
    assert "禁用" in env["msg"]


def test_access_plan_not_include(server):
    """套餐未关联数据源时普通用户 403，管理员豁免。"""
    api, db = server.api, server.db
    token = api.register("q_user2", "q_user2@example.com", "pass1234")

    db.unbind_source_from_plan("free", FAKE_A)

    status, env = api.get(_search(), token=token, expected=403)
    assert "套餐不包含" in env["msg"]

    # 管理员跳过访问控制（无 baseUrl 会走到 handler 报参数错误，但绝不应 403）
    admin = api.admin_token()
    status, _ = api.get(_search(), token=admin)
    assert status != 403, "管理员请求不应被访问控制拦截"


def test_rate_limit_interval(server, admin):
    """QuotaCost.Interval 生效：同用户第二次请求 429；管理员豁免。"""
    api, db = server.api, server.db
    db.set_quota_cost_interval(FAKE_A, "search", 60)

    token = api.register("q_user6", "q_user6@example.com", "pass1234")

    # 第 1 次：放行（无 baseUrl 会走到 handler 报参数错误，但不应被限流）
    status, _ = api.get(_search(), token=token)
    assert status != 429, "第 1 次请求即被限流"

    # 第 2 次：同用户同 IP，间隔内 429
    status, _ = api.get(_search(), token=token, expected=429)

    # 管理员豁免一切速率限制
    status, _ = api.get(_search(), token=admin)
    assert status != 429, "管理员请求被限流，want 豁免"


def test_rate_limit_window_count(server, admin):
    """窗口计数限流：窗口内允许突发（前 N 次放行），用满后剩余窗口一律 429。"""
    api, db = server.api, server.db
    # fake_a/detail：60 秒内最多 3 次
    db.set_quota_cost_window(FAKE_A, "detail", limit_count=3, window_sec=60)

    token = api.register("q_user9", "q_user9@example.com", "pass1234")
    path = "/%s/detail" % FAKE_A + _q({"bookId": "1"})

    # 窗口内可突发：连续 3 次全部放行
    for i in range(1, 4):
        status, _ = api.get(path, token=token)
        assert status != 429, f"窗口内第 {i} 次请求被限流"

    # 第 4 次：窗口已累计 3 次，429
    status, _ = api.get(path, token=token, expected=429)

    # 管理员豁免
    status, _ = api.get(path, token=admin)
    assert status != 429, "管理员请求被限流，want 豁免"


def test_rate_limit_window_expiry(server):
    """1 秒窗口过期后恢复放行（用 fake_b 与其余用例隔离限流器状态）。"""
    api, db = server.api, server.db
    db.set_quota_cost_window(FAKE_B, "detail", limit_count=1, window_sec=1)

    token = api.register("q_user11", "q_user11@example.com", "pass1234")
    path = "/%s/detail" % FAKE_B + _q({"bookId": "1"})

    status, _ = api.get(path, token=token)
    assert status != 429, "第 1 次请求即被限流"

    status, _ = api.get(path, token=token, expected=429)

    time.sleep(1.2)
    status, _ = api.get(path, token=token)
    assert status != 429, "窗口过期后仍被限流，want 放行"


def test_rate_limit_plan_interval(server, admin):
    """套餐级限流（QuotaCostPlan.interval）：间隔内连续请求第 2 次 429，管理员豁免。"""
    api, db = server.api, server.db
    db.insert_quota_cost_plan("free", FAKE_C, "content", interval=10)

    token = api.register("q_user7", "q_user7@example.com", "pass1234")
    db.bind_plan("q_user7", "free")

    path = "/%s/content" % FAKE_C + _q({"bookId": "1", "itemId": "2"})

    status, _ = api.get(path, token=token)
    assert status != 429, "第 1 次请求即被限流"

    status, _ = api.get(path, token=token, expected=429)

    status, _ = api.get(path, token=admin)
    assert status != 429, "管理员请求被限流，want 豁免"


def test_billing_quota_exhausted(server, fake_upstream):
    """假上游成功(200)后计费扣减，当日额度用完后返回 429。"""
    api, db = server.api, server.db
    upstream = fake_upstream
    db.insert_platform_baseurl(FAKE_A, f"http://127.0.0.1:{upstream.server_address[1]}")

    # free 套餐 fake_a 当日限额改为 1
    db.set_quota_limit("free", FAKE_A, 1)

    token = api.register("q_user5", "q_user5@example.com", "pass1234")

    # 第 1 次：上游成功 → 200。假源把上游 JSON 原样透出（不做 Legado 归一化、不套信封），
    # 假上游固定回 {}，这里正是验证「上游成功 → 计费已扣」的支点
    status, env = api.get(_search(), token=token, expected=200)
    assert env == {}, f"期望上游原样透传, got {env}"

    # 额度用尽：第 2 次 429，提示含「额度已用完」
    status, env = api.get(_search(), token=token, expected=429)
    assert "额度已用完" in env["msg"]


def test_billing_deducts_on_success(server, fake_upstream):
    """上游成功(200)后写 quota_usage_logs 一行。"""
    api, db = server.api, server.db
    upstream = fake_upstream
    db.insert_platform_baseurl(FAKE_A, f"http://127.0.0.1:{upstream.server_address[1]}")

    token = api.register("q_user4", "q_user4@example.com", "pass1234")
    uid = db.user_id_by_name("q_user4")

    api.get(_search(), token=token, expected=200)

    row = db.q1(
        "SELECT COUNT(*) FROM quota_usage_logs WHERE user_id = ? AND group_code = 'fake_a' "
        "AND interface = 'search'", (uid,))
    assert row[0] == 1, f"用量流水条数 = {row[0]}, want 1"


def test_expected_endpoints(server, admin):
    """数据源接口全量回归：三个假源及其声明的动作接口都要在 /endpoints 里，且**不多不少**。

    只断言这三个假源自己的动作集合相等，不断言整个数据源集合相等——
    `sources/all.go` 是「本部署携带哪些源」的清单（骨架为空、携带形态往里加源包），
    拿相等去要求携带形态等于要求它不携带，那条断言在骨架自己的设计里就不成立。
    """
    expected = {src: FAKE_ACTIONS for src in (FAKE_A, FAKE_B, FAKE_C)}

    status, env = server.api.get("/endpoints", token=admin, expected=200)
    got = {e["id"]: {ep["path"] for ep in e["endpoints"]} for e in env["data"]}

    missing = sorted(set(expected) - set(got))
    assert not missing, f"缺少假源: {missing}（实得 {sorted(got)}）"
    for source, actions in expected.items():
        want = {f"/{source}/{act}" for act in actions}
        assert want == got[source], f"{source} 的动作集不一致: want {sorted(want)} got {sorted(got[source])}"
