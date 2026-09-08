# 音频接口、编解码与 FreeSWITCH 对照

文档版本 1.12.0。云蝠 Voice Runtime 默认使用 `relay` 同格式 RTP 透传，可显式启用 `g711` 双腿或 local 单腿实时处理图；1.12 增加已 ACK 本地通话的流式 PCM 下行。G.711 图采用原生 8 kHz PCM；离线 Rust 音频 SDK 保留 8/16/24/48 kHz 单声道 PCM、P0 编解码及显式分支重采样。G.722/Opus 等离线后端、ASR/TTS 与完整录音尚未接入实时图。当前运行能力由真实 worker 握手决定，完整合同见[实时处理参考](processed-media-reference.md)及[管理压测](processed-testing-reference.md)。

## 管理接口与页面

`GET /v1/codecs` 返回 `CodecCatalog`，完整模型见 [OpenAPI](openapi.json)。管理后台“音频编解码”页面展示该接口真实结果；“压力与电话测试”使用同一套测试预设。

| 字段 | 合同 |
|---|---|
| `version` | 目录合同版本，当前 `1.2.0`，独立于文档版本 |
| `media_mode` | relay为`same_codec_rtp_passthrough`，g711为`g711_audio_graph` |
| `live_transcoding` | 仅显式启用g711且全部预期worker健康并声明真实处理能力时为true |
| `runtime_media` | 每次请求重新读取worker代次、PID、健康、能力、运行模式及观测时刻 |
| `live_codec_profiles` | 当前模式允许的格式；g711只有PCMU/PCMA，是否就绪须同时看runtime_media |
| `codec_profiles` | 测试 PT、名称、音频采样率、RTP 时钟、声道、包时长、fmtp、验证边界 |
| `native_audio` | 媒体程序实际探测的编解码后端、PCM 格式与限制；探测失败时为 null |
| `native_audio.pcm` | 保留原 SDK 的 16 kHz 格式说明，不能用它推断新入口只有这一种采样率 |
| `native_audio.audio_frame` | 新帧支持的四种采样率、10/20 ms 帧长、最大 960 个样本；`live_audio_graph=false` |
| `native_audio.codecs[].native_pcm` | 每种编码原生 PCM 入口实际探测的可用性、PCM 采样率、RTP 时钟与失败原因 |
| `native_probe_error` | 探测失败原因；成功为空字符串 |
| `scope / roadmap` | 当前边界及尚未提供的格式或后端 |

只读请求受原管理 Host 边界及重请求预算约束，正常返回 200；来源不符返回 403，重请求预算耗尽返回 503。后端缺失仍返回 200，并明确表示对应编码不可用。每个控制实例只执行一次受信媒体程序的 `--audio-capabilities`，超时两秒，stdout/stderr 分别限 64 KiB。更换库路径或程序后，重启控制实例才重新探测；页面刷新不会反复派生进程。

## 实时 SIP / RTP 格式

| 格式 | 测试 PT | 音频 Hz | RTP 时钟 Hz | 测试声道 | 当前能力 |
|---|---:|---:|---:|---:|---|
| PCMU / PCMA | 0 / 8 | 8000 | 8000 | 1 | relay同格式透传；g711实时异律处理；Rust原生SDK |
| G722 | 9 | 16000 | 8000 | 1 | 同格式透传；可选原生 SDK 后端 |
| OPUS | 111 | 48000 | 48000 | 2 | 同格式透传；可选原生 SDK 后端 |
| G729 / G729A | 18 | 8000 | 8000 | 1 | 同格式透传，SDK 编解码未接入 |
| G726-32 / -16 / -24 / -40 | 110 / 112 / 113 / 114 | 8000 | 8000 | 1 | 同位率及打包方式透传 |
| AAL2-G726-32 / -16 / -24 / -40 | 120 / 121 / 122 / 123 | 8000 | 8000 | 1 | 与 G726 独立识别，不隐式变换位打包 |
| L16 | 119 | 16000 | 16000 | 1 | 网络大端 PCM 透传，不能直接当作内部小端 PCM |

上述 PT 是发生器配置，真实动态 PT 按 `a=rtpmap` 识别。relay两腿必须选择同编码、同PT及可兼容参数，转码或更换G726打包方式会拒绝。显式g711图可以在两腿独立协商PCMU/PCMA及载荷号；非G711或非20ms仍在分配前拒绝。实际可接受的 ptime/fmtp 组合由 SDP 和 Rust 元数据校验共同决定，测试预设统一 20 ms。G.722 每个 20 ms 包的 RTP 时间戳步长为 160；Opus 为 960。Opus SDP 的 `/48000/2` 不要求每个包都携带双声道音频。

