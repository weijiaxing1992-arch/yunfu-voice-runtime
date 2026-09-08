package server

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"rustswitch/control/internal/dialplan"
	"rustswitch/control/internal/esl"
	"rustswitch/control/internal/media"
	"strconv"
	"strings"
	"time"
)

const (
	applicationQueuePerLeg = 8
	applicationByteBudget  = 16 * 1024 * 1024
	applicationLifetime    = 60 * time.Second
)

// applicationRequest 仅在呼叫主循环受理；ESL 线程不直接读取通道映射。
type applicationRequest struct {
	ctx   context.Context
	value esl.ExecuteRequest
	reply chan string
}

// applicationMediaResult 在固定媒体线程完成后回到主循环，不产生每通话后台协程。
type applicationMediaResult struct {
	worker     int
	generation uint64
	uuid       string
	playbackID uint64
	op         string
	reply      media.Reply
	err        error
}

type applicationCursor struct {
	after, generation uint64
	inflight          bool
	next              time.Time
}

// applicationEngine 的所有字段只由 Server.Run 访问；堆中每条活跃通道最多一个定时项。
type applicationEngine struct {
	lanes                map[string]*applicationLane
	disabledDigits       map[uint64][2]string // 媒体观测不完整后持续禁用收号，直到真实挂机。
	waiting              applicationHeap
	workers              []applicationCursor
	jobs, bytes, maxJobs int
	nextPlayback         uint64
}

type applicationLane struct {
	plan       *dialplan.Extension // XML动作表只读共享；索引随真实完成推进。
	planIndex  int
	planFailed bool
	uuid       string
	call       *Call
	side       int
	queue      []*applicationJob
	active     *applicationJob
	heapIndex  int
	due        time.Time
}

type applicationTone struct {
	durationMS  uint32
	frequencyHz uint16
}
type applicationRead struct {
	minimum, maximum      int
	variable, terminators string
	timeout, digitTimeout time.Duration
}

// applicationJob 同时保存应用状态和媒体确认状态，只有真实媒体确认才能完成播放。
type applicationJob struct {
	filePath                                 string // 媒体root内相对WAV文件名；实际打开与格式检查归Rust。
	sleep                                    time.Duration
	sleepDeadline                            time.Time
	fromDialplan                             bool
	request                                  esl.ExecuteRequest
	read                                     *applicationRead
	tone                                     *applicationTone
	variable, value, digits                  string
	remove                                   bool
	bytes                                    int
	started                                  bool
	hardDeadline, digitDeadline, playbackDue time.Time
	playbackID                               uint64
	playbackState                            string
	mediaInflight                            bool
	stopWanted                               bool
	finishWanted                             bool
	finishResponse                           string
	playbackStarted, playbackStopped         bool // 四事件按当前任务身份去重，不借订阅成功伪造播放。
	parkEntered, parkLeft                    bool
	playbackRetries                          uint8 // 有界退避，失败不续期、不为每条通话创建定时协程。
	endedPending, releaseConfirmed           bool  // 挂机后仍等待实际媒体所有权释放和已提交RPC回复。
}

type applicationHeap []*applicationLane

func (h applicationHeap) Len() int           { return len(h) }
func (h applicationHeap) Less(i, j int) bool { return h[i].due.Before(h[j].due) }
func (h applicationHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIndex = i
	h[j].heapIndex = j
}
func (h *applicationHeap) Push(value any) {
	lane := value.(*applicationLane)
	lane.heapIndex = len(*h)
	*h = append(*h, lane)
}
func (h *applicationHeap) Pop() any {
	old := *h
	lane := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	lane.heapIndex = -1
	return lane
}

// initApplications 在媒体池创建后、Run 前初始化全部有界队列。
func (s *Server) initApplications() {
	workers := 0
	if s.pool != nil {
		workers = len(s.pool.Workers)
	}
	limit := s.Config.Limits.MaxCalls*2 + 128
	if limit < 128 {
		limit = 128
	}
	if limit > 20000 {
		limit = 20000
	}
	s.applicationRequests = make(chan applicationRequest, 128)
	s.applicationResults = make(chan applicationMediaResult, workers*128+128)
	s.applications = &applicationEngine{lanes: make(map[string]*applicationLane), disabledDigits: make(map[uint64][2]string), workers: make([]applicationCursor, workers), maxJobs: limit}
}

