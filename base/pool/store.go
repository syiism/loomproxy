package pool

import (
	"encoding/json"
	"log"
	"sort"
	"sync"
	"time"

	"loomproxy/models"
)

// deviceOf 把库记录还原为 Provider 视角的号（Ident + Attrs + 额度台账）
//
// `attrs` 解不开时**必须出声**（待办清单 P59）：这个形状的后果是"号在库里但凭证为空"，
// 而下游症状是 Provider 各种奇怪的失败或静默拿不到号——查的人第一站绝不会想到那一列。
// 常见成因不是代码写错（`Attrs` 的类型就是 `map[string]string`，塞嵌套根本编译不过），
// 而是**库外面动过这一列**：手填的 SQL、半截的文本、别的版本写出来的形态。
func deviceOf(row *models.PoolDevice) *Device {
	attrs, err := decodeAttrsErr(row.Attrs)
	if err != nil {
		noticeBadAttrs(row.Pool, row.Ident, err)
	}
	return &Device{
		Ident:   row.Ident,
		Attrs:   attrs,
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
	m, _ := decodeAttrsErr(raw)
	return m
}

// decodeAttrsErr 是 decodeAttrs 的带错版本：**空串合法**（那个号就是没附加凭证），
// 非空却解不开才是异常——把这两种混在一起报，日志就会被"本来没凭证的号"刷满。
func decodeAttrsErr(raw string) (map[string]string, error) {
	m := map[string]string{}
	if raw == "" {
		return m, nil
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]string{}, err
	}
	return m, nil
}

// badAttrsNoticed 记住"这个号的 attrs 已经点过一次名"：同一行被反复读（快照、领取、巡检都会读它），
// 每读一次喊一声会把日志泡坏——判据同 `db.NoticeReplacedSetting` 与号池的 `ErrCapacityReached`。
// 键是 池名+标识，量级就是库里的号数，不需要回收。
var badAttrsNoticed sync.Map // "pool/ident" -> struct{}

// noticeBadAttrs 出一条不含凭证内容的点名日志。
// 标识走 `maskIdent`：这列本身可能就是上游身份，**原始值不得进日志**（AGENTS §11）。
func noticeBadAttrs(poolName, ident string, err error) {
	key := poolName + "/" + ident
	if _, seen := badAttrsNoticed.Load(key); seen {
		return
	}
	badAttrsNoticed.Store(key, struct{}{})
	log.Printf("ERROR: 号池 %s 的号 %s 的 attrs 解不开（%v）——它会被当作「凭证为空」的号使用，"+
		"症状通常是 Provider 拿不到号或拿到的号不能用；这一列只可能由库外面改动过",
		poolName, maskIdent(ident), err)
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
