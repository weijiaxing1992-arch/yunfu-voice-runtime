# PCM 轮次队列与内部数据通道（1.12.0）

本文描述 Go Pool 与 Rust Worker 之间的内部 PCM 控制和数据合同。1.12.0 正式候选文档已纳入内部媒体与真实 SIP 外部入口的限定验收，实际部署版本须由 `GET /v1/docs` 核对，见 [1.12 验证记录](release-validation-v1.12.md)。外部业务应使用 [RVA1 入口](rva1-reference.md)，不能绕过通话授权直接使用媒体 session。

## 适用范围与调用边界

本批增加 Go Pool 到 Rust 媒体 Worker 的流式 PCM 下行能力：控制命令使用既有 JSONL 管道，音频批次使用该 Worker 独占的 Unix datagram socketpair。它是内部接口，不是 HTTP、ESL、WebSocket 或 FreeSWITCH 原生模块 ABI。面向同 UID 可信进程的外部入口由控制面另外提供 [RVA1](rva1-reference.md)，在实际 SIP ACK 后授权当前 A 腿 UUID。

本批没有接入 ASR/TTS 供应商、VAD 语音触发、LLM 或双轨录音。已存在的本地 SIP/IVR 仍有自己的 ACK 和应用执行条件；直接内部 Pool 调用只校验媒体会话、处理图、Worker 代次和队列状态，不证明 SIP 已完成 ACK。现有外部 Server SDK/RVA1 在此层之外检查真实 ACK、通话状态与不可变 Worker/会话/轮次句柄；原始 Pool/IPC 仍不能直接将 session ID 当作可写音频凭据。

路径为：

```text
内部 Go 调用者
  ├─ JSONL：begin / end / interrupt / status
  └─ 私有 Unix datagram：RSP1 PCM 批次 → RSR1 受理回执
                                       ↓
                        固定单写 turn 队列（最多 50 帧）
                                       ↓
                          8kHz PCM → G.711 编码
                                       ↓
                         本地 A 腿统一 RTP / RTCP
```

入站 A 腿仍独立接收、解码和消费；下行 PCM 不回灌到 RX，也不将 RX 反射成回声。原生 G.722/Opus SDK、24/48kHz AudioFrame 或 TTS 输出不意味着该通道接收这些采样率。

## 前置条件与身份

1. Worker 的实际 `ready` 声明 `pcm_turn_v1`；本地 G.711 分配同时保留 `processed_g711_v1`、`processed_g711_local_v1` 和明确 `processing_topology:"local"` 的原有校验。
2. 已分配的是 local A 腿、G.711、8000Hz、单声道、20ms。relay、bridge、未知 session 和尚未 begin 的轮次不能接受 PCM。音频按该腿真实协商的 PCMU/PCMA 和 PT 编码，不固定假定所有呼叫均为 PT0 或 PT8。
3. Go `Request.Generation` 必须显式匹配实际 Worker 代次；它只供 Go 校验，不写入 JSON。`PushPCM` 的 generation 参数也必须匹配当前进程。
4. socketpair 在该次子进程启动时建立，子端固定继承 FD3，环境变量为 `RUSTSWITCH_PCM_FD=3`。Rust 检查描述符类型/Unix 域/已连接对端并设为非阻塞。它没有文件系统路径或对外监听端口。
5. **原始 IPC 调用者不能在同一 Worker 代次释放后复用 session ID。** 当前 Go 主服务单调生成 session ID，Worker 重启使用新 FD 与代次。RSP1 不包含单独的会话授权令牌；如果自行复用 `(session, turn_id)`，旧数据报与新会话可能无法区分，不能声称该原始调用方式已受跨会话防重放保护。
6. 同一 session 只保留当前 turn。turn_id 为非零 u64，新 turn_id 必须严格增加；不能在同一 session 重新使用旧编号。同 ID 同配置的 begin 幂等返回当前状态，即使已经完成、停止或失败，也不复活。

`SupportsPCMTurn()` 表示同代次握手能力，不保证此时数据 lane 仍可写，也不是某个 SIP 呼叫可播放的许可。lane 发生故障后，JSON status/interrupt/release 仍可用；不能把 capability=true 显示为端到端实时会话已就绪。

## 音频与队列限制

