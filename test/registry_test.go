// Package test 黑盒测试：从外部包视角测试 base 公开 API。
package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"loomproxy-go/base"
	"loomproxy-go/conf"
)

type fakeHandler struct {
	base.BaseHandler
	tag string
}

func newFake(tag string) func(*base.APIConfig) base.Handler {
	return func(*base.APIConfig) base.Handler {
		h := &fakeHandler{tag: tag}
		h.Name = tag
		h.Path = "/" + tag + "/search"
		return h
	}
}

func (f *fakeHandler) Handle(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	return map[string]interface{}{"tag": f.tag}, nil
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := base.NewRegistry[base.Handler]()

	if err := r.Register("a", newFake("a"), 1, nil); err != nil {
		t.Fatalf("register a failed: %v", err)
	}
	if err := r.Register("a", newFake("a"), 1, nil); err != base.ErrAlreadyRegistered {
		t.Fatalf("duplicate register should fail with ErrAlreadyRegistered, got %v", err)
	}
	if _, err := r.Get("missing"); err != base.ErrHandlerNotFound {
		t.Fatalf("get missing should fail with ErrHandlerNotFound, got %v", err)
	}

	h, err := r.Get("a")
	if err != nil {
		t.Fatalf("get a failed: %v", err)
	}
	if h.GetName() != "a" {
		t.Fatalf("expected handler name a, got %s", h.GetName())
	}

	inst := &fakeHandler{tag: "inst"}
	inst.Name = "inst"
	if err := r.RegisterInstance("inst", base.Handler(inst), 5, nil); err != nil {
		t.Fatalf("register instance failed: %v", err)
	}
	got, err := r.Get("inst")
	if err != nil || got != base.Handler(inst) {
		t.Fatalf("instance get mismatch: %v %v", got, err)
	}
}

func TestRegistryAllSortedAndFilter(t *testing.T) {
	r := base.NewRegistry[base.Handler]()
	r.Register("low", newFake("low"), 1, map[string]interface{}{"type": "x"})
	r.Register("high", newFake("high"), 10, map[string]interface{}{"type": "y"})

	sorted := r.AllSorted()
	if len(sorted) != 2 || sorted[0].GetName() != "high" {
		t.Fatalf("AllSorted should put high priority first, got %v", namesOf(sorted))
	}

	filtered := r.FilterByMetadata("type", "x")
	if len(filtered) != 1 || filtered[0].GetName() != "low" {
		t.Fatalf("FilterByMetadata mismatch, got %v", namesOf(filtered))
	}

	routes := r.Routes()
	if _, ok := routes["/high/search"]; !ok {
		t.Fatalf("Routes should contain /high/search, got %v", routes)
	}
}

func TestRegistryRemoveAndClear(t *testing.T) {
	r := base.NewRegistry[base.Handler]()
	r.Register("a", newFake("a"), 1, nil)

	if !r.Remove("a") {
		t.Fatal("Remove should return true for existing entry")
	}
	if r.Remove("a") {
		t.Fatal("Remove should return false for missing entry")
	}

	r.Register("b", newFake("b"), 1, nil)
	r.Clear()
	if len(r.All()) != 0 {
		t.Fatal("Clear should empty the registry")
	}
}

func TestRegistryConcurrentReadWrite(t *testing.T) {
	r := base.NewRegistry[base.Handler]()
	r.Register("seed", newFake("seed"), 1, nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, err := r.Get("seed"); err != nil {
					t.Errorf("concurrent get failed: %v", err)
					return
				}
				_ = r.All()
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("w%d", i)
			r.Register(name, newFake(name), i, nil)
			r.Remove(name)
		}(i)
	}
	wg.Wait()
}

func namesOf(handlers []base.Handler) []string {
	var names []string
	for _, h := range handlers {
		names = append(names, h.GetName())
	}
	return names
}

