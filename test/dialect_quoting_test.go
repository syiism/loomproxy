package test

// 方言守卫：生产代码与用例都不得在 SQL 里写死反引号。反引号是 MySQL/SQLite 方言，
// PostgreSQL 用双引号，写死的后果是「查询报错被吞掉、设置全读成空」（待办清单 P1）。
// 正确写法：条件用 map 形式（Where(map[string]interface{}{"key": k})），让 GORM 按 dialector 加引号。
// 行尾标 dialect-allow 的行是显式豁免（断言 GORM 生成 SQL 形态的用例）。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"loomproxy/models"
)

var quotedIdentPat = regexp.MustCompile("`[A-Za-z_][A-Za-z0-9_]*`")

var dialectScanDirs = []string{
	"app", "base", "cmd", "conf", "db", "gate", "handlers", "middleware",
	"models", "sources", "test", "testkit", "utils",
}

func TestNoHardcodedDialectQuoting(t *testing.T) {
	var hits []string
	for _, dir := range dialectScanDirs {
		root := filepath.Join("..", dir)
		if _, err := os.Stat(root); err != nil {
			continue // 该形态（骨架 / 携带数据源的分支）不带这个目录
		}
		if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "dialect_quoting_test.go") {
				return nil // 本文件的正则与对照组里就有反引号
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			for i, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "//") || strings.Contains(line, "dialect-allow") {
					continue
				}
				if ids := identsInDoubleQuoted(line); len(ids) > 0 {
					rel, _ := filepath.Rel("..", path)
					hits = append(hits, rel+":"+strconv.Itoa(i+1)+" 写了 "+strings.Join(ids, " "))
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("遍历 %s 失败: %v", root, err)
		}
	}
	sort.Strings(hits)
	if len(hits) > 0 {
		t.Fatalf("发现 %d 处写死反引号的 SQL（PostgreSQL 会语法报错），改让 GORM 按方言加引号：\n%s",
			len(hits), strings.Join(hits, "\n"))
	}
}

// TestSettingQueryQuotedPerDialect 给出「为什么 map 形式才叫方言正确」的直接证据：
// 同一张表在 postgres 方言下 DryRun 出的 SQL 用双引号，而写死反引号的那条把反引号带出去。
// DryRun + 关自动 ping 不连库，本机没装 PostgreSQL 也照样跑。
func TestSettingQueryQuotedPerDialect(t *testing.T) {
	pg, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=127.0.0.1 port=1 user=x password=x dbname=x sslmode=disable",
		PreferSimpleProtocol: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Skipf("本机无法初始化 postgres 方言（不连库）: %v", err)
	}

	var s models.SystemSetting
	mapSQL := pg.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Where(map[string]interface{}{"key": "rank_public_sources"}).First(&s)
	})
	if !strings.Contains(mapSQL, `"key"`) || strings.Contains(mapSQL, "`") {
		t.Errorf("map 形式在 PG 方言下应生成双引号，实得：%q", mapSQL)
	}
	hardSQL := pg.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Where("`key` = ?", "rank_public_sources").First(&s) // dialect-allow
	})
	if !strings.Contains(hardSQL, "`key`") {
		t.Errorf("对照组失效：写死反引号应当把反引号原样带进 SQL，实得：%q", hardSQL)
	}
	updateSQL := pg.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Model(&s).Where(map[string]interface{}{"key": "announcement"}).Update("value", "")
	})
	if strings.Contains(updateSQL, "`") {
		t.Errorf("UPDATE 条件在 PG 方言下不该出现反引号，实得：%q", updateSQL)
	}
}

// identsInDoubleQuoted 返回行内所有双引号字符串里的反引号标识符；跳过转义
func identsInDoubleQuoted(line string) []string {
	var out []string
	for i := 0; i < len(line); i++ {
		if line[i] != '"' {
			continue
		}
		var sb strings.Builder
		j := i + 1
		for j < len(line) {
			switch line[j] {
			case '\\':
				j += 2
				continue
			case '"':
				j = len(line)
			default:
				sb.WriteByte(line[j])
				j++
			}
		}
		out = append(out, quotedIdentPat.FindAllString(sb.String(), -1)...)
		i = j
	}
	return out
}
