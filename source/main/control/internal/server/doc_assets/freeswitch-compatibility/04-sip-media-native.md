# 04 SIP、媒体、原生模块与切换连续性标准

文档状态：接口目标与验收规范，**不是已经实现或通过认证的能力清单**。按用户确认，目标锁定 FreeSWITCH 最新正式版 `v1.11.3`，固定 commit `ef32e205295e29f034f1453ad245ba5efb07b94a`。用户部署的编译选项、模块、配置及终端清单尚待导入。认证必须绑定具体基线二进制、配置和模块哈希。在线官方手册用于定位功能，默认值、特殊行为和响应格式由上述固定源码与基线实测共同确定，不能将持续更新的手册当作固定版本行为完全相同的证明。[固定版本源码](https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a)

本章的“必须”指替换产品获得对应兼容性声明前必须满足的要求。未实现、未执行、阻塞、已知差异均不能记为通过；业务当前未启用的可选模块可以标注“本部署未启用”，但不得从面向所有 FreeSWITCH 用户的完整兼容分母中删除。

## 4.1 兼容目标与当前差距

对现有客户而言，兼容必须表现为：原终端、运营商中继、SIP 代理、ESL 客户端、拨号计划和媒体文件流程，在相同配置下获得相同的呼叫结果。仅能接收相同方法名、完成一次 PCMU 呼叫或复用一个编解码库，都不能证明兼容。

协议级验收比较的是外部可观察行为。随机 Call-ID、tag、branch、UUID、端口和时间戳允许通过**预先声明的映射**对齐；映射必须保持两条呼叫腿、事务、事件、话单和录音之间的引用关系。不能把错误原因码、缺失事件、错误号码、认证放宽、媒体方向或计费时间归入“随机差异”。SIP 合法的头域顺序变化可在语义比较器中归一化；依赖特定字节格式的真实设备另做原始报文回归。

截至应用版本 v0.3、文档发行版本 1.5：

| 范围 | 当前实际状态 | 取得本章认证还缺少什么 |
|---|---|---|
| Go SIP 提供器 | 可信中继上的有限 UDP/TCP/TLS `INVITE/ACK/CANCEL/BYE/OPTIONS`，双腿呼叫、可靠连接归属及断线回收；TCP/TLS 已完成真实 Rust 双向 RTP 呼叫回归 | 完整 Sofia 配置与 SIP 行为；注册、路由、重协商、WS/WSS 等仍缺失；现有测试通过不等于 Sofia 等价 |
| Rust 媒体 | G.711、G.722、Opus、G.729、G.726 等受限格式的同编码 RTP 透传、RTCP 与 telephone-event 转发；批量收包、地址校验、容量保护 | 完整协商、NAT、终结型 RTCP、加密、实时转码和录音/会议等媒体应用，以及 FreeSWITCH 变量语义 |
| C 编解码器 | 自定义 `rs_codec_v1` ABI 与 G.711 插件；离线音频 SDK 提供规范 PCM、G.711 及可选 G.722/Opus 后端 | 实际转码通话、全部选定 codec 的参数和错误行为；离线 SDK 通过不代表实时转码 |
| 原生协议提供器 | `rs_protocol_v1` 预留契约 | Sofia-SIP/PJSIP 实际集成及与 Go 状态机的完整衔接 |
| FreeSWITCH 模块 | 不能直接加载原 `mod_*.so` | 原生模块运行环境或逐模块移植；两者需要不同认证 |
| 无中断切换 | 尚无完整现网切换验收 | 呼叫排空方案、入口路由、事件和话单收敛、回滚；活动状态迁移另验 |

当前代码事实见项目 [README](../../README.md)、[codec ABI](../../native/include/rustswitch_codec.h) 与 [protocol ABI](../../native/include/rustswitch_protocol.h)。

## 4.2 Sofia profile、gateway 与终端配置的契约

