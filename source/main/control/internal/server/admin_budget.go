package server

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// 管理页面与 SIP 使用独立监听；管理连接上限约束 net/http 创建的连接协程，而不是呼叫并发。
const adminConnectionLimit = 64
const adminHeavyRequestLimit = 8

var errAdminBusy = errors.New("管理重请求已达并发上限，请稍后重试；此额度不限制状态与停止请求，全部请求仍受连接上限约束")

// adminLoad 把连接、重响应和资源观测预算绑定到一个 Server，不共享不同测试实例的额度。
type adminLoad struct {
	heavy       chan struct{}      // 重文档、XML 和报告请求的并发槽；拒绝时不等待队列。
	active      atomic.Int64       // 正在执行的重请求数。
	rejected    atomic.Uint64      // 因重请求额度不足返回 503 的累计次数。
	connections atomic.Int64       // 已被 HTTP 服务器接纳、尚未关闭的连接数。
	observer    controllerObserver // 每秒最多一次的运行时采样缓存。
}

// newAdminLoad 只分配固定大小的控制结构，不启动后台或每呼叫协程。
func newAdminLoad() *adminLoad {
	return &adminLoad{heavy: make(chan struct{}, adminHeavyRequestLimit)}
}

// heavyAdminRequest 只限制可能大批构造或复制正文的操作，为状态、停止、排空及保护策略保留执行机会。
func heavyAdminRequest(r *http.Request) bool {
	p := r.URL.Path
	return p == "/v1/codecs" || strings.HasPrefix(p, "/v1/fs-config") || strings.HasPrefix(p, "/v1/docs") ||
		(strings.HasPrefix(p, "/v1/tests/") && strings.HasSuffix(p, "/report")) ||
		(p == "/v1/config" && r.Method == http.MethodPut)
}

// serve 在读取大正文或进入配置锁之前取得额度；过载只返回可重试错误，不堆积等待 goroutine。
func (b *adminLoad) serve(next http.Handler, w http.ResponseWriter, r *http.Request) {
	if !heavyAdminRequest(r) {
		next.ServeHTTP(w, r)
		return
	}
	select {
	case b.heavy <- struct{}{}:
		b.active.Add(1)
		defer func() { b.active.Add(-1); <-b.heavy }()
	default:
		b.rejected.Add(1)
		w.Header().Set("Retry-After", "1")
		adminError(w, http.StatusServiceUnavailable, errAdminBusy)
		return
	}
	next.ServeHTTP(w, r)
}

// limitedAdminListener 在 Accept 之前获取槽位，避免先创建大量 HTTP 连接协程再在 handler 内等待。
type limitedAdminListener struct {
	net.Listener
	slots  chan struct{} // 最多64个已接受连接；额外连接由系统监听队列处理。
	closed chan struct{} // Close 唤醒尚未取得额度的 Accept。
	once   sync.Once
	load   *adminLoad
}

// limitAdminConnections 由控制器初始化时调用一次，管理监听关闭不影响 SIP/媒体监听。
func (s *Server) limitAdminConnections(listener net.Listener) net.Listener {
	return &limitedAdminListener{Listener: listener, slots: make(chan struct{}, adminConnectionLimit), closed: make(chan struct{}), load: s.adminLoad}
}

// Accept 的每个错误路径都释放自己的额度，成功路径将额度的释放所有权交给连接 Close。
func (l *limitedAdminListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.closed:
		return nil, net.ErrClosed
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	if l.load != nil {
		l.load.connections.Add(1)
	}
	return &limitedAdminConn{Conn: conn, release: func() {
		if l.load != nil {
			l.load.connections.Add(-1)
		}
		<-l.slots
	}}, nil
}

// Close 可以重复调用，同时解除底层监听和额度等待，配合 HTTP Shutdown 的有限退出窗口。
func (l *limitedAdminListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

// limitedAdminConn 即使发生重复 Close 也只归还一个连接槽，避免计数变负或释放他人的额度。
type limitedAdminConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedAdminConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
