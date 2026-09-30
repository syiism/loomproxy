package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loomproxy-go/base"
	"loomproxy-go/conf"
	"loomproxy-go/utils"
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
	hasRawData := false

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
				hasRawData = true
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

	if !hasRawData {
		cacheKey := utils.CacheKey("datafiles", params)
		utils.DefaultCache().Set(cacheKey, result)
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

func init() {
	base.Register("data_files", NewDataFilesHandler, 0, map[string]interface{}{
		"type": "datafiles",
	})
}
