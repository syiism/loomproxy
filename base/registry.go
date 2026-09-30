package base

import (
	"fmt"
	"sort"
	"sync"
)

// SourceMeta 数据源级声明：源包在 init() 里调 RegisterSource 一次性登记，
// 是路由对账与 seed 播种的唯一事实来源（app.go / seed.go 不再出现按源硬编码清单）。
type SourceMeta struct {
	Code        string   `json:"code"`                  // 数据源码（data_sources.name / 路由前缀 / quota group_code）
	Display     string   `json:"display"`               // 显示名（data_sources.display_name）
	Category    string   `json:"category"`              // 分类
	Description string   `json:"description,omitempty"` // 描述
	SortOrder   int      `json:"sort_order"`            // 排序（datasources 列表顺序）
	Status      int      `json:"status"`                // 1 启用 / 0 禁用（播种用，缺省 1）
	Actions     []string `json:"actions"`               // 声明的动作集（路由对账 + quota costs 播种）
	// FixedBaseURL 声明该源上游地址写死在源实现中（如签名接口、HTML 抓取站）：
	// 路由不接收 baseUrl 参数，也不做用户/平台配置回落与 SSRF 校验
	FixedBaseURL bool `json:"fixed_base_url"`
}

var (
	sourceMu      sync.Mutex
	sourceMetas   = map[string]SourceMeta{}
	sourceOrdered []string // 登记顺序（消耗侧按 SortOrder 稳定排序）
)

// RegisterSource 登记数据源级声明（同 Code 重复登记报错）。
func RegisterSource(m SourceMeta) error {
	if m.Code == "" || len(m.Actions) == 0 {
		return fmt.Errorf("RegisterSource: code 与 actions 必填")
	}
	if m.Status == 0 {
		m.Status = 1
	}
	sourceMu.Lock()
	defer sourceMu.Unlock()
	if _, exists := sourceMetas[m.Code]; exists {
		return fmt.Errorf("数据源重复声明: %s", m.Code)
	}
	sourceMetas[m.Code] = m
	sourceOrdered = append(sourceOrdered, m.Code)
	return nil
}

// DeclaredSources 返回全部数据源声明（按 SortOrder 升序稳定输出）。
func DeclaredSources() []SourceMeta {
	sourceMu.Lock()
	defer sourceMu.Unlock()
	out := make([]SourceMeta, 0, len(sourceOrdered))
	for _, code := range sourceOrdered {
		out = append(out, sourceMetas[code])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SortOrder < out[j].SortOrder })
	return out
}

// GetSourceMeta 取单个数据源声明。
func GetSourceMeta(code string) (SourceMeta, bool) {
	sourceMu.Lock()
	defer sourceMu.Unlock()
	m, ok := sourceMetas[code]
	return m, ok
}

// HandlerInfo 处理器注册信息（泛型版本，编译时确定类型，无运行时断言）
type HandlerInfo[T any] struct {
	Handler    T
	Priority   int
	Metadata   map[string]interface{}
	IsInstance bool
	Factory    func(*APIConfig) T
}

// Registry 泛型处理器注册表。
// 底层使用 sync.Map：注册集中在启动阶段的 init()（写一次），运行时全部为并发读取，
// 读写分离无锁竞争，契合本项目的访问模式。
type Registry[T any] struct {
	store sync.Map // name -> *HandlerInfo[T]
}

func NewRegistry[T any]() *Registry[T] {
	return &Registry[T]{}
}

// Register 注册处理器工厂（延迟实例化）
func (r *Registry[T]) Register(name string, factory func(*APIConfig) T, priority int, metadata map[string]interface{}) error {
	if _, exists := r.store.Load(name); exists {
		return ErrAlreadyRegistered
	}
	r.store.Store(name, &HandlerInfo[T]{
		Priority:   priority,
		Metadata:   metadata,
		IsInstance: false,
		Factory:    factory,
	})
	return nil
}

// RegisterInstance 注册处理器实例
func (r *Registry[T]) RegisterInstance(name string, instance T, priority int, metadata map[string]interface{}) error {
	if _, exists := r.store.Load(name); exists {
		return ErrAlreadyRegistered
	}
	r.store.Store(name, &HandlerInfo[T]{
		Handler:    instance,
		Priority:   priority,
		Metadata:   metadata,
		IsInstance: true,
	})
	return nil
}

// Get 获取处理器（工厂型每次新建实例）
func (r *Registry[T]) Get(name string) (T, error) {
	return r.GetOrCreate(name, nil)
}

