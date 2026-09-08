package server

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"rustswitch/control/internal/config"
	"slices"
	"strings"
	"time"
)

// adminWeb 随 Go 可执行文件交付，管理页面不依赖外部 CDN、独立前端服务器或互联网。
//
//go:embed web
var adminWeb embed.FS

// adminError 使用统一 JSON 错误，前端可在失败或版本冲突时保留尚未保存的表单。
func adminError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"error": err.Error()})
}

// adminResponse 保存已完成事务的响应快照，锁内代码只构造结果，不接触客户端连接。
type adminResponse struct {
	status int // HTTP 状态码与正文在同一版本下确定。
	body   any // 仅保存独立值；可变切片和映射必须复制后再发布。
}

// write 在管理锁已经释放后发送，包括错误正文和响应头，慢客户端不能阻塞排空。
func (response adminResponse) write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(response.status)
	writeJSON(w, response.body)
}

// adminFailure 仅构造失败结果，让校验与持久化错误沿用相同的锁外发送路径。
func adminFailure(status int, err error) adminResponse {
	return adminResponse{status: status, body: map[string]any{"error": err.Error()}}
}

// adminTransaction 串行化版本检查、状态提交和快照获取，返回前必定释放管理锁。
func (s *Server) adminTransaction(operation func() adminResponse) adminResponse {
	s.admin.mu.Lock()
	defer s.admin.mu.Unlock()
	return operation()
}

// decodeAdmin 限制正文大小并拒绝未知字段/拼接 JSON；所有写接口只接受显式 JSON 请求。
func decodeAdmin(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		adminError(w, 400, fmt.Errorf("请求 JSON 无效：%w", err))
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		adminError(w, 400, errors.New("请求必须只有一个 JSON 对象"))
		return false
	}
	return true
}

// protectAdmin 保持回环管理边界，并阻止跨站表单和 DNS 重绑定修改管理状态。
// CSRF 令牌只通过同源配置响应提供；它不是远程用户认证，远程管理仍应经 SSH 隧道。
func (s *Server) protectAdmin(next http.Handler) http.Handler {
	// 在服务接受连接之前建立预算；闭包复用同一实例，不能按请求重新分配额度。
	s.adminLoad = newAdminLoad()
	budget := s.adminLoad
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'")
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		ip, parseErr := netip.ParseAddr(host)
		if host != "localhost" && (parseErr != nil || !ip.IsLoopback()) {
			adminError(w, 403, errors.New("管理页面仅接受本机地址"))
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, e := url.Parse(origin)
				if e != nil || u.Scheme != "http" || u.Host != r.Host {
					adminError(w, 403, errors.New("拒绝跨站配置修改"))
					return
				}
			}
			if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
				adminError(w, 415, errors.New("管理写入必须使用 application/json"))
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-RustSwitch-CSRF")), []byte(s.adminToken)) != 1 {
				// 明确标记尚未进入业务处理器的令牌失效，允许页面只更新令牌后重试一次原请求。
				w.Header().Set("X-RustSwitch-CSRF-Refresh", "required")
				adminError(w, 403, errors.New("配置令牌无效，请刷新页面后重试"))
				return
			}
		}
		budget.serve(next, w, r)
	})
}

// registerAdmin 把页面与管理操作绑定到现有回环监听；明确路由避免未知路径返回假成功。
func (s *Server) registerAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/codecs", s.getCodecs)
	s.registerDocumentation(mux)
	s.registerBenchmark(mux)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "index.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("GET /app.css", func(w http.ResponseWriter, r *http.Request) { s.serveAsset(w, "app.css", "text/css; charset=utf-8") })
	mux.HandleFunc("GET /app.js", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "app.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("GET /v1/config", s.getAdminConfig)
	mux.HandleFunc("PUT /v1/config", s.putAdminConfig)
	mux.HandleFunc("GET /v1/guard", s.getGuard)
	mux.HandleFunc("PUT /v1/guard", s.putGuard)
	mux.HandleFunc("POST /v1/drain", func(w http.ResponseWriter, r *http.Request) {
		s.adminTransaction(func() adminResponse {
			s.draining.Store(true)
			return adminResponse{200, map[string]any{"draining": true}}
		}).write(w)
	})
	mux.HandleFunc("POST /v1/resume", func(w http.ResponseWriter, r *http.Request) {
		s.adminTransaction(func() adminResponse {
			if s.stopping.Load() {
				return adminFailure(409, errors.New("服务正在退出，不能恢复接入"))
			}
			s.draining.Store(false)
			return adminResponse{200, map[string]any{"draining": false}}
		}).write(w)
	})
	mux.HandleFunc("GET /v1/fs-config", s.getFSConfig)
	mux.HandleFunc("PUT /v1/fs-config", s.putFSConfig)
	mux.HandleFunc("GET /v1/fs-config/file", s.getFSFile)
	mux.HandleFunc("PUT /v1/fs-config/file", s.putFSFile)
	mux.HandleFunc("GET /v1/fs-config/export", s.getFSExport)
}

