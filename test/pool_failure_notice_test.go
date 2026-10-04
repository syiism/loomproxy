package test

// 维护协程每一 tick 都会重试临期号，所以「持续性但不是故障」的失败（上游当天到顶、正在维护、网络一时不通）
// 会变成每 60 秒一条日志——待办清单 P36 为 `ErrCapacityReached` 单独治过一次（生产 v62 上线后
// qm_device 每 60 秒一条 error，把「这个池有问题」的读数泡坏）。P76 把同一道守卫铺到维护循环的三个出口。
//
// 用例走「台账刷新失败」这一支：假 Provider 的 failRefresh 返回**固定文案**，正好代表"同一种失败"。

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"loomproxy/base/pool"
)

// capturePoolLog 把标准日志接进缓冲区；返回按子数数的函数与整段文本的读法。
func capturePoolLog(t *testing.T) (spoken func(string) int, text func() string) {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return func(sub string) int { return strings.Count(buf.String(), sub) },
		func() string { return buf.String() }
}

// bringHotUp 建一个只有一个号的燃烧型池并启动，返回池与那台号的 ident。
func bringHotUp(t *testing.T, fp *fakeProvider, ident string) *pool.Pool {
	t.Helper()
	insertDevice(t, ident, pool.StatusCold, 0, fakeQuotaMax, nil)
	fp.quota[ident] = pool.Quota{Total: fakeQuotaMax}
	p := newFakePool(t, pool.New(fp, fakePoolConfig()))
	p.Start()
	if got := deviceStatus(t, ident); got != pool.StatusHot {
		t.Fatalf("首个活跃号 = %q, want hot——前提不成立，后面的 Maintain 什么都验不了", got)
	}
	return p
}

func TestPoolRenewFailureSpeaksOnce(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	fp.claimExtend = time.Minute // 转正后即临期，每轮 Maintain 都会去刷新台账

	const ident = "abababab-0000-0000-0000-0000000000f1"
	p := bringHotUp(t, fp, ident)

	spoken, text := capturePoolLog(t)
	fp.failRefresh[ident] = true
	for i := 0; i < 4; i++ {
		p.Maintain()
	}
	if n := spoken("refresh hot device"); n != 1 {
		t.Errorf("连续 4 轮同样的失败喊了 %d 条, want 1——每 tick 一条就是 P36 那种把读数泡坏的噪音", n)
	}
	if n := spoken("此后只累计次数"); n != 1 {
		t.Errorf("「只说一次」那句说明出现 %d 次, want 1——缺了它，读日志的人不知道后面不会再喊", n)
	}
	if n := spoken("模拟上游故障"); n != 1 {
		t.Errorf("失败文案本身出现 %d 次, want 1（同一种失败重复喊 = 守卫没生效）", n)
	}

	delete(fp.failRefresh, ident)
	fp.quota[ident] = pool.Quota{Total: fakeQuotaMax, ExpiresAt: time.Now().Add(time.Hour)}
	p.Maintain()
	if n := spoken("台账已恢复"); n != 1 {
		t.Errorf("恢复那一条出现 %d 次, want 1", n)
	}
	if !strings.Contains(text(), "此前连续失败 4 次") {
		t.Errorf("恢复条没带上累计次数（应是 4 次）——只说「又好了」会让人以为刚才只错了一次\n日志：%s", text())
	}
}

func TestPoolRenewFailureRespeaksWhenMessageChanges(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	fp.claimExtend = time.Minute

	const ident = "cdcdcdcd-0000-0000-0000-0000000000f2"
	p := bringHotUp(t, fp, ident)

	spoken, _ := capturePoolLog(t)
	// 同一个键、两种不同的错：文案变了就算状态变化，必须再说一次
	// （否则真换了一种故障会被上一句的沉默吞掉）
	fp.failRefresh[ident] = true
	p.Maintain()
	delete(fp.failRefresh, ident)
	delete(fp.quota, ident) // 换成「未预期的号」这条错
	p.Maintain()
	if n := spoken("refresh hot device"); n != 2 {
		t.Errorf("换了错误文案后喊了 %d 条, want 2（同一种只喊一次，换一种必须再喊）", n)
	}
}
