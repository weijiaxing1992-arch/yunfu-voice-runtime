# 媒体按键采集、提示音与 PCM 接口

文档版本 1.12.0。本页描述 RustSwitch 自有 Go→Rust 逐行 JSON 交互媒体协议；独立流式音频合同见 [PCM turn](pcm-turn-reference.md)，已 ACK 的外部入口见 [RVA1](rva1-reference.md)。它为本地 IVR 和已有桥接通话提供真实 telephone-event 输入与 G.711 提示音，不是 FreeSWITCH ESL 协议，也不是完整播放器、实时 AI、ASR、TTS 或全编解码转码接口。

参考基线仍为 FreeSWITCH 1.11.3，提交 `ef32e205295e29f034f1453ad245ba5efb07b94a`。原版通道按键队列、收号和文件播放分别见 [switch_channel_queue_dtmf](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_channel.c#L504)、[switch_ivr_collect_digits_count](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_ivr.c#L1342)、[switch_ivr_play_file](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_ivr_play_say.c#L1247)。本实现没有替代这些函数的全部语义；本地回归不等于原版对测。

## 通道与生命周期

一行一个请求、一行一个对应响应，`id` 是本次 RPC 的关联编号。Go 的 JSON 控制管道每 worker 串行处理一个在途请求，写入和回复共用一秒期限；控制队列与行长度都有上限。PCM 数据另用每 worker 一条独立 FD3 datagram lane，不把每帧样本送进 JSON 或 SIP 主循环。内部版本暂保持 `protocol_version:1`，原有 allocate/connect/release/stats 字段继续兼容；旧媒体程序会明确拒绝新操作，不能用旧程序声称已支持交互能力。

`allocate` 保存已验证的 A 腿 RTP/RTCP 地址与 offer 中的编码、telephone-event 参数。只 A 腿的本地 IVR 可以采集 A 按键并向 A 播放，无需假 B 地址。relay 桥接在 `connect` 后双向采集与原样转发；g711 bridge 则经过真实解码/编码图，见 [处理合同](processed-media-reference.md)。仍然分配四个 UDP 端口，本地 IVR 未优化为两端口模式。

主编码、DTMF PT、独立时钟和事件集合沿用原协商契约。采集发生在源地址、RTP 结构、PT、事件集合及速率验证之后；不做 NAT 自动学习、带内 DTMF 检测或 SRTP 解密。源地址匹配不是密码学认证。

释放会话会关闭 socket、取消未完成按键、播放及 PCM turn 计时项并清待发队列；释放本身不生成虚假的播放成功状态。已产生的全局事件暂留在有界日志中；消费者必须同时匹配 worker 代次和会话号，不能把释放后读到的历史事件分配给新呼叫。

## 拉取真实按键

请求：

```json
{"id":10,"op":"dtmf_events","after_seq":0,"limit":64}
```

| 字段 | 类型与范围 | 含义 |
|---|---|---|
| `after_seq` | uint64，缺省 0 | 当前 worker 代次内已消费的末尾事件序号。不能大于当前最新序号。 |
| `limit` | 整数 1–64 | 最多交付的事件数；不接受 0 或任意批量大小。 |

正常响应示例：

```json
{"id":10,"ok":true,"type":"dtmf_events","events":[{"sequence":1,"session":42,"leg":"a","kind":"digit","digit":"5","event":5,"duration_ticks":4800,"clock_rate":48000,"ssrc":1234,"timestamp":10000,"reason":""}],"next_seq":1,"oldest_seq":1,"overflow":false}
```

| 响应字段 | 语义 |
|---|---|
| `events` | 本批事件数组，最多 64 项。空数组只表示当前没有新交付的完成或不完整事件。 |
| `next_seq` | 本批实际交付末尾序号；空批次保持请求游标。不能直接推进到尚未交付的全局最新序号。 |
| `oldest_seq` | 日志仍保留的最早序号；尚无事件时为 1。 |
| `overflow` | `after_seq < oldest_seq - 1`。存在已被覆盖的事件，活跃收号必须明确失败，不能按普通超时处理。 |

每个事件的字段：

| 字段 | 类型 | 语义 |
|---|---|---|
| `sequence` | uint64 | worker 代次内严格递增的全局序号。 |
| `session` | uint64 | 原始媒体会话编号，不是端口或 SIP Call-ID。 |
| `leg` | `a` / `b` | 输入来源腿。 |
| `kind` | `digit` / `incomplete` | 完成事件或可确认的不完整输入。 |
| `digit` | 字符串 | `0`–`9`、`*`、`#`、`A`–`D`；事件 16（flash）与不完整事件为空。 |
| `event` | uint8，0–16 | 原始事件编号，必须属于协商集合。 |
| `duration_ticks` | uint16 | 报文中的持续刻度；实际时长为该值除以 `clock_rate`。 |
| `clock_rate` | uint32 | 已协商的 8000、16000、32000、44100 或 48000 Hz；与主音频时钟可以不同。 |
| `ssrc` / `timestamp` | uint32 | 原始 RTP 同步源和事件起点；不是墙钟时间。 |
| `reason` | 字符串 | 完成时为空；不完整时提供原因。 |

只有真实收到 E 位且持续时间非零的合法事件才会形成完成通知。relay 的合法重复结束包继续转发；g711 bridge 保持同事件输出起点并使用本机序号，本地单腿不回送入站事件。采集层按腿、SSRC、事件起点和编号去重。每方向保留最近 32 项，并用至多四个 SSRC 的时间戳水位拒绝缓存覆盖后的旧重传，支持时间戳自然回绕。这里实现的是常用按键集合；RFC 长事件分段没有完整重组能力，检测到分段会拒绝假报多个按键。[RFC 4733](https://www.rfc-editor.org/rfc/rfc4733)

| `incomplete.reason` | 触发条件与业务处理 |
|---|---|
| `end_packet_not_received` | 已收到起始/持续事件，两秒没有后续包且未确认结束；不得补造完成数字。 |
| `next_event_before_end` | 同 SSRC 下一起点已到达，上次事件仍未结束；先交付缺口，再交付新事件。 |
| `pending_event_evicted` | 32 项有界记录被填满时仍有未完成项；明确暴露丢失的状态。 |
| `segmented_event_unsupported` | 检测到尚未支持的长事件分段。 |
| `ssrc_limit` | 同方向已观察到四个 SSRC，再出现新来源编号；该方向采集进入不可用状态，直到会话释放。 |
| `event_rate_limit` | 新事件超过每方向 50 次/秒及 50 次突发预算；该方向采集进入不可用状态，直到会话释放。 |

消费者收到 `incomplete` 应将该次收号明确失败；对采集防护导致的不可用状态，后续收号也应拒绝。此状态不停止原有合法桥接 RTP。worker 全局只保留 1024 个事件，覆盖通过游标显式暴露，空闲万路不会为每路创建事件队列或轮询协程。

推荐由控制主循环以固定节拍按 worker 拉取，每 worker 最多一个在途 RPC，完成回调只投递到有界结果队列。Go 的 `Request.AfterSeq/Limit` 与 `Reply.Events/NextSeq/OldestSeq/Overflow` 已提供强校验；每个 worker 重启后必须重建游标，不跨代次续用。

未收到包不能证明音频静默；如果所有事件包都在到达本机前丢失，本采集器无法凭空检测按键。它不会把报文缺失编码成“静音帧”或正常完成。应用若需要语音活动检测、网络质量定位或严格区分全部丢包与无人输入，需单独实现并验收。

## 真实 G.711 提示音

启动示例：

```json
{"id":20,"op":"playback_start","session":42,"playback_id":1,"leg":"a","frequency_hz":1000,"duration_ms":200}
```

| 字段 | 限制 |
|---|---|
| `session` | 必须为当前 worker 已分配的会话。 |
| `playback_id` | 非零 uint64；新任务编号必须大于该会话上一任务。相同编号、相同参数重试返回原状态，不重播。 |
| `leg` | `a` 或 `b`；A 可在仅本地一腿模式播放，B 必须已设置真实对端。 |
| `frequency_hz` | 单频 200–2000 Hz。 |
| `duration_ms` | 20–10000 毫秒，必须为 20 的整数倍。 |

仅接受已协商的 PCMU/PCMA、8 kHz、单声道，包括正确声明的动态 PT。按协议协商的 PT 生成真实 μ-law/A-law 字节，20 毫秒一个 160 字节音频负载。RTP 音频时间戳每包增加 160。relay 的旧播放源独立于桥中原 RTP 流，不能由该路径推导完整 RTCP 发送端报告；g711 local 的原 PCM 则进入图编码，与 PCM turn/DTMF 共享 TX 身份并产生本机 RTCP。g711 bridge 不支持播放注入。[RFC 3551](https://www.rfc-editor.org/rfc/rfc3551)

一个会话同时最多一项播放，一个 worker 同时最多 64 项。relay 桥播放期间只替代目标腿来自桥另一端的主音频和 CN 舒适噪声；反方向主音频、telephone-event 与 RTCP 继续转发。目的腿原主音频及 CN 有意被替代的数量记入 `stats.playback_replaced_packets`，不能把该数解释为拥塞丢包。结束或停止后恢复普通桥转发。

查询与停止：

```json
{"id":21,"op":"playback_status","session":42,"playback_id":1}
{"id":22,"op":"playback_stop","session":42,"playback_id":1}
```

三种操作均返回同类状态：

```json
{"id":21,"ok":true,"type":"playback_state","session":42,"playback_id":1,"state":"completed","sent_packets":10,"total_packets":10,"message":""}
```

| `state` | 含义 |
|---|---|
| `loading` | 仅文件放音：已受理但尚未解码，此时sent/total均为0；失败/取消仍可发生，不能据此报告已发音。 |
| `running` | 正在发送或等待最后一帧的播放时间结束。 |
| `completed` | 所有标称包均被本机 UDP 接受，且最后一帧的 20 毫秒时间已结束。不能只凭此认定远端收到或播放。 |
| `stopped` | 收到匹配任务编号的停止命令；不假报完成。重复停止返回同状态。 |
| `failed` | 调度迟到超过一帧，或 UDP 发送失败/阻塞；`message` 给出具体原因。 |

`sent_packets` 是本机成功发送数；单音的 `total_packets` 为原始时长除以20；WAV加载成功后为ceil(samples/160)，加载中为0，不会为失败或少发缩小分母。任务迟到不会靠连续补发制造突发，也不会延长播放窗口后假报原时长完成。

下列请求返回 `ok:false,type:error,message`：未知会话、缺失 B 对端、非 G.711 编码、超出频率/时长范围、相同任务号不同参数、已有播放仍运行、旧任务号、新播放超过 worker 上限，或查询/停止不匹配的任务。调用者必须发布失败的 ESL 应用完成事件，不能报告成功。

输入没有文件名、下载地址、shell 命令或插件路径。应用层最多映射所声明的有限单音形式，例如 `tone_stream://%(200,0,1000)`；WAV文件通过独立 `playback_file_start` 入口提供，详见 [文件合同](file-playback-reference.md)；多频节奏、音乐等待、TTS 和完整原版 `tone_stream` 语法仍不在本接口范围内。

## 验证与证据边界

纯 Rust 状态回归覆盖结束去重、跨时钟、缺结束和下一事件缺口、1024 项日志覆盖与 16 KiB 控制行边界、旧事件重放/时间戳回绕、SSRC 和事件率预算、长事件分段拒绝、G.711 波形与 RTP 递增、播放迟到失败。

`tests/media_interaction_e2e.py` 使用真实 Rust worker、独立 UDP 端点和独立 G.711 解码检查，验证完成去重但原样桥转发、48 kHz telephone-event、错误来源/PT/事件拒绝、缺结束包、释放取消计时、本地只 A 腿、PCMU/PCMA 实际正弦相关度、播放停止和恢复桥、播放期间反向音频及双腿按键。应分别运行普通 UDP 与 connected UDP 模式，并保留每轮日志；子环境若 UDP 绑定被拒绝，属于未执行网络验证，不能列为通过。

以上证明功能路径和已测报文行为，不代表声卡听感、原版 FreeSWITCH 对测、SIP 全协议兼容、语音 AI 对话质量或 10000 路主动播放容量。媒体透传容量与主动播放/收号功能组合必须分别验收。

## PCM 与既有应用的并存规则

本地 g711 的 PCM turn 与 tone/WAV 只能有一个音频发送所有者；外部 SDK 同时检查已执行和排队的提示音应用。纯静默 read 和 park 不持有音频 TX，仍可收集真实输入按键；本地主动 DTMF 可以与 PCM 共用 RTP SSRC/包序，事件时间戳不随结束重传变化。

`playback_stop`、`uuid_break` 和 read 终止符不自动取消 PCM。轮次中断必须使用对应 turn_id 的 INTERRUPT，并核对实际 sent/discarded/queued 与终态；受理或本地句柄关闭不等于 Rust 停发。完整帧格式、背压、未知结果与尾帧语义分别见 [内部 PCM](pcm-turn-reference.md) 和 [RVA1](rva1-reference.md)。