// CompatibilityExecute 只返回入队受理；已受理应用归通道所有，不因 ESL 连接关闭丢失。
func (s *Server) CompatibilityExecute(ctx context.Context, value esl.ExecuteRequest) string {
	if s.applicationRequests == nil || s.ctx == nil {
		return "-ERR application execution is unavailable"
	}
	r := applicationRequest{ctx: ctx, value: value, reply: make(chan string, 1)}
	select {
	case <-ctx.Done():
		return "-ERR operation cancelled"
	default:
	}
	select {
	case s.applicationRequests <- r:
	case <-ctx.Done():
		return "-ERR operation cancelled"
	default:
		return "-ERR application request queue full"
	}
	select {
	case result := <-r.reply:
		return result
	case <-ctx.Done():
		return "-ERR operation cancelled"
	case <-s.ctx.Done():
		return "-ERR server stopped"
	}
}

// validApplicationVariable 仅开放基础变量，不隐式宣称数组、展开、导出及身份覆写兼容。
func validApplicationVariable(name string) bool {
	return name != "" && len(name) <= 128 && name != "uuid" && name != "sip_call_id" && !strings.ContainsAny(name, "= []{}$\t\r\n\x00")
}

// parseApplicationTone 精确开放 tone_stream 的单频、零间隔、有限时长子集。
func parseApplicationTone(value string) (*applicationTone, error) {
	if !strings.HasPrefix(value, "tone_stream://%(") || !strings.HasSuffix(value, ")") {
		return nil, errors.New("only bounded tone_stream://%(duration_ms,0,frequency_hz) is supported")
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(value, "tone_stream://%("), ")"), ",")
	if len(parts) != 3 || parts[1] != "0" {
		return nil, errors.New("tone_stream requires one frequency and zero interval")
	}
	duration, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil || duration < 20 || duration > 10000 || duration%20 != 0 {
		return nil, errors.New("tone duration must be 20..10000 ms in multiples of20")
	}
	frequency, err := strconv.ParseUint(parts[2], 10, 16)
	if err != nil || frequency < 200 || frequency > 2000 {
		return nil, errors.New("tone frequency must be 200..2000 Hz")
	}
	return &applicationTone{uint32(duration), uint16(frequency)}, nil
}

// parseApplication 明确校验已实现边界；不能把任意文件、TTS 或正则菜单当作可执行。
func parseApplication(request esl.ExecuteRequest) (*applicationJob, error) {
	j := &applicationJob{request: request, bytes: len(request.Arguments) + len(request.Application) + len(request.EventUUID) + 256}
	if len(request.Arguments) > 4096 || strings.Contains(request.Arguments, "${") || strings.ContainsRune(request.Arguments, 0) {
		return nil, errors.New("argument expansion or size is unsupported")
	}
	switch request.Application {
	case "answer", "park":
		if request.Arguments != "" {
			return nil, errors.New("answer/park flags are unsupported")
		}
	case "hangup":
		if request.Arguments != "" && request.Arguments != "NORMAL_CLEARING" {
			return nil, errors.New("only NORMAL_CLEARING hangup is supported")
		}
	case "sleep":
		value, err := strconv.ParseUint(request.Arguments, 10, 32)
		if err != nil || value > 3600000 {
			return nil, errors.New("sleep requires integer0..3600000 milliseconds")
		}
		j.sleep = time.Duration(value) * time.Millisecond
	case "set":
		j.variable, j.value, _ = strings.Cut(request.Arguments, "=")
		j.remove = j.value == ""
		if !validApplicationVariable(j.variable) {
			return nil, errors.New("unsupported variable name")
		}
	case "unset":
		j.variable = request.Arguments
		j.remove = true
		if !validApplicationVariable(j.variable) {
			return nil, errors.New("unsupported variable name")
		}
	case "playback":
		var err error
		j.tone, j.filePath, err = parseApplicationPlayback(request.Arguments)
		if err != nil {
			return nil, err
		}
	case "read":
		parts := strings.Fields(request.Arguments)
		if len(parts) < 4 || len(parts) > 7 {
			return nil, errors.New("read requires min max silence-or-tone variable [timeout_ms [terminators [digit_timeout_ms]]]")
		}
		minimum, err := strconv.Atoi(parts[0])
		if err != nil || minimum < 1 || minimum > 64 {
			return nil, errors.New("read minimum must be 1..64")
		}
		maximum, err := strconv.Atoi(parts[1])
		if err != nil || maximum < minimum || maximum > 64 {
			return nil, errors.New("read maximum must be minimum..64")
		}
		if !validApplicationVariable(parts[3]) || parts[3] == "read_result" || parts[3] == "read_terminator_used" {
			return nil, errors.New("unsupported read result variable")
		}
		if parts[2] != "silence" {
			j.tone, j.filePath, err = parseApplicationPlayback(parts[2])
			if err != nil {
				return nil, err
			}
		}
		r := &applicationRead{minimum: minimum, maximum: maximum, variable: parts[3], terminators: "#", timeout: time.Second, digitTimeout: time.Second}
		if len(parts) > 4 {
			value, e := strconv.Atoi(parts[4])
			if e != nil || value < 0 || value > 60000 {
				return nil, errors.New("read timeout must be 0..60000 ms")
			}
			if value < 1000 {
				value = 1000
			}
			r.timeout = time.Duration(value) * time.Millisecond
			r.digitTimeout = r.timeout
		}
		if len(parts) > 5 {
			r.terminators = parts[5]
			if r.terminators == "none" {
				r.terminators = ""
			}
			for _, digit := range r.terminators {
				if !strings.ContainsRune("0123456789*#ABCD", digit) {
					return nil, errors.New("read supports literal DTMF terminators or none")
				}
			}
		}
		if len(parts) > 6 {
			value, e := strconv.Atoi(parts[6])
			if e != nil || value < 0 || value > 60000 {
				return nil, errors.New("digit timeout must be 0..60000 ms")
			}
			if value > 0 {
				r.digitTimeout = time.Duration(value) * time.Millisecond
			}
		}
		j.read = r
	default:
		return nil, errors.New("application is not implemented")
	}
	return j, nil
}

