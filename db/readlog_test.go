package db

// P99 第一步的出声形状：同一 (落点, 表) 只喊一次（P59/P36 那族判据——读在热路径上，
// 每请求一条 ERROR 会把真正该看的读数泡坏）。这条钉住去重本身：
// 两次同键只落一行、第二次的原文不出现、不同键各落一行。变异：摘掉 LoadOrStore 去重 → 三行全出，用例红。

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestLogReadFailDedupePerKey(t *testing.T) {
	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) }()

	LogReadFail("t:x", errors.New("boom"))
	LogReadFail("t:x", errors.New("boom-again"))
	LogReadFail("t:y", errors.New("boom"))

	out := buf.String()
	if got := strings.Count(out, "读库失败"); got != 2 {
		t.Fatalf("两条不同键各落一行，实得 %d 行：%q", got, out)
	}
	if strings.Contains(out, "boom-again") {
		t.Errorf("同键第二次的原文不该再出现（去重失效）：%q", out)
	}
	if !strings.Contains(out, "t:x") || !strings.Contains(out, "t:y") {
		t.Errorf("键要进日志（落点:表 是排障的钥匙）：%q", out)
	}
}
