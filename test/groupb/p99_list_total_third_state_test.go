package groupb

// 待办清单 P99 剩下的那一格：**列表页的 total**。
//
// 坏形状（改前实测过，不是推测）：把 `quota_usage_logs` 整张表拿掉，
// `GET /admin/usage-logs` 仍然 200、仍然回 `"total":0`，服务端只出一条 LogReadFail。
// 于是面板上「共 0 条」与「这次根本没读到」长得一模一样——
// 而 P99 第一步选的是"只出声不改响应"，第二步只把 -1 推广到了 `/admin/stats` 那四个 Count。
//
// 现在两处列表口（管理端与本人面）读失败都写 `-1`，形状照 P54 的 `table_growth`
// 与 P99 第二步的看板格；**不改成 500** 是刻意的：这一页的**行**可能已经取到了，
// 打死整页会丢掉那部分真相，而第三态本来就是给"数字不可用而表格照常可读"准备的。
//
// 反向断言也在这一条里：失败样本必须**只伤读数不伤鉴权**（P99 第二步现场注释那条——
// 第一次想拿改名 `roles` 当样本就是因为会把管理员鉴权一起打掉）。

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"testing"

	"loomproxy/db"
)

func totalOf(t *testing.T, env apiEnvelope) float64 {
	t.Helper()
	v, ok := env.dataMap(t)["total"]
	if !ok {
		t.Fatalf("响应里没有 total 字段（data=%s）", string(env.Data))
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("total 不是数字而是 %#v", v)
	}
	return f
}

func TestListTotalThirdStateWhenCountFails(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	// 1) 基线：表在而没行 → total 必须是 0（不是 -1）。
	//    这一条是给"-1 会不会顺手把空列表也报成不可用"准备的对照。
	status, env := doJSON(t, srv, http.MethodGet, "/admin/usage-logs?page=1", nil, admin)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("基线就该 200/code=0，实际 status=%d code=%d msg=%s", status, env.Code, env.Msg)
	}
	if got := totalOf(t, env); got != 0 {
		t.Fatalf("基线 total 应为 0，得到 %v（空列表被报成不可用就是第三态做过头）", got)
	}

	// 2) 失败样本：只动这张流水表，鉴权读的 users / auth_sessions 一字不碰
	if err := db.DB.Exec("DROP TABLE quota_usage_logs").Error; err != nil {
		t.Fatalf("DROP 失败样本没造出来: %v", err)
	}

	status, env = doJSON(t, srv, http.MethodGet, "/admin/usage-logs?page=1", nil, admin)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("列表读失败应当仍是 200（行可能已取到），实际 status=%d code=%d", status, env.Code)
	}
	if got := totalOf(t, env); got != -1 {
		t.Fatalf("读失败时 total 应为 -1（第三态），得到 %v——0 与「没读到」又混回去了", got)
	}
	// 出声那一半也要还在：改响应值不替掉日志
	if !strings.Contains(buf.String(), "admin_usage_total") {
		t.Fatalf("读失败没出声（日志=%s）", buf.String())
	}

	// 3) 本人面同一个形状（两个入口共用同一格语义，不许只修管理端）
	token := registerUser(t, srv, "third_state_user", "third-state@example.invalid", "Passw0rd123")
	status, env = doJSON(t, srv, http.MethodGet, "/quota/usage-logs?page=1", nil, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("本人面读失败也应 200，实际 status=%d code=%d", status, env.Code)
	}
	if got := totalOf(t, env); got != -1 {
		t.Fatalf("本人面 total 应为 -1，得到 %v", got)
	}

	// 4) 反向断言：这次坏法只伤读数。看板与鉴权照常，且看板那格的哨兵不该被牵连
	status, env = doJSON(t, srv, http.MethodGet, "/admin/stats", nil, admin)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("/admin/stats 不该被流水表牵连，实际 status=%d code=%d", status, env.Code)
	}
	if got, ok := env.dataMap(t)["total_users"].(float64); !ok || got < 0 {
		t.Fatalf("total_users 不该变成哨兵，得到 %#v", env.dataMap(t)["total_users"])
	}
}