| 项目 | 当前合同 |
| --- | --- |
| PCM | 有符号 16 位，单声道，8000Hz；在线字节序固定 little-endian |
| 每帧 | 20ms，160 样本，320 字节 |
| 一批 RSP1 | 1–5 完整帧，即 160/320/480/640/800 样本，最多 100ms |
| `buffer_ms` | 20–1000，20 的整数倍；最多 50 帧、8000 样本、16000 字节 |
| `prebuffer_ms` | 20 至 buffer_ms，20 的整数倍 |
| 偏移 | `offset_samples == 当前 accepted_samples`，单位是样本，不是字节、RTP 包或时间戳 |
| 队列写入 | 单 Worker 线程独占；先校验整批再复制，满队列/旧轮次/错误偏移整批拒绝 |
| 缺数据 | 不造静音、不重播已发送帧，不将未到数据标为用户沉默 |
| 旧播放互斥 | Rust 拒绝正在 running/loading 的 tone/WAV；外部 SDK 另检查活动及排队的 WAV/tone TX 任务，反向启动播放也校验 PCM 所有权。需要显式结束或停止 |
| DTMF | 已协商的本地主动 telephone-event 可以使用同一 TX 身份，音频与事件分别保留时间语义 |

begin 使用固定数组存储，首次启用可一次性分配队列对象；稳态 Rust 队列 push/front/commit 不创建逐帧堆对象。Go 每批仍有有界样本副本、任务和结果通道；不能把队列分配实验表述为整个产品零分配或零复制。

## JSONL 控制接口

以下是 Go→Rust 子进程的原始控制示例，不可发送给 HTTP 或 ESL。每条 JSON 以换行结束，`id` 用于控制请求关联。示例编号不是生产端口或现网会话。

### pcm_turn_begin

```json
{"id":101,"op":"pcm_turn_begin","session":42,"turn_id":100,"buffer_ms":200,"prebuffer_ms":40}
```

校验 turn_id、配置和互斥条件完成后才替换旧轮次。更大 ID 的合法 begin 原子丢弃旧轮次待发送内容，当前计数从零开始；旧 ID 请求不能继续写入。同 ID 同配置不改变原队列、曲线、开始时刻或已发送量，同 ID 不同配置拒绝。会话只保留当前轮次，没有持久化的历史 turn 查询接口。

初始受理回执示例：

```json
{"id":101,"ok":true,"type":"pcm_turn_state","session":42,"turn_id":100,"state":"buffering","accepted_samples":0,"sent_samples":0,"discarded_samples":0,"queued_samples":0,"queued_bytes":0,"queued_ms":0,"oldest_age_ms":0,"buffer_ms":200,"prebuffer_ms":40,"error":""}
```

达到 prebuffer 后建立首帧发送期限；首帧需与本地图其他已发送音频位置对齐，不能覆盖上一帧的采样区间。begin 成功本身不证明有 PCM 入队或产生 RTP。

### pcm_turn_end

```json
{"id":102,"op":"pcm_turn_end","session":42,"turn_id":100,"final_samples":320}
```

`final_samples` 必须等于实际已受理样本数，且是 160 的整数倍；零值必须明确保留在 JSON 中。正确 end 关闭该轮 push，并进入 draining。未达到 prebuffer 的短轮次也允许排空。空轮次 end 可立即 completed；非空轮次必须在最后一帧 UDP 完整成功返回后至少 20ms 才 completed。

end 的成功回执可能是 draining，也可能在实际尾音区间已经结束时为 completed；不得以 end 返回 ok 代替排空完成。重复相同 final_samples 的 end 不重启音频。已停止或失败轮次不重新打开。

### pcm_turn_interrupt

```json
{"id":103,"op":"pcm_turn_interrupt","session":42,"turn_id":100,"fade_ms":40}
```

fade_ms 只允许 0、20、40，省略等同 0。中断立即禁止旧 push：

- 0：取消所有未发送帧，进入 stopped；已被系统调用接受的网络数据无法撤回。
- 20/40：最多保留队头 1/2 个尚未发送帧，取消其余队尾。现有帧不足时只处理已经存在的帧，不等待新数据，不复制最后已发送帧。
- 淡出覆盖实际保留的 N=160 或 320 个样本。第 i 个样本乘 `(N-1-i)/(N-1)`，有符号整数向零截断；首样本增益 1，最后样本精确为 0。
- 已有计划时刻保持不变，首次 fade 命令即使在 due+2ms 到达也不重锚同轮样本时间。尚未建立首帧期限的 buffering 才建立淡出首时刻。
- stopping 中重复非零 interrupt（包含另一非零 fade_ms）保持原曲线、包数和期限；不会重新淡出。fade_ms=0 可以升级为立即清空。
- 先检查该轮真实供音/发送期限；已经失败或到期的旧帧不能通过 interrupt 重新定时成为合法音频。存在超时竞争时可保留 failed 原因，不能将失败伪装成完整播放。

