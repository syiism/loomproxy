package gate

// 成因→文案那张表的包内用例（它为什么在 gate 里而不是某个 handler 里，见 TransferErrorStatus 的注释）。

import (
	"errors"
	"testing"
)

func TestTransferErrorStatusCoversEveryCause(t *testing.T) {
	causes := []error{
		ErrTransferAmount, ErrTransferSame, ErrTransferSource,
		ErrTransferUnlimited, ErrTransferNoGrant, ErrTransferInsufficient,
	}
	seen := map[string]string{}
	for _, e := range causes {
		status, msg := TransferErrorStatus(e)
		if status != 400 {
			t.Errorf("%v 的状态码 = %d, want 400（这些都是使用方写错参数，不是服务端故障）", e, status)
		}
		if msg == "" || msg == "转移失败" {
			t.Errorf("%v 落到了 default 那一格——新增成因忘了配文案，就会把内部错误原文发给用户", e)
		}
		if prev, dup := seen[msg]; dup {
			t.Errorf("两个成因共用一句话：%v 与 %v → %q（用户看不出自己错在哪一条）", prev, e, msg)
		}
		seen[msg] = e.Error()
	}
	// 包装过一层也要认（handler 拿到的是 tx 里 return 出来的错误，可能被 fmt.Errorf("%w") 带过）
	if status, _ := TransferErrorStatus(errors.New("外层: " + ErrTransferSame.Error())); status != 500 {
		t.Errorf("纯字符串拼接的错误不该被当成已知成因（状态码 %d）", status)
	}
	if _, ok := transferPlan(100, 0, 50, 0, 101); ok {
		t.Error("超额转出没被拒（transferPlan 的可转量上限失效）")
	}
}
