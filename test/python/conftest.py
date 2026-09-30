# -*- coding: utf-8 -*-
"""test/python 脚手架 —— Go 版集成测试（test/*_test.go）的 Python 移植。

设计（对应 test/testserver_test.go 的 newTestServer 思路）：
  * 每个用例起一个独立 Go 服务子进程：临时目录 SQLite + AUTH_ENABLED=true +
    POOL_ENABLED=false + 固定测试 JWT_SECRET，跑完即销毁，用例间互不污染；
  * 服务二进制来自 cmd/fakegateway（登记 testkit/fakesource 的三个假源），底座产品入口
    不带任何数据源，跨进程用例没有可打的路由；
  * 通过内置 http.server 假上游注入 data-source 平台默认 baseUrl（SSRF 可信豁免），
    得以覆盖“上游成功→计费扣减→额度用尽 429”的完整主链路；
  * 通过 sqlite3 直写同一份 SQLite（WAL，与 Go 进程并发读写安全），组合四类中间件
    （鉴权/计费/限流/号池之外的访问控制）场景，与 Go 用例直接改库的思路一致。

运行（仓库根目录）：
  pip install pytest
  pytest test/python -v
脚手架会自动在 web/dist 补占位、缺失时执行 `CGO_ENABLED=1 go build`，
也可用环境变量 LOOMPROXY_BIN 指向预编译的二进制以加速。
"""
import json
import os
import socket
import sqlite3
import subprocess
import threading
import time
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request, urlopen

import pytest

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
TEST_DATA = os.path.join(REPO_ROOT, "data")

ADMIN_USER = "admin"
ADMIN_PASS = "admin1234"
JWT_SECRET = "test-jwt-secret"

# ---------- 二进制准备 ----------

def _binary() -> str:
    """返回可执行的测试服务二进制（cmd/fakegateway，带假源夹具）。可被 LOOMPROXY_BIN 覆盖。"""
    custom = os.environ.get("LOOMPROXY_BIN")
    if custom:
        assert os.path.exists(custom), f"LOOMPROXY_BIN 指定的二进制不存在: {custom}"
        return custom
    # 每次会话重建：夹具声明与 Go 源码的变更不该被缓存的二进制悄悄带过（增量编译很快）
    target = os.path.join(REPO_ROOT, "loomproxy-fakegateway")
    # go:embed 需要 web/dist 存在占位（gitignored），补一个空的避免编译失败
    dist = os.path.join(REPO_ROOT, "web", "dist")
    os.makedirs(os.path.join(dist, "assets"), exist_ok=True)
    idx = os.path.join(dist, "index.html")
    if not os.path.exists(idx):
        with open(idx, "w") as f:
            f.write("<!DOCTYPE html><html><head><meta charset=\"utf-8\">"
                    "<title>LoomProxy test</title></head><body></body></html>")
    if not os.listdir(os.path.join(dist, "assets")):
        with open(os.path.join(dist, "assets", "app.js"), "w") as f:
            f.write("/* test placeholder */")
    print("构建测试服务二进制 cmd/fakegateway（首次较慢）...", flush=True)
    r = subprocess.run(
        ["go", "build", "-o", target, "./cmd/fakegateway"],
        cwd=REPO_ROOT,
        env={**os.environ, "CGO_ENABLED": "1"},
    )
    assert r.returncode == 0, "go build ./cmd/fakegateway 失败"
    return target


BINARY = _binary()


# ---------- 工具 ----------

def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class Api:
    """极简 HTTP 客户端（标准库实现，避免额外依赖），处理统一响应信封 {"code","msg","data"}。"""

    def __init__(self, base: str):
        self.base = base

    @staticmethod
    def build_query(params: dict) -> str:
        return "?" + urlencode(params)

    def request(self, method, path, body=None, token=None, expected=None):
        data = json.dumps(body).encode() if body is not None else None
        headers = {}
        if body is not None:
            headers["Content-Type"] = "application/json"
        if token:
            headers["Authorization"] = "Bearer " + token
        req = Request(self.base + path, data=data, headers=headers, method=method)
        try:
            resp = urlopen(req, timeout=20)
            status, raw = resp.getcode(), resp.read()
        except HTTPError as e:
            status, raw = e.code, e.read()
        env = None
        try:
            env = json.loads(raw)
        except Exception:
            env = None
        if expected is not None:
            assert status == expected, (
                f"{method} {path} 期望 {expected} 实得 {status}: {raw[:300]!r}")
        return status, env

    def get(self, path, token=None, expected=None):
        return self.request("GET", path, None, token, expected)

    def post(self, path, body=None, token=None, expected=None):
        return self.request("POST", path, body, token, expected)

    def patch(self, path, body=None, token=None, expected=None):
        return self.request("PATCH", path, body, token, expected)

    def put(self, path, body=None, token=None, expected=None):
        return self.request("PUT", path, body, token, expected)

    def delete(self, path, token=None, expected=None):
        return self.request("DELETE", path, None, token, expected)

    def register(self, username, email, password):
        status, env = self.post("/auth/register",
                                {"username": username, "email": email, "password": password},
                                expected=200)
        token = env["data"]["token"]
        assert token, f"注册 {username} 响应缺少 token: {env}"
        return token

    def login(self, identity, password):
        status, env = self.post("/auth/login",
                                {"username": identity, "password": password}, expected=200)
        return env["data"]["token"]

    def admin_token(self):
        return self.login(ADMIN_USER, ADMIN_PASS)


