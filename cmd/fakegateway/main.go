// Command fakegateway 是跨进程测试（test/python）的服务入口：底座产品二进制不带任何
// 数据源，而管线用例（鉴权 → 监控 → baseUrl 解析 → 访问控制 → 计费 → 限流）需要有
// 真实挂载的路源可打。这里只做一件事——登记 testkit/fakesource 的三个假源，其余与
// 产品入口完全一致。
//
// 不属于产品功能：不要部署、不要在产品入口调用 fakesource.Register()。
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"loomproxy/app"
	"loomproxy/testkit/fakesource"
)

func main() {
	fakesource.Register()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal: %v, shutting down...", sig)
		cancel()
	}()

	if err := app.Run(ctx); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
