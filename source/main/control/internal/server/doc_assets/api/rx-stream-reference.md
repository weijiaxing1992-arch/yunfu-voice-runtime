# 本地通话上行音频：RXS2 与 Go RX SDK

本文属于 1.13 文档包，包含已验证的内部 RX 收音合同。实际安装版本须通过 `GET /v1/docs` 及部署回执核对，不能从文档包版本推定已安装或改变兼容认证状态。测试来源为冻结的 Rust 媒体候选 08、Go RXS2 SDK 与 Server SDK 03；实际通过范围见末尾验收记录。它为 Voice Agent 提供可追溯、可撤销的本地收音基础，尚无外部 ASR HTTP/WebSocket 接口、供应商适配器、VAD 或完整实时对话验收。

这是 RustSwitch 内部接口，不是 FreeSWITCH 同名命令、ESL 报文、媒体 bug API 或 C/C++ ABI 的兼容实现。现有 SIP、拨号计划与下行 PCM 的适配范围仍以各自文档为准。

## 1. 支持范围与通道

当前只接受实际分配为 `local`、处理版本 `1` 的 A 腿 G.711 音频：PCMA 或 PCMU，音频采样率和 RTP 时钟均为 8000 Hz，单声道、20 ms。导出的 PCM 保持原生 8 kHz S16LE，每帧 160 样本；不自动转成 16 kHz。桥接双腿、B 腿、纯转发、G.722、Opus 和其他 Codec 的实时收音不在此合同内。

控制使用现有 JSON IPC。音频使用可选的 `RUSTSWITCH_RX_FD=4`：父进程创建已连接的 `AF_UNIX/SOCK_DGRAM` socketpair，Rust 写入，Go 读取。FD3 下行 PCM 保持独立。每个媒体 Worker 进程代次使用新的 socketpair；Go 将通道绑定 Worker 与 Generation，旧代数据不能进入新代次。Generation 不增加到 RXS2 头或 JSON 请求中。

`ready.capabilities` 必须包含 `rx_g711_local_v2`，并同时具备 `processed_g711_v1`、`processed_g711_local_v1`。JSON `protocol_version` 仍为 `1`；RXS2 的魔数、能力名和头长度共同标识本次二进制合同。旧 RXS1/v1 不被静默接受。未报告能力的旧 Worker 不具备 RX 资格。

该通道要求同一主机及相同单调时钟命名空间，不能经网络转发为外部音频协议。

## 2. JSON 控制与状态

三个请求均要求 `id`、`op`、`session`、`subscription_id`。`id` 是请求关联号，取无符号 64 位整数；另外两个 ID 必须非零。数值上限为 `18446744073709551615`，客户端不能用精度不足的浮点数保存 ID。

```json
{"id":1,"op":"rx_subscribe","session":73,"subscription_id":1}
```

```json
{"id":2,"op":"rx_status","session":73,"subscription_id":1}
```

```json
{"id":3,"op":"rx_unsubscribe","session":73,"subscription_id":1}
```

| 操作 | 行为 |
|---|---|
| `rx_subscribe` | 每个会话最多一个活动订阅。Rust 同 ID 重试返回原订阅的当前快照，不复活终态、不重新校准时钟；更大 ID 只在旧订阅已停止或失败后创建 |
| `rx_status` | 查询同一会话及订阅，推进应过期记录的清理并返回实际 Rust 状态；未知会话、无订阅或 ID 不匹配均拒绝 |
| `rx_unsubscribe` | 停止观察并撤销待发音频，返回实际 `stopped` 或已有 `failed`；不等待 Go 消费或终止数据报送达 |

上述是媒体 IPC 语义。Server SDK 的同 ID Subscribe 使用缓存，区别见第 7 节。

成功回执示例：

```json
{"id":2,"ok":true,"type":"rx_state","session":73,"subscription_id":1,"state":"active","produced_events":3,"submitted_events":2,"dropped_events":0,"queued_events":1,"produced_samples":320,"submitted_samples":160,"dropped_samples":0,"oldest_age_ms":7,"error":""}
```

