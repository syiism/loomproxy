package test

// 待办清单 P109 的固化面：会话钩子 `scripts/check-gate-run-shape.sh` 的判据表。
//
// 为什么一条「会话里的命令怎么跑」要用例来钉：P120 刚量过一次同族形状——
// 一道 defence 写在文档里、又**不在 make build 的检查链上**，于是坏在三周没人知道（viewport-probe）。
// 这条钩子一旦静默失效（改坏、路径挪走、`--cmd` 参数名换掉），症状是"我又开始把红门禁读成绿门禁"，
// 而那恰好是最不可能被当成工具故障来查的症状。所以它必须进门禁，而不是只活在一个 JSON 片段里。
//
// 钉三件事：① 该拦的形状都拦住（含 `cd x && make build | tail` 这种管道比 && 结合得紧的形态）；
// ② 合法跑法一条不误伤（钩子挂在每条 Bash 前，误伤就是替维护者把会话卡住）；
// ③ stdout 保持干净——钩子按 JSON 解析 stdout，把诊断写进 stdout 会让每一次放行变成解析错误。

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func gateScript(t *testing.T) string {
	t.Helper()
	// 用例的工作目录是 test/，脚本在仓库根的 scripts/ 下；用 runtime 定位而不是假设相对路径能穿过 -race
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 拿不到本文件路径")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "scripts", "check-gate-run-shape.sh")
}

func runGate(t *testing.T, script, cmd string) (int, string) {
	t.Helper()
	out, err := exec.Command("bash", script, "--cmd", cmd).Output()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("跑钩子脚本失败: %v", err)
	}
	return code, string(out)
}

func TestGateRunShape_BlocksSwallowedExitCode(t *testing.T) {
	bad := []string{
		"make build 2>&1 | tail -25",
		"make build | tee /tmp/gate.log",
		"cd /x && make vet 2>&1 | head -5",
		"make -C web build 2>&1 | tail",
		"printf x; make build | tail",
		"DEPLOY_BUILD_EXIT=0; cd /y && make build 2>&1 | tail -3",
		"cd /x\nmake build | tail -5\n",
	}
	for _, cmd := range bad {
		code, out := runGate(t, gateScript(t), cmd)
		if code != 2 {
			t.Errorf("该拦没拦（期望 exit 2，实际 %d）: %q", code, cmd)
		}
		if out != "" {
			t.Errorf("stdout 必须干净（钩子按 JSON 解析它），收到 %q", out)
		}
	}
}

func TestGateRunShape_AllowsLegitShapes(t *testing.T) {
	good := []string{
		`make build > /tmp/gate.log 2>&1; echo "BUILD_EXIT=$?" >> /tmp/gate.log`,
		`cd /repo && make build > /tmp/gate.log 2>&1; echo "BUILD_EXIT=$?" >> /tmp/gate.log`,
		"bash -c \"set -o pipefail; make build | tail -3\"",
		"make build | tail -3; echo ${PIPESTATUS[0]}",
		"make build",
		`grep -rn "make build" Makefile`,
		`echo "make build | tail" > /tmp/x.md`,
		"git log --oneline | head -5",
		"go test ./... 2>&1 | tail -5",
		"make build > /tmp/g.log 2>&1; tail -5 /tmp/g.log | wc -l",
		"grep -E \"a|b\" Makefile | wc -l",
		"make build > /tmp/gate.log 2>&1\ntail -1 /tmp/gate.log | wc -l\n",
	}
	for _, cmd := range good {
		code, _ := runGate(t, gateScript(t), cmd)
		if code != 0 {
			t.Errorf("合法跑法被拦（期望 0，实际 %d）: %q", code, cmd)
		}
	}
}

// 钩子的真实喂法：整段事件 JSON 从 stdin 进来。这一条钉的是「接线形状」——
// 判据本身对的时候，如果 stdin 解析那一段坏了，钩子会一路放行而毫无症状。
func TestGateRunShape_HookStdinShape(t *testing.T) {
	script := gateScript(t)
	cases := []struct {
		json string
		want int
	}{
		{`{"tool_name":"Bash","tool_input":{"command":"make build 2>&1 | tail -25"}}`, 2},
		{`{"tool_name":"Bash","tool_input":{"command":"make build > /tmp/g.log 2>&1; echo BUILD_EXIT=$? >> /tmp/g.log"}}`, 0},
		{`{"tool_name":"Edit","tool_input":{"file_path":"a.go"}}`, 0},
		{`{}`, 0},
	}
	for _, c := range cases {
		cmd := exec.Command("bash", script)
		cmd.Stdin = strings.NewReader(c.json)
		out, err := cmd.Output()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("跑钩子脚本失败: %v", err)
		}
		if code != c.want {
			t.Errorf("stdin 形态判成 %d，期望 %d：%s", code, c.want, c.json)
		}
		if len(out) > 0 && c.want == 0 {
			t.Errorf("放行时 stdout 必须为空，收到 %q", string(out))
		}
	}
}
