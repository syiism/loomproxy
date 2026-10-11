package test

// 运维报表端点 GET /admin/monitor/report.html 的三条硬判据。
//
// 为什么每条都要单独钉：
//  1. **鉴权**：报表里是逐源逐日的真实调用量。它是内存渲染直出、不落盘，所以唯一的关口就是这个端点本身。
//     「/data 不鉴权」那条教训（catalog/datafiles.go 的 h.Auth=false + 默认白名单含 /data）说明
//     "顺手找个地方放文件"会直接把运营数据挂到公网上，这里必须证明没这回事。
//  2. **cookie 通道**：前端是 `window.open` 顶层导航，带不上 Authorization 头，靠的是登录写下的
//     HttpOnly cookie（utils/jwt.go SetTokenCookie → TokenFromRequest 第三档）。
//     这条通道不测就等于没通——它是整个"不新造凭证"决定的承重墙。
//  3. **两个出口同源**：报表与 /admin/monitor/trend 必须共用 MonitorTrend。
//     数字对不上就是长出了第二份口径（P72 日界、P22 成功判据都只允许一处定义）。

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/models"
	"loomproxy/utils"
)

const reportSrcName = "report_probe_src"

func fetchReport(t *testing.T, url string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("造请求失败: %v", err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求报表失败: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体失败: %v", err)
	}
	return resp, string(body)
}

func TestMonitorReportRequiresAuthAndSharesAggregation(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	url := srv.URL + "/admin/monitor/report.html"

	// 一条可预期的明细：昨天（平台时区）两次调用，一次成功一次失败。
	zone := utils.PlatformZone()
	at := time.Now().In(zone).AddDate(0, 0, -1)
	rows := []models.ApiCallLog{
		{Source: reportSrcName, Action: "search", Status: 200, Username: "probe", CreatedAt: at},
		{Source: reportSrcName, Action: "content", Status: 500, Username: "probe", CreatedAt: at},
	}
	for i := range rows {
		if err := db.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("插入明细失败: %v", err)
		}
	}

	// 1) 未授权拿不到
	resp, body := fetchReport(t, url, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未授权应 401，实际 %d，正文起 %q", resp.StatusCode, snippet(body))
	}

	// 2) Bearer 通
	resp, body = fetchReport(t, url, map[string]string{"Authorization": "Bearer " + admin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Bearer 应 200，实际 %d，正文起 %q", resp.StatusCode, snippet(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type 应为 text/html，实际 %q", ct)
	}
	for _, want := range []string{reportSrcName, "search", "content", "明细口径", "逐源 × 接口"} {
		if !strings.Contains(body, want) {
			t.Errorf("报表正文缺 %q——逐源/逐接口或口径标注没渲染出来", want)
		}
	}
	// 口径必须写死"不是内存计数"，否则排障时会拿报表去对主表
	if !strings.Contains(body, "内存计数") {
		t.Errorf("报表没写明与内存计数的区别（两套窗口混读是老问题）")
	}

	// 3) cookie 通道：window.open 靠的就是它，请求里**不带**任何 Authorization 头
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("造 cookie 请求失败: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "loomproxy_token", Value: admin})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cookie 请求失败: %v", err)
	}
	defer resp.Body.Close()
	cookieBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("仅带 cookie 应 200（这是 window.open 那条路），实际 %d，正文起 %q",
			resp.StatusCode, snippet(string(cookieBody)))
	}

	// 4) 两个出口同源：报表里该源的总调用 == /admin/monitor/trend 里该源各行之和
	type trendRow struct {
		Day     string `json:"day"`
		Source  string `json:"source"`
		Total   int64  `json:"total"`
		Success int64  `json:"success"`
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Days    []string   `json:"days"`
			Sources []string   `json:"sources"`
			Rows    []trendRow `json:"rows"`
		} `json:"data"`
	}
	r, err := http.NewRequest(http.MethodGet, srv.URL+"/admin/monitor/trend", nil)
	if err != nil {
		t.Fatalf("造 trend 请求失败: %v", err)
	}
	r.Header.Set("Authorization", "Bearer "+admin)
	tr, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("请求 trend 失败: %v", err)
	}
	defer tr.Body.Close()
	raw, _ := io.ReadAll(tr.Body)
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("解析 trend 响应失败: %v（响应起 %s）", err, snippet(string(raw)))
	}
	var jsonTotal int64
	for _, row := range env.Data.Rows {
		if row.Source == reportSrcName {
			jsonTotal += row.Total
		}
	}
	if jsonTotal != 2 {
		t.Fatalf("trend 侧该源总量 = %d, want 2（明细没被聚合进来？先查日界与窗口）", jsonTotal)
	}

	// 报表那一行的「总调用」单元格：行形状是 <td><code>源</code></td> → spark 单元格 → 数字列
	re := regexp.MustCompile(`(?s)<code>` + regexp.QuoteMeta(reportSrcName) + `</code></td>.*?<td class="num">(\d+)</td>`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("报表里找不到 %s 的总量单元格（行形状变了？）", reportSrcName)
	}
	htmlTotal, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		t.Fatalf("解析报表总量失败: %v", err)
	}
	if htmlTotal != jsonTotal {
		t.Errorf("同一份聚合两个出口对不上：报表 %d vs trend %d——说明长出了第二份口径", htmlTotal, jsonTotal)
	}
}

func snippet(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 80 {
		return s[:80]
	}
	return s
}
