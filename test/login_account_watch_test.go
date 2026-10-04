package test

// 账号侧登录尝试的**只读**观察（待办清单 P40① 缺的那份材料）。
//
// 这条观察存在的理由写在两处：
//   - 现网 14 天 journal 是「536 次登录失败 / 222 个不同 IP，单 IP 最多 14 次」——
//     这个形状既可能是很多人各输错几次，也可能是分布式慢速爆破，**按 IP 的闸门分不出这两种**；
//     而 P40①（账号侧要不要独立闸门）唯一缺的就是这份能把两者分开的数。
//   - 所以这个文件里最重要的断言是**反向**的：观察到了这么多失败，处置一次都不许发生。
//     拍不拍、怎么拍是人做的事；这一版只把材料摆出来。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"loomproxy/handlers/auth"
)

func watchRowOf(principal string) (auth.AccountWatchSnapshot, bool) {
	for _, r := range auth.LoginAccountWatch() {
		if r.Principal == principal {
			return r, true
		}
	}
	return auth.AccountWatchSnapshot{}, false
}

func TestLoginAccountWatchObservesWithoutDisposing(t *testing.T) {
	srv := newTestServer(t)
	registerUser(t, srv, "watchacct", "watchacct@example.com", "passw0rd123")

	// 一个账号被 12 个不同 IP 各试错一次：正是按 IP 那道闸（10 次/分钟、连错 10 次锁）摸不到的形状
	for i := 1; i <= 12; i++ {
		ip := fmt.Sprintf("10.77.0.%d", i)
		if st, _ := loginFromIP(t, srv, "watchacct", "wrong-pass", ip); st != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误登录 status = %d, want 401", i, st)
		}
	}

	row, ok := watchRowOf("watchacct")
	if !ok {
		t.Fatalf("观察表里没有 watchacct——整条通路没记账，后面的断言都是空转")
	}
	if row.Fails != 12 {
		t.Errorf("失败计数 = %d, want 12", row.Fails)
	}
	if row.DistinctIPs != 12 {
		t.Errorf("不同 IP 数 = %d, want 12（这条读数存在的意义就是「一个账号来自多少个出口」）", row.DistinctIPs)
	}
	if len(row.SampleIPs) > 8 {
		t.Errorf("样本 IP 数 = %d, want ≤8（上界没生效，一行读数就能被喷长）", len(row.SampleIPs))
	}
	if len(row.SampleIPs) == 0 {
		t.Errorf("样本 IP 为空而 distinct=%d：只有总数没有来源，等于没法去库里对着查", row.DistinctIPs)
	}

	// 反向断言：一次处置都没发生——没有任何 IP 被锁
	snap := auth.SecurityAttemptSnapshot()
	locked := 0
	for _, s := range snap {
		if s.Locked {
			locked++
		}
	}
	if locked != 0 {
		t.Errorf("12 次分散失败之后有 %d 个 IP 处于锁定——观察参与了判定，这条读数的意义就反了", locked)
	}
	// 而且此刻用正确密码仍然能登录（它没变成一道隐形的闸）
	if st, env := loginFromIP(t, srv, "watchacct", "passw0rd123", "10.77.9.9"); st != http.StatusOK {
		t.Fatalf("被观察的账号登录 status = %d（msg=%s）, want 200——观察改变了行为", st, env.Msg)
	}
	row, ok = watchRowOf("watchacct")
	if !ok {
		t.Fatalf("成功后再从表里找 watchacct 找不到了")
	}
	if row.Attempts != 13 || row.Fails != 12 || row.DistinctIPs != 13 {
		t.Errorf("成功那次记账后 = %d/%d/%d, want 13/12/13（成功也算一次尝试：它提供「这个账号同时在被人正常登录」的分母）",
			row.Attempts, row.Fails, row.DistinctIPs)
	}

	// 端点契约：面板那一列的标黄阈值必须由响应下发，且它就是「设备与密钥」页用的同一条定义。
	// 这条断言防的是将来有人在 Vue 里写死一个数——那是 P46/P53 反复在防的「两处写同一份值」。
	status, env := doJSON(t, srv, http.MethodGet, "/admin/security/attempts", nil, authHeader(adminToken(t, srv)))
	if status != http.StatusOK {
		t.Fatalf("读 /admin/security/attempts status = %d, want 200", status)
	}
	var payload struct {
		Items        []auth.AttemptSnapshot      `json:"items"`
		Accounts     []auth.AccountWatchSnapshot `json:"accounts"`
		AccountsMeta struct {
			MultiIPYellow int `json:"multi_ip_yellow"`
		} `json:"accounts_meta"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("解不开响应 data：%v（%s）", err, string(env.Data))
	}
	if len(payload.Accounts) == 0 {
		t.Errorf("端点没带回 accounts 观察（面板那一列会是空的）")
	}
	if want := auth.SuspectDistinctIPs(); payload.AccountsMeta.MultiIPYellow != want {
		t.Errorf("accounts_meta.multi_ip_yellow = %d, want %d（必须与 suspect_distinct_ips 同一条定义）",
			payload.AccountsMeta.MultiIPYellow, want)
	}
}

func TestLoginAccountWatchKeysAreNormalizedAndBounded(t *testing.T) {
	srv := newTestServer(t)

	// 1) 同一账号的三种写法（大小写、首尾空白）必须合成一条读数——否则"被试最多的账号"这个数没意义
	for i, p := range []string{"MixedCase", "  mixedcase  ", "MIXEDCASE"} {
		if st, _ := loginFromIP(t, srv, p, "wrong-pass", fmt.Sprintf("10.88.0.%d", i+1)); st != http.StatusUnauthorized {
			t.Fatalf("标识 %q 登录 status = %d, want 401", p, st)
		}
	}
	row, ok := watchRowOf("mixedcase")
	if !ok || row.Attempts != 3 {
		t.Fatalf("三种写法没合成一条读数：%+v ok=%v（want mixedcase / attempts=3）", row, ok)
	}

	// 2) 键是用户输入，长度必须被夹住（否则这张表按输入长度长）。
	//    这里**没有**"空标识"那一条：`Login` 在查库之前就把 trim + 小写做了，纯空白用户名走的是
	//    「没填」那条 400，永远到不了这个表——为一个到不了的路径写守卫，换来的是一条
	//    变异掉守卫也照样绿的断言（本轮实测：删掉 `key == ""` 的判断，用例全绿）。
	long := strings.Repeat("z", 100)
	if st, _ := loginFromIP(t, srv, long, "wrong-pass", "10.88.0.91"); st != http.StatusUnauthorized {
		t.Fatalf("超长标识登录 status = %d, want 401", st)
	}
	var found auth.AccountWatchSnapshot
	hits := 0
	for _, r := range auth.LoginAccountWatch() {
		if strings.HasPrefix(r.Principal, "zzz") {
			found, hits = r, hits+1
		}
	}
	if hits == 0 {
		t.Fatalf("找不到超长标识的观察记录：%+v", auth.LoginAccountWatch())
	}
	if hits != 1 {
		t.Errorf("超长标识聚成了 %d 条记录, want 1（同一个输入写两次不该长出两个键）", hits)
	}
	if n := len([]rune(found.Principal)); n != 65 || !strings.HasSuffix(found.Principal, "…") {
		t.Errorf("截断后的键长 %d rune = %q, want 65（64 个字符 + 省略号）", n, found.Principal)
	}

	// 3) 排序：失败多的在前（面板第一屏要能直接看见最该看的那条）
	for i := 0; i < 2; i++ {
		_, _ = loginFromIP(t, srv, "noisyacct", "wrong-pass", fmt.Sprintf("10.88.1.%d", i+1))
	}
	rows := auth.LoginAccountWatch()
	if len(rows) < 3 {
		t.Fatalf("观察表行数 %d, want ≥3", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Fails < rows[i].Fails {
			t.Errorf("第 %d 行失败数 %d 小于后一行 %d——排序没按失败数降序", i, rows[i-1].Fails, rows[i].Fails)
		}
	}
	if rows[0].Principal != "mixedcase" {
		t.Errorf("排第一的是 %q, want mixedcase（它失败 3 次）", rows[0].Principal)
	}
}
