package test

// 中间件链顺序守护用例。
//
// 装配改为注册表驱动之后，「链长什么样」不再由 app.go 的行序表达，因此顺序必须由断言钉住：
// 这三条不变量各有一次真实的事故面——
//   - monitor 早于 access/billing/ratelimit：否则 403/429 不进监控明细，自动拉黑也失去信号源；
//   - apiauth 早于 monitor 与 baseurl：否则监控明细拿不到 username、baseUrl 拿不到用户配置；
//   - ipblock 晚于 cors：否则 OPTIONS 预检被拉黑 IP 拦死。
// 顺序改动的正确姿势是改 middleware 的 Order 常量，本用例随即失败以逼一次审查。

import (
	"reflect"
	"strings"
	"testing"

	"loomproxy/middleware"
	_ "loomproxy/middleware/all"
)

func TestMiddlewareRouteChainOrder(t *testing.T) {
	got := middleware.RouteNames(middleware.Spec{Source: "fake_a", Action: "search", AuthRequired: true})
	want := []string{"apiauth", "monitor", "baseurl", "access", "billing", "ratelimit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("数据源路由链顺序 = %s，期望 %s", strings.Join(got, "→"), strings.Join(want, "→"))
	}
}

func TestMiddlewareControlPlaneChainSkipsSourceAxes(t *testing.T) {
	// 控制面端点（无源名、未声明鉴权）：monitor/billing/ratelimit 不应进链
	got := middleware.RouteNames(middleware.Spec{})
	want := []string{"baseurl", "access"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("控制面路由链 = %s，期望 %s", strings.Join(got, "→"), strings.Join(want, "→"))
	}
}

func TestMiddlewareGlobalsOrder(t *testing.T) {
	got := middleware.GlobalNames()
	want := []string{"requestid", "recovery", "logging", "cors", "cachecontrol", "ipblock"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("全局链顺序 = %s，期望 %s", strings.Join(got, "→"), strings.Join(want, "→"))
	}

	// 不变量的显式断言（顺序改动若只动这两项，上面的全序列断言可能被一起改掉而漏掉）
	pos := make(map[string]int, len(got))
	for i, name := range got {
		pos[name] = i
	}
	if pos["ipblock"] <= pos["cors"] {
		t.Errorf("ipblock 必须晚于 cors，否则预检请求被拉黑 IP 拦死")
	}
	if len(middleware.Globals()) != len(got) {
		t.Errorf("Globals() 返回 %d 条，与 GlobalNames() 的 %d 条不一致", len(middleware.Globals()), len(got))
	}
}