class Db:
    """直连同一份 SQLite（服务进程 WAL 模式），等价于 Go 用例里的 db.DB 改库。"""

    def __init__(self, db_path: str):
        self.path = db_path

    def _conn(self) -> sqlite3.Connection:
        return sqlite3.connect(self.path)

    def q1(self, sql, args=()):
        con = self._conn()
        try:
            return con.execute(sql, args).fetchone()
        finally:
            con.close()

    def sql(self, sql, args=()) -> int:
        con = self._conn()
        try:
            cur = con.execute(sql, args)
            con.commit()
            return cur.lastrowid
        finally:
            con.close()

    # ---- 语义化快捷方法 ----
    def user_id_by_name(self, username):
        row = self.q1("SELECT id FROM users WHERE username = ?", (username,))
        assert row, f"用户 {username} 不存在"
        return row[0]

    def plan_id_by_code(self, code):
        row = self.q1("SELECT id FROM quota_plans WHERE code = ?", (code,))
        assert row, f"套餐 {code} 不存在"
        return row[0]

    def bind_plan(self, username, plan_code):
        self.sql("UPDATE users SET plan_id = ? WHERE username = ?",
                 (self.plan_id_by_code(plan_code), username))

    def disable_source(self, source):
        self.sql("UPDATE data_sources SET status = 0 WHERE name = ?", (source,))

    def set_quota_cost_interval(self, source, iface, interval):
        self.sql("UPDATE quota_costs SET interval = ? WHERE group_code = ? AND interface = ?",
                 (interval, source, iface))

    def set_quota_cost_window(self, source, iface, limit_count, window_sec, interval=0):
        self.sql("UPDATE quota_costs SET limit_count = ?, window_sec = ?, interval = ? "
                 "WHERE group_code = ? AND interface = ?",
                 (limit_count, window_sec, interval, source, iface))

    def set_quota_limit(self, plan_code, target, new_limit, scope="source"):
        self.sql("UPDATE quota_limits SET `limit` = ? WHERE plan_id = ? AND scope = ? AND target = ?",
                 (new_limit, self.plan_id_by_code(plan_code), scope, target))

    def insert_quota_cost_plan(self, plan_code, source, iface,
                               interval=0, limit_count=0, window_sec=60):
        self.sql("INSERT INTO quota_cost_plans "
                 "(plan_id, group_code, interface, interval, limit_count, window_sec, created_at, updated_at) "
                 "VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))",
                 (self.plan_id_by_code(plan_code), source, iface, interval, limit_count, window_sec))

    def insert_platform_baseurl(self, source, base_url):
        self.sql("INSERT INTO platform_source_configs "
                 "(source_name, base_url, created_at, updated_at) VALUES (?,?,datetime('now'),datetime('now'))",
                 (source, base_url))

    def unbind_source_from_plan(self, plan_code, source):
        self.sql("DELETE FROM quota_plan_data_sources WHERE plan_id = ? AND data_source_id = "
                 "(SELECT id FROM data_sources WHERE name = ?)",
                 (self.plan_id_by_code(plan_code), source))


@dataclass
class ServerCtx:
    api: Api
    db: Db
    base: str


def _spawn():
    """构建并启动一个全新 LoomProxy 服务；返回 (ServerCtx, proc)。"""
    data_dir = tempfile_data_dir()
    port = _free_port()
    env = {k: v for k, v in os.environ.items() if "PROXY" not in k.upper()}
    env.update({
        "DB_TYPE": "sqlite",
        "DATA_DIR": data_dir,
        "DB_NAME": "test",
        "AUTH_ENABLED": "true",
        "JWT_SECRET": JWT_SECRET,
        "ADMIN_USERNAME": ADMIN_USER,
        "ADMIN_PASSWORD": ADMIN_PASS,
        "SERVER_HOST": "127.0.0.1",
        "SERVER_PORT": str(port),
        "REDIS_ENABLED": "false",
        "POOL_ENABLED": "false",
        "UPSTREAM_CACHE_TTL": "0",
        "NO_PROXY": "localhost,127.0.0.1",
    })
    proc = subprocess.Popen([BINARY], env=env,
                            stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT)
    base = f"http://127.0.0.1:{port}"
    api = Api(base)
    deadline = time.time() + 30
    while time.time() < deadline:
        if proc.poll() is not None:
            raise RuntimeError(f"LoomProxy 进程提前退出，code={proc.returncode}")
        try:
            if api.get("/")[0] == 200:
                break
        except Exception:
            pass
        time.sleep(0.2)
    else:
        proc.kill()
        raise RuntimeError("等待 LoomProxy 就绪超时")
    return ServerCtx(api=api, db=Db(os.path.join(data_dir, "test.db")), base=base), proc


def tempfile_data_dir() -> str:
    import tempfile
    d = tempfile.mkdtemp(prefix="loomproxy-py-")
    return d


@pytest.fixture
def server():
    """每个用例一个全新服务+全新库，等价于 Go 的 newTestServer。"""
    ctx, proc = _spawn()
    yield ctx
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()


@pytest.fixture
def api(server):
    return server.api


@pytest.fixture
def db(server):
    return server.db


@pytest.fixture
def admin(server):
    return server.api.admin_token()


@pytest.fixture
def fake_upstream():
    """假上游：任意请求返回 {}（假源原样透传，返回 200 并触发计费）。"""

    class H(BaseHTTPRequestHandler):
        def do_GET(self):
            body = b"{}"
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    yield srv
    srv.shutdown()