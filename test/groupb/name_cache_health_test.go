package groupb

// 命名缓存写侧体检（待办清单 P2）：丢弃与写失败不能只活在关停日志里，面板要读得到。
// 这里不接真 Redis——base.SetSubjectStore 注入一个带体检面的假后端即可，
// 验的是「端点把这三个数原样报出去」与「纯内存时 persist 缺席」两件事。

import (
	"encoding/json"
	"net/http"
	"testing"

	"loomproxy/base"
)

// healthStore 只提供契约要求的四个方法 + PersistHealth，数字是喂给断言的已知值
type healthStore struct{ queued, dropped, failed int64 }

func (healthStore) SaveBook(source, ident, name, media string)                 {}
func (healthStore) SaveChapter(source, bookIdent, ident, title string)         {}
func (healthStore) LoadBook(source, ident string) (string, string, bool)       { return "", "", false }
func (healthStore) LoadChapter(source, bookIdent, ident string) (string, bool) { return "", false }
func (h healthStore) PersistHealth() (int64, int64, int64)                     { return h.queued, h.dropped, h.failed }

type nameCacheResp struct {
	NameCache struct {
		Books    int64 `json:"books"`
		Chapters int64 `json:"chapters"`
		Persist  *struct {
			Queued  int64 `json:"queued"`
			Dropped int64 `json:"dropped"`
			Failed  int64 `json:"failed"`
		} `json:"persist"`
	} `json:"name_cache"`
}

func TestMonitorSubjectsNameCachePersistHealth(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	get := func() nameCacheResp {
		status, env := doJSON(t, srv, http.MethodGet, "/admin/monitor/subjects?dim=book", nil, authHeader(admin))
		if status != http.StatusOK {
			t.Fatalf("读 /admin/monitor/subjects status=%d，want 200", status)
		}
		var got nameCacheResp
		if err := json.Unmarshal(env.Data, &got); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		return got
	}

	// 纯内存：没有持久化可丢，persist 必须缺席——报 0 会被读成「接了 Redis 且一条没丢」
	base.SetSubjectStore(nil)
	if got := get(); got.NameCache.Persist != nil {
		t.Errorf("未注入持久化后端时不应带 persist，实得 %+v", got.NameCache.Persist)
	}

	base.SetSubjectStore(healthStore{queued: 12, dropped: 3456, failed: 128})
	t.Cleanup(func() { base.SetSubjectStore(nil) })
	got := get()
	p := got.NameCache.Persist
	if p == nil {
		t.Fatalf("注入带体检面的后端后应报出 persist，实得响应 %+v", got.NameCache)
	}
	if p.Queued != 12 || p.Dropped != 3456 || p.Failed != 128 {
		t.Errorf("persist 原样透传失效：queued=%d dropped=%d failed=%d，want 12/3456/128",
			p.Queued, p.Dropped, p.Failed)
	}
}
