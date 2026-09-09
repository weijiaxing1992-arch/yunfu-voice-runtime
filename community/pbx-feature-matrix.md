# FreeSWITCH / SIP PBX 功能矩阵：RustSwitch 的实现与验收边界

更新日期：2026-09-09。本页面向正在比较 FreeSWITCH 替代方案、SIP PBX、呼叫中心和语音智能体内核的团队，将常见依赖整理为 **45 项评估能力**。它不是原版全部模块清单，也不是兼容率或本轮测试结果。

固定对照为 FreeSWITCH **1.11.3 / `ef32e205295e29f034f1453ad245ba5efb07b94a`**。RustSwitch 主工程为 [`source/main/`](../source/main/)，独立 ASR 候选为 [`source/asr-candidate/`](../source/asr-candidate/)。官方链接用于说明原版概念，查阅日期为 2026-09-09；原版实际能力还取决于固定构建、模块装载及配置。

## 状态阅读方法

| 标记 | 本页含义 |
| --- | --- |
| 受限实现 | 有运行代码，但仅覆盖表中边界；业务迁移需逐场景复验 |
| 仅透传 | 可以在指定 SDP/格式下转发，不代表能解码、转码或用于 AI 音频处理 |
| 离线 SDK | 独立编解码/帧处理入口，不代表实时 SIP 音频图已接入 |
| 自有接口 | 提供 RustSwitch 自身能力，不兼容原版同类协议或模块 ABI |
| 未实现 | 缺少对应运行能力；配置导出、目录占位或路线图不算实现 |

不同标签可以同时出现。本页不授予“绿色已验证”；有效证据与历史局限见[当前能力与验证状态](../08-当前能力与验证状态.md)。完整机器目录仍以[全部逐项状态](../backlog/FreeSWITCH-全部逐项状态.json)为准；业务迁移步骤见[迁移评估指南](freeswitch-migration.md)。

## SIP、线路与终端接入（01—10）

