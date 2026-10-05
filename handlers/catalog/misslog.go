package catalog

// 静态字典缺位的**请求期**读数（待办清单 P51① 的另一半）。
//
// 启动核对只能看见「各源声明过」的字典在不在位；而生产上真正打不中的那些路径
// 多数**没有任何源声明**（14 天里约 1170 次，全是旧导入客户端在要已经不存在的番茄字典），
// 它们在启动那一刻天生不在名单里，只有人来要的时候才显形。这一条就是补那个盲区。
//
// 三条刻意的取舍：
//  1. **不改响应**——状态码与正文照旧。缺文件到底该回 200 还是 404 是下游可见变更，
//     维护者还没拍（P51②），所以这一版只让服务端自己看得见，不碰对外行为。
//  2. **必须节流**——`/data/...` 是免鉴权端点，一次不节流的出声就是一个免费的刷日志入口
//     （判据同 `utils.touchApiKeyLastUsed` 的 5 分钟节流与号池 `ErrCapacityReached` 的「状态变化才出声」）。
//  3. **不持有无界状态**——节流靠"上一次出事的时刻"而不是"见过哪些路径"的表，
//     窗口内点名前 8 条路径，多的只报个数；喷不同的路径既刷不了日志也撑不爆内存。

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"loomproxy/base"
)

// missNameSample 一条聚合里最多逐条点出几条路径；剩下的只报条数。
const missNameSample = 8

// MissWatcher 累计「有人来要一个不在位的静态字典」，并按时间窗口出一条日志。
// 零值不可用，用 NewMissWatcher 构造。
type MissWatcher struct {
	window time.Duration

	total    atomic.Int64
	declared atomic.Int64 // 有源声明、但产物不在位（= 部署缺产物）
	unknown  atomic.Int64 // 没有任何源声明它（= 有人在要一个不存在的东西）

	mu    sync.Mutex
	last  time.Time
	names []string
}

// NewMissWatcher 构造一个读数器。窗口是给用例留的（生产用 `time.Minute`），
// 不是配置项——它只决定"多久说一次"，不改变任何对外行为。
func NewMissWatcher(window time.Duration) *MissWatcher {
	return &MissWatcher{window: window}
}

// Note 记一次缺位。declared 由 `base.DeclaresDataFile` 决定，两类分开数：
// 「产物没装」与「没人声明过却有人来要」是两件不同的运维事实，合成一个数就没法分开处置。
func (w *MissWatcher) Note(source, stem string) {
	declared := base.DeclaresDataFile(stem)
	w.total.Add(1)
	if declared {
		w.declared.Add(1)
	} else {
		w.unknown.Add(1)
	}

	now := time.Now()
	// 登记 + 取本窗口样本是一小段临界区，包进闭包用 defer 解锁；
	// 那条 ERROR 留在锁外打（它里面还会读原子计数、拼字符串，持锁跑这些没意义）。
	// 原来这里是两处尾解锁（一支在未到期时 `Unlock(); return`）——中间 panic 就把 `w.mu` 永久留在手里（待办清单 P93）
	sample, n, due := func() ([]string, int, bool) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.names = append(w.names, fmt.Sprintf("%s/%s.json(声明=%v)", source, stem, declared))
		if !w.last.IsZero() && now.Sub(w.last) < w.window {
			return nil, 0, false
		}
		s := w.names
		if len(s) > missNameSample {
			s = s[:missNameSample]
		}
		cnt := len(w.names)
		w.names = nil
		w.last = now
		return s, cnt, true
	}()
	if !due {
		return
	}

	log.Printf("ERROR: 静态字典缺位：累计 %d 次（有源声明=%d，通常是部署缺产物；无声明=%d，通常是旧客户端在要已下架的字典）；本窗口的缺位样例 %d 条：\n  %s%s",
		w.total.Load(), w.declared.Load(), w.unknown.Load(), n,
		strings.Join(sample, "\n  "),
		func() string {
			if len(sample) < missNameSample {
				return ""
			}
			return fmt.Sprintf("\n  …（本窗口还有 %d 条未逐条点名）", n-missNameSample)
		}())
}

// Totals 取这个读数器的累计：总缺位数、其中「有源声明」（部署缺产物）与「无声明」（有人在要不存在的东西）。
// 两类分开数才有处置价值：前者是"补一次产物"，后者是"那批客户端该退了"。
func (w *MissWatcher) Totals() (total, declared, unknown int64) {
	return w.total.Load(), w.declared.Load(), w.unknown.Load()
}

// dataFileMisses 是这一个进程共享的读数器：进程级就够，缺位是部署事实而不是用户维度的事。
var dataFileMisses = NewMissWatcher(time.Minute)

// DataFileMissTotals 取进程级读数（缺位进监控页的话就是这三个数，不要长第二份计数器——**这里是那一份**）。
// 用例也用它验「请求确实被记下来了」：日志那条通路是节流的，按增量断言会不稳定。
func DataFileMissTotals() (total, declared, unknown int64) {
	return dataFileMisses.Totals()
}