`rx_state` 的以下 15 个字段全部必填，不接受 `null`：

| 字段 | 类型与范围 | 含义 |
|---|---|---|
| `id` | u64 | 对应请求 ID |
| `ok` | 固定 `true` | 成功取得状态，不代表音频已送达应用 |
| `type` | 固定 `rx_state` | 回执类型 |
| `session` | 非零 u64 | 冻结的媒体会话 |
| `subscription_id` | 非零 u64 | 冻结的订阅 |
| `state` | `active` / `stopped` / `failed` | Rust 订阅状态 |
| `produced_events` | u64 | 已产生的原始观察记录数 |
| `submitted_events` | u64 | 原始观察成功交给 Unix socket 的数量 |
| `dropped_events` | u64 | Rust 已丢弃的原始观察数量 |
| `queued_events` | 整数 0..8 | Rust 导出队列中的原始观察数量 |
| `produced_samples` | u64，160 的倍数 | 已产生 PCM 样本数，包括历史 PLC |
| `submitted_samples` | u64，160 的倍数 | 交给 Unix socket 的 PCM 样本数 |
| `dropped_samples` | u64，160 的倍数 | Rust 已丢弃 PCM 样本数 |
| `oldest_age_ms` | u64 | 当前 Rust 队头观察年龄，空队列为 0 |
| `error` | 字符串枚举 | 非失败状态为空串；失败原因见下表 |

计数按原始观察对账，CN、DTMF 状态及边界没有 PCM 样本。导出的 Gap、End、ExportFailed 摘要不增加上述原始观察计数。

```text
produced_events = submitted_events + dropped_events + queued_events
queued_samples = produced_samples - submitted_samples - dropped_samples
0 <= queued_samples <= queued_events * 160
produced_samples / 160 <= produced_events
submitted_samples / 160 <= submitted_events
dropped_samples / 160 <= dropped_events
```

`queued_samples` 仅是推导值，不是 RX JSON 字段。各项样本计数及推导值必须为 160 的倍数，运算须检查溢出与下溢。终态 `queued_events=0`；空队列 `oldest_age_ms=0`。同 ID subscribe 的快照不主动清过期队列，因此 Schema 不把年龄强行限制在 100 以下。

[媒体 IPC Schema](media-ipc.schema.json) 校验字段、范围和状态条件，不能执行上述跨字段加减或验证请求/响应关联。Go 与 Rust 负责运行时算术、身份、状态和连续性检查。请求及回执沿用 Serde 忽略未知字段的行为，RX Schema 也允许附加字段；这不改变已有 `WorkerConfig` 拒绝未知字段的规则。

| `failed` 的 `error` | 含义 |
|---|---|
| `invalid_observation` | 观察记录无法按合同编码 |
| `sequence_exhausted` | 原始观察序号无法继续递增 |
| `sample_count_exhausted` | PCM 样本计数溢出 |
| `gap_metadata_discontinuity` | 无法合并为保真的连续丢弃摘要 |
| `rx_lane_unavailable` | 上行通道失效或持续无法前进 |
| `rx_observation_failed` | Graph 观察器显式失败 |
| `rx_clock_out_of_range` | 观察时钟换算或固定期限越界 |

`active`、`stopped` 的 `error` 为 `""`，第一个实际失败原因保留。参数错误、无会话、不支持的拓扑、旧 ID 或时钟初始校准失败使用普通 `type=error` 回执，不能伪造为成功的 `rx_state`。例如：

```json
{"id":1,"ok":false,"type":"error","message":"RX requires local topology"}
```

`message` 是诊断文本，不是稳定错误码。明确拒绝、控制超时和结果未知须区分处理。

## 3. Worker 指标与应用侧计数

