package admin

// 运维报表：把「近 7 天调用趋势」出一份**独立文档**，而不是在监控页里画一张随源数变长的组合图。
//
// 为什么是这个形状（三条都被实测挡过，不是风格选择）：
//   - 落盘到 data/ 不可行：`/data` 不鉴权（catalog/datafiles.go 的 h.Auth=false + 默认白名单含 /data），
//     且出口强制 .json；data/ 在装配仓还是 git 跟踪的产物树，服务器写进去会让工作树常脏，
//     而全仓没有文件保留清理——报表会永久堆积。所以这里**内存渲染直出，什么都不写**。
//   - 鉴权不新造凭证：登录时已写 HttpOnly cookie（utils/jwt.go 的 SetTokenCookie），
//     而本组的 AdminRequired → parseTokenFromContext → utils.TokenFromRequest 第三档就认 cookie，
//     所以浏览器 `window.open` 这种顶层导航天然带着它。URL 里不进 token，日志与历史里也不留凭证。
//   - 读数一律来自 MonitorTrend（与 /admin/monitor/trend 同一个函数）：日界唯一由 utils.DayStart
//     定义（P72）、成功唯一判据是 2xx 且非带内失败（P22）。报表不许长出第二套口径。

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/utils"
)

//go:embed monitor_report.tmpl.html
var monitorReportFS embed.FS

// reportBar 一天的迷你柱：高度百分比在 Go 侧算好，模板只负责画（模板里不写字母数字转换）。
type reportBar struct {
	Day        string
	Total      int64
	Success    int64
	HeightPct  int
	FailMarker bool // 该日有失败：颜色之外再给一个记号，不看颜色的人也读得出（P33 那条判据）
}

// reportSourceRow 一个数据源的一行 small multiple。
type reportSourceRow struct {
	Source      string
	Total       int64
	Success     int64
	Rate        string
	Fail        int64
	MaxDayTotal int64
	Bars        []reportBar
}

// reportActionRow 源 × 接口的长尾表一行。
type reportActionRow struct {
	Source  string
	Action  string
	Total   int64
	Success int64
	Fail    int64
	Rate    string
}

type reportView struct {
	GeneratedAt string
	WindowDays  int
	DayLabels   []string
	Empty       bool
	Total       int64
	Success     int64
	Fail        int64
	Rate        string
	Sources     []reportSourceRow
	Actions     []reportActionRow
	// 口径脚注里要复述的两句硬事实，从 Go 侧带过去，免得文档与代码各写一份
	WindowStart string
}

// ratePct 把 total/success 转成一句人读的成功率；total=0 不给 "0%"（那会被读成"全失败"）。
func ratePct(total, success int64) string {
	if total <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", float64(success)*100/float64(total))
}

// buildReportView 把 MonitorTrend 的三份读数摊成模板要的形状。
// 逐源按窗口内总量降序：源再多也是「一行一个」，图上的 series 数不再等于源数。
func buildReportView(t *TrendData, now time.Time) reportView {
	v := reportView{
		GeneratedAt: now.In(utils.PlatformZone()).Format("2006-01-02 15:04:05 MST"),
		WindowDays:  trendDays,
		WindowStart: t.Days[0],
	}
	for _, d := range t.Days {
		v.DayLabels = append(v.DayLabels, d[5:]) // MM-DD
	}

	bySource := make(map[string]*reportSourceRow)
	var actMap = make(map[string]*reportActionRow)
	for _, r := range t.Rows {
		s := bySource[r.Source]
		if s == nil {
			s = &reportSourceRow{Source: r.Source}
			bySource[r.Source] = s
		}
		s.Total += r.Total
		s.Success += r.Success
		if h := r.Total; h > s.MaxDayTotal {
			s.MaxDayTotal = h
		}
		v.Total += r.Total
		v.Success += r.Success
	}
	for _, a := range t.ByAction {
		k := a.Source + "|" + a.Action
		p := actMap[k]
		if p == nil {
			p = &reportActionRow{Source: a.Source, Action: a.Action}
			actMap[k] = p
		}
		p.Total += a.Total
		p.Success += a.Success
	}

	// 柱子高度按「本源窗口内最高的一天」归一：36 个源的量级差三个数量级时，
	// 用全局最大值会让小源整行是空的——这里要比的是形状，不是绝对高低。
	for _, r := range t.Rows {
		s := bySource[r.Source]
		pct := 0
		if s.MaxDayTotal > 0 {
			pct = int(r.Total * 100 / s.MaxDayTotal)
		}
		if r.Total > 0 && r.Success < r.Total {
			pct = max(pct, 8) // 有失败的日子即使量小也别画成看不见
		}
		s.Bars = append(s.Bars, reportBar{
			Day: r.Day[5:], Total: r.Total, Success: r.Success,
			HeightPct: pct, FailMarker: r.Total-r.Success > 0,
		})
	}
	// 无调用的天补占位柱，逐日列不能断档（与 Days 序列同一个口径）
	for _, s := range bySource {
		if len(s.Bars) < len(t.Days) {
			seen := make(map[string]bool)
			for _, b := range s.Bars {
				seen[b.Day] = true
			}
			for _, d := range t.Days {
				if !seen[d[5:]] {
					s.Bars = append(s.Bars, reportBar{Day: d[5:], HeightPct: 2})
				}
			}
			sort.Slice(s.Bars, func(i, j int) bool { return s.Bars[i].Day < s.Bars[j].Day })
		}
		s.Fail = s.Total - s.Success
		s.Rate = ratePct(s.Total, s.Success)
	}

	v.Fail = v.Total - v.Success
	v.Rate = ratePct(v.Total, v.Success)
	v.Empty = len(bySource) == 0

	for _, s := range bySource {
		v.Sources = append(v.Sources, *s)
	}
	sort.Slice(v.Sources, func(i, j int) bool {
		if v.Sources[i].Total != v.Sources[j].Total {
			return v.Sources[i].Total > v.Sources[j].Total
		}
		return v.Sources[i].Source < v.Sources[j].Source
	})
	for _, a := range actMap {
		a.Fail = a.Total - a.Success
		a.Rate = ratePct(a.Total, a.Success)
		v.Actions = append(v.Actions, *a)
	}
	sort.Slice(v.Actions, func(i, j int) bool {
		if v.Actions[i].Total != v.Actions[j].Total {
			return v.Actions[i].Total > v.Actions[j].Total
		}
		return v.Actions[i].Source+v.Actions[i].Action < v.Actions[j].Source+v.Actions[j].Action
	})
	return v
}

// GetMonitorReport 出一份自包含的 HTML 运维报表（内联样式，可另存、可打印）。
func GetMonitorReport(c *gin.Context) {
	t, err := MonitorTrend()
	if err != nil {
		c.String(http.StatusInternalServerError, "读取窗口明细失败：%v", err)
		return
	}
	tmpl, err := template.ParseFS(monitorReportFS, "monitor_report.tmpl.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "报表模板不可用：%v", err)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, buildReportView(t, time.Now())); err != nil {
		c.String(http.StatusInternalServerError, "报表渲染失败：%v", err)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", buf.Bytes())
}
