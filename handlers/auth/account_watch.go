package auth

// 账号侧登录尝试的**只读观察**（为待办清单 P40① 准备的那份数据，本身不做任何处置）。
//
// 为什么要它：P40 的闸门只按 IP，而 14 天的生产 journal 读数是
// 「536 次登录失败 / 222 个不同 IP，单 IP 最多 14 次」——这个形状既可能是大量用户各输错几次，
// 也可能是分布式爆破，**按 IP 的计数器分不出这两种**，而它恰是"要不要账号侧闸门"这个决策唯一缺的东西。
//
// 三条边界写死在这里：
//  1. **只看不管**：不锁、不延窗、不参与 `locked()` 判定。账号侧要不要闸门是 P40①，还没拍；
//     拍了就用这份读数拍，不拍它就只是一个每小时过期的内存表。
//  2. **不入库**：与 `ipAttemptLimiter` 同一种形态——进程内存、重启即清、多实例各有一份（AGENTS §12）。
//     把「谁在何时用什么账号试登录」变成一份长期留存表，是另一件事、要单独立项定保留期（P40 的「没做的那一半」）。
//  3. **有界**：账号条目数越过阈值就扫过期；每个账号只记**前 N 个**不同 IP 的具体值，
//     再多只加计数——喷 IP 撑不爆这张表，就像那 222 个 IP 撑不爆上一张。

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// accountWatchKeep 是一条账号观察活多久。比限频窗口（1 分钟）长得多：
	// 慢速试一个账号正是按 IP 那道闸摸不到的形状，窗口太短就等于只看得到"输错密码"。
	accountWatchKeep = time.Hour
	// accountWatchPurgeThreshold 同 attemptPurgeThreshold：不是容量上限，是"越过就顺手扫一次"的触发点。
	accountWatchPurgeThreshold = 4096
	// accountWatchIPLimit 每个账号最多逐条记住几个来源 IP，其余只进 distinct_ips 的数。
	accountWatchIPLimit = 8
	// accountWatchKeyLimit 账号标识的截断长度：键是用户输入，别让它按输入长度长。
	accountWatchKeyLimit = 64
)

type accountTally struct {
	attempts    int
	fails       int
	firstSeen   time.Time
	lastSeen    time.Time
	ips         map[string]bool // 只存前 accountWatchIPLimit 个
	distinctIPs int             // 真实不同 IP 数（含没记住的）
}

// AccountWatchSnapshot 是一条账号观察在管理面的形状。
// 字段名一律用「尝试/失败」而不是「攻击/爆破」——**这份数据不足以支持那个词**，判定是人做的。
type AccountWatchSnapshot struct {
	Principal   string    `json:"principal"` // 归一化后的登录标识（小写、去首尾空白、截断）
	Attempts    int       `json:"attempts"`
	Fails       int       `json:"fails"`
	DistinctIPs int       `json:"distinct_ips"`
	SampleIPs   []string  `json:"sample_ips"` // 至多 accountWatchIPLimit 个；超出只体现在 distinct_ips
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
}

type accountWatchStore struct {
	mu       sync.Mutex
	byAcct   map[string]*accountTally
	warnings int // 被截断掉的 IP 记录数（读数里以 distinct 体现，这里只留一个内部计数便于自查）
}

var loginAccountWatch = &accountWatchStore{byAcct: make(map[string]*accountTally)}

// watchKey 归一化登录标识：去首尾空白 + 转小写 + 按 rune 截断。
// 刻意不做「邮箱取前缀」这类归一：那会把两个不同账号合成一个，读数额就假了。
func watchKey(principal string) string {
	k := strings.ToLower(strings.TrimSpace(principal))
	if len(k) > accountWatchKeyLimit {
		r := []rune(k)
		if len(r) > accountWatchKeyLimit {
			r = r[:accountWatchKeyLimit]
		}
		k = string(r) + "…"
	}
	return k
}

// note 记一次尝试。`success` 为真时也算一次尝试（它提供"这个账号同时在被人正常登录"的分母）。
//
// 调用方传进来的标识**已经过 `Login` 的 trim + 小写**（那里是入口，空白用户名在进 DB 查询之前
// 就被判成"没填"并 400），所以这里不再补一道"空键守卫"——为一个到不了的路径写检查，
// 换来的是一条永远不会红的断言（本轮就是这么被自己的用例骗过一次：那条断言在变异掉守卫之后仍然绿）。
func (s *accountWatchStore) note(principal, ip string, success bool) {
	key := watchKey(principal)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.byAcct[key]
	if t == nil {
		t = &accountTally{firstSeen: now, ips: make(map[string]bool)}
		s.byAcct[key] = t
	}
	t.attempts++
	if !success {
		t.fails++
	}
	t.lastSeen = now
	if !t.ips[ip] {
		t.ips[ip] = true
		t.distinctIPs++
		if len(t.ips) > accountWatchIPLimit {
			delete(t.ips, ip) // 只丢"新来的这个"，已记住的不被挤掉：样本要稳定，否则面板每刷一次换一批
			s.warnings++
		}
	}
	if len(s.byAcct) > accountWatchPurgeThreshold {
		s.purgeLocked(now)
	}
}

// purgeLocked 删掉超过 accountWatchKeep 没再被动的账号。调用方须已持锁。
func (s *accountWatchStore) purgeLocked(now time.Time) {
	for k, t := range s.byAcct {
		if now.Sub(t.lastSeen) > accountWatchKeep {
			delete(s.byAcct, k)
		}
	}
}

// snapshot 导出当前观察：失败多的排前面，其次按尝试数、再按标识。顺带扫一次过期条目。
func (s *accountWatchStore) snapshot() []AccountWatchSnapshot {
	now := time.Now()
	s.mu.Lock()
	s.purgeLocked(now)
	out := make([]AccountWatchSnapshot, 0, len(s.byAcct))
	for k, t := range s.byAcct {
		ips := make([]string, 0, len(t.ips))
		for ip := range t.ips {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		out = append(out, AccountWatchSnapshot{
			Principal: k, Attempts: t.attempts, Fails: t.fails,
			DistinctIPs: t.distinctIPs, SampleIPs: ips,
			FirstSeen: t.firstSeen, LastSeen: t.lastSeen,
		})
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Fails != out[j].Fails {
			return out[i].Fails > out[j].Fails
		}
		if out[i].Attempts != out[j].Attempts {
			return out[i].Attempts > out[j].Attempts
		}
		return out[i].Principal < out[j].Principal
	})
	return out
}

// LoginAccountWatch 导出登录尝试的账号侧观察（管理端点用）。**只读**：处置语义一点没动。
func LoginAccountWatch() []AccountWatchSnapshot { return loginAccountWatch.snapshot() }

// ResetLoginAccountWatchForTest 清表（集成用例共享进程，与 ResetAttemptLimitersForTest 同一个钩子位）。
func ResetLoginAccountWatchForTest() {
	loginAccountWatch.mu.Lock()
	loginAccountWatch.byAcct = make(map[string]*accountTally)
	loginAccountWatch.warnings = 0
	loginAccountWatch.mu.Unlock()
}