仍只支持 IPv4、一个 `RTP/AVP` 音频流和双向 `sendrecv`。本轮没有增加 SRTP、ICE、WebRTC、重协商、转接、会议或实时录音；实时双腿处理限G.711。AMR-NB、AMR-WB、EVS、iLBC、G.723.1、GSM 当前明确拒绝；后端计划不能作为已支持宣告。

## 辅助媒体与丢包语义

telephone-event 单独保留载荷、时钟及合法事件集合，relay校验RFC4733事件结构后透传，g711按新TX序号和映射时间戳终结转发，保持协商结果。已实现的接收路径还会对完整 RTP 按键去重，发布 `DTMF` 业务事件并供受限 `read` 应用收号；显式配置 `dtmf_type=info` 时使用经过真实 SIP 对话与来源校验的 INFO 按键，避免两种输入重复收号。收号失效、缓存和超时见 [IVR 接口](ivr-reference.md)。新增本地 A 腿 `uuid_send_dtmf` 发送队列与自有状态查询见 [DTMF 发送接口](dtmf-send-reference.md)；它发送 telephone-event，不合成带内按键音，也不把发送动作冒充接收到的 `DTMF` 事件。音内 DTMF 检测、桥接通话发送及原版全部收发语义仍未实现。

CN 单独协商载荷及与主媒体匹配的时钟，不能把任何未知 PT 当作舒适噪声。离线 SDK 的输入明确分成 `Packet`、`PacketLoss`、`ComfortNoise`。空字节包被拒绝，不默认为丢包。输出区分真实解码 `Decoded`、调用方提供的 PCM `SuppliedPcm`、编解码器 PLC `CodecPlc`、历史帧近似补偿 `HistoryConcealment`、缺失音频和 CN 参数。CN 只保留 SID 音量及谱参数，当前没有 RFC3389 噪声合成；它还会清除旧语音历史，避免后续丢包重放 CN 之前的语音。丢包和 CN 都不能被调用方当作“用户沉默”。

G.711/G.722 离线 SDK 的历史近似补偿有衰减和 120ms 上限，无历史或超限返回缺失；Opus SDK 使用官方解码器 PLC。实时 G.711 图已提供有界抖动缓冲和有限历史补偿，CN 仍不合成噪声。1.12 的 PCM turn 支持旧轮失效和明确 0/20/40ms 中断；VAD 自动触发、ASR/TTS 供应商、完整 FEC 或端到端丢包恢复未完成。`uuid_break` 不自动取消 PCM，具体边界见 [轮次接口](pcm-turn-reference.md)。不能承诺网络零丢包。

构建来源、原生ABI限制与完整许可见 [原生音频说明](../../native/audio/README.md)。

## Rust SDK 与原生库

入口在 `media/src/audio`。`AudioFrame` 保存单声道有符号 16 位样本，导入/导出使用 S16LE，只接受精确 10 ms 或 20 ms，不隐式补零或裁剪。合法帧的最大样本数为 960，即 1920 字节 PCM。

| 采样率 | 10 ms 样本数 | 20 ms 样本数 | 入口 |
|---|---:|---:|---|
| 8000 Hz | 80 | 160 | `with_sample_rate` / `from_s16le_at` |
| 16000 Hz | 160 | 320 | 原 `new` / `from_s16le`，或显式采样率入口 |
| 24000 Hz | 240 | 480 | `with_sample_rate` / `from_s16le_at` |
| 48000 Hz | 480 | 960 | `with_sample_rate` / `from_s16le_at` |

`AudioFrame::with_sample_rate(samples, rate)` 接收样本所有权，`from_s16le_at(bytes, rate)` 验证小端字节及帧长；`samples()` 只读，`valid_samples()` 返回有效样本数。`Clone` 会复制样本，导入、导出和异采样率转换仍可能分配内存，不能宣称零复制或实时线程零分配。

`AudioCodec::new(name)` 保持原 16 kHz PCM 合同。新增 `AudioCodec::native_rate(name)` 使用下表原生 PCM 入口；这不改变 SDP 的 RTP 时钟规则。编码会拒绝与上下文采样率不同的帧，24 kHz PCM 需要调用方显式转换，不能直接传给任意编码器。编码输出是载荷字节，不包含 RTP 包头或自动发包调度。

