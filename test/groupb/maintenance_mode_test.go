package groupb

// 维护模式（待办清单 P53② 接线）：开着时数据面路由一律 503、管理员放行、匿名与非管理员被拦。
// 设置走 db.SettingBool（10 秒缓存），关掉时热路径零成本。

import (
	"net/http"
	"testing"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/models"
)

func setMaintenance(t *testing.T, on bool) {
	t.Helper()
	v := "false"
	if on {
		v = "true"
	}
	if err := db.DB.Model(&models.SystemSetting{}).
		Where(map[string]interface{}{"key": "maintenance_mode"}).
		Update("value", v).Error; err != nil {
		t.Fatalf("设置 maintenance_mode 失败: %v", err)
	}
	db.InvalidateSettingCache("maintenance_mode")
}

func TestMaintenanceGate(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	// 注册响应自带 token（register 即登录）
	st, envReg := doJSON(t, srv, "POST", "/auth/register", map[string]interface{}{
		"username": "maintuser", "email": "maint@test.local", "password": "maint1234",
	}, nil)
	if st != http.StatusOK {
		t.Fatalf("注册失败：%d %s", st, envReg.Msg)
	}
	userTok, _ := envReg.dataMap(t)["token"].(string)
	userHdr := authHeader(userTok)

	// 挂假上游，先造「维护关闭时正常」的基线
	setPlatformUpstream(t, "fake_a", staticUpstream(t, `{"bookList":[{"bookId":"a1","name":"甲书"}]}`).URL)
	if st, _ := doJSON(t, srv, "GET", "/fake_a/search?query=维护测试", nil, admin); st != http.StatusOK {
		t.Fatalf("维护关闭时 admin 打 fake_a = %d, want 200", st)
	}
	if st, _ := doJSON(t, srv, "GET", "/fake_a/search?query=维护测试", nil, userHdr); st != http.StatusOK {
		t.Fatalf("维护关闭时普通用户 = %d, want 200", st)
	}

	setMaintenance(t, true)
	t.Cleanup(func() { setMaintenance(t, false) })

	// 开着：管理员放行
	if st, _ := doJSON(t, srv, "GET", "/fake_a/search?query=维护测试", nil, admin); st != http.StatusOK {
		t.Errorf("维护开启 + 管理员 = %d, want 200（管理员放行）", st)
	}
	// 开着：普通用户与匿名被拦
	if st, _ := doJSON(t, srv, "GET", "/fake_a/search?query=维护测试", nil, userHdr); st != http.StatusServiceUnavailable {
		t.Errorf("维护开启 + 普通用户 = %d, want 503", st)
	}
	// 匿名在 apiauth 层（Order 100）就被 401 拦下，到不了维护门——这是分层事实，不是回归；
	// 维护门拦匿名只在 AUTH_ENABLED=false 时可见（那种配置下 apiauth 直通）
	if st, _ := doJSON(t, srv, "GET", "/fake_a/search?query=维护测试", nil, nil); st != http.StatusUnauthorized {
		t.Errorf("维护开启 + 匿名 = %d, want 401（apiauth 层先拦，分层事实）", st)
	}

	// 被拦下的请求也必须**完整走完观测链**：没进 handler 就没有 CallSubject，
	// monitor 递给 RecordCall 的是 nil subject（与 access/billing 拦下的 403/429 同一条路）。
	// 这一条断言存在的理由：本轮有人把 `subject.InBandReason` 读在 nil 保护之外，
	// 于是每个被拦的请求都在 recovery 里炸一次；而 recovery 修成"响应已写出就不再补一份信封"之后，
	// 状态码与正文全都正常——**只有这里能看出链断了**。
	var observed bool
	for _, rc := range base.RecentCalls(30) {
		if rc.Source == "fake_a" && rc.Action == "search" && rc.Status == http.StatusServiceUnavailable {
			observed = true
		}
	}
	if !observed {
		t.Error("维护门的 503 没进监控读数（观测链在 panic 里断了，不是「没配监控」）")
	}
}
