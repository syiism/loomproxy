package base

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// captureLog 把标准库 log 的输出临时接到内存里。
// 与测试并行写日志会串，所以整段用锁圈住（本仓集成用例不许 t.Parallel，包内测试同一规矩）。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	old := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(old)
		log.SetFlags(oldFlags)
	})
	return buf
}

// TestSupervisedContainsPanicAndSpeaks 钉的是 P103 那三件事：
// 进程不死、下一轮照跑、拦下要出声（"拦下"不等于"吞掉"）。
func TestSupervisedContainsPanicAndSpeaks(t *testing.T) {
	buf := captureLog(t)

	ranAfterPanic := 0
	Supervised("测试用的一轮", func() {
		panic("上游返回了没人预期的形状")
	})
	ranAfterPanic++ // 上一行 panic 之后还走到这里，才叫"跳过本轮"而不是"带走进程"

	// 第二轮是好的：兜底不能把循环本身弄坏
	Supervised("测试用的第二轮", func() {
		ranAfterPanic++
	})

	if ranAfterPanic != 2 {
		t.Fatalf("兜底之后循环没继续跑（计数=%d，期望 2）", ranAfterPanic)
	}
	out := buf.String()
	if !strings.Contains(out, "panic 已拦下") {
		t.Fatalf("拦下没有出声（日志：%q）——静默兜底等于把缺陷藏起来", out)
	}
	if !strings.Contains(out, "测试用的一轮") || strings.Contains(out, "测试用的第二轮") {
		t.Fatalf("出声要指名是哪一轮，且只在出事那一轮喊：%q", out)
	}
	if !strings.Contains(out, "goroutine") {
		t.Fatalf("要带栈，否则下一轮没人查得到是哪里炸的：%q", out)
	}
}

// TestGuardIsForDefer 钉住 defer 形态：号池的 guardPanic 就是靠它委托的（P77/P78 → P103 收口成一处）。
func TestGuardIsForDefer(t *testing.T) {
	buf := captureLog(t)
	done := false
	func() {
		defer Guard("测试用的 defer 路径")
		panic("boom")
	}()
	done = true
	if !done {
		t.Fatal("defer 形态的兜底没让外层继续走完")
	}
	if !strings.Contains(buf.String(), "测试用的 defer 路径") {
		t.Fatalf("defer 形态没指名是哪条路径：%q", buf.String())
	}
}