| 编码名称 | `native_rate` 的 PCM Hz | RTP 时钟 Hz | 后端 |
|---|---:|---:|---|
| `PCMU` / `PCMA` | 8000 | 8000 | Rust G.711 |
| `G722` | 16000 | 8000 | 可选固定 SpanDSP 子集 |
| `OPUS` | 48000 | 48000 | 可选固定官方 libopus；SDK PCM 仍为单声道 |

SDK 仅接入表中 P0 编码，其他格式的实时透传不代表 SDK 可以解码。一个编解码上下文由一个方向的调用者顺序使用；原生对象销毁之前动态库保持加载。

### 显式媒体时间和离线分支

`AudioPosition::new(media_time_ns, rtp)` 的时间是流起点后的媒体时间，不是 UTC 或 UDP 到包时刻。可选 `RtpPosition` 分开保存 RTP 时间戳与时钟；推进 10/20 ms 时，媒体时间检查溢出，RTP 时间戳按自己的时钟推进并允许 32 位回绕。G.722 的 16 kHz PCM 不会因此把 8 kHz RTP 时钟改成 16 kHz。调用方通过 `AudioFrame::at_position` 或 `AudioBlock::new(output, position)` 显式附加时间；SDK 不自行推断网络时序。

`AudioBranch::new(source_rate, target_rate)` 支持表中四种采样率之间的离线转换，`process(AudioBlock)` 检查源帧格式及时间连续性。同采样率直接转移帧所有权；异采样率使用有状态的 127 抽头 FIR 参考转换器。`filter_delay_ns()` 返回滤波算法延迟，当前不会自动补偿媒体时间戳；保留的 RTP 信息仍属于源流，不改写成输出编码的时钟。

分支拒绝倒退或重叠的媒体时间；遇到前向时间缺口、`MissingAudio` 或 CN 会重置滤波历史，保持缺失/CN 元数据，不生成替代 PCM。`close()` 清理历史并拒绝继续输入，不补出虚构尾帧。每帧转换有界，但输出仍分配样本；主观质量、ASR 效果及生产容量需要独立验收。上述 FIR 分支转换仅供离线 SDK 调用，尚未接入实时 ASR/TTS 分流。实时 G.711 图已使用借用 AudioFrameView，但保持原生 8k；PCM turn 的期限与淡出由本地发送调度器提供，不调用此离线转换器。

### 构建与实际探测

```sh
python3 native/audio/build_codecs.py --prefix /absolute/private/audio-libs
export RUSTSWITCH_G722_LIBRARY=/absolute/private/audio-libs/librustswitch_g722.so
export RUSTSWITCH_OPUS_LIBRARY=/absolute/private/audio-libs/opus/lib/libopus.so
bin/rustswitch-media --audio-capabilities
bin/rustswitch-media --check-audio
```

macOS 扩展名为 `.dylib`。库路径必须是绝对路径；从可信部署环境配置，不接受网页提供任意库路径。脚本可用 `--opus-archive` 指定离线官方归档，仍校验 SHA-256。不会安装系统库。SpanDSP 来源与 LGPL 许可保存在 `native/vendor/spandsp`；Opus 固定 1.5.2，归档 SHA-256 为 `65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1`。

`--audio-capabilities` 分别返回原 16 kHz `pcm`、新 `audio_frame` 能力和每个编码的 `native_pcm` 探测，`live_transcoding` 与 `audio_frame.live_audio_graph` 均为 `false`。`--check-audio` 检查 P0 实际编码/解码、原生 PCM 采样率、显式分支转换、PLC 来源及 CN 语义；任一所需后端缺失或检查失败会非零退出。该结果不是 MOS、原版成对差分或实时转码压测。原有 `rs_codec_get_v1` G.711 ABI 检查保持可用；新的原生后端不能直接装载 FreeSWITCH `mod_*.so`。

## IPC 扩展与版本边界

`allocate/connect` 均支持 `codec{name,sample_rate,rtp_clock_rate,channels,ptime_ms,fmtp}`。`fmtp` 字段必需，无参数时传 `""`。新编码的 `connect` 必须携带完整 `codec` 和 `payload`，并与分配时的编码、PT、采样率、RTP 时钟、声道和 ptime 一致；fmtp 的方向性窄化由 Go 协商层校验。省略或 null 的 `codec` 只用于旧 PT 0/8、20 ms 合同。旧版 `connect` 无 codec 时忽略其 `payload` 并沿用原会话 PT，例如 PCMA 会话收到旧 Go 默认 `payload=0` 仍保持 PT8，不切换为 PCMU。

