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
	// 这份清单要按调用者的套餐裁剪，所以**命中白名单也要解析凭证**（待办清单 P100）：
	// 文档建议运维把 /datasources 加进 AUTH_WHITELIST 来匿名分发，那条做法过去会让
	// 所有已登录用户拿到免费版视图而毫无报错——按套餐裁剪那段代码从此走不到。
	// 匿名仍然可以拿（免强制不变），只是拿的是匿名那一份。
	h.IdentityOptional = true
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

	// 缓存键**按用户分**（下面把 uid 放进键的原料里），不是"同套餐共享"——那两句旧注释与代码相反，
	// 谁照它们去"简化"掉 uid，就会把 A 用户按套餐裁剪过的列表发给 B 用户（待办清单 P84 那一族：
	// 键少一段维度不是命中率低，是发错人）。用例钉子：`test/datasources_cache_key_test.go`。
	// 匿名与登录视图共用 `datasource:` 段，好让 `InvalidateDatasourcesCache` 一次清干净。
	var cachePrefix string
	if userID > 0 {
		cachePrefix = "datasource:auth"
	} else {
		cachePrefix = "datasource:anon"
	}

	// 键格式为「前缀:散列(原料)」，原料里必须有「这条响应属于谁」那一段：
	// 本 handler 的响应是按套餐裁剪过的，而**套餐是在查完缓存之后才解析的**——
	// 所以身份必须在键里，不能等到读出来之后再判。
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
	if err := db.DB.Where("status = 1").Find(&enabledGroups).Error; err != nil {
		db.LogReadFail("catalog_groups:source_groups", err)
	}
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
