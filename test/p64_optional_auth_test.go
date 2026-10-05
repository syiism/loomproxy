package test

// 可选鉴权（待办清单 P64）：AUTH_ENABLED=false 时网关直通是对的，但直通不等于「解析也跳过」——
// 过去合法 JWT 打 /endpoints 得 401（没人往 context 写 user_id，端点自己回 401），
// 面板「数据源」页整块加载失败。修复后：关着也要把能解析的凭证挂上，解析失败按匿名放行。

import (
	"net/http"
	"strings"
	"testing"

	"loomproxy/conf"
)

func withAuthOff(t *testing.T) {
	t.Helper()
	prev := conf.Config.AuthEnabled
	conf.Config.AuthEnabled = false
	t.Cleanup(func() { conf.Config.AuthEnabled = prev })
}

func TestOptionalAuthStillResolvesCredentials(t *testing.T) {
	srv := newTestServer(t)
	withAuthOff(t)

	// 合法 JWT：注册响应自带 token；必须 200 且不是「缺少登录凭证」（修前是 401）
	st, envReg := doJSON(t, srv, "POST", "/auth/register", map[string]interface{}{
		"username": "p64user", "email": "p64@test.local", "password": "pass1234",
	}, nil)
	if st != http.StatusOK {
		t.Fatalf("注册失败：%d %s", st, envReg.Msg)
	}
	tok, _ := envReg.dataMap(t)["token"].(string)
	if tok == "" {
		t.Fatal("注册响应里没有 token")
	}
	status, env := doJSON(t, srv, "GET", "/endpoints", nil, authHeader(tok))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("AUTH_ENABLED=false + 合法 JWT 打 /endpoints = %d code=%d msg=%s（P64：不该再把网关关闭当未登录）",
			status, env.Code, env.Msg)
	}

	// 无凭证/坏票：按匿名放行——但 /endpoints 是 user_id 过滤的端点，匿名无身份可挂，
	// 端点自己的守卫回 401（这是端点语义，不是网关拦的；P64 修的是合法凭证被网关关闭误伤）
	//
	// 顺带钉住那 401 的**句子**（P64 剩下的那一半）：这里过去发裸英文 `unauthorized`，
	// 中文界面读不出"要登录"还是"网关没开"；而现在网关不拦了，这句话是这条端点唯一的用户可读成因。
	for _, tc := range []struct{ what, token string }{
		{"无凭证", ""},
		{"坏票", "not-a-jwt"},
	} {
		var h map[string]string
		if tc.token != "" {
			h = authHeader(tc.token)
		}
		status, env := doJSON(t, srv, "GET", "/endpoints", nil, h)
		if status != http.StatusUnauthorized {
			t.Fatalf("AUTH_ENABLED=false + %s = %d，端点守卫应回 401（匿名无身份）", tc.what, status)
		}
		if env.Msg == "unauthorized" || env.Msg == "" {
			t.Errorf("%s 那支 401 的 msg 还是裸英文/空的：%q——中文界面读不出该登录还是该开票", tc.what, env.Msg)
		}
		for _, want := range []string{"套餐", "凭证"} {
			if !strings.Contains(env.Msg, want) {
				t.Errorf("%s 那支 401 的 msg 少了 %q：%q", tc.what, want, env.Msg)
			}
		}
	}
}

// TestOptionalAuthDoesNotLoosenEnforcement 追认 C 的**另一半**：
// 「网关关了也认凭证」不等于「强制被关掉」，也不等于「什么凭证都算认出来」。
// 两半各有一档断言，而且都靠**文案的出处**分辨（这正是把裸 `unauthorized` 换成中文句的额外收益：
// 网关发的 401 说「缺少鉴权凭证」，端点守卫发的 401 说"要按套餐过滤"，一眼分得清是谁拦的）：
//   - 网关开着：匿名必须被**网关**拦，而不是落到端点守卫；带合法会话正常通过。
//   - 网关关着：**已吊销**的会话不能被认出来——"不强制"只管"不拦"，不管"照样给归属"；
//     `resolveJWT` 里那句 `db.ValidateSession` 在两种模式下都得跑（P56：会话行就是鉴权的执行点）。
func TestOptionalAuthDoesNotLoosenEnforcement(t *testing.T) {
	srv := newTestServer(t)
	prev := conf.Config.AuthEnabled
	t.Cleanup(func() { conf.Config.AuthEnabled = prev })
	conf.Config.AuthEnabled = true // 网关开着：/endpoints 要凭证；注册走白名单不受影响

	st, envReg := doJSON(t, srv, "POST", "/auth/register", map[string]interface{}{
		"username": "p64enforce", "email": "p64e@test.local", "password": "pass1234",
	}, nil)
	if st != http.StatusOK {
		t.Fatalf("注册失败：%d %s", st, envReg.Msg)
	}
	tok, _ := envReg.dataMap(t)["token"].(string)
	if tok == "" {
		t.Fatal("注册响应里没有 token")
	}

	// 开着：匿名由网关拦——文案是网关那句「缺少鉴权凭证」
	status, env := doJSON(t, srv, "GET", "/endpoints", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("AUTH_ENABLED=true + 匿名 = %d, want 401", status)
	}
	if !strings.Contains(env.Msg, "缺少鉴权凭证") {
		t.Errorf("网关开着时匿名应被网关拦（msg 该含「缺少鉴权凭证」），实际 = %q——"+
			"若落到端点守卫那句，说明「关了才认凭证」被顺手做成了「谁都不拦」", env.Msg)
	}
	// 开着：带合法会话正常通过
	if status, env = doJSON(t, srv, "GET", "/endpoints", nil, authHeader(tok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("AUTH_ENABLED=true + 合法 JWT = %d code=%d msg=%s", status, env.Code, env.Msg)
	}
	// 登出，把这张票吊销掉（前置不成立就当场报错，不留空转断言）
	if status, _ = doJSON(t, srv, "POST", "/auth/logout", nil, authHeader(tok)); status != http.StatusOK {
		t.Fatalf("logout = %d，前置不成立，下面的「已吊销会话」断言会空转", status)
	}

	// 关着：同一张已吊销的票**不能**被认成那个账号
	conf.Config.AuthEnabled = false
	status, env = doJSON(t, srv, "GET", "/endpoints", nil, authHeader(tok))
	if status != http.StatusUnauthorized {
		t.Fatalf("AUTH_ENABLED=false + 已吊销会话 = %d（code=%d msg=%s），want 401——"+
			"「关了也认凭证」若绕过 ValidateSession，这里会拿到归属并回 200", status, env.Code, env.Msg)
	}
	if strings.Contains(env.Msg, "缺少鉴权凭证") {
		t.Errorf("网关关着时不该由网关拦，msg = %q", env.Msg)
	}
}
