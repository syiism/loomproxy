package base

import (
	"strconv"
	"strings"
)

// 书籍媒介类型的标准枚举：监控明细、源声明、上游写法三方共用。
//
// **骨架不判断「哪个源是什么媒介」**——那属数据源知识。媒介只有三个合法来源：
// 响应自带（Legado 的 contentType / bookTypeCode）、源自己的声明
// （SourceMeta.MediaType / SearchTab.MediaType）、同一本书此前的判定（命名缓存）。
//
// 三者都没有 = 空串。空串落库保留原样，聚合层展示成「未判定」——这样被拦掉的请求
// （401/403/429，从没触及内容）不会污染媒介榜单，而「源还没声明」在面板上是一眼可见的空桶。
const (
	MediaNovel = "novel" // 小说
	MediaAudio = "audio" // 音频（听书）
	MediaComic = "comic" // 漫画（**只**表示漫画）
	MediaVideo = "video" // 视频（含短剧、漫剧、番剧）
)

// MediaLabel 中文展示名；空串返回「未判定」
func MediaLabel(media string) string {
	switch media {
	case MediaNovel:
		return "小说"
	case MediaAudio:
		return "音频"
	case MediaComic:
		return "漫画"
	case MediaVideo:
		return "视频"
	}
	return "未判定"
}

// IsValidMedia 声明值合法性：空串 = 未声明（合法），其余必须命中枚举
func IsValidMedia(media string) bool {
	if media == "" {
		return true
	}
	switch media {
	case MediaNovel, MediaAudio, MediaComic, MediaVideo:
		return true
	}
	return false
}

// MediaFromBookTypeCode 按 Legado 规范映射书籍类型码：
// 0 小说 / 1 漫画 / 2 听书 / 3 视频 / 4 文件（本地小说，归小说）。未知返回空串。
func MediaFromBookTypeCode(code int) string {
	switch code {
	case 0, 4:
		return MediaNovel
	case 1:
		return MediaComic
	case 2:
		return MediaAudio
	case 3:
		return MediaVideo
	}
	return ""
}

// MediaFromContentType 归一正文类型：既接受 Legado 规范词（text/image/audio/video），
// 也接受本仓 DTO 的既有写法（novel/manga）。error 与空返回空串（这次没有内容可判）。
func MediaFromContentType(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case MediaAudio, MediaVideo:
		return strings.ToLower(strings.TrimSpace(contentType))
	case "manga", "image", "comic":
		return MediaComic
	case "text", "novel":
		return MediaNovel
	}
	return ""
}

// NormalizeMediaName 认中英文的媒介写法。这是**平台口径**而非某个源的知识：
// 影像类（短剧/漫剧/番剧/影视）统一归 video，`comic` 只留给漫画——漫剧是动态影像，不是漫画。
// 无法识别返回空串。
func NormalizeMediaName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if s == "" {
		return ""
	}
	switch s {
	case MediaNovel, MediaAudio, MediaComic, MediaVideo:
		return s
	}
	contains := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(s, w) {
				return true
			}
		}
		return false
	}
	switch {
	case contains("听书", "音频", "有声", "播客", "电台"):
		return MediaAudio
	case contains("漫画", "绘本", "条漫"):
		return MediaComic
	case contains("短剧", "漫剧", "番剧", "影视", "视频", "动画"):
		return MediaVideo
	case contains("小说", "图书", "书籍", "网文"):
		return MediaNovel
	}
	return ""
}

// MediaFromSearchTab 按请求的 tab 参数在源声明里取媒介；源或该 tab 未声明则返回空串。
// tab 参数名多字段兼容（Legado 用 tabType，历史写法有 tab_type / searchType）。
func MediaFromSearchTab(source string, params map[string]interface{}) string {
	meta, ok := GetSourceMeta(source)
	if !ok || len(meta.SearchTabs) == 0 {
		return ""
	}
	tab := ParamString(params, "tabType", "tab_type", "searchType")
	if tab == "" {
		return ""
	}
	for _, t := range meta.SearchTabs {
		if strconv.Itoa(t.TabType) == tab {
			return t.MediaType
		}
	}
	return ""
}

// ParamString 从请求参数 map 里按顺序取第一个非空标量并转字符串。
// base 不依赖 utils（utils → base 会成环），语义与 utils.FirstNonEmpty 一致：
// 上游与下游的标识可能是数字，只认 string 会静默丢成空串。
func ParamString(params map[string]interface{}, names ...string) string {
	for _, n := range names {
		if s := scalarToString(params[n]); s != "" {
			return s
		}
	}
	return ""
}

func scalarToString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case bool:
		if t {
			return "true"
		}
		return ""
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

// MediaValues 已声明媒介枚举的固定顺序。按媒介筛选的白名单必须用它而不是 IsValidMedia：
// 后者接受空串（「源没声明媒介」是合法状态），拿来做入参校验会把垃圾空值也放行。
func MediaValues() []string {
	return []string{MediaNovel, MediaAudio, MediaComic, MediaVideo}
}

// MediaOption 一个媒介候选：value 进查询、label 进展示
type MediaOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// MediaCandidates 出可以直接渲染的媒介候选（枚举顺序稳定，不含空值）
func MediaCandidates() []MediaOption {
	vals := MediaValues()
	out := make([]MediaOption, 0, len(vals))
	for _, m := range vals {
		out = append(out, MediaOption{Value: m, Label: MediaLabel(m)})
	}
	return out
}

// IsMediaValue 严格判定 m 是枚举内的媒介值。与 IsValidMedia 的区别是有意的：
// 后者认空串（「源没声明媒介」是合法状态），而入参校验要的是「调用方确实给了一个合法值」。
// 公开端点与管理端点共用这一份，免得两处各写一个循环然后各自漂移。
func IsMediaValue(m string) bool {
	for _, v := range MediaValues() {
		if v == m {
			return true
		}
	}
	return false
}
