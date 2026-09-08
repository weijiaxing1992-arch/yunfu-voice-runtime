package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// TestDocumentationManifestAndFiles 验证每份索引文件可下载且字节、哈希和格式均与原文一致。
func TestDocumentationManifestAndFiles(t *testing.T) {
	s, handler, _ := newAdminTestServer(t)
	response := adminRequest(t, handler, "GET", "/v1/docs", "", nil)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var manifest documentationManifest
	if err := json.Unmarshal(response.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version == "" || len(manifest.Documents) < 20 || manifest.Counts["http_operations"] != 26 || manifest.Counts["comparison_entries"] < 3923 {
		t.Fatal("文档清单未覆盖目标范围", manifest.Counts)
	}
	for _, file := range manifest.Documents {
		t.Run(file.Path, func(t *testing.T) {
			response := adminRequest(t, handler, "GET", "/v1/docs/file?path="+url.QueryEscape(file.Path), "", nil)
			if response.Code != 200 || response.Body.Len() != file.Bytes {
				t.Fatal("文件缺失或截断", response.Code)
			}
			digest := sha256.Sum256(response.Body.Bytes())
			if hex.EncodeToString(digest[:]) != file.SHA256 {
				t.Fatal("下载内容与清单哈希不符")
			}
			if response.Header().Get("Content-Type") != documentationContentType(file.Path) {
				t.Fatal("文本类型错误")
			}
		})
	}
	if s.admin.state.Revision != 1 || s.draining.Load() {
		t.Fatal("只读文档改变了管理状态")
	}
}

// TestDocumentationPathBoundary 防止文档查看器变成任意路径读取或将错误参数视为合法名称。
func TestDocumentationPathBoundary(t *testing.T) {
	_, handler, _ := newAdminTestServer(t)
	for _, query := range []string{"", "?path=", "?path=..%2F..%2Fconfig%2Flocal.json", "?path=%2Fetc%2Fpasswd", "?path=a%5Cb", "?path=api%2F.%2FREADME.md", "?path=api%2FREADME.md&path=api%2FREADME.md", "?path=api%2FREADME.md&other=1", "?path=%xx"} {
		response := adminRequest(t, handler, "GET", "/v1/docs/file"+query, "", nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("无效查询被接受：%s => %d", query, response.Code)
		}
	}
	response := adminRequest(t, handler, "GET", "/v1/docs/file?path=config/local.json", "", nil)
	if response.Code != 404 {
		t.Fatal("未列入清单的运行配置被读取", response.Code)
	}
	response = adminRequest(t, handler, "POST", "/v1/docs/file?path=api/README.md", "", map[string]any{})
	if response.Code != 403 {
		t.Fatal("新增文档入口绕过现有写入边界", response.Code)
	}
}

// TestDocumentationArchiveCompleteness 校验归档全量包含原文，且缓存可供并发只读请求复用。
func TestDocumentationArchiveCompleteness(t *testing.T) {
	_, handler, _ := newAdminTestServer(t)
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			response := adminRequest(t, handler, "GET", "/v1/docs/export.zip", "", nil)
			if response.Code != 200 || !strings.Contains(response.Header().Get("Content-Disposition"), "rustswitch-interface-docs.zip") {
				t.Error("ZIP 下载失败", response.Code)
				return
			}
			archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
			if err != nil {
				t.Error(err)
				return
			}
			if len(archive.File) != len(documentationIndex.files)+1 {
				t.Error("ZIP 文档不完整")
				return
			}
			for _, file := range archive.File {
				if !validDocumentationPath(file.Name) {
					t.Error("ZIP 路径无效")
					return
				}
				reader, err := file.Open()
				if err != nil {
					t.Error(err)
					return
				}
				data, err := io.ReadAll(reader)
				_ = reader.Close()
				if err != nil {
					t.Error(err)
					return
				}
				original, err := documentationAssets.ReadFile("doc_assets/" + file.Name)
				if err != nil || !bytes.Equal(data, original) {
					t.Error("ZIP 内容不是原始发布文档", file.Name)
					return
				}
			}
		}()
	}
	group.Wait()
}

// TestDocumentationModelAliases 验证机器模型的专用入口与文档原文相同，不返回HTML外壳。
func TestDocumentationModelAliases(t *testing.T) {
	_, handler, _ := newAdminTestServer(t)
	for route, file := range map[string]string{"/v1/docs/openapi.json": "api/openapi.json", "/v1/docs/comparison.json": "api/comparison.json"} {
		response := adminRequest(t, handler, "GET", route, "", nil)
		original, err := documentationAssets.ReadFile("doc_assets/" + file)
		if err != nil || response.Code != 200 || !json.Valid(response.Body.Bytes()) || !bytes.Equal(original, response.Body.Bytes()) {
			t.Fatal("模型专用入口与原文不同", route)
		}
	}
}
