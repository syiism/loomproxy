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

var tzShanghai *time.Location
var tzOnce sync.Once

func TZShanghai() *time.Location {
	tzOnce.Do(func() {
		tzShanghai = time.FixedZone("CST", conf.Config.TZOffsetHours*3600)
	})
	return tzShanghai
}

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
	return time.Unix(ts, 0).In(TZShanghai()).Format("2006-01-02 15:04:05")
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
	return time.Unix(ts, 0).In(TZShanghai()).Format("2006-01-02")
}

func toInt(s string) (int, error) {
	return strconv.Atoi(s)
}