func TestRegisterSourceDeclaration(t *testing.T) {
	if err := base.RegisterSource(base.SourceMeta{
		Code: "test_src_a", Display: "测试源A", Category: "test",
		SortOrder: 990, Actions: []string{"search", "detail"},
	}); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	// 同 Code 重复声明报错
	if err := base.RegisterSource(base.SourceMeta{Code: "test_src_a", Actions: []string{"search"}}); err == nil {
		t.Fatal("重复声明应报错")
	}
	// 缺 actions 报错
	if err := base.RegisterSource(base.SourceMeta{Code: "test_src_b"}); err == nil {
		t.Fatal("缺 actions 应报错")
	}
	// Status 缺省补 1（启用）
	if err := base.RegisterSource(base.SourceMeta{Code: "test_src_c", Actions: []string{"search"}}); err != nil {
		t.Fatalf("RegisterSource c: %v", err)
	}
	m, ok := base.GetSourceMeta("test_src_c")
	if !ok || m.Status != 1 {
		t.Fatalf("Status 缺省补 1 失败: %+v ok=%v", m, ok)
	}
	// GetSourceMeta 与 DeclaredSources 可读回
	if m, ok = base.GetSourceMeta("test_src_a"); !ok || m.Display != "测试源A" || len(m.Actions) != 2 {
		t.Fatalf("GetSourceMeta 异常: %+v ok=%v", m, ok)
	}
	found := false
	for _, s := range base.DeclaredSources() {
		if s.Code == "test_src_a" {
			found = true
		}
	}
	if !found {
		t.Fatal("DeclaredSources 缺 test_src_a")
	}
}

// TestDatasourcesRendersDeclaredSearchTabs /datasources 的搜索分类取自各源声明：
// 声明了自有形态的源下发其声明，未声明的回落底座通用分类——底座不按分类名做分支。
func TestDatasourcesRendersDeclaredSearchTabs(t *testing.T) {
	srv := newTestServer(t)

	status, env := doJSON(t, srv, http.MethodGet, "/datasources", nil, nil)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /datasources 失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}

	var items []struct {
		ID        string           `json:"id"`
		SearchTab []base.SearchTab `json:"search_tab"`
	}
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("解析数据源列表失败: %v", err)
	}
	tabs := map[string][]base.SearchTab{}
	for _, it := range items {
		tabs[it.ID] = it.SearchTab
	}

	declared, ok := tabs[fakeC]
	if !ok {
		t.Fatalf("列表缺 %s（items=%v）", fakeC, items)
	}
	if len(declared) != 2 || declared[0].TabType != 1 || declared[1].Name != "假分类二" {
		t.Fatalf("%s 未下发声明的搜索分类: %+v", fakeC, declared)
	}
	if declared[0].BdID != "fa" {
		t.Fatalf("bd_id 未按声明下发: %+v", declared[0])
	}

	fallback, ok := tabs[fakeA]
	if !ok {
		t.Fatalf("列表缺 %s", fakeA)
	}
	if len(fallback) != 1 || fallback[0].TabType != 3 {
		t.Fatalf("%s 未回落底座通用分类: %+v", fakeA, fallback)
	}
}

// TestDescribeDataFileFromDeclaration /data 的字典文件说明取自源声明，
// 底座不内置任何具体数据源的文件名。
func TestDescribeDataFileFromDeclaration(t *testing.T) {
	srv := newTestServer(t)

	if got := base.DescribeDataFile("fake_dict"); got != "假源字典" {
		t.Fatalf("DescribeDataFile 取自声明失败: %q", got)
	}
	if got := base.DescribeDataFile("undeclared_dict"); got != "" {
		t.Fatalf("未声明的文件应返回空串, got %q", got)
	}

	dir := filepath.Join(conf.Config.DataDir, "fake")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建数据字典目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fake_dict.json"), []byte(`{"k":"v"}`), 0o644); err != nil {
		t.Fatalf("写字典文件失败: %v", err)
	}

	// /data 系列端点直接返回对象本身（不带 code/msg/data 信封）
	status, raw := doRaw(t, srv, http.MethodGet, "/data/fake", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /data/fake status=%d body=%s", status, truncate(string(raw), 200))
	}
	var list struct {
		Source string `json:"source"`
		Count  int    `json:"count"`
		Files  []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("解析字典列表失败: %v（body=%s）", err, truncate(string(raw), 300))
	}
	if len(list.Files) != 1 || list.Files[0].Name != "fake_dict" {
		t.Fatalf("字典列表异常: %+v", list)
	}
	if list.Files[0].Description != "假源字典" {
		t.Fatalf("说明未按声明下发: %+v", list.Files[0])
	}
}
