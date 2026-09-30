package test

// 内容维度与媒介判定的单元测试（base/legado.ObserveCall + base 的命名缓存）。
//
// 命名缓存与假源声明都是进程级共享态，用例之间禁止并行，并在开头清缓存。

import (
	"strings"
	"testing"

	"loomproxy/base"
	"loomproxy/base/legado"
)

func observeOne(t *testing.T, source string, params map[string]interface{}, result interface{}) *base.CallSubject {
	t.Helper()
	base.ResetNameCaches()
	subj := &base.CallSubject{}
	legado.ObserveCall(source, params, result, subj)
	return subj
}

func TestObserveCallFromTypedDTO(t *testing.T) {
	s := observeOne(t, "fake_a", map[string]interface{}{"key": "剑来"},
		legado.SearchResponse{BookList: []legado.BookItem{
			{BookId: "b1", Name: "剑来"},
			{BookId: "b2", Name: "剑来 番外"},
		}})
	if s.Keyword != "剑来" || s.ResultCount != 2 {
		t.Fatalf("搜索维度 = keyword %q / count %d，期望 剑来 / 2", s.Keyword, s.ResultCount)
	}
	// 搜索结果的书名应灌进缓存，供后续只带标识的请求反查
	if name, _ := base.LookupBook("fake_a", "b1"); name != "剑来" {
		t.Errorf("书名缓存未灌入：LookupBook(fake_a, b1) = %q", name)
	}
}

func TestObserveCallMediaFromDetailAndContent(t *testing.T) {
	// 详情的 bookType 写法优先于类型码（DTO 的码是 int，零值 0 = 小说，
	// 不能被当成「源声明了小说」覆盖明确的「听书」写法）
	s := observeOne(t, "fake_a", map[string]interface{}{"bookId": "b9"},
		legado.BookDetail{Name: "某有声书", BookId: "b9", BookType: "听书", BookTypeCode: 0})
	if s.Media != base.MediaAudio {
		t.Errorf("详情媒介 = %q，期望 %s", s.Media, base.MediaAudio)
	}

	// 正文：Legado 规范词与本仓写法都要认
	for ct, want := range map[string]string{
		"audio": base.MediaAudio, "video": base.MediaVideo,
		"manga": base.MediaComic, "text": base.MediaNovel,
	} {
		s := observeOne(t, "fake_a", map[string]interface{}{"itemId": "c1"},
			legado.ContentResponse{ContentType: ct, Data: map[string]interface{}{"content": "正文"}})
		if s.Media != want {
			t.Errorf("contentType %q → 媒介 %q，期望 %q", ct, s.Media, want)
		}
		if s.ResultCount != 1 {
			t.Errorf("contentType %q 的正文非空，result_count 应为 1，实为 %d", ct, s.ResultCount)
		}
	}
	// error 与空正文不得伪装成任何一种媒介
	s = observeOne(t, "fake_a", nil, legado.ContentResponse{ContentType: "error"})
	if s.Media != "" {
		t.Errorf("ContentType=error 应为未判定（空串），实为 %q", s.Media)
	}
}

func TestObserveCallMediaPriority(t *testing.T) {
	// 1. 响应自带 > tab 声明 > 源默认
	s := observeOne(t, "fake_c", map[string]interface{}{"tabType": "2", "bookId": "b1"},
		legado.BookDetail{Name: "响应说漫画是假象", BookType: "视频"})
	if s.Media != base.MediaVideo {
		t.Errorf("响应自带应压过 tab 声明，实得 %q", s.Media)
	}

	// 2. tab 声明（fake_c 的 tab 2 = comic，tab 1 未声明）
	s = observeOne(t, "fake_c", map[string]interface{}{"tabType": "2"},
		map[string]interface{}{"unrelated": 1})
	if s.Media != base.MediaComic {
		t.Errorf("tab 2 应按声明得 comic，实得 %q", s.Media)
	}
	s = observeOne(t, "fake_c", map[string]interface{}{"tabType": "1"},
		map[string]interface{}{"unrelated": 1})
	if s.Media != "" {
		t.Errorf("未声明的 tab 1 应保持未判定，实得 %q", s.Media)
	}

	// 3. 源默认（fake_b 声明 audio）
	s = observeOne(t, "fake_b", map[string]interface{}{}, map[string]interface{}{"x": 1})
	if s.Media != base.MediaAudio {
		t.Errorf("fake_b 应回落源默认 audio，实得 %q", s.Media)
	}

	// 4. 命名缓存：同一本书此前判过 comic，只带标识的请求也能标出来
	// （不能走 observeOne——它开头会清缓存，把预置的条目抹掉）
	base.ResetNameCaches()
	base.RememberBook("fake_a", "b7", "缓存里的书", base.MediaComic)
	s = &base.CallSubject{}
	legado.ObserveCall("fake_a", map[string]interface{}{"bookId": "b7"},
		map[string]interface{}{"chapterList": []interface{}{}}, s)
	if s.Media != base.MediaComic || s.BookName != "缓存里的书" {
		t.Errorf("缓存反查失效：media %q / book %q", s.Media, s.BookName)
	}
}

