package esl

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options 将协议接入与呼叫控制分离；本阶段仅允许本机入站连接。
// API 回调必须服从上下文取消，不能启动不受限制的后台任务。
type Options struct {
	Listen, Password                                        string
	API                                                     func(context.Context, string, string) string
	Execute                                                 func(context.Context, ExecuteRequest) string
	MaxConnections, JobWorkers, QueueSize                   int
	AuthTimeout, FrameTimeout, WriteTimeout, CommandTimeout time.Duration
}

// Server 使用固定后台执行器和有界连接/输出队列，慢客户端不能阻塞媒体主循环。
type Server struct {
	options    Options
	listener   net.Listener
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	clients    map[*client]struct{}
	jobs       chan job
	wg         sync.WaitGroup
	acceptDone chan struct{}
	closeOnce  sync.Once
	sequence   atomic.Uint64
	dropped    atomic.Uint64
}

type job struct{ command, arguments, uuid string }
type event struct {
	name    string
	headers map[string]string
	body    []byte
}
type eventFilter struct{ header, value string }

const maxQueuedBytes = 16 * 1024 * 1024

// 事件订阅只接受确有生产发布入口的集合；不能以 ALL 声称完整原事件源。
// 标准事件名称与实际事件源分开：允许订阅尚无产生路径的普通名称，不因此合成事件。

type client struct {
	server          *Server
	conn            net.Conn
	out             chan Frame
	done            chan struct{}
	closeOnce       sync.Once
	mu              sync.Mutex
	authed          bool
	format          string
	events          map[string]bool
	filters         []eventFilter
	ctx             context.Context
	cancel          context.CancelFunc
	authDeadline    time.Time
	remaining       atomic.Int32
	queuedBytes     atomic.Int64
	eventsEnabled   bool
	eventGeneration uint64
}

// Listen 在绑定端口前验证资源边界；默认禁用远程访问，避免沿用公开默认密码。
func Listen(parent context.Context, o Options) (*Server, error) {
	host, _, err := net.SplitHostPort(o.Listen)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return nil, errors.New("ESL listen requires a literal loopback address")
	}
	if o.Password == "" || len(o.Password) > 256 || strings.ContainsAny(o.Password, "\r\n\x00") || o.API == nil {
		return nil, errors.New("invalid ESL password or API handler")
	}
	if o.MaxConnections == 0 {
		o.MaxConnections = 64
	}
	if o.JobWorkers == 0 {
		o.JobWorkers = 8
	}
	if o.QueueSize == 0 {
		o.QueueSize = 128
	}
	if o.MaxConnections < 1 || o.MaxConnections > 1024 || o.JobWorkers < 1 || o.JobWorkers > 64 || o.QueueSize < 1 || o.QueueSize > 1024 {
		return nil, errors.New("invalid ESL resource bounds")
	}
	if o.AuthTimeout == 0 {
		o.AuthTimeout = 5 * time.Second
	}
	if o.FrameTimeout == 0 {
		o.FrameTimeout = 10 * time.Second
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = 2 * time.Second
	}
	if o.CommandTimeout == 0 {
		o.CommandTimeout = 5 * time.Second
	}
	if o.AuthTimeout < time.Millisecond || o.FrameTimeout < time.Millisecond || o.WriteTimeout < time.Millisecond || o.CommandTimeout < time.Millisecond {
		return nil, errors.New("invalid ESL deadline")
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", o.Listen)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Server{options: o, listener: ln, ctx: ctx, cancel: cancel, clients: make(map[*client]struct{}), jobs: make(chan job, o.QueueSize), acceptDone: make(chan struct{})}
	for i := 0; i < o.JobWorkers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	go s.accept()
	// 父上下文取消也必须释放监听和活跃连接，不能依赖调用方再显式 Close。
	context.AfterFunc(ctx, func() { _ = s.Close() })
	return s, nil
}

// Addr 返回实际监听地址，支持隔离测试使用操作系统分配的端口。
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// DroppedClients 记录输出溢出而主动断开的连接数，绝不静默丢弃单个事件后继续伪装完整流。
func (s *Server) DroppedClients() uint64 { return s.dropped.Load() }

// Close 先停止接入，再取消回调并等待有界工作组结束，防止关闭后继续访问业务状态。
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.listener.Close()
		<-s.acceptDone
		s.mu.Lock()
		for c := range s.clients {
			c.close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return nil
}