淡出帧实际发送后仍等待最后 20ms 区间结束才 stopped。该命令是内部轮次控制，**不会自动把现有 `uuid_break`、read 终止符或 VAD 映射到此 turn**。

### pcm_turn_status

```json
{"id":104,"op":"pcm_turn_status","session":42,"turn_id":100}
```

只返回匹配当前轮次的状态；旧 turn 不用新轮次状态冒充成功。查询可以确认尾帧已经结束或供音已经超时，不代替媒体定时器执行发送。

| 字段 | 单位与含义 |
| --- | --- |
| `turn_id` | 当前非零轮次编号，u64 |
| `state` | buffering、playing、draining、stopping、completed、stopped、failed；idle 仅内部无状态/二进制空快照语义 |
| `accepted_samples` | 已成功原子入队的样本累计；不是已发送或远端听到 |
| `sent_samples` | UDP 完整成功返回的 PCM 样本累计，每帧增加 160；不包含 DTMF |
| `discarded_samples` | 已受理但因取消/失败丢弃的完整帧样本数；已发出的音频不计为丢弃 |
| `queued_samples` | 当前待发样本数，包含还未达到 prebuffer 的帧 |
| `queued_bytes` | queued_samples × 2 |
| `queued_ms` | queued_samples ÷ 8 |
| `oldest_age_ms` | 当前最老待发帧从入队至快照的单调年龄；空队列明确为 0 |
| `buffer_ms` / `prebuffer_ms` | 当前轮次冻结的 u16 配置 |
| `error` | 未失败时为空；failed 时为稳定原因码 |

所有状态始终满足 `accepted_samples = sent_samples + queued_samples + discarded_samples`。completed/stopped/failed 的 queued_samples 为零。Go 对 PCM 回执要求字段真实存在且非 null，并检查整帧、容量、守恒和身份；缺字段不能默认为“全零成功”。u64 是精确整数，任何未来 JavaScript 暴露层还需避免超过安全整数范围后的隐式舍入。

JSON 错误沿用 `{"id":104,"ok":false,"type":"error","message":"..."}`。错误正文不是 FreeSWITCH `-ERR` 格式；成功回执不能省略字段。Go 调用例仅描述内部 API：

```go
// generation 必须来自本次通话绑定的实际媒体 Worker 代次。
request := media.Request{Op: "pcm_turn_begin", Generation: generation,
    Session: session, TurnID: turnID, BufferMS: 200, PrebufferMS: 40}
_, err := pool.Call(ctx, worker, request)
if err != nil {
    return err
}
// Pool已经校验完整回执；成功后才提交独立音频批次。
```

## RSP1：PCM 数据批次

`Pool.PushPCM(ctx, worker, generation, session, turnID, offset, samples []int16)` 将批次复制到有界任务，并使用独立 lane。整数头字段全部为 little-endian，每个数据报必须完整且独立，长度精确等于 `44 + count×2`。

| 偏移 | 字节数 | 字段 | 要求 |
| --- | --- | --- | --- |
| 0 | 4 | magic | ASCII `RSP1` |
| 4 | 4 | reserved | 全零 |
| 8 | 8 | request_id | 非零，当前数据 lane 单调关联号；不同于 JSON 控制 id |
| 16 | 8 | session | 非零已分配媒体会话 |
| 24 | 8 | turn_id | 当前已 begin 的轮次 |
| 32 | 8 | offset_samples | 必须等于当前 accepted_samples |
| 40 | 2 | count | 160–800，160 的整数倍 |
| 42 | 2 | reserved | 全零 |
| 44 | count×2 | PCM | S16LE、8kHz、mono |

不支持空批、半帧、超过 5 帧、其他采样率元数据、压缩音频、WAV 头或 JSON/base64 PCM。数据报不携带 sample_rate/channels，调用者负责提交真正的8k单声道样本；字节长度校验不能识别被误标的24/48k音频，更不会自动重采样。过长数据报用额外一个字节检测截断，不会把合法前缀当完整请求。重复 request_id 不是去重协议；重复 offset 在第一次已入队后会明确失败，生产者不能盲目重发未知结果。

