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
	// 内容维度：由 base/legado 从规范化响应回填，没抽到就是空串（不建 index——
	// 面板是 contains LIKE 筛选，B-tree 用不上，只换来写放大）
	Keyword      string `gorm:"size:128" json:"keyword"`       // 搜索词
	BookName     string `gorm:"size:160" json:"book_name"`     // 书名
	ChapterTitle string `gorm:"size:255" json:"chapter_title"` // 章节标题
	// 标识：与名称同源，但正文/目录只带标识时名称可能反查不到（缓存未命中），
	// 此时名称留空而标识照记，事后靠它回填。旧行（升级前写入）这两列是 NULL。
	BookIdent    string `gorm:"size:512" json:"book_ident"`      // 书目标识（bookId 等）
	ChapterIdent string `gorm:"size:512" json:"chapter_ident"`   // 章节标识（itemId/章节 url 等）
	Media        string `gorm:"size:16;index" json:"media_type"` // 媒介：novel/audio/comic/video，空=未判定
	ResultCount  int    `gorm:"default:0" json:"result_count"`   // 结果条数（0 = 空结果，榜单据此统计）
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