// accept 只为已获得连接额度的客户端创建两条协程。
func (s *Server) accept() {
	defer close(s.acceptDone)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.ctx.Err() != nil || len(s.clients) >= s.options.MaxConnections {
			s.mu.Unlock()
			conn.Close()
			continue
		}
		clientContext, clientCancel := context.WithCancel(s.ctx)
		c := &client{server: s, conn: conn, out: make(chan Frame, s.options.QueueSize), done: make(chan struct{}), format: "plain", events: make(map[string]bool), ctx: clientContext, cancel: clientCancel, authDeadline: time.Now().Add(s.options.AuthTimeout)}
		c.remaining.Store(2)
		s.clients[c] = struct{}{}
		s.wg.Add(2)
		s.mu.Unlock()
		go c.write()
		go c.read()
	}
}

func (c *client) close() { c.closeWithDrop(false) }

// closeWithDrop 原子记录一次流中断；并发发布不能重复增加同一连接的丢弃计数。
func (c *client) closeWithDrop(dropped bool) {
	c.closeOnce.Do(func() {
		if dropped {
			c.server.dropped.Add(1)
		}
		c.cancel()
		close(c.done)
		c.conn.Close()
	})
}

// finished 读写协程均终止后才归还连接额度；仍在退出的 API 不能绕过 MaxConnections。
func (c *client) finished() {
	if c.remaining.Add(-1) == 0 {
		c.server.mu.Lock()
		delete(c.server.clients, c)
		c.server.mu.Unlock()
	}
	c.server.wg.Done()
}

func frameSize(f Frame) int64 {
	size := int64(len(f.Command) + len(f.Body) + 2)
	for _, h := range f.Headers {
		size += int64(len(h.Name) + len(h.Value) + 3)
	}
	return size
}

// enqueue 永不等待网络；满队列会关闭整个连接，使调用方能够识别事件流中断。
func (c *client) enqueue(f Frame) bool {
	if isClosed(c.done) {
		return false
	}
	size := frameSize(f)
	if size > maxQueuedBytes || c.queuedBytes.Add(size) > maxQueuedBytes {
		if size <= maxQueuedBytes {
			c.queuedBytes.Add(-size)
		}
		c.closeWithDrop(true)
		return false
	}
	select {
	case <-c.done:
		c.queuedBytes.Add(-size)
		return false
	case c.out <- f:
		return true
	default:
		c.queuedBytes.Add(-size)
		c.closeWithDrop(true)
		return false
	}
}
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
func reply(value string) Frame {
	return Frame{Headers: []Header{{"Content-Type", "command/reply"}, {"Reply-Text", value}}}
}
func apiReply(value string) Frame {
	if value == "" {
		value = "-ERR no reply\n"
	}
	return Frame{Headers: []Header{{"Content-Type", "api/response"}, {"Content-Length", strconv.Itoa(len(value))}}, Body: []byte(value)}
}

// write 是唯一套接字写入者，完整处理短写，事件与回复不会在字节层相互穿插。
func (c *client) write() {
	defer c.finished()
	defer c.close()
	for {
		select {
		case <-c.done:
			return
		case <-c.server.ctx.Done():
			return
		case f := <-c.out:
			if f.isEvent {
				c.mu.Lock()
				current := c.eventsEnabled && f.eventGeneration == c.eventGeneration
				c.mu.Unlock()
				if !current {
					c.queuedBytes.Add(-frameSize(f))
					continue
				}
			}
			err := c.writeFrame(f)
			c.queuedBytes.Add(-frameSize(f))
			if err != nil {
				return
			}
			// 原版正常退出和错误密码在最终回复后发送断开通知；始终由唯一写协程发送。
			if f.Get("Reply-Text") == "+OK bye" || f.Get("Reply-Text") == "-ERR invalid" {
				body := []byte("Disconnected, goodbye.\nSee you at ClueCon! http://www.cluecon.com/\n")
				_ = c.writeFrame(Frame{Headers: []Header{{"Content-Type", "text/disconnect-notice"}, {"Content-Length", strconv.Itoa(len(body))}}, Body: body})
				return
			}
		}
	}
}

// writeFrame 写入完整单帧，截止时间不会随短写逐次续期。
func (c *client) writeFrame(f Frame) error {
	wire, err := Encode(f)
	if err != nil {
		return err
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.server.options.WriteTimeout)); err != nil {
		return err
	}
	for len(wire) > 0 {
		n, err := c.conn.Write(wire)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		wire = wire[n:]
	}
	return nil
}

