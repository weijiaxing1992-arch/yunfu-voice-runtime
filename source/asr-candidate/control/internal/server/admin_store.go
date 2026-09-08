package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"rustswitch/control/internal/config"
	"sync"
)

// adminState 是一次完整的管理配置提交；同一文件同时保存版本、峰值策略和 XML 草稿。
// Desired 只在下一次启动生效，Policy 可在运行中更新；原始启动文件由运维人员保留。
type adminState struct {
	// Format 是磁盘结构版本；Revision 是所有配置页面共享的乐观并发版本。
	Format   int    `json:"format"`
	Revision uint64 `json:"revision"`
	// BaseDigest 检测人工修改启动文件，Desired 保存下一次启动采用的完整配置。
	BaseDigest string        `json:"base_digest"`
	Desired    config.Config `json:"desired"`
	// Policy 为即时准入策略；FSFiles 只保存相对官方基线的 XML 修改和新增文件。
	Policy  GuardPolicy       `json:"policy"`
	FSFiles map[string]string `json:"fs_files"`
}

// adminStore 串行化网页写入。文件同步不在 SIP 业务循环或 Rust 媒体循环执行。
type adminStore struct {
	// mu 保护整个提交与版本检查；业务收发通过独立 Guard 锁读取策略。
	mu sync.Mutex
	// path 是原始日志路径派生的管理状态文件，不能通过网页改变。
	path string
	// state 为最近成功提交；active 为本次进程启动时采用的配置。
	state  adminState
	active config.Config
	// persisted 表示已读取或成功写入状态文件，notice 解释启动文件优先等情况。
	persisted bool
	notice    string
	// XML目录缓存有自己的短锁，不在管理状态锁内解析文件。
	catalog fsCatalogCache
}

// CheckConfiguration 同时检查启动文件和已保存管理状态，不创建网络监听或工作进程。
func CheckConfiguration(base config.Config) error {
	_, err := openAdminStore(base)
	return err
}

// configDigest 绑定原始启动配置；人工改动启动文件优先于之前保存的待重启草稿。
func configDigest(c config.Config) string {
	wire, _ := json.Marshal(c)
	digest := sha256.Sum256(wire)
	return hex.EncodeToString(digest[:])
}

// openAdminStore 读取管理状态并选择本次有效配置，不开启监听也不修改磁盘。
// 每个日志文件对应独立管理状态，避免多个测试实例或服务互相覆盖。
func openAdminStore(base config.Config) (*adminStore, error) {
	// 固定官方目录在监听启动前一次性校验/解析，避免第一个管理请求承担初始化长任务。
	if _, err := baseFSParsed(); err != nil {
		return nil, fmt.Errorf("FreeSWITCH参考模板无效：%w", err)
	}
	s := &adminStore{path: base.Journal.Path + ".admin.json", active: base}
	s.state = adminState{Format: 1, Revision: 1, BaseDigest: configDigest(base), Desired: base, Policy: DefaultGuardPolicy(base.Limits), FSFiles: map[string]string{}}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 8<<20))
	decoder.DisallowUnknownFields()
	var saved adminState
	if err = decoder.Decode(&saved); err != nil {
		return nil, fmt.Errorf("管理配置损坏：%w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("管理配置必须只有一个 JSON 对象")
	}
	if saved.Format != 1 || saved.Revision == 0 {
		return nil, errors.New("管理配置版本无效")
	}
	if saved.FSFiles == nil {
		saved.FSFiles = map[string]string{}
	}
	if saved.BaseDigest != s.state.BaseDigest {
		// 人工启动配置优先，但无关字段的调整不能取消已经收紧的业务保护。
		// 仅在新的资源硬上限更低时收紧策略；软降速与重试设置原样保留。
		if saved.Revision == ^uint64(0) {
			return nil, errors.New("配置版本已耗尽")
		}
		saved.Desired = base
		saved.Policy.MaxActiveCalls = min(saved.Policy.MaxActiveCalls, base.Limits.MaxCalls)
		saved.Policy.MaxEstablishingCalls = min(saved.Policy.MaxEstablishingCalls, base.Limits.MaxCalls)
		saved.Policy.CallsPerSecond = min(saved.Policy.CallsPerSecond, base.Limits.CallsPerSecond)
		saved.Policy.BurstCalls = min(saved.Policy.BurstCalls, base.Limits.BurstCalls)
		saved.BaseDigest = s.state.BaseDigest
		saved.Revision++
		s.notice = "启动文件已变更：采用文件配置；已保存峰值策略与 FreeSWITCH 草稿保留，超出新硬上限的保护参数已收紧。"
	}
	if err = saved.Desired.Validate(); err != nil {
		return nil, fmt.Errorf("待启动配置无效：%w", err)
	}
	if _, err = NewAdmissionGuard(saved.Desired.Limits, saved.Policy); err != nil {
		return nil, fmt.Errorf("峰值策略无效：%w", err)
	}
	if err = validateFSFiles(saved.FSFiles); err != nil {
		return nil, err
	}
	s.state, s.active, s.persisted = saved, saved.Desired, true
	s.active.SourcePath = base.SourcePath
	return s, nil
}

// commitLocked 在临时文件同步完成后原子替换状态文件；调用者必须持有 mu。
// 只有替换成功才发布内存版本，失败时网页不能得到虚假的“保存成功”。
func (s *adminStore) commitLocked(next adminState) error {
	if s.state.Revision == ^uint64(0) {
		return errors.New("配置版本已耗尽")
	}
	next.Revision = s.state.Revision + 1
	wire, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if len(wire) > 7<<20 {
		return errors.New("配置草稿超过 7 MiB 总量限制")
	}
	dir := filepath.Dir(s.path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".rustswitch-admin-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(append(wire, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, s.path); err != nil {
		return err
	}
	// rename 是提交点。目录同步失败时文件已经替换，不能继续保留旧内存版本。
	s.state, s.persisted = next, true
	if folder, e := os.Open(dir); e == nil {
		if e = folder.Sync(); e != nil {
			slog.Warn("管理配置已保存，但目录同步失败", "error", e)
		}
		_ = folder.Close()
	}
	return nil
}

// restartRequiredLocked 只比较运行配置，不把峰值策略或 XML 导出草稿误报为重启需求。
func (s *adminStore) restartRequiredLocked() bool {
	a, b := s.active, s.state.Desired
	a.SourcePath, b.SourcePath = "", ""
	return !reflect.DeepEqual(a, b)
}

// cloneFSFiles 为一次事务复制草稿映射，校验失败时不会污染当前已提交状态。
func cloneFSFiles(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for path, value := range input {
		result[path] = value
	}
	return result
}