// TestObserveCallChapterFlow 打通「搜索灌名 → 目录灌章节名 → 正文反查全维度」这条链路：
// 监控明细里只有标识时，靠的就是这条链。
func TestObserveCallChapterFlow(t *testing.T) {
	base.ResetNameCaches()

	legado.ObserveCall("fake_a", map[string]interface{}{"key": "仙"},
		legado.SearchResponse{BookList: []legado.BookItem{{BookId: "b1", Name: "仙途"}}}, &base.CallSubject{})
	legado.ObserveCall("fake_a", map[string]interface{}{"bookId": "b1"},
		legado.ChapterResponse{ChapterList: []legado.ChapterItem{
			{ItemId: "b1|c1", Title: "第一章"},
			{ItemId: "b1|c2", Title: "第二章"},
		}}, &base.CallSubject{})

	// 正文请求只带 itemId（bookId 由章节标识推导）
	s := &base.CallSubject{}
	legado.ObserveCall("fake_a", map[string]interface{}{"itemId": "b1|c2"},
		legado.ContentResponse{ContentType: "text", Data: map[string]interface{}{"content": "正文正文"}}, s)
	if s.ChapterTitle != "第二章" {
		t.Errorf("章节名未从缓存反查出来，得 %q", s.ChapterTitle)
	}
	if s.BookName != "仙途" {
		t.Errorf("书名未从缓存反查出来（应由 bookId 推导 + 搜索灌入），得 %q", s.BookName)
	}
	if s.Media != base.MediaNovel || s.ResultCount != 1 {
		t.Errorf("正文媒介/结果数 = %q / %d，期望 novel / 1", s.Media, s.ResultCount)
	}
}

func TestObserveCallTruncatesAndTolerates(t *testing.T) {
	// 按 rune 截断：中文不能被截成半个字
	long := strings.Repeat("书", 300)
	s := observeOne(t, "fake_a", map[string]interface{}{"key": strings.Repeat("搜", 300)},
		legado.BookDetail{Name: long, BookId: "bx"})
	if len([]rune(s.Keyword)) != base.SubjectKeywordMax {
		t.Errorf("搜索词应截到 %d rune，实为 %d", base.SubjectKeywordMax, len([]rune(s.Keyword)))
	}
	if len([]rune(s.BookName)) != base.SubjectBookMax || strings.ContainsRune(s.BookName, 0xFFFD) {
		t.Errorf("书名截断异常：%d rune，是否含替换符 %v",
			len([]rune(s.BookName)), strings.ContainsRune(s.BookName, 0xFFFD))
	}

	// 容错：各种奇怪输入只留空值，绝不 panic
	for i, bad := range []interface{}{
		nil, "", 42, []interface{}{1, 2},
		map[string]interface{}{"bookList": "不是数组"},
		map[string]interface{}{"bookList": []interface{}{"不是对象", nil}},
		map[string]interface{}{"bookInfo": "不是对象"},
		map[string]interface{}{"bookTypeCode": "非数字"},
	} {
		s := observeOne(t, "fake_a", map[string]interface{}{"bookId": "b"}, bad)
		if s.Media != "" && s.Media != base.MediaAudio {
			t.Errorf("奇怪形状 #%d 不应判定出媒介，得 %q", i, s.Media)
		}
	}
}
