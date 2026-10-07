package test

// 源声明的自有设置项（`SourceMeta.Settings`）在骨架侧的落库与登记。
//
// 这是「源要一个可热改、面板能编辑、又可能含令牌的配置」的通用声明位：骨架不认识任何具体源键，
// 但声明之后统一办三件事——① 播种成 system_settings 行（行不存在才建，已存在只对账 type）；
// ② Sensitive 的键登记进 db.SensitiveSettingKeys（读接口回空串、写侧留空=保持原值）；
// ③ 行随 GET /admin/settings 下发、面板按 sensitive_keys 渲染密钥控件。
//
// 夹具 fake_b 声明了两个键（普通型 SetKeyMode + 密钥型 SetKeyToken），一次覆盖两条路径——
// 骨架树里不存在任何真实源键，这两个键只存在于夹具里。

import (
	"net/http"
	"testing"

	"loomproxy/db"
	"loomproxy/models"
	"loomproxy/testkit"
	"loomproxy/testkit/fakesource"
)

func TestSourceDeclaredSettingsSeededAndRegistered(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	// ① 普通型声明项：Default 初值落库
	if v := testkit.DBGetSetting(t, fakesource.SetKeyMode); v != "fast" {
		t.Fatalf("声明初值未落库：%s = %q（期望 fast）", fakesource.SetKeyMode, v)
	}

	// ② type 也按声明落库（面板写入端按它做形态校验，漂了就等于「能声明不能保存」）
	var row models.SystemSetting
	if err := db.DB.Where(map[string]interface{}{"key": fakesource.SetKeyMode}).First(&row).Error; err != nil {
		t.Fatalf("读声明设置行失败: %v", err)
	}
	if row.Type != "string" {
		t.Errorf("声明 type 未落库：%s.Type = %q（期望 string）", fakesource.SetKeyMode, row.Type)
	}

	// ③ 敏感键登记进 SensitiveSettingKeys（读接口掩码与写侧「留空=保持原值」的判定都读它）
	if !db.SensitiveSettingKeys[fakesource.SetKeyToken] {
		t.Fatalf("敏感键 %s 未被登记进 SensitiveSettingKeys", fakesource.SetKeyToken)
	}

	// ④ 造一个真实令牌值，验读接口不回显真值、且名单随响应下发
	testkit.WriteSettingValue(t, fakesource.SetKeyToken, "REAL-FAKE-TOKEN")
	status, env := doJSON(t, srv, http.MethodGet, "/admin/settings", nil, admin)
	if status != http.StatusOK {
		t.Fatalf("GET /admin/settings = %d", status)
	}
	data := env.dataMap(t)
	items, _ := data["items"].([]interface{})
	var gotMode, gotToken interface{}
	for _, it := range items {
		m, _ := it.(map[string]interface{})
		switch m["key"] {
		case fakesource.SetKeyMode:
			gotMode = m["value"]
		case fakesource.SetKeyToken:
			gotToken = m["value"]
		}
	}
	if gotMode != "fast" {
		t.Errorf("普通声明项应回显真值，实际 = %v", gotMode)
	}
	if gotToken != "" {
		t.Errorf("敏感声明项被回显：%v（真令牌不得出读接口）", gotToken)
	}
	sk, _ := data["sensitive_keys"].([]interface{})
	found := false
	for _, k := range sk {
		if k == fakesource.SetKeyToken {
			found = true
		}
	}
	if !found {
		t.Errorf("sensitive_keys 缺 %s：%v", fakesource.SetKeyToken, sk)
	}
}
