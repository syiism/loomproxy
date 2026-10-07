package groupb

// 待办清单 P69：一行 `quota_limits(scope=source)` 的形状只许有两个意图口。
//
// 过去这行在四处各拼一遍，而**缺省值有两份**：helper 与播种给套餐默认档（free 100 / vip 1000 / 自定义 -1），
// 通用「限制项」CRUD 静默给 0。于是"同一次授权"按通路落成不同的行，而 0 在限额语义上既不是"不限"
// 也不是"不给用"——它是个没人定义的数（P48 那一族里「0 是没写还是要 0」的同一个形状）。
//
// 这里钉三件事：
//   - 授权走默认档时，值只可能来自 `db.DefaultPerSourceLimit`（helper 那条路）；
//   - 显式给值那条路**缺 limit 就是 400**，不再静默落 0；
//   - 显式给 -1 时落 -1，不会被"顺手补个默认档"改掉。

import (
	"net/http"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
)

// pickUngrantedSource 从本次测试库里挑一个源，并清掉 free 套餐在它身上的旧行——
// 集成用例禁止 t.Parallel()，但同一个库会被多条用例共用，所以"先清再测"是前提而不是可选项。
func pickUngrantedSource(t *testing.T, planID uint) string {
	t.Helper()
	var srcs []models.DataSource
	if err := db.DB.Limit(10).Find(&srcs).Error; err != nil || len(srcs) == 0 {
		t.Fatalf("测试库里没有数据源可挑: err=%v n=%d", err, len(srcs))
	}
	for _, s := range srcs {
		if err := db.DB.Where("plan_id = ? AND scope = ? AND target = ?", planID, "source", s.Name).
			Delete(&models.QuotaLimit{}).Error; err != nil {
			t.Fatalf("清旧限额行失败（target=%s）: %v", s.Name, err)
		}
		return s.Name
	}
	t.Fatal("挑不出可用数据源")
	return ""
}

func findGrant(t *testing.T, planID uint, name string) (models.QuotaLimit, bool) {
	t.Helper()
	var row models.QuotaLimit
	err := db.DB.Where("plan_id = ? AND scope = ? AND target = ?", planID, "source", name).First(&row).Error
	return row, err == nil
}

func TestGrantDefaultRowMatchesPlanDefault(t *testing.T) {
	newTestServer(t) // 只要装配好的库与播种；这一条不打 HTTP

	freeID := planIDByCode(t, "free")
	name := pickUngrantedSource(t, freeID)

	created, err := gate.GrantPlanSource(freeID, name)
	if err != nil || !created {
		t.Fatalf("GrantPlanSource created=%v err=%v", created, err)
	}
	row, ok := findGrant(t, freeID, name)
	if !ok {
		t.Fatal("helper 说建了，库里却没有那一行")
	}
	if want := db.DefaultPerSourceLimit("free"); row.Limit != want {
		t.Errorf("helper 建出的 limit = %d, want 套餐默认档 %d——两个入口必须给同一个数", row.Limit, want)
	}
	if row.Scope != "source" || row.Target != name || row.PlanID != freeID {
		t.Errorf("行的形状不对: %+v", row)
	}

	// 自定义套餐没有默认档，落到"不限"(-1) 而不是 0
	custom := db.NewSourceGrantRow(freeID, "没有这个码的套餐", name)
	if custom.Limit != -1 {
		t.Errorf("未知套餐的默认档 = %d, want -1（0 是一个没人定义的数，不是「不限」）", custom.Limit)
	}
}

func TestCreateLimitRequiresExplicitLimit(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	freeID := planIDByCode(t, "free")
	name := pickUngrantedSource(t, freeID)

	// 1) 缺 limit：400，且库里不能凭空多出一行 limit=0
	status, env := doJSON(t, srv, http.MethodPost, "/admin/quotas/limits",
		map[string]interface{}{"plan_id": freeID, "scope": "source", "target": name}, admin)
	if status != http.StatusBadRequest {
		t.Fatalf("缺 limit 的 POST = %d (%s), want 400——静默落 0 就是这一条要收掉的东西", status, env.Msg)
	}
	if _, ok := findGrant(t, freeID, name); ok {
		t.Error("被拒的请求仍然建了行")
	}
	if !strings.Contains(env.Msg, "limit") {
		t.Errorf("400 的话没说要什么: %q", env.Msg)
	}

	// 2) 显式 -1：落 -1（不限额），不会被补成默认档
	status, env = doJSON(t, srv, http.MethodPost, "/admin/quotas/limits",
		map[string]interface{}{"plan_id": freeID, "scope": "source", "target": name, "limit": -1}, admin)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("显式 -1 的 POST = %d (%s)", status, env.Msg)
	}
	row, ok := findGrant(t, freeID, name)
	if !ok {
		t.Fatal("显式授权没落行")
	}
	if row.Limit != -1 {
		t.Errorf("limit = %d, want -1——显式给值那条口不许掺默认档", row.Limit)
	}
}

// P105：period 列的判定已被摘掉（P70②），但列与默认值（`month`）还在——
// 唯一的写值在 `db.NewExplicitGrant` 显式给 `day`。这条钉住：本库 scope=source 的行 period 只可能是 day，
// 包括新授权的那一行。变异：把构造器里的 `Period: "day"` 摘掉，此用例红（sqlite 无列默认，落到 NULL）。
func TestSourceGrantRowsCarryDayPeriod(t *testing.T) {
	newTestServer(t)
	freeID := planIDByCode(t, "free")
	name := pickUngrantedSource(t, freeID)
	if _, err := gate.GrantPlanSource(freeID, name); err != nil {
		t.Fatalf("GrantPlanSource err=%v", err)
	}
	var periods []string
	if err := db.DB.Raw("SELECT DISTINCT period FROM quota_limits WHERE scope = 'source'").Scan(&periods).Error; err != nil {
		t.Fatalf("读 period 失败: %v", err)
	}
	if len(periods) != 1 || periods[0] != "day" {
		t.Fatalf("scope=source 的 period 应只有 day，实得 %v", periods)
	}
}
