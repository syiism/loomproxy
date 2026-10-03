package db

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"loomproxy/conf"
	"loomproxy/models"
)

var DB *gorm.DB

func Init() error {
	if DB != nil {
		if sqlDB, err := DB.DB(); err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
		DB = nil
	}

	var dialector gorm.Dialector

	dbType := conf.Config.DBType
	switch dbType {
	case "postgres", "postgresql", "pg":
		dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			conf.Config.DBHost,
			conf.Config.DBPort,
			conf.Config.DBUser,
			conf.Config.DBPassword,
			conf.Config.DBName,
			conf.Config.DBSSLMode,
		)
		dialector = postgres.Open(dsn)

	case "mysql":
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
			conf.Config.DBUser,
			conf.Config.DBPassword,
			conf.Config.DBHost,
			conf.Config.DBPort,
			conf.Config.DBName,
		)
		dialector = mysql.Open(dsn)

	case "sqlite", "sqlite3", "":
		dbPath := filepath.Join(conf.Config.DataDir, conf.Config.DBName+".db")
		// WAL + busy_timeout + NORMAL 同步：并发写不再直接报 database is locked
		// （等待而非失败），崩溃恢复与读写并发也优于默认回滚日志模式
		dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL", dbPath)
		dialector = sqlite.Open(dsn)

	default:
		return fmt.Errorf("unsupported DB_TYPE: %s", dbType)
	}

	logLevel := logger.Warn
	if conf.Config.ServerLogLevel == "debug" {
		logLevel = logger.Info
	}

	// IgnoreRecordNotFoundError：可选查询（First/Take）无记录是常态，不打印错误
	gormLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logLevel,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)

	var err error
	DB, err = gorm.Open(dialector, &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		return fmt.Errorf("failed to connect database: %w", err)
	}

	// SQLite 单连接：database/sql 默认不限连接数，多连接下读事务升级为写事务
	// 会触发 SQLITE_BUSY_SNAPSHOT（busy_timeout 不重试该类冲突），串行化连接
	// 可彻底消除（GORM 官方对 sqlite 的建议）；进程内 SQLite 查询为微秒级，
	// 单连接不构成吞吐瓶颈
	if dbType == "sqlite" || dbType == "sqlite3" || dbType == "" {
		if sqlDB, err := DB.DB(); err == nil {
			sqlDB.SetMaxOpenConns(1)
		}
	}

	// 清理 user_roles 关联表中的重复记录（必须在 AutoMigrate 添加唯一索引之前执行）
	// 按方言选择 SQL：MySQL 支持 DELETE ... FROM 多表语法；
	// SQLite/PostgreSQL 用标准相关子查询（包裹一层派生表以兼容 MySQL 的同表限制）。
	// 全新库首次启动时表尚不存在，跳过避免误报错误日志
	if DB.Migrator().HasTable("user_roles") {
		if DB.Dialector.Name() == "mysql" {
			DB.Exec(`DELETE t1 FROM user_roles t1 INNER JOIN user_roles t2 WHERE t1.id > t2.id AND t1.user_id = t2.user_id AND t1.role_id = t2.role_id`)
		} else {
			DB.Exec(`DELETE FROM user_roles WHERE id NOT IN (SELECT min_id FROM (SELECT MIN(id) AS min_id FROM user_roles GROUP BY user_id, role_id) t)`)
		}
	}

	err = DB.AutoMigrate(
		&models.User{},
		&models.Role{},
		&models.UserRole{},
		&models.SystemSetting{},
		&models.QuotaPlan{},
		&models.QuotaLimit{},
		&models.UserQuota{},
		&models.AuthSession{},
		&models.ApiKey{},
		&models.QuotaCost{},
		&models.QuotaUsageLog{},
		&models.PoolDevice{},
		&models.UserQuotaOverride{},
		&models.UserSourceConfig{},
		&models.PlatformSourceConfig{},
		&models.DataSource{},
		&models.SourceGroup{},
		// 旧「套餐-数据源关联」表不再建：授权与限额是同一行（待办清单 P34），
		// 全新库压根不该有它。存量库（P34 之前升上来的）里它还在，由 seed 的 alignPlanGrants
		// 一次性搬进限额表后由运维带备份 DROP——搬完之前不能停读，否则整套授权会凭空消失（P34 的新语义：没有那行 = 没权限）。
		&models.QuotaCostPlan{},
		&models.ApiCallLog{},
		&models.ApiCallStat{},
		&models.RedemptionCode{},
		&models.RedemptionLog{},
		&models.BlockedIP{},
		&models.VerificationCode{},
	)
	if err != nil {
		return fmt.Errorf("failed to auto migrate: %w", err)
	}

	// 组概念已移除（2026-08-09）：删除历史组表（表不存在时忽略错误）
	_ = DB.Migrator().DropTable("quota_source_groups")

	// 卡密自 2026-09-29 起按明文落库（换取码可随时查回，见 models.RedemptionCode 与
	// handlers/admin/redeem.go），因此这里**不做任何启动期哈希**：曾有段「明文 → SHA-256」的
	// 存量迁移残留，每次重启都会把新生成的明文码重新哈希掉，与明文策略相反，也让
	// tools/migrate-cards 的回写活不过一次重启（2026-10-01 移除）。
	// 历史哈希行的兼容在兑换侧：明文未命中时按哈希回落匹配（handlers/userconfig/redeem.go）。

	if err := Seed(DB); err != nil {
		log.Printf("WARNING: Seed data failed: %v", err)
	}

	log.Printf("Database connected (type=%s)", dbType)
	return nil
}
