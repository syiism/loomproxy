package gate

// 扣减冷却：同一用户对「同一数据源 + 同一接口 + 同一内容」在窗口内只扣一次额度。
//
// 为什么需要（待办清单 P25）：计费按请求，判据只有 `status==200`。实测生产上第一篇正文
// 第一条请求 10 秒才回来，客户端等不及重试同一篇，两次都是 200、各扣 1 点额度——
// 用户看到的是「没读几章就没额度了」。AGENTS §12 把「按请求计费」列为有意取舍，
// 这条是它的边界外溢：重试与重读同一章，在用户眼里是同一件事。
//
// 内容标识取自 base.CallSubject（app 在进 handler 前挂进 context，handler 返回后由
// legado.ObserveCall 从请求参数填好 bookId/itemId/搜索词），所以扣减那一刻拿得到。
// **抽不到标识就不去重**——宁可多扣一次，也不能把两篇不同内容并成一条扣费。
//
// 为什么不用「查最近 N 秒的明细」做判据：明细行是内存环形缓冲淘汰时才批量落库的
// （base/metrics.go），且 monitor 落库发生在 billing 返回之后——扣减那一刻本条明细还不存在。
//
// 状态只在进程内：本部署是单二进制，重启丢掉冷却记录最多多扣一次，不会漏扣。

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"loomproxy/base"
	"loomproxy/conf"
	"loomproxy/middleware"

	"github.com/gin-gonic/gin"
)

type deductKey struct {
	userID uint
	source string
	action string
	ident  string
}

var (
	deductMu   sync.Mutex
	deductAt   = make(map[deductKey]time.Time)
	deductSkip atomic.Int64

	deductJanitorOnce sync.Once
	// deductIdleTTL 回收阈值：窗口再长也留一个上限，闲置超两倍的键必已过期。
	// 键含用户与内容维度，不回收会随读过的章数无限增长。
	deductIdleTTL = 30 * time.Minute
)

// deductWindow 当前冷却窗口；<=0 表示关闭（回到按请求扣减）。
func deductWindow() time.Duration {
	sec := conf.Config.BillingDedupeSec
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

// alreadyDeducted 回答「本次扣减该不该跳过」，并在不该跳过时把时间戳记上。
// 检查与登记必须在同一把锁里做：并发重试同一篇时，两只请求不能各自看到「没扣过」。
func alreadyDeducted(c *gin.Context, userID uint, source, action string) bool {
	window := deductWindow()
	if window <= 0 || userID == 0 {
		return false
	}
	ident := deductIdent(subjectOf(c))
	if ident == "" {
		return false
	}
	key := deductKey{userID, source, strings.ToLower(action), ident}

	startDedupeJanitor()

	now := time.Now()
	deductMu.Lock()
	defer deductMu.Unlock()
	if at, ok := deductAt[key]; ok && now.Sub(at) < window {
		deductSkip.Add(1)
		return true
	}
	deductAt[key] = now
	return false
}

// subjectOf 取本次调用的内容维度载体；没挂上或类型不对就当没有（不去重）。
func subjectOf(c *gin.Context) *base.CallSubject {
	raw, exists := c.Get(middleware.CtxCallSubject)
	if !exists {
		return nil
	}
	subj, _ := raw.(*base.CallSubject)
	return subj
}

// deductIdent 用「书名标识 + 章节标识 + 搜索词」拼出内容身份；全空返回空串表示无法判同。
func deductIdent(subj *base.CallSubject) string {
	if subj == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, p := range []string{subj.BookKey, subj.ChapterKey, subj.Keyword} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	// 分隔符用不可打印字符：标识本身可能含冒号与斜杠（章节 URL 形态）
	return strings.Join(parts, "\x1f")
}

// startDedupeJanitor 懒启动回收协程（每进程一次），清掉超过窗口的旧键。
//
// **窗口在协程启动时取一次快照，循环里不再读 `conf.Config`。**
// 这不是为了省事：`conf.Config` 在生产里由 `conf.Load()` 写一次就不再动，
// 而这个协程**跨整个进程存活**——它每 tick 去读那个全局指针，
// 在集成测试里就与"下一条用例替换 conf.Config"构成一次真数据竞争
// （-race 下整包失败，症状是毫不相干的一条用例报 `race detected`）。
// 快照不改变行为：回收阈值只决定"键别无限攒着"，不需要跟着配置同步；
// 真要改冷却窗口是改环境变量，而那必然伴随一次重启。
func startDedupeJanitor() {
	deductJanitorOnce.Do(func() {
		window := deductWindow()
		idle := window * 2
		if window <= 0 || idle > deductIdleTTL {
			idle = deductIdleTTL
		}
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				base.Supervised("扣减时间去重表的一轮清理", func() {
					now := time.Now()
					// 循环体里就地写 defer 会等协程退出才解锁（下一次 tick 就抢不到锁了），
					// 所以这一段包闭包——尾解锁的问题是 panic 会把锁永久留在手里（待办清单 P93）
					func() {
						deductMu.Lock()
						defer deductMu.Unlock()
						for k, at := range deductAt {
							if now.Sub(at) > idle {
								delete(deductAt, k)
							}
						}
					}()
				})
			}
		}()
	})
}

// DedupeStats 给自省与管理端用：累计被冷却挡掉的扣减次数、当前在册键数、当前窗口秒数。
func DedupeStats() (skipped int64, keys int, windowSec int) {
	deductMu.Lock()
	defer deductMu.Unlock()
	return deductSkip.Load(), len(deductAt), conf.Config.BillingDedupeSec
}