| `stats` 字段 | 口径 |
|---|---|
| `rx_active_subscriptions` | 当前仍接受观察的订阅数量 |
| `rx_queued_events` | 当前所有 Rust 导出队列中的原始观察数，不含 Gap/终止摘要 |
| `rx_observation_storage_bytes` | Graph 观察器分配的存储，不含 Rust 导出队列、OS socket 或 Go SDK 队列 |
| `rx_observation_failed` | 当前 Graph 观察器处于失败状态的数量，不是累计通道错误数；通道隔离后观察器已关闭时可为 0 |

四项为非负 u64，当前候选始终输出。Schema 保留对旧 Worker 的兼容，允许旧 `stats` 缺少它们；缺失表示不可用，不能当成 0 或 RX 已通过。

Go `RXSnapshot` 另记 `ReceivedRecords/DeliveredRecords/DroppedRecords/ExportGapEvents`、对应样本数、`FreshnessFailures/ClockFailures/QueuedRecords/Error`。这些记录计数包含 Gap 和终态，不能与 Rust 原始观察口径相加。Rust `submitted` 只证明交给内核；Go `delivered` 只证明 SDK 检查点交付，二者都不等于 ASR 已识别。Server 最后拒绝的帧也可能已计入底层 SDK delivered，必须分别解释。

## 4. RXS2 二进制格式

RXS2 不是 JSON Schema 的额外请求分支。每个数据报由固定 168 字节头加正文组成，正文最多 480 字节，总长最多 648。所有多字节整数使用小端；不得拼接数据报、截断正文或附加尾部。所有记录仅来自 A/RX。

| 偏移 | 类型 | 字段及约束 |
|---|---|---|
| 0 | 4 字节 | 魔数 `RXS2` |
| 4 | u8 | `kind`，见下表 |
| 5 | u8 | 保留，必须 0 |
| 6 | u16 | `flags`，仅低 10 位有效 |
| 8 | u64 | `session`，非零 |
| 16 | u64 | `subscription_id`，非零 |
| 24 | u64 | `event_seq`，原始观察从 1 递增；摘要规则见下文 |
| 32 | u64 | `source_generation` |
| 40 | u64 | `source_segment` |
| 48 | u64 | 展开后的 RTP 时间戳，未声明有效则 0 |
| 56 | u64 | 展开后的 RTP 包序，未声明有效则 0 |
| 64 | u64 | `media_time_ns`，未声明有效则 0；不是 UTC |
| 72 | u32 | SSRC，未声明有效则 0 |
| 76 | u32 | 音频采样率，固定 8000 |
| 80 | u32 | RTP 时钟，固定 8000 |
| 84 | u16 | `body_bytes`，实际正文长度 |
| 86 | u16 | `sample_count`，仅解码与 PLC 为 160，其余 0 |
| 88 | u64 | `gap_first_seq`，仅 ExportGap 使用 |
| 96 | u64 | `gap_last_seq`，仅 ExportGap 使用 |
| 104 | u32 | `gap_decoded_events`，仅 ExportGap 使用 |
| 108 | u32 | `gap_plc_events`，仅 ExportGap 使用 |
| 112 | u64 | `known_duration_ticks`，只记录已知媒体区间 |
| 120 | u32 | `export_age_ms`，Rust 导出队列年龄，非端到端延迟 |
| 124 | u16 | `reason`，观察原因或 Gap 原因位 |
| 126 | u16 | 保留，必须 0 |
| 128 | u64 | `discarded_packets`，确认源边界丢弃的编码包数 |
| 136 | u32 | `observation_age_ms`，Graph 观察至当前提交的年龄 |
| 140 | u32 | `arrival_age_ms`，仅 flags bit8 有效 |
| 144 | u32 | `deadline_lateness_ms`，仅 flags bit9 有效 |
| 148 | u8 | `boundary`，0..6 |
| 149 | u8 | `cn_applied`，0 或 1 |
| 150 | u16 | `clock_domain`，必须匹配接收平台 |
| 152 | u64 | `observation_lower_bound_ns`，原始观察时刻的同机单调时钟下界 |
| 160 | u64 | `expires_at_ns`，固定到期时刻 |
| 168 | 0..480 字节 | 正文，按 kind 解释 |

