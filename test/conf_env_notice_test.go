package test

// 待办清单 P98（第三十四遍巡检发现）：环境变量的非法值以前一律静默回落到默认值。
// 方向上最危险的两位恰好都在这一族里——
//   `MONITOR_RETENTION_DAYS=1y` → 0（永久保留，把「表只进不出」打开了）
//   `AUTH_ENABLED=1`            → false（以为鉴权开着，其实整层网关关着）
// 这两条都不报错，只会让下一个人从「读数为什么不对」开始查。
//
// 本用例钉的是**两件事**，缺一不可：
//   ① 解析语义一个字没变（布尔位仍只认真正的 true）——把它改宽是行为改动，不是修 bug；
//   ② 出声现在有了：日志里必须点出键名与回落原因。
// 另有一条把 conf 的**文档化默认值**钉在解析器上：`test/monitor_retention_test.go` 里那句
// 「MONITOR_RETENTION_DAYS 默认应为 0」读的是测试脚手架的零值（结构体字面量里没写的字段），
// 把 `conf.go` 的默认改成 7 它照样绿——那是假绿灯，真正的默认钉子在这页。

import (
	"bytes"
	"log"
	"os"
	"strings"
	"sync"
	"testing"

	"loomproxy/conf"
)

// syncBuf 是带锁的日志缓冲：`go test -race ./...` 里前面用例留下的后台协程
// （回填循环、号池维护）可能在捕获期间写标准日志，无锁的 bytes.Buffer 会被判成数据竞争——
// 那种红不是被测代码的问题，是探针自己的问题（同 P94 报告 §9.2 那一族"探针自造读数"）。
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// loadConfIsolated 在**空目录**里跑一次 conf.Load() 并把日志接到 buffer 里。
// 为什么换 cwd：`loadDotEnv` 先找当前工作目录的 `.env`，测试的 cwd 是包目录——
// 开发者手上那份 `.env`（仓库根的那份不算，但有人会在包目录放）会混进读数，
// 换到空 temp 目录让"这次没有 .env"成为一个事实而不是假设。
// conf.Config 是指针全局，t.Cleanup 里还原，所以这条用例不依赖、也不污染别的用例。
func loadConfIsolated(t *testing.T) *syncBuf {
	t.Helper()

	prev := conf.Config
	buf := &syncBuf{}
	logPrev := log.Writer()

	cwdPrev, err := os.Getwd()
	if err != nil {
		t.Fatalf("取 cwd 失败: %v", err)
	}
	dir := t.TempDir()

	t.Cleanup(func() { conf.Config = prev })
	t.Cleanup(func() { log.SetOutput(logPrev) })
	t.Cleanup(func() {
		if err := os.Chdir(cwdPrev); err != nil {
			t.Errorf("切回原目录失败: %v", err)
		}
	})

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("切到空目录失败: %v", err)
	}
	log.SetOutput(buf)
	conf.Load()
	return buf
}

func TestEnvBadValuesStillFallBackButNowMakeNoise(t *testing.T) {
	// 布尔位：`1` 与 `yes` 都不是 true（这条钉的是语义没被改宽）
	t.Setenv("AUTH_ENABLED", "1")
	t.Setenv("REDIS_ENABLED", "yes")
	// 整数位：带单位不是整数
	t.Setenv("MONITOR_RETENTION_DAYS", "1y")
	t.Setenv("BILLING_DEDUPE_SEC", "5m")
	// 浮点位
	t.Setenv("UPSTREAM_CACHE_TTL", "fast")
	// 合法的对照：这一条不许出声，否则门禁变成噪音，下一个人就开始无视它
	t.Setenv("POOL_MAINTAIN_SEC", "90")

	// AUTH_ENABLED 解析成 false，下面那条"默认 JWT_SECRET 就拒绝启动"的检查不会触发；
	// 仍给一个显式值，免得开发者环境里恰好没设而把用例跑成 nil 分支。
	t.Setenv("JWT_SECRET", "unit-test-secret-not-a-real-one")

	buf := loadConfIsolated(t)
	out := buf.String()

	if conf.Config.AuthEnabled {
		t.Error("AUTH_ENABLED=1 被读成了 true：本用例钉的是「只认真正的 true」，不是把它改宽")
	}
	if conf.Config.RedisEnabled {
		t.Error("REDIS_ENABLED=yes 被读成了 true（同上）")
	}
	if conf.Config.MonitorRetentionDays != 0 {
		t.Errorf("非法值应回落到默认 0（永久保留），实为 %d", conf.Config.MonitorRetentionDays)
	}
	if conf.Config.BillingDedupeSec != 300 {
		t.Errorf("非法值应回落到默认 300，实为 %d", conf.Config.BillingDedupeSec)
	}
	if conf.Config.UpstreamCacheTTL != 10 {
		t.Errorf("非法值应回落到默认 10，实为 %v", conf.Config.UpstreamCacheTTL)
	}
	if conf.Config.PoolMaintainSec != 90 {
		t.Errorf("合法的 POOL_MAINTAIN_SEC=90 没被采用（实为 %d）——回落把合法值也吞了", conf.Config.PoolMaintainSec)
	}

	for _, key := range []string{"AUTH_ENABLED", "REDIS_ENABLED", "MONITOR_RETENTION_DAYS", "BILLING_DEDUPE_SEC", "UPSTREAM_CACHE_TTL"} {
		if !strings.Contains(out, key) {
			t.Errorf("日志里没有对 %s 出声（静默回落正是本用例要拦的东西）。日志=%q", key, out)
		}
	}
	if strings.Contains(out, "POOL_MAINTAIN_SEC") {
		t.Errorf("合法值也被出声了，门禁变噪音。日志=%q", out)
	}
	// 凭证不在这条通路上（`envStr`/`envList` 没有"非法值"概念，因此不会回显）：
	// 这条断言盯的是"哪天有人给字符串键加出声，别把值带进日志"（P62 同判据）。
	if strings.Contains(out, "unit-test-secret-not-a-real-one") {
		t.Error("日志里出现了 JWT_SECRET 的原值")
	}
}