// handleApplicationRequest 与信令状态串行，先获得实际通道及队列额度，再回复受理。
func (s *Server) handleApplicationRequest(r applicationRequest) {
	respond := func(value string) {
		select {
		case r.reply <- value:
		default:
		}
	}
	if r.ctx.Err() != nil {
		respond("-ERR operation cancelled")
		return
	}
	entry := s.compatChannels[r.value.UUID]
	if entry.call == nil || entry.call.Ended {
		respond(fmt.Sprintf("-ERR invalid session id [%s]", r.value.UUID))
		return
	}
	j, err := parseApplication(r.value)
	if err != nil {
		respond("-ERR " + err.Error())
		return
	}
	if j.hasPlayback() && s.pcmOwnsTX(entry.call) {
		respond("-ERR PCM turn owns audio output; confirm its terminal state first")
		return
	}
	e := s.applications
	if e == nil {
		respond("-ERR application execution is unavailable")
		return
	}
	if err := s.applicationReady(&applicationLane{call: entry.call, side: entry.side}, j); err != nil {
		respond("-ERR " + err.Error())
		return
	}
	lane := e.lanes[r.value.UUID]
	if lane != nil && lane.active != nil && !r.value.EventLock {
		respond("-ERR concurrent application requires event-lock true; interruption is unsupported")
		return
	}
	if e.jobs >= e.maxJobs || e.bytes+j.bytes > applicationByteBudget || lane != nil && len(lane.queue)+1 >= applicationQueuePerLeg {
		respond("-ERR application queue full")
		return
	}
	if lane == nil {
		lane = &applicationLane{uuid: r.value.UUID, call: entry.call, side: entry.side, heapIndex: -1}
		e.lanes[lane.uuid] = lane
	}
	lane.queue = append(lane.queue, j)
	e.jobs++
	e.bytes += j.bytes
	respond("+OK")
	s.startNextApplication(lane, time.Now())
}

// applicationVariable 复用基本变量预算，内部收号结果也不能绕过每腿 128 项/64KiB 限额。
func applicationVariable(lane *applicationLane, name, value string, remove bool) error {
	variables := lane.call.compatVariables[lane.side]
	if remove {
		delete(variables, name)
		return nil
	}
	size := len(name) + len(value)
	for key, existing := range variables {
		if key != name {
			size += len(key) + len(existing)
		}
	}
	if _, ok := variables[name]; !ok && len(variables) >= 128 || size > 65536 {
		return errors.New("variable budget exceeded")
	}
	if variables == nil {
		variables = make(map[string]string)
		lane.call.compatVariables[lane.side] = variables
	}
	variables[name] = value
	return nil
}

// applicationEvent 仅发布真实开始、完成和明确失败的局部字段，不伪造完整 FreeSWITCH 通道状态。
func (s *Server) applicationEvent(lane *applicationLane, j *applicationJob, name, response string) {
	if s.compatEvents == nil {
		return
	}
	headers := map[string]string{"Unique-ID": lane.uuid, "Application": j.request.Application, "Application-Data": j.request.Arguments, "Application-UUID": j.request.EventUUID, "Application-UUID-Name": j.request.EventUUIDName}
	if name == "CHANNEL_EXECUTE_COMPLETE" {
		headers["Application-Response"] = response
		if !j.started {
			headers["variable_rustswitch_application_state"] = "cancelled_before_execution"
		}
	}
	for key, value := range lane.call.compatVariables[lane.side] {
		headers["variable_"+key] = value
	}
	s.compatEvents(name, headers, nil)
}