// read 对每帧施加绝对截止时间；空行和逐字节慢发不能无限延长当前预算。
func (c *client) read() {
	defer c.finished()
	closeOnReturn := true
	defer func() {
		if closeOnReturn {
			c.close()
		}
	}()
	if !c.enqueue(Frame{Headers: []Header{{"Content-Type", "auth/request"}}}) {
		return
	}
	reader := bufio.NewReaderSize(c.conn, 4096)
	for {
		c.mu.Lock()
		authed := c.authed
		c.mu.Unlock()
		timeout := c.server.options.FrameTimeout
		if !authed {
			timeout = time.Until(c.authDeadline)
			if timeout <= 0 {
				return
			}
		} else {
			c.conn.SetReadDeadline(time.Now().Add(30 * time.Minute))
			if _, err := reader.Peek(1); err != nil {
				return
			}
		}
		c.conn.SetReadDeadline(time.Now().Add(timeout))
		f, err := ReadFrame(reader, true, 64*1024, 16*1024*1024)
		if err != nil {
			return
		}
		if !c.command(f) {
			closeOnReturn = false
			return
		}
	}
}

// command 明确区分协议命令与业务 API；未实现的控制方式必须返回错误。
func (c *client) command(f Frame) bool {
	command, args, hasArguments := strings.Cut(f.Command, " ")
	command = strings.ToLower(command)
	c.mu.Lock()
	authed := c.authed
	c.mu.Unlock()
	if command == "exit" || command == "..." {
		c.enqueue(reply("+OK bye"))
		return false
	}
	if !authed {
		if command == "auth" && hasArguments {
			if subtle.ConstantTimeCompare([]byte(args), []byte(c.server.options.Password)) == 1 {
				c.mu.Lock()
				c.authed = true
				c.mu.Unlock()
				c.enqueue(reply("+OK accepted"))
				return true
			}
			c.enqueue(reply("-ERR invalid"))
			return false
		}
		// 原版非 auth 命令保持未认证，回复 command not found；不得调用业务处理器。
		c.enqueue(reply("-ERR command not found"))
		return true
	}
	if command == "sendmsg" {
		c.executeMessage(f, args)
		return true
	}
	if len(f.Body) > 0 {
		c.enqueue(reply("-ERR command body not supported"))
		return true
	}
	switch command {
	case "api", "bgapi":
		name, arguments, _ := strings.Cut(args, " ")
		if command == "api" {
			// fs_cli -x 发送 console_execute: true；它要求完整控制台命令路径，不能直接忽略头。
			console, err := consoleExecuteFlag(f.Get("console_execute"))
			if err != nil {
				c.enqueue(apiReply("-ERR " + err.Error() + "\n"))
				return true
			}
			if console {
				name, arguments, err = singleConsoleCommand(args)
				if err != nil {
					c.enqueue(apiReply("-ERR " + err.Error() + "\n"))
					return true
				}
			}
			ctx, cancel := context.WithTimeout(c.ctx, c.server.options.CommandTimeout)
			value := c.server.options.API(ctx, trimAPIWhitespace(name), trimAPIWhitespace(arguments))
			cancel()
			c.enqueue(apiReply(value))
			return true
		}
		uuid := f.Get("Job-UUID")
		if uuid == "" {
			uuid = newUUID()
		}
		if len(uuid) > 36 {
			uuid = uuid[:36]
		}
		// 回复入队和任务入队在同一锁内，工作线程发布事件之前先保证命令回复已入队。
		c.server.mu.Lock()
		if len(c.server.jobs) == cap(c.server.jobs) {
			c.server.mu.Unlock()
			c.enqueue(reply("-ERR background queue full"))
			return true
		}
		ok := c.enqueue(Frame{Headers: []Header{{"Content-Type", "command/reply"}, {"Reply-Text", "+OK Job-UUID: " + uuid}, {"Job-UUID", uuid}}})
		if ok {
			c.server.jobs <- job{name, arguments, uuid}
		}
		c.server.mu.Unlock()
	case "event":
		c.subscription(args, false)
	case "nixevent":
		c.subscription(args, true)
	case "noevents":
		c.mu.Lock()
		enabled := c.eventsEnabled
		c.eventsEnabled = false
		c.eventGeneration++
		c.events = make(map[string]bool)
		c.mu.Unlock()
		if enabled {
			c.enqueue(reply("+OK no longer listening for events"))
		} else {
			c.enqueue(reply("-ERR not listening for events"))
		}
	case "filter":
		c.filter(args)
	case "linger", "nolinger":
		c.enqueue(reply("-ERR not controlling a session"))
	case "nolog":
		c.enqueue(reply("-ERR not loging"))
	default:
		c.enqueue(reply("-ERR command not found"))
	}
	return true
}

