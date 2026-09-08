# SIP、媒体、内部通信与原生接口完整说明

> 本文档包 1.13.0 包含已验证的 [本地 A 腿 RX 收音、RXS2 与 Go SDK](rx-stream-reference.md)，保留 [PCM 下行轮次](pcm-turn-reference.md) 和 [外部 RVA1 入口](rva1-reference.md)。实际验证见 [1.13 记录](release-validation-v1.13.md)，实际安装版本须通过 `GET /v1/docs` 及部署回执核对，不能从文档包版本推定已安装。

文档版本 1.13.0；对应 RustSwitch 0.3.0 开发原型。本文覆盖非 HTTP 接口，HTTP 字段和错误见 [HTTP 接口说明](http-reference.md)，FreeSWITCH 的逐项差异见 [完整对照说明](freeswitch-comparison.md)。内部媒体 JSON 协议使用版本 1，二进制 RXS2 单独标识，与管理接口的 `/v1` 路径不是同一个版本体系。线路注册、本地呼入和 IVR 的组合用法见 [AI 呼叫接入](ai-calling-reference.md)。

## 接口分层与调用方向

| 接口 | 连接方式 | 当前使用者 | 运行状态 | FreeSWITCH 对应关系 |
|---|---|---|---|---|
| 管理 HTTP | 回环 TCP / JSON、文本、ZIP | 管理后台、运维脚本 | 已实现，26 个显式 HTTP 操作 | 独立管理协议；不能让 ESL 客户端直接切换到此端口 |
| SIP | 配置地址的 IPv4 UDP / TCP / TLS | 可信中继与固定上游 | 受限双腿桥接、本地 A 腿、上游注册及 Digest 客户端 | 与 Sofia 在部分 SIP 报文上互通；不等同完整 Sofia profile 或分机注册服务器 |
| RTP / RTCP | 每条通话预留四个 IPv4 UDP 端口 | A/B 两侧音频端点、本地 IVR/PCM | relay 同编码转发；G.711 双腿/本地解码、编码与本机 RTCP | 只覆盖限定 G.711 图，不提供完整媒体引擎、录音或全部转码语义 |
| 媒体 IPC | 子进程 stdin / stdout 的逐行 JSON | Go 控制面与 Rust worker | 内部接口，已实现 | 无 ESL、JSON-RPC 或 `switch_core_session_*` 二进制等价关系 |
| 内部 PCM 数据 | 每 worker 独立 FD3 Unix datagram，RSP1/RSR1 | Go Pool 与 Rust 单写轮次队列 | 已实现，8k mono/20ms；不是 SIP 授权接口 | 自有内部合同，无原版线格式映射 |
| 内部 RX 数据 | 每 worker 独立 FD4 Unix datagram，RXS2 | Rust 本地 A 腿与 Go 接收 SDK | 有界8k mono/20ms，固定时效与明确缺口 | 自有内部合同，不兼容原版媒体回调或ESL |
| Server RX SDK | Go `Server.SubscribeRX` 与冻结句柄 | 控制进程内可信适配器 | 正确ACK/local/G.711/同代资格，挂机撤读 | 无外部HTTP路径，不代表ASR供应商接口 |
| 外部 PCM 流 | 私有 Unix stream，RVA1/RVR1 | 本机同 UID 可信进程 | 可选部署，已 ACK 本地 A 腿授权及限定真实验收 | 自有协议，不是 ESL speak/TTS/模块 ABI |
| C 编解码 ABI | 本机动态库，C 函数表 | Rust 独立插件检查器 | 版本 1 已实现 | 自定义适配接口；不能直接加载原版 `mod_*.so` |
| C 协议 ABI | 函数表头文件 | 未来原生协议提供器 | 预留，未装载/执行 | Sofia-SIP/PJSIP 接入目标，消息 schema 尚未定义 |
| 生命周期日志 | 单写者 UTF-8 JSONL 文件 | 日志采集、验收工具 | 已实现，异步持久化 | 不是 ESL 事件流或完整 CDR |
| CLI | 本机进程参数和信号 | 部署、校验、压测 | 已实现 | 主进程 CLI 不等于 fs_cli；可选入站 ESL 支持有限 `fs_cli -x` 命令 |

入站 ESL 已实现有限子集，完整启动、认证、API、事件和限制见 [ESL 接口](esl-reference.md)。双端探针与剩余覆盖见 [成对测试用例](conformance-testing.md)。

## SIP 请求、响应与状态

UDP 信令监听为 `sip.listen`，对外地址取 `sip.advertise`，TCP/TLS 监听由 `sip.stream` 配置。配置 `sip.dialplan` 时使用启动冻结的受限XML权威匹配：未命中404，详见[XML合同](dialplan-reference.md)。未启用XML时，来话被叫号码精确命中 `sip.local_extensions` 时分配本地 A 腿媒体并自动应答；否则构造发给固定上游的 `sip:<user>@<upstream>`。本地列表最多 256 项，每项 1–32 个数字或 `+` 字符，不接受重复项、正则或通配符。配置页面中的 FreeSWITCH XML 拨号计划、目录和网关草稿不会参与此路由。