| kind | 名称 | 正文与语义 |
|---|---|---|
| 1 | Decoded | 严格 320 字节 S16LE、160 样本，实际 G.711 解码 |
| 2 | HistoryPlc | 同格式；有限历史衰减补偿，不能当成新收到的用户语音 |
| 3 | Missing | 无正文，明确缺失 |
| 4 | ComfortNoise | 1..480 字节真实 CN SID，无 PCM |
| 5 | LocalExpired | 无正文，本地音频已错过处理期限 |
| 6 | AuxiliaryExpired | 无正文，辅助状态过期；不自动等同 20 ms 音频丢失 |
| 7 | SourceBoundary | 无正文，实际来源边界 |
| 8 | InactiveSuspended | 无正文，观察已进入非活动挂起 |
| 9 | ObservationFailed | 无正文，观察器显式失败 |
| 10 | ExportGap | 无正文，连续丢弃范围摘要 |
| 11 | End | 无正文，正常终止摘要 |
| 12 | ExportFailed | 无正文，上行失败摘要 |
| 13 | Auxiliary | 无正文，DTMF 辅助状态，不生成音频 |

最后一帧 PLC 可以带 `suspended_after`，不能重复这帧 PCM 来补一个状态通知。CN、PLC、真实缺包和 DTMF 保持不同类型，VAD/ASR 适配器不能将它们一律视为用户沉默。此处导出 DTMF 状态不扩大既有 telephone-event 按键 API 的合同。

`flags` 各位分别为：bit0 SSRC 有效；bit1 RTP 时间戳有效；bit2 RTP 包序有效；bit3 媒体时间有效；bit4 有来源边界；bit5 CN 辅助转发已过期；bit6 `suspended_after`；bit7 RTP marker；bit8 arrival 已知；bit9 deadline 已知。其余位必须为 0，对应元数据未声明有效则数值必须为 0。

`boundary`：0 无；1 初始来源；2 SSRC 变化；3 包序重启；4 时间戳重启；5 新段；6 已知源 RTCP BYE 结束。非零值须与 bit4 一致；除新段 5 外，非零边界必须使用 SourceBoundary。边界 6 不声明 RTP 包序有效。`discarded_packets` 只在 SourceBoundary 非零；`cn_applied=1` 只允许 ComfortNoise 或 AuxiliaryExpired。

普通观察 `reason` 为：0 无；1 MissingPacket；2 AudioDeadline；3 AuxiliaryDeadline；4 StaleComfortNoise；5 InvalidComfortNoise；6 ToneTimeout；7 DecoderFailure；8 ObservationOverflow；9 ObservationSequenceExhausted；10 JitterBufferLimit；11 LateArrival。End 固定 0，ExportFailed 固定 1，具体失败仍由 JSON 查询。

ExportGap 的 `reason` 使用非零位掩码：1 队列满、2 年龄超时、4 停止/撤销，可组合。必须满足 `gap_first_seq>=1`、`gap_last_seq>=gap_first_seq`、`event_seq=gap_last_seq`，解码数加 PLC 数不超过该范围记录数。接收端按前一个序号校验缺口连续性，不能允许无摘要跳序或重复交付。

Gap 可以跨来源段，因此其 flags、来源身份、RTP/媒体位置、时长、丢弃编码包数、观察/arrival/deadline 年龄、boundary、cn_applied 均为 0，不能借摘要冒充某一具体来源。End、ExportFailed 的 `event_seq` 是最终产生序号，允许 0；它们同样没有上述来源信息，也没有 Gap 字段。其他 kind 的四个 Gap 字段均为 0。Go 严格拒绝未知 kind、保留位、错误长度、非法组合或错订阅身份。

## 5. 100 ms 固定期限

`clock_domain=1` 表示 Linux `CLOCK_MONOTONIC`，`2` 表示 macOS `CLOCK_UPTIME_RAW`。Rust 新订阅以共享时钟前采样、Instant 锚点、共享时钟后采样建立映射，校准跨度不得超过 1 ms；失败仅拒绝本次订阅。已有 ID 不重新锚定。

