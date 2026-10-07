package testkit

// 组 A 与组 B **两边都要用**的那批夹具（待办清单 P104 正式拆分的第二批下沉）：
// 14 条函数 + 2 条结构体 + 2 条常量，全部按"被对面那一组用到、而实现长在另一组的文件里"数出来。
//
// 两批的分工：`helpers.go` 是「B 用到、定义在 A」那 12 条，这一页是反向的这批，
// 合计 30 条——正是条目里当初数出来的那个数。**跨组带方法的类型一条都不下沉**：
// `registry_test.go` 的 `fakeHandler`（类型 + 方法 + 构造器三件套）就是 §13 那次撤回撞过的形状，
// 这次不再逐符号搬——把那 0.5s+1.5s 的两个文件留在 A，切组让位于"类型不拆"这条判据。
// 裁判是编译器：任何漏掉的一条都会表现为某一边 undefined。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/handlers/subjectrank"
	"loomproxy/models"
)

type DashSource struct {
	SourceCode     string `json:"source_code"`
	GroupName      string `json:"group_name"`
	EffectiveTotal int64  `json:"effective_total"`
}

type DashResp struct {
	Sources []DashSource `json:"sources"`
	Groups  []struct {
		ID    uint   `json:"id"`
		Name  string `json:"name"`
		Count int64  `json:"count"`
	} `json:"groups"`
	UngroupedCount int64  `json:"ungrouped_count"`
	PlanName       string `json:"plan_name"`
}

const SearchEnvelope = `{"bookList":[{"bookId":"bk1","name":"测试书"},{"bookId":"bk2","name":"测试书 二"}]}`

const DetailEnvelope = `{"bookInfo":{"bookId":"bk9","name":"某剧","bookType":"漫剧"}}`

func StaticUpstream(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, payload)
	}))
	t.Cleanup(up.Close)
	return up
}

func SetPlatformUpstream(t *testing.T, source, upstreamURL string) {
	t.Helper()
	if err := db.DB.Create(&models.PlatformSourceConfig{
		SourceName: source, BaseURL: upstreamURL,
	}).Error; err != nil {
		t.Fatalf("写入 %s 的平台默认 baseUrl 失败: %v", source, err)
	}
}

func InsertSubjectCall(t *testing.T, source, action, keyword, book, media string, resultCount, status int) {
	t.Helper()
	if err := db.DB.Create(&models.ApiCallLog{
		Username: "tester", IP: "1.2.3.4", Source: source, Action: action,
		Status: status, LatencyMs: 10, CreatedAt: time.Now(),
		Keyword: keyword, BookName: book, Media: media, ResultCount: resultCount,
	}).Error; err != nil {
		t.Fatalf("插入内容维度明细失败: %v", err)
	}
}

func MustOK(t *testing.T, srv *httptest.Server, token, path string) {
	t.Helper()
	status, body := DoRaw(t, srv, "GET", path, nil, AuthHeader(token))
	if status != http.StatusOK {
		t.Fatalf("%s 期望 200，实为 %d：%s", path, status, string(body))
	}
}

func CoverageRow(t *testing.T, rows []subjectrank.CoverageRow, source, action string) subjectrank.CoverageRow {
	t.Helper()
	for _, r := range rows {
		if r.Source == source && r.Action == action {
			return r
		}
	}
	t.Fatalf("覆盖率里没有 %s/%s：%+v", source, action, rows)
	return subjectrank.CoverageRow{}
}

func InsertIdentRow(t *testing.T, source, bookIdent, bookName, chapterIdent, chapterTitle string) uint {
	t.Helper()
	row := models.ApiCallLog{
		Username: "bf_u", IP: "127.0.0.1", Source: source, Action: "content",
		Status: http.StatusOK, LatencyMs: 12, CreatedAt: time.Now(),
		BookName: bookName, ChapterTitle: chapterTitle,
		BookIdent: bookIdent, ChapterIdent: chapterIdent,
		Media: base.MediaNovel, ResultCount: 1,
	}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("写入带标识的明细失败: %v", err)
	}
	return row.ID
}

func SetVerifySetting(t *testing.T, key, value string) {
	t.Helper()
	if err := db.DB.Model(&models.SystemSetting{}).Where(map[string]interface{}{"key": key}).
		Update("value", value).Error; err != nil {
		t.Fatalf("写设置 %s 失败: %v", key, err)
	}
	db.InvalidateSettingCache(key)
}

func EnableVerifyScene(t *testing.T, scene string) {
	t.Helper()
	// 条件用 map 形式让 GORM 按方言引号 `key`：写死反引号的 SQL 在 PostgreSQL 上是语法错误
	if err := db.DB.Model(&models.SystemSetting{}).Where(map[string]interface{}{"key": "verify_code_scenes"}).
		Update("value", scene).Error; err != nil {
		t.Fatalf("启用场景 %s 失败: %v", scene, err)
	}
	db.InvalidateSettingCache("verify_code_scenes")
}

func SendCode(t *testing.T, srv *httptest.Server, scene, target string) (int, Envelope) {
	t.Helper()
	return DoJSON(t, srv, http.MethodPost, "/verify/send",
		map[string]interface{}{"scene": scene, "target": target}, nil)
}

func LoginFromIP(t *testing.T, srv *httptest.Server, username, password, ip string) (int, Envelope) {
	t.Helper()
	return DoJSON(t, srv, http.MethodPost, "/auth/login", map[string]interface{}{
		"username": username,
		"password": password,
	}, map[string]string{"X-Forwarded-For": ip})
}

func GrantRow(t *testing.T, planID uint) models.QuotaLimit {
	t.Helper()
	var row models.QuotaLimit
	err := db.DB.Where("plan_id = ? AND scope = ? AND target = ?", planID, "source", "fake_b").First(&row).Error
	if err != nil {
		t.Fatalf("查 fake_b 的限额行失败（plan=%d）: %v", planID, err)
	}
	return row
}

func Dashboard(t *testing.T, srv *httptest.Server, token string) DashResp {
	t.Helper()
	status, env := DoJSON(t, srv, http.MethodGet, "/quota/dashboard", nil, AuthHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /quota/dashboard 失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	var out DashResp
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("解析 dashboard 响应失败: %v", err)
	}
	return out
}

func SettingValue(t *testing.T, key string) (value, typ string) {
	t.Helper()
	var s models.SystemSetting
	if err := db.DB.Where(map[string]interface{}{"key": key}).First(&s).Error; err != nil {
		t.Fatalf("读取设置项 %s 失败: %v", key, err)
	}
	return s.Value, s.Type
}