| 方法 | 输入和行为 | 主要输出 | 尚未提供的能力 |
|---|---|---|---|
| OPTIONS | 有效且可信来源的请求 | 200，`Allow: INVITE, ACK, CANCEL, BYE, OPTIONS, INFO` | 不查询 FreeSWITCH profile 或用户注册状态 |
| 初始 INVITE | 无 To-tag；合法 Via、From-tag、Call-ID、CSeq、Contact、Max-Forwards 和 SDP | 100 Trying；上游振铃/183、最终响应，或本地媒体分配后的 200/SDP | 支持启动冻结的受限 XML 条件和固定上游 Digest 挑战重试；尚无 originate API、完整 FreeSWITCH XML 条件语义、入站用户口令鉴权或复杂分叉 |
| 同一 INVITE 重传 | 原 Call-ID、来源和事务键匹配 | 重放上次 A 腿响应，不再分配媒体或扣新准入令牌 | 不提供任意请求幂等键 |
| ACK | 匹配已有 A 腿成功对话 | 通话标记 established，按既有状态推进 | 不能用限流延迟 ACK；未知 ACK 不产生“接通成功” |
| CANCEL | 匹配建立中的 A 腿事务 | 对 CANCEL 响应，并取消 B 腿/结束 A 腿 INVITE；处理迟到 200 | 不是通用 uuid_kill API |
| BYE | 匹配来源、标签和序列的既有对话 | 响应并清理另一腿、媒体与定时器 | 不支持控制面崩溃后恢复旧对话 |
| 对话内 re-INVITE | 已有对话但需重新协商 | 501 Re-INVITE Not Implemented | 保持、恢复、媒体地址重协商未实现 |
| INFO | 已建立对话；实际来源、连接、标签和递增 CSeq；通道显式 `dtmf_type=info` | `application/dtmf-relay` 有效按键进入收号并返回 200；同事务重传不重复收号 | 不接收任意 INFO 包，不提供音内检测或 INFO 发送 |
| 入站 REGISTER / MESSAGE / SUBSCRIBE / NOTIFY / REFER / UPDATE / PRACK 等 | 当前未支持的服务端方法 | 405，列出当前 Allow 方法 | 终端注册、消息、订阅、转接等未实现；出站 REGISTER 客户端另见下文 |

INFO 的 `Signal` 支持 `0-9`、`*`、`#`、`A-D` 以及数字事件编号 10–15；`Duration` 必须显式提供 20–8191 毫秒。模式值忽略大小写，建议使用 `info`。未启用模式为 488、错误类型为 415、错误正文为 400、错误对话为 481、Route/Require/Info-Package 扩展为 501。完整生命周期与 RTP 去重边界见 [IVR 接口](ivr-reference.md)。

可选 `sip.registration` 向唯一固定上游发送 REGISTER、刷新和注销；可选 `sip.trunk_auth` 为 REGISTER 与出站 INVITE 提供真实 Digest 客户端。它们不使入站 REGISTER 变成可用的终端注册服务器。算法、超时、可信来源、私有密码文件及退出边界见 [中继注册与认证](trunk-registration.md)。

报文使用 `CRLF` 行分隔及 `CRLF CRLF` 头/正文分隔。单个 SIP UDP 报文最多 16,384 字节，SDP 最多 8,192 字节；解析器限制头行和 SDP 行数。紧凑头 `v/f/t/i/m/l/c` 归并为对应标准名称，重复的单值头和含歧义事务字段会拒绝解析。

不可信来源、不能解析的报文以及已满的接收队列可能直接丢弃并计数，不承诺总能回复 SIP 错误。非白名单来源只有与固定上游地址端口完全匹配时才有额外接收许可。白名单不是用户口令认证，也不能替代公网 SBC。

| 本地状态 | 典型触发 | 对端处理与 FreeSWITCH 差异 |
|---|---|---|
| 400 | Max-Forwards、Request URI、Contact 无效 | 修正请求；原因短语不承诺与 FreeSWITCH 相同 |
| 405 | 未实现的请求方法 | 不能把该方法映射成已实现的 FreeSWITCH 功能 |
| 481 | 带 To-tag 却没有对应通话，或未知事务/对话 | 不存在恢复会话或查询 UUID 的替代接口 |
| 482 | 同一对话标识出现合并/冲突请求 | 重新检查事务唯一性 |
| 483 | Max-Forwards 为零 | 处理路由环路；不能当作峰值限流 |
| 488 | 不可支持的 SDP、对端或编码组合 | 按下方媒体范围重新协商 |
| 501 | Route/Record-Route、Require、Session-Expires、非 SDP 初始协商或 re-INVITE | 不具备完整 Sofia 扩展行为 |
| 503 + Retry-After | 并发/CPS/建立中保护，排空、日志、事务表或可用媒体资源不足 | 建议等待后发起新事务；没有服务端等待队列，不保证重试必然接通 |

上表列出接入层常见错误，并不枚举所有下游最终响应。上游失败、取消、媒体分配/连接失败和建立/ACK 超时还会触发对应状态清理。FreeSWITCH hangup cause、ESL `CHANNEL_*` 事件和本项目 SIP 原因短语不是一一映射。

### 可复用的完整报文构造示例

示例只构造字节，不发送网络请求。替换地址、端口、标签、分支和 Call-ID，且确保媒体地址属于允许网段。Content-Length 必须按编码后字节数计算。

```python
body = (
    "v=0\r\no=test 1 1 IN IP4 127.0.0.1\r\ns=test\r\n"
    "c=IN IP4 127.0.0.1\r\nt=0 0\r\n"
    "m=audio 30000 RTP/AVP 0 101\r\n"
    "a=rtcp:30001 IN IP4 127.0.0.1\r\n"
    "a=rtpmap:0 PCMU/8000\r\n"
    "a=rtpmap:101 telephone-event/8000\r\na=ptime:20\r\n"
).encode()
headers = [
    "INVITE sip:1001@127.0.0.1:5060 SIP/2.0",
    "Via: SIP/2.0/UDP 127.0.0.1:5090;branch=z9hG4bK-doc-example;rport",
    "From: <sip:alice@local>;tag=doc-a", "To: <sip:1001@local>",
    "Call-ID: doc-example@local", "CSeq: 1 INVITE", "Max-Forwards: 70",
    "Contact: <sip:alice@127.0.0.1:5090>", "Content-Type: application/sdp",
    f"Content-Length: {len(body)}", "", "",
]
wire = "\r\n".join(headers).encode() + body
```