// TestEnvDocumentedDefaults 把文档里那几条默认钉在解析器上。
// 只挑**改了会改变运维后果**的几位，不是把整张表抄一遍——抄一遍就是第二份事实来源，
// 而且每加一个设置项都要来这里补一行。
func TestEnvDocumentedDefaults(t *testing.T) {
	for _, key := range []string{
		"MONITOR_RETENTION_DAYS", "MONITOR_BACKFILL_SEC", "BILLING_DEDUPE_SEC",
		"CACHE_TTL", "UPSTREAM_CACHE_TTL", "DB_TYPE", "AUTH_ENABLED", "TZ_OFFSET_HOURS",
		"REDIS_PASSWORD", "POOL_RENEW_BEFORE_SEC", "POOL_HOOK_TIMEOUT_SEC",
		"RANK_CACHE_SEC", // 待办清单 P124：这一位曾经**只声明没装配**，见下面那一格的注释
	} {
		t.Setenv(key, "") // env* 把空串当未设置
	}
	t.Setenv("JWT_SECRET", "unit-test-secret-not-a-real-one")

	loadConfIsolated(t)

	cases := []struct {
		name string
		got  interface{}
		want interface{}
		why  string
	}{
		{"MonitorRetentionDays", conf.Config.MonitorRetentionDays, 0, "0=永久保留且不做清理（AGENTS §8 点名的那个默认）"},
		{"MonitorBackfillSec", conf.Config.MonitorBackfillSec, 900, "名称回填循环的间隔"},
		{"BillingDedupeSec", conf.Config.BillingDedupeSec, 300, "同内容扣减冷却窗口（P25）"},
		{"CacheTTL", conf.Config.CacheTTL, 300, "接口缓存 TTL"},
		{"UpstreamCacheTTL", conf.Config.UpstreamCacheTTL, float64(10), "上游短 TTL 缓存"},
		{"DBType", conf.Config.DBType, "mysql", "代码默认 mysql，而 `.env.example` 默认 sqlite——这个差别就写在 §8"},
		{"AuthEnabled", conf.Config.AuthEnabled, false, "默认关；关掉不等于谁都不认（P64）"},
		{"TZOffsetHours", conf.Config.TZOffsetHours, 8, "平台时区唯一值（P71）"},
		{"RedisPassword", conf.Config.RedisPassword, "", "默认空密码——写死一个等于让无密码的 Redis 连不上"},
		{"PoolRenewBeforeSec", conf.Config.PoolRenewBeforeSec, 300, "号池临期续领提前量"},
		{"PoolHookTimeoutSec", conf.Config.PoolHookTimeoutSec, 30, "持锁调源钩子的截止（P81）"},
		// P124：这一格是本轮补的**装配钉子**。`RankCacheSec` 曾经声明了、`handlers/rank` 读了它，
		// 但 `conf.Load()` 的装配块里没有写入方——Go 给零值 0，而 0 的语义是"每次真算"，
		// 于是 P118 ③ 那颗缓存装了等于没装，而 `.env.example` 那句 `RANK_CACHE_SEC=60` 只是展示。
		// **为什么四条榜用例没抓到**：它们都直接 `conf.Config.RankCacheSec = sec` 改全局再跑，
		// 那条通路测的是"值怎么用"，永远不经过装配块——同族那句「读的是测试脚手架的零值」在这里第二次成立。
		{"RankCacheSec", conf.Config.RankCacheSec, 60, "公开榜出口层缓存的存活秒数（P118 ③），<=0 才是每次真算"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s 默认值应为 %v（%s），实为 %v", c.name, c.want, c.why, c.got)
		}
	}
}

// 待办清单 P124 的第二条腿：默认值钉住之后，还要证明**环境变量真的能改到它**。
// 只钉默认的那一半会漏掉另一种坏法：装配块里写了 `envInt("RANK_CACHE_SEC", 60)`，
// 但键名拼错（`RANK_CACHE_SECONDS`）——默认照样是 60、这条用例照样绿，而 .env 改成什么都不算。
// 所以这里三个值一起走真 `conf.Load()`：未设置=60、显式 120=120、显式 0=0（0 有业务含义：每次真算）。
func TestRankCacheSecEnvKeyActuallyReachesTheField(t *testing.T) {
	t.Setenv("JWT_SECRET", "unit-test-secret-not-a-real-one")

	t.Setenv("RANK_CACHE_SEC", "")
	loadConfIsolated(t)
	if got := conf.Config.RankCacheSec; got != 60 {
		t.Errorf("未设置时 RANK_CACHE_SEC 应为默认 60，实为 %d", got)
	}

	t.Setenv("RANK_CACHE_SEC", "120")
	loadConfIsolated(t)
	if got := conf.Config.RankCacheSec; got != 120 {
		t.Errorf("环境变量写着 120，读出来却是 %d——键名或装配块对不上（P124 的那一半）", got)
	}

	t.Setenv("RANK_CACHE_SEC", "0")
	loadConfIsolated(t)
	if got := conf.Config.RankCacheSec; got != 0 {
		t.Errorf("0 是合法值（每次真算），不该被兜底成默认，实为 %d", got)
	}
}