普通观察的 `observation_lower_bound_ns` 非零，且不得溢出；`expires_at_ns` 必须精确等于它加 `100000000` ns。期限从原始 Graph 观察换算一次，Rust 排队、重试、编码、OS 停留、Go 接收和交付都不续期。ExportGap、End、ExportFailed 不携带可恢复音频，两个时间字段均为 0，但时钟域仍须匹配。

Go 在接收与 Read 时复核公共单调时钟，Server Read 再调用该帧的 `CheckFresh()`。下界来自未来、时钟域错误、公共时钟读取失败、期限非法或已经到期都明确失败；旧 PCM 不作为正常语音交付。Go Read 另限制本地队列停留不足 100 ms，不能靠摘要无音频期限绕过慢消费边界。

这些是明确检查点，不是函数返回或业务使用的原子保证。调用者在真正送给模型或写外部连接前仍须复核 `frame.CheckFresh()`，并遵守通话撤权；一次成功 Read 不能覆盖之后任意暂停。预算从 Graph 观察开始，不代表声学采集到 ASR 的总延迟，也不覆盖系统休眠的墙钟时间。Linux 父子进程必须在相同 time namespace。

## 6. 有界队列、失败与回收

| 层 | 实际边界 |
|---|---|
| Graph 观察器 | 每订阅 8 个普通槽及独立无 PCM 失败终态；只在订阅期间分配，每个实际 local A 方向观察只取一次 |
| Rust 导出 | 每订阅 8 个记录槽，加独立 Gap 和终止摘要；总订阅数受 Worker `max_calls` 限制 |
| Go SDK | 每订阅 8 个槽，每 Worker 固定一个接收循环；慢消费者局部失败，清除未读数据，保留清理句柄 |
| Server 控制 | 全服务固定 4 个执行者，64 个授权请求槽、64 个在途预留及 64 个 job/result 槽 |

以上是不同层的缓冲，不能合称整条通道“只有 8 槽”；OS socket 另有内核缓冲。Rust 队列满或年龄达到 100 ms 时丢最旧观察，合并连续 event_seq、decoded/PLC 数和原因位，下次发送先提交 Gap，再提交新鲜记录。不能保真合并或计数耗尽时显式失败，不无标记地继续。Go 局部慢读者的失败不阻塞其他订阅。

每次媒体事件循环 `flush_rx` 最多 128 次发送尝试，可以对同一订阅尝试多次；这不是 128 会话的容量或公平吞吐保证。整条通道暂时不可写时退避 1 ms；连续至少 1 秒无成功写入进展，在下一次发送检查时隔离通道并失败相关活动订阅。它不是独立的 1 秒定时器，也不承诺静止无事件时恰好 1 秒触发。

SIP、TX、主动 DTMF 和 Release 不等待 RX 消费。媒体 Release 最多尝试两次待发 Gap/终止摘要，随后释放，不保证 End 到达。实际 Release 回执或对应 Worker 代次消失才是回收事实，不能从未收到 End 推断泄漏，也不能从本地 EOF 推断远端已停止。已 stopped 的订阅不被其他通道错误改成 failed。

## 7. Server SDK 授权与生命周期

```go
func (s *Server) SubscribeRX(ctx context.Context, uuid string, subscriptionID uint64) (*RXHandle, media.Reply, error)
func (h *RXHandle) UUID() string
func (h *RXHandle) SubscriptionID() uint64
func (h *RXHandle) Read(ctx context.Context) (media.RXFrame, error)
func (h *RXHandle) Status(ctx context.Context) (media.Reply, error)
func (h *RXHandle) Unsubscribe(ctx context.Context) (media.Reply, error)
func (h *RXHandle) Snapshot() media.RXSnapshot
func (h *RXHandle) Retired() bool
```

