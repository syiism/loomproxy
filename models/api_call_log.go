package models

import "time"

// ApiCallLog 数据源接口调用明细（监控用，与额度计费无关）。
// 由内存环形缓冲淘汰时批量落库 + 服务关停时兜底落库。
// 保留期由 MONITOR_RETENTION_DAYS 决定：默认 0=永久保留且不清理，>0 时写入方顺带清理。
type ApiCallLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"size:64;index" json:"username"` // 调用者用户名（API Key/匿名为空）
	IP        string    `gorm:"size:64" json:"ip"`             // 调用者客户端 IP
	Source    string    `gorm:"size:32;index" json:"source"`   // 数据源码
	Action    string    `gorm:"size:32" json:"action"`         // 接口动作，如 search
	Status    int       `json:"status"`                        // HTTP 状态码
	LatencyMs int64     `json:"latency_ms"`                    // 耗时（毫秒）
	CreatedAt time.Time `gorm:"index" json:"created_at"`       // 调用时间
}

func (ApiCallLog) TableName() string {
	return "api_call_logs"
}

// ApiCallStat 接口调用次数的永久归档（纪念用）：
// 历史明细超过保留期被清理前，按 数据源/接口 聚合累加到此表，
// 明细会过期，累计次数永不删除。
type ApiCallStat struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Source         string    `gorm:"size:32;uniqueIndex:idx_acs_source_action;not null" json:"source"`
	Action         string    `gorm:"size:32;uniqueIndex:idx_acs_source_action;not null" json:"action"`
	Total          int64     `gorm:"default:0" json:"total"`
	Success        int64     `gorm:"default:0" json:"success"`
	Failed         int64     `gorm:"default:0" json:"failed"`
	TotalLatencyMs int64     `gorm:"default:0" json:"total_latency_ms"`
	MaxLatencyMs   int64     `gorm:"default:0" json:"max_latency_ms"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (ApiCallStat) TableName() string {
	return "api_call_stats"
}
