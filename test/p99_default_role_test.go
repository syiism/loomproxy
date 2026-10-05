package test

// 待办清单 P99（第三十五遍巡检）：注册时的默认角色回落是**静默**的，而它旁边那段默认套餐的回落
// 在 P48/P53 那轮就补了出声——同一个形状在一个函数里写了两遍，修好一遍，另一遍没人知道。
//
// 静默回落的代价不是"这个用户没角色"那么轻：改前那段把查不到的 `defaultRole`（零值，ID=0）
// 照样塞进 `Roles: []models.Role{defaultRole}`，而那是 `many2many:user_roles` 关联——
// GORM 见到零值主键的关联对象会去**插一行新角色**（`name`/`code` 都是空串，
// 而这两列是 `uniqueIndex` + NOT NULL，空串合法）。于是：
//   第一次撞上去 → 库里多出一个谁也对不上的幽灵角色，而注册成功、没人喊；
//   第二次撞上去 → 唯一索引把**注册本身**打成 500（对用户是"注册失败，请稍后再试"，真因在日志里）。
// 现在回落目标也不存在时给**空角色列表**（失败关闭：按 code 的判定一律查不到，与 `planID=0` 同理）并出声。

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/models"
)

func registeredRoleCodes(t *testing.T, username string) []string {
	t.Helper()
	var codes []string
	err := db.DB.Model(&models.User{}).
		Joins("JOIN user_roles ON user_roles.user_id = users.id").
		Joins("JOIN roles ON roles.id = user_roles.role_id").
		Where("users.username = ?", username).
		Pluck("roles.code", &codes).Error
	if err != nil {
		t.Fatalf("查 %s 的角色失败: %v", username, err)
	}
	return codes
}

func roleRowCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&models.Role{}).Count(&n).Error; err != nil {
		t.Fatalf("数 roles 失败: %v", err)
	}
	return n
}

func TestRegisterDefaultRoleFallbackSpeaks(t *testing.T) {
	srv := newTestServer(t)

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })
	spoken := func() int { return strings.Count(buf.String(), "设置 default_role 填的值没生效") }

	// 1) 默认值就是 user：注册结果与改前逐字相同（回归护栏，不是新行为），且不该出声
	writeSettingValue(t, "default_role", "user")
	before := spoken()
	registerUser(t, srv, "p99_user", "p99_user@example.com", "pass1234")
	codes := registeredRoleCodes(t, "p99_user")
	if len(codes) != 1 || codes[0] != "user" {
		t.Fatalf("默认配置下注册的角色 = %v, want [user]", codes)
	}
	if n := spoken() - before; n != 0 {
		t.Errorf("合法值不该出声，实得 %d 条", n)
	}

	// 2) 配置真被读：改成 vip 新人就是 vip（这条改前也成立，作用是防止后面的改动把读取顺序写坏）
	writeSettingValue(t, "default_role", "vip")
	registerUser(t, srv, "p99_vip", "p99_vip@example.com", "pass1234")
	if codes := registeredRoleCodes(t, "p99_vip"); len(codes) != 1 || codes[0] != "vip" {
		t.Fatalf("default_role=vip 时注册的角色 = %v, want [vip]", codes)
	}

	// 3) 填了一个不存在的角色码：回落到 user，并且说一声（改前回落成立但**静默**，所以牙齿在这半句）
	writeSettingValue(t, "default_role", "user-copy")
	before = spoken()
	registerUser(t, srv, "p99_typo", "p99_typo@example.com", "pass1234")
	if codes := registeredRoleCodes(t, "p99_typo"); len(codes) != 1 || codes[0] != "user" {
		t.Fatalf("非法角色码回落后的角色 = %v, want [user]", codes)
	}
	if n := spoken() - before; n != 1 {
		t.Errorf("非法角色码的替换出声 %d 条, want 1（改前这里 0 条：回落发生了但没人知道）", n)
	}
	if line := buf.String(); !strings.Contains(line, `"user-copy"`) || !strings.Contains(line, "角色码不存在") {
		t.Errorf("出声内容没把话说清（要含填的值与原因）：\n%s", line)
	}

	// 4) **这条是变异牙齿**：连回落目标 `user` 也不在了。
	//    改前把零值角色塞进 many2many → 插出一个 code='' 的幽灵角色（roles 行数 +1），
	//    而第二次注册直接被唯一索引打成 500。现在给空角色列表 + 出声。
	if err := db.DB.Where("code = ?", "user").Delete(&models.Role{}).Error; err != nil {
		t.Fatalf("删掉 user 角色失败: %v", err)
	}
	writeSettingValue(t, "default_role", "user-copy")
	rolesBefore := roleRowCount(t)
	before = spoken()
	registerUser(t, srv, "p99_norole", "p99_norole@example.com", "pass1234")
	if got := roleRowCount(t); got != rolesBefore {
		t.Errorf("roles 表行数从 %d 变成 %d——注册不该造出一个新角色行（改前的形状就是它）", rolesBefore, got)
	}
	if codes := registeredRoleCodes(t, "p99_norole"); len(codes) != 0 {
		t.Errorf("回落目标也不存在时该给空角色，实得 %v", codes)
	}
	if n := spoken() - before; n != 1 {
		t.Errorf("「回落目标也没了」的替换出声 %d 条, want 1（静默把人注册成没有角色是最坏形态）", n)
	}
	if line := buf.String(); !strings.Contains(line, "回落目标 user 也不存在") {
		t.Errorf("出声里没写清是回落目标也没了：\n%s", line)
	}

	// 5) 第二次撞上同一个形状：注册**仍然成功**（改前在这里会因为唯一索引 500），且不重复喊
	before = spoken()
	registerUser(t, srv, "p99_norole2", "p99_norole2@example.com", "pass1234")
	if codes := registeredRoleCodes(t, "p99_norole2"); len(codes) != 0 {
		t.Errorf("第二次注册的角色 = %v, want 空", codes)
	}
	if n := spoken() - before; n != 0 {
		t.Errorf("同一种替换重复出声 %d 条, want 0", n)
	}
}