## RSR1：批次受理回执

固定 96 字节。头字段仍为 little-endian；没有状态可提供时 current_turn_id 和状态计数为零，state=idle，不代表实际通话处于空闲。

| 偏移 | 字节数 | 字段 |
| --- | --- | --- |
| 0 | 4 | magic=`RSR1` |
| 4 | 2 | code |
| 6 | 2 | reserved=0 |
| 8 | 8 | 请求 request_id |
| 16 | 8 | 请求 session |
| 24 | 8 | 请求 turn_id |
| 32 | 8 | current_turn_id |
| 40 | 8 | accepted_samples |
| 48 | 8 | sent_samples |
| 56 | 8 | queued_samples |
| 64 | 8 | discarded_samples |
| 72 | 8 | oldest_age_ms |
| 80 | 1 | state 枚举 |
| 81 | 15 | reserved=0 |

state 映射：0 idle、1 buffering、2 playing、3 draining、4 stopping、5 completed、6 stopped、7 failed。

| code | 名称 | 含义 |
| --- | --- | --- |
| 0 | 空错误名 | 整批已入队；accepted 必须等于 offset+count，current_turn_id 必须匹配请求 |
| 1 | invalid_packet | 长度、保留位、身份、帧长或样本累积溢出不合法 |
| 2 | unknown_session | 没有该媒体会话 |
| 3 | unsupported | 不是 local 处理图 |
| 4 | unknown_turn | 当前会话尚未建立 PCM turn |
| 5 | stale_turn | 请求轮次与当前轮次不符 |
| 6 | closed | 当前轮次已经关闭写入口 |
| 7 | offset_mismatch | 偏移与当前受理样本数不符 |
| 8 | queue_full | 剩余容量无法完整容纳本批，零入队 |
| 9 | unavailable | Go 解码器预留的已知错误名；当前 Rust push 分派未发送此 code，lane 失联按传输错误处理 |

code 为 0 只证明该批受理。RSR1 不携带完整 error 原因文本或 buffer 配置；failed 的精确原因通过 JSON status 查询。旧 turn 的拒绝回执允许携带真实 current_turn_id，但不能把请求 ID 和当前 ID 混写。

## 背压、取消与故障

Go 每 Worker 一个数据协程，最多 64 个排队批次和 1 个在途请求；不按通话创建数据协程。64 批上限不等于 Rust 每轮 50 帧队列：一个是生产任务队列，另一个是每通话的媒体缓冲，两层均独立校验。Go 只用短锁校验身份/入队和复制至多 1600 字节，不持锁等待 socket。

每次 Go 数据 exchange 的写出和回执共用一秒期限。Rust 每轮最多收 32 批，最多保留一份已执行请求的待回回执；未回出前暂停该 lane 读取。Rust回复发送遇到 `EAGAIN/EWOULDBLOCK`、`EINTR` 与 macOS Unix datagram 缓冲满的 `ENOBUFS` 都保留同一待回回复，不重新执行已受理的 PCM，也不重置一秒总期限。等待回复不阻塞媒体线程，待回复一秒仍未发出则关闭该 lane。已建立 RTP 和 JSON 控制的生命周期独立，尚受理音频不会凭空从统计中消失。

| 情况 | 调用方应如何解释 |
| --- | --- |
| 入队前 ctx 已取消 | 未提交，返回 ctx 错误 |
| Go 任务仍排队时成功取消 | 确定尚未写给 Rust，不自动重试 |
| 在途 ctx 取消、写/读超时、短回执或身份/字段异常 | `ErrPCMOutcomeUnknown`；可能已受理，必须查询当前轮次，不能假定零执行后盲目重发 |
| 确定的 RSR1 非零 code | `PCMRejection`，本批零入队；修正身份/偏移或等待容量后再做有依据的下一次提交 |
| lane 已失效或代次不符 | `ErrPCMUnavailable`；故障 lane 不用于新 push |
| 数据 lane 丢失 | JSON status/interrupt/release 仍可用于查明状态与停止；数据通道没有本轮自动重连 |
| JSON 控制失联 | 沿用原 Worker 失联退出策略，不能据数据 lane 独立性承诺整进程永不影响存量呼叫 |
| release / Worker 退出 | PCM 定时器、队列和相关资源释放，旧会话不能继续产生新发送 |