`allocate/connect` 使用 `dtmf_clock_rate`、`dtmf_events`、`cn_payload`、`cn_clock_rate` 保留辅助协商；CN null 表示取消。完整形状见 [媒体 IPC Schema](media-ipc.schema.json)。必须成套部署本轮 Go 与 Rust；旧 worker 可能忽略新增命令字段，版本 1 握手不能单独证明音频扩展能力。

本地电话的 `dtmf_send` / `dtmf_send_status` 是新增真实发送及查询操作，受媒体队列和时间预算约束；启用本地发送时间线后拒绝转成透明桥，包括重复 `connect`。受限 WAV/提示音播放与按键发送可共享本地 RTP 序号和时钟，但不因此支持任意编码混音或实时转码。业务入口及失败处理见 [DTMF 发送接口](dtmf-send-reference.md)。

媒体配置新增 `excluded_port_blocks`，每项是相对分区起点四对齐的完整四端口块。Go 按固定分片边界过滤下发，Rust 独立校验范围、重复与剩余容量，排除块不分配给通话。此设置用于保留受保护或繁忙资源，不扩大 UDP 地址空间。

## 与 FreeSWITCH 分别验证

| FreeSWITCH 能力 | RustSwitch 当前对应 | 仍需验证或实现 |
|---|---|---|
| `show codecs` / 实际加载编解码模块 | `/v1/codecs` JSON 目录 | ESL 入口、原版返回格式与动态注册语义 |
| Sofia SDP 与 RTP 引擎 | 受限协商、relay 同格式透传与 G.711 图 | 原版实例逐组合成对呼叫、重协商、安全媒体与复杂线路 |
| 编解码与实时媒体图 | 多采样率离线 SDK 保持；真实 G.711 双腿/单腿 8k PCM 图及有界轮次下行 | G.722/Opus 等实时图、实时分支重采样、ASR/TTS 供应商和全部原版行为 |
| DTMF 收号和事件 | 完整 RTP 按键去重、受限 INFO 输入、真实 DTMF 事件及 read | 原版全部队列、事件字段、音内检测与复杂收号语义 |
| `uuid_send_dtmf` | 已 ACK 本地 A 腿的有界 telephone-event 发送；另有自有状态查询 | 桥接通话、原版全参数、全部时序及对端业务接收验证 |
| CN / PLC / FEC / jitterbuffer | 显式丢包/CN SDK；G.711 实时有界抖动、来源标记与有限 PLC | FEC、噪声合成、完整网络质量与容量验收 |
| AMR / EVS 等模块 | 尚未实现 | 按运营商接入配置验证，不能靠目录占位认定兼容 |

所有原始对照项、未实现项与测试证据分别保留在 [完整对照](freeswitch-comparison.md) 和 [逐项验证账本](verification-ledger.md)。未运行原版实例的条目不授予原版兼容通过结论。

标准依据：[RFC3551](https://www.rfc-editor.org/rfc/rfc3551)、[Opus RTP RFC7587](https://www.rfc-editor.org/rfc/rfc7587)、[telephone-event RFC4733](https://www.rfc-editor.org/rfc/rfc4733)、[CN RFC3389](https://www.rfc-editor.org/rfc/rfc3389)、[AMR RFC4867](https://www.rfc-editor.org/rfc/rfc4867)。FreeSWITCH 对照绑定 1.11.3 固定提交；标准描述不代替产品测试。

## PCM 外部入口与格式转换边界

[RVA1](rva1-reference.md) 在真实 ACK 后授权本地 A 腿，以独立二进制连接提交 8kHz mono S16LE 的完整 20ms 帧；实际路径是调用方 PCM→图编码→统一 RTP/RTCP，不先做 G.711 编码再解码。内部 RSP1/RSR1、控制命令、队列上限与未知结果恢复见 [PCM turn](pcm-turn-reference.md)。原 C ABI/16k SDK 不变。

24k TTS 来源可在调用者的离线或专用适配分支显式转换到 8k 后提交；RVA1 无采样率字段，长度正确不代表音频格式正确，也不会自动识别或重采样。该样本入口不等于真实 TTS 服务、ASR/VAD 或录音已接入；最新限定验收与保留的 500/5000 失败见 [1.12 记录](release-validation-v1.12.md)。