桥接成功的逻辑顺序为：A INVITE → 分配媒体 → B INVITE → B 180/183 或 200 → A 200 → A ACK → 双向 RTP/RTCP → BYE → 媒体释放。早期 183 可先建立媒体转发。A/B 使用独立 Call-ID 和标签，不能直接把 A 的对话头复制到 B。本地 IVR 跳过 B 腿请求，只产生真实 A 腿身份；收到 A ACK 后才能执行 `read`、`playback`。未收到 ACK 时按配置期限清理，不制造上游接通或额外 UUID。

### TCP / TLS 可靠传输

`sip.stream` 为可选配置，字段见 [完整字段字典](schema-reference.md)。省略保持 UDP。TCP/TLS 两腿使用各自真实连接 ID，缓存、事务和断连清理不允许跨连接复用；关闭连接只处理归属该连接的通话。消息上限 16KiB，单连接待写队列 16 帧，硬连接预算默认 256。首字节起的完整帧期限默认 5 秒，空闲期限 300 秒，写期限 1 秒。TLS 上游校验证书链及名称，不提供跳过验证开关。

可靠传输不重传 SIP 请求及非 2xx 最终响应；INVITE 2xx 仍遵守端到端 ACK 重传与超时回收规则。支持 IPv4 TCP/TLS 并不包含 WS/WSS、IPv6、客户端证书、DNS/SRV、SIPS 路由、SRTP 或 WebRTC。

## SDP 与媒体报文契约

| 项目 | 当前要求或行为 |
|---|---|
| 地址族 | IPv4，SDP `IN IP4`；精确来源/目的地与配置网段匹配 |
| 媒体流 | 恰好一个 `m=audio ... RTP/AVP ...` |
| 编码 | `relay` 支持 PCMU/PCMA、G722、Opus、G729、G726/AAL2、L16，双腿须同编码同 PT，见 [音频接口](audio-reference.md)。`g711` 实时图限定 PCMU/PCMA、8kHz、单声道、20ms；桥接可独立协商两腿并执行 PCMU↔PCMA 转换，见 [双腿处理](processed-media-reference.md)；本地图仅 A 腿，见 [本地处理](processed-local-reference.md) |
| ptime | `relay` 缺省 20ms，按编码允许包时长，A/B 参数须兼容；`g711` 实时图固定 20ms，并校验每包 160 字节 G.711 主音频；离线 AudioFrame SDK 的 10/20ms 范围不代表实时图支持其他帧长 |
| telephone-event | 独立协商时钟、动态 payload 96–127 与事件集合；校验后透传并采集完整、去重后的按键；可发布 DTMF 和供受限 `read` 使用 |
| RTCP | 与 RTP 独立端口，可显式 `a=rtcp`，缺省按 RTP+1 推导；拒绝无效端口和复用 |
| 方向 | 当前双向 sendrecv；sendonly/recvonly/inactive 不支持 |
| 未支持能力 | SRTP、ICE/WebRTC、RTCP mux、多媒体流、视频、NAT 学习、混音、录音、会议 |
| 转发验证 | RTP/RTCP 版本、长度、扩展边界和协商负载校验；来源不符/非法包丢弃并计数 |
| 端口预算 | 每桥接通话四个端口；释放后进入配置的隔离期，受占用和分片分布影响 |
| UDP 连接模式 | `connect_sockets=true` 后内核按协商对端过滤；内核过滤的包可能不进入应用来源拒绝计数 |
| 包率 | RTP 每腿使用配置的令牌额度；RTCP 当前另固定为 50 包/秒 |
| 发送拥塞 | 有界队列与 5 ms 重试期限，超额/过期丢弃并计数，不无限排队 |

`tx_packets` 表示发送调用接受报文，不保证物理网卡或远端收到；`rx_packets` 包含读取后被校验拒绝的报文。macOS 不支持本实现的 socket 丢包辅助计数时会报告 `socket_drop_counter_supported=false`，不能把对应零值解释为内核零丢包。延迟、MOS 和逐路质量不是当前接口承诺的指标。

