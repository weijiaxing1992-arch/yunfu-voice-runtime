package server

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// documentationAssets 由受控构建脚本从源码文档生成；运行时不会访问工作目录或外部站点。
//
//go:embed doc_assets
var documentationAssets embed.FS

// documentationFile 是公开文档白名单中的一个条目，哈希对应实际下载的原始字节。
type documentationFile struct {
	Path     string `json:"path"`     // 文档包内相对路径，不能表示服务器上的任意文件。
	Title    string `json:"title"`    // 管理页面显示的中文标题。
	Category string `json:"category"` // 接口、对照、参考标准、源码契约等阅读分类。
	Format   string `json:"format"`   // md、json、csv、h 或 txt，供页面选择安全阅读方式。
	Bytes    int    `json:"bytes"`    // 原始 UTF-8 文本字节数。
	SHA256   string `json:"sha256"`   // 构建时计算的完整内容校验值。
}

// documentationManifest 固定本次二进制中的文档版本，文档覆盖数不代表兼容通过率。
type documentationManifest struct {
	Version          string              `json:"version"`           // 文档集版本，独立于媒体管道协议版本。
	ReferenceVersion string              `json:"reference_version"` // 对照的 FreeSWITCH 固定版本。
	ReferenceCommit  string              `json:"reference_commit"`  // 固定源代码提交，避免后续上游漂移。
	Documents        []documentationFile `json:"documents"`         // 允许读取和下载的全部文档。
	Counts           map[string]int      `json:"counts"`            // HTTP operation 与对照目录条目数量。
	Scope            string              `json:"scope"`             // 静态目录、当前接口和未验证边界。
	Notice           string              `json:"notice"`            // 兼容差异及实际验收状态提示。
}

// documentationIndex 懒加载只读索引；之后并发请求不需要管理配置锁，不影响热更新和通话。
var documentationIndex struct {
	once     sync.Once                    // 每个进程只解码一次可信的内嵌清单。
	manifest documentationManifest        // 完整清单供导出遍历。
	files    map[string]documentationFile // 精确路径白名单。
	err      error                        // 构建产物缺失或损坏时向调用者报告失败。
}

// validDocumentationPath 拒绝绝对路径、反斜线与任何点段，只允许规范的包内路径。
func validDocumentationPath(name string) bool {
	if name == "" || len(name) > 240 || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") || path.Clean(name) != name {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// loadDocumentation 验证清单结构和文件存在性；索引不允许任意路径落到磁盘读取。
func loadDocumentation() error {
	documentationIndex.once.Do(func() {
		wire, err := documentationAssets.ReadFile("doc_assets/manifest.json")
		if err != nil {
			documentationIndex.err = err
			return
		}
		if err = json.Unmarshal(wire, &documentationIndex.manifest); err != nil {
			documentationIndex.err = err
			return
		}
		documentationIndex.files = map[string]documentationFile{}
		for _, file := range documentationIndex.manifest.Documents {
			if !validDocumentationPath(file.Path) {
				documentationIndex.err = errors.New("文档索引路径无效")
				return
			}
			if _, exists := documentationIndex.files[file.Path]; exists {
				documentationIndex.err = errors.New("文档索引路径重复")
				return
			}
			data, readErr := documentationAssets.ReadFile("doc_assets/" + file.Path)
			if readErr != nil || len(data) != file.Bytes {
				documentationIndex.err = fmt.Errorf("文档文件缺失或长度不符：%s", file.Path)
				return
			}
			documentationIndex.files[file.Path] = file
		}
	})
	return documentationIndex.err
}

// registerDocumentation 发布只读文档入口；管理页代码示例不会通过这些入口执行业务写入。
func (s *Server) registerDocumentation(mux *http.ServeMux) {
	mux.HandleFunc("GET /docs.js", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "docs.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("GET /docs.css", func(w http.ResponseWriter, r *http.Request) { s.serveAsset(w, "docs.css", "text/css; charset=utf-8") })
	mux.HandleFunc("GET /v1/docs", func(w http.ResponseWriter, r *http.Request) {
		if err := loadDocumentation(); err != nil {
			adminError(w, 500, err)
			return
		}
		data, err := documentationAssets.ReadFile("doc_assets/manifest.json")
		if err != nil {
			adminError(w, 500, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /v1/docs/openapi.json", func(w http.ResponseWriter, r *http.Request) { serveDocumentationFile(w, "api/openapi.json") })
	mux.HandleFunc("GET /v1/docs/comparison.json", func(w http.ResponseWriter, r *http.Request) { serveDocumentationFile(w, "api/comparison.json") })
	mux.HandleFunc("GET /v1/docs/file", func(w http.ResponseWriter, r *http.Request) {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) != 1 || len(query["path"]) != 1 || !validDocumentationPath(query.Get("path")) {
			adminError(w, 400, errors.New("必须提供唯一且有效的文档相对路径 path"))
			return
		}
		serveDocumentationFile(w, query.Get("path"))
	})
	mux.HandleFunc("GET /v1/docs/export.zip", exportDocumentation)
}

// documentationContentType 明确声明原始文档类型，Markdown 和头文件作为文本返回，不执行 HTML。
func documentationContentType(name string) string {
	switch path.Ext(name) {
	case ".json":
		return "application/json; charset=utf-8"
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}

// serveDocumentationFile 仅查找清单中的内嵌资源；合法但未列出的名称返回 404。
func serveDocumentationFile(w http.ResponseWriter, name string) {
	if err := loadDocumentation(); err != nil {
		adminError(w, 500, err)
		return
	}
	if _, exists := documentationIndex.files[name]; !exists {
		adminError(w, 404, errors.New("文档不存在或未列入发布清单"))
		return
	}
	data, err := documentationAssets.ReadFile("doc_assets/" + name)
	if err != nil {
		adminError(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", documentationContentType(name))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": path.Base(name)}))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// documentationArchive 缓存不可变压缩包，重复下载无需重新压缩数千条对照资料。
var documentationArchive struct {
	once sync.Once // 文档嵌入二进制后不变，每个进程只生成一次 ZIP。
	data []byte    // 已完成且可重复读取的归档。
	err  error     // 不发送残缺 ZIP，构建失败返回明确的 500。
}

// exportDocumentation 包含清单与全部已发布原文，不打包运行配置、令牌、日志或用户 XML 草稿。
func exportDocumentation(w http.ResponseWriter, r *http.Request) {
	if err := loadDocumentation(); err != nil {
		adminError(w, 500, err)
		return
	}
	documentationArchive.once.Do(func() {
		var buffer bytes.Buffer
		writer := zip.NewWriter(&buffer)
		names := []string{"manifest.json"}
		for name := range documentationIndex.files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			data, err := documentationAssets.ReadFile("doc_assets/" + name)
			if err != nil {
				documentationArchive.err = err
				return
			}
			entry, err := writer.Create(name)
			if err != nil {
				documentationArchive.err = err
				return
			}
			if _, err = entry.Write(data); err != nil {
				documentationArchive.err = err
				return
			}
		}
		if err := writer.Close(); err != nil {
			documentationArchive.err = err
			return
		}
		documentationArchive.data = buffer.Bytes()
	})
	if documentationArchive.err != nil {
		adminError(w, 500, documentationArchive.err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="rustswitch-interface-docs.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(documentationArchive.data)))
	_, _ = w.Write(documentationArchive.data)
}
