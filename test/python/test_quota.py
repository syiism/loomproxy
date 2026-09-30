# -*- coding: utf-8 -*-
"""quota 模块集成测试（Python 版）：数据源访问控制/计费/限流中间件主路径。
对应 Go 版 test/quota_integration_test.go。
中间件链：monitor → baseURLCheck → DataSourceAccess → Billing → RateLimit → handler。
"""
import time
from urllib.parse import urlencode


def _q(params: dict) -> str:
    return "?" + urlencode(params)


def _qq_search():
    return "/qq_luomu/search" + _q({"query": "测试"})


def test_access_disabled_source(server):
    """数据源被管理员禁用（status=0）后全员 403。"""
    api, db = server.api, server.db
    token = api.register("q_user1", "q_user1@example.com", "pass1234")

    db.disable_source("qq_luomu")

    status, env = api.get(_qq_search(), token=token, expected=403)
    assert "禁用" in env["msg"]


def test_access_plan_not_include(server):
    """套餐未关联数据源时普通用户 403，管理员豁免。"""
    api, db = server.api, server.db
    token = api.register("q_user2", "q_user2@example.com", "pass1234")

    db.unbind_source_from_plan("free", "qq_luomu")

    status, env = api.get(_qq_search(), token=token, expected=403)
    assert "套餐不包含" in env["msg"]

    # 管理员跳过访问控制（无 baseUrl 会走到 handler 报参数错误，但绝不应 403）
    admin = api.admin_token()
    status, _ = api.get(_qq_search(), token=admin)
    assert status != 403, "管理员请求不应被访问控制拦截"


def test_rate_limit_interval(server, admin):
    """QuotaCost.Interval 生效：同用户第二次请求 429；管理员豁免。"""
    api, db = server.api, server.db
    db.set_quota_cost_interval("qq_luomu", "search", 60)

    token = api.register("q_user6", "q_user6@example.com", "pass1234")

    # 第 1 次：放行（无 baseUrl 会走到 handler 报参数错误，但不应被限流）
    status, _ = api.get(_qq_search(), token=token)
    assert status != 429, "第 1 次请求即被限流"

    # 第 2 次：同用户同 IP，间隔内 429
    status, _ = api.get(_qq_search(), token=token, expected=429)

    # 管理员豁免一切速率限制
    status, _ = api.get(_qq_search(), token=admin)
    assert status != 429, "管理员请求被限流，want 豁免"


def test_rate_limit_window_count(server, admin):
    """窗口计数限流：窗口内允许突发（前 N 次放行），用满后剩余窗口一律 429。"""
    api, db = server.api, server.db
    # qq_luomu/detail：60 秒内最多 3 次
    db.set_quota_cost_window("qq_luomu", "detail", limit_count=3, window_sec=60)

    token = api.register("q_user9", "q_user9@example.com", "pass1234")
    path = "/qq_luomu/detail" + _q({"bookId": "1"})

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
    """1 秒窗口过期后恢复放行。"""
    api, db = server.api, server.db
    db.set_quota_cost_window("qm_luomu", "detail", limit_count=1, window_sec=1)

    token = api.register("q_user11", "q_user11@example.com", "pass1234")
    path = "/qm_luomu/detail" + _q({"bookId": "1"})

    status, _ = api.get(path, token=token)
    assert status != 429, "第 1 次请求即被限流"

    status, _ = api.get(path, token=token, expected=429)

    time.sleep(1.2)
    status, _ = api.get(path, token=token)
    assert status != 429, "窗口过期后仍被限流，want 放行"


def test_rate_limit_plan_interval(server, admin):
    """套餐级限流（QuotaCostPlan.interval）：间隔内连续请求第 2 次 429，管理员豁免。"""
    api, db = server.api, server.db
    db.insert_quota_cost_plan("free", "qq_luomu", "content", interval=10)

    token = api.register("q_user7", "q_user7@example.com", "pass1234")
    db.bind_plan("q_user7", "free")

    path = "/qq_luomu/content" + _q({"bookId": "1", "itemId": "2"})

    status, _ = api.get(path, token=token)
    assert status != 429, "第 1 次请求即被限流"

    status, _ = api.get(path, token=token, expected=429)

    status, _ = api.get(path, token=admin)
    assert status != 429, "管理员请求被限流，want 豁免"


def test_billing_quota_exhausted(server, fake_upstream):
    """假上游成功(200)后计费扣减，当日额度用完后返回 429。"""
    api, db = server.api, server.db
    upstream = fake_upstream
    db.insert_platform_baseurl("qq_luomu", f"http://127.0.0.1:{upstream.server_address[1]}")

    # free 套餐 qq_luomu 当日限额改为 1
    db.set_quota_limit("free", "qq_luomu", 1)

    token = api.register("q_user5", "q_user5@example.com", "pass1234")

    # 第 1 次：上游成功 → 200（统一 Legado 空书单，data 源裸 JSON 而非信封）
    status, env = api.get(_qq_search(), token=token, expected=200)
    assert env is not None and env.get("bookList") == []

    # 额度用尽：第 2 次 429，提示含「额度已用完」
    status, env = api.get(_qq_search(), token=token, expected=429)
    assert "额度已用完" in env["msg"]


def test_billing_deducts_on_success(server, fake_upstream):
    """上游成功(200)后写 quota_usage_logs 一行。"""
    api, db = server.api, server.db
    upstream = fake_upstream
    db.insert_platform_baseurl("qq_luomu", f"http://127.0.0.1:{upstream.server_address[1]}")

    token = api.register("q_user4", "q_user4@example.com", "pass1234")
    uid = db.user_id_by_name("q_user4")

    api.get(_qq_search(), token=token, expected=200)

    row = db.q1(
        "SELECT COUNT(*) FROM quota_usage_logs WHERE user_id = ? AND group_code = 'qq_luomu' "
        "AND interface = 'search'", (uid,))
    assert row[0] == 1, f"用量流水条数 = {row[0]}, want 1"


def test_expected_endpoints(server, admin):
    """数据源接口全量回归：/endpoints 应包含 10 个数据源及其动作接口。"""
    expected = {
        "qq_luomu": ["search", "detail", "chapter", "content", "recommend", "rank", "related", "author"],
        "fq_mufan": ["search", "detail", "chapter", "content", "front", "landing", "recommend", "rank"],
        "fq_xinghai": ["search", "detail", "chapter", "content", "recommend", "rank"],
        "fq_luomu": ["search", "detail", "chapter", "content"],
        "fq_jingluo": ["search", "detail", "chapter", "content"],
        "qq_luomu": ["search", "detail", "chapter", "content"],
        "qm_luomu": ["search", "detail", "chapter", "content"],
        "sq_luomu": ["search", "detail", "chapter", "content"],
        "uxx": ["search", "detail", "chapter", "content", "recommend"],
        "xmly": ["search", "detail", "chapter", "content"],
    }
    status, env = server.api.get("/endpoints", token=admin, expected=200)
    got = {e["id"]: {ep["path"] for ep in e["endpoints"]} for e in env["data"]}

    assert set(got) == set(expected), f"数据源集合不一致: want {sorted(expected)} got {sorted(got)}"
    for source, actions in expected.items():
        for act in actions:
            assert f"/{source}/{act}" in got[source], f"缺少 {source}/{act}"