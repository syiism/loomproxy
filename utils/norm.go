// 数据源侧共用的通用工具：响应字段兼容读取、时间与文案格式化、时区。
// 新增书源时优先复用这里的能力，不要在源包内重复实现。
package utils

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"loomproxy/conf"
)

func ToString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// JSON 数字会被解析为 float64，大整数用 %v 会变成科学计数法（如 3.380555e+06）
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	}
	return fmt.Sprintf("%v", v)
}

func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// PlatformZone 平台时区的**唯一读取口**：值来自 `TZ_OFFSET_HOURS`（默认 8）。
// 这一族原本有七处写法——四处写死 `time.FixedZone("CST", 8*3600)`（额度日界、验证码日限、
// 距下次重置、统计面板的"今日新增"）、三处跟着进程时区 `time.Local`（榜单窗口、趋势图日界、
// 名称回填窗口）。两套都不看配置，于是把 `TZ_OFFSET_HOURS` 改成别的值，只有格式化那一侧跟着变，
// 而"今天零点"在页面上仍然是三个时刻。现网系统时区恰好是 +08:00、配置也是 8，
// 三种口径撞在一起才一直没露出来（待办清单 P71）。
// 沿用原来那个 `sync.Once`：配置在 `conf.Load()` 之后不再变，缓存住省掉每次请求里的 Location 分配。
var tzOnce sync.Once
var tzPlatform *time.Location

func PlatformZone() *time.Location {
	tzOnce.Do(func() {
		// conf.Config 为 nil（conf.Load 之前的纯函数调用面，如 uxx 的 ParseLedger 进单测）时
		// 按默认 +8 兜底——与 TZ_OFFSET_HOURS 的默认值语义一致；不守卫的话唯一读取口自己先 panic
		//（分支侧 S52 收口时实测，用例 TestPlatformZoneSafeWithoutConf 钉住）。
		hours := 8
		if conf.Config != nil {
			hours = conf.Config.TZOffsetHours
		}
		tzPlatform = time.FixedZone("CST", hours*3600)
	})
	return tzPlatform
}

// DayStart 平台时区里「今天零点往前 `days-1` 天」的那一刻——**所有日界只由这一处算**。
// 「当日」传 1，「近 7 天」传 7。要改口径就改这里，不要在调用方再算一遍零点。
func DayStart(days int) time.Time {
	if days < 1 {
		days = 1
	}
	loc := PlatformZone()
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
}

// TZShanghai 是 PlatformZone 的旧名字，只留给尚未迁移的调用方（携带形态的 `sources/xmly` 还有一处）。
// 新代码一律用 PlatformZone / DayStart——这里保留的不是"第二份事实"，它只是同一个定义的另一块门牌。
func TZShanghai() *time.Location { return PlatformZone() }

func NormalizeAPIBase(baseURL string, prefix string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, prefix) {
		return base
	}
	return base + prefix
}

func FormatTime(tsStr string) string {
	if tsStr == "" {
		return ""
	}
	ts, err := toInt64(tsStr)
	if err != nil {
		return ""
	}
	if ts > 1e12 {
		ts = ts / 1000
	}
	return time.Unix(ts, 0).In(PlatformZone()).Format("2006-01-02 15:04:05")
}

func FormatWordCount(wordNum string) string {
	if wordNum == "" {
		return "0"
	}
	n, err := toInt(wordNum)
	if err != nil {
		return wordNum
	}
	if n > 10000 {
		return fmt.Sprintf("%.1f万", float64(n)/10000.0)
	}
	return fmt.Sprintf("%d", n)
}

func toInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// ToInt64 字符串转 int64
func ToInt64(s string) (int64, error) {
	return toInt64(s)
}

// FormatDate 时间戳格式化为 YYYY-MM-DD（上海时区）
func FormatDate(ts int64) string {
	return time.Unix(ts, 0).In(PlatformZone()).Format("2006-01-02")
}

func toInt(s string) (int, error) {
	return strconv.Atoi(s)
}
