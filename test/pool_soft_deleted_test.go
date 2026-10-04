package test

// 号池「手工软删行」的可见性读数（待办清单 P35② 的那一半能自己说话的部分）。
//
// 分清了两件事：
//   - **要不要框架自动物理清理**软删行——是决策，还没拍，本轮不做；
//   - **这些行今天完全隐形**——是事实。框架自己不产生软删行（回收那一路走 `Unscoped()` 硬删），
//     所以它们只可能来自手工 SQL（分支 S27 就软删过一次腾名额）；而所有按状态的计数都带
//     `deleted_at IS NULL` 的默认作用域，于是这些行占着库、可能撞 `idx_pool_device` 唯一索引，
//     却不出现在任何读数是。本轮补的就是这一格数，并且现网复算过：**0 行**（29 行活、0 行软删），
//     所以这不是"正在堆积"，是"堆积发生了也没人知道"。
//
// 最重要的一条断言是第 3 段：只写 `Unscoped()` 而忘记 `deleted_at IS NOT NULL`，
// 这个数会等于总行数——面板从此天天报一堆根本不存在的堆积，比原来更糟。

import (
	"testing"

	"loomproxy/base/pool"
	"loomproxy/db"
	"loomproxy/models"
)

func insertPoolRow(t *testing.T, poolName, ident, status string) uint {
	t.Helper()
	d := models.PoolDevice{Pool: poolName, Ident: ident, Status: status}
	if err := db.DB.Create(&d).Error; err != nil {
		t.Fatalf("预置号池行 %s/%s 失败: %v", poolName, ident, err)
	}
	return d.ID
}

func TestPoolStatusReportsSoftDeletedRows(t *testing.T) {
	newTestServer(t)

	prov := newFakeProvider()
	prov.poolName = "sd_watch"
	cfg := fakePoolConfig()
	p := newFakePool(t, pool.New(prov, cfg))
	name := p.Name()

	insertPoolRow(t, name, "活号", pool.StatusCold)
	doomed := insertPoolRow(t, name, "被手工软删的号", pool.StatusDead)
	insertPoolRow(t, name, "另一个活号", pool.StatusCold)

	// 1) 软删之前：一格都不该报
	if got := p.Status().SoftDeleted; got != 0 {
		t.Fatalf("三行都在位时软删读数 = %d, want 0", got)
	}

	// 2) 软删一行：读数报 1，而它在按状态的计数与明细里**全都隐形**
	if err := db.DB.Delete(&models.PoolDevice{}, doomed).Error; err != nil {
		t.Fatalf("软删那一行失败: %v", err)
	}
	st := p.Status()
	if st.SoftDeleted != 1 {
		t.Errorf("软删一行后的读数 = %d, want 1", st.SoftDeleted)
	}
	// 隐形的那一面：所有按状态的计数都不含它。这格与上面那格是**一对**——
	// 只报第一格说明不了问题，只有"它哪都不出现、只在新那一格里露一次头"才是要防的形状
	var sum int64
	for _, n := range st.Counts {
		sum += n
	}
	if sum != 2 {
		t.Errorf("按状态合计 = %d, want 2（软删行在所有状态计数里都不该出现）", sum)
	}

	// 3) 只有真硬删掉才归零：这条防的是把 `deleted_at IS NOT NULL` 写丢——
	//    只留 `Unscoped()` 的写法在这里会报成"活行也是堆积"
	if err := db.DB.Unscoped().Delete(&models.PoolDevice{}, doomed).Error; err != nil {
		t.Fatalf("硬删那一行失败: %v", err)
	}
	if got := p.Status().SoftDeleted; got != 0 {
		t.Errorf("硬删后读数 = %d, want 0（判据必须只数 deleted_at 非空的行）", got)
	}
	// 活行仍然在位（顺手核对，免得上面那次删除把范围弄宽）
	var alive int64
	if err := db.DB.Model(&models.PoolDevice{}).Where("pool = ?", name).Count(&alive).Error; err != nil {
		t.Fatalf("数活行失败: %v", err)
	}
	if alive != 2 {
		t.Errorf("活行数 = %d, want 2", alive)
	}

	// 4) 跨池不串：别的池的软删行不进本池读数
	other := insertPoolRow(t, "sd_watch_other", "别人家的号", pool.StatusCold)
	if err := db.DB.Delete(&models.PoolDevice{}, other).Error; err != nil {
		t.Fatalf("软删另一个池的行失败: %v", err)
	}
	if got := p.Status().SoftDeleted; got != 0 {
		t.Errorf("别的池软删一行后，本池读数 = %d, want 0", got)
	}
}
