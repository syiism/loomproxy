package test

// 启动期的静态字典在位性核对（待办清单 P51 的 ①，机制来自分支侧 S39 的现场）。
//
// 这条防的是「不报错的错」：字典是**部署产物**，装进 `DATA_DIR/<分类>/` 这一步没有任何机制保证会被做到，
// 而缺位的后果是发现页一片空列表、监控里全是 200。七猫三源的 `data/qm/` 就从上线起空着好几轮，
// 唯一的信号是源自己打的一句 `log.Printf`，且 `sync.Once` 之后不再重说。
//
// 三段断言各钉一件事：
//   ① 缺位要被点出来（否则核对是空转）；
//   ② 放好之后要归零（否则每次都刷屏，而下一个人就又开始无视 ERROR）；
//   ③ **半截文件要在册且原因变成 `invalid_json`**——这条才真正钉住判据是 `json.Valid` 还是 `os.Stat`。
//      只验 ①② 的话，把判据换成「文件存在即可」用例全绿，而生产上「拷坏了」这一类照样漏。

import (
	"loomproxy/conf"
	"loomproxy/handlers/catalog"
	"loomproxy/testkit"
	"loomproxy/testkit/fakesource"
	"path/filepath"
	"testing"
)

func writeDict(t *testing.T, root, dir, name, body string) {
	t.Helper()
	testkit.WriteDict(t, root, dir, name, body)
}

func TestCheckDeclaredDataFiles(t *testing.T) {
	// 装配优先：`conf.Config` 与数据源注册表都是装配时才建好的全局态
	newTestServer(t)

	dir := t.TempDir()
	prev := conf.Config.DataDir
	conf.Config.DataDir = dir
	t.Cleanup(func() { conf.Config.DataDir = prev })

	// 携带形态里除夹具外还有真实源声明 DataFiles（uxx/xmly/qm），断言必须对着
	// **fake_b 那一条**做而不是对清单整体——「全集相等」的写法在任何多源部署上必红
	//（同族教训：`/endpoints` 集合相等断言，骨架 9fd43a2 修过一次）
	findFake := func(rows []catalog.MissingDataFile) *catalog.MissingDataFile {
		for i := range rows {
			if rows[i].Dir == "fake" && rows[i].File == fakesource.DictFile {
				return &rows[i]
			}
		}
		return nil
	}
	missing := catalog.CheckDeclaredDataFiles(dir)
	if len(missing) == 0 {
		t.Fatal("空目录下应点出缺位，实得 0 条（核对空转？）")
	}
	if m := findFake(missing); m == nil {
		t.Fatalf("缺位清单里没有 fake_b 的字典：%+v", missing)
	} else if m.Reason != "missing" {
		t.Errorf("缺位读数不对：dir=%q file=%q reason=%q（目录应取 Category 而不是数据源码）", m.Dir, m.File, m.Reason)
	}

	// 放好之后 fake_b 那条必须消失（其余源的缺位与这条用例无关）
	writeDict(t, dir, "fake", fakesource.DictFile, `{"k":"v"}`)
	if got := catalog.CheckDeclaredDataFiles(dir); findFake(got) != nil {
		t.Fatalf("字典已就位仍报缺位：%+v", got)
	}

	// 半截文件：读得到、不是合法 JSON —— 与「没有」同类，但原因要分开
	writeDict(t, dir, "fake", fakesource.DictFile, `{"k":`)
	got := catalog.CheckDeclaredDataFiles(dir)
	if m := findFake(got); m == nil || m.Reason != "invalid_json" {
		t.Fatalf("半截文件被判成在位（判据退化成了文件存在？）：%+v", got)
	}

	// DATA_DIR 整个不存在：不 panic，按缺位处理（缺位清单非空即可，条数随部署声明的源数走）
	if got := catalog.CheckDeclaredDataFiles(filepath.Join(dir, "no-such-root")); len(got) == 0 {
		t.Errorf("根目录不存在时应报缺位而非崩或沉默，实得 0 条")
	}
}