// subscription 仅声明目前真实产生的事件；不把尚未实现的事件订阅伪装成可用能力。
func (c *client) subscription(args string, remove bool) {
	parts := strings.Fields(args)
	c.mu.Lock()
	defer c.mu.Unlock()
	format := c.format
	if !remove && len(parts) > 0 {
		first := strings.ToLower(parts[0])
		if first == "plain" || first == "json" || first == "xml" {
			format = first
			c.format = format // 原版即使随后没有有效关键词，也先保存显式格式。
			parts = parts[1:]
		}
	}
	if len(parts) == 0 {
		c.enqueue(reply("-ERR no keywords supplied"))
		return
	}
	for i, name := range parts {
		parts[i] = strings.ToUpper(name)
		found := parts[i] == "ALL"
		for _, supported := range supportedEvents {
			found = found || parts[i] == supported
		}
		if !found {
			c.enqueue(reply("-ERR event not supported: " + name))
			return
		}
	}
	for _, name := range parts {
		if remove {
			if name == "ALL" {
				c.events = make(map[string]bool)
			} else {
				if c.events["ALL"] {
					delete(c.events, "ALL")
					for _, n := range supportedEvents {
						c.events[n] = true
					}
				}
				delete(c.events, name)
			}
		} else {
			c.events[name] = true
		}
	}
	// nixevent ALL 清空选择，但原版仍保留 EVENTS 开关；随后 noevents 必须返回成功。
	c.eventsEnabled = true
	if remove {
		c.enqueue(reply("+OK events nixed"))
	} else {
		c.enqueue(reply("+OK event listener enabled " + format))
	}
}

// filter 实现普通字符串的忽略大小写 OR 匹配及负匹配排除；正则语义另列未兼容。
func (c *client) filter(args string) {
	args = strings.TrimLeft(args, " ")
	name, value, hasValue := strings.Cut(args, " ")
	remove := strings.EqualFold(name, "delete") && hasValue
	if remove || (strings.EqualFold(name, "add") && hasValue) {
		name, value, hasValue = strings.Cut(value, " ")
	}
	if name == "" || (!remove && !hasValue) {
		c.enqueue(reply("-ERR invalid syntax"))
		return
	}
	if strings.ContainsAny(name, ":\r\n\x00") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "ARRAY::") || strings.ContainsAny(name, "[]") {
		// 正则、数组和索引属于另外的原事件容器语义，不能退化成普通字符串后假装支持。
		c.enqueue(reply("-ERR advanced filters not supported"))
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if remove || value == "" {
		kept := c.filters[:0]
		for _, f := range c.filters {
			if remove && strings.EqualFold(name, "all") {
				continue
			}
			if !strings.EqualFold(f.header, name) || (value != "" && f.value != value) {
				kept = append(kept, f)
			}
		}
		c.filters = kept
		if remove {
			c.enqueue(reply(fmt.Sprintf("+OK filter deleted. [%s][%s]", name, value)))
		} else {
			// switch_event_add_header_string 的空值删除同名头；回复仍为 filter added。
			c.enqueue(reply(fmt.Sprintf("+OK filter added. [%s]=[%s]", name, value)))
		}
		return
	}
	if len(c.filters) >= 128 {
		c.enqueue(reply("-ERR filter budget exceeded"))
		return
	}
	c.filters = append(c.filters, eventFilter{name, value})
	c.enqueue(reply(fmt.Sprintf("+OK filter added. [%s]=[%s]", name, value)))
}

// worker 使用固定并行度。Job-UUID 是关联号，不是幂等键；相同值的任务仍分别执行。
func (s *Server) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.jobs:
			if s.ctx.Err() != nil {
				return
			}
			ctx, cancel := context.WithTimeout(s.ctx, s.options.CommandTimeout)
			body := s.options.API(ctx, trimAPIWhitespace(j.command), trimAPIWhitespace(j.arguments))
			cancel()
			h := map[string]string{"Job-UUID": j.uuid, "Job-Command": j.command}
			if j.arguments != "" {
				h["Job-Command-Arg"] = j.arguments
			}
			s.Publish("BACKGROUND_JOB", h, []byte(body))
		}
	}
}