// serveAsset 只读取预先注册的内嵌静态资源，外部输入不能用于拼接磁盘路径。
func (s *Server) serveAsset(w http.ResponseWriter, name, contentType string) {
	data, err := adminWeb.ReadFile("web/" + name)
	if err != nil {
		adminError(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}

// guardSnapshot 只读取原子计数，不从 HTTP goroutine 遍历主循环私有的通话映射。
func (s *Server) guardSnapshot() GuardSnapshot {
	return s.guard.Snapshot(s.Stats.Active.Load(), s.Stats.Established.Load(), time.Now())
}

// cloneAdminConfig 复制全部可变切片与可选传输对象，并保留 nil 与空数组的 JSON 区别。
func cloneAdminConfig(value config.Config) config.Config {
	if value.ASRStream != nil {
		stream := *value.ASRStream
		value.ASRStream = &stream
	}
	if value.PCMStream != nil {
		stream := *value.PCMStream
		value.PCMStream = &stream
	}
	if value.SIP.Dialplan != nil {
		dialplan := *value.SIP.Dialplan
		value.SIP.Dialplan = &dialplan
	}
	if value.SIP.TrunkAuth != nil {
		auth := *value.SIP.TrunkAuth
		value.SIP.TrunkAuth = &auth
	}
	if value.SIP.Registration != nil {
		registration := *value.SIP.Registration
		value.SIP.Registration = &registration
	}
	if value.SIP.Stream != nil {
		stream := *value.SIP.Stream
		value.SIP.Stream = &stream
	}
	value.SIP.TrustedNetworks = slices.Clone(value.SIP.TrustedNetworks)
	value.SIP.LocalExtensions = slices.Clone(value.SIP.LocalExtensions)
	value.Media.AllowedRemoteNetworks = slices.Clone(value.Media.AllowedRemoteNetworks)
	value.Media.CPUCores = slices.Clone(value.Media.CPUCores)
	value.Media.ExcludedPortBlocks = slices.Clone(value.Media.ExcludedPortBlocks)
	return value
}

// adminConfigLocked 汇总运行值和下一次启动值；调用者持锁，返回值与持久状态不共享可变数组。
func (s *Server) adminConfigLocked() map[string]any {
	return map[string]any{"revision": s.admin.state.Revision, "csrf_token": s.adminToken, "active": cloneAdminConfig(s.admin.active), "desired": cloneAdminConfig(s.admin.state.Desired), "restart_required": s.admin.restartRequiredLocked(), "guard": s.guardSnapshot(), "persisted": s.admin.persisted, "notice": s.admin.notice}
}

// getAdminConfig 返回一致版本快照；页面用 revision 防止多个窗口互相覆盖设置。
func (s *Server) getAdminConfig(w http.ResponseWriter, r *http.Request) {
	s.adminTransaction(func() adminResponse {
		return adminResponse{200, s.adminConfigLocked()}
	}).write(w)
}

// revisionConflictLocked 在锁内冻结冲突版本，响应统一在锁外发送，绝不覆盖其他已提交修改。
func (s *Server) revisionConflictLocked(revision uint64) *adminResponse {
	if revision == s.admin.state.Revision {
		return nil
	}
	return &adminResponse{409, map[string]any{"error": "配置已被其他操作更新，请载入最新版本后重新合并修改", "revision": s.admin.state.Revision}}
}

// putAdminConfig 校验下一次启动配置。运行中的 SIP、端口与工作进程保持当前配置，直到正常重启。
func (s *Server) putAdminConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision uint64        `json:"revision"`
		Config   config.Config `json:"config"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	if err := body.Config.Validate(); err != nil {
		adminError(w, 422, err)
		return
	}
	s.adminTransaction(func() adminResponse {
		if conflict := s.revisionConflictLocked(body.Revision); conflict != nil {
			return *conflict
		}
		// 本机文件根、XML及私有音频入口随部署冻结；页面不能改变入口的开关、路径或资源预算。
		if body.Config.Media.Binary != s.admin.active.Media.Binary || body.Config.Journal.Path != s.admin.active.Journal.Path || body.Config.Media.PlaybackRoot != s.admin.active.Media.PlaybackRoot || !sameDialplan(body.Config.SIP.Dialplan, s.admin.active.SIP.Dialplan) || !samePCMStream(body.Config.PCMStream, s.admin.active.PCMStream) || !sameASRStream(body.Config.ASRStream, s.admin.active.ASRStream) {
			return adminFailure(422, errors.New("程序、日志、放音根目录、XML拨号计划、PCM流入口和ASR代理由部署启动文件管理，页面中只读"))
		}
		if _, err := NewAdmissionGuard(body.Config.Limits, s.admin.state.Policy); err != nil {
			return adminFailure(422, fmt.Errorf("请先将峰值策略调至新的启动上限以内：%w", err))
		}
		next := s.admin.state
		next.Desired = body.Config
		if err := s.admin.commitLocked(next); err != nil {
			return adminFailure(500, fmt.Errorf("配置未保存：%w", err))
		}
		return adminResponse{200, s.adminConfigLocked()}
	}).write(w)
}

// samePCMStream 比较部署原值，保留省略与启用对象的区别；零预算不在保存时偷偷改为默认值。
func samePCMStream(a, b *config.PCMStream) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// sameASRStream 按部署原值比较；零值预算与省略状态不在保存草稿时被偷偷改写。
func sameASRStream(a, b *config.ASRStream) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// getGuard 输出当前保护策略与统计，读取不会消耗新呼叫令牌。
func (s *Server) getGuard(w http.ResponseWriter, r *http.Request) {
	s.adminTransaction(func() adminResponse {
		return adminResponse{200, map[string]any{"revision": s.admin.state.Revision, "guard": s.guardSnapshot()}}
	}).write(w)
}

// putGuard 先完整校验、再持久化、最后一次性更新运行策略，磁盘错误不改变当前保护。
func (s *Server) putGuard(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision uint64      `json:"revision"`
		Policy   GuardPolicy `json:"policy"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	s.adminTransaction(func() adminResponse {
		if conflict := s.revisionConflictLocked(body.Revision); conflict != nil {
			return *conflict
		}
		if err := s.guard.Validate(body.Policy); err != nil {
			return adminFailure(422, err)
		}
		if _, err := NewAdmissionGuard(s.admin.state.Desired.Limits, body.Policy); err != nil {
			return adminFailure(422, fmt.Errorf("策略超过待重启配置的硬上限：%w", err))
		}
		next := s.admin.state
		next.Policy = body.Policy
		if err := s.admin.commitLocked(next); err != nil {
			return adminFailure(500, fmt.Errorf("策略未保存：%w", err))
		}
		// 上方已针对不变的启动硬边界校验；这里不会因并发流量改变验证结果。
		if err := s.guard.Update(body.Policy); err != nil {
			return adminFailure(500, err)
		}
		return adminResponse{200, map[string]any{"revision": s.admin.state.Revision, "guard": s.guardSnapshot()}}
	}).write(w)
}

// fsConfigSnapshot 只读取独立的草稿快照；目录缓存/计算均不持管理状态锁，配置仍仅用于导出。
func (s *Server) fsConfigSnapshot(saved map[string]string, revision uint64) (map[string]any, error) {
	files, err := effectiveFSFiles(saved)
	if err != nil {
		return nil, err
	}
	catalog, err := s.admin.catalog.catalog(files)
	if err != nil {
		return nil, err
	}
	return map[string]any{"revision": revision, "mode": "export_only", "runtime_supported": false, "reference_version": "1.11.3", "files": catalog, "overrides": map[string]string{}, "modified_files": len(saved), "capabilities": fsCapabilities()}, nil
}

// getFSConfig 读取草稿。参数 value 已包含已保存修改，overrides 仅作为页面本次编辑的入口。
func (s *Server) getFSConfig(w http.ResponseWriter, r *http.Request) {
	s.admin.mu.Lock()
	saved, revision := cloneFSFiles(s.admin.state.FSFiles), s.admin.state.Revision
	s.admin.mu.Unlock()
	value, err := s.fsConfigSnapshot(saved, revision)
	if err != nil {
		adminFailure(500, err).write(w)
		return
	}
	adminResponse{200, value}.write(w)
}

// putFSConfig 应用本次参数修改，保留其他文件及未编辑属性；保存不触发任何模块加载。
func (s *Server) putFSConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision  uint64            `json:"revision"`
		Overrides map[string]string `json:"overrides"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	// 先绑定版本和独立文件快照，再在锁外解析/校验；不能把较早快照覆盖并发提交。
	s.admin.mu.Lock()
	conflict := s.revisionConflictLocked(body.Revision)
	saved := cloneFSFiles(s.admin.state.FSFiles)
	s.admin.mu.Unlock()
	if conflict != nil {
		conflict.write(w)
		return
	}
	files, err := effectiveFSFiles(saved)
	if err != nil {
		adminFailure(500, err).write(w)
		return
	}
	updated, err := patchFSParameters(files, body.Overrides)
	if err != nil {
		adminFailure(422, err).write(w)
		return
	}
	for name, value := range updated {
		if value != files[name] {
			saved[name] = value
		}
	}
	if err = validateFSFileLimits(saved); err != nil {
		adminFailure(422, err).write(w)
		return
	}
	// 同内容缓存复用此前完整解析结果；任何变化均经过严格解析，先完成响应目录才提交。
	value, err := s.fsConfigSnapshot(saved, body.Revision)
	if err != nil {
		adminFailure(422, err).write(w)
		return
	}
	s.adminTransaction(func() adminResponse {
		if conflict := s.revisionConflictLocked(body.Revision); conflict != nil {
			return *conflict
		}
		next := s.admin.state
		next.FSFiles = saved
		if err := s.admin.commitLocked(next); err != nil {
			return adminFailure(500, err)
		}
		value["revision"] = s.admin.state.Revision
		return adminResponse{200, value}
	}).write(w)
}

// getFSFile 返回原始 XML 文本，不执行预处理指令；未知路径不会读取本机文件。
func (s *Server) getFSFile(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("path")
	if !validFSPath(name) {
		adminError(w, 400, errors.New("配置路径无效"))
		return
	}
	s.adminTransaction(func() adminResponse {
		files, err := effectiveFSFiles(s.admin.state.FSFiles)
		if err != nil {
			return adminFailure(500, err)
		}
		raw, ok := files[name]
		if !ok {
			return adminFailure(404, errors.New("配置文件不存在"))
		}
		return adminResponse{200, map[string]any{"path": name, "xml": raw, "revision": s.admin.state.Revision}}
	}).write(w)
}

// putFSFile 支持高级结构修改及新增文件，适用于用户、网关和复杂拨号计划等配置。
func (s *Server) putFSFile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision uint64 `json:"revision"`
		Path     string `json:"path"`
		XML      string `json:"xml"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	if _, err := parseFSFile(body.Path, body.XML); err != nil {
		adminError(w, 422, err)
		return
	}
	// 高级XML也先取独立版本快照；完整草稿限额与结构验证不占用管理状态锁。
	s.admin.mu.Lock()
	conflict := s.revisionConflictLocked(body.Revision)
	saved := cloneFSFiles(s.admin.state.FSFiles)
	s.admin.mu.Unlock()
	if conflict != nil {
		conflict.write(w)
		return
	}
	saved[body.Path] = body.XML
	if err := validateFSFileLimits(saved); err != nil {
		adminFailure(422, err).write(w)
		return
	}
	if _, err := s.admin.catalog.catalog(saved); err != nil {
		adminFailure(422, err).write(w)
		return
	}
	s.adminTransaction(func() adminResponse {
		if conflict := s.revisionConflictLocked(body.Revision); conflict != nil {
			return *conflict
		}
		next := s.admin.state
		next.FSFiles = saved
		if err := s.admin.commitLocked(next); err != nil {
			return adminFailure(500, err)
		}
		return adminResponse{200, map[string]any{"revision": s.admin.state.Revision, "path": body.Path, "xml": body.XML, "runtime_supported": false}}
	}).write(w)
}

// getFSExport 在锁内取得值副本后释放锁，ZIP 压缩和客户端下载不会阻塞其他配置操作。
func (s *Server) getFSExport(w http.ResponseWriter, r *http.Request) {
	s.admin.mu.Lock()
	files, err := effectiveFSFiles(s.admin.state.FSFiles)
	revision := s.admin.state.Revision
	s.admin.mu.Unlock()
	if err != nil {
		adminError(w, 500, err)
		return
	}
	if err = exportFSConfig(w, files, revision); err != nil {
		return
	}
}
