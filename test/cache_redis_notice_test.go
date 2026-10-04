package test

// 第十二遍：接口缓存的 Redis 那一段坏掉时，系统必须知道自己坏了（P68）。
//
// 修前的形状有两处：`Set` 连 `c.redis.Set(...)` 返回的错误都没接，`Get` 把「连不上」与
// 「没命中」回成同一个 `(nil,false)`（判据同 P56 那条「返回值两种含义」）。
// 后果不是崩溃而是**看不见的降级**：缓存不再持久写、重启后名称维度整片留空（P16 那格），
// 而日志里没有一行能说明"为什么"。
//
// 这条用例需要真的把 Redis 弄挂，所以自己起一份**只在随机端口、只对本用例可见**的实例
// （绝不碰机器上 6379 那个）；没有 redis-server 的机器直接 skip，不伪装成通过。

import (
	"bytes"
	"log"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"loomproxy/conf"
	"loomproxy/utils"
)

func stopRedis(cmd *exec.Cmd) {
	// 本机 Go 是 go1.27.1-X:nodwarf5，它的 os/exec **没有 Cmd.Kill 这个方法**
	// （Cmd.Process.Kill 有）。别把它"顺手改回" cmd.Kill()——那在这台机器上编译不过。
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取空闲端口失败: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startRedis(t *testing.T, port int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("redis-server", "--port", strconv.Itoa(port), "--save", "", "--dir", t.TempDir(),
		"--daemonize", "no", "--loglevel", "warning")
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动本地 redis 失败: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond); err == nil {
			_ = c.Close()
			return cmd
		}
		time.Sleep(80 * time.Millisecond)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	t.Fatalf("redis 起了但端口 %d 一直没就绪", port)
	return nil
}

func TestCacheRedisDegradationSpeaks(t *testing.T) {
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("这台机器上没有 redis-server：这条用例需要真把 Redis 弄挂，不能拿断言伪装通过")
	}
	newTestServer(t)

	port := freePort(t)
	srv := startRedis(t, port)
	t.Cleanup(func() { stopRedis(srv); _ = srv.Wait() })

	// conf.Config 是全局的：**记下并还原**（P57 的教训：改全局不还原，会把不相干的用例卷进来）
	enabled0, host0, port0 := conf.Config.RedisEnabled, conf.Config.RedisHost, conf.Config.RedisPort
	t.Cleanup(func() {
		conf.Config.RedisEnabled, conf.Config.RedisHost, conf.Config.RedisPort = enabled0, host0, port0
	})
	conf.Config.RedisEnabled, conf.Config.RedisHost, conf.Config.RedisPort = true, "127.0.0.1", port

	c, err := utils.NewCacheWithRedis(50, time.Minute)
	if err != nil {
		t.Fatalf("建带 Redis 的缓存失败: %v", err)
	}
	c.Set("probe:k", 1)
	if v, ok := c.Get("probe:k"); !ok || v != float64(1) {
		t.Fatalf("Redis 通路没打通（得到 %v,%v）——后面所有「坏了会出声」的断言都是空转", v, ok)
	}

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOut)
	spoken := func(sub string) int { return strings.Count(buf.String(), sub) }

	// ① 先验「未命中」与「故障」分得开——**必须在 Redis 还活着的时候**查一个必然没有的键。
	//    放在弄挂之后，测到的只是"连不上也出声"，与混判无关（第一版我就是这么排错的）
	if _, ok := c.Get("probe:definitely-absent"); ok {
		t.Errorf("一个没写过的键居然命中了")
	}
	time.Sleep(120 * time.Millisecond)
	if n := spoken("读取失败"); n != 0 {
		t.Errorf("未命中被报成故障（出声 %d 条, want 0）——这正是原来那个混判", n)
	}

	// ② 真的把它弄挂
	stopRedis(srv)
	_ = srv.Wait()

	// ③ 写与读各出一声，且**同一种只出一声**
	for i := 0; i < 3; i++ {
		c.Set("probe:k", i+2)
		c.Get("probe:k")
		c.Del("probe:k") // 失效通路单独一份：它失败意味着"撤销返回成功、缓存照样命中"
	}
	if n := spoken("写入失败"); n != 1 {
		t.Errorf("写入失败出声 %d 条, want 1（每次一发就是泡坏读数）", n)
	}
	if n := spoken("读取失败"); n != 1 {
		t.Errorf("读取失败出声 %d 条, want 1", n)
	}
	if n := spoken("失效失败"); n != 1 {
		t.Errorf("Redis 的 Del 失败出声 %d 条, want 1（失效失败比写失败更要紧，不能并进写那一份）", n)
	}
	if !strings.Contains(buf.String(), "ERROR:") || !strings.Contains(buf.String(), "connect") {
		t.Errorf("出声里没带成因（读的人还得猜）：\n%s", buf.String())
	}

	// ④ 恢复：再来一声「已恢复」，并带上此前累计的次数
	restored := startRedis(t, port)
	t.Cleanup(func() { stopRedis(restored); _ = restored.Wait() })
	// 客户端有重连退避，给一点时间后重试到成功为止（最多 5 秒）
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.Set("probe:k", 99)
		if _, ok := c.Get("probe:k"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Redis 恢复后缓存仍然不可用：\n%s", buf.String())
		}
		time.Sleep(150 * time.Millisecond)
	}
	c.Del("probe:k") // 失效侧要单独走一次，才会出它自己那一声恢复
	if n := spoken("失效已恢复"); n != 1 {
		t.Errorf("Del 侧恢复没出声（%d 条, want 1）", n)
	}
	if n := spoken("写入已恢复"); n != 1 {
		t.Errorf("恢复没出声（%d 条, want 1）——坏→好翻转两头都该说一次", n)
	}
	if !strings.Contains(buf.String(), "此前累计失败") {
		t.Errorf("恢复那句里没带累计次数，读的人估不出影响面：\n%s", buf.String())
	}
}