// startNextApplication 每腿串行推进；一次最多处理八条已受理应用，长期收号留在堆中等待。
func (s *Server) startNextApplication(lane *applicationLane, now time.Time) {
	for lane.active == nil {
		if lane.call.Ended {
			return
		}
		if lane.planFailed {
			s.hangupApplication(lane.call)
			return
		}
		if len(lane.queue) == 0 && !s.pullDialplanAction(lane) {
			break
		}
		j := lane.queue[0]
		lane.queue[0] = nil
		lane.queue = lane.queue[1:]
		lane.active = j
		j.started = true
		j.hardDeadline = now.Add(applicationLifetime)
		s.applicationEvent(lane, j, "CHANNEL_EXECUTE", "")
		if err := s.applicationReady(lane, j); err != nil {
			s.completeApplication(lane, "-ERR "+err.Error())
			continue
		}
		switch j.request.Application {
		case "answer":
			if lane.call.Established {
				s.completeApplication(lane, "_none_")
				continue
			}
			if !s.answerLocalApplication(lane.call) {
				s.completeApplication(lane, "-ERR cannot answer channel")
				continue
			}
			j.hardDeadline = time.Time{} // 已有SIP ACK期限负责真实回收；不能60秒后假完成。
			return
		case "hangup":
			s.hangupApplication(lane.call)
			return
		case "park":
			j.hardDeadline = time.Time{} // 挂机/全通话期限才释放，无每通道轮询或额外线程。
			s.parkLifecycle(lane, j, false)
			return
		case "sleep":
			j.hardDeadline = time.Time{}
			if j.sleep == 0 {
				s.completeApplication(lane, "_none_")
				continue
			}
			j.sleepDeadline = now.Add(j.sleep)
			s.scheduleApplication(lane)
			return
		}
		if j.read == nil && !j.hasPlayback() {
			err := applicationVariable(lane, j.variable, j.value, j.remove)
			response := "_none_"
			if err != nil {
				response = "-ERR " + err.Error()
			}
			s.completeApplication(lane, response)
			continue
		}
		if j.request.Application == "playback" {
			if _, err := playbackTerminators(lane.call.compatVariables[lane.side]); err != nil {
				s.completeApplication(lane, "-ERR "+err.Error())
				continue
			}
			_ = applicationVariable(lane, "playback_terminator_used", "", true)
		}
		if j.read != nil {
			if reason := s.applications.disabledDigits[lane.call.ID][lane.side]; reason != "" && !strings.EqualFold(lane.call.compatVariables[lane.side]["dtmf_type"], "info") {
				s.completeApplication(lane, "-ERR DTMF collection disabled: "+reason)
				continue
			}
			_ = applicationVariable(lane, j.read.variable, "", true)
			_ = applicationVariable(lane, "read_result", "", true)
			_ = applicationVariable(lane, "read_terminator_used", "", true)
			if !j.hasPlayback() {
				j.digitDeadline = now.Add(j.read.timeout)
			}
		}
		if j.hasPlayback() {
			// 应用排队后可能出现新PCM授权，实际发起媒体操作前再次核对TX归属。
			if s.pcmOwnsTX(lane.call) {
				s.completeApplication(lane, "-ERR PCM turn owns audio output")
				continue
			}
			s.applications.nextPlayback++
			j.playbackID = s.applications.nextPlayback
			if j.playbackID == 0 {
				s.applications.nextPlayback++
				j.playbackID = s.applications.nextPlayback
			}
			j.playbackState = "starting"
			op := "playback_start"
			if j.filePath != "" {
				op = "playback_file_start"
			}
			if err := s.submitApplicationMedia(lane, op); err != nil {
				if j.read != nil {
					_ = applicationVariable(lane, "read_result", "failure", false)
				}
				s.completeApplication(lane, "-ERR "+err.Error())
				continue
			}
		}
		s.scheduleApplication(lane)
	}
	if lane.active == nil && len(lane.queue) == 0 {
		delete(s.applications.lanes, lane.uuid)
	}
}

