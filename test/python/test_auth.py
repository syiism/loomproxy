# -*- coding: utf-8 -*-
"""auth 模块集成测试（Python 版）：注册/登录（用户名+邮箱）/会话管理/禁用/软删除。
对应 Go 版 test/auth_integration_test.go。
"""
import base64
import json


def _jwt_payload(token: str) -> dict:
    """解析 JWT 的 payload（不验签，仅测试用）。"""
    parts = token.split(".")
    assert len(parts) == 3, f"JWT 格式错误: {len(parts)} 段"
    padded = parts[1] + "=" * (-len(parts[1]) % 4)
    return json.loads(base64.urlsafe_b64decode(padded))


def test_register_and_login(server):
    """注册 → 用户名登录 → GET /auth/me 返回本人资料。"""
    api = server.api
    token = api.register("alice", "alice@example.com", "pass1234")
    assert token

    token2 = api.login("alice", "pass1234")

    status, env = api.get("/auth/me", token=token2, expected=200)
    assert env["code"] == 0
    assert env["data"]["username"] == "alice"


def test_login_with_email(server):
    """回归 fcb8c43：登录支持邮箱（前端单输入框统一填 username 字段）。"""
    api = server.api
    api.register("bob", "bob@example.com", "pass1234")

    token = api.login("bob@example.com", "pass1234")
    assert token

    status, env = api.get("/auth/me", token=token, expected=200)
    assert env["data"]["username"] == "bob"


def test_login_wrong_password(server):
    """错误密码返回 401 且提示含「用户名或密码错误」。"""
    api = server.api
    api.register("carol", "carol@example.com", "pass1234")

    status, env = api.post("/auth/login",
                           {"username": "carol", "password": "wrongpass1"},
                           expected=401)
    assert "用户名或密码错误" in env["msg"]


def test_login_disabled_user(server, admin):
    """回归 44db90f：禁用用户登录 403，且已签发 token 随会话吊销失效。"""
    api, db = server.api, server.db
    token = api.register("dave", "dave@example.com", "pass1234")
    uid = db.user_id_by_name("dave")

    # 管理员禁用（status=0，同步吊销会话）
    status, env = api.patch(f"/admin/users/{uid}", {"status": 0},
                            token=admin, expected=200)
    assert env["code"] == 0

    status, env = api.post("/auth/login",
                           {"username": "dave", "password": "pass1234"},
                           expected=403)
    assert "禁用" in env["msg"]

    # 禁用前签发的旧 token 已失效
    api.get("/auth/me", token=token, expected=401)


def test_register_duplicate(server):
    """重复用户名/邮箱注册返回 409。"""
    api = server.api
    api.register("erin", "erin@example.com", "pass1234")

    # 重复用户名
    api.post("/auth/register",
             {"username": "erin", "email": "erin2@example.com", "password": "pass1234"},
             expected=409)
    # 重复邮箱
    api.post("/auth/register",
             {"username": "erin2", "email": "erin@example.com", "password": "pass1234"},
             expected=409)


def test_logout_revokes_session(server):
    """logout 吊销当前会话，原 token 立即失效。"""
    api = server.api
    token = api.register("grace", "grace@example.com", "pass1234")

    status, env = api.post("/auth/logout", token=token, expected=200)
    assert env["code"] == 0

    api.get("/auth/me", token=token, expected=401)


def test_me_requires_auth(server):
    """无凭证访问受保护接口返回 401。"""
    status, env = server.api.get("/auth/me", expected=401)


def test_admin_login(server):
    """seed 创建的 admin 可登录，/auth/me 返回 admin。"""
    api = server.api
    token = api.admin_token()
    status, env = api.get("/auth/me", token=token, expected=200)
    assert env["code"] == 0
    assert env["data"]["username"] == "admin"


def test_login_never_expires(server, admin):
    """系统设置 jwt_expire_hours=-1 时签发无 exp 的永久 token。"""
    api = server.api
    status, env = api.put("/admin/settings/jwt_expire_hours", {"value": "-1"},
                          token=admin, expected=200)
    assert env["code"] == 0

    status, env = api.post("/auth/register",
                           {"username": "ne_user", "email": "ne_user@example.com",
                            "password": "pass1234"}, expected=200)
    payload = _jwt_payload(env["data"]["token"])
    assert "exp" not in payload, "永久 token 不应包含 exp 声明"

    api.get("/auth/me", token=env["data"]["token"], expected=200)