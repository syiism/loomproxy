package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loomproxy/base"
	"loomproxy/conf"
)

type FileInfo struct {
	Name        string `json:"name"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	Description string `json:"description"`
}

type SourceInfo struct {
	FileCount int        `json:"file_count"`
	Files     []FileInfo `json:"files"`
}

type SourcesResponse struct {
	Count   int                   `json:"count"`
	Sources map[string]SourceInfo `json:"sources"`
}

type FileListResponse struct {
	Source string     `json:"source"`
	Count  int        `json:"count"`
	Files  []FileInfo `json:"files"`
}

type ErrorResponse struct {
	Error     string   `json:"error"`
	Available []string `json:"available"`
}

type DataFilesHandler struct {
	base.BaseHandler
	dataRoot string
}

func NewDataFilesHandler(_ *base.APIConfig) base.Handler {
	h := &DataFilesHandler{
		BaseHandler: *base.NewBaseHandler(),
	}
	h.Path = "/data"
	h.Name = "data_files"
	h.Methods = []string{"GET"}
	h.QueryParams = []string{}
	h.Description = "静态数据文件。支持 GET /data（总览）、GET /data/<分类>（目录列表）、GET /data/<分类>/<文件>.json（文件直出）"
	h.Auth = false
	h.dataRoot = conf.Config.DataDir
	return h
}

func (h *DataFilesHandler) dataDir(source string) string {
	return filepath.Join(h.dataRoot, source)
}

func (h *DataFilesHandler) listSources() []string {
	var sources []string
	info, err := os.Stat(h.dataRoot)
	if err != nil || !info.IsDir() {
		return sources
	}
	entries, err := os.ReadDir(h.dataRoot)
	if err != nil {
		return sources
	}
	for _, entry := range entries {
		if entry.IsDir() {
			sources = append(sources, entry.Name())
		}
	}
	sort.Strings(sources)
	return sources
}

func (h *DataFilesHandler) fileInfo(path string) FileInfo {
	stat, _ := os.Stat(path)
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	stem := name[:len(name)-len(ext)]
	size := int64(0)
	if stat != nil {
		size = stat.Size()
	}
	return FileInfo{
		Name:        stem,
		Filename:    name,
		Size:        size,
		Description: base.DescribeDataFile(stem),
	}
}

func (h *DataFilesHandler) listFiles(source string) []FileInfo {
	d := h.dataDir(source)
	info, err := os.Stat(d)
	if err != nil || !info.IsDir() {
		return []FileInfo{}
	}
	matches, err := filepath.Glob(filepath.Join(d, conf.Config.DataFileGlob))
	if err != nil {
		return []FileInfo{}
	}
	sort.Strings(matches)
	var result []FileInfo
	for _, p := range matches {
		result = append(result, h.fileInfo(p))
	}
	return result
}

func (h *DataFilesHandler) readFileRaw(source, name string) ([]byte, error) {
	path := filepath.Join(h.dataDir(source), name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (h *DataFilesHandler) Handle(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	var result interface{}

	parts, _ := params["_datafile_parts"].([]string)

	if len(parts) >= 1 {
		source := parts[0]
		source = strings.TrimSuffix(source, ".json")

		if len(parts) >= 2 {
			// /data/<分类>/<文件>.json
			name := parts[1]
			if name == "" {
				// /data/<分类>/
				name = source
				source = parts[0]
				if name != "" && strings.HasSuffix(name, ".json") {
					name = strings.TrimSuffix(name, ".json")
				} else {
					// 返回目录列表
					result = h.buildFileList(source)
					return result, nil
				}
			} else {
				name = strings.TrimSuffix(name, ".json")
			}
			data, err := h.readFileRaw(source, name+".json")
			if err != nil {
				var available []string
				for _, f := range h.listFiles(source) {
					validatePath := filepath.Join(h.dataDir(source), fmt.Sprintf("%s.json", f.Name))
					if raw, e := os.ReadFile(validatePath); e == nil && json.Valid(raw) {
						available = append(available, f.Name)
					}
				}
				result = ErrorResponse{
					Error:     fmt.Sprintf("file '%s.json' not found in source '%s'", name, source),
					Available: available,
				}
			} else {
				result = data
			}
		} else {
			// /data/<分类> — 目录列表
			result = h.buildFileList(source)
		}
	} else {
		// /data — 总览
		sources := h.listSources()
		sourceInfo := make(map[string]SourceInfo)
		for _, s := range sources {
			files := h.listFiles(s)
			sourceInfo[s] = SourceInfo{
				FileCount: len(files),
				Files:     files,
			}
		}
		result = SourcesResponse{
			Count:   len(sources),
			Sources: sourceInfo,
		}
	}

	return result, nil
}

func (h *DataFilesHandler) buildFileList(source string) FileListResponse {
	files := h.listFiles(source)
	var validFiles []FileInfo
	for _, f := range files {
		rawPath := filepath.Join(h.dataDir(source), fmt.Sprintf("%s.json", f.Name))
		if raw, err := os.ReadFile(rawPath); err == nil && json.Valid(raw) {
			validFiles = append(validFiles, f)
		}
	}
	return FileListResponse{
		Source: source,
		Count:  len(validFiles),
		Files:  validFiles,
	}
}

// 书源下发：面板「导入书源」不再读管理员手填的直链，而是指向这里的固定约定路径，
// 文件由静态托管（GET /data/<分类>/<文件>.json）直出。部署要换书源就是替换这个文件，
// 不必碰设置、也不必重新编译。
const (
	BookSourceDir  = "shuyuan"
	BookSourceFile = "bookSource"
)

// BookSourcePath 对外暴露的相对路径（前端用 window.location.origin 拼成绝对地址：
// 浏览器地址栏才是用户真实到达的域，服务端转发的 X-Forwarded-Host 可能被伪造）。
// 托管通道就是本 handler 自己的 /data/<分类>/<文件>.json 形态。
func BookSourcePath() string {
	return "/data/" + BookSourceDir + "/" + BookSourceFile + ".json"
}

// BookSourceReady 书源文件是否已就位。判据用 json.Valid 而不是「文件存在」：
// 半截的文件会让 App 导入失败，而面板上一切正常——宁可不给按钮。
func BookSourceReady() bool {
	path := filepath.Join(conf.Config.DataDir, BookSourceDir, BookSourceFile+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Valid(raw)
}

// MissingDataFile 一条「源声明了、但不在位」的静态字典文件。
// Reason 只有两种：missing（读不到）与 invalid_json（读到了但不是合法 JSON）——
// 半截文件与没有文件对下游是同一类故障（导入方拿到的是坏数据），所以合在一条读数里点名、
// 只用 Reason 区分「要去拷文件」还是「拷坏了」。
type MissingDataFile struct {
	Source string `json:"source"`
	Dir    string `json:"dir"`
	File   string `json:"file"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// CheckDeclaredDataFiles 启动期核对：各源在 `DataFiles` 声明位登记的文件是否真在 `dataRoot` 下就位。
// 返回缺位清单（空 = 都在位）；**它不改任何状态、也不阻断启动**，调用方（`app.Run`）逐条打 ERROR。
//
// 为什么需要这条（待办清单 P51、分支侧 S39）：字典是**部署产物**，而「二进制 + `.env` + `data/`」
// 这个部署单元里没有任何一步保证新增源的文件被放进目录——七猫三源的 `data/qm/` 就从 v62 起一直是空的。
// 缺位的症状不是崩，是**静默降级**：源的发现页查不到栏目、一片空列表，而监控里全是 200。
// 当时唯一的信号是源自己 `log.Printf` 的一句，而且 `sync.Once` 之后不再重说——
// 不报错的错最贵，所以这道核对由底座做，且按声明位遍历（新源接进来自动被覆盖，不需要谁记得去加）。
//
// 目录取 `SourceMeta.Category`：`/data/<分类>/<文件>.json` 的第一段就是它
// （fake 源是 `fake`，七猫三形态共用 `qm`）。按数据源码去找会找到一个不存在的目录——
// **多形态源拆码不拆字典目录**，回落只在 Category 为空时用得上。
// 判据是「读得到且 `json.Valid`」，与 `BookSourceReady` 同一套（两处各写一遍就会分叉）。
func CheckDeclaredDataFiles(dataRoot string) []MissingDataFile {
	type key struct{ dir, name string }
	seen := map[key]bool{}
	var missing []MissingDataFile
	for _, m := range base.DeclaredSources() {
		dir := m.Category
		if dir == "" {
			dir = m.Code
		}
		for _, f := range m.DataFiles {
			k := key{dir, f.Name}
			if seen[k] {
				continue // 同一份字典被同族多个源共用时只点名一次
			}
			seen[k] = true
			path := filepath.Join(dataRoot, dir, f.Name+".json")
			raw, err := os.ReadFile(path)
			switch {
			case err != nil:
				missing = append(missing, MissingDataFile{Source: m.Code, Dir: dir, File: f.Name, Path: path, Reason: "missing"})
			case !json.Valid(raw):
				missing = append(missing, MissingDataFile{Source: m.Code, Dir: dir, File: f.Name, Path: path, Reason: "invalid_json"})
			}
		}
	}
	return missing
}

func init() {
	base.Register("data_files", NewDataFilesHandler, 0, map[string]interface{}{
		"type": "datafiles",
	})
}
