package test

// 待办清单 P63：0 在有的键上是哨兵，不是笔误——所以要有一条"允许 0"的整数读取口。
//
// 这条不是假想需求：携带形态里 uxx 的广告冷却标定把 `uxx_ad_cool_hi_sec = 0` 当"还没有上界证据"
// （判据写的就是 `hi == 0`），而冷却本身可以被管理员设成 0 表示"不设冷却"。
// 用 `SettingInt` 读它们的症状是「我明明填了 0，它却每次等三小时」——分支侧记在 S50。
//
// 三段各钉一件事：**0 静默生效**（这是两条函数唯一的分界）、**负数仍然出声**
// （允许 0 不等于放开取值域）、以及**对照**——同一个 0 在 `SettingInt` 那边必须仍然回默认，
// 否则哪天有人"顺手把两条统一掉"，那条哨兵语义就会在一次看起来无害的重构里消失。

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/models"
)

const (
	nonNegKey   = "test_zero_is_a_value_sec"
	positiveKey = "test_zero_is_a_mistake_sec"
)

func writeZeroTestSetting(t *testing.T, key, value string) {
	t.Helper()
	res := db.DB.Model(&models.SystemSetting{}).
		Where(map[string]interface{}{"key": key}).Update("value", value)
	if res.Error != nil {
		t.Fatalf("改 %s 失败: %v", key, res.Error)
	}
	if res.RowsAffected == 0 {
		if err := db.DB.Create(&models.SystemSetting{Key: key, Value: value, Type: "number"}).Error; err != nil {
			t.Fatalf("补 %s 失败: %v", key, err)
		}
	}
	db.InvalidateSettingCache(key)
}

// readStoredSetting 把库里的存值读回来。**0 是有业务含义的值**，所以写过之后必须读回一次——
// 判据来自踩坑判据「代码里写的 0 落到库里变成 1」（GORM 对带 default 标签的零值字段直接省略该列）
// 那一条的「0 有业务含义的列，用例必须 Create 后读回来」。
func readStoredSetting(t *testing.T, key string) string {
	t.Helper()
	var s models.SystemSetting
	if err := db.DB.Where(map[string]interface{}{"key": key}).First(&s).Error; err != nil {
		t.Fatalf("读回 %s 失败: %v", key, err)
	}
	return s.Value
}

func TestSettingIntNonNegAllowsZero(t *testing.T) {
	newTestServer(t)

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	spoken := func(key string) int { return strings.Count(buf.String(), key+" 填的值没生效") }

	// 1) 0 是合法值：按 0 走，且**不出声**（出声就意味着又被当成"没配"）
	writeZeroTestSetting(t, nonNegKey, "0")
	if stored := readStoredSetting(t, nonNegKey); stored != "0" {
		t.Fatalf("样本没落库（库里是 %q）——后面所有关于 0 的断言都会空转", stored)
	}
	before := spoken(nonNegKey)
	if got := db.SettingIntNonNeg(nonNegKey, 10800); got != 0 {
		t.Errorf("填 0 的生效值 = %d, want 0（回默认就是把哨兵当笔误）", got)
	}
	if n := spoken(nonNegKey) - before; n != 0 {
		t.Errorf("填 0 出声 %d 条, want 0（合法值不该喊）", n)
	}

	// 2) 正数照常静默生效
	writeZeroTestSetting(t, nonNegKey, "42")
	before = spoken(nonNegKey)
	if got := db.SettingIntNonNeg(nonNegKey, 10800); got != 42 {
		t.Errorf("填 42 的生效值 = %d, want 42", got)
	}
	if n := spoken(nonNegKey) - before; n != 0 {
		t.Errorf("填 42 出声 %d 条, want 0", n)
	}

	// 3) 负数**仍然**不是合法值：允许 0 不等于放开取值域
	writeZeroTestSetting(t, nonNegKey, "-1")
	before = spoken(nonNegKey)
	if got := db.SettingIntNonNeg(nonNegKey, 10800); got != 10800 {
		t.Errorf("填 -1 的生效值 = %d, want 默认 10800", got)
	}
	if n := spoken(nonNegKey) - before; n != 1 {
		t.Errorf("填 -1 出声 %d 条, want 1", n)
	}

	// 4) 非整数与空值照旧出声（两条函数共用一份实现，这三态不该有差异）
	writeZeroTestSetting(t, nonNegKey, "三小时")
	before = spoken(nonNegKey)
	if got := db.SettingIntNonNeg(nonNegKey, 10800); got != 10800 {
		t.Errorf("填「三小时」的生效值 = %d, want 默认 10800", got)
	}
	if n := spoken(nonNegKey) - before; n != 1 {
		t.Errorf("填「三小时」出声 %d 条, want 1", n)
	}
	if err := db.DB.Model(&models.SystemSetting{}).
		Where(map[string]interface{}{"key": nonNegKey}).Update("value", "").Error; err != nil {
		t.Fatalf("清空 %s 失败: %v", nonNegKey, err)
	}
	db.InvalidateSettingCache(nonNegKey)
	before = spoken(nonNegKey)
	if got := db.SettingIntNonNeg(nonNegKey, 10800); got != 10800 {
		t.Errorf("值为空的生效值 = %d, want 默认 10800", got)
	}
	if n := spoken(nonNegKey) - before; n != 1 {
		t.Errorf("值为空出声 %d 条, want 1", n)
	}

	// 5) 对照：同一个 0 在 `SettingInt` 那一侧**必须**仍按默认（两条函数的分界就在这儿）
	writeZeroTestSetting(t, positiveKey, "0")
	if stored := readStoredSetting(t, positiveKey); stored != "0" {
		t.Fatalf("对照样本没写进去（库里是 %q）——那这一步是在数空气", stored)
	}
	beforePos := spoken(positiveKey)
	if got := db.SettingInt(positiveKey, 5); got != 5 {
		t.Errorf("SettingInt 读到 0 的生效值 = %d, want 默认 5（两条函数的分界被抹掉了？）", got)
	}
	if n := spoken(positiveKey) - beforePos; n != 1 {
		t.Errorf("SettingInt 读到 0 出声 %d 条, want 1", n)
	}
}
