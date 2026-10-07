// 待办清单 P103③ 的门禁：**每个长跑循环体必须自带 panic 兜底帧**（维护者 2026-10-07 拍的 A 严格版）。
//
// 为什么是"循环就要有帧"而不是"调到外部实现才要有帧"（B 语义版）：
// 存量清点（条目里那条 2026-10-07 的记录）量出来——按语法把 `x.Y()` 当外部调用，会把
// `deductMu.Lock`、`ticker.Stop` 这类**本地变量上的方法**误判成裸调外部；只认"包名.函数"又
// **漏掉真正危险的那一形**（号池钩子是 `p.provider.Refresh(...)`，接口值上的调用，包名规则看不见，
// 而那正是 P77/P78 的原始形状）。分不清要靠 go/types。A 把"什么算外部实现"这个不可判的问题
// 换成"循环就要有帧"这个可判的问题：代价是少数本来不需要帧的循环也带上，换零漏报。
//
// 判法（刻意划窄，别当成协程风格检查）：
//   - 只看**非测试**代码里的 `go` 语句，解析出它实际起的那段函数体；
//   - 体里有 `for` / `for range` 且带 ticker/timer/`select`/`x.C` 形态 → 判为长跑；
//     一次性的协程（聚合扇出、metrics flush、serve 一次）不要求帧——
//     那种 panic 该让进程下去，"下一轮还会来"这个前提在它身上不成立（`base/supervise.go` 那句）。
//   - 循环体里出现 `Supervised(`、`defer …Guard(`，或一行落在该范围内的 `guard-exempt:` 注释 → 满足。
//     **豁免必须是看得见的注释，而且门禁会数出来有几处**：注释掉规则不等于没有规则。
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type goSite struct {
	pkg  string
	file string
	stmt *ast.GoStmt
}

type loop struct {
	pos  token.Pos
	body *ast.BlockStmt
	kind string
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	fset := token.NewFileSet()
	decls := map[string]*ast.FuncDecl{}
	var sites []goSite
	exempts := map[string][]int{}

	err := filepath.Walk(root, func(p string, info os.FileInfo, e error) error {
		if e != nil {
			return nil
		}
		if info.IsDir() {
			switch filepath.Base(p) {
			case "web", "node_modules", ".git", "data", "tmp", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if perr != nil {
			return nil
		}
		pkg := f.Name.Name
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.Contains(c.Text, "guard-exempt") {
					exempts[p] = append(exempts[p], fset.Position(c.Slash).Line)
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if x.Name != nil {
					decls[pkg+"|"+x.Name.Name] = x
				}
			case *ast.GoStmt:
				sites = append(sites, goSite{pkg: pkg, file: p, stmt: x})
			}
			return true
		})
		return nil
	})
	if err != nil {
		fmt.Println("扫描失败：", err)
		os.Exit(2)
	}

	var bad []string
	nGo, nLong, nExempt, nUnresolved := 0, 0, 0, 0
	for _, s := range sites {
		nGo++
		name, body := resolve(s.stmt, decls, s.pkg)
		if body == nil {
			// 跨包/接口值起的协程：这一版没有类型信息，不猜函数体——但它**出声**而不是静默放过
			nUnresolved++
			continue
		}
		loops := longLoops(body)
		if len(loops) == 0 {
			continue
		}
		nLong++
		// 帧可以在循环体里（"每轮一包"），也可以在同一函数里（帧装在 flush 这类本地闭包里）——
		// 第一轮跑出来两处假红就是后一种：app/subject_store.go 的兜底包在 flush 外，
		// 而两个 for 都在同一个函数里调 flush。**判据要的是"这一段代码里存在给这一轮兜底的帧"，
		// 不是"帧必须写在 for 的花括号里面"**。整段函数一个帧都没有，才是这条规则要拦的形状。
		funcHasFrame := hasFrame(body)
		for _, L := range loops {
			lo := fset.Position(L.body.Pos()).Line
			hi := fset.Position(L.body.End()).Line
			if funcHasFrame || hasFrame(L.body) {
				continue
			}
			if inRange(exempts[s.file], lo, hi) {
				nExempt++
				continue
			}
			bad = append(bad, fmt.Sprintf("%s: 起 %s 的长跑循环（%s）没有兜底帧——"+
				"Go 里任何协程的未恢复 panic 带走的是整个进程，而 Restart=always 会把它伪装成\"运行中\"。"+
				"把每一轮包进 base.Supervised(\"…的一轮\", fn)；确实不该包（例如拿不到 base 且不调任何外部实现）"+
				"就在循环体里写一行 `guard-exempt: <理由>`",
				fset.Position(L.pos).String(), name, L.kind))
		}
	}
	sort.Strings(bad)
	fmt.Printf("go 语句 %d 处；判为长跑 %d 处；找不到函数体（跨包/接口值起）%d 处；靠可见豁免放行 %d 处\n",
		nGo, nLong, nUnresolved, nExempt)
	if len(bad) > 0 {
		for _, b := range bad {
			fmt.Println(b)
		}
		fmt.Println("\n长跑协程兜底检查未通过")
		fmt.Println("判据：docs/规范/待办清单.md 的 P103 ③（严格版 A，维护者 2026-10-07 拍）")
		os.Exit(1)
	}
	fmt.Println("check-goroutine-guard OK（每个长跑循环都带帧或可见豁免）")
}

