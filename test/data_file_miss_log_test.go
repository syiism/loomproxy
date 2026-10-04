package test

// 静态字典缺位的请求期读数（待办清单 P51① 的另一半；分支侧 S48 的巡检发现也落在这里）。
//
// 为什么启动核对（`CheckDeclaredDataFiles`）不够：它只看**各源声明过**的字典，
// 而生产 14 天里真正打不中的约 1170 次**多数没有任何源声明**（旧导入的客户端在要已下架的番茄字典）——
// 这些路径在启动那一刻天生不在名单里，只有人来要的时候才显形。
//
// 两条断言各钉一件事：
//   ① **对外行为一字未动**：缺文件仍然回 200 + 那个自造的 `error` 正文。
//      改 404 是下游可见变更，维护者还没拍（P51②）；这一条钉住的是"我只加读数，没顺手改响应"。
//   ② 服务端自己看得见，且**两类分开数**（有源声明=部署缺产物 / 无声明=有人在要不存在的东西）。

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"loomproxy/conf"
	"loomproxy/handlers/catalog"
	"loomproxy/testkit/fakesource"
)

func getDataFile(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读 %s 响应失败: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

func TestStaticDictMissCountedAndResponseUnchanged(t *testing.T) {
	srv := newTestServer(t)

	// 夹具里 fake_b 声明了字典 `fakesource.DictFile`（分类目录 fake），临时 DATA_DIR 里没装任何产物 → 声明而不在位
	total0, declared0, unknown0 := catalog.DataFileMissTotals()
	status, body := getDataFile(t, srv.URL+"/data/fake/"+fakesource.DictFile+".json")
	if status != http.StatusOK {
		t.Errorf("缺文件 status = %d, want 200——**这一版刻意不改对外行为**（404 与否等 P51② 拍板）", status)
	}
	if !strings.Contains(body, "not found in source") {
		t.Errorf("缺文件的正文形状变了：%q", body)
	}
	if strings.Contains(body, `"code"`) {
		t.Errorf("正文里冒出了标准信封字段，形状与生产实测不同（生产是 {\"error\",\"available\"}）：%q", body)
	}
	total1, declared1, unknown1 := catalog.DataFileMissTotals()
	if declared1-declared0 != 1 || unknown1-unknown0 != 0 || total1-total0 != 1 {
		t.Errorf("有源声明的缺位没被正确记账：total+%d declared+%d unknown+%d, want 1/1/0",
			total1-total0, declared1-declared0, unknown1-unknown0)
	}

	// 没有任何源声明的路径：同一件事，但运维处置完全不同（该退的是客户端，不是补产物）
	_, _ = getDataFile(t, srv.URL+"/data/fake/never_declared_anywhere.json")
	if _, _, u := catalog.DataFileMissTotals(); u-unknown0 != 1 {
		t.Errorf("无声明的缺位 = %d, want 1（这一类占生产缺位的绝大多数）", u-unknown0)
	}

	// 在位的那一份不该被记成缺位：把产物装上再打一次
	// （写进 `conf.Config.DataDir` 就行：handler 构造时取的就是这一个值，测试服务器建好后没人改它）
	writeDict(t, conf.Config.DataDir, "fake", fakesource.DictFile, `{"k":"v"}`)
	status, body = getDataFile(t, srv.URL+"/data/fake/"+fakesource.DictFile+".json")
	if status != http.StatusOK || !strings.Contains(body, `"k"`) {
		t.Fatalf("装好之后该直出正文，实得 status=%d body=%q", status, body)
	}
	if t2, d2, u2 := catalog.DataFileMissTotals(); t2 != total1+1 || d2 != declared1 || u2 != unknown0+1 {
		t.Errorf("在位文件被记成了缺位：total=%d declared=%d unknown=%d（应只在前面那三次缺位上增长）", t2, d2, u2)
	}
}

func TestMissWatcherThrottleAndSplitCounts(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	lines := func() int { return strings.Count(buf.String(), "静态字典缺位") }

	// 1) 窗口 1 小时：第一次出声，之后同一窗口内不再喊（`/data/...` 免鉴权，
	//    不节流就等于给任何人一个免费的刷日志入口）
	w := catalog.NewMissWatcher(time.Hour)
	w.Note("fake", "alpha")
	if n := lines(); n != 1 {
		t.Fatalf("首次缺位出声 %d 条, want 1", n)
	}
	for i := 0; i < 4; i++ {
		w.Note("fake", "alpha")
	}
	if n := lines(); n != 1 {
		t.Errorf("窗口内重复缺位出声 %d 条, want 仍 1（节流没做成就是刷日志）", n)
	}
	if total, declared, unknown := w.Totals(); total != 5 || unknown != 5 || declared != 0 {
		t.Errorf("计数 = %d/%d/%d, want 5/0/5（计数不去重，去重的只是出声）", total, declared, unknown)
	}
	// 出声是**首次缺位立刻说**，不是等窗口结束：所以这一条里的累计数是 1，
	// 而窗口内攒下的其它路径要到下一次出声才点名（见下面第 4 段）
	line := buf.String()
	if !strings.Contains(line, "fake/alpha.json") || !strings.Contains(line, "累计 1 次") {
		t.Errorf("首次出声缺了关键读数（路径与当时的累计次数）：\n%s", line)
	}
	if !strings.Contains(line, "无声明=1") {
		t.Errorf("没把「有没有源声明」这一分类说出来：\n%s", line)
	}

	// 2) 有源声明的那一类：名字取夹具里真声明过的字典，另一类才不会被混进来
	buf.Reset()
	w2 := catalog.NewMissWatcher(time.Hour)
	w2.Note("fake", fakesource.DictFile)
	if _, declared, unknown := w2.Totals(); declared != 1 || unknown != 0 {
		t.Errorf("声明过的字典被分成无声明：declared=%d unknown=%d, want 1/0——两类的处置完全不同，混了就没法用", declared, unknown)
	}
	if !strings.Contains(buf.String(), "有源声明=1") {
		t.Errorf("出声正文没说清这是部署缺产物：\n%s", buf.String())
	}

	// 3) 窗口=0：每次都出声。这条是**反向守卫**——上面那条"只喊一次"必须是窗口在起作用，
	//    而不是出声本身坏了（否则 1 用空转也能过）
	buf.Reset()
	w3 := catalog.NewMissWatcher(0)
	for i := 0; i < 3; i++ {
		w3.Note("fake", "beta")
	}
	if n := lines(); n != 3 {
		t.Errorf("窗口为 0 时出声 %d 条, want 3（出声通路本身必须是活的）", n)
	}

	// 4) 一条聚合里点名有上限，其余只报条数：喷不同路径不能把一行日志撑成无限长。
	//    这里要等过一个窗口才看得出"窗口内攒下的路径会在下一次出声里被点名"——
	//    只发 9 条不等窗口的话，第一条就自己把队列冲掉了，剩下的永远不会出现在同一行里。
	//    窗口取 200ms 而不是 20ms：那 9 次 Note 之间的调度抖动就能越过 20ms，会多冲出一条来把计数搅乱。
	buf.Reset()
	w4 := catalog.NewMissWatcher(200 * time.Millisecond)
	for i := 0; i < 9; i++ {
		w4.Note("fake", string(rune('a'+i)))
	}
	// 第 1 条会在 i=0 那次立刻出声（首次缺位必须马上看得见），所以这里要从**下一次出声**那一行开始数；
	// 不切这一刀，数到的 9 是"第一行的 1 条 + 第二行的 8 条"，看着像上限坏了
	first := buf.String()
	if n := strings.Count(first, "fake/"); n != 1 {
		t.Errorf("首次出声点名 %d 条, want 1（当时队列里只有那一条）\n%s", n, first)
	}
	time.Sleep(260 * time.Millisecond)
	w4.Note("fake", "zzz")
	out := strings.TrimPrefix(buf.String(), first)
	if n := strings.Count(out, "fake/"); n != 8 {
		t.Errorf("一条聚合里点名 %d 条, want 8（上限之外只报数）\n%s", n, out)
	}
	if !strings.Contains(out, "还有 1 条未逐条点名") {
		t.Errorf("超出上限的部分没有交代：\n%s", out)
	}
	if !strings.Contains(out, "累计 10 次") {
		t.Errorf("第二次出声的累计数不对（应该是全量累计，不是本窗口的条数）：\n%s", out)
	}
}
