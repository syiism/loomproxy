package test

// 设置项的值按声明的 type 校验，bool 判据收成一处（待办清单 P60）。
//
// 修前的形状：`type` 被当成"行为声明"（`json` 型确实双侧校验），
// 但 `bool`/`number` 是"存什么就是什么"；而读取侧对 bool 有两套判据——
// `register_enabled` 那一路去空白转小写，`device_watch_enabled` 与 `auto_block_enabled` 那几路是精确 `== "true"`。
// 于是库里存进 `True`（人或脚本敲的）时，**同一个值在两个开关上给出相反结果**，
// 而面板只显示那个值，看不出它被读成了什么。
//
// 校验刻意只管**形态**不管**语义**：`0` 与负数都是合法整数
// （`jwt_expire_hours=-1` 是在用的"永不过期"；限额类填 0 的语义还等拍，见 P48②）——
// 在这里把 0 挡在门外，就正是 P48 那一族自己犯过的错。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/models"
)

func putSettingValue(t *testing.T, srv *httptest.Server, admin map[string]string, key, value string) (int, string) {
	t.Helper()
	st, env := doJSON(t, srv, http.MethodPut, "/admin/settings/"+key,
		map[string]interface{}{"value": value}, admin)
	return st, env.Msg
}

func TestSettingValueValidatedByDeclaredType(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	// 1) bool：合形态的写法收下，并**归一存储**——存进去的永远是 "true"/"false"，
	//    这样"面板显示的值"与"被读出来的语义"不可能再分家
	if st, msg := putSettingValue(t, srv, admin, "device_watch_enabled", "  TRUE  "); st != http.StatusOK {
		t.Fatalf("保存 bool 值 TRUE status = %d（msg=%s）, want 200", st, msg)
	}
	if got, _ := settingValue(t, "device_watch_enabled"); got != "true" {
		t.Errorf("归一后存储值 = %q, want true（不归一就等于把大小写问题推给每一个读取方）", got)
	}

	// 2) bool 拒掉"看着像开"的写法：这些值过去会被静默读成关
	for _, bad := range []string{"1", "yes", "on", "开启"} {
		st, msg := putSettingValue(t, srv, admin, "device_watch_enabled", bad)
		if st != http.StatusBadRequest {
			t.Errorf("bool 存 %q status = %d（msg=%s）, want 400", bad, st, msg)
			continue
		}
		if !strings.Contains(msg, "true") {
			t.Errorf("拒绝时没说清只接受什么（值 %q）：%q", bad, msg)
		}
	}
	// 拒了就不能顺手把库改坏：上面那四次都失败，存储值仍是归一后的 true
	if got, _ := settingValue(t, "device_watch_enabled"); got != "true" {
		t.Errorf("被拒的写入动了库：存储值 = %q, want 仍是 true", got)
	}

	// 3) number 只认整数，但 **0 与负数合法**
	if st, msg := putSettingValue(t, srv, admin, "jwt_expire_hours", "-1"); st != http.StatusOK {
		t.Errorf("保存 -1 status = %d（msg=%s）, want 200（-1 是「永不过期」的在用形态）", st, msg)
	}
	if st, msg := putSettingValue(t, srv, admin, "suspect_distinct_ips", "0"); st != http.StatusOK {
		t.Errorf("保存 0 status = %d（msg=%s）, want 200——校验形态不校验语义，别在这里重犯 P48 那一族", st, msg)
	}
	if st, _ := putSettingValue(t, srv, admin, "max_active_sessions", "五个"); st != http.StatusBadRequest {
		t.Errorf("number 存非整数 status = %d, want 400", st)
	}

	// 4) 读取侧只剩一套判据：库里真出现历史遗留的 "True"（**绕过 API 直接写库**）时，
	//    所有读 bool 设置的地方必须给出同一个答案
	if err := db.DB.Model(&models.SystemSetting{}).
		Where(map[string]interface{}{"key": "device_watch_enabled"}).Update("value", "True").Error; err != nil {
		t.Fatalf("直接写库失败: %v", err)
	}
	db.InvalidateSettingCache("device_watch_enabled")
	if !db.SettingBool("device_watch_enabled", false) {
		t.Errorf(`SettingBool 把 "True" 读成了 false——归一判据没生效`)
	}
	// 设备页那个读数过去是精确 `== "true"`，现在必须跟同一个判据
	_, env := doJSON(t, srv, http.MethodGet, "/admin/devices", nil, admin)
	if !strings.Contains(string(env.Data), `"device_watch_enabled":true`) {
		t.Errorf("设备页读数没跟着统一判据走（库里是 True）：%.200s", string(env.Data))
	}
}