FreeSWITCH 的 SIP endpoint 由 Sofia profile 与 gateway 等配置组织；注册客户端、终端注册服务和一次呼叫的路由是不同职责。[官方 Sofia profile](https://developer.signalwire.com/freeswitch/users-and-endpoints/sip-profiles/)、[gateway 与中继注册](https://developer.signalwire.com/freeswitch/users-and-endpoints/gateways/)、[核心概念](https://developer.signalwire.com/freeswitch/foundations/introduction/)

| 编号 / FreeSWITCH 概念 | RustSwitch 当前状态 | 源码或接口合同 | 迁移验收要点 |
| --- | --- | --- | --- |
| 01 · `mod_sofia` / SIP profile / UDP、TCP、TLS | **受限实现**：可信 IPv4 来源、固定上游，TCP/TLS 显式启用；不提供完整多 profile 管理 | [SIP 与可靠传输](../source/main/docs/api/protocol-reference.md) | 完整呼叫分帧、连接归属、背压、TLS 链和名称验证；OPTIONS 成功不能替代呼叫验收 |
| 02 · gateway REGISTER / Digest / refresh | **受限实现**：唯一上游注册客户端、INVITE 认证、刷新及注销；没有多网关池 | [注册合同](../source/main/docs/api/trunk-registration.md)、[注册代码](../source/main/control/internal/server/trunk_registration.go) | AOR/认证账户区别、挑战失败、423、刷新超时、退避、退出注销及真实回呼 |
| 03 · directory / 分机账户 / registrar | **未实现**：入站 REGISTER 返回不支持；本地号码列表不是账户与位置数据库 | [协议合同](../source/main/docs/api/protocol-reference.md) | 多终端注册、认证、Contact 更新、失效、查找和呼入；依赖这些能力的业务保留原服务 |
| 04 · SIP INVITE / bridge / A、B 通道 | **受限实现**：固定上游双腿桥接或本地 A 腿，独立 Call-ID 与标签 | [协议合同](../source/main/docs/api/protocol-reference.md)、[呼叫代码](../source/main/control/internal/server/calls.go) | 双向媒体、ACK、双方 BYE、重复 INVITE、CANCEL 与迟到 200、清理后的资源 |
| 05 · `originate` / dial string / 分叉与多线路路由 | **未实现**完整入口；固定上游转发不能替代主动外呼 API、顺序/并行拨号和失败切换 | [ESL 范围](../source/main/docs/api/esl-reference.md)、[呼叫接入](../source/main/docs/api/ai-calling-reference.md) | 逐项盘点 dial string、变量、超时、失败原因、取消赢家/输家及费用副作用 |
| 06 · 180 / 183 / early media | **受限实现**：桥接可处理上游振铃、183 与最终响应；不提供全部预应答应用语义 | [SIP 状态与顺序](../source/main/docs/api/protocol-reference.md) | 早期 SDP、回铃/音频方向、183 后取消、最终失败和媒体释放 |
| 07 · hold / unhold / transfer / REFER / re-INVITE | **未实现**：re-INVITE 返回 501；保持、恢复、转接和重协商未提供 | [明确拒绝范围](../source/main/docs/api/protocol-reference.md) | 保持与恢复 SDP、盲转/咨询转、转接失败回原通道、旧媒体终止；当前应记阻塞 |
| 08 · PRACK / 100rel / UPDATE / Session Timers / 订阅消息 | **未实现**相关扩展；未支持方法或 Require、Session-Expires 等被拒绝 | [SIP 错误与扩展](../source/main/docs/api/protocol-reference.md) | 实际运营商是否强制扩展；不能用关闭参考端必要功能来扩大兼容结论 |
| 09 · SIP 地址与路由 / DNS、IPv6、Route、NAT | **受限实现**：实际目的地是固定 IPv4；无 DNS/SRV、IPv6、复杂 Route/Record-Route 或 NAT 学习 | [地址与来源合同](../source/main/docs/api/protocol-reference.md) | 发布地址、Contact、可信网段、SBC 路由、重定向与媒体来源；URI 中域名不等于 DNS 路由 |
| 10 · SIP WebRTC / Verto / WSS / ICE / SRTP | **未实现**；页面“小电话”是 SIP/媒体测试，不是浏览器麦克风软电话 | [协议范围](../source/main/docs/api/protocol-reference.md)、[测试定义](../source/main/docs/api/testing-reference.md) | 浏览器信令、ICE、DTLS-SRTP、证书和 NAT 需另建完整实现与互通矩阵 |

WebRTC over SIP 与 Verto 的信令入口不同，均不能由普通 SIP/TLS 监听自动获得。[官方 SIP WebRTC](https://developer.signalwire.com/freeswitch/users-and-endpoints/webrtc-sip/)、[Verto](https://developer.signalwire.com/freeswitch/users-and-endpoints/verto/)

## ESL、拨号计划与扩展接口（11—20）

FreeSWITCH Event Socket 有入站与出站模式；API、后台作业和事件传输应分别检查。XML、变量展开及动态 HTTP 配置又是独立接口面。[官方 Event Socket](https://developer.signalwire.com/freeswitch/integration/event-socket/)、[XML 拨号计划](https://developer.signalwire.com/freeswitch/dialplan/xml/)、[通道变量](https://developer.signalwire.com/freeswitch/reference/channel-variables/)、[XML Curl / HTTAPI](https://developer.signalwire.com/freeswitch/integration/xml-curl/)

| 编号 / FreeSWITCH 概念 | RustSwitch 当前状态 | 源码或接口合同 | 迁移验收要点 |
| --- | --- | --- | --- |
| 11 · 入站 `mod_event_socket` / `fs_cli -x` | **受限实现**：默认关闭，回环地址和私有密码文件，有限单命令执行；无完整交互控制台 | [ESL 合同](../source/main/docs/api/esl-reference.md)、[ESL 服务](../source/main/control/internal/esl/server.go) | 认证、UTF-8 字节长度、拆包/粘包、未知命令、实际 stdout 错误；客户端退出 0 不等于成功 |
| 12 · 出站 ESL / `socket` / `myevents` | **未实现**完整出站控制流程；不能直接接管原版 outbound ESL 应用 | [ESL 差异](../source/main/docs/api/esl-reference.md) | connect 握手、单通道控制、断线后的应用行为、linger/resume 等全部另验 |
| 13 · `api` / `bgapi` / `BACKGROUND_JOB` | **受限实现**：仅执行已有 API 子集；固定后台资源预算；Job-UUID 关联作业 | [API 合同](../source/main/docs/api/esl-reference.md)、[执行代码](../source/main/control/internal/server/esl_api.go) | 受理与完成区别、原始正文、失败、重试副作用、队列满、断开连接不自动取消 |
| 14 · `event plain/json/xml` / `filter` / 通道事件 | **受限实现**：实际发布有限生命周期、DTMF、播放和作业事件；无全部 CUSTOM 与完整事件字段 | [事件合同](../source/main/docs/api/event-lifecycle-reference.md)、[ESL 过滤](../source/main/docs/api/esl-reference.md) | 每个依赖事件的实际发出、顺序、过滤语义和慢消费者；订阅名称被接受不等于事件已实现 |
| 15 · `uuid_getvar` / `uuid_setvar` / `uuid_setvar_multi` / `export` | **受限实现**：通道腿内有界变量；不提供通用展开、继承及副作用；多项写入不是原子事务 | [变量合同](../source/main/docs/api/variables-reference.md) | A/B 腿隔离、缺失值、保护字段、部分成功、`${...}` 与继承依赖 |
| 16 · `uuid_exists` / `uuid_dump` / `show` / `status` | **受限实现 / 自有输出**：有限 UUID 快照、API 目录与运行状态；不是原版所有字段和格式 | [快照合同](../source/main/docs/api/channel-snapshot-reference.md)、[ESL API](../source/main/docs/api/esl-reference.md) | 每个字段的来源、时点、空值、存在/不存在通道；不得用自有 status 冒充原版输出 |
| 17 · XML context / extension / condition / action | **受限实现**：启动冻结；单一 destination_number RE2 条件及受限动作；无完整预处理、展开与重载 | [拨号计划合同](../source/main/docs/api/dialplan-reference.md)、[夹具](../source/main/tests/fixtures/native-dialplan.xml) | 首次命中、未命中、非法 XML、PCRE 差异、动作顺序；导出草稿与运行配置分别核对 |
| 18 · `mod_xml_curl` / HTTAPI / `reloadxml` | **未实现**相应动态运行行为；后台配置编辑和导出不是 HTTP 动态拨号计划 | [拨号计划边界](../source/main/docs/api/dialplan-reference.md)、[未完成清单](../13-项目未完成清单.md) | 动态目录/路由请求字段、超时、缓存、后端失败和热更新均需另实现 |
| 19 · Lua / JavaScript 等脚本宿主 | **未实现**原版脚本运行环境和 session API；不能直接复制 PBX 业务脚本运行 | [未完成清单](../13-项目未完成清单.md)、[原版目录](../backlog/FreeSWITCH-全部逐项状态.json) | 依赖模块、脚本函数、阻塞、取消、线程归属和资源隔离；优先识别必须保留的脚本 |
| 20 · `mod_*.so` / 模块注册 / C 接口扩展 | **自有接口 / 部分预留**：自定义编解码 C ABI；协议 ABI 仍预留，不能直接加载 FreeSWITCH 模块 | [协议与 ABI](../source/main/docs/api/protocol-reference.md)、[C 头文件](../source/main/native/include/) | ABI 版本、所有权、错误、线程与生命周期；头文件存在不证明原模块二进制兼容 |

FreeSWITCH 应用和模块的运行依赖可从[官方模块管理](https://developer.signalwire.com/freeswitch/configuration/module-loading/)及[应用参考](https://developer.signalwire.com/freeswitch/dialplan/dptools/)进一步核对。迁移脚本时应识别它调用的具体能力，不能只比较脚本语言名称。

## PBX、IVR 与呼叫中心业务（21—32）

播放、收号、转接和录音等 FreeSWITCH 应用有各自参数与通道语义；RustSwitch 的有限应用集合不能代表全部 `mod_dptools`。[官方拨号计划应用参考](https://developer.signalwire.com/freeswitch/dialplan/dptools/)

| 编号 / FreeSWITCH 概念 | RustSwitch 当前状态 | 源码或接口合同 | 迁移验收要点 |
| --- | --- | --- | --- |
| 21 · `answer` / `set` / `unset` / `sleep` / `park` / `hangup` | **受限实现**：与 `read`、`playback` 合为八种 sendmsg 执行动作；参数与前置状态有限 | [IVR / 应用合同](../source/main/docs/api/ivr-reference.md)、[应用代码](../source/main/control/internal/server/esl_applications.go) | ACK 前后资格、队列和执行事件、event-lock、挂机竞态；受理回复不能代替动作完成 |
| 22 · `playback` / 提示音 / `uuid_break` | **受限实现**：可信播放根内的有限 WAV/提示音和停止逻辑；无任意 URL、格式、循环、seek | [文件播放](../source/main/docs/api/file-playback-reference.md)、[IVR](../source/main/docs/api/ivr-reference.md) | 8 kHz 单声道格式、文件限制、听到的内容、停止确认、DTMF 中断与错误清理 |
| 23 · RTP telephone-event / `uuid_send_dtmf` | **受限实现**：接收完整按键去重；发送限已 ACK 本地 A 腿；无桥接通话发送全语义 | [音频辅助事件](../source/main/docs/api/audio-reference.md)、[发送合同](../source/main/docs/api/dtmf-send-reference.md) | 时钟/PT/事件集、重复尾包、结束丢包、发送节奏、实际对端收号与队列回收 |
| 24 · SIP INFO DTMF / 带内 DTMF | **受限实现 / 未实现**：显式 INFO 模式可接收合法对话内按键；未实现 INFO 发送和带内检测 | [IVR DTMF](../source/main/docs/api/ivr-reference.md)、[SIP INFO](../source/main/docs/api/protocol-reference.md) | 来源、标签、CSeq、正文、Duration、重传去重；不能重复统计 INFO 与 RTP |
| 25 · `read` / `play_and_get_digits` / 多级 IVR 菜单 | **受限实现** `read`；完整菜单树、重试分支、语言短语及动态路由未提供 | [IVR 合同](../source/main/docs/api/ivr-reference.md)、[XML 限制](../source/main/docs/api/dialplan-reference.md) | 首位/位间超时、最少/最多位、终止符、错误播放、`read_result`；媒体异常不记为用户沉默 |
| 26 · valet parking / pickup / intercept / eavesdrop | **未实现**完整驻留取回、代接与监听；有限 `park` 不等于驻留业务 | [IVR 范围](../source/main/docs/api/ivr-reference.md)、[未完成清单](../13-项目未完成清单.md) | 驻留槽、归属、并发取回、权限、监听腿与录音边界；当前保留原系统 |
| 27 · `mod_conference` / 多方会议 | **未实现**混音会议、成员控制和会议事件 | [媒体范围](../source/main/docs/api/protocol-reference.md)、[未完成清单](../13-项目未完成清单.md) | 混音、成员进退、静音、主持、DTMF 控制、录音和容量分别验收 |
| 28 · `mod_fifo` / `mod_callcenter` / ACD | **未实现**排队、坐席、技能/层级分配与队列事件；准入限流不是业务队列 | [保护实现](../source/main/control/internal/server/guard.go)、[未完成清单](../13-项目未完成清单.md) | 排队顺序、坐席状态、超时、放弃、重入、转人工、数据库状态一致性 |
| 29 · `mod_voicemail` / 语音信箱 | **未实现**信箱、留言存储、收听与通知 | [未完成清单](../13-项目未完成清单.md)、[原版逐项目录](../backlog/FreeSWITCH-全部逐项状态.json) | 信箱认证、留言完成/中断、存储失败、通知及清理，不以播放能力替代 |
| 30 · `record` / `record_session` / 双轨录音 | **未实现**完整实时录音；RX/PCM 流接口不是录音文件服务 | [RX 合同](../source/main/docs/api/rx-stream-reference.md)、[长期验收](../source/main/docs/api/voice-runtime-v1-acceptance.md) | 两腿时间对齐、缺口标记、暂停/恢复、磁盘背压、挂机落盘和保存策略 |
| 31 · CDR / `mod_cdr_csv` / XML CDR / 数据库 | **自有接口**：有生命周期 JSONL；没有完整原版 CDR 格式、账务和全部存储后端 | [日志合同](../source/main/docs/api/protocol-reference.md)、[日志源码](../source/main/control/internal/journal/) | 每腿/每呼叫关联、应答/结束时点、失败原因、重复写入与费用核对 |
| 32 · Fax / T.38 / 视频业务 | **未实现**传真、T.38、多媒体流和视频处理 | [SDP 范围](../source/main/docs/api/protocol-reference.md)、[未完成清单](../13-项目未完成清单.md) | 传真协商与页结果、媒体切换、视频编码和多流必须独立实现；音频 RTP 透传不能证明 |

对应原版业务可进一步查阅[会议](https://developer.signalwire.com/freeswitch/applications/conferencing/)、[FIFO 与 Call Center](https://developer.signalwire.com/freeswitch/applications/call-queues/)、[语音信箱](https://developer.signalwire.com/freeswitch/applications/voicemail/)、[CDR](https://developer.signalwire.com/freeswitch/integration/cdr/)和[传真 T.38](https://developer.signalwire.com/freeswitch/applications/fax/)。这些原版功能的存在不意味着当前 RustSwitch 已经具备同名模块。

## 编解码、RTP 与语音智能体（33—43）

FreeSWITCH 的 codec 列表与实际装载、协商和转码路径相关。迁移时应分开核对“协商接受、同格式透传、解码、编码、实时转码、业务音频接入”。[官方 codec 表](https://developer.signalwire.com/freeswitch/reference/codec-table/)、[媒体与 codec 协商](https://developer.signalwire.com/freeswitch/media-and-codecs/codecs/)

| 编号 / FreeSWITCH 概念 | RustSwitch 当前状态 | 源码或接口合同 | 迁移验收要点 |
| --- | --- | --- | --- |
| 33 · G.711 PCMA / PCMU | **受限实时实现**：relay 同格式转发；g711 图支持 8 kHz/mono/20 ms 双腿或本地处理，可 PCMA↔PCMU | [音频合同](../source/main/docs/api/audio-reference.md)、[实时图](../source/main/docs/api/processed-media-reference.md) | 协商、真实样本与异律转换、两腿独立 PT、包长、乱序和尾延迟；不以 SDK 自测替代通话 |
| 34 · G.722 高清语音 | **仅透传 + 可选离线 SDK**；未接入实时解码/转码图 | [G.722 时钟与后端](../source/main/docs/api/audio-reference.md) | 音频 16 kHz 与 RTP 8 kHz 分开；20 ms 时间戳增量 160，后端探测和对端听感分别验收 |
| 35 · Opus | **仅透传 + 可选离线 SDK**；未接入实时 Opus 图，不代表 WebRTC 可用 | [Opus 范围](../source/main/docs/api/audio-reference.md) | RTP 时钟 48 kHz、fmtp/ptime、真实编码载荷、可选库装载；FEC 与动态码率另验 |
| 36 · G.729 / G.729A | **仅透传**：有受限格式协商；无 SDK 编解码后端 | [音频合同](../source/main/docs/api/audio-reference.md) | 两腿相同编码/PT及兼容参数、帧和时间戳；需要 ASR 或转码时不能直接使用载荷 |
| 37 · G.726 / AAL2-G726 | **仅透传**：16/24/32/40 位率族分别识别；不转换位打包方式 | [G.726 格式表](../source/main/docs/api/audio-reference.md) | 位率、命名与 packing 完全匹配；SIP 接通不证明对端解码正确 |
| 38 · L16 / 内部 PCM 音频帧 | **分层实现**：L16 仅透传、使用网络大端；离线 AudioFrame SDK 内部为单声道 i16 样本，提供 S16LE 字节接口，支持 8/16/24/48 kHz、10/20 ms | [帧与采样率合同](../source/main/docs/api/audio-reference.md) | 大小端、样本数、显式重采样、媒体时间；实时 g711 图仍为 8 kHz，不能推断统一实时 16 kHz |
| 39 · AMR-NB / AMR-WB / EVS / iLBC / G.723.1 / GSM | **未实现**：当前协商明确拒绝；移动网与传统格式仍为能力缺口 | [当前拒绝格式](../source/main/docs/api/audio-reference.md)、[未完成清单](../13-项目未完成清单.md) | 先确认 SBC 是否已转成 G.711；需原生处理时另验后端、RTP 打包、互通及音质 |
| 40 · RTP / RTCP / jitter buffer | **受限实现**：IPv4 单音频流、独立 RTP/RTCP 端口、来源与包结构检查；G.711 图有有界抖动处理 | [RTP/RTCP 合同](../source/main/docs/api/protocol-reference.md)、[处理图](../source/main/docs/api/processed-media-reference.md) | 包丢失/乱序/重复、序号/时间戳回绕、RTCP、端口隔离与清理；无 RTCP mux、安全媒体和多流 |
| 41 · CN / DTX / PLC / FEC / VAD | **部分实现**：区分丢包/CN，SDK PLC 与有限历史补偿，G.711 实时有限补偿；无 CN 合成、完整 FEC 或自动 VAD | [辅助媒体与丢包语义](../source/main/docs/api/audio-reference.md) | 真实音频/补偿/缺失来源、补偿上限、静音与丢包区别；不宣称零丢包或 ASR 端点判断已完成 |
| 42 · `detect_speech` / `speak` / ASR、TTS、Voice Agent | **未实现完整主链路**：ASR1 候选有模拟代理与来源/时效检查，尚未合并；无完整真实供应商对话闭环 | [ASR 候选合同](../candidate/asr-stream-reference.md)、[长期验收](../source/main/docs/api/voice-runtime-v1-acceptance.md) | 真实识别/合成质量、LLM 轮次、final 晚到、超时取消、转人工、双工与长期运行；mock 不算真实识别 |
| 43 · 媒体回调 / PCM 流 / 打断控制 | **自有接口**：本地 A 腿 RX、内部 RXS2、私有 Unix RVA1 下行及 PCM 轮次；不是原版媒体回调 ABI | [RX](../source/main/docs/api/rx-stream-reference.md)、[RVA1](../source/main/docs/api/rva1-reference.md)、[轮次](../source/main/docs/api/pcm-turn-reference.md) | ACK/local/G.711/同代资格、令牌、期限、背压、挂机撤销、旧轮拒绝；调用方供 PCM 不等于已接入 TTS |

原版语音识别、合成和播放检测应用的概念见[官方应用参考](https://developer.signalwire.com/freeswitch/dialplan/dptools/)。RustSwitch 以音频流接口为后续集成基础，并未提供这些原版应用的完整行为兼容。

## 运行保护与故障恢复（44—45）

| 编号 / FreeSWITCH 对照领域 | RustSwitch 当前状态 | 源码或接口合同 | 迁移验收要点 |
| --- | --- | --- | --- |
| 44 · 会话容量 / 呼叫速率 / 管理观测 / 压测 | **自有实现，容量待验收**：并发、建立中和 CPS 保护，以及隔离发生器/管理页面；超限拒绝不等于排队缓接 | [保护代码](../source/main/control/internal/server/guard.go)、[测试合同](../source/main/docs/api/testing-reference.md)、[运维压测](../07-运维与压测说明.md) | 同配置参考测试，既有通话质量、最忙分片、CPU/线程/队列上界、完整发生器证据；5,000/10,000 路目标值不是容量证明 |
| 45 · 排空 / 进程故障 / 部署回退 / 活动通话恢复 | **部分实现**：有排空、超时与资源回收；worker 故障会影响所属通话，无活动通话跨机无损恢复 | [稳定性合同](../source/main/docs/api/stability-reference.md)、[诊断合同](../source/main/docs/api/runtime-diagnostics-reference.md)、[迁移回退](freeswitch-migration.md) | 故障影响面、停止新呼叫、在途清理、重启与回退验证；不承诺任意故障零服务损失 |

## 如何推进一个缺口

选定业务依赖后，关联原版逐项 ID、固定原版版本与配置、最小复现、RustSwitch 实现位置、需要比较的输入/输出，以及清理和容量约束。先实现可运行的用例，再修复代码并生成匹配当前快照的证据；配置文件数量、声明数量或历史绿色数量不能代替这个过程。

本页覆盖评估中常见的 45 个组合领域，不能替代完整 [3,999 条目录的状态说明](../08-当前能力与验证状态.md)。不同条目粒度和状态维度不适合直接计算兼容百分比。社区报告问题与贡献请遵循[社区指南](../COMMUNITY.md)，并关联[未完成清单](../13-项目未完成清单.md)；新增验证不会通过编辑本页自动改变管理后台状态。