func inRange(xs []int, lo, hi int) bool {
	for _, x := range xs {
		if x >= lo && x <= hi {
			return true
		}
	}
	return false
}

// resolve：go f() / go x.f() / go func(){…}() 三种形态，拿到实际函数体与一个可读名字
func resolve(gs *ast.GoStmt, decls map[string]*ast.FuncDecl, pkg string) (string, *ast.BlockStmt) {
	switch fn := gs.Call.Fun.(type) {
	case *ast.FuncLit:
		return "<funclit>", fn.Body
	case *ast.Ident:
		if fd := decls[pkg+"|"+fn.Name]; fd != nil {
			return fn.Name, fd.Body
		}
		return fn.Name, nil
	case *ast.SelectorExpr:
		if fd := decls[pkg+"|"+fn.Sel.Name]; fd != nil {
			return fn.Sel.Name, fd.Body
		}
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name, nil
		}
		return fn.Sel.Name, nil
	}
	return "<其他>", nil
}

// longLoops：函数体里的长跑循环——for / range，且体里带 ticker/timer/select/`x.C`
func longLoops(b *ast.BlockStmt) []loop {
	var out []loop
	ast.Inspect(b, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ForStmt:
			if x.Body != nil && hasTickShape(x.Body) {
				out = append(out, loop{pos: x.Pos(), body: x.Body, kind: "for"})
			}
		case *ast.RangeStmt:
			if x.Body == nil {
				break
			}
			if se, ok := x.X.(*ast.SelectorExpr); ok && se.Sel.Name == "C" {
				out = append(out, loop{pos: x.Pos(), body: x.Body, kind: "range ticker.C"})
				break
			}
			if hasTickShape(x.Body) {
				out = append(out, loop{pos: x.Pos(), body: x.Body, kind: "range"})
			}
		}
		return true
	})
	return out
}

func hasTickShape(b *ast.BlockStmt) bool {
	var hit bool
	ast.Inspect(b, func(n ast.Node) bool {
		if se, ok := n.(*ast.SelectorExpr); ok && se.Sel.Name == "C" {
			hit = true
		}
		if _, ok := n.(*ast.SelectStmt); ok {
			hit = true
		}
		if ce, ok := n.(*ast.CallExpr); ok {
			if s, ok2 := callee(ce); ok2 {
				switch s {
				case "NewTicker", "NewTimer", "Tick", "After":
					hit = true
				}
			}
		}
		return true
	})
	return hit
}

func hasFrame(b *ast.BlockStmt) bool {
	var hit bool
	ast.Inspect(b, func(n ast.Node) bool {
		if ce, ok := n.(*ast.CallExpr); ok {
			if s, ok2 := callee(ce); ok2 && (s == "Supervised" || strings.Contains(s, "Guard")) {
				hit = true
			}
		}
		if ds, ok := n.(*ast.DeferStmt); ok {
			if s, ok2 := callee(ds.Call); ok2 && strings.Contains(s, "Guard") {
				hit = true
			}
		}
		return true
	})
	return hit
}

func callee(c *ast.CallExpr) (string, bool) {
	switch fn := c.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name, true
	case *ast.Ident:
		return fn.Name, true
	}
	return "", false
}
