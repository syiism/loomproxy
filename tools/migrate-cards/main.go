// migrate-cards 一次性迁移工具：把 2026-09-29 前（SHA-256 哈希落库时期）生成的
// 卡密原件回写为明文。卡密清单运行时读取（cwd 或二进制同目录的 codes.txt，
// 每行一条，卡密属敏感信息故不入库/不编译期嵌入）。
//
// 用法：在部署目录（含 .env 与 codes.txt）执行 ./migrate-cards [-dry]
//
//	-dry          只预演不写库
//	数据库连接读 cwd/.env 的 DB_HOST/DB_PORT/DB_USER/DB_PASSWORD/DB_NAME（环境变量优先）
//
// 仅支持 MySQL（与生产一致）；对每条卡密：按哈希定位行 → 校验明文无冲突 → 回写。
package main

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	dry := flag.Bool("dry", false, "只预演不写库")
	flag.Parse()

	codes, err := loadCodes()
	if err != nil {
		fmt.Println("读取卡密清单失败:", err)
		os.Exit(1)
	}
	if len(codes) == 0 {
		fmt.Println("卡密清单为空")
		os.Exit(1)
	}
	fmt.Printf("待迁移卡密 %d 条%s\n\n", len(codes), map[bool]string{true: "（dry-run 预演）", false: ""}[*dry])

	db, err := openDB()
	if err != nil {
		fmt.Println("连接数据库失败:", err)
		os.Exit(1)
	}
	defer db.Close()

	var ok, already, missing, conflict int
	for _, code := range codes {
		hash := sha256Hex(code)
		var id int64
		var status int
		err := db.QueryRow("SELECT id, status FROM redemption_codes WHERE code = ?", hash).Scan(&id, &status)
		switch {
		case err == sql.ErrNoRows:
			// 哈希行不存在：可能已迁移过（明文行在）或码不存在
			err2 := db.QueryRow("SELECT id FROM redemption_codes WHERE code = ?", code).Scan(&id)
			if err2 == nil {
				fmt.Printf("  [=] %s  已是明文，跳过\n", code)
				already++
			} else {
				fmt.Printf("  [!] %s  未找到（码不存在，或非本库生成的卡密）\n", code)
				missing++
			}
		case err != nil:
			fmt.Printf("  [!] %s  查询失败: %v\n", code, err)
			missing++
		default:
			// 明文冲突防护（唯一索引下不应发生）
			var dup int64
			_ = db.QueryRow("SELECT COUNT(*) FROM redemption_codes WHERE code = ? AND id <> ?", code, id).Scan(&dup)
			if dup > 0 {
				fmt.Printf("  [!] %s  已存在同明文的其他行（id=%d），跳过请人工检查\n", code, dup)
				conflict++
				continue
			}
			if *dry {
				fmt.Printf("  [✓] %s  → 命中哈希行 id=%d（status=%d），将回写明文\n", code, id, status)
				ok++
				continue
			}
			if _, err := db.Exec("UPDATE redemption_codes SET code = ? WHERE id = ?", code, id); err != nil {
				fmt.Printf("  [!] %s  回写失败: %v\n", code, err)
				missing++
				continue
			}
			fmt.Printf("  [✓] %s  已迁移（原哈希行 id=%d，status=%d）\n", code, id, status)
			ok++
		}
	}

	var legacyLeft int64
	_ = db.QueryRow("SELECT COUNT(*) FROM redemption_codes WHERE LENGTH(code) = 64").Scan(&legacyLeft)
	fmt.Printf("\n汇总：迁移 %d / 已是明文 %d / 未找到 %d / 冲突 %d；剩余 64 位哈希行 %d 条（无原件不可迁移，兑换时走哈希回落）\n",
		ok, already, missing, conflict, legacyLeft)
}

// loadCodes 卡密清单（运行时读取）：cwd 或二进制同目录的 codes.txt。
// 每行一条，自动大写、去空白，跳过空行与 # 注释。
func loadCodes() ([]string, error) {
	path := "codes.txt"
	if _, err := os.Stat(path); err != nil {
		if exe, e := os.Executable(); e == nil {
			alt := filepath.Join(filepath.Dir(exe), "codes.txt")
			if _, e2 := os.Stat(alt); e2 == nil {
				path = alt
			}
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("未找到卡密清单 %s：请在同目录创建 codes.txt（每行一条卡密）", path)
	}
	defer f.Close()
	fmt.Println("使用卡密清单:", path)
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return normalize(lines), nil
}

func normalize(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, l := range in {
		l = strings.ToUpper(strings.TrimSpace(l))
		if l == "" || strings.HasPrefix(l, "#") || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// openDB 连接配置：环境变量优先，其次 cwd/.env（与主程序加载顺序一致）。仅支持 MySQL。
func openDB() (*sql.DB, error) {
	env := loadEnvFile(".env")
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		if v := env[k]; v != "" {
			return v
		}
		return def
	}
	if t := get("DB_TYPE", "mysql"); t != "mysql" {
		return nil, fmt.Errorf("DB_TYPE=%s：本工具仅支持 MySQL（与生产部署一致）", t)
	}
	host := get("DB_HOST", "127.0.0.1")
	port := get("DB_PORT", "3306")
	user := get("DB_USER", "root")
	pass := get("DB_PASSWORD", "")
	name := get("DB_NAME", "loomproxy")
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Local", user, pass, host, port, name)
	fmt.Printf("连接 %s@%s:%s/%s\n", user, host, port, name)
	return sql.Open("mysql", dsn)
}

// loadEnvFile 极简 .env 解析：KEY=VALUE 按首个人切分，不覆盖已存在环境变量。
func loadEnvFile(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		k, v := strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		if _, exists := os.LookupEnv(k); !exists {
			out[k] = v
		}
	}
	return out
}