func (s *Server) completeApplication(lane *applicationLane, response string) {
	j := lane.active
	if j == nil {
		return
	}
	if lane.heapIndex >= 0 {
		heap.Remove(&s.applications.waiting, lane.heapIndex)
	}
	if j.fromDialplan && strings.HasPrefix(response, "-ERR") {
		lane.planFailed = true
	}
	s.parkLifecycle(lane, j, true)
	s.applicationEvent(lane, j, "CHANNEL_EXECUTE_COMPLETE", response)
	s.applications.jobs--
	s.applications.bytes -= j.bytes
	lane.active = nil
}

// finishApplication 有在途播放时先申请停止并等确认，不把声卡/RTP残留当作已经完成。
func (s *Server) finishApplication(lane *applicationLane, response string, now time.Time) {
	j := lane.active
	if j == nil {
		return
	}
	j.finishWanted = true
	j.finishResponse = response
	j.digitDeadline = time.Time{}
	if j.playbackID != 0 && (j.mediaInflight || j.playbackState == "starting" || j.playbackState == "loading" || j.playbackState == "running") {
		j.stopWanted = true
		if !j.mediaInflight {
			if err := s.submitApplicationMedia(lane, "playback_stop"); err != nil {
				applicationPlaybackRetry(j, now)
			}
		}
		s.scheduleApplication(lane)
		return
	}
	s.completeApplication(lane, response)
	s.startNextApplication(lane, now)
}

// scheduleApplication 原位更新堆节点，按键重置超时不会留下无界的过期定时对象。
func (s *Server) scheduleApplication(lane *applicationLane) {
	j := lane.active
	if j == nil {
		return
	}
	due := j.hardDeadline
	for _, candidate := range []time.Time{j.digitDeadline, j.playbackDue, j.sleepDeadline} {
		if !candidate.IsZero() && (due.IsZero() || candidate.Before(due)) {
			due = candidate
		}
	}
	if due.IsZero() {
		if lane.heapIndex >= 0 {
			heap.Remove(&s.applications.waiting, lane.heapIndex)
		}
		return
	}
	lane.due = due
	if lane.heapIndex < 0 {
		heap.Push(&s.applications.waiting, lane)
	} else {
		heap.Fix(&s.applications.waiting, lane.heapIndex)
	}
}

// submitApplicationMedia 复用固定媒体队列，应用不能新增进程或独占信令线程等待 RPC。
func (s *Server) submitApplicationMedia(lane *applicationLane, op string) error {
	j := lane.active
	if s.pool == nil || j == nil {
		return errors.New("media unavailable")
	}
	leg := "a"
	if lane.side == 1 {
		leg = "b"
	}
	request := media.Request{Op: op, Session: lane.call.ID, Generation: lane.call.Generation, PlaybackID: j.playbackID, Leg: leg, FilePath: j.filePath}
	if j.tone != nil {
		request.DurationMS = j.tone.durationMS
		request.FrequencyHz = j.tone.frequencyHz
	}
	r := applicationMediaResult{worker: lane.call.Worker, generation: lane.call.Generation, uuid: lane.uuid, playbackID: j.playbackID, op: op}
	err := s.pool.Submit(s.ctx, r.worker, request, func(reply media.Reply, err error) {
		r.reply, r.err = reply, err
		select {
		case s.applicationResults <- r:
		case <-s.ctx.Done():
		}
	})
	if err == nil {
		j.mediaInflight = true
		j.playbackDue = time.Time{}
	}
	return err
}