RTP/RTCP 标准参考：[RFC 3550](https://www.rfc-editor.org/rfc/rfc3550)、[RFC 4733](https://www.rfc-editor.org/rfc/rfc4733)。FreeSWITCH 具体能力和差分要求见 [SIP、媒体及原生兼容标准](../freeswitch-compatibility/04-sip-media-native.md)。

编解码、辅助媒体和离线 PCM SDK 的完整合同见 [音频接口与 FreeSWITCH 对照](audio-reference.md)。

实时电话事件采集与单频提示音见 [媒体交互接口](media-interaction.md)。提示音只支持 PCMU/PCMA 8 kHz，时长 20–10000 ms 且为 20 的整数倍、频率 200–2000 Hz；这不把其他可透传编码变成可播放或可转码编码。事件丢失和处理预算耗尽明确反馈业务失败，不能将缺包解释为用户沉默。

## 媒体内部 JSON 管道

此接口只用于 Go 创建的 Rust 子进程。配置通过 `--worker-config '<JSON>'` 参数传入；命令通过 stdin、回复通过 stdout，每条为 UTF-8 JSON 加一个 `LF`。stdout 不允许混入普通日志。不要把此通道开放为远程管理协议。

完整机器模型见 [媒体 IPC JSON Schema](media-ipc.schema.json)。本模型描述线上 JSON 形状及已知运行约束；不是新的 RPC 服务。

1.13增加 `rx_subscribe`、`rx_status`、`rx_unsubscribe` 与 `rx_state` JSON；音频本体通过 FD4 RXS2 传输，不作为JSON音频数组返回。共享时钟期限、8槽队列、15个状态字段、4个当前资源指标和Go句柄清理规则见 [完整RX合同](rx-stream-reference.md)。RX与FD3下行独立；媒体session不等于SIP UUID，外层必须另行授权。

| 通用字段/规则 | 说明 |
|---|---|
| `id` | uint64 请求关联号；Go 按 worker 串行递增，ready 主动握手用 0；不是协商版本或去重键 |
| `op` | `allocate / connect / release / stats / shutdown / dtmf_events / dtmf_send / dtmf_send_status / playback_start / playback_file_start / playback_status / playback_stop / pcm_turn_begin / pcm_turn_end / pcm_turn_interrupt / pcm_turn_status / rx_subscribe / rx_status / rx_unsubscribe` |
| `session` | uint64、当前 worker 内的资源编号；不同 worker 和重启代次不得混用 |
| `generation` | Go 内部请求字段，标记 `json:"-"`，不会进入 Rust JSON；Go 在队列前后检查 |
| `ok` | bool，媒体操作结果；不是 SIP 接通或媒体质量结果 |
| `type` | `ready / allocated / ack / stats / error / dtmf_events / dtmf_send_state / playback_state / pcm_turn_state / rx_state` |
| 单条命令长度 | 最多 16,384 字节，结尾换行必须位于该长度内 |
| 无效 JSON / EOF | 当前 reader 会关闭命令通道，worker 结束；不是每条都返回结构化 error |
| 已解码业务错误 | `ok=false,type=error,message=<文本原因>`；文本不是稳定的机器错误码全集 |
| 握手等待 | Go 最多等 5 秒；必须匹配 id=0、protocol_version=1、worker_id、PID 和 ready 类型；处理/PCM/RX另核对能力声明，RX要求processed_g711_v1、processed_g711_local_v1、rx_g711_local_v2及同代有效FD4 |
| 单次 RPC | 当前最多等待约 1 秒，超时会终止该媒体进程，影响所属分片；不只是取消此查询 |
| 串行与版本 | Go Submit 有序进入有界队列；同通话最多一个在途 connect 与一个最新待更新目标，结束后丢弃待更新。Rust 按实际到达顺序执行，线上 connect 仍无协商版本，绕过 Go 直接发送 IPC 不具备上述排序保证 |
| 响应与背压 | 校验 id、类型、会话和分配端口；排队饱和不执行，释放保持预留并退避重试。响应违约或明确释放失败按工作进程代次故障回收；受影响分片可能掉话 |
| 取消与输出 | 同步 Call 的排队取消与执行开始原子仲裁；已在途命令由既有 RPC 期限约束，调用者提前取消不证明零执行。PCM/RX SDK对可能执行但未收齐回执的结果明确返回unknown并保留非nil清理句柄；取消不等于媒体停止。Rust控制输出有写入与退出期限，退出先释放媒体端口；详见稳定性说明 |

### 启动配置

| 字段 | 类型 | 约束/含义 |
|---|---|---|
| connect_sockets | boolean | 缺省 false，协商后固定对端 |
| worker_id | 非负整数 | Go 分片编号 |
| bind_ip | IPv4 字符串 | 媒体监听 IP |
| port_start / port_end | uint16 | 包含边界的端口分区，起始偶数，完整四端口块 |
| max_calls | 正整数 | 本 worker 同时分配资源的硬上限，受端口和文件描述符约束 |
| receive_buffer_bytes | 整数 16,384–1,048,576 | 每 socket 请求缓冲大小；实际值取决于内核 |
| port_reuse_delay_ms | 非负整数 | 隔离期，worker 校验不超过 60,000 ms |
| max_packets_per_second_per_leg | 整数 100–10,000 | RTP 每腿令牌额度 |
| allowed_remote_networks | CIDR 字符串数组 | 非空，媒体目的地址白名单 |
| excluded_port_blocks | 整数数组，可省略 | 完整四端口块起点，相对分区起点四对齐；范围、重复及剩余容量均校验 |
| cpu_core | 非负整数或 null | Linux 可选 CPU 绑定；非 Linux 指定时拒绝 |

启动配置拒绝未知字段；消息枚举当前没有同等的 `deny_unknown_fields` 约束，客户端仍应只发送已定义字段。具体参数边界须同时满足 Go 启动配置校验和 Rust worker 校验，不能只根据序列化类型决定可部署值。

### 操作及成对消息

以下每个代码块中的对象都是单独一行消息，实际管道需追加 LF。端口仅作示例，不代表已分配。

```json
{"id":0,"ok":true,"type":"ready","protocol_version":1,"worker_id":0,"pid":12345}
```

| 操作 | 请求字段 | 成功响应 | 重试和失败含义 |
|---|---|---|---|
| allocate | id,op,session,a{rtp,rtcp},payload,codec,dtmf_payload,dtmf_clock_rate,dtmf_events,cn_payload,cn_clock_rate | allocated 加 session、a_rtp/a_rtcp/b_rtp/b_rtcp | 相同会话与相同参数重试复用分配；参数冲突、非法对端、容量/绑定失败返回 error |
| connect | id,op,session,b{rtp,rtcp},payload,codec,dtmf_payload,dtmf_clock_rate,dtmf_events,cn_payload,cn_clock_rate | ack | relay 新编码须提供 codec 和 payload，并与 allocate 的编码、PT、采样率、时钟、声道和 ptime 一致；g711 bridge 按独立两腿协商，local 拒绝 connect，详见处理图合同；不存在会话或协商冲突失败，线上无协商版本字段 |
| release | id,op,session | ack | 会话不存在也确认；已有会话回收并隔离端口，不能立刻当作可用 |
| stats | id,op | stats 加完整计数对象 | 读取快照并回收隔离到期端口；没有每通话媒体详情 |
| dtmf_events | id,op,limit；after_seq 缺省 0 | dtmf_events 加 events、next_seq、oldest_seq、overflow | 单 worker 代次的有界记录，limit 为 1–64；记录覆盖必须作为收号失败处理 |
| playback_file_start | id,op,session,playback_id,leg,path | playback_state | 相对root内受限WAV；受理loading后异步读取、实际播放或failed；详见[file-playback](file-playback-reference.md) |
| playback_start | id,op,session,playback_id,leg,frequency_hz,duration_ms | playback_state | 非零播放 ID；真实 G.711 提示音，每 worker 最多 64 路，不能据 running 认定播放已完成 |
| playback_status / playback_stop | id,op,session,playback_id | playback_state | running/completed/stopped/failed 及实际发送包数；停止和失败不能解释为完整播放成功 |
| dtmf_send / dtmf_send_status | id,op,session；发送另含 leg,digits,duration_ms | dtmf_send_state | 已 ACK 本地 A 腿的实际有界事件发送与状态；[完整字段](dtmf-send-reference.md) |
| pcm_turn_begin | id,op,session,turn_id,buffer_ms,prebuffer_ms | pcm_turn_state | 新轮严格递增；同 ID 同配置幂等且不复活；仅 local G.711 |
| pcm_turn_end | id,op,session,turn_id,final_samples | pcm_turn_state | final 等于 accepted，排空并等待末尾 20ms 才 completed |
| pcm_turn_interrupt | id,op,session,turn_id；fade_ms 缺省 0 | pcm_turn_state | 0/20/40ms，关闭旧输入，仅使用已有头帧淡出；重复请求不续期 |
| pcm_turn_status | id,op,session,turn_id | pcm_turn_state | 真实 accepted/sent/discarded/queued 与配置、年龄、失败原因，字段缺失不得补零 |
| rx_subscribe | id,op,session,subscription_id | rx_state | 非零且新ID严格递增；仅local G.711、有效FD4/共享时钟；同ID返回原状态、不复活、不续期 |
| rx_status | id,op,session,subscription_id | rx_state | 新鲜Rust状态、produced/submitted/dropped/queued事件和样本对账；与Server同ID缓存回执不同 |
| rx_unsubscribe | id,op,session,subscription_id | rx_state | 停止匹配订阅，丢弃未发送观察并保留终态；同ID重复不改变已完成结果 |
| shutdown | id,op | ack，然后退出 | 直接结束 worker，不等待自然通话排空；管理后台不向用户暴露此写命令 |

PCM 的四条 JSON 控制消息、完整回执、状态转移及未知结果详见 [PCM turn](pcm-turn-reference.md)。音频不使用 JSON：Go 创建专用 Unix datagram socketpair，子进程继承 `RUSTSWITCH_PCM_FD=3`，实际校验后才声明 `pcm_turn_v1`。RSP1 最多 44 字节头加 1600 字节 PCM，RSR1 固定 96 字节；一 worker 一条有界 lane，与 JSON 信令队列分离。

外部客户端不能凭内部 session 直接供音。[RVA1](rva1-reference.md) 使用可选部署配置 `pcm_stream`，在主循环确认已 ACK 本地 A 腿和当前 Worker 代次后授予 token。RVA1 外部错误码与 RSR1 内部错误码不同，队列满外部为 5、内部为 8；它们也不是 HTTP 状态或 FreeSWITCH `-ERR`。

RX使用独立FD4单向RXS2，不占FD3回执或传统播放互斥。168字节头保留subscription/session、时间线、源状态、缺口、共享时钟与不可续期截止时间。普通记录到期后不得作为新语音交付；gap/终态无音频期限但仍校验身份与结构。Server只授权已ACK A腿，外部适配器尚未提供；详见 [RX生命周期与错误](rx-stream-reference.md)。

`codec` 对象中的 `fmtp` 字符串必须存在，无参数时使用 `""`。省略或 null 的 codec 仅兼容旧 PT0/8、20 ms 合同；此时 Connect 的 payload 被忽略，使用分配时的 PT。旧 Go 给 PCMA Connect 发送 `payload=0` 仍保留 PT8。显式提供 codec 时必须同时提供非 null 的 payload，不能用旧默认值覆盖实际协商 PT。上述同编码沿用规则用于 relay；显式 g711 两腿的独立 PT/编码与 local 拒绝 Connect 规则见 [实时处理](processed-media-reference.md)。

```json
{"id":1,"op":"allocate","session":100,"a":{"rtp":"127.0.0.1:30000","rtcp":"127.0.0.1:30001"},"payload":0,"dtmf_payload":101}
{"id":1,"ok":true,"type":"allocated","session":100,"a_rtp":20000,"a_rtcp":20001,"b_rtp":20002,"b_rtcp":20003}
{"id":2,"op":"connect","session":100,"b":{"rtp":"127.0.0.1:31000","rtcp":"127.0.0.1:31001"},"dtmf_payload":101}
{"id":2,"ok":true,"type":"ack"}
{"id":3,"op":"release","session":100}
{"id":3,"ok":true,"type":"ack"}
{"id":4,"op":"connect","session":999,"b":{"rtp":"127.0.0.1:31000","rtcp":"127.0.0.1:31001"},"dtmf_payload":null}
{"id":4,"ok":false,"type":"error","message":"unknown media session"}
```

最后一条展示不存在会话的当前文本错误；文本并非独立稳定的错误码协议。`dtmf_payload:null` 明确表示关闭电话事件，不应该省略成“保留旧配置”的命令。

## C 编解码接口

以 [rs_codec_v1 原始头文件](../../native/include/rustswitch_codec.h) 为最终结构布局契约。动态库须导出 `const rs_codec_v1 *rs_codec_get_v1(void)`，返回描述符在卸载前有效。原始头文件同时纳入下载包；后端阅读器会按发布路径定位。

| 字段/回调 | 类型/方向 | 调用契约 |
|---|---|---|
| abi_version / struct_size | uint32 | 版本 1；结构大小须覆盖宿主读取字段 |
| name | char[32] | 固定长度编码名称，提供者保证终止字符 |
| sample_rate / channels | uint32 | PCM 为 int16_t，约定采样率与声道数 |
| max_pcm_samples / max_encoded_bytes | uint32 | 单次样本与字节上限，用于分配缓冲 |
| create | int32(void **context) | 成功写非空上下文；返回 0 成功，负值错误 |
| destroy | void(void *context) | 与 create 配对精确释放一次，必须早于库卸载 |
| encode | int32(context,input,input_samples,output,output_capacity,output_length) | PCM 到编码；容量/输出长度单位为字节 |
| decode | int32(context,input,input_bytes,output,output_capacity_samples,output_samples) | 编码到 PCM；输出容量/长度单位为 int16 样本 |

输入输出指针只借用到调用返回，不得保留指针、写超容量、阻塞媒体线程或跨 ABI 抛异常；一个上下文只由一个调用线程使用。动态库属于可信本机代码，ABI 检查不构成隔离沙箱。C++ 提供者必须 `extern "C"` 导出并捕获异常。字段布局会受目标平台 ABI 影响，macOS 构建不能直接复制到 Linux。

当前 `--check-codec` 执行独立 G.711 示例往返，输出 `abi_version,samples,max_absolute_error,passed`；通过只表示示例宿主/插件连通。桥接呼叫仍为同编码透明转发，不会因为放置一个插件就获得 Opus、转码、录音或会议。

## 预留 C 协议提供器

[rs_protocol_v1 头文件](../../native/include/rustswitch_protocol.h) 仅定义 `abi_version,struct_size,create,submit,poll_event,destroy`。

| 回调 | 已声明的边界 | 仍未定义或实现 |
|---|---|---|
| create(config,config_len,context) | 显式配置字节长度、创建上下文 | 配置 JSON schema、提供器加载与版本协商 |
| submit(context,command,command_len) | 显式命令字节长度，保存时自行复制 | 命令 schema、每类返回码、重试/去重 |
| poll_event(context,output,capacity,length,timeout_ms) | 返回 1 有事件、0 超时、负值错误；输出不超容量 | 事件 schema、队列溢出、重连补发和并发规则 |
| destroy(context) | 释放提供器资源 | 与其他回调并发时的完整生命周期契约 |

协议提供器未来应拥有 SIP 事务、重传和传输 socket，Go 拥有业务路由与跨腿状态；同一事务不能同时由两个状态机处理。当前没有可调用的 Sofia/PJSIP 实现或对应装载入口，不能据头文件编写一个“已兼容”的客户端。

## JSONL 生命周期事件

日志路径为 `journal.path`。每行一个 UTF-8 JSON 对象，时间为 UTC RFC 3339 格式，可含纳秒。字段：`time` 必有时间、`run_id` 必有本次控制进程运行标识、`kind` 必有事件种类；`call_id` 是可选 A 腿 Call-ID，`reason` 是可选文本。它们不是 FreeSWITCH 的完整 Unique-ID、Event-Name 或 Hangup-Cause 字段。

| kind | 产生时机 |
|---|---|
| controller_started | 控制进程本次初始化完成的启动记录 |
| call_admitted | 通过准入并预留呼叫资源，不表示媒体分配或接通已成功 |
| call_answered | A 腿最终成功响应阶段，不等于已收到 ACK |
| call_established | A 腿成功 ACK 已确认，建立计数增加 |
| call_ended | 首次结束通话；reason 说明原因，媒体异步释放可能稍后完成 |
| transaction_capacity_exhausted | 本地事务表容量不足，reason 标记请求方法 |
| internal_message_error | 生成或解析内部构造信令失败 |

```json
{"time":"2026-09-05T12:00:00Z","run_id":"example-run","call_id":"example-call@local","kind":"call_ended","reason":"normal"}
```

此记录是结构示例，reason 不是标准枚举。日志按约 20 ms 批量 Flush/Sync，`written` 与 `synced` 必须区分。队列满或磁盘失败可能丢事件并保持失败状态，后续准入关闭；启动只修复末尾不完整记录，不恢复通话。没有事件订阅、过滤、回放或 CDR 查询 HTTP 端点；FreeSWITCH 事件标准及验收见 [ESL 与事件对照](../freeswitch-compatibility/02-esl-events.md)。

## 进程 CLI 与退出

控制进程使用 `-config <文件>`，缺省 `config/local.json`；`-check-config` 校验原文件及已保存的管理状态，不绑定网络端口。首次 SIGINT/SIGTERM 停止新准入并等待存量通话，第二次信号强制取消；系统服务等待时间应覆盖最长通话。`POST /v1/drain` 仅停止新准入，不直接退出服务；已经收到退出信号后不能通过 resume 取消退出。

媒体进程使用 `--worker-config <JSON字符串>` 或 `--check-codec <动态库路径>`，两者互斥，另有 `--help` / `--version`。worker 的 shutdown 或控制管道断开会结束媒体；这与 FreeSWITCH 核心会话宿主的生命周期不同。

流量工具 `callbench` 与 `local_bench.py` 面向隔离验收。`--capacity-mode` 只作用于编排器自己启动的实例，临时关闭额外业务保护，结束恢复原开关；启动硬限额和媒体压力保护仍保留。它不是生产通用的无限制接口。命令行完整参数和默认值列于下一节。

## 命令行完整参数快照

下列帮助输出来自本次交付程序；保留参数原名与默认值，中文注释和使用边界见上文。仅执行帮助选项，不启动通话或采集。

### 控制进程

```text
Usage of bin/rustswitch:
  -check-config
    	validate configuration without binding sockets
  -config string
    	configuration file (default "config/local.json")
```

### 媒体进程

```text
RustSwitch Rust media worker; launched by the Go controller

Usage: rustswitch-media [OPTIONS]

Options:
      --worker-config <WORKER_CONFIG>  
      --check-codec <CHECK_CODEC>      
  -h, --help                           Print help
  -V, --version                        Print version
```

### 流量发生器

```text
Usage of bin/callbench:
  -advertise-ip string
    	IPv4 address reachable from RustSwitch (default "127.0.0.1")
  -bind-ip string
    	local IPv4 address for caller and media sockets (default "127.0.0.1")
  -caller-port int
    	caller SIP UDP port; 0 uses an OS ephemeral port
  -calls int
    	number of simultaneous bidirectional calls (default 100)
  -connect-media
    	connect generator UDP sockets to the negotiated server peers; requires dedicated sockets
  -cps int
    	call setup rate (default 10)
  -direct-media
    	diagnostic only: bypass the media server and send RTP directly between simulated endpoints
  -media-network string
    	allowed RustSwitch media network; defaults to server IP/32
  -media-port-end int
    	explicit endpoint UDP port range end
  -media-port-start int
    	explicit endpoint UDP port range start; 0 uses OS ephemeral ports
  -output string
    	optional JSON report path
  -payload int
    	Codec preset payload；完整列表见 GET /v1/codecs
  -phase-slots int
    	phase groups within each 20ms interval (1..1000); used only with --spread (default 20)
  -progress string
    	optional atomic JSON progress path; parent directory must exist
  -seconds int
    	media duration after all calls are established (default 10)
  -senders int
    	traffic generator sender goroutines (default 4)
  -sequential-media-ports
    	use sequential explicit ports instead of deterministic shuffle; useful for hash-collision stress
  -server string
    	RustSwitch SIP address (default "127.0.0.1:5060")
  -socket-groups int
    	endpoint socket groups; 0 gives every call dedicated sockets, positive shares sockets and demultiplexes by SSRC
  -spread
    	spread call packet phases across each 20ms interval; false sends synchronized bursts (default true)
  -uas-listen string
    	SIP UAS address; set RustSwitch upstream to this address (default "127.0.0.1:5070")
```

新增可选参数：`--caller-port` 默认 `0`，继续由系统选择临时主叫 SIP UDP 端口；显式值须在 `1..65535`，由隔离编排器提前规划，绑定失败时不会换用其他端口。

`--progress` 默认关闭。指定文件后，父目录必须已存在，且路径不能与 `--output` 相同；发生器每约 500 ms 及阶段转换时，以同目录临时文件和 Rename 原子发布权限为 `0600` 的 JSON。字段为 `phase`、`requested_calls`、`attempted_calls`、`established_calls`、`setup_failed`、`elapsed_seconds`、`media_elapsed_seconds`。阶段依次为 `starting`、`dialing`、`media`、`teardown`、`finished`；最后一项只在通过验收且完整报告输出后发布，失败或被终止时保留最近阶段。建立数是累计成功数量，尝试数不重复计算 SIP 重传；媒体秒数在停止发送后冻结，不含尾包等待及拆线。快照不包含仍由媒体线程修改的普通收发包计数，是否通过仍以最终报告和退出码为准；写入失败不会伪造完成，也不自动创建目录。

### 本地验收编排

```text
usage: local_bench.py [-h] [--calls CALLS] [--cps CPS] [--seconds SECONDS]
                      [--phase-slots PHASE_SLOTS] [--sequential-ports]
                      [--endpoint-ip ENDPOINT_IP] [--endpoint-connected]
                      [--socket-groups SOCKET_GROUPS]
                      [--endpoint-port-start ENDPOINT_PORT_START]
                      [--endpoint-port-end ENDPOINT_PORT_END]
                      [--port-start PORT_START] [--port-end PORT_END]
                      [--connected] [--workers WORKERS] [--media MEDIA]
                      [--direct] [--capacity-mode] [--burst] [--payload {0,8}]
                      [--work-dir WORK_DIR] [--report REPORT]
                      [--control CONTROL] [--generator GENERATOR]

optional arguments:
  -h, --help            show this help message and exit
  --calls CALLS
  --cps CPS
  --seconds SECONDS
  --phase-slots PHASE_SLOTS
  --sequential-ports
  --endpoint-ip ENDPOINT_IP
                        IPv4 address already configured on this test host
  --endpoint-connected
  --socket-groups SOCKET_GROUPS
  --endpoint-port-start ENDPOINT_PORT_START
  --endpoint-port-end ENDPOINT_PORT_END
  --port-start PORT_START
  --port-end PORT_END
  --connected
  --workers WORKERS
  --media MEDIA
  --direct              diagnostic only: bypass Rust media
  --capacity-mode       本次隔离服务按启动硬上限压测；暂时关闭额外峰值保护，保留媒体健康准入
  --burst               synchronize all RTP packet phases
  --payload {0,8}
  --work-dir WORK_DIR
  --report REPORT
  --control CONTROL
  --generator GENERATOR
```

### Linux只读采集

```text
usage: collect_linux.py [-h] --interface INTERFACE [--admin ADMIN]
                        [--seconds SECONDS] [--interval INTERVAL] --output
                        OUTPUT

optional arguments:
  -h, --help            show this help message and exit
  --interface INTERFACE
  --admin ADMIN
  --seconds SECONDS
  --interval INTERVAL
  --output OUTPUT
```

## 媒体统计字段与分组

下表为 Rust `Stats` 的基础字段。完整当前字段还包括表后的处理、按键和播放分组，由 [媒体 Schema](media-ipc.schema.json) 汇总。HTTP 中 worker 初次采样前可能是空对象；未知不等于零，声明能力却缺少必需统计的样本不得判为健康。

| 字段 | JSON类型 | 含义 |
|---|---|---|
| `user_cpu_seconds` | number | 进程所有线程累计用户态 CPU 秒数。 |
| `system_cpu_seconds` | number | 进程所有线程累计内核态 CPU 秒数。 |
| `peak_resident_bytes` | integer | 进程启动以来的常驻内存峰值，统一换算为字节。 |
| `connected_sockets` | integer | 统计时已经连接对端的 socket 数量。 |
| `peer_changed_drops` | integer | 对端变更时清理发送队列而丢弃的包数。 |
| `active_calls` | integer | 当前已分配的媒体会话数，不限定 SIP 是否已经接通。 |
| `available_blocks` | integer | 当前可尝试绑定的四端口块数，不含仍在隔离期的块。 |
| `receive_syscalls` | integer | 包括 WouldBlock 在内的接收调用总数。 |
| `receive_batches` | integer | 成功返回的接收批次数，可结合 rx_packets 估算实际批次大小。 |
| `max_receive_batch` | integer | 启动以来单次接收到的最大包数，不能替代平均批次指标。 |
| `rx_packets` | integer | 从 socket 读取的报文总数，包含之后被校验拒绝的报文。 |
| `rx_bytes` | integer | 内核报告的数据报字节总数，Linux 上可包括超出缓冲的原始长度。 |
| `tx_packets` | integer | 被发送调用完整接受的包数，不能证明已越过网卡或到达对端。 |
| `tx_bytes` | integer | 与 tx_packets 对应的累计发送字节。 |
| `invalid_packets` | integer | 长度、RTP/RTCP 结构或协商负载类型校验失败的包数。 |
| `source_rejected` | integer | 源地址不匹配的拒绝数；connected UDP 被内核先过滤的包不会计入。 |
| `peer_not_ready` | integer | B 腿尚未设置时到达、无法转发的包数。 |
| `rate_limited` | integer | 超出对应 RTP/RTCP 令牌桶额度的包数。 |
| `send_errors` | integer | 非 WouldBlock 发送失败或异常发送长度次数。 |
| `receive_errors` | integer | 排除 WouldBlock/Interrupted 的接收错误次数。 |
| `send_queue_drops` | integer | 发送队列达到固定长度后拒绝的新包数。 |
| `send_expired` | integer | 重试前已经超过短发送期限的包数。 |
| `socket_rx_drops` | integer | 从 Linux SO_RXQ_OVFL 辅助字段累计得到的 socket 接收溢出增量。 |
| `socket_drop_counter_supported` | boolean | 平台是否支持上述计数；不支持时零值不能解释为不存在内核丢包。 |
| `playback_replaced_packets` | integer | relay 播放有意替代的主音频/CN，不是拥塞丢包。 |
| `dtmf_send_packets` | integer | 实际成功提交 UDP 的事件包，包含重复结束包。 |
| `dtmf_send_errors` | integer | 主动事件发送或调度失败。 |
| `dtmf_send_cancelled_digits` | integer | 释放时尚未完整发送的数字。 |
| `dtmf_send_active` | integer | 仍有待发数字的本地发送器数，上限 64。 |

`processed_*` 基础 22 字段逐项见 [实时图统计](processed-media-reference.md)，本地新增 5 个 `processed_local_*` 字段见 [本地统计](processed-local-reference.md)。PCM TX 复用编码/发送指标，逐轮 8k 样本进度使用 `pcm_turn_state`/RVR1，不把下行样本累加到 RX consumed/energy。

1.13的 `rx_active_subscriptions`、`rx_queued_events`、`rx_observation_storage_bytes`、`rx_observation_failed` 都是当前资源量，每次stats在媒体单写者上采集；最后一项不是累计丢包。存储字节只计算Graph观察队列，不含导出/系统/Go队列。逐订阅累计使用rx_state；Rust submitted、Go delivered和ASR实际使用不同，不能互相代填。历史Worker缺字段保留未知，不自动补0。

1.12 的 PCM `processed_send_deadline_misses` 包含发送前超期拒发和 UDP 已成功返回但返回时迟到至少 20ms 两类。后一类必须保留实际 sent_samples，再取消剩余队列；lateness max/8 桶记录发送前核对，不包含发送调用返回耗时。该指标不是网络丢包率，详见 [时间与完成语义](pcm-turn-reference.md)。
