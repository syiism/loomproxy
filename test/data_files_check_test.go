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
	"os"
	"path/filepath"
	"testing"

	"loomproxy/conf"
	"loomproxy/handlers/catalog"
	"loomproxy/testkit/fakesource"
)

func writeDict(t *testing.T, root, dir, name, body string) {
	t.Helper()
	full := filepath.Join(root, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatalf("建字典目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(full, name+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("写字典失败: %v", err)
	}
}

func TestCheckDeclaredDataFiles(t *testing.T) {
	// 装配优先：`conf.Config` 与数据源注册表都是装配时才建好的全局态
	newTestServer(t)

	dir := t.TempDir()
	prev := conf.Config.DataDir
	conf.Config.DataDir = dir
	t.Cleanup(func() { conf.Config.DataDir = prev })

	// 夹具里只有 fake_b 声明了 DataFiles（分类目录 fake，文件 fake_dict）
	missing := catalog.CheckDeclaredDataFiles(dir)
	if len(missing) != 1 {
		t.Fatalf("空目录下应点出 1 条缺位，实得 %+v", missing)
	}
	m := missing[0]
	if m.Dir != "fake" || m.File != fakesource.DictFile || m.Reason != "missing" {
		t.Errorf("缺位读数不对：dir=%q file=%q reason=%q（目录应取 Category 而不是数据源码）", m.Dir, m.File, m.Reason)
	}

	// 放好之后必须归零
	writeDict(t, dir, "fake", fakesource.DictFile, `{"k":"v"}`)
	if got := catalog.CheckDeclaredDataFiles(dir); len(got) != 0 {
		t.Fatalf("字典已就位仍报缺位：%+v", got)
	}

	// 半截文件：读得到、不是合法 JSON —— 与「没有」同类，但原因要分开
	writeDict(t, dir, "fake", fakesource.DictFile, `{"k":`)
	got := catalog.CheckDeclaredDataFiles(dir)
	if len(got) != 1 || got[0].Reason != "invalid_json" {
		t.Fatalf("半截文件被判成在位（判据退化成了文件存在？）：%+v", got)
	}

	// DATA_DIR 整个不存在：不 panic，按缺位处理
	if got := catalog.CheckDeclaredDataFiles(filepath.Join(dir, "no-such-root")); len(got) != 1 {
		t.Errorf("根目录不存在时应报缺位而非崩或沉默，实得 %+v", got)
	}
}