// GetOrCreate 获取处理器，工厂型可传入配置
func (r *Registry[T]) GetOrCreate(name string, config *APIConfig) (T, error) {
	info, ok := r.getInfo(name)
	if !ok {
		var zero T
		return zero, ErrHandlerNotFound
	}
	if info.IsInstance {
		return info.Handler, nil
	}
	if info.Factory != nil {
		return info.Factory(config), nil
	}
	var zero T
	return zero, ErrInvalidHandler
}

// GetInfo 获取注册信息
func (r *Registry[T]) GetInfo(name string) (*HandlerInfo[T], bool) {
	return r.getInfo(name)
}

func (r *Registry[T]) getInfo(name string) (*HandlerInfo[T], bool) {
	if v, ok := r.store.Load(name); ok {
		return v.(*HandlerInfo[T]), true
	}
	return nil, false
}

// All 返回全部处理器（顺序不定）
func (r *Registry[T]) All() []T {
	var result []T
	r.store.Range(func(_, v interface{}) bool {
		if h, ok := instantiate(v.(*HandlerInfo[T]), nil); ok {
			result = append(result, h)
		}
		return true
	})
	return result
}

// AllClasses 返回全部工厂（不含实例）
func (r *Registry[T]) AllClasses() []func(*APIConfig) T {
	var result []func(*APIConfig) T
	r.store.Range(func(_, v interface{}) bool {
		info := v.(*HandlerInfo[T])
		if !info.IsInstance && info.Factory != nil {
			result = append(result, info.Factory)
		}
		return true
	})
	return result
}

// AllInstances 返回全部实例
func (r *Registry[T]) AllInstances() []T {
	var result []T
	r.store.Range(func(_, v interface{}) bool {
		info := v.(*HandlerInfo[T])
		if info.IsInstance {
			result = append(result, info.Handler)
		}
		return true
	})
	return result
}

// AllSorted 返回按优先级降序的全部处理器
func (r *Registry[T]) AllSorted() []T {
	var infos []*HandlerInfo[T]
	r.store.Range(func(_, v interface{}) bool {
		infos = append(infos, v.(*HandlerInfo[T]))
		return true
	})
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Priority > infos[j].Priority
	})
	var result []T
	for _, info := range infos {
		if h, ok := instantiate(info, nil); ok {
			result = append(result, h)
		}
	}
	return result
}

// FilterByMetadata 按元数据键值过滤
func (r *Registry[T]) FilterByMetadata(key string, value interface{}) []T {
	var result []T
	r.store.Range(func(_, v interface{}) bool {
		info := v.(*HandlerInfo[T])
		if mv, ok := info.Metadata[key]; ok && mv == value {
			if h, ok := instantiate(info, nil); ok {
				result = append(result, h)
			}
		}
		return true
	})
	return result
}

// Remove 移除注册项
func (r *Registry[T]) Remove(name string) bool {
	if _, ok := r.store.Load(name); ok {
		r.store.Delete(name)
		return true
	}
	return false
}

// Clear 清空注册表
func (r *Registry[T]) Clear() {
	r.store.Range(func(k, _ interface{}) bool {
		r.store.Delete(k)
		return true
	})
}

// Routes 返回 path -> 路由信息 的映射
func (r *Registry[T]) Routes() map[string]map[string]interface{} {
	routes := make(map[string]map[string]interface{})
	r.store.Range(func(k, v interface{}) bool {
		name := k.(string)
		info := v.(*HandlerInfo[T])
		h, ok := instantiate(info, nil)
		if !ok {
			return true
		}
		if path := routePathOf(h); path != "" {
			routes[path] = map[string]interface{}{
				"handler":    h,
				"methods":    routeMethodsOf(h),
				"name":       name,
				"isInstance": info.IsInstance,
			}
		}
		return true
	})
	return routes
}

// instantiate 从注册信息得到处理器实例
func instantiate[T any](info *HandlerInfo[T], config *APIConfig) (T, bool) {
	if info.IsInstance {
		return info.Handler, true
	}
	if info.Factory != nil {
		return info.Factory(config), true
	}
	var zero T
	return zero, false
}

// 以下两个辅助把 Routes 对 Handler 接口的依赖与泛型实现解耦，
// T 约束为 any 时通过接口断言访问路由元数据。
func routePathOf(v interface{}) string {
	if h, ok := v.(interface{ GetPath() string }); ok {
		return h.GetPath()
	}
	return ""
}

func routeMethodsOf(v interface{}) []string {
	if h, ok := v.(interface{ GetMethods() []string }); ok {
		return h.GetMethods()
	}
	return nil
}
