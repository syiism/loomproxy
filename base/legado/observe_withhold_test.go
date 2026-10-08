package legado

import (
	"testing"

	"loomproxy/base"
)

// P113② 钉住：用户关掉 content_consent（subj.ContentWithheld=true）时，
// ObserveCall 抽出来的书名/章节名**不写命名缓存**（内存 + Redis SubjectStore）。
// monitor 在 c.Next() 之后 WithholdContent 抹的是明细字段，够不到这条写缓存路径——
// 这一格是第五面（合规口径：关掉后新调用不再捕获这些维度）。
func TestObserveCallSkipsNameCacheWhenWithheld(t *testing.T) {
	base.ResetNameCaches()
	const src = "withheld_test_src"

	// 第一轮：withheld=true，同一次 ObserveCall 抽到的书名必须不进缓存
	withheld := &base.CallSubject{ContentWithheld: true}
	ObserveCall(src, map[string]interface{}{"key": "被 withhold 的搜索词"},
		SearchResponse{BookList: []BookItem{{BookId: "bid_wh", Name: "不应入缓存的书"}}}, withheld)

	if got, _ := base.LookupBook(src, "bid_wh"); got != "" {
		t.Fatalf("withheld 时命名缓存被写入：LookupBook 命中 %q，应为空", got)
	}

	// 第二轮：对照 withheld=false，同名流程必须能写进缓存——证明断言不空转
	consented := &base.CallSubject{}
	ObserveCall(src, map[string]interface{}{"key": "正常搜索"},
		SearchResponse{BookList: []BookItem{{BookId: "bid_ok", Name: "应入缓存的书"}}}, consented)

	if got, _ := base.LookupBook(src, "bid_ok"); got != "应入缓存的书" {
		t.Fatalf("consented 时命名缓存未写入：LookupBook=%q，期望 %q", got, "应入缓存的书")
	}

	// 第三轮：withheld=true 时章节名同样不写
	whCh := &base.CallSubject{ContentWithheld: true, BookKey: "bid_wh"}
	ObserveCall(src, map[string]interface{}{},
		ChapterResponse{ChapterList: []ChapterItem{{ItemId: "cid_wh", Title: "不应入缓存的章"}}}, whCh)
	if got := base.LookupChapter(src, "bid_wh", "cid_wh"); got != "" {
		t.Fatalf("withheld 时章节名进缓存：%q", got)
	}
}
