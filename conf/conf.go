package conf

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const defaultJWTSecret = "loomproxy-default-secret-change-me"

type ConfMgr struct {
	TimeoutConnect float64
	TimeoutPool    float64

	CacheTTL     int
	CacheMaxSize int

	// 上游响应短 TTL 缓存（detail/chapter 等幂等接口，缓解上游限流）
	UpstreamCacheTTL     float64
	UpstreamCacheMaxSize int

	// 熔断器（按上游 host）：连续失败 CIRCUIT_BREAKER_FAILURES 次后开启，
	// CIRCUIT_BREAKER_COOLDOWN 秒内快速失败，之后放行探测
	CircuitBreakerEnabled  bool
	CircuitBreakerFailures int
	CircuitBreakerCooldown float64

	// 上游 IP 代理池与请求头轮换（对抗按 IP/指纹的限流）
	UpstreamProxies   []string
	UpstreamProxyFile string
	UpstreamUARotate  bool
	// 动态代理 API（如 https://proxy.scdn.io/api/get_proxy.php?...），
	// 定时拉取并健康校验后入池；空为禁用
	UpstreamProxyAPI         string
	UpstreamProxyAPIScheme   string // API 返回代理的协议（http/socks5），与 API 的 protocol 参数对应
	UpstreamProxyAPIInterval int    // 拉取间隔秒
	UpstreamProxyCheckURL    string // 代理健康校验 URL

	ServerHost     string
	ServerPort     int
	ServerLogLevel string

	TZOffsetHours int

	ErrorCode int

	DataDir      string
	DataFileGlob string

	// MonitorRetentionDays 接口调用明细（api_call_logs）的保留天数；
	// <=0 表示永久保留且不做清理（默认），>0 时过期明细先聚合归档再删除
	MonitorRetentionDays int

	// MonitorBackfillSec 名称回填的轮询间隔（秒）：用命名缓存补明细里缺失的书名/章节名。
	// <=0 关闭定时回填（只留手动入口）；回填幂等，所以间隔只决定「空名最多留多久」
	MonitorBackfillSec int

	AuthEnabled   bool
	APIKeys       []string
	AuthWhitelist []string

	DBType     string
	DBHost     string
	DBPort     int
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string

	RedisEnabled  bool
	RedisHost     string
	RedisPort     int
	RedisPassword string
	RedisDB       int

	JWTSecret      string
	JWTExpireHours int

	AdminUsername string
	AdminPassword string

	// 号池（base/pool 通用能力，具体业务池由各数据源的 Provider 注册）
	PoolEnabled bool
	// 冷备号数量（未领取、不过期，热号退役时转正）
	PoolColdSpares int
	// 热号上限（平时 1 个，观测到上游限流时错误驱动扩容至此上限）
	PoolMaxHot int
	// dead 号保留上限，超出部分物理删除
	PoolMaxDead int
	// 热号到期前多少秒续领（领取叠加无损耗）
	PoolRenewBeforeSec int
	// 维护协程巡检间隔（秒）
	PoolMaintainSec int

	// RetiredSources 本部署已下线的历史数据源码（逗号分隔，默认空）：启动时清理其在各
	// 配置表的存量行。底座不携带源实现，无从知道该清理谁的存量行，故清单由部署侧声明
	RetiredSources []string
}

var Config *ConfMgr

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		return strings.ToLower(v) == "true"
	}
	return def
}

func envList(key, def string) []string {
	raw := envStr(key, def)
	var result []string
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			result = append(result, k)
		}
	}
	return result
}

func loadDotEnv() {
	// 优先用当前工作目录的 .env（开发场景），其次用可执行文件同级的 .env（部署场景）
	var envPath string
	if wd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(wd, ".env")
		if _, err := os.Stat(candidate); err == nil {
			envPath = candidate
		}
	}
	if envPath == "" {
		if exePath, err := os.Executable(); err == nil {
			candidate := filepath.Join(filepath.Dir(exePath), ".env")
			if _, err := os.Stat(candidate); err == nil {
				envPath = candidate
			}
		}
	}
	if envPath == "" {
		return
	}

	f, err := os.Open(envPath)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		value = strings.Trim(value, `"'`)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		os.Setenv(key, value)
	}
	_ = scanner.Err()
}

