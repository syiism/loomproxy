// Package pool 是通用的「号池」（凭证池）框架：把上游资源按冷热分离管理，
// 平时只维持最小活跃数量以减慢资源燃烧，临期自动续期、耗尽自动换号、
// 观测到上游限流时错误驱动扩容。
//
// 框架与业务解耦：状态机、维护节奏、持久化、状态快照由本包提供，
// 「号」的创建/额度查询/领取由数据源实现 Provider 接口注入
// （典型场景：设备号池、账号池、临时 token 池）。
package pool

import (
	"context"
	"time"
)

// 号状态（models.PoolDevice.Status）
const (
	StatusHot   = "hot"   // 已领取、有效期内，正在服务请求
	StatusCold  = "cold"  // 未领取或额度已耗尽但未续新，不过期，可随时转正
	StatusSpent = "spent" // 周期内领取次数已用完，周期重置后复活
	StatusDead  = "dead"  // 上游持续失败/被风控，超量后物理清理
)

// Quota 一次额度刷新的结果：周期内可领次数、已领次数、已领资源的到期时间。
// 到期时间为零值表示该号当前没有有效资源。
type Quota struct {
	Total     int
	Used      int
	ExpiresAt time.Time
}

// Exhausted 周期内领取次数是否已用完
func (q Quota) Exhausted() bool { return q.Used >= q.Total }

// Remaining 已领资源的墙钟剩余时长
func (q Quota) Remaining() time.Duration {
	if q.ExpiresAt.IsZero() {
		return 0
	}
	return time.Until(q.ExpiresAt)
}

// Device 池中的一个号。Ident 为池内唯一标识（落库、去重、状态展示），
// Attrs 存放上游请求所需的附加凭证（如设备 sn、签名 salt），由 Provider 自解释。
type Device struct {
	Ident string
	Attrs map[string]string
	Quota Quota
}

// Provider 业务侧适配：本包不知晓任何上游协议，号的全部生命周期操作由此实现。
type Provider interface {
	// Name 池名（通常取数据源码），进程内唯一
	Name() string
	// Create 新建一个号（只建号不领取资源，新号应为未过期状态）
	Create(ctx context.Context) (*Device, error)
	// Refresh 拉取该号的真实额度台账（不产生领取）
	Refresh(ctx context.Context, dev *Device) (Quota, error)
	// Claim 为该号领取/续期一次资源（要求叠加无损：多次领取延长有效期而非重置）
	Claim(ctx context.Context, dev *Device) error
}

// ResourceExpiredClassifier 可选实现：判定错误是否属于「号资源到期」类
// （属正常状态变化，应走自愈补领而非计入限流扩容信号）。未实现时所有非 nil
// 错误都计入疑似限流。
type ResourceExpiredClassifier interface {
	IsResourceExpired(err error) bool
}
