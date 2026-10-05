package test

// `/announcement` 上那个 `maintenance` 字段是维护横幅的**只读信号**（待办清单 P95）。
// 挂在这个端点上而不是新开一条，是因为前端本来就每次进入拉它一次；而它必须**独立于公告内容**成立：
// 公告为空是常态（seed 里就是空串），如果信号跟着公告一起被"空就不返回"的逻辑吞掉，横幅永远不会出现。
// 所以下面三档都刻意在**公告为空**的前提下测。

import (
	"net/http"
	"testing"
)

func TestAnnouncementCarriesMaintenanceFlag(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	// 前提：这条端点在当前实现下要凭证（不在默认白名单里），横幅只在登录后可见——这是现状，不是缺陷
	get := func() map[string]interface{} {
		status, env := doJSON(t, srv, http.MethodGet, "/announcement", nil, admin)
		if status != http.StatusOK || env.Code != 0 {
			t.Fatalf("GET /announcement = %d code=%d msg=%s", status, env.Code, env.Msg)
		}
		data := env.dataMap(t)
		if c, _ := data["content"].(string); c != "" {
			t.Fatalf("前提不成立：公告应为空串才有断言意义，实际 = %q", c)
		}
		return data
	}

	if v, ok := get()["maintenance"]; !ok {
		t.Fatal("响应里没有 maintenance 字段——横幅的信号源没了，前端会静默不显示（构建与用例都不报）")
	} else if v != false {
		t.Errorf("默认 maintenance = %v, want false（seed 里 maintenance_mode=false）", v)
	}

	setMaintenance(t, true)
	t.Cleanup(func() { setMaintenance(t, false) })
	if v := get()["maintenance"]; v != true {
		t.Errorf("拨开维护后 maintenance = %v, want true——注意设置读口有 10 秒缓存，测试里靠失效绕开", v)
	}

	setMaintenance(t, false)
	if v := get()["maintenance"]; v != false {
		t.Errorf("关回维护后 maintenance = %v, want false", v)
	}
}