`uuid` 必须是当前 `compatChannels` 实际索引的 A 腿 UUID，非空、最多 128 字节、不得含空格或 TAB/CR/LF。它不是媒体 session ID、B 腿 UUID 或鉴权凭据。主循环在受理及成功回执交付时都检查真实本地 A 腿已经正确 ACK、Established、Allocated，且未 Ended/Releasing；生效处理模式、实际 Allocation、PCMA/PCMU 格式和 SDP PTime 均满足第 1 节。健康 Worker、正 PID、非零 Generation 及三个能力必须与通话冻结身份一致。

RX 不需要 `pcm_turn_v1`，也不占用 TX 互斥，可与 park、静默 read、现有放音/WAV、PCM 下行、主动 DTMF 并行；这不扩大这些应用各自的资格或容量合同。

句柄保存不可变 UUID、Worker、Generation、session 和 subscription，不持有 Call。Read 不读通话全局映射、不走 SIP 主循环、不创建逐帧 context、AfterFunc 或 goroutine。每个句柄只允许一个在途 Read；Status/Unsubscribe 共用另一个单在途控制标记，读与控制可并行。

Read 依次检查本地资格、底层 SDK 读取及期限、Server 撤权、帧 `CheckFresh()`，最后再查 Retired/readClosed。过期或撤权后关闭本地读权，不能将迟帧返回为正常音频；最后一次检查与真正业务使用仍非原子操作。

同 ID 的 Server.SubscribeRX 返回原句柄和最后一次已知成功回执，不再次发送媒体 subscribe，也不重新授权。缓存可能来自早前 Subscribe、Status 或 Unsubscribe，不等于新执行的 Status；其中旧 `active` 不能证明现在可读。没有成功缓存时返回原句柄与 `ErrRXSubscribeUnknown`。取得新鲜远端计数应调用 `h.Status(ctx)`；即使它返回 active，也不复活本地已关闭的读权。

取消前尚未提交可明确返回 context 错误；一旦可能已提交，必须保留返回的非 nil 句柄，即使同时有错误。未知结果不能当作不存在。取消会永久撤销该句柄的读权，迟到绑定也执行 `RevokeRead()`，清队列并唤醒读取，同时保留 Status/Unsubscribe。恢复流程为：保留原句柄，等待在途结果后查询或退订，取得实际 stopped/failed，再在电话仍合格时使用严格更大的 ID。旧 ID 不能复活；旧流没有实际终态时更大 ID 也拒绝。

通话 finish 先撤销读权，再推进原有 Release，不等待消费者或模型。实际释放或匹配 Worker+Generation 失败后退休句柄；迟回执不能重新授权已挂机通话。新代次不受旧失败通知影响。Server.Close 先撤读权、取消服务上下文，关闭媒体池并等待固定 4 个 RX 控制执行者退出。每次控制执行最多 3 秒，从执行开始计时，不能据此保证排队加执行总计 3 秒；调用者仍应设置业务 context 期限。

`Snapshot()` 是底层媒体本地快照，未绑定时表示 Pending。它的 Retired 不等同于 Server 层 `h.Retired()`；Server 已撤权而 Rust 尚 active 是两个层次的真实状态。Snapshot 不是新鲜远端查询，也不能代替完整授权。

| 错误 | 处理 |
|---|---|
| `ErrRXInvalid` | 参数不合法，修正 UUID/ID |
| `ErrRXNotEligible` | 未满足真实 ACK/local/G.711/Allocation/同代能力，未提交新订阅 |
| `ErrRXHandleBusy` | 额度、在途操作或绑定未到；不要并发堆积重试 |
| `ErrRXSubscribeUnknown` | 保留非 nil 句柄，等待后用同一身份 Status/Unsubscribe 对账 |
| `ErrRXStaleSubscription` | ID 回退或旧订阅没有实际终态，先完成清理 |
| `ErrRXHandleClosed` | Server 读权已撤销，同 ID 不复活；尚未退休时仍可清理 |
| `ErrRXHandleRetired` | 身份已释放或失效，停止使用旧句柄 |
| `media.ErrRXPending` / `media.ErrRXOutcomeUnknown` | 底层控制在途或结果未知，保留同一身份收敛 |
| `media.ErrRXSlowConsumer` / `media.ErrRXExpired` / `media.ErrRXClockInvalid` | 明确慢消费、期限或时钟失败；不解释为静音，清理旧流后用更高 ID |

