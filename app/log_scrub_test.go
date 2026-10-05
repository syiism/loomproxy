package app

// 待办清单 P92：服务端日志里不能留阅读行为数据。
//
// 现网实测到的形状是 `handleError` 把传输层错误原样落盘，而 Go 的 http 错误文本**必带完整请求 URL**，
// Legado 这一族的查询串里就是用户在读什么（搜索词、书目标识、章节标识）。
// `sanitizeUpstreamMsg` 只管对外的句子，日志侧此前没有任何一处洗过它。
//
// 用例里的词与标识都是**编造的**——真实用户搜索词属于阅读行为数据，不进仓库（AGENTS §11）。

import (
	"strings"
	"testing"
)

func TestScrubQueryForLogHidesReadingData(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// 这些片段是"用户在读什么"的载体，一个都不许留在洗完的文本里
		forbid []string
		// 排障要看的部分必须留下
		want []string
	}{
		{
			name:   "传输层错误原文（HTML 源的搜索形状）",
			in:     `Get "https://www.uxx001.com/?c=search&wd=%E7%BC%96%E9%80%A0%E6%90%9C%E7%B4%A2%E8%AF%8D&sort=addtime&order=desc&page=1": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
			forbid: []string{"wd=", "sort=", "order=", "page=", "%E7%BC%96"},
			want:   []string{"www.uxx001.com", "context deadline exceeded"},
		},
		{
			name:   "签名站的查询串里带设备与签名参数",
			in:     `signedFetch 持续失败: https://api5-lite-sinfonlinec.novelfm.com/novelfm/bookmall/search/page/v1/?aid=3040&device_sn=FAKE-DEVICE&sig=FAKESIGNATURE: Post "https://api5-lite-sinfonlinec.novelfm.com/novelfm/bookmall/search/page/v1/?aid=3040": 403`,
			forbid: []string{"device_sn", "sig=", "aid="},
			want:   []string{"api5-lite-sinfonlinec.novelfm.com", "/novelfm/bookmall/search/page/v1/"},
		},
		{
			name:   "章节标识也在查询串里",
			in:     `Get "https://h.example.com/chapter?item_id=FAKEBOOK123&chapterID=42": EOF`,
			forbid: []string{"item_id", "chapterID", "FAKEBOOK123"},
			want:   []string{"h.example.com/chapter", "EOF"},
		},
		{
			name:   "没有 URL 的原文一字不改",
			in:     `上游返回非 2xx: 502 Bad Gateway`,
			forbid: []string{"隐去"},
			want:   []string{"上游返回非 2xx: 502 Bad Gateway"},
		},
	}

	for _, c := range cases {
		got := scrubQueryForLog(c.in)
		for _, f := range c.forbid {
			if strings.Contains(got, f) {
				t.Errorf("%s：洗完仍留着 %q（这就是用户在读什么）\n  原文：%s\n  洗完：%s", c.name, f, c.in, got)
			}
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s：排障要看的 %q 被一起洗掉了\n  洗完：%s", c.name, w, got)
			}
		}
	}
}