配置兼容必须保留对象名、配置入口、继承关系、变量展开时机、默认值以及修改后的生效边界。把旧 XML 转换为内部结构是实现选择；要求客户重写 gateway、终端密码、拨号字符串或管理脚本，不能获得“原配置直接替换”的认证。官方将 profile 与 gateway 作为明确的配置与运行对象，gateway 的拨号入口包括 `sofia/gateway/<name>/<number>`。[Sofia profiles](https://developer.signalwire.com/freeswitch/users-and-endpoints/sip-profiles/)、[Gateways](https://developer.signalwire.com/freeswitch/users-and-endpoints/gateways/)

下表是本项目制定的最小差分测试集，每行必须按启用的 profile、传输和真实终端扩展。

| 编号 | 需保持的契约 | 最少测试输入 | 通过条件与证据 |
|---|---|---|---|
| SIP-C01 | profile 名称、监听地址、域、context、dialplan、alias | 多 profile、同名冲突、IPv4/IPv6、端口被占用、禁用 profile | 加载结果、失败范围、监听集合、入口 context 和查询输出与基线一致 |
| SIP-C02 | gateway 名称与所有参数的作用域、优先级、缺省值 | 注册/不注册中继；proxy、register-proxy、outbound-proxy 分离；入/出方向变量 | 实际请求目的地、From/Contact、认证身份、路由变量与基线一致；不能忽略未知配置却报告完整成功 |
| SIP-C03 | profile/gateway 生命周期 | `start/stop/restart/rescan/killgw` 等基线可用命令；`reloadxml` 后新旧呼叫并存 | 接受的命令语法、返回值、影响的呼叫范围及生效时机与基线一致；完整命令清单来自基线能力目录 |
| SIP-C04 | REGISTER 用户目录与 Digest | 正确/错误口令；realm、nonce 过期、stale、qop、重放；现网使用的算法 | 挑战头、校验、重试、注册状态和事件一致；不得以不校验认证实现表面接通 |
| SIP-C05 | 注册绑定与刷新 | 多 Contact、重复 REGISTER、过期边界、注销单个/全部绑定、多设备同账号 | 绑定数量、寻址结果、到期行为、通知和持久化作用域一致 |
| SIP-C06 | 注册传输与连接寿命 | UDP/TCP/TLS/WS/WSS；连接复用、空闲、重连、断线后注册刷新 | 原终端不更改配置可注册与回呼；记录旧绑定可达性和恢复时限 |
| SIP-C07 | 注册中继恢复与 OPTIONS 探活 | 401/407、拒绝、丢响应、DNS 故障、连续成功/失败、超时重试 | 状态转换、下一次重试时间窗口、路由可用性及 `sofia::gateway_state` 类事件与基线匹配 |
| SIP-C08 | 身份与透传头 | From、PAI、RPID、Privacy、Diversion、History-Info、自定义 `sip_h_*` 等现网变量 | A/B 腿头域、主被叫显示、隐私行为及变量可见性符合基线；不盲目跨信任域复制所有头 |
| SIP-C09 | ACL 与域隔离 | 同账号跨域、目录不存在、ACL 命中/未命中、网关入口与用户入口交叉 | 访问控制、认证触发和失败事件一致；合法用户未被新的默认策略阻断 |
| SIP-C10 | 地址、端口和 DNS | `sip-ip/rtp-ip/ext-sip-ip/ext-rtp-ip`；A/AAAA/SRV/NAPTR 的基线启用组合与故障 | 对外公布地址、实际发送地址、重试路由和生存时间行为与基线一致 |
| SIP-C11 | 证书与 TLS 策略 | 现有证书路径、链、私钥口令、有效期、对端验证策略、协议/套件限制 | 无需重新签发证书或改客户端信任配置；成功和拒绝边界一致；不自动降级加密 |
| SIP-C12 | 存量注册与监控可见性 | 查询注册、联系人查找、profile/gateway 状态、刷新配置 | 业务和运维脚本的原命令、字段及状态语义保持；注册在线迁移需另过 MIG-03 |

**实现约束：**Sofia-SIP 可优先作为协议库候选，但 `mod_sofia` 还连接 FreeSWITCH 的目录、变量、事件、数据库与媒体状态。换用 PJSIP 或直接调用 Sofia-SIP 都必须过同一套行为测试，不能以“使用相同协议库”替代验收。

## 4.3 SIP 事务、对话与 SDP 行为矩阵

协议测试同时采用标准检查与 FreeSWITCH 差分。SIP 基本事务/对话、可靠临时响应、会话定时器及 WebSocket 传输分别以 [RFC 3261](https://www.rfc-editor.org/rfc/rfc3261.html)、[RFC 3262](https://www.rfc-editor.org/rfc/rfc3262.html)、[RFC 4028](https://www.rfc-editor.org/rfc/rfc4028.html)、[RFC 7118](https://www.rfc-editor.org/rfc/rfc7118.html) 为协议依据。历史特殊行为若与标准或现代安全要求冲突，应记录为明确的基线差异，并经部署方决定是否启用兼容配置；不能默默改变现网行为后仍宣称完全一致。

| 编号 | 场景 | 必须观察的行为 |
|---|---|---|
| SIP-D01 | UDP 基本呼入/呼出；同终端双向并发 | 请求/响应、独立呼叫腿、ACK、媒体建立、BYE、事件和话单完整闭环 |
| SIP-D02 | TCP/TLS 分片、粘包、多消息、连接中断 | Content-Length、消息边界、连接复用、失败传播；不得把一次读取视为完整 SIP 消息 |
| SIP-D03 | WS/WSS 升级、子协议、分帧、控制帧、连接关闭 | 握手与 SIP 消息边界兼容；浏览器原客户端无需改库；WSS、DTLS 证书分别核验 |
| SIP-D04 | 初始 INVITE 带 SDP / 无 SDP | offer/answer 所在消息、ACK 中 SDP、早协商/晚协商时机与基线一致 |
| SIP-D05 | 100/180/183 的不同组合；早期媒体变更 | 振铃、回铃音、计费前媒体、`ignore_early_media` 等基线配置及进展事件一致 |
| SIP-D06 | 100rel、PRACK，RSeq/RAck 错误/重复/超时 | 双腿可靠临时响应分别维护；不重复回答、重复启动媒体或跨腿混用序号 |
| SIP-D07 | 丢失 INVITE/1xx/200/ACK/BYE/最终失败响应 | 基线的重传与清理时间窗口、重复报文幂等性；一个业务呼叫不因重传生成额外话单 |
| SIP-D08 | CANCEL 早于/晚于 200；CANCEL 与 B 腿接通竞争 | CANCEL 事务结果、INVITE 最终结果、必要 ACK/BYE、所有胜出/失败分支资源释放；不以已发 CANCEL 忽略迟到 200 |
| SIP-D09 | 上游/下游并发 BYE；未知对话；错误 CSeq/tag | 响应、关闭原因、事件次序和计费结束点；无串话或双重业务结算 |
| SIP-D10 | Route/Record-Route、loose/strict route、Contact 更新 | 对话内请求沿正确路由集发送；代理存在时双腿独立；不能只按最近包源地址返回 |
| SIP-D11 | 3xx 重定向、多 Contact、目标环路 | 跟随与透传策略、重试上限、认证和自定义头传播、最终失败原因与基线一致 |
| SIP-D12 | 并行/顺序 fork；多个 18x、多分支 200、全分支失败 | 胜出分支、早期媒体选择、取消未胜出分支、失败原因合并和事件关联一致 |
| SIP-D13 | re-INVITE/UPDATE 保持恢复；`sendrecv/sendonly/recvonly/inactive`、零地址保持 | 单向/双向媒体、保持音乐、CHANNEL_HOLD/UNHOLD、SDP 版本变化与基线一致 |
| SIP-D14 | 同时 re-INVITE 的 glare、拒绝重协商、ACK 缺失 | 旧协商状态回退、重试、失败处理一致；拒绝新 offer 后仍能维持原通话 |
| SIP-D15 | 通话中改 codec/ptime/媒体 IP/端口；增加/关闭媒体 m-line | 双腿协商原子性、PT 映射、资源切换和旧包处理；无已确认 SDP 与实际媒体路径不一致 |
| SIP-D16 | Session-Expires、Min-SE、refresher、422、刷新丢失 | 主动/被动刷新角色、方法、重试和释放时机与配置基线一致；长通话持续覆盖多次刷新 |
| SIP-D17 | REFER 盲转、带 Replaces 的咨询转接、失败转接恢复 | REFER/NOTIFY 订阅结果、桥接关系、UUID 关联、旧腿关闭时机及话单结果一致 |
| SIP-D18 | INFO DTMF、MESSAGE、SUBSCRIBE/NOTIFY、MWI/BLF/Presence | 按现网启用模块逐项测试内容类型、事件包、订阅刷新、通知版本、权限和拒绝结果；不能统一返回 200 |
| SIP-D19 | 畸形/过长消息、重复头、未知 Require、未知方法、multipart SDP | 与基线的接受/拒绝边界、错误码和连接处置对齐；不会损坏其他会话 |
| SIP-D20 | 本地 drain、CPS/会话/媒体资源耗尽、下游拥塞 | 对外错误码、Retry-After 是否存在、相关变量和事件一致；已接通呼叫服务质量单独计量 |
| SIP-D21 | 非 SIP 终端模块被使用 | `mod_verto`、其他 endpoint 的协议与命令另列模块测试；通过 SIP over WSS 不能替代 Verto JSON-RPC 兼容 |

为避免状态分裂，每条 SIP 事务只能由一个协议提供器拥有重传、连接和事务定时器；Go 控制面拥有跨腿业务决策。协议提供器必须把临时对话、最终对话、路由集、协商提交/回滚、transport 关闭及原因码上报到统一契约。当前 `rs_protocol_v1` 仅定义传递字节的函数指针，还没有这些完整事件 schema 和幂等规则。

## 4.4 媒体、DTMF、编解码、录音、会议与传真

FreeSWITCH 的 normal、proxy、bypass 是不同媒体行为：normal 可处理媒体；proxy 保留服务端转发路径；bypass 让端点直接交换媒体。选用 bypass 后取得的并发数不能称为服务器转发容量。相关控制项应与 [官方媒体说明](https://developer.signalwire.com/freeswitch/media-and-codecs/handling/) 对齐。编解码优先级、早晚协商和参数交集必须分别验证，依据 [官方 codec 协商说明](https://developer.signalwire.com/freeswitch/media-and-codecs/codecs/) 定位固定版本实现。

| 编号 | 兼容面 | 必须覆盖的输入与结果 |
|---|---|---|
| MED-01 | normal / proxy / bypass / bridge 后 bypass | 每模式抓取两腿 SDP 与媒体路径；切换保持、转接和录音时按基线恢复媒体；模式未支持不可自动改为另一模式 |
| MED-02 | RTP 包与时间轴 | SSRC、sequence 回绕、timestamp 回绕、marker、CSRC、header extension、padding、重复/乱序；验证透明转发或终结重建是否符合所选模式 |
| MED-03 | 动态 PT 与多速率 codec | 同 codec 不同 PT、两腿 PT 不同、fmtp/ptime/maxptime、音频采样率与 RTP clock 区别、CN/DTX；协商成功必须能实际双向播放 |
| MED-04 | codec 集合与优先级 | 按基线实际加载清单逐个及成对验收 PCMU/PCMA、G722、Opus、其他语音/视频 codec；验证 absolute/inherit codec 类变量、禁用转码和协商失败 |
| MED-05 | 转码与重采样 | 每个已声明支持的有向 codec 对、声道数/采样率/帧长组合；音质、音量、延迟、CPU、解码错误处理和两腿 RTP 节奏均达预先冻结门槛 |
| MED-06 | 抖动缓冲、PLC、FEC、VAD | 确定性注入延迟/乱序/丢失/重复；输出音频和计数与基线功能设置一致；网络真实丢包与补偿输出分别统计 |
| MED-07 | RFC 2833/4733 telephone-event | 动态 PT、不同 clock、0–9/*/#/A–D、长按、结束包重传、跨包间隔、双腿重复；一次按键产生的事件数量、持续时间单位和顺序与基线相同 |
| MED-08 | SIP INFO 与带内 DTMF | INFO 内容类型、duration、无效数字；语音与双音混合；检测、生成、透传和模式转换分别验证；重复来源不导致重复业务输入 |
| MED-09 | DTMF 与应用交互 | IVR、收号、play-and-get-digits、放音打断、DTMF 绑定与注入；相同输入得到相同业务分支和相关变量 |
| MED-10 | NAT 与源地址学习 | 对称 RTP、SDP 私网地址、rport/received、NAT 映射变化、合法媒体换源、禁止 auto-adjust 的配置、陌生源注入；学习窗口及安全边界与基线对齐 |
| MED-11 | RTCP | 独立端口/rtcp-mux、SR/RR/SDES/BYE、扩展反馈、报告间隔、丢包/jitter 统计、终结与透明转发分别测；不能用转发 RTCP 证明生成报告兼容 |
| MED-12 | SRTP/SDES | 现网加密套件、mandatory/optional/forbidden、方向覆盖、重协商/换密钥、重放、认证失败；不能回落明文而报告加密成功 |
| MED-13 | DTLS-SRTP、ICE 与 WebRTC | fingerprint/角色、握手重传、候选选择、ICE restart、网络切换、rtcp-mux、多媒体协商；按基线支持的组合测真实浏览器；记录失败事件与恢复窗口 |
| MED-14 | 放音、提示音、TTS/ASR/流式源 | 文件 URI、相对路径、语言/声音资产、媒体格式、seek/stop、播放完成/打断、外部服务超时；音频产物与应用事件同时验证 |
| MED-15 | 双腿录音与媒体 bug | record_session、uuid_record 及所用 API；单声道/立体声/方向、开始停止暂停恢复、追加、通话转接、录音跟随；文件可打开、声道归属、时长、路径及事件正确 |
| MED-16 | 录音故障 | 目录不存在、无写权限、磁盘满、编码失败、进程故障；API/事件错误与通话是否继续的行为对齐；不得发成功事件但缺文件 |
| MED-17 | 音频会议 | 按房间和成员数拓扑验收入会/离会、静音、主持人、PIN、锁定、DTMF 菜单、音量、旁听、成员控制、录音；输出音频、成员查询与 CUSTOM 事件一致 |
| MED-18 | 视频与会议 | codec/PT/fmtp、分辨率、帧率、关键帧请求、PLI/FIR/NACK、音画同步；passthrough、转码、混屏分别验收，房间布局/屏幕共享按启用基线覆盖 |
| MED-19 | G.711 传真、T.38、网关转换 | txfax/rxfax、T.38 re-INVITE 接受/拒绝/回退、UDPTL 冗余、页数、ECM、超时、音频↔T.38；收发文件、页内容、结果变量和完成钩子一致 |
| MED-20 | 超时、静音、保持期间超时 | RTP inactivity、hold timeout、早期媒体超时、合法 DTX 和单向媒体；不因丢一包或合法无声误杀通话；终止时间、原因和事件与基线相同 |
| MED-21 | 通话媒体资源释放 | 正常挂断、失败、fork、CANCEL race、re-INVITE 回滚、worker 故障后，端口、文件、codec context、定时器和共享缓冲最终回收；旧会话包不能进入复用后的新会话 |

**当前特别缺口：**Rust 可选 connected UDP 将 peer 固定到协商地址；这不是 FreeSWITCH RTP auto-adjust/NAT 学习语义。优化不能阻止合法终端换源，也不能为追求兼容而放开无条件换源。实现时应在 MED-10 明确定义学习状态和可认证变更，然后在新 peer 生效时原子更新目的地并清理旧队列。

WebRTC 的 SIP over WS/WSS 与 Verto 是两种信令入口；加密传输与媒体安全也有独立的证书与状态。支持其中一个入口不能推出另一个入口可用。[官方 WebRTC over SIP 文档](https://developer.signalwire.com/freeswitch/users-and-endpoints/webrtc-sip/)

媒体功能的结果不只是一条 `+OK`。录音/放音必须核对相应开始和停止事件、路径和完成状态；会议应核对房间/成员事件及控制结果；传真应核对实际页内容与结果变量。事件名与字段以固定版本采集为准，不为方便实现而创造“等价但改名”的事件。[官方事件目录](https://developer.signalwire.com/freeswitch/programming/events-catalog/)、[会议](https://developer.signalwire.com/freeswitch/applications/conferencing/)、[传真](https://developer.signalwire.com/freeswitch/applications/fax/)

## 4.5 SIP / Q.850 / FreeSWITCH 原因码的映射

原因码是业务接口：呼叫中心重拨、计费、告警及运营商重试会读取它。必须同时保存原始协议结果与 FreeSWITCH 语义，不得仅将所有非 2xx 合并为“呼叫失败”。应逐腿验证 `hangup_cause`、`Hangup-Cause`、`sip_term_status`、`proto_specific_hangup_cause`、`last_bridge_hangup_cause`、`originate_disposition` 与话单中的对应值。[官方呼叫失败说明](https://developer.signalwire.com/freeswitch/troubleshooting/call-setup/)

以下覆盖已核对的 `v1.11.3` 函数 `sofia_glue_sip_cause_to_freeswitch()` 全部显式分支和默认分支。它是该函数的映射表；更高层的 Reason 覆盖、配置与实际调用时机仍须运行测试，也不能通过反向查表生成所有出站响应。[固定源码：sofia_glue.c](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/sofia_glue.c#L1814)

| 输入的最终 SIP 响应 | 应核对的 FreeSWITCH 原因名称 |
|---|---|
| 200 | `NORMAL_CLEARING` |
| 401 / 402 / 403 / 407 / 603 / 608 | `CALL_REJECTED` |
| 607 | `UNWANTED` |
| 404 | `UNALLOCATED_NUMBER` |
| 408 / 504 | `RECOVERY_ON_TIMER_EXPIRE` |
| 410 | `NUMBER_CHANGED` |
| 413 / 414 / 416 / 420 / 421 / 423 / 505 / 513 | `INTERWORKING` |
| 480 | `NO_USER_RESPONSE` |
| 484 | `INVALID_NUMBER_FORMAT` |
| 485 / 604 | `NO_ROUTE_DESTINATION` |
| 486 / 600 | `USER_BUSY` |
| 487 | `ORIGINATOR_CANCEL` |
| 488 / 606 | `INCOMPATIBLE_DESTINATION` |
| 502 | `NETWORK_OUT_OF_ORDER` |
| 405 | `SERVICE_UNAVAILABLE` |
| 406 / 415 / 501 | `SERVICE_NOT_IMPLEMENTED` |
| 482 / 483 | `EXCHANGE_ROUTING_ERROR` |
| 400 / 481 / 500 / 503 | `NORMAL_TEMPORARY_FAILURE` |
| 428 | `NO_IDENTITY` |
| 429 | `BAD_IDENTITY_INFO` |
| 437 | `UNSUPPORTED_CERTIFICATE` |
| 438 | `INVALID_IDENTITY` |
| 没有显式映射的其他值 | `NORMAL_UNSPECIFIED` |

必须补全三类独立测试：

1. **协议→内部：**遍历源码中的全部显式分支及未知状态码；比较带/不带 Reason/Q.850 的响应、BYE，验证配置是否允许 Reason 覆盖 SIP 映射。
2. **内部→协议：**遍历基线原因枚举与 `respond`、`hangup`、`uuid_kill`、路由失败、资源不足、超时等入口；核对 wire status、reason phrase、Reason 头和变量。多个 SIP 状态可能归并为同一原因，反向映射不保证可逆。
3. **多腿聚合：**同时忙线/无应答/拒绝、fork 全失败、先失败后重试成功、取消与接通竞争；比较最终 A 腿、各 B 腿以及业务返回结果。不能只检查最后到达的 B 腿原因。

可记录的测试证据必须包含触发入口、两腿报文、事件、最终话单与源码分支；只核对一个 HTTP 状态码不满足要求。

## 4.6 C/C++ 生态兼容与 FreeSWITCH 模块 ABI

FreeSWITCH 模块通过宿主提供的接口注册端点、应用、API、codec 等，并参与加载、运行与卸载生命周期；模块开发使用其开发头文件和相关核心服务。[官方模块开发](https://developer.signalwire.com/freeswitch/FreeSWITCH-Explained/Community/Contributing-Code/Creating-New-Modules/)、[模块加载](https://developer.signalwire.com/freeswitch/configuration/module-loading/)

固定版源码中 `SWITCH_API_VERSION` 为 `5`，模块定义暴露函数表，装载器检查该版本，模块接口同时持有多类接口指针、锁、引用计数和内存池。版本号相同只是一个检查条件，不是所有结构与运行行为兼容的充分条件。[模块定义](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_types.h#L2604)、[模块接口结构](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L64)、[装载器检查](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_loadable_module.c#L1757)

必须把下列四种兼容声明分开，且给每个原生组件标记一种：

| 等级 | 客户可继续使用的东西 | 实现与验收要求 | 不能推出的结论 |
|---|---|---|---|
| N-LIB 库复用 | libopus、libsrtp、SpanDSP、Sofia-SIP 等选定库 | 为确定版本库编写适配器；验证参数、线程、内存、错误和媒体/协议结果 | 不能直接加载原 `mod_opus.so`、`mod_sofia.so` 或客户模块 |
| N-SRC 源码移植 | 有源码的客户/社区模块 | 逐模块调整或提供源码 API 兼容层后重新编译，实际执行功能用例 | 不是原 `.so` 文件原样可用，也不是所有模块都能重编译 |
| N-BIN 二进制兼容 | 客户现有且哈希不变的 `.so` | 在同 CPU/OS/动态库环境下，完整满足所需符号、结构布局、调用约定、生命周期与语义；原文件通过全部业务测试 | 仅 `dlopen` 成功或提供同名函数不代表 ABI/行为兼容 |
| N-HOST 原版兼容宿主 | 经认证的原模块与其对应 FreeSWITCH 运行环境 | 保留匹配版本的 FreeSWITCH 宿主和依赖，原模块在宿主中运行；内部连接 Rust/Go 新能力 | 这是一套含原版核心的兼容发行方案，不能宣称已完整移除 FreeSWITCH C 核心 |

现有 RustSwitch 的 `rs_codec_get_v1`、`rs_codec_v1`、`rs_protocol_v1` 是新项目自己的 ABI。它们不含 FreeSWITCH channel/session、内存池、事件总线、codec/frame、media bug、数据库、XML 查询、状态处理器等宿主服务，所以不能把“保留 C/C++ 生态”写成“所有 FreeSWITCH 二进制模块均兼容”。Go 对象也不能直接作为原模块期待的 FreeSWITCH 结构体指针传递。

**建议实施路线：**先建立外部兼容面，由 Go 承担兼容控制逻辑、Rust 提供经过验证的媒体能力；必须保留的原模块先在 N-HOST 环境内运行，再逐模块移到 N-LIB/N-SRC 或完成 N-BIN 服务。进程隔离的协议/codec 服务本身并不能接纳任意依赖 channel/media-bug 的模块；这些模块需要完整宿主。对宿主到 Rust 的媒体分流，必须实现明确的接入模块与媒体边界，并重新测试录音、转码、会议、事件及变量。当前代码尚没有这个兼容宿主或分流模块。

若交付物要求完全 Rust/Go 替换原核心，则 N-HOST 只能是过渡阶段，完整验收最终仍需消除外部对原核心的依赖，并通过本部署全部模块与业务用例。若客户要求现有任意闭源模块原样加载，必须先取得模块清单与运行样本才能定义有限、可测的 N-BIN 范围；不能承诺未知模块的无限兼容性。

| 编号 | 原生验收场景 | 通过标准 |
|---|---|---|
| NAT-01 | 模块清单与装载 | 文件哈希、版本、CPU/OS、依赖、load/unload/reload 与失败返回均已记录；未满足依赖不会误报加载成功 |
| NAT-02 | 原有注册接口 | 模块注册的 applications、APIs、events、codecs、file formats 与实际能力目录一致 |
| NAT-03 | 内存和对象寿命 | session/channel/frame/内存池指针在其规定寿命内有效；错误退出、取消及卸载无越界、悬空引用或泄漏 |
| NAT-04 | 线程、回调与重入 | 媒体线程与控制线程语义、阻塞规则、回调次序、锁与重入行为满足被移植模块实际要求 |
| NAT-05 | codec 生命周期 | 初始化、fmtp、编码、解码、PLC/FEC、reset/destroy、非法长度及并发；不能跨 Rust/Go/C 边界传播异常或 unwinding |
| NAT-06 | 运行中卸载/故障 | 活跃调用对模块的引用、拒绝/等待卸载、孤立模块进程退出、重启后的资源回收与基线声明一致 |
| NAT-07 | 非 codec 原生模块 | media bug、ASR/TTS、MRCP、存储、CDR、文件格式、定时器及自定义模块分别用真实工作流验证 |
| NAT-08 | ABI 认证复现 | 客户原二进制未经修改在认证环境执行；结果必须包含功能测试而非只列动态符号检查 |

## 4.7 “无感替换”与“活动状态在线迁移”分别验收

**原客户端无需改造**和**切换时连接不断**是不同目标。前者解决接口兼容，后者还需要复制或保持正在运行的协议、传输、媒体、应用与持久化状态。DNS/VIP 改向只能改变后续流量的到达位置；不能自然生成目标节点不存在的 SIP 对话、TLS/ESL 连接、SRTP 密钥、RTP 时间轴和文件句柄。

| 编号 | 切换目标 | 必须设计的机制 | 通过条件 |
|---|---|---|---|
| MIG-01 | 原配置与原客户端替换后可用 | 安装路径、配置、证书、入口地址、账号、模块和管理命令兼容 | 基线业务全回归；客户业务代码、终端设置和运营商配置不变，允许预先约定的运维部署步骤 |
| MIG-02 | 存量呼叫排空后切换 | 旧节点保留已有对话/媒体；新呼叫通过已存在或新增并经认证的入口策略去新节点；等待旧呼叫自然结束后停机 | 排空期间已有通话不中断，旧新节点事件/话单归属正确；记录最长等待时间及新呼叫可用性，超时不能自动强拆还称无损 |
| MIG-03 | 存量注册/订阅平滑接续 | 注册绑定、过期时间、凭据校验策略、NAT/transport 可达性、订阅版本及网关刷新所有权 | 已注册终端继续可被叫；重注册风暴可控；数据库复制不被误认为 TCP/TLS/WS 连接也已迁移 |
| MIG-04 | 活动通话在线迁移 | 两腿事务/对话、路由、应用执行点、定时器、媒体 SSRC/序列、抖动缓冲、SRTP/DTLS/ICE、codec 状态、录音等完整方案 | 无需等通话结束；故障注入后媒体缺口、信令成功率、重复执行、录音/话单连续性均达冻结指标；若只能恢复部分功能必须明确范围 |
| MIG-05 | 活动 ESL/脚本连接在线迁移 | TCP/TLS 连接保持层或明确重连方案；订阅过滤器、linger、job/execute 关联、未完成命令、事件游标与客户端解析状态 | 原客户端无需重连才可称连接不断；若要重连则按重连兼容验收，不能将重放伪装成原版具备的可靠续传 |
| MIG-06 | 故障回滚 | 防止同一呼叫双主、地址归属、命令幂等、计费去重、注册归属与残留状态清理 | 失败切换能在冻结窗口内恢复；不产生重复扣费、重复外呼或两端同时接管 |

本阶段应优先形成可验证的 MIG-01/MIG-02 方案。MIG-02 在有合适入口路由时能让新旧版本并存、等待旧通话结束；若现场只有单个地址且没有对话路由/保留旧流量的机制，则不能默认推导出新呼叫零停机。MIG-04/MIG-05 属于独立工程与独立认证，当前原型并未实现，单机故障也不能通过接口文档获得零中断保证。

## 4.8 万路容量必须按功能组合认证

容量单位先冻结：本项目目标中的一“路”默认是**一通同时成立的双向桥接呼叫**，通常涉及两个 channel；FreeSWITCH 的 session/channel 指标必须明确换算，不能把 10,000 个 channel 宣称为 10,000 通双腿呼叫。多方会议以参与者和房间数描述，不能强行沿用双腿呼叫数字。

以 G.711、20 ms、160 字节音频负载、IPv4 且无加密/扩展为算例：10,000 通双向呼叫对媒体服务器贡献约 1,000,000 RTP 收包/秒及 1,000,000 发包/秒；按 RTP/UDP/IPv4 共 40 字节头计算，约 1.6 Gbit/s 入站和 1.6 Gbit/s 出站。该估算尚未包含以太网开销、RTCP、DTMF、重传/冗余或隧道；它是测试规划算例，不是硬件性能结论。

以下每一组合都必须独立报告达标容量。不能用 PCMU proxy 的成绩覆盖 Opus 转码、SRTP、录音或会议。

| 容量档案 | 冻结的功能组合 | 除通话数外的关键负载 |
|---|---|---|
| CAP-01 | G.711 双向媒体转发，20 ms，SIP/UDP | 双向真实满额包率；注册/事件/CDR 工作量；独立发生器和接收端 |
| CAP-02 | normal 媒体，同 codec，含 DTMF/RTCP | 媒体处理开销、定时器、播放/收号真实比例；不是 proxy 的更名 |
| CAP-03 | codec A→B 与 B→A 转码 | 每种 codec、帧长、声道、复杂度、采样率及质量门槛 |
| CAP-04 | SIP/TLS 或 WSS + DTLS/SRTP/ICE | 活动连接数、TLS 握手/秒、注册刷新、ICE restart 比例、加密包率 |
| CAP-05 | 录音 | 同时录音通话比例、单/双声道、文件格式、实际磁盘带宽/延迟、文件关闭与持久化时间 |
| CAP-06 | 音频会议 | 例如 1,000 房×10 人与 10 房×1,000 人分别测试；发言比例、采样率、混音与录音配置 |
| CAP-07 | 视频或传真 | 每路码率/分辨率/帧率、透传/转码/混屏；传真页数、图像复杂度、并发和成功率 |
| CAP-08 | 现网混合业务 | 根据真实使用比例组合以上能力，持续有建呼/拆呼、转接、保持、刷新及事件消费 |

验收报告必须包含：

1. **稳定性：**预热后目标并发持续运行，建议容量门槛至少连续 60 分钟，另做不少于 24 小时的目标业务稳定性运行；具体时长与容错阈值在执行前冻结，失败后不能缩短窗口以获得通过。
2. **满额供给：**发生器完成目标 CPS、同时在话数和每流实际包率；发送量不足时标记“负载不足”，不能以低丢包率通过。每个方向逐流校验内容、序列、重复、乱序和接收窗口。
3. **SLO：**分别规定建呼成功率、p95/p99 建呼时延、DTMF 延迟、媒体时延/缺口、系统新增丢包、意外掉话、事件缺失、录音完整率、话单正确率。若目标是实验条件下系统新增丢包为零，应将阈值写为零，并保留所有失败包及窗口；它不构成对任意公网丢包的承诺。
4. **完整证据：**服务与发生器 CPU/内存/文件描述符、网卡/内核/socket 丢包、收发计数、事件队列、磁盘和录音、两腿协议结果，以及时间同步误差。硬件型号、NUMA、网卡队列、内核、构建参数、依赖与配置哈希一并归档。
5. **压力边界：**目标负载下做注册风暴、瞬时 CPS、慢 ESL 消费者、XML 后端/磁盘变慢、外部中继拒绝和媒体故障；验证准入拒绝的基线语义以及已有呼叫的质量。稳定目标性能与超载保护效果分别给结论。
6. **清理收敛：**停止供给后，呼叫、对话、注册/订阅保留项、定时器、端口、文件和事件队列按各自生命周期收敛；不能将仍有活动通话的压测进程强杀后称“资源释放通过”。

历史本机千路记录和历史 1.4 Linux 虚拟机两次五千路通过记录保留各自版本边界。本轮 1.5 四次新构建及一次旧版对照，共五轮均未提供足量负载；最新 capacity-04 实际发收各 2,407,092 包，标称提供率 48.14184%，另有发生器发送写错误 8 次，未通过验收。这些记录不能认证 10,000 路、完整 FreeSWITCH 兼容、生产网卡路径或任意上述功能组合；独立 Linux 服务器到位后仍需按冻结档案复验。详见 [1.5 交付验证说明](../api/release-validation-v1.5.md)。
