package pool

import (
	"encoding/json"
	"sort"
	"time"

	"loomproxy/models"
)

// deviceOf 把库记录还原为 Provider 视角的号（Ident + Attrs + 额度台账）
func deviceOf(row *models.PoolDevice) *Device {
	return &Device{
		Ident:   row.Ident,
		Attrs:   decodeAttrs(row.Attrs),
		Payload: json.RawMessage(row.Payload),
		Quota: Quota{
			Total:     row.TotalQuota,
			Used:      row.UsedQuota,
			ExpiresAt: derefTime(row.ExpireAt),
		},
	}
}

func encodeAttrs(attrs map[string]string) string {
	if len(attrs) == 0 {
		return ""
	}
	b, err := json.Marshal(attrs)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeAttrs(raw string) map[string]string {
	m := map[string]string{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	return m
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// payloadKeys 列出载荷的第一层键名（面板只列键名，值一律不出接口）；非对象或坏 JSON 返回空
func payloadKeys(raw string) []string {
	if raw == "" {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
