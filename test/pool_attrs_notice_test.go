package test

// 号池 `attrs` 解不开时要出声（待办清单 P59）。
//
// 这个形状的后果是「号在库里但凭证为空」，而下游症状是 Provider 拿不到号或拿到的号不能用——
// 查的人第一站绝不会想到是那一列。修前 `decodeAttrs` 把 `json.Unmarshal` 的错整个丢掉，
// 于是**库里明明有一条坏行，系统与日志都装作没看见**。
//
// 三条断言各钉一件事：会喊（不是空转）、**同一行只喊一次**（这列被反复读：领取、刷新、巡检都读它）、
// 以及**日志里没有原始标识**（那一列本身就是上游凭证，脱敏不是可选项）。

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"loomproxy/base/pool"
	"loomproxy/db"
	"loomproxy/models"
)

func TestPoolBadAttrsSpeaks(t *testing.T) {
	newTestServer(t)

	fp := newFakeProvider()
	fp.poolName = "attrs_watch"
	cfg := fakePoolConfig()
	cfg.ColdSpares = 1
	p := newFakePool(t, pool.New(fp, cfg))
	name := p.Name()

	// 一条 attrs 坏掉的行（形态像"被人手填过"或"文本被截断"）
	brokenIdent := "very-long-device-ident-0123456789"
	plainIdent := "plain-device-ident-0002"
	if err := db.DB.Create(&models.PoolDevice{
		Pool: name, Ident: brokenIdent, Status: pool.StatusCold, Attrs: `{"sn":"`,
	}).Error; err != nil {
		t.Fatalf("预置坏 attrs 的号失败: %v", err)
	}
	// 一条 attrs 为空的行：那是"本来就没附加凭证"，不该被喊出来
	if err := db.DB.Create(&models.PoolDevice{
		Pool: name, Ident: plainIdent, Status: pool.StatusCold,
	}).Error; err != nil {
		t.Fatalf("预置空 attrs 的号失败: %v", err)
	}

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	spoken := func() int { return strings.Count(buf.String(), "attrs 解不开") }

	// **通路**：`Reauthorize` 一定走 `refresh → deviceOf`，读的就是这一列。
	// （不用 `Start`/`Acquire` 触发：那两条要不要读到这行取决于转正了哪条冷备，
	//  拿它们做"重复读不再喊"的断言就是一条空断言——本轮变异 M2 就是这么抓出来的。）
	before := spoken()
	if _, err := p.Reauthorize(&pool.Device{Ident: brokenIdent}); err == nil {
		t.Log("Reauthorize 返回了错（不影响本用例：只要它读过那一行）")
	}
	if n := spoken() - before; n != 1 {
		t.Fatalf("首次读坏 attrs 的出声 = %d 条, want 1（0 条说明通路没接上，后面的断言全是空转）", n)
	}

	// 只验**自己那一行**：号池别的诊断日志本来就打原始标识，那是既有先例、不归这条用例管；
	// 拿整段缓冲去扫会把别人的行算到我头上（本轮第一版就这么红过一次）
	noticeLine := ""
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "attrs 解不开") {
			noticeLine = l
		}
	}
	if !strings.Contains(noticeLine, "attrs_watch") {
		t.Errorf("出声里没说是哪个池：%q", noticeLine)
	}
	if strings.Contains(noticeLine, brokenIdent) {
		t.Errorf("这条日志里出现了原始标识 %q——那一列本身就是上游凭证，新写的日志不该把它带出来", brokenIdent)
	}
	if !strings.Contains(noticeLine, "***") {
		t.Errorf("标识没走脱敏（应含 ***）：%q", noticeLine)
	}

	// 同一行再读两次：只喊一次（这一列被反复读：刷新、领取、巡检都会读它）
	before = spoken()
	_, _ = p.Reauthorize(&pool.Device{Ident: brokenIdent})
	_, _ = p.Reauthorize(&pool.Device{Ident: brokenIdent})
	if n := spoken() - before; n != 0 {
		t.Errorf("同一行重复读又出声 %d 条, want 0（去重没做成，而这列每轮巡检都读）", n)
	}

	// 空 attrs 是合法形态（那个号就是没附加凭证），走同一条通路读它，不该出声
	before = spoken()
	_, _ = p.Reauthorize(&pool.Device{Ident: plainIdent})
	if n := spoken() - before; n != 0 {
		t.Errorf("空 attrs 的号被点名 %d 条, want 0（喊它就是给合法形态造噪声）", n)
	}
}
