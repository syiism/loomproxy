package test

// 书源下发：不再读管理员手填的直链，改为静态托管 /data/shuyuan/bookSource.json。
// 这组用例钉住三件事：文件没就位时 ready=false（面板因此不给按钮）、
// 半截 JSON 不算就位（App 导入会失败，宁可不给）、旧设置键在升级时被清掉。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/models"
)

func writeBookSource(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(conf.Config.DataDir, "shuyuan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建书源托管目录失败: %v", err)
	}
	path := filepath.Join(dir, "bookSource.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写书源文件失败: %v", err)
	}
	return path
}

func importConfig(t *testing.T, srv *httptest.Server, token string) map[string]interface{} {
	t.Helper()
	status, env := doJSON(t, srv, "GET", "/user/import-config", nil, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /user/import-config 失败（status=%d msg=%s）", status, env.Msg)
	}
	return env.dataMap(t)
}

func TestBookSourceHostedDelivery(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "bs_u1", "bs_u1@example.com", "pass1234")

	// 1) 未托管：只报未就位，不报错
	data := importConfig(t, srv, token)
	if data["book_source_path"] != "/data/shuyuan/bookSource.json" {
		t.Errorf("下发的路径 = %v, want /data/shuyuan/bookSource.json", data["book_source_path"])
	}
	if data["ready"] != false {
		t.Errorf("文件不存在时 ready = %v, want false", data["ready"])
	}
	if _, leaked := data["legado_import_url"]; leaked {
		t.Error("响应里不该再有 legado_import_url（书源直链已由托管位置取代）")
	}

	// 2) 半截 JSON 不算就位
	writeBookSource(t, `[{"bookSourceName":"写到一半`)
	if importConfig(t, srv, token)["ready"] != false {
		t.Error("非法 JSON 不应判成就位——面板给了按钮，App 那边只会导入失败")
	}

	// 3) 合法文件：ready=true，且 /data 直出原文（免鉴权，阅读 App 才能直接拉）
	content := `[{"bookSourceName":"测试书源","bookSourceUrl":"https://example.invalid/fq_novel/search"}]`
	path := writeBookSource(t, content)
	if importConfig(t, srv, token)["ready"] != true {
		t.Fatalf("合法文件应判就位")
	}
	status, raw := doRaw(t, srv, "GET", "/data/shuyuan/bookSource.json", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("取书源文件 status = %d, want 200", status)
	}
	if !strings.Contains(string(raw), "测试书源") {
		t.Errorf("书源文件内容没直出，实得 %s", truncate(string(raw), 200))
	}

	// 4) 撤下文件要立刻回到未就位（不能残留 true）
	if err := os.Remove(path); err != nil {
		t.Fatalf("删书源文件失败: %v", err)
	}
	if importConfig(t, srv, token)["ready"] != false {
		t.Error("撤下文件后 ready 仍为 true")
	}
}

func TestSeedRemovesLegacyImportURLSetting(t *testing.T) {
	srv := newTestServer(t)
	_ = srv

	// 模拟升级前的库：那一行还在，且带着旧值
	if err := db.DB.Create(&models.SystemSetting{
		Key: "legado_import_url", Value: "https://old.example.invalid/book.json", Type: "string",
	}).Error; err != nil {
		t.Fatalf("造旧键失败: %v", err)
	}
	if err := db.Seed(db.DB); err != nil {
		t.Fatalf("Seed 失败: %v", err)
	}

	var n int64
	db.DB.Model(&models.SystemSetting{}).
		Where(map[string]interface{}{"key": "legado_import_url"}).Count(&n)
	if n != 0 {
		t.Errorf("废弃键 legado_import_url 未被清掉（剩 %d 行）——留着它会赖在面板「自定义配置」卡里，看起来像个还能生效的开关", n)
	}
}
