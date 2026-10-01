package test

// /data 是**直读文件**的通道（待办清单 P16）。这里曾经每次命中都往共享缓存 Set 一份结果、
// 而全仓没有对应的 Get——白占内存不说，还让人以为「/data 有缓存失效问题要处理」。
// 现在删掉了那次写，同时删掉了启动时对 /data 的预热（预热一个没人读的缓存没有意义）。
//
// 这条用例钉的是**可见性**：改完文件必须立刻出现。将来若有人给这条通道加回带 TTL 的缓存，
// 这里会先失败——这是有意的：这条通道承载书源 JSON 托管下发，「更新了却读到旧值」是事故。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"loomproxy/conf"
)

type dataSourcesResp struct {
	Count   int `json:"count"`
	Sources map[string]struct {
		FileCount int `json:"file_count"`
	} `json:"sources"`
}

func TestDataFilesServeFreshOnEveryRequest(t *testing.T) {
	srv := newTestServer(t)
	dir := filepath.Join(conf.Config.DataDir, "zz_p16")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建测试数据目录失败: %v", err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o644); err != nil {
			t.Fatalf("写测试数据文件 %s 失败: %v", name, err)
		}
	}
	overview := func() dataSourcesResp {
		t.Helper()
		// /data 走的是原样直出，不是 {code,msg,data} 信封——所以读原始 body
		status, raw := doRaw(t, srv, http.MethodGet, "/data", nil, nil)
		if status != http.StatusOK {
			t.Fatalf("GET /data status=%d，want 200（body=%s）", status, truncate(string(raw), 200))
		}
		var got dataSourcesResp
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("解析 /data 总览失败: %v（body=%s）", err, truncate(string(raw), 200))
		}
		return got
	}

	if got := overview(); got.Sources["zz_p16"].FileCount != 0 {
		t.Fatalf("空目录不应有文件，实得 %+v", got.Sources["zz_p16"])
	}

	write("alpha", `{"k":1}`)
	if got := overview(); got.Sources["zz_p16"].FileCount != 1 {
		t.Errorf("新建的 alpha.json 应立刻出现在总览里（中间隔一层缓存就会晚一拍），实得 %+v", got.Sources)
	}

	write("beta", `{"k":2}`)
	if got := overview(); got.Sources["zz_p16"].FileCount != 2 {
		t.Errorf("第二个文件也应立刻可见，实得 %+v", got.Sources)
	}

	// 原样直出的正文同样读最新的：路径 /data/<分类>/<文件>
	status, raw := doRaw(t, srv, http.MethodGet, "/data/zz_p16/beta", nil, nil)
	if status != http.StatusOK || !bytes.Contains(raw, []byte(`"k":2`)) {
		t.Errorf("GET /data/zz_p16/beta 应原样直出最新内容，实得 status=%d body=%s", status, truncate(string(raw), 200))
	}
}