底层明确拒绝、通道错误和 context 错误也可能返回。Server 不将这些内部错误伪装为 FreeSWITCH ESL 回复或新 HTTP 错误码。

## 8. 本批实际验证与未完成部分

以下记录截至本候选编写时，均保留原始结果及源/二进制指纹。不同测试层不相加成兼容功能数量，也不据此宣称 5000/10000 路 Voice Agent、外部 ASR 或 FreeSWITCH 成对认证通过。

| 证据 | 已验证范围 | 结果与限制 |
|---|---|---|
| `media-build-08/receipt.json` | 当前 Rust 08 库测试、Clippy、release 构建 | 170 个库测试通过、0 失败；包含既有功能，不是 170 项 RX 或 FS 认证 |
| `go-clock-01/receipt.json` | Go RXS2 SDK、时钟、队列及竞态 | 21 顶层 + 84 子项通过；macOS arm64 实际执行，Linux arm64/amd64 仅编译不等于运行通过 |
| `server-sdk-03/receipt.json` | Server 授权、错 ACK、取消/未知、迟回执、撤权、代次、TX 并行、有界执行与关闭 | 15 顶层 + 38 子项通过；定向 race、无实际网络，四件拥有源码前后稳定 |
| `rust-rx-rxs2-udp-01` / `rust-rx-rxs2-connected-01` | 直接 Rust 控制、FD4 和 RTP 原始记录 | 各 12 项通过，共 24 项；绑定历史 Rust 06，不能冒充 Rust 08 全套复跑 |
| `schema-02/receipt.json` | JSON Schema 及原始控制回执离线核验 | 185 向量：29 个结构正例（其中 4 个守恒或退订状态反例由独立运行约束检查拒绝）、156 个结构负例。24 份历史 wire 中 82 个 rx_state、42 个 stats、24 个 ready、18 个真实拒绝回执均符合各自结构和语义；无网络重跑 |
| `go-pause-01/receipt.json` | Rust 08 实际 RTP → OS FD4 → Go SDK 进程暂停恢复 | PCMA/PCMU × 普通/connected UDP 四组通过；旧数据均报期限失败，SDK DeliveredSamples=0，退订与释放完成。范围没有 SIP 或模型 |
| `server-sip-03/receipt.json` | Rust 08 + Server 03，真实 SIP 授权与收音回收 | 1 顶层 + 4 子项通过，4 通电话、32 帧/5120 样本逐样本核验；错 ACK 拒绝、正确 ACK 授权、BYE 撤权、fresh-zero 状态、四媒体端口复绑及 32 秒终态 GC。不是容量或供应商认证 |

当前 Rust 08 验证二进制 SHA-256 为 `1a3fdd0342f689ff88b3fe89056b23bc0858cd1177601dc5c68947d699227dde`。历史 24 项直接 wire 证据绑定 `de02c36cffd093118f2c59e85c366b4eee2fab26fa69572349f71f41741c9028`，两者明确分开。

后续仍需外部 ASR 传输与重连协议、供应商真实消费、VAD/endpointing、打断仲裁、双轨录音、其他 Codec 的处理图、Linux 同机同配置容量/长稳测试，以及目标 FreeSWITCH 的成对行为对照。本批没有为这些缺口预填绿色。

相关合同：[本地处理媒体](processed-local-reference.md)、[桥接处理媒体](processed-media-reference.md)、[PCM 回合](pcm-turn-reference.md)、[RVA1 下行通道](rva1-reference.md)、[Voice Runtime 方向](voice-runtime-direction.md)、[V1 验收标准](voice-runtime-v1-acceptance.md)。