首次等待可播数据最多 2 秒；在此期间不断发送不够 prebuffer 的片段不能重置期限。进入 playing 后，队列空时无 20ms 空转定时器：从最后实际发送成功后的 20ms 区间结束再等 100ms，无后续数据则失败。真正供音缺口恢复允许新的计划起点；有在队帧时追加数据不能延后队头来掩盖迟到。

Worker 每次调度每个会话最多一帧，一轮最多处理 128 个 PCM 到期项。相对计划期限迟到至少 20ms 时取消剩余音频，不集中补发旧帧。记录中包括 `pcm_first_audio_timeout`、`pcm_audio_gap_timeout`、`pcm_schedule_late`、`pcm_encode_failed`、`pcm_udp_send_failed`、`pcm_send_return_late`；均须按实际失败分支解释，不给供应商连接贴上已接入标签。

## 时间与完成语义

连续音频的计划时刻严格每帧推进 20ms，对应 8kHz RTP 时钟的 160 ticks，不能每帧取实际发送时刻重锚。正常供音恢复与新轮次可有明确正向间隙；同轮淡出不是新的样本时间线。PCM、tone/WAV 与 DTMF 共用 local Graph 的 SSRC 和包序；DTMF 的固定事件起点不随其结束重传变化。并行 DTMF 时，单看音频包的序号可能跳过事件包，应在完整 RTP 流中核对序号连续性。

成功发送后使用系统调用返回时刻记录 sent_samples 和最后一帧结束等待。`completed` 证明已受理样本完成本地发送与尾帧 20ms 时间区间，不证明终端已听到、网络零丢包、无抖动或 ASR/TTS 任务已完成。中断不能撤回此前交给内核、网络或终端缓冲的音频。

本轮 `processed_send_deadline_misses` 的 PCM 路径扩展为“发送期限违例”：包括发送前拒发，以及 UDP 已完整返回成功但返回时已超过期限。后一情况如实计入 sent_samples，失败并取消剩余队列，不能再把该帧计成未发送。原双腿验收门槛不因此改变。

`processed_output_lateness_ns_max` 和 8 桶记录最终发送前核对时刻，或编码前已确定超期的核对时刻；不是 UDP 返回时延。因此 `pcm_send_return_late` 可能出现 miss>0 但对应发送前桶小于 20ms。不能将 deadline_misses 与输入缺包、expired、discarded 直接相加当作实际网络损失。`processed_encoded_frames` 计编码动作，也不等于成功提交 UDP。

## 已验证范围与来源

内部候选 `work/voice-runtime-m2-pcm/pcm-03/receipt.json` 及独立 review 确认两种 UDP 模式共 **42 方法与 16 子例通过**，52 次 Release 后新鲜统计归零且端口可重新绑定，218 件原始产物哈希核验通过。409 个源文件与该次构建一致。媒体二进制与外部入口 build-02 使用同一个 SHA；这不使内部 42 项自动覆盖新的 Go/SIP 授权入口。

`faults-02` 与 `faults-connected-02` 各 3 方法通过，其中每种模式有 2 个真实 socket/媒体方法及 1 个 FD 类型方法（3 个子例）。回执黑洞期间 50 帧/8000 样本正确发送、JSON 状态在 200ms 内回复；超过 1 秒后 lane 隔离，status/interrupt/release 仍可用。短暂慢读恢复后 18 批各受理一次。这里的观测来自限定故障夹具，不能作任意负载延迟保证。

同批 Rust 为 147 单元与 1 集成通过，Clippy `-D warnings` 退出 0。独立队列实验只证明固定队列在测量范围内的稳态分配行为，不是整个 Worker 零分配或容量证明。原始 `faults-01` 的 ENOBUFS 失败、`pcm-02` 的 RTCP 输入夹具时序失败均保留，修复与完整证据口径见 [发布验证](release-validation-v1.12.md)。上述 `work/...` 是研发工作区原始证据路径，不是本接口文档下载包内资源。

外部 [RVA1](rva1-reference.md) 已另完成真实 SIP ACK、token、下行 PCM 和挂机回收用例；ASR/TTS 供应商、VAD 触发、录音、其他 Codec 实时图仍未完成。内部通过不授予 FreeSWITCH 原版对应模块绿色，也不改变双腿 500 路及后续 5000/10000 路尚未通过的容量结论。