// advanceApplications 只弹出到期通道；DTMF每分片最多一个在途请求，与通话并发数量无关。
func (s *Server) advanceApplications(now time.Time) {
	e := s.applications
	if e == nil {
		return
	}
	for processed := 0; processed < 256 && len(e.waiting) > 0 && !e.waiting[0].due.After(now); processed++ {
		lane := heap.Pop(&e.waiting).(*applicationLane)
		j := lane.active
		if j == nil {
			continue
		}
		if !j.sleepDeadline.IsZero() && !now.Before(j.sleepDeadline) {
			s.finishApplication(lane, "_none_", now)
			continue
		}
		if !j.hardDeadline.IsZero() && !now.Before(j.hardDeadline) {
			// 业务硬期限到达后仍不能留下播放；媒体停止未确认会以失败结束并由通道媒体释放保证回收。
			if j.playbackID != 0 && (j.mediaInflight || j.playbackState == "starting" || j.playbackState == "loading" || j.playbackState == "running") {
				// 无法确认播放已停止时终止真实双腿并回收媒体，不能只删除应用状态留下声音。
				s.compatibilityChannelCommand("uuid_kill", lane.uuid)
				continue
			}
			if j.read != nil {
				_ = applicationVariable(lane, "read_result", "failure", false)
			}
			s.finishApplication(lane, "-ERR application lifetime exceeded", now)
			continue
		}
		if !j.digitDeadline.IsZero() && !now.Before(j.digitDeadline) {
			result := "timeout"
			if len(j.digits) < j.read.minimum {
				result = "failure"
			}
			s.finishRead(lane, result, "", now)
			continue
		}
		if !j.playbackDue.IsZero() && !now.Before(j.playbackDue) && !j.mediaInflight {
			op := "playback_status"
			if j.stopWanted {
				op = "playback_stop"
			}
			if err := s.submitApplicationMedia(lane, op); err != nil {
				applicationPlaybackRetry(j, now)
			}
		}
		s.scheduleApplication(lane)
	}
	if s.pool == nil {
		return
	}
	for worker := range e.workers {
		cursor := &e.workers[worker]
		if cursor.inflight || now.Before(cursor.next) {
			continue
		}
		generation := s.pool.Workers[worker].Generation.Load()
		if cursor.generation != generation {
			cursor.after = 0
			cursor.generation = generation
		}
		r := applicationMediaResult{worker: worker, generation: generation, op: "dtmf_events"}
		request := media.Request{Op: "dtmf_events", Generation: generation, AfterSeq: cursor.after, Limit: 64}
		err := s.pool.Submit(s.ctx, worker, request, func(reply media.Reply, err error) {
			r.reply, r.err = reply, err
			select {
			case s.applicationResults <- r:
			case <-s.ctx.Done():
			}
		})
		cursor.next = now.Add(50 * time.Millisecond)
		if err == nil {
			cursor.inflight = true
		}
	}
}

// handleApplicationMedia 检查通道、分片代次和播放身份，迟到回复不能命中新呼叫或后续应用。
func (s *Server) handleApplicationMedia(r applicationMediaResult) {
	e := s.applications
	if e == nil {
		return
	}
	if r.op == "dtmf_events" {
		s.handleApplicationDigits(r)
		return
	}
	lane := e.lanes[r.uuid]
	if lane == nil || lane.active == nil || lane.active.playbackID != r.playbackID || lane.call.Generation != r.generation {
		return
	}
	j := lane.active
	j.mediaInflight = false
	now := time.Now()
	if j.endedPending {
		// 已提交的开始/查询可能晚于release回复到主循环；先收集真实开始证据，再统一结束。
		if r.err == nil && r.reply.OK && r.reply.Type == "playback_state" && r.reply.PlaybackID == j.playbackID {
			s.observePlaybackLifecycle(lane, j, r.reply)
		}
		s.finishReleasedApplication(lane)
		return
	}
	if r.err != nil || !r.reply.OK || r.reply.Type != "playback_state" || r.reply.PlaybackID != j.playbackID {
		message := "invalid playback confirmation"
		if r.err != nil {
			message = r.err.Error()
		}
		if r.reply.Message != "" {
			message = r.reply.Message
		}
		// RPC失联或异常回执并不证明播放器已经停止。有界退避重试stop，最终由硬期限
		// 终止真实通道并等待release；不能发虚假的STOP或提前交付应用完成。
		j.finishWanted, j.stopWanted = true, true
		j.finishResponse = "-ERR " + message
		j.digitDeadline = time.Time{}
		applicationPlaybackRetry(j, now)
		if j.read != nil {
			_ = applicationVariable(lane, "read_result", "failure", false)
		}
		s.scheduleApplication(lane)
		return
	}
	j.playbackState = r.reply.State
	s.observePlaybackLifecycle(lane, j, r.reply)
	switch r.reply.State {
	case "loading", "running":
		if j.stopWanted {
			if err := s.submitApplicationMedia(lane, "playback_stop"); err != nil {
				applicationPlaybackRetry(j, now)
			}
		} else {
			delay := 100 * time.Millisecond
			if r.op == "playback_start" && j.tone != nil {
				delay = time.Duration(j.tone.durationMS)*time.Millisecond + 50*time.Millisecond
			}
			j.playbackDue = now.Add(delay)
		}
	case "completed", "stopped":
		j.playbackDue = time.Time{}
		if r.reply.State == "completed" && (r.reply.SentPackets != r.reply.TotalPackets || r.reply.TotalPackets == 0) || r.reply.State == "stopped" && !j.stopWanted {
			if j.read != nil {
				_ = applicationVariable(lane, "read_result", "failure", false)
			}
			s.finishApplication(lane, "-ERR playback did not complete the requested media", now)
			return
		}
		if j.finishWanted {
			s.finishApplication(lane, j.finishResponse, now)
			return
		}
		if j.read != nil {
			if j.digitDeadline.IsZero() {
				j.digitDeadline = now.Add(j.read.timeout)
			}
		} else if r.reply.State == "completed" && r.reply.SentPackets == r.reply.TotalPackets && r.reply.TotalPackets > 0 {
			s.finishApplication(lane, "FILE PLAYED", now)
			return
		} else {
			s.finishApplication(lane, "-ERR playback stopped before completion", now)
			return
		}
	case "failed":
		s.failPlayback(lane, r.reply.Message, now)
		return
	default:
		j.playbackState = "failed"
		s.finishApplication(lane, "-ERR unknown playback state", now)
		return
	}
	s.scheduleApplication(lane)
}