func Load() {
	loadDotEnv()
	Config = &ConfMgr{
		TimeoutConnect: envFloat("TIMEOUT_CONNECT", 5.0),
		TimeoutPool:    envFloat("TIMEOUT_POOL", 10.0),

		CacheTTL:     envInt("CACHE_TTL", 300),
		CacheMaxSize: envInt("CACHE_MAXSIZE", 128),

		UpstreamCacheTTL:     envFloat("UPSTREAM_CACHE_TTL", 10),
		UpstreamCacheMaxSize: envInt("UPSTREAM_CACHE_MAXSIZE", 512),

		CircuitBreakerEnabled:  envBool("CIRCUIT_BREAKER_ENABLED", true),
		CircuitBreakerFailures: envInt("CIRCUIT_BREAKER_FAILURES", 5),
		CircuitBreakerCooldown: envFloat("CIRCUIT_BREAKER_COOLDOWN", 30),

		UpstreamProxies:   envList("UPSTREAM_PROXIES", ""),
		UpstreamProxyFile: envStr("UPSTREAM_PROXY_FILE", ""),
		UpstreamUARotate:  envBool("UPSTREAM_UA_ROTATE", false),

		UpstreamProxyAPI:         envStr("UPSTREAM_PROXY_API", ""),
		UpstreamProxyAPIScheme:   envStr("UPSTREAM_PROXY_API_SCHEME", "http"),
		UpstreamProxyAPIInterval: envInt("UPSTREAM_PROXY_API_INTERVAL", 120),
		UpstreamProxyCheckURL:    envStr("UPSTREAM_PROXY_CHECK_URL", "https://www.baidu.com"),

		ServerHost:     envStr("SERVER_HOST", "0.0.0.0"),
		ServerPort:     envInt("SERVER_PORT", 8081),
		ServerLogLevel: envStr("SERVER_LOG_LEVEL", "info"),

		TZOffsetHours: envInt("TZ_OFFSET_HOURS", 8),

		ErrorCode: envInt("ERROR_CODE", -1),

		DataDir:      envStr("DATA_DIR", "data"),
		DataFileGlob: envStr("DATA_FILE_GLOB", "*.json"),

		MonitorRetentionDays: envInt("MONITOR_RETENTION_DAYS", 0),
		MonitorBackfillSec:   envInt("MONITOR_BACKFILL_SEC", 900),

		AuthEnabled:   envBool("AUTH_ENABLED", false),
		APIKeys:       envList("API_KEYS", ""),
		AuthWhitelist: envList("AUTH_WHITELIST", "/,/data,/auth/register,/auth/login,/panel"),

		DBType:     envStr("DB_TYPE", "mysql"),
		DBHost:     envStr("DB_HOST", "localhost"),
		DBPort:     envInt("DB_PORT", 3306),
		DBUser:     envStr("DB_USER", "root"),
		DBPassword: envStr("DB_PASSWORD", "123456"),
		DBName:     envStr("DB_NAME", "loomproxy"),
		DBSSLMode:  envStr("DB_SSLMODE", "disable"),

		RedisEnabled:  envBool("REDIS_ENABLED", true),
		RedisHost:     envStr("REDIS_HOST", "localhost"),
		RedisPort:     envInt("REDIS_PORT", 6379),
		RedisPassword: envStr("REDIS_PASSWORD", ""), // 默认无密码：envStr 把 .env 里的空值也当未设置，默认值写死一个密码就等于让"无密码的 Redis"连不上
		RedisDB:       envInt("REDIS_DB", 0),

		JWTSecret:      envStr("JWT_SECRET", "loomproxy-default-secret-change-me"),
		JWTExpireHours: envInt("JWT_EXPIRE_HOURS", 24*7),

		AdminUsername: envStr("ADMIN_USERNAME", ""),
		AdminPassword: envStr("ADMIN_PASSWORD", ""),

		PoolEnabled:        envBool("POOL_ENABLED", true),
		PoolColdSpares:     envInt("POOL_COLD_SPARES", 2),
		PoolMaxHot:         envInt("POOL_MAX_HOT", 3),
		PoolMaxDead:        envInt("POOL_MAX_DEAD", 10),
		PoolRenewBeforeSec: envInt("POOL_RENEW_BEFORE_SEC", 300),
		PoolMaintainSec:    envInt("POOL_MAINTAIN_SEC", 60),

		RetiredSources: envList("RETIRED_SOURCES", ""),
	}

	// 生产环境（启用鉴权）强制要求修改默认 JWT_SECRET
	if Config.AuthEnabled && Config.JWTSecret == defaultJWTSecret {
		log.Fatalf("SECURITY ERROR: JWT_SECRET 使用默认值，生产环境必须设置强随机密钥（环境变量 JWT_SECRET）")
	}

	// 开发模式下使用默认值仅警告
	if Config.JWTSecret == defaultJWTSecret && Config.ServerLogLevel == "debug" {
		log.Printf("WARNING: 使用默认 JWT_SECRET，生产环境请务必通过 JWT_SECRET 环境变量设置强密钥")
	}
}