// trimAPIWhitespace 对齐 switch_api_execute 执行前的 ASCII 空白裁剪；
// 作业事件仍使用原始 j.arguments，不能把协议输入和实际 API 参数混为一谈。
func trimAPIWhitespace(value string) string { return strings.Trim(value, " \t\r\n\v\f") }

func newUUID() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Publish 复制业务事件并分发，连接间订阅与过滤器互不影响；不持有任何业务锁等待网络。
func (s *Server) Publish(name string, headers map[string]string, body []byte) {
	h := make(map[string]string, len(headers)+5)
	for k, v := range headers {
		h[k] = v
	}
	h["Event-Name"] = name
	h["Event-Date-Timestamp"] = strconv.FormatInt(time.Now().UnixMicro(), 10)
	h["Event-Sequence"] = strconv.FormatUint(s.sequence.Add(1), 10)
	h["FreeSWITCH-Switchname"] = "rustswitch"
	var copiedBody []byte
	if body != nil {
		copiedBody = append([]byte{}, body...)
	}
	e := event{name, h, copiedBody}
	s.mu.Lock()
	clients := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	// 同一事件按每种格式最多序列化一次；不可变帧可以安全共享给各连接，避免订阅数放大编码开销。
	encoded := make(map[string]Frame, 3)
	encodingErrors := make(map[string]error, 3)
	for _, c := range clients {
		c.mu.Lock()
		if !c.authed || !c.eventsEnabled || (!c.events["ALL"] && !c.events[name]) || !matchFilters(c.filters, h) {
			c.mu.Unlock()
			continue
		}
		f, ready := encoded[c.format]
		err, failed := encodingErrors[c.format]
		if !ready && !failed {
			f, err = serializeEvent(e, c.format)
			if err == nil {
				encoded[c.format] = f
			} else {
				encodingErrors[c.format] = err
			}
		}
		if err == nil {
			f.isEvent = true
			f.eventGeneration = c.eventGeneration
			c.enqueue(f)
		} else {
			c.closeWithDrop(true)
		}
		c.mu.Unlock()
	}
}

func matchFilters(filters []eventFilter, h map[string]string) bool {
	if len(filters) == 0 {
		return true
	}
	send := false
	for _, f := range filters {
		var actual string
		found := false
		for k, v := range h {
			if strings.EqualFold(k, f.header) {
				actual = v
				found = true
				break
			}
		}
		if !found {
			continue
		}
		value := f.value
		positive := true
		for len(value) > 0 && (value[0] == '+' || value[0] == '-' || value[0] == ' ') {
			if value[0] == '+' {
				positive = true
			}
			if value[0] == '-' {
				positive = false
			}
			value = value[1:]
		}
		if strings.EqualFold(actual, value) {
			if !positive {
				return false
			}
			send = true
		}
	}
	return send
}

// serializeEvent 的外层长度按实际编码字节计算；plain 事件值使用百分号编码，正文保持原始字节。
func serializeEvent(e event, format string) (Frame, error) {
	var body []byte
	if format == "json" {
		h := make(map[string]string, len(e.headers)+1)
		for k, v := range e.headers {
			h[k] = v
		}
		if e.body != nil {
			h["Content-Length"] = strconv.Itoa(len(e.body))
			h["_body"] = string(e.body)
		}
		var err error
		body, err = json.Marshal(h)
		if err != nil {
			return Frame{}, err
		}
	} else if format == "xml" {
		var err error
		body, err = serializeXMLEvent(e)
		if err != nil {
			return Frame{}, err
		}
	} else {
		keys := make([]string, 0, len(e.headers))
		for k := range e.headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			if strings.ContainsAny(k, ": \r\n\x00") {
				return Frame{}, errors.New("invalid event header")
			}
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(encodeEventValue(e.headers[k]))
			b.WriteByte('\n')
		}
		if len(e.body) > 0 {
			fmt.Fprintf(&b, "Content-Length: %d\n", len(e.body))
		}
		b.WriteByte('\n')
		b.Write(e.body)
		body = []byte(b.String())
	}
	return Frame{Headers: []Header{{"Content-Type", "text/event-" + format}, {"Content-Length", strconv.Itoa(len(body))}}, Body: body}, nil
}
