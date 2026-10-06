package base

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loomproxy/conf"
)

// withProxyAPI 临时把代理 API 指到 httptest 上，跑完恢复全局（包内测试不许 t.Parallel，同集成用例规矩）。
func withProxyAPI(t *testing.T, url string) {
	t.Helper()
	prev := conf.Config
	conf.Config = &conf.ConfMgr{UpstreamProxyAPI: url, UpstreamProxyAPIScheme: "http"}
	t.Cleanup(func() { conf.Config = prev })
}

// TestFetchProxyAPIHonorsContext 钉的是 P103 的第二半：**接了 ctx 就要往下传**。
// 改之前这两处出口写的是 `http.NewRequest`，取消信号到不了——本条用例就是把 ctx 取消掉，
// 要求 fetch 立刻失败；没有 NewRequestWithContext 时它会等 client 的 15 秒超时并把数据读回来。
func TestFetchProxyAPIHonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 慢响应：不睡就分不出"被取消"与"太快所以没取消成"
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"proxies":["127.0.0.1:8888","127.0.0.1:8889"]}}`)
	}))
	defer srv.Close()
	withProxyAPI(t, srv.URL)

	// 先做一次正例：样本必须走得通，否则下面那条"取消生效"是在空转（判据页那条断言空转）
	got, err := fetchProxyAPI(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("正例就没读通（%v / %v）——用例的样本不成立", err, got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fetchProxyAPI(ctx); err == nil {
		t.Fatal("ctx 已取消仍取回数据：fetchProxyAPI 没把 ctx 传给 http 请求（P103）")
	} else if !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("取消的报错形状不对（期望含 context canceled）：%v", err)
	}
}

// TestCheckProxyAliveHonorsContext 钉的是同一件事在校验那一侧：一轮补充要听得见关停。
// 断言的是**耗时**而不是"存活与否"——代理为空时 clientForProxy 走的是默认客户端，
// 存活与否还取决于客户端配置，而"取消之后不该等满一个响应"这条只取决于 ctx 有没有传下去。
func TestCheckProxyAliveHonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	// 正例：请求真的出去了（服务端 300ms 才答），所以不取消时必须等满这一段时间——
	// 没有这一段，下面的"立刻返回"可能是空转（压根没发出请求也会很快）。
	t0 := time.Now()
	checkProxyAlive(context.Background(), "%invalid-proxy%", srv.URL)
	if elapsed := time.Since(t0); elapsed < 250*time.Millisecond {
		t.Fatalf("正例只等了 %v：请求没走到服务端，样本不成立", elapsed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t0 = time.Now()
	if checkProxyAlive(ctx, "%invalid-proxy%", srv.URL) {
		t.Fatal("ctx 已取消仍判定存活：checkProxyAlive 没把 ctx 传给请求（P103）")
	}
	if elapsed := time.Since(t0); elapsed > 200*time.Millisecond {
		t.Fatalf("取消后仍等了 %v：等的是服务端的响应而不是 ctx（P103）", elapsed)
	}
}
