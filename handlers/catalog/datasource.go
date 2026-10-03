package catalog

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"loomproxy/base"
	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
	"loomproxy/utils"
)

type DatasourceItem struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Category  string           `json:"category"`
	MediaType string           `json:"media_type,omitempty"` // 源声明的默认媒介（tab 级随 search_tab 下发）
	GroupID   uint             `json:"group_id,omitempty"`
	Group     string           `json:"group,omitempty"` // 所属分组名：纯视图信息，下游客户端不必依赖（分组不是键）
	SearchTab []base.SearchTab `json:"search_tab"`
	Files     []string         `json:"files"`
}

type DatasourcesResponse struct {
	Code int              `json:"code"`
	Msg  string           `json:"msg"`
	Data []DatasourceItem `json:"data"`
}

type DatasourceHandler struct {
	base.BaseHandler
}

func NewDatasourceHandler(_ *base.APIConfig) base.Handler {
	h := &DatasourceHandler{
		BaseHandler: *base.NewBaseHandler(),
	}
	h.Path = "/datasources"
	h.Name = "datasources"
	h.Methods = []string{"GET"}
	h.QueryParams = []string{}
	h.Description = "列出所有可用数据源名称"
	h.Auth = true
	return h
}

var defaultSearchTabs = []base.SearchTab{
	{TabType: 3, BdID: "2", Name: "小说"},
}

func listDataFiles(sourceType string) []string {
	if sourceType == "" {
		return []string{}
	}
	dir := filepath.Join(conf.Config.DataDir, sourceType)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) == ".json" {
			files = append(files, name[:len(name)-5])
		}
	}
	sort.Strings(files)
	return files
}

func getFiles(sourceType string) []string {
	return listDataFiles(sourceType)
}

func (h *DatasourceHandler) Handle(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	// 从 params 中获取用户ID（由调用方注入）
	var userID uint
	if uid, ok := params["__uid"].(uint); ok {
		userID = uid
	}

	// 匿名用户与登录用户按 plan 类型分 key 缓存，避免同套餐用户重复缓存
	var cachePrefix string
	if userID > 0 {
		cachePrefix = "datasource:auth"
	} else {
		cachePrefix = "datasource:anon"
	}

	// 去掉 uid 参数，同套餐用户共享缓存；键格式为「前缀:散列」，
	// 前缀须可被 DelPrefix 识别（管理端写操作后批量失效，见 InvalidateDatasourcesCache）
	cleanParams := make(map[string]interface{}, 1)
	cleanParams["uid"] = userID
	cacheKey := cachePrefix + ":" + utils.CacheKey(cachePrefix, cleanParams)
	if cached, ok := utils.DefaultCache().Get(cacheKey); ok {
		return cached, nil
	}

	// 1. 查询所有启用的数据源
	var dataSources []models.DataSource
	query := db.DB.Where("status = 1").Order("sort_order ASC, id ASC")
	if err := query.Find(&dataSources).Error; err != nil {
		return nil, err
	}

	// 2. 按套餐过滤已授权的数据源（授权=限额行，见 gate/grant.go；匿名按免费版）
	{
		planID := gate.FreePlanID()
		if userID > 0 {
			var user models.User
			if err := db.DB.Preload("Plan").First(&user, userID).Error; err == nil {
				planID = gate.ResolvePlanForUser(&user).ID
			}
		}
		dataSources = gate.FilterByPlan(dataSources, planID)
	}

	// 3. 构建响应数据

	// 停用的组不下发组名（其源等同未分组）；组改动会走 InvalidateDatasourcesCache，此处只读一次
	var enabledGroups []models.SourceGroup
	db.DB.Where("status = 1").Find(&enabledGroups)
	groupNames := make(map[uint]string, len(enabledGroups))
	for _, g := range enabledGroups {
		groupNames[g.ID] = g.Name
	}

	data := make([]DatasourceItem, 0, len(dataSources))
	for _, ds := range dataSources {
		item := DatasourceItem{
			ID:       ds.Name,
			Name:     ds.DisplayName,
			Category: ds.Category,
		}
		if ds.GroupID != nil {
			if n, ok := groupNames[*ds.GroupID]; ok {
				item.GroupID = *ds.GroupID
				item.Group = n
			}
		}
		item.SearchTab = defaultSearchTabs
		// 源自有形态（多媒介/多站点）时按声明渲染，底座不做分类分支
		if meta, ok := base.GetSourceMeta(ds.Name); ok {
			item.MediaType = meta.MediaType
			if len(meta.SearchTabs) > 0 {
				item.SearchTab = meta.SearchTabs
			}
		}
		// 分类目录 data/{category}/*.json 存在时列出其文件（源自有的分类/元数据字典）
		item.Files = getFiles(ds.Category)
		data = append(data, item)
	}

	resp := DatasourcesResponse{
		Code: 0,
		Msg:  "ok",
		Data: data,
	}

	utils.DefaultCache().Set(cacheKey, resp)
	return resp, nil
}

// DatasourcesCachePrefix /datasources 响应缓存的键前缀（匿名与登录视图共用此前缀段）
const DatasourcesCachePrefix = "datasource:"

// InvalidateDatasourcesCache 使 /datasources 全部缓存视图（匿名 + 各登录用户）立即失效。
// 数据源 CRUD、套餐-数据源关联变更、用户套餐变更后调用；此前缓存仅按 CACHE_TTL
// 自然过期，管理端改动最长 5 分钟才对用户可见
func InvalidateDatasourcesCache() {
	utils.DefaultCache().DelPrefix(DatasourcesCachePrefix)
}

func init() {
	base.Register("datasources", NewDatasourceHandler, 0, map[string]interface{}{
		"type": "datasource",
	})
}
