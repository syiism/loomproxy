package utils

import (
	"testing"
	"time"

	"loomproxy/conf"
)

// PlatformZone 必须在 conf.Config == nil（conf.Load 之前的纯函数调用面）时也不 panic，
// 且按默认 +8 兜底——分支侧 S52 收口时，uxx 的 ParseLedger 单测就是这么炸的。
func TestPlatformZoneSafeWithoutConf(t *testing.T) {
	prev := conf.Config
	conf.Config = nil
	t.Cleanup(func() { conf.Config = prev })

	loc := PlatformZone() // 修复前：nil 指针 panic
	if _, off := time.Now().In(loc).Zone(); off != 8*3600 {
		t.Fatalf("conf 缺席时的偏移 = %d 秒, want +8h", off)
	}
}
