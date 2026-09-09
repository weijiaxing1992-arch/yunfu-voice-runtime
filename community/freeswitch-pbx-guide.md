# FreeSWITCH 与 PBX 指南：电话系统、RustSwitch 与语音智能体

[中文](freeswitch-pbx-guide.md) · [English](freeswitch-pbx-guide.en.md) · [项目首页](../README.md) · [文档中心](../DOCUMENTATION.md)

更新日期：**2026-09-09**。本文面向评估开源 PBX、IP PBX、SIP 电话平台和 Voice Agent 的开发、集成与运维人员，解释常见概念、配置关系、测试方法，以及它们在 RustSwitch 中的实际范围。

FreeSWITCH 外部事实参考 SignalWire 官方文档，链接随相关内容列出；本项目状态以主接口 **1.13.0**、[协议合同](../05-协议与SDK文档.md)和[未完成清单](../13-项目未完成清单.md)为准。兼容参考仍固定为 **FreeSWITCH 1.11.3**，并非把持续更新的官方文档当作该固定版本已运行验证的证据。

> **RustSwitch 是面向语音智能体的 FreeSWITCH 替代方向，当前为开发预览。** 本文介绍的 PBX 功能不自动成为本项目已实现功能；完整 PBX、全协议兼容、真实 ASR–LLM–TTS 闭环及 5000/10000 路完整验收尚未完成。

## 1. 先确定需要解决的电话业务