// handleApplicationDigits 仅将媒体层已校验来源且完成去重的事件交给收号，不信任客户端自报按键。
func (s *Server) handleApplicationDigits(r applicationMediaResult) {
	e := s.applications
	if r.worker < 0 || r.worker >= len(e.workers) {
		return
	}
	cursor := &e.workers[r.worker]
	cursor.inflight = false
	if r.generation != cursor.generation {
		return
	}
	if r.err != nil || !r.reply.OK || r.reply.Type != "dtmf_events" {
		s.failWorkerReads(r.worker, r.generation, "DTMF media query failed")
		return
	}
	if r.reply.Overflow {
		s.failWorkerReads(r.worker, r.generation, "DTMF event journal overflow")
	}
	previous := cursor.after
	for _, digit := range r.reply.Events {
		if digit.Sequence <= previous || digit.Sequence > r.reply.NextSeq {
			s.failWorkerReads(r.worker, r.generation, "invalid DTMF event sequence")
			return
		}
		previous = digit.Sequence
		call := s.calls[digit.Session]
		if call == nil || call.Ended || call.Worker != r.worker || call.Generation != r.generation {
			continue
		}
		side := 0
		if digit.Leg == "b" {
			side = 1
		} else if digit.Leg != "a" {
			continue
		}
		if strings.EqualFold(call.compatVariables[side]["dtmf_type"], "info") {
			continue
		} // INFO 模式不得重复收集 RTP 按键。
		if digit.Kind == "incomplete" {
			s.disableApplicationDigits(call, side, "incomplete DTMF: "+digit.Reason)
			continue
		}
		if digit.Kind == "digit" && digit.Event == 16 && digit.Digit == "" {
			continue
		} // Hook flash 不属于此菜单的按键集合。
		if digit.Kind != "digit" || len(digit.Digit) != 1 || !strings.Contains("0123456789*#ABCD", digit.Digit) || digit.ClockRate == 0 {
			s.failWorkerReads(r.worker, r.generation, "invalid DTMF completion")
			return
		}
		s.applicationDTMF(call, side, digit.Digit, uint32(digit.DurationTicks), digit.ClockRate, "RTP")
	}
	if r.reply.NextSeq < cursor.after {
		s.failWorkerReads(r.worker, r.generation, "DTMF cursor moved backwards")
		return
	}
	cursor.after = r.reply.NextSeq
}

// applicationDTMF 是已验证 RTP/INFO 输入的统一入口；调用者必须先验证来源、对话和去重。
// Duration 对外使用 FreeSWITCH 的 8k 音频刻度，协议原始时钟不混同于音频采样率。
func (s *Server) applicationDTMF(call *Call, side int, digit string, durationTicks, clockRate uint32, source string) {
	if call == nil || call.Ended || side < 0 || side > 1 || clockRate == 0 || len(digit) != 1 || !strings.Contains("0123456789*#ABCD", digit) {
		return
	}
	uuid := call.compatUUIDs[side]
	if uuid == "" {
		return
	}
	if source == "RTP" && strings.EqualFold(call.compatVariables[side]["dtmf_type"], "info") {
		return
	}
	if source == "RTP" && s.applications != nil && s.applications.disabledDigits[call.ID][side] != "" {
		return
	}
	if s.compatEvents != nil {
		s.compatEvents("DTMF", map[string]string{"Unique-ID": uuid, "DTMF-Digit": digit, "DTMF-Duration": strconv.FormatUint(uint64(durationTicks)*8000/uint64(clockRate), 10), "DTMF-Source": source}, nil)
	}
	if s.applications != nil {
		lane := s.applications.lanes[uuid]
		if lane != nil && lane.active != nil {
			if lane.active.read != nil {
				s.collectApplicationDigit(lane, digit, time.Now())
			} else {
				s.terminatePlaybackDigit(lane, digit, time.Now())
			}
		}
	}
}

