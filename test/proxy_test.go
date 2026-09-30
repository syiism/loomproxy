package test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"loomproxy-go/base"
	"loomproxy-go/conf"
)

// fakeProxy 返回一个伪装成 HTTP 代理的测试服务器：不真正转发，直接返回标记响应
func fakeProxy(hits *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"proxied":true}`)
	}))
}

// 配置了代理池后，上游请求应经由代理发出（目标服务器零触达）
func TestUpstreamProxyRoutesThroughProxy(t *testing.T) {
	setupConf(100, 30)

	var proxyHits, targetHits atomic.Int64
	proxySrv := fakeProxy(&proxyHits)
	defer proxySrv.Close()
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		fmt.Fprint(w, `{"proxied":false}`)
	}))
	defer targetSrv.Close()

	conf.Config.UpstreamProxies = []string{proxySrv.URL}

	h := base.NewBaseHandler()
	m, err := h.FetchJSON(context.Background(), targetSrv.URL, nil)
	if err != nil {
		t.Fatalf("FetchJSON 出错: %v", err)
	}
	if m["proxied"] != true {
		t.Fatalf("响应应来自代理: %v", m)
	}
	if proxyHits.Load() == 0 {
		t.Fatal("代理服务器未被调用")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("目标服务器不应被直接调用，实际 %d 次", targetHits.Load())
	}
}

// 代理故障时应自动换代理/回退直连完成请求
func TestProxyFailureFailover(t *testing.T) {
	setupConf(100, 30)

	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer targetSrv.Close()

	// 池中唯一代理是不可达地址，应回退直连成功
	conf.Config.UpstreamProxies = []string{"http://127.0.0.1:1"}

	h := base.NewBaseHandler()
	for i := 0; i < 3; i++ {
		m, err := h.FetchJSON(context.Background(), targetSrv.URL, nil)
		if err != nil {
			t.Fatalf("第 %d 次请求应回退直连成功: %v", i+1, err)
		}
		if m["ok"] != true {
			t.Fatalf("响应内容不符: %v", m)
		}
	}
}

// UA 轮换开启时自动注入 UA；Handler 显式设置的 UA 优先
func TestUserAgentRotation(t *testing.T) {
	setupConf(100, 30)
	conf.Config.UpstreamUARotate = true
	defer func() { conf.Config.UpstreamUARotate = false }()

	uas := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uas <- r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	h := base.NewBaseHandler()

	// 未显式设置 UA：应注入轮换池中的 UA
	if _, err := h.FetchJSON(context.Background(), srv.URL, nil); err != nil {
		t.Fatalf("FetchJSON 出错: %v", err)
	}
	if ua := <-uas; ua == "" {
		t.Fatal("UA 轮换开启时应注入 User-Agent")
	}

	// 显式设置 UA：应保持不变
	custom := "MyCustomAgent/1.0"
	if _, err := h.FetchJSON(context.Background(), srv.URL, map[string]string{"User-Agent": custom}); err != nil {
		t.Fatalf("FetchJSON 出错: %v", err)
	}
	if ua := <-uas; ua != custom {
		t.Fatalf("显式 UA 不应被覆盖，实际: %s", ua)
	}
}

// 按数据源/接口门控代理：列表项支持整源（fake_c）与单接口（fake_c/chapter）两种粒度，其余直连；空列表不限制
func TestProxyPerSourceGating(t *testing.T) {
	setupConf(100, 30)

	var proxyHits, targetHits atomic.Int64
	proxySrv := fakeProxy(&proxyHits)
	defer proxySrv.Close()
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"proxied":false}`)
	}))
	defer targetSrv.Close()

	conf.Config.UpstreamProxies = []string{proxySrv.URL}

	sources := ""
	base.SetProxySourcesGetter(func() string { return sources })
	defer base.SetProxySourcesGetter(nil)

	fetch := func(path string) map[string]interface{} {
		h := base.NewBaseHandler()
		h.Path = path
		m, err := h.FetchJSON(context.Background(), targetSrv.URL, nil)
		if err != nil {
			t.Fatalf("FetchJSON 出错: %v", err)
		}
		return m
	}

	// 仅启用 fake_c/chapter：fake_c/search 应直连，fake_c/chapter 应走代理
	sources = "fake_c/chapter"
	if m := fetch("/fake_c/search"); m["proxied"] != false {
		t.Fatalf("未启用代理的接口应直连: %v", m)
	}
	if m := fetch("/fake_c/chapter"); m["proxied"] != true {
		t.Fatalf("启用代理的接口应经代理: %v", m)
	}

	// 整源粒度：fake_a 全部接口走代理
	sources = "fake_a,fake_c/chapter"
	if m := fetch("/fake_a/search"); m["proxied"] != true {
		t.Fatalf("启用代理的数据源应经代理: %v", m)
	}

	// 不在列表中的数据源：应直连
	if m := fetch("/fake_c/search"); m["proxied"] != false {
		t.Fatalf("未启用代理的数据源应直连: %v", m)
	}

	// 空列表 = 不限制：应走代理
	sources = ""
	if m := fetch("/fake_c/search"); m["proxied"] != true {
		t.Fatalf("空列表应保持不限制（走代理）: %v", m)
	}
}