PBX（Private Branch Exchange，专用交换系统）服务于组织内部及对外电话；IP PBX 使用 IP 电话技术承担这些业务。实际选型通常涉及分机、号码路由、权限、总机、转接、留言和运维。FreeSWITCH 本身是可编程通信交换平台，其默认配置提供 SIP PBX 起点，而不是只提供一个通话转发进程。[官方核心概念](https://developer.signalwire.com/freeswitch/foundations/introduction/)

不要从“支持多少协议”开始，先列出业务不可缺少的通话流程：

| 业务 | 首先明确 | RustSwitch 当前评估重点 |
|---|---|---|
| 企业办公电话 | 分机注册、内外线权限、转接、留言、终端管理 | 完整分机 PBX 尚未实现，应先核对缺口 |
| 客服与呼叫中心 | 排队、坐席状态、转人工、录音、话单 | 不把准入限流等同于坐席排队系统 |
| 自动语音菜单 | 接听、提示音、按键、超时、分支、挂机 | 已有受限本地 IVR/read/playback，可逐项测试 |
| 电话语音智能体 | 持续收音、边听边说、轮次取消、模型超时 | G.711/PCM/RX 基础已有，真实模型链路待集成 |
| SIP 中继媒体服务 | 信令互通、音频格式、来源校验、资源预算 | 从固定线路与同格式媒体路径开始 |

这些是按本项目现状给出的评估步骤，不是其他产品的排名或采购结论。若现网必须依赖尚未实现的能力，应保留现有服务，先隔离验证候选路径。

## 2. PBX、Media Server 与 SBC 的职责

一个系统可以组合多个角色，但验收时应分别观察它们：

| 角色 | 需要解决的问题 | 本项目边界 |
|---|---|---|
| PBX / IP PBX | 谁能打电话、号码去向、分机业务如何执行 | 仅有部分线路与应用能力，不是完整企业电话套件 |
| SIP 控制面 | 建立/结束对话、认证、路由、事务与事件 | Go 提供受限 SIP 与呼叫状态管理 |
| Media Server（媒体服务器） | 收发音频、播放、收号、转码、混音或录音 | Rust 主线以转发、G.711 实时图和 PCM/RX 为基础 |
| [SBC（会话边界控制器）](https://signalwire.com/blog/metaswitch-end-of-life) | 在网络边界组织信令、媒体和安全策略 | 双腿桥接、白名单、限流不足以证明完整 SBC 能力 |
| Voice Agent 应用 | 识别、业务决策、生成语音、管理对话轮次 | 真实供应商和完整业务编排仍待接入 |

FreeSWITCH 区分让媒体经过处理、在服务端代理转发以及让端点直接交换媒体的模式；因此仅统计“呼叫连接数”不能说明服务器实际处理了多少音频。[官方媒体处理](https://developer.signalwire.com/freeswitch/media-and-codecs/handling/)

可把以下链路作为**职责示意**，不是本预览已集成拓扑：

```text
电话 / 软电话 / 运营商 SIP Trunk
                    │
          网络接入与安全边界
                    │
     呼叫路由与状态控制 ── 管理、指标、业务事件
                    │
       RTP 音频与实时处理
                    │
      ASR → 业务 / LLM → TTS
```

RustSwitch 的真实进程、端口和候选连接见[部署拓扑](../02-项目拓扑图.md)与[软件架构](../03-软件架构图.md)。图中的外部 AI 不提供现成账号、模型或计费服务。

## 3. SIP Trunk、线路注册与分机注册

SIP Trunk 是与运营商或其他电话系统交换呼叫的逻辑线路。在 FreeSWITCH 中，gateway 配置归属于 Sofia profile，可选择向上游注册，也可使用不发送 REGISTER 的对等线路。是否需要注册取决于线路接入合同。[官方网关与中继注册](https://developer.signalwire.com/freeswitch/users-and-endpoints/gateways/)

**向上游注册**和**接收分机注册**是两个方向：前者由本服务充当客户端；后者需要保存终端绑定、验证身份并把来电定位到终端。FreeSWITCH 的 Sofia profile 与用户目录参与这些工作。[官方 SIP Profiles](https://developer.signalwire.com/freeswitch/users-and-endpoints/sip-profiles/)

RustSwitch 当前只有单固定上游 REGISTER/Digest 客户端；实际目的地来自 `sip.upstream`，不是完整多 gateway 管理，也不是让软电话注册进来的 registrar。配置重启要求、URI 限制和私有凭据文件规则见[本项目线路注册合同](../reference/docs/api/trunk-registration.md)。

接入一条真实线路时，至少记录下面的项目：

| 项目 | 验证问题 |
|---|---|
| 注册与呼叫认证 | REGISTER 成功后，INVITE 是否还需要单独的认证挑战？ |
| 身份与路由 | 认证用户名、AOR、主叫显示、被叫格式是否各自正确？ |
| 可达性 | Contact、SIP 宣告地址和媒体地址是否能从对端访问？ |
| 失效与刷新 | 过期、认证失败、连接断开、超时后是否恢复且不会无限重试？ |
| 呼叫方向 | 呼入与呼出是否都经过真实端点验证？ |
| 拆线与释放 | CANCEL、BYE、未收到 ACK 和异常断开是否结束正确资源？ |

注册成功只说明注册事务达到相应结果，不证明号码路由、主叫权限、双向语音或长期可用性。

## 4. IVR、DTMF、ESL 与 XML 如何配合

### IVR：播放提示之后，还需要正确处理输入

IVR（Interactive Voice Response）把提示、输入和分支动作连接起来。FreeSWITCH 提供 XML 菜单与 `ivr` 应用，可组织菜单、子菜单和动作。[官方 IVR 菜单](https://developer.signalwire.com/freeswitch/applications/ivr-menus/)

RustSwitch 已实现本地通道上的受限播放、`read` 收号及若干基础应用。验收应覆盖第一位超时、位间超时、终止键、重复输入、提示音被打断、播放失败、挂机取消和结果变量；完整菜单树语义不能仅凭 `read` 成功推断。[本项目 IVR 合同](../reference/docs/api/ivr-reference.md)

### DTMF：电话按键与语音音频分别核验

常见按键传递方式包括 RTP `telephone-event`、SIP INFO 和音频内双音。FreeSWITCH 文档仍使用 `rfc2833` 配置名，并说明该电话事件规范由 RFC 4733 取代；RTP 电话事件不能与音频内按键检测混为一谈。[官方术语表](https://developer.signalwire.com/freeswitch/reference/glossary/)

本项目支持合同限定的 RTP 电话事件接收和本地发送，以及显式启用的 INFO 收号；没有因此实现音频内 DTMF 检测或所有 INFO 变体。按键发包完成也不证明远端 IVR 接受了按键。[DTMF 发送边界](../reference/docs/api/dtmf-send-reference.md)

### ESL：命令回复与异步事件分开处理

FreeSWITCH 的 `mod_event_socket` 提供 TCP 控制/事件接口，分为外部程序连入的 inbound 模式，以及 FreeSWITCH 按通话连接外部程序的 outbound 模式。[官方 Event Socket](https://developer.signalwire.com/freeswitch/integration/event-socket/)

RustSwitch 当前是显式启用的入站 ESL 子集。客户端需要正确处理鉴权、字节长度、分片/粘包以及混入命令响应之间的事件；`+OK` 仅表示规定阶段的受理，播放与收号仍应关联完成事件、结果字段和真实媒体。[本项目 ESL 合同](../reference/docs/api/esl-reference.md)

### XML：文件能编辑，不代表应用能执行

FreeSWITCH XML Dialplan 使用 context、extension、condition、action 组织路由与应用执行；变量和求值时机也是语义的一部分。[官方 XML Dialplan](https://developer.signalwire.com/freeswitch/dialplan/xml/)

RustSwitch 的配置工作区、XML 导出和启动时冻结的运行 Dialplan 子集需要分别看待。导出的 `conference`、`voicemail` 或其他模块配置不表示对应模块已经运行。[本项目拨号计划合同](../reference/docs/api/dialplan-reference.md)

## 5. 常见 PBX 功能与 RustSwitch 的现状映射

下表将官方功能入口与本项目状态放在同一行。它是阅读索引，不是逐条互通认证；本项目细节统一以[当前状态](../08-当前能力与验证状态.md)和[工作清单](../13-项目未完成清单.md)为准。

| PBX 功能 | FreeSWITCH 官方入口 | RustSwitch 当前范围 / 缺口 |
|---|---|---|
| 分机、用户目录 | [核心配置关系](https://developer.signalwire.com/freeswitch/foundations/introduction/) | 有限本地号码接听；完整终端注册、目录与租户管理未完成 |
| 线路、gateway、注册 | [Sofia gateways](https://developer.signalwire.com/freeswitch/users-and-endpoints/gateways/) | 单固定上游客户端；多线路及完整 gateway 语义未完成 |
| IVR / 播放 / 收号 | [IVR menus](https://developer.signalwire.com/freeswitch/applications/ivr-menus/) | 本地受限实现；完整菜单、脚本与应用参数须逐项补齐 |
| 盲转、咨询转接 | [`transfer` / `att_xfer`](https://developer.signalwire.com/freeswitch/dialplan/dptools/) | 不能把双腿桥接或 `park` 当完整转接；REFER 等仍有缺口 |
| 录音 | [`record_session` / `uuid_record`](https://developer.signalwire.com/freeswitch/media-and-codecs/audio-files/) | RX 收音不等于录音产品；完整录音与双轨留存待建设 |
| 电话会议、混音 | [`mod_conference`](https://developer.signalwire.com/freeswitch/applications/conferencing/) | 未实现完整会议；两腿音频桥接不等于多方混音 |
| 排队、坐席、ACD | [`mod_fifo` / `mod_callcenter`](https://developer.signalwire.com/freeswitch/applications/call-queues/) | 完整队列、坐席分配与状态管理未完成 |
| 语音信箱 Voicemail | [`mod_voicemail`](https://developer.signalwire.com/freeswitch/applications/voicemail/) | 未实现完整留言、检索和消息管理 |
| 事件与外部控制 | [`mod_event_socket`](https://developer.signalwire.com/freeswitch/integration/event-socket/) | 入站有限命令/事件；outbound ESL 与完整事件语义未完成 |
| 话单 CDR | [事件、CDR 与日志模块](https://developer.signalwire.com/freeswitch/module-reference/event-handlers/) | 本地事件日志不等同完整计费话单、对账或原版 CDR 模块 |
| 浏览器电话 | [SIP over WSS](https://developer.signalwire.com/freeswitch/users-and-endpoints/webrtc-sip/) | 管理页面不是 WebRTC 电话；WS/WSS、ICE、DTLS-SRTP 等仍待实现 |

会议、队列、语音信箱各有独立状态和资源需求。特别是 FreeSWITCH `mod_callcenter` 的 queue、agent、tier 模型，不能用“同时建立中呼叫上限”替代。[官方队列模型](https://developer.signalwire.com/freeswitch/applications/call-queues/)

## 6. 从 PBX 到 Voice Agent，新增了哪些验收要求

PBX 呼叫流程可以作为起点，语音智能体还需要把媒体与外部异步服务连接起来。以下是本项目的集成验收建议：

- **持续收音**：音频来源、会话/代次和采样率明确；丢失与过期帧不能变成真实用户沉默。
- **流式播放**：TTS 迟到、排队过长或轮次被取消时，旧语音不能进入下一轮通话。
- **打断**：分别确认业务取消、队列停止、媒体停止和剩余缓存，避免只返回成功命令。
- **模型失效**：ASR/TTS 超时、连接断开和慢消费者应释放资源或有明确降级结果。
- **转人工**：验证需要的接续/转接路径与失败回退，不能把概念图视为已实现呼叫中心。
- **数据记录**：音频、识别文本、应用事件和通道身份需要可关联，但测试材料应脱敏。

RustSwitch 主线提供 G.711 实时图、PCM 下行与 RX 收音基础；ASR1 是单独公开、尚未合入主线的候选，mock 只验证协议。真实识别、TTS、LLM、自动 VAD 打断和完整业务链路仍待建设。[内部音频与候选合同](../05-协议与SDK文档.md)

## 7. 编解码与 WebRTC：选型时不要跳过媒体路径

G.711 PCMA/PCMU、G.722、Opus、G.729 等名称出现在 SDP 或管理页面中，可能分别表示能够识别、协商、转发或执行编解码。集成测试应明确究竟需要哪一层，而不只问“支持这个 codec 吗”。

本项目当前实时图以 G.711 为基础；G.722/Opus 可选原生后端及离线验证不等于已经接入所有实时路径。G.729、G.726 等应按各自载荷合同判断透传范围，不能宣布已有完整实时转码。[本项目音频能力](../reference/docs/api/audio-reference.md)

FreeSWITCH 的浏览器接入有 SIP over WS/WSS 和 Verto 路径，WebRTC 媒体还涉及 ICE 与 DTLS-SRTP。一个 SIP TCP/TLS 监听器或网页按钮不能替代这套浏览器媒体协议。[官方 WebRTC over SIP](https://developer.signalwire.com/freeswitch/users-and-endpoints/webrtc-sip/)

因此，RustSwitch 后台“小电话”目前是隔离 SIP/媒体模拟测试。需要浏览器麦克风、移动网络或 NAT 穿越时，应单独列出相应协议与安全验收，不能沿用模拟测试通过结论。

## 8. 如何组织一次可复现的 PBX 迁移评估

1. **固定版本**：记录 FreeSWITCH 提交/配置、RustSwitch 提交/变体、工具链和二进制哈希。
2. **建立业务清单**：按注册、呼入、呼出、IVR、转接、录音和事件消费拆分具体场景。
3. **映射依赖**：列出真正使用的命令、变量、模块、事件和媒体格式；对照完整缺口。
4. **先做单路**：保存双方 SIP/SDP、事件、双向音频及最终资源状态。
5. **加入失败路径**：认证错误、取消/接通竞争、缺失 ACK、播放失败、超时与进程重启。
6. **再测容量**：保持编解码、包时长、CPS、时长、录音/AI功能与硬件条件一致。
7. **给出有范围的结论**：记录通过的场景、差异、未运行项和回退要求，不计算虚假的全量百分比。

本仓库 [3999 条原始对照](../backlog/FreeSWITCH-全部逐项状态.csv)混合声明、配置与验收条目，不是“3999 个 PBX 功能”。历史绿色也不是当前源码的自动认证；文档门禁阻塞和容量失败应继续保留。[证据使用规则](../DOCUMENTATION.md)

对于压力测试，至少分开记录呼叫请求数、实际接通峰值、媒体发送量、有效接收量、错误/丢失、尾延迟和释放后的残留资源。本项目压力工具还受隔离端口和发生器能力限制；未完整生成结果不能判通过。[运维与压力测试](../07-运维与压测说明.md)

## 9. 常见问题

### RustSwitch 现在可以替换完整 FreeSWITCH PBX 吗？

不能作这个结论。已有有限线路、媒体、IVR、ESL/XML 子集，但完整分机、转接、会议、队列、语音信箱和其他模块仍有缺口。应按实际业务清单逐项验证，而不是直接替换现网全部功能。

### “注册成功但打不通”应该先看什么？

先分开检查注册状态、INVITE 是否到达、号码路由/认证结果、SDP 地址与 codec、真实 ACK 和双向 RTP。每一步保存可关联的事务或通道身份；注册状态本身无法定位语音问题。

### `+OK`、HTTP 200 或一次 `uuid_exists` 为 true，能证明业务完成吗？

不能。分别核对命令受理、应用完成、媒体实际处理、对端收到及业务结果；完整通话还需要挂机和资源回收。对应判断见[ESL](../reference/docs/api/esl-reference.md)及[事件生命周期](../reference/docs/api/event-lifecycle-reference.md)。

### 呼叫排队和峰值保护有什么不同？

排队是等待坐席并按照业务策略分配；峰值保护是控制新呼叫准入，减少过载。本项目保护策略不提供完整 ACD/坐席队列，也不通过无限排队保证最终接通。

### XML 模板有会议和语音信箱，为什么仍显示未实现？

模板属于编辑/导出能力。运行时加载、应用执行、状态、事件、音频和持久化是另外的实现与验收层。导出上游模板不会带来对应 FreeSWITCH 模块宿主。

### 已有 PCM/RX，能直接替代录音或 ASR 产品吗？

不能。音频接口是集成基础，还需要录音存储/索引、真实识别模型、错误恢复、访问控制和生命周期验收。ASR mock 不输出真实识别结果。

### 如何贡献有价值的兼容信息？

提交[互通报告](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues/new?template=compatibility_report.yml)，写明固定版本、具体模块/命令、最小场景、双方结果和脱敏证据。贡献方式见[社区指南](../COMMUNITY.md)与[贡献说明](../CONTRIBUTING.md)。

## 10. 接下来阅读

- 专题评估：[45 项 PBX 功能矩阵](pbx-feature-matrix.md)、[FreeSWITCH 迁移与回退指南](freeswitch-migration.md)。
- 安装与实验：[快速开始](../QUICKSTART.md)、[使用说明](../06-使用说明.md)。
- 开发与接入：[HTTP 接口](../04-HTTP接口文档.md)、[协议与 SDK](../05-协议与SDK文档.md)、[源码导航](../source/README.md)。
- 迁移与验收：[固定版本对照标准](../source/main/docs/freeswitch-compatibility/README.md)、[当前状态](../08-当前能力与验证状态.md)、[未完成清单](../13-项目未完成清单.md)。
- 开源与安全：[许可范围](../LICENSE_SCOPE.md)、[第三方声明](../THIRD_PARTY_NOTICES.md)、[安全政策](../SECURITY.md)。

本文为 RustSwitch 社区的原创整理与集成建议，引用官方资料用于说明上游功能。FreeSWITCH 名称不代表本项目获其认证、背书或官方发行资格；模块能否使用还应核对选定上游版本的实际构建、加载和配置。
