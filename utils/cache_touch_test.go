package utils

// Touch 的内存那一段（Redis 段的 EXPIRE 语义同判据，适配器在 app 侧另有通路用例）。
// 判据两条：**不新建**（写入口只有一份判定）与**续的是命**（到期时刻从最后一次使用起算）。

import (
	"testing"
	"time"
)

func TestTouchNeverCreatesInMemory(t *testing.T) {
	c := NewCache(16, time.Minute)
	if c.Touch("absent", time.Second) {
		t.Error("Touch 对不存在的键返回了 true")
	}
	if _, ok := c.Get("absent"); ok {
		t.Error("Touch 把不存在的键**建出来**了——写入口只该有一份")
	}
	if c.Touch("absent", 0) {
		t.Error("ttl<=0 的 Touch 不该成功")
	}
}

func TestTouchSlidesExpiry(t *testing.T) {
	c := NewCache(16, time.Minute)
	c.SetTTL("k", "v", 40*time.Millisecond)
	// 每 20ms 续一次 40ms：100ms 后这格仍在——固定 TTL 早就死了，这条就是"滑窗"的证。
	for i := 0; i < 6; i++ {
		time.Sleep(20 * time.Millisecond)
		if !c.Touch("k", 40*time.Millisecond) {
			t.Fatalf("第 %d 轮续期失败（格不该在续期途中消失）", i)
		}
	}
	if _, ok := c.Get("k"); !ok {
		t.Error("持续被 Touch 的格子仍按写入时刻到期了——滑窗没生效")
	}
	// 放手让它到期的那半：不续了就要真的会走。
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Error("停止续期后 80ms（> 最后一续的 40ms）这格还活着——TTL 被续成了永不过期")
	}
}
