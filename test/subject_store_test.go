package test

// 命名缓存的持久化后端：进程内那张表一重启就空，之后只带标识的正文/目录调用只能记到标识、
// 名称留空（排行榜与监控里的空值几乎都来自这里）。接上后端后，重启不再丢，
// 且历史空行还能靠回填端点补回来。

import (
	"testing"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/models"
)

type fakeSubjectStore struct {
	books    map[string][2]string // source|ident -> {name, media}
	chapters map[string]string    // source|ident -> title
}

func newFakeSubjectStore() *fakeSubjectStore {
	return &fakeSubjectStore{books: map[string][2]string{}, chapters: map[string]string{}}
}

func (f *fakeSubjectStore) key(source, ident string) string { return source + "|" + ident }

func (f *fakeSubjectStore) SaveBook(source, ident, name, media string) {
	f.books[f.key(source, ident)] = [2]string{name, media}
}
func (f *fakeSubjectStore) SaveChapter(source, bookIdent, ident, title string) {
	f.chapters[f.key(source, bookIdent+"|"+ident)] = title
}
func (f *fakeSubjectStore) LoadBook(source, ident string) (string, string, bool) {
	v, ok := f.books[f.key(source, ident)]
	if !ok {
		return "", "", false
	}
	return v[0], v[1], true
}
func (f *fakeSubjectStore) LoadChapter(source, bookIdent, ident string) (string, bool) {
	v, ok := f.chapters[f.key(source, bookIdent+"|"+ident)]
	return v, ok
}

func TestSubjectStoreRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	base.ResetNameCaches()
	defer base.SetSubjectStore(nil)
	admin := adminToken(t, srv)

	st := newFakeSubjectStore()
	base.SetSubjectStore(st)
	if !base.SubjectStoreLoaded() {
		t.Fatal("注入后端后 SubjectStoreLoaded 应为 true")
	}

	// 1) 详情响应带出的「标识 → 名称」要同时写给后端（内存缓存之外）
	setPlatformUpstream(t, "fake_b", staticUpstream(t, detailEnvelope).URL)
	mustOK(t, srv, admin, "/fake_b/detail")
	if got := st.books["fake_b|bk9"]; got[0] != "某剧" {
		t.Fatalf("后端未收到书名映射，实得 %+v", st.books)
	}

	// 2) 模拟重启：内存缓存清空，只剩后端。此后回填端点应能凭标识补出书名
	base.ResetNameCaches()
	row := models.ApiCallLog{
		Username: "su", IP: "127.0.0.1", Source: "fake_b", Action: "content",
		Status: 200, LatencyMs: 9, BookIdent: "bk9", BookName: "",
		Media: base.MediaVideo, ResultCount: 1,
	}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("写入待回填明细失败: %v", err)
	}
	_, env := doJSON(t, srv, "POST", "/admin/monitor/backfill-subjects?days=7", nil, authHeader(admin))
	if env.Code != 0 {
		t.Fatalf("回填失败: msg=%s", env.Msg)
	}
	if got := env.dataMap(t)["book_filled"]; got != float64(1) {
		t.Fatalf("应凭后端映射补出 1 处书名，实得 %v", got)
	}
	var after models.ApiCallLog
	if err := db.DB.First(&after, row.ID).Error; err != nil || after.BookName != "某剧" {
		t.Fatalf("书名未回填成功: %+v err=%v", after, err)
	}

	// 3) 后端里没有的标识不该被猜出来
	unknown := models.ApiCallLog{
		Username: "su", IP: "127.0.0.1", Source: "fake_b", Action: "content",
		Status: 200, LatencyMs: 9, BookIdent: "bk-不存在", Media: base.MediaVideo, ResultCount: 1,
	}
	if err := db.DB.Create(&unknown).Error; err != nil {
		t.Fatalf("写入无映射明细失败: %v", err)
	}
	if name, _ := base.LookupBook("fake_b", "bk-不存在"); name != "" {
		t.Errorf("无映射的标识被反查成了 %q，应保持为空", name)
	}

	// 4) 摘掉后端（未启用 Redis 的部署）不应影响既有行为
	base.SetSubjectStore(nil)
	if base.SubjectStoreLoaded() {
		t.Error("摘掉后端后 SubjectStoreLoaded 应为 false")
	}
}
