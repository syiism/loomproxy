package groupb

// /data/<分类>/<文件>.json 免鉴权、原样直出（AGENTS §7 的部署事实），
// 而 handler 里是 `filepath.Join(dataDir(source), name+".json")`——
// gin 会把路径参数**反转义**，所以 `%2e%2e%2f` 这类编码进来的 `../` 有变成一个穿出 DATA_DIR
// 的相对路径的风险。本用例探的就是这条：穿出即缺陷，穿不出即固化成守卫。

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loomproxy/conf"
)

// rawGet 按**原样的转义路径**发一次 GET（Go 的 client 会保留 RawPath，这正是网关收到的那一串）
func rawGet(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestDataFilesCannotEscapeDataDir(t *testing.T) {
	srv := newTestServer(t)

	dataDir := conf.Config.DataDir
	if dataDir == "" {
		t.Fatal("conf.Config.DataDir 是空的——探针没有基准目录，断言会空转")
	}
	parent := filepath.Dir(dataDir)
	secret := filepath.Join(parent, "probe_secret.json")
	sentinel := "PROBE-SENTINEL-不应该被读到"
	if err := os.WriteFile(secret, []byte(" {\n \"leak\": \""+sentinel+"\"\n}\n"), 0o600); err != nil {
		t.Fatalf("写探针文件失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("探针文件没落地（%v）——后面的请求什么也证明不了", err)
	}

	// 五种形态：编码的 ../（三种，都被路由形状挡在外面）、裸 .. 的两段式（**这条在加守卫前能读到内容**）、
	// 以及单段 /data/..（列名单也是一种泄露）
	cases := []struct {
		why  string
		path string
	}{
		{"name 段里编码 ../（gin 反转义后就是我们代码收到的 name）",
			srv.URL + "/data/fake_a/%2e%2e%2f%2e%2e%2fprobe_secret.json"},
		{"source 段里编码 ../",
			srv.URL + "/data/%2e%2e%2f%2e%2e%2fprobe_secret.json"},
		{"name 段编码 ../ 但只上一层（DATA_DIR 的父目录）",
			srv.URL + "/data/fake_a/%2e%2e/probe_secret.json"},
		{"source 段用裸 .. （实测这一条在加守卫之前是**能读到内容的**）",
			srv.URL + "/data/../probe_secret.json"},
		{"单段 /data/..：就算不读内容，也会把父目录的 .json 名单念出来",
			srv.URL + "/data/.."},
	}
	for _, c := range cases {
		status, body := rawGet(t, c.path)
		if strings.Contains(body, "probe_secret") && !strings.Contains(c.path, "probe_secret") {
			// 只有"名字不在 URL 里却出现在响应里"才算念出了目录名单（读的那几条本来就在 URL 里带过这个名字）
			t.Errorf("把父目录的文件名念出来了（名单本身就是泄露）：\n  形态：%s\n  body=%s",
				c.why, truncate(body, 200))
		}
		if strings.Contains(body, sentinel) {
			t.Errorf("穿出 DATA_DIR 读到了探针文件！\n  形态：%s\n  URL：%s\n  status=%d body=%s",
				c.why, c.path, status, truncate(body, 200))
			continue
		}
		// 收口的形状（P51② 已拍板）：一律 404 + 统一 "not found"，不区分原因、不念名单——
		// 含糊的"200 且什么都没读"与"200 说非法"都不算过
		if status != http.StatusNotFound || !strings.Contains(body, "not found") {
			t.Errorf("形态「%s」应 404 + not found（P51②），status=%d body=%s",
				c.why, status, truncate(body, 160))
		}
		t.Logf("形态「%s」→ status=%d，未泄露（body 前 80 字：%s）",
			c.why, status, truncate(body, 80))
	}

	// 反向自检：正常的字典路径必须仍然读得到，否则上面几条"没泄露"可能只是整条路由坏了
	if err := os.MkdirAll(filepath.Join(dataDir, "probe_dir"), 0o755); err != nil {
		t.Fatalf("建探针目录失败: %v", err)
	}
	good := filepath.Join(dataDir, "probe_dir", "ok.json")
	if err := os.WriteFile(good, []byte(`{"ok": 1}`), 0o644); err != nil {
		t.Fatalf("写正常探针失败: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(dataDir, "probe_dir")) })
	status, body := rawGet(t, srv.URL+"/data/probe_dir/ok.json")
	if status != http.StatusOK || !json.Valid([]byte(body)) || !strings.Contains(body, `"ok"`) {
		t.Fatalf("通路自检失败：/data/probe_dir/ok.json status=%d body=%s——那么上面四条『没读到』都是空转",
			status, truncate(body, 160))
	}
}
