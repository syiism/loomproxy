package base

// 代理重试那一路的**接线**验证。
//
// 这一条是怎么被要求上的：`Fetch` 在"第一发网络错误 → 换代理/回退直连"那段里，
// 会把同一个 `*http.Request` 交出去第二次。第一发已经把 `req.Body` 读走了，
// 第二次发出去的就是空体——上游回「缺参数/签名不符」，读起来却像我们的签名错了。
//
// **写这条用例的过程里证伪了一个更简单的说法**：连接被拒（`127.0.0.1:1` 那种）时失败发生在
// 写请求之前，`req.Body` 没被消费，第二次照发原样——那个形状的变异**测不红**。
// 会坏的形状是"对端收了几个字节再断"：那时 body 已经被读走。所以下面用的是那种假代理。
// 留这一层的理由是这条轴的入参是 `io.Reader`（调用方可以给流，不只是 strings.Reader），
// 骨架不该在第二次发空体。

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loomproxy/conf"
)

func TestFetchRetriesProxyWithFullBody(t *testing.T) {
	saved := conf.Config
	if saved == nil {
		conf.Config = &conf.ConfMgr{}
	}
	conf.Config.UpstreamProxies = []string{dyingProxy(t)}
	t.Cleanup(func() { conf.Config = saved })

	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.Store(string(b))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	t.Cleanup(srv.Close)

	payload := `{"version":1,"data":"` + strings.Repeat("密", 400) + `","nonce":"iv"}`
	h := &BaseHandler{Path: "/demo/content"}
	resp, err := h.Fetch(context.Background(), srv.URL, "POST",
		map[string]string{"content-type": "application/json"}, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("代理断了应当回退直连成功，实际报错: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if s, _ := got.Load().(string); s != payload {
		t.Errorf("代理重试那一次发出的是空体或被截断的体：got %d 字节，want %d 字节", len(s), len(payload))
	}
}

// dyingProxy 收下连接、读掉一截就把连接掐断：客户端此时已经从 req.Body 读走了数据、
// 却还没把请求发完——这正是"重试要能重发同一个体"的那个形状。
func dyingProxy(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("本机起不了监听: ", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(cc net.Conn) {
				defer cc.Close()
				buf := make([]byte, 1024)
				_, _ = cc.Read(buf)
				time.Sleep(20 * time.Millisecond)
			}(c)
		}
	}()
	return "http://" + ln.Addr().String()
}