func (s *Server) collectApplicationDigit(lane *applicationLane, digit string, now time.Time) {
	j := lane.active
	if j.finishWanted {
		return
	}
	if strings.Contains(j.read.terminators, digit) {
		result := "success"
		if len(j.digits) < j.read.minimum {
			result = "failure"
		}
		s.finishRead(lane, result, digit, now)
		return
	}
	j.digits += digit
	j.digitDeadline = now.Add(j.read.digitTimeout)
	if len(j.digits) >= j.read.maximum {
		s.finishRead(lane, "success", "", now)
		return
	}
	if j.playbackID != 0 && (j.playbackState == "starting" || j.playbackState == "loading" || j.playbackState == "running") {
		j.stopWanted = true
		if !j.mediaInflight {
			if err := s.submitApplicationMedia(lane, "playback_stop"); err != nil {
				applicationPlaybackRetry(j, now)
			}
		}
	}
	s.scheduleApplication(lane)
}

func (s *Server) finishRead(lane *applicationLane, result, terminator string, now time.Time) {
	j := lane.active
	j.digitDeadline = time.Time{}
	for _, pair := range [][2]string{{"read_result", result}, {"read_terminator_used", terminator}, {j.read.variable, j.digits}} {
		if err := applicationVariable(lane, pair[0], pair[1], pair[1] == ""); err != nil {
			s.finishApplication(lane, "-ERR "+err.Error(), now)
			return
		}
	}
	s.finishApplication(lane, "_none_", now)
}
func (s *Server) failRead(lane *applicationLane, reason string) {
	_ = applicationVariable(lane, "read_result", "failure", false)
	s.finishApplication(lane, "-ERR "+reason, time.Now())
}
func (s *Server) failWorkerReads(worker int, generation uint64, reason string) {
	for _, call := range s.calls {
		if call.Worker == worker && call.Generation == generation && !call.Ended {
			for side := range 2 {
				if !strings.EqualFold(call.compatVariables[side]["dtmf_type"], "info") {
					s.disableApplicationDigits(call, side, reason)
				}
			}
		}
	}
}

// disableApplicationDigits 记录持久失败，下个 read 不能把已经丢失的按键伪装为静默。
func (s *Server) disableApplicationDigits(call *Call, side int, reason string) {
	state := s.applications.disabledDigits[call.ID]
	if state[side] == "" {
		state[side] = reason
		s.applications.disabledDigits[call.ID] = state
	}
	lane := s.applications.lanes[call.compatUUIDs[side]]
	if lane != nil && lane.active != nil && lane.active.read != nil {
		s.failRead(lane, reason)
	}
}

// cancelCallApplications 由真实挂机/故障回收调用；每个已受理任务恰好交付一次失败结束。
// 媒体会话释放同时停止该会话的播放，迟到确认按 Application-UUID 和播放编号丢弃。
func (s *Server) cancelCallApplications(call *Call, reason string) {
	if s.applications == nil || call == nil {
		return
	}
	delete(s.applications.disabledDigits, call.ID)
	for _, uuid := range call.compatUUIDs {
		lane := s.applications.lanes[uuid]
		if lane == nil {
			continue
		}
		if lane.active != nil {
			if lane.active.read != nil {
				_ = applicationVariable(lane, "read_result", "failure", false)
			}
			response := "-ERR channel ended: " + reason
			if lane.active.started && (lane.active.request.Application == "hangup" || lane.active.request.Application == "park" && reason == "normal_hangup") {
				response = "_none_"
			}
			j := lane.active
			if j.playbackID != 0 && (j.mediaInflight || j.playbackState == "starting" || j.playbackState == "loading" || j.playbackState == "running") {
				// release由呼叫回收提交；保留一个已有槽位，不新建协程或无界取消队列。
				j.endedPending, j.releaseConfirmed = true, !call.Allocated
				j.finishResponse = response
				j.hardDeadline, j.playbackDue, j.digitDeadline = time.Time{}, time.Time{}, time.Time{}
				s.scheduleApplication(lane)
				s.finishReleasedApplication(lane)
			} else {
				s.playbackLifecycleStop(lane, j, "done")
				s.completeApplication(lane, response)
			}
		}
		for _, j := range lane.queue {
			s.applicationEvent(lane, j, "CHANNEL_EXECUTE_COMPLETE", "-ERR channel ended before execution: "+reason)
			s.applications.jobs--
			s.applications.bytes -= j.bytes
		}
		lane.queue = nil
		if lane.active == nil {
			delete(s.applications.lanes, uuid)
		}
	}
}
