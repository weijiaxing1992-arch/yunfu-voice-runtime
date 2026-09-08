# FreeSWITCH 功能覆盖与超越能力审计

**结论：当前不能无感替换FreeSWITCH，不能宣称完整功能兼容，也没有证据证明全面超越或生产单机万路媒体容量。** 本文逐项覆盖固定目录与关键业务域；“覆盖完整”仅指静态审计分母闭合，不是功能实现率。

审计日期：2026-09-06。固定参考：FreeSWITCH 1.11.3，提交 `ef32e205295e29f034f1453ad245ba5efb07b94a`。自研运行源码哈希随JSON一并冻结；原版动态差分本轮执行次数为0。

## 版本与范围

本次实际打开官方[最新发行入口](https://github.com/signalwire/freeswitch/releases/latest)，跳转到[FreeSWITCH v1.11.3](https://github.com/signalwire/freeswitch/releases/tag/v1.11.3)，页面标为Latest，和本项目固定基线一致。页面列出该版的安全、稳定性修复及Sofia/Verto聊天API协议门禁变化；不能把旧分支或master的行为混入本基线。[固定提交](https://github.com/signalwire/freeswitch/commit/ef32e205295e29f034f1453ad245ba5efb07b94a)用于源码定位。

这是有日期的正式发行观察；离线生成/检查不会自动重新确认未来的最新版本。用户现网二进制、发行补丁、构建选项、第三方模块和脚本尚未采集，不能把上游vanilla清单冒充现场完整运行清单。

## 找到的清单缺口与准确分母

| 分母 | 审计覆盖 | 含义 |
| --- | --- | --- |
| 原源码目录catalog | 144 / 144 | 143个mod_*目录，加1个sdk/autotools示例目录 |
| 完整固定Git树的mod_*目录 | 144 / 144 | 增补只有Makefile.am的mod_com_g729；仍不是已构建/加载模块数 |
| 本审计并集 | 145 / 145 | 保留所有原条目、SDK示例及新增构建目录 |
| API/应用等注册声明位置 | 538 / 538 | 533个映射到目录，5个内建core注册；重复与条件编译声明仍保留 |
| 所选参考源文件 | 712 / 712 | 同时复核SHA-256及完整树中的Git blob SHA-1 |
| 已认证原模块等价 | 0 | 没有原模块宿主，没有FreeSWITCH成对动态验收结果 |

`sdk/autotools`源码声明的是`mod_example`；SDK示例不能计为生产业务模块。完整树的[`mod_com_g729`](https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_com_g729)仅列构建文件，C/C++选定文件扫描没有收录它。此次补齐的是目录审计边界，不推断该产物可以构建、下载、授权或加载。

538是注册宏出现位置，其中292个API、222个应用、10个JSON API和14个聊天应用；17个声明仍需人工解析，不能视作全部独立运行命令数。内建MSRP和vpx注册另列；宏动态生成、实际模块加载、外部原生模块、数据库/脚本后端及现场配置决定的能力，仍需运行盘点。原catalog的875个param出现位置与管理台4,304个可编辑属性使用不同计数规则，均不等于运行功能数。

## 当前确实提供的有限范围

- 自研Go SIP UDP/TCP/TLS可信固定中继，受限INVITE/ACK/CANCEL/BYE/OPTIONS、双腿状态、早期媒体与释放流程；并非完整Sofia端点。
- 控制面新增PCMU/PCMA、G722、Opus、G729、G726/AAL2各码率及L16格式协商、独立RTP时钟与CN描述；Rust媒体同编码转发、分片和Linux批量接收必须按实际接线及逐格式结果验证，不能将协商/测试向量等同转码、混音、录音或WebRTC。
- 自有HTTP管理、热更新准入、JSONL事件、配置草稿与真实本机测试工具；均有自己的协议与数据模型。
- 官方XML编辑和ZIP导出，以及自有C codec ABI检查；XML不进入当前业务引擎，插件检查不等于FreeSWITCH模块或真实转码通话。

下表“相近用途”“部分协议”“仅导出”均不计原模块兼容分子。源码实现、项目自测、原版成对验收是三种不同证据；本审计执行的是静态检查，既有回归定义不算本轮已跑。

## 关键业务域逐项结论

| 业务域 | 当前判断 | 成对验收仍需执行 |
| --- | --- | --- |
| 原生模块宿主与客户端ABI | 自有codec ABI可独立检查，原FreeSWITCH模块、switch_*宿主和libesl对象语义未实现。 | `PAIR-NATIVE`：原样加载启用模块及第三方产物；比对导出符号、内存所有权、回调顺序、卸载和异常。 |
| SIP中继与完整Sofia行为 | 支持可信固定中继六种入站方法、UDP/TCP/TLS、受限上游注册/Digest和本地自动应答；终端注册、复杂路由和完整Sofia状态机缺失。 | `PAIR-SIP`：比较正常、拒接、重传、CANCEL/200竞争、早期媒体、Route、PRACK、REFER、重协商与分叉报文。 |
| 用户注册、Digest与权限 | 固定上游REGISTER客户端与Digest认证呼叫已实现；无终端注册数据库、位置服务或用户目录鉴权。 | `PAIR-REGISTRATION`：注册刷新/注销、nonce过期、重放、认证失败、同号多终端、域隔离与注册故障恢复。 |
| TCP/TLS与IPv6信令 | 已实现IPv4 TCP/TLS双腿、证书校验与连接身份；IPv6、DNS/SRV、客户端证书及完整SIPS边界未实现。 | `PAIR-SECURE-TRANSPORT`：相同证书及传输策略测试握手、认证、TLS错误、连接关闭、DNS/SRV与IPv6往返。 |
| H.323、设备与内部端点 | 没有H.323/OPAL、Skinny、ALSA设备和FreeSWITCH loopback/rtc端点宿主；本机发生器不是这些端点实现。 | `PAIR-ENDPOINTS`：逐已启用端点使用原客户端和设备测试建立、媒体、转接、断连、驱动失败及内部会话生命周期。 |
| WebRTC、Verto与安全媒体 | 未实现WebSocket/Verto、ICE/STUN/TURN、DTLS/SRTP和RTCP复用。 | `PAIR-WEBRTC`：原浏览器客户端不改配置连通；采集ICE、DTLS、SRTP、重协商、NAT变化与断网恢复。 |
| RTP/RTCP与DTMF | 单音频流同编码转发、独立RTCP与telephone-event；控制面已扩展明确编码身份、双时钟及CN。每种格式的worker接收与实际双向结果须联验，不能从SDP列表推导全部终端兼容。 | `PAIR-MEDIA`：逐方向校验RTP/RTCP、DTMF事件和序列；覆盖丢包、乱序、重复、抖动、恶意包及端点变更。 |
| 多编码、重采样与转码通话 | 已有多编码SDP与测试格式，不能沿用仅PT0/8的控制协商结论；跨编码通话、重采样、重新分包及原FreeSWITCH codec宿主仍无完整验收。自有C ABI和新增原生音频实现需各自提供构建、自检及真实通话证据。 | `PAIR-CODECS`：按现场编码矩阵完成真实通话、重采样、ptime变换、质量与CPU对比，核对授权依赖。 |
| IVR、放音、收号与通话应用 | 本地号码/XML受限路由、WAV及G711提示音、RTP/INFO read、park与播放打断已实现；无完整IVR菜单、监听、停车检索或完整转接。 | `PAIR-IVR`：原IVR菜单、playback/read、超时、打断、DTMF、失败分支、转接和事件轨迹逐项对跑。 |
| 会议与混音 | 没有会议成员状态、音频混合、静音/能量检测、布局或会议录制。 | `PAIR-CONFERENCE`：多人入退会、混音、静音、音量、录制、事件及跨编码会议完整对跑，单独压测会议负载。 |
| 通话录音与媒体捕获 | 没有录音应用、媒体bug、文件格式、双声道、暂停续录与落盘故障语义。 | `PAIR-RECORDING`：核对录音起止时间、采样格式、双腿/双声道、异常断话、磁盘满、断电与文件可播放性。 |
| 语音信箱 | 没有语音信箱存储、交互菜单、邮件通知、MWI及用户访问流程。 | `PAIR-VOICEMAIL`：原用户登录、留言、播放删除、配额、通知、MWI、错误码和磁盘恢复成对验收。 |
| 呼叫中心、FIFO与坐席 | 没有坐席状态、队列策略、等待媒体、分配、超时或队列持久化。 | `PAIR-QUEUES`：多队列多坐席、重入、抢接、溢出、取消、接通竞争与重启恢复，核对统计和CDR。 |
| 路由、号码转换与业务查询 | 运行支持固定上游、本地精确号码和冻结XML目的号码条件；没有LCR、ENUM、动态分配与完整dialplan变量语义。 | `PAIR-ROUTING`：原号码规则及外部查询在相同失败条件下比对选择、重试、计费前缀和变量作用域。 |
| XML配置、directory与dialplan | 官方配置可编辑导出；可选冻结XML执行受限号码条件和顺序应用，不含完整预处理、变量展开、嵌套条件、anti-action或inline。 | `PAIR-XML`：原XML不修改加载；比对include、$${}/${}、目录查询、嵌套条件、break/continue和热重载。 |
| 动态XML HTTP回调 | 没有mod_xml_curl的请求字段、编码、缓存、绑定顺序、错误和fallback执行。 | `PAIR-XML-CURL`：用同一个可控HTTP后端核对请求字节、字段、超时、HTTP状态、not found、缓存与本地fallback。 |
| Lua、JavaScript、Python等脚本 | 没有原语言运行时和会话API宿主；可编辑脚本配置不意味着原脚本会执行。 | `PAIR-SCRIPTS`：原脚本及库不改运行；核对Session/API/Event、XML handler、回调、GC、异常和并发隔离。 |
| Event Socket、fs_cli与libesl | 已实现入站ESL认证、有限api/bgapi及真实事件子集；outbound、完整execute参数/事件与libesl对象ABI未完成。 | `PAIR-ESL`：原fs_cli/libesl无修改连接，逐字节比较认证、命令、异步结果、事件过滤/排序和断连恢复。 |
| 事件接口与外部消息系统 | 入站ESL有真实通道/应用/播放/停泊事件子集；全部事件字段、CUSTOM子类和AMQP等消息合同仍未完成。 | `PAIR-EVENTS`：核对事件全字段、偏序、订阅过滤、重复、背压、断线与外部AMQP/组播/SNMP接口。 |
| 话单、计数与投递 | JSONL事件日志不是FreeSWITCH CSV/XML/JSON/数据库CDR，字段与交付语义尚未实现。 | `PAIR-CDR`：核对接听与挂断时点、原因、字段、文件/HTTP/数据库格式、重试、重复和重启对账。 |
| 数据库、缓存和目录后端 | 没有PostgreSQL/MariaDB、Redis、Mongo、Memcache及模块数据API兼容。 | `PAIR-DATABASES`：同一数据集验证查询、事务、连接故障、重试、字符集、作用域和连接池压力。 |
| HTTAPI与外部业务接口 | 没有HTTAPI、SCGI、LDAP、SignalWire等原业务协议；HTTP管理页不是这些业务接口。 | `PAIR-INTEGRATIONS`：原外部业务系统零改动对接，核对HTTP/XML字节、认证、状态、超时、重试与副作用。 |
| 实时计费与运营商结算 | 没有nibblebill或OSP的余额控制、实时扣费、路由授权和结算语义。 | `PAIR-BILLING`：余额边界、并发扣费、断话退款、结算幂等、后端失败与CDR账目守恒。 |
| 传真、FSK与信号处理 | 没有T.38传真或SpanDSP/FSK业务执行；透明G.711转发不能替代传真端到端验收。 | `PAIR-FAX`：同一传真终端/测试页验证音频传真及T.38切换、ECM、超时、结果码和文件输出。 |
| 视频、图像与音视频容器 | SDP只接受单音频流，未实现视频编解码、视频会议、过滤、录制或图像格式。 | `PAIR-VIDEO`：多视频/音频流、H.264/VPx、容器读写、分辨率、布局、关键帧和同步逐场景对跑。 |
| 文本消息、聊天计划与MSRP | SIP MESSAGE等未列入方法分派；没有SMS/SMPP、MSRP文件传输和chatplan业务。 | `PAIR-MESSAGING`：原MESSAGE/SMPP/MSRP客户端测试编码、路由、附件、离线、重试与安全失败。 |
| 文件、音源与流式媒体 | 没有FreeSWITCH文件接口、音调/背景音源、HTTP缓存或VLC/SHOUT等执行层。 | `PAIR-FORMATS`：逐格式验证寻址、暂停、seek、循环、采样转换、流中断、缓存和文件错误。 |
| ASR、TTS与语言播报 | 没有语音识别/合成引擎或say语言规则执行；目录包含语言参数不计功能实现。 | `PAIR-SPEECH`：原识别语法与语种、播报词法、数字日期、打断、引擎故障和音频输出对比。 |
| CLI、API与服务管理 | 自有HTTP与CLI覆盖部分管理用途，不兼容原命令名、参数、文本输出及启动行为。 | `PAIR-COMMANDS`：逐注册API/应用及实际动态接口核对参数、输出、状态、错误、异步事件和服务启动/退出。 |
| 日志格式与运维集成 | 自有进程日志和JSONL有记录用途，尚未适配原日志模块格式、轮转及消费者。 | `PAIR-LOGGING`：原日志采集器不改解析，验证格式、级别、轮转、阻塞、磁盘故障及丢失统计。 |
| 准入、过载与业务保护 | 自有热更新并发/建立中/CPS保护可用；没有证明相较FreeSWITCH在相同负载更优。 | `PAIR-ADMISSION`：相同容量与业务策略成对比较准入、503/Retry-After、已建立通话、公平性和恢复。 |
| 媒体时钟与调度 | Go定时器堆和Rust就绪循环为内部实现；未兼容FreeSWITCH timer模块接口。 | `PAIR-TIMERS`：核对长短定时器、取消竞态、漂移、时钟调整、音频节奏和满载尾延迟。 |
| 诊断与测试模块 | 自有callbench和管理测试可做本机模拟；不是FreeSWITCH诊断模块或动态兼容差分器。 | `PAIR-DIAGNOSTICS`：运行原诊断API并对比输出，另验证差分器能够抓到故意注入的漏包、漏事件和错腿。 |
| 故障、排空、在线迁移与恢复 | 可排空及分片重启，但故障分片通话会丢失；控制进程故障后的无损保持和跨机接管未实现。 | `PAIR-RELIABILITY`：分别验收drain、新呼叫切换、live在途接管、回滚、控制/媒体/整机故障及话单RPO/RTO。 |
| 万路容量及是否全面超越 | 未提供同硬件同业务的FreeSWITCH成对性能结果；历史万路建成不等于万路媒体满负载通过。 | `PAIR-PERFORMANCE`：同硬件内核网卡及独立发生器，以相同编解码、转码/会议/录音/ESL负载比较CPS、并发、丢包、延迟、CPU/RSS和故障恢复。 |

## 完整目录审计表

每一行均已定位固定源码；“原模块宿主未实现”不是动态测试失败，而是当前没有可执行原模块及宿主语义的实现。此静态审计不采集原版加载或运行结果；已执行的限定成对结果请查看独立成对报告。已在vanilla配置中声明load也不代表本机曾构建或加载。

| 模块 / 目录 | 分类 | 原功能范围 | RustSwitch现状 | 注册点 | 固定来源 |
| --- | --- | --- | --- | --- | --- |
| `autotools` | SDK示例 | 外部模块autotools开发示例，源码定义mod_example | SDK示例，非业务模块 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/sdk/autotools/src/mod_example.c#L41) |
| `mod_alsa` | 协议与设备端点 | ALSA本机音频终端 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_alsa/mod_alsa.c#L44) |
| `mod_amqp` | 事件与话单 | AMQP事件发布及命令集成 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_amqp/mod_amqp.c#L43) |
| `mod_amr` | 编解码接口 | AMR媒体编解码接口 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_amr/mod_amr.c#L41) |
| `mod_amrwb` | 编解码接口 | AMR-WB媒体编解码接口 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_amrwb/mod_amrwb.c#L41) |
| `mod_av` | 业务应用 | FFmpeg音视频格式与编解码集成 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_av/mod_av.c#L50) |
| `mod_avmd` | 业务应用 | 语音信号中的提示音检测 | 原模块宿主未实现 | 4 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_avmd/mod_avmd.c#L155) |
| `mod_b64` | 编解码接口 | Base64示例媒体编码接口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_b64/mod_b64.c#L35) |
| `mod_basic` | 脚本语言宿主 | BASIC解释器及会话API宿主 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/languages/mod_basic/mod_basic.c#L43) |
| `mod_bert` | 业务应用 | 媒体误码测试应用 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_bert/mod_bert.c#L35) |
| `mod_blacklist` | 业务应用 | 黑名单查询与管理应用 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_blacklist/mod_blacklist.c#L40) |
| `mod_bv` | 编解码接口 | BroadVoice媒体编解码接口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_bv/mod_bv.c#L36) |
| `mod_callcenter` | 业务应用 | 呼叫中心队列、坐席及分配策略 | 原模块宿主未实现 | 5 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_callcenter/mod_callcenter.c#L52) |
| `mod_cdr_csv` | 事件与话单 | CSV话单输出及模板字段 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_cdr_csv/mod_cdr_csv.c#L65) |
| `mod_cdr_pg_csv` | 事件与话单 | PostgreSQL与CSV话单输出 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_cdr_pg_csv/mod_cdr_pg_csv.c#L44) |
| `mod_cdr_sqlite` | 事件与话单 | SQLite话单输出 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_cdr_sqlite/mod_cdr_sqlite.c#L77) |
| `mod_cidlookup` | 业务应用 | 主叫身份信息查询 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_cidlookup/mod_cidlookup.c#L45) |
| `mod_cluechoo` | 业务应用 | cluechoo示例应用与API | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_cluechoo/mod_cluechoo.c#L43) |
| `mod_codec2` | 编解码接口 | Codec2媒体编解码接口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_codec2/mod_codec2.c#L50) |
| `mod_com_g729` | 编解码接口 | 完整树中的G.729构建目录，仅有Makefile.am | 仅构建目录，运行产物待采集 | 0 | [源码](https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_com_g729) |
| `mod_commands` | 业务应用 | 管理API、UUID命令与状态查询 | 仅部分协议用途 | 159 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L48) |
| `mod_conference` | 业务应用 | 会议成员、混音、控制与录制 | 原模块宿主未实现 | 3 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/mod_conference.c#L46) |
| `mod_console` | 日志后端 | FreeSWITCH控制台日志输出 | 功能相近，接口不兼容 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/loggers/mod_console/mod_console.c#L36) |
| `mod_curl` | 业务应用 | 拨号应用与API中的HTTP请求 | 原模块宿主未实现 | 4 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_curl/mod_curl.c#L51) |
| `mod_cv` | 业务应用 | 视频运动检测与计算机视觉处理 | 原模块宿主未实现 | 3 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_cv/mod_cv.cpp#L51) |
| `mod_db` | 业务应用 | 核心键值存储相关API与应用 | 原模块宿主未实现 | 4 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_db/mod_db.c#L41) |
| `mod_dialplan_asterisk` | 拨号计划 | Asterisk风格拨号计划解析 | 原模块宿主未实现 | 3 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/dialplans/mod_dialplan_asterisk/mod_dialplan_asterisk.c#L37) |
| `mod_dialplan_directory` | 拨号计划 | 目录驱动的拨号计划 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/dialplans/mod_dialplan_directory/mod_dialplan_directory.c#L39) |
| `mod_dialplan_xml` | 拨号计划 | XML拨号计划条件与应用执行 | 仅部分协议用途 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/dialplans/mod_dialplan_xml/mod_dialplan_xml.c#L38) |
| `mod_directory` | 业务应用 | 按姓名查询分机的目录业务 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_directory/mod_directory.c#L39) |
| `mod_distributor` | 业务应用 | 加权分配与节点选择 | 原模块宿主未实现 | 3 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_distributor/mod_distributor.c#L43) |
| `mod_dptools` | 业务应用 | answer、bridge、playback等拨号应用集合 | 仅部分协议用途 | 145 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_dptools/mod_dptools.c#L44) |
| `mod_easyroute` | 业务应用 | 数据库驱动的简易路由 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_easyroute/mod_easyroute.c#L73) |
| `mod_enum` | 业务应用 | ENUM号码解析与路由 | 原模块宿主未实现 | 3 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_enum/mod_enum.c#L43) |
| `mod_erlang_event` | 事件与话单 | Erlang事件与控制接口 | 原模块宿主未实现 | 3 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_erlang_event/mod_erlang_event.c#L44) |
| `mod_esf` | 业务应用 | 附加SIP相关应用 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_esf/mod_esf.c#L36) |
| `mod_esl` | 业务应用 | 从FreeSWITCH业务内使用ESL客户端 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_esl/mod_esl.c#L44) |
| `mod_event_multicast` | 事件与话单 | 组播事件传输 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_multicast/mod_event_multicast.c#L48) |
| `mod_event_socket` | 事件与话单 | 入站与出站Event Socket控制协议 | 仅部分协议用途 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c#L43) |
| `mod_event_test` | 事件与话单 | 事件系统测试模块 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_test/mod_event_test.c#L35) |
| `mod_expr` | 业务应用 | 表达式计算API | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_expr/mod_expr.c#L204) |
| `mod_fail2ban` | 事件与话单 | Fail2ban相关安全事件对接 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_fail2ban/mod_fail2ban.c#L6) |
| `mod_fifo` | 业务应用 | FIFO呼叫队列与接听分配 | 原模块宿主未实现 | 6 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_fifo/mod_fifo.c#L37) |
| `mod_flite` | 语音识别与合成 | Flite本地语音合成 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/asr_tts/mod_flite/mod_flite.c#L54) |
| `mod_format_cdr` | 事件与话单 | 可配置格式的话单生成 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_format_cdr/mod_format_cdr.c#L91) |
| `mod_fsk` | 业务应用 | FSK信号与数据处理 | 原模块宿主未实现 | 4 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_fsk/mod_fsk.c#L42) |
| `mod_fsv` | 业务应用 | FreeSWITCH视频文件录制与播放 | 原模块宿主未实现 | 4 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_fsv/mod_fsv.c#L38) |
| `mod_g723_1` | 编解码接口 | G.723.1媒体负载接口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_g723_1/mod_g723_1.c#L54) |
| `mod_g729` | 编解码接口 | G.729媒体负载接口，不能仅凭模块名推断可转码 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_g729/mod_g729.c#L38) |
| `mod_graylog2` | 日志后端 | Graylog日志对接 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/loggers/mod_graylog2/mod_graylog2.c#L37) |
| `mod_h323` | 协议与设备端点 | H.323协议端点 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_h323/mod_h323.cpp#L153) |
| `mod_hash` | 业务应用 | 哈希存储与资源限制后端 | 原模块宿主未实现 | 4 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_hash/mod_hash.c#L43) |
| `mod_hiredis` | 业务应用 | Hiredis支持的数据与资源限制后端 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_hiredis/mod_hiredis.c#L38) |
| `mod_httapi` | 业务应用 | HTTP驱动的交互式电话业务协议 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_httapi/mod_httapi.c#L38) |
| `mod_http_cache` | 业务应用 | HTTP媒体对象缓存 | 原模块宿主未实现 | 6 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_http_cache/mod_http_cache.c#L87) |
| `mod_ilbc` | 编解码接口 | iLBC媒体编解码接口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_ilbc/mod_ilbc.c#L36) |
| `mod_imagick` | 媒体文件与流 | ImageMagick图像格式集成 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/formats/mod_imagick/mod_imagick.c#L65) |
| `mod_java` | 脚本语言宿主 | Java虚拟机与会话API宿主 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/languages/mod_java/modjava.c#L45) |
| `mod_json_cdr` | 事件与话单 | JSON话单生成与投递 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_json_cdr/mod_json_cdr.c#L91) |
| `mod_lcr` | 业务应用 | 最小成本路由与运营商选择 | 原模块宿主未实现 | 3 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_lcr/mod_lcr.c#L156) |
| `mod_ldap` | 目录后端 | LDAP目录后端 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/directories/mod_ldap/mod_ldap.c#L44) |
| `mod_limit` | 业务应用 | 会话资源限制API、应用及后端分派 | 功能相近，接口不兼容 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_limit/mod_limit.c#L39) |
| `mod_local_stream` | 媒体文件与流 | 本地媒体流与背景音源 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/formats/mod_local_stream/mod_local_stream.c#L40) |
| `mod_logfile` | 日志后端 | 文件日志、格式与轮转 | 功能相近，接口不兼容 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/loggers/mod_logfile/mod_logfile.c#L37) |
| `mod_loopback` | 协议与设备端点 | 内部loopback会话端点 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_loopback/mod_loopback.c#L45) |
| `mod_lua` | 脚本语言宿主 | Lua会话、API与XML处理宿主 | 原模块宿主未实现 | 4 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/languages/mod_lua/mod_lua.cpp#L44) |
| `mod_managed` | 脚本语言宿主 | 托管语言与会话API宿主 | 原模块宿主未实现 | 5 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/languages/mod_managed/mod_managed.cpp#L53) |
| `mod_mariadb` | 数据库驱动 | MariaDB数据库驱动 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/databases/mod_mariadb/mod_mariadb.c#L47) |
| `mod_memcache` | 业务应用 | Memcached缓存接口 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_memcache/mod_memcache.c#L43) |
| `mod_mongo` | 业务应用 | MongoDB数据接口 | 原模块宿主未实现 | 3 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_mongo/mod_mongo.c#L53) |
| `mod_native_file` | 媒体文件与流 | 原生编码媒体文件格式 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/formats/mod_native_file/mod_native_file.c#L35) |
| `mod_nibblebill` | 业务应用 | 通话计费与余额控制 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_nibblebill/mod_nibblebill.c#L124) |
| `mod_odbc_cdr` | 事件与话单 | ODBC话单输出 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_odbc_cdr/mod_odbc_cdr.c#L39) |
| `mod_opal` | 协议与设备端点 | OPAL协议栈端点 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_opal/mod_opal.cpp#L100) |
| `mod_openh264` | 编解码接口 | OpenH264视频编码接口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_openh264/mod_openh264.cpp#L51) |
| `mod_opus` | 编解码接口 | Opus媒体编解码接口 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_opus/mod_opus.c#L44) |
| `mod_opusfile` | 媒体文件与流 | Opus文件与流格式 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/formats/mod_opusfile/mod_opusfile.c#L60) |
| `mod_osp` | 业务应用 | OSP鉴权、路由与结算集成 | 原模块宿主未实现 | 3 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_osp/mod_osp.c#L2952) |
| `mod_perl` | 脚本语言宿主 | Perl解释器及会话API宿主 | 原模块宿主未实现 | 4 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/languages/mod_perl/mod_perl.c#L54) |
| `mod_pgsql` | 数据库驱动 | PostgreSQL数据库驱动 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/databases/mod_pgsql/mod_pgsql.c#L52) |
| `mod_png` | 媒体文件与流 | PNG图像文件接口 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/formats/mod_png/mod_png.c#L43) |
| `mod_pocketsphinx` | 语音识别与合成 | PocketSphinx语音识别 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/asr_tts/mod_pocketsphinx/mod_pocketsphinx.c#L41) |
| `mod_posix_timer` | 定时器 | POSIX媒体定时器实现 | 功能相近，接口不兼容 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/timers/mod_posix_timer/mod_posix_timer.c#L47) |
| `mod_prefix` | 业务应用 | 前缀树查询与管理 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_prefix/mod_prefix.c#L43) |
| `mod_python3` | 脚本语言宿主 | Python3解释器及会话API宿主 | 原模块宿主未实现 | 4 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/languages/mod_python3/mod_python3.c#L63) |
| `mod_random` | 业务应用 | 随机值相关API | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_random/mod_random.c#L40) |
| `mod_redis` | 业务应用 | Redis数据与资源限制接口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_redis/mod_redis.c#L37) |
| `mod_reference` | 协议与设备端点 | 端点开发参考实现 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_reference/mod_reference.c#L37) |
| `mod_rtc` | 协议与设备端点 | rtc端点接口与媒体会话 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_rtc/mod_rtc.c#L39) |
| `mod_rtmp` | 协议与设备端点 | RTMP协议端点与音视频会话 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_rtmp/mod_rtmp.c#L46) |
| `mod_say_de` | 语言播报 | 德语数字、时间等语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_de/mod_say_de.c#L54) |
| `mod_say_en` | 语言播报 | 英语数字、时间等语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_en/mod_say_en.c#L52) |
| `mod_say_es` | 语言播报 | 西班牙语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_es/mod_say_es.c#L53) |
| `mod_say_es_ar` | 语言播报 | 阿根廷西班牙语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_es_ar/mod_say_es_ar.c#L54) |
| `mod_say_fa` | 语言播报 | 波斯语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_fa/mod_say_fa.c#L58) |
| `mod_say_fr` | 语言播报 | 法语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_fr/mod_say_fr.c#L53) |
| `mod_say_he` | 语言播报 | 希伯来语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_he/mod_say_he.c#L53) |
| `mod_say_hr` | 语言播报 | 克罗地亚语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_hr/mod_say_hr.c#L53) |
| `mod_say_hu` | 语言播报 | 匈牙利语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_hu/mod_say_hu.c#L52) |
| `mod_say_it` | 语言播报 | 意大利语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_it/mod_say_it.c#L53) |
| `mod_say_ja` | 语言播报 | 日语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_ja/mod_say_ja.c#L54) |
| `mod_say_nl` | 语言播报 | 荷兰语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_nl/mod_say_nl.c#L53) |
| `mod_say_pl` | 语言播报 | 波兰语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_pl/mod_say_pl.c#L53) |
| `mod_say_pt` | 语言播报 | 葡萄牙语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_pt/mod_say_pt.c#L53) |
| `mod_say_ru` | 语言播报 | 俄语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_ru/mod_say_ru.c#L66) |
| `mod_say_sv` | 语言播报 | 瑞典语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_sv/mod_say_sv.c#L55) |
| `mod_say_th` | 语言播报 | 泰语语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_th/mod_say_th.c#L59) |
| `mod_say_zh` | 语言播报 | 中文数字、时间等语言播报规则 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/say/mod_say_zh/mod_say_zh.c#L59) |
| `mod_shell_stream` | 媒体文件与流 | 外部进程媒体流输入 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/formats/mod_shell_stream/mod_shell_stream.c#L40) |
| `mod_shout` | 媒体文件与流 | SHOUT及流式音频格式集成 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/formats/mod_shout/mod_shout.c#L52) |
| `mod_signalwire` | 业务应用 | SignalWire服务集成 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_signalwire/mod_signalwire.c#L59) |
| `mod_silk` | 编解码接口 | SILK媒体编解码接口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_silk/mod_silk.c#L37) |
| `mod_siren` | 编解码接口 | Siren语音编码接口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_siren/mod_siren.c#L43) |
| `mod_skel` | 业务应用 | 应用模块开发骨架 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_skel/mod_skel.c#L43) |
| `mod_skel_codec` | 编解码接口 | 编解码模块开发骨架 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_skel_codec/mod_skel_codec.c#L55) |
| `mod_skinny` | 协议与设备端点 | Skinny/SCCP协议端点 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_skinny/mod_skinny.c#L42) |
| `mod_smpp` | 事件与话单 | SMPP消息协议集成 | 原模块宿主未实现 | 4 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_smpp/mod_smpp.c#L38) |
| `mod_sms` | 业务应用 | 文本消息与聊天计划应用 | 原模块宿主未实现 | 9 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_sms/mod_sms.c#L41) |
| `mod_snapshot` | 业务应用 | 通话媒体片段捕获 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_snapshot/mod_snapshot.c#L41) |
| `mod_sndfile` | 媒体文件与流 | libsndfile音频文件读写 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/formats/mod_sndfile/mod_sndfile.c#L37) |
| `mod_snmp` | 事件与话单 | SNMP运行指标接口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_snmp/mod_snmp.c#L50) |
| `mod_sofia` | 协议与设备端点 | Sofia SIP端点、profile与gateway | 仅部分协议用途 | 11 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L50) |
| `mod_spandsp` | 业务应用 | SpanDSP传真及信号处理 | 原模块宿主未实现 | 20 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_spandsp/mod_spandsp.c#L53) |
| `mod_spy` | 业务应用 | 通话监听与监控业务 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_spy/mod_spy.c#L39) |
| `mod_syslog` | 日志后端 | 系统日志输出 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/loggers/mod_syslog/mod_syslog.c#L44) |
| `mod_test` | 业务应用 | 业务与媒体测试模块 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_test/mod_test.c#L39) |
| `mod_timerfd` | 定时器 | Linux timerfd媒体时钟 | 功能相近，接口不兼容 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/timers/mod_timerfd/mod_timerfd.c#L41) |
| `mod_tone_stream` | 媒体文件与流 | 音调媒体流生成 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/formats/mod_tone_stream/mod_tone_stream.c#L35) |
| `mod_translate` | 业务应用 | 号码及字符串转换规则 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_translate/mod_translate.c#L37) |
| `mod_tts_commandline` | 语音识别与合成 | 外部命令语音合成 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/asr_tts/mod_tts_commandline/mod_tts_commandline.c#L37) |
| `mod_v8` | 脚本语言宿主 | V8 JavaScript与会话API宿主 | 原模块宿主未实现 | 8 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/languages/mod_v8/mod_v8.cpp#L108) |
| `mod_valet_parking` | 业务应用 | 驻留、取回与停车位控制 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_valet_parking/mod_valet_parking.c#L45) |
| `mod_verto` | 协议与设备端点 | Verto/WebSocket电话控制及浏览器会话 | 原模块宿主未实现 | 3 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_verto/mod_verto.c#L41) |
| `mod_video_filter` | 业务应用 | 视频过滤与处理 | 原模块宿主未实现 | 4 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_video_filter/mod_video_filter.c#L39) |
| `mod_vlc` | 媒体文件与流 | VLC媒体文件与流集成 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/formats/mod_vlc/mod_vlc.c#L149) |
| `mod_vmd` | 业务应用 | 语音信箱提示音检测 | 原模块宿主未实现 | 2 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_vmd/mod_vmd.c#L126) |
| `mod_voicemail` | 业务应用 | 语音信箱存储、通知与访问 | 原模块宿主未实现 | 23 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_voicemail/mod_voicemail.c#L49) |
| `mod_voicemail_ivr` | 业务应用 | 语音信箱交互菜单 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_voicemail_ivr/mod_voicemail_ivr.c#L45) |
| `mod_webm` | 媒体文件与流 | WebM音视频容器格式 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/formats/mod_webm/mod_webm.cpp#L49) |
| `mod_xml_cdr` | XML集成 | XML话单生成与投递 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/xml_int/mod_xml_cdr/mod_xml_cdr.c#L80) |
| `mod_xml_curl` | XML集成 | HTTP获取动态XML配置、目录及拨号计划 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/xml_int/mod_xml_curl/mod_xml_curl.c#L39) |
| `mod_xml_ldap` | XML集成 | LDAP驱动的XML查询 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/xml_int/mod_xml_ldap/mod_xml_ldap.c#L133) |
| `mod_xml_rpc` | XML集成 | XML-RPC及HTTP控制入口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/xml_int/mod_xml_rpc/mod_xml_rpc.c#L76) |
| `mod_xml_scgi` | XML集成 | SCGI动态XML查询 | 原模块宿主未实现 | 1 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/xml_int/mod_xml_scgi/mod_xml_scgi.c#L37) |
| `mod_yuv` | 编解码接口 | YUV视频媒体接口 | 原模块宿主未实现 | 0 | [源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/codecs/mod_yuv/mod_yuv.c#L34) |

每行完整JSON还包含：与vanilla配置文件的对应关系、原加载声明、所有注册点ID、宿主状态、功能重叠的严格范围、RustSwitch证据以及必需成对用例。没有配置文件映射的模块仍可用高级XML编辑器创建产物，但不因此获得运行能力。

### 内建注册点

| 命令/应用 | 源码 | 状态 |
| --- | --- | --- |
| `msrp`（api） | [固定源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_msrp.c#L1890) | 未实现；原版差分未跑 |
| `uuid_msrp_send`（api） | [固定源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_msrp.c#L1892) | 未实现；原版差分未跑 |
| `msrp_recv_file`（app） | [固定源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_msrp.c#L1893) | 未实现；原版差分未跑 |
| `msrp_send_file`（app） | [固定源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_msrp.c#L1894) | 未实现；原版差分未跑 |
| `vpx`（api） | [固定源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_vpx.c#L2026) | 未实现；原版差分未跑 |

## 自有扩展不能作为全面超越的证据

| 实现 | 准确范围 |
| --- | --- |
| 热更新业务准入策略 | 并发、建立中与CPS保护；不计原limit模块兼容。 |
| Rust媒体分片与Linux批量收包 | 内部实现和隔离手段；没有同场景FreeSWITCH基准，不能宣布更快或零故障。 |
| 中文HTTP管理与文档界面 | 自有管理入口和配置产物，不能计为ESL/XML业务执行兼容。 |
| 本机真实SIP/媒体测试编排 | 有界独立实例、报告与清理；不替代独立发生器Linux容量或原版差分。 |
| 自有C codec ABI检查 | 可信G.711插件往返测试入口；不提供FreeSWITCH模块二进制兼容或通话转码。 |

目前能够说明架构选择和特定实现存在，不能说明FreeSWITCH没有类似能力，更不能把Rust语言、减少进程内共享、管理界面或单次低负载测试当作全面性能优势。完整功能集仍明显不足；功能等价未达到之前，同一编解码转发基准也不能外推录音、转码、会议与全部事件业务。

已有历史容量结果的边界见[0.2优化报告](../optimization-v0.2.md)与[可靠性审查](../optimization-review-2026-09-05.md)：曾建立万路不代表万路持续双向有效负载通过。当前Linux生产服务器验收尚未完成，跨机在途状态接管和零服务损失也未证明。

## 必需的成对验收规则

1. 这些PAIR项是验收分组，不是全部行为用例的封闭总数；每个现场启用模块须展开完整正常、异常及副作用用例，冻结真正的强制用例分母。
2. 冻结现场FreeSWITCH二进制、模块、依赖、配置、脚本和客户端哈希；无法采集的项保持未知。
3. 同一输入在参考FreeSWITCH与RustSwitch各执行，收集协议字节、事件偏序、变量、CDR、文件、外部请求和资源副作用。
4. 正常、边界、错误、并发、超时、重试、重启、负载和安全反例全部纳入；跳过、未跑和未知不得计为通过。
5. 只允许预先冻结的归一化白名单，如随机UUID与时间偏移；错误码、腿归属、关键字段和因果顺序不得随意忽略。
6. 容量比较须同硬件/内核/网卡/调频/NUMA及相同业务模块、编解码、媒体节奏和外部系统；发生器与被测机分离。
7. 至少区分持续媒体、持续建拆、转码、录音、会议、全事件订阅、过载恢复及长稳；同时报告成功率、有效负载、逐向缺包、时延分位、CPU/RSS与RPO/RTO。
8. usage兼容、万路性能、drain切换、live在途迁移分别形成结论，任何一项不通过不能用别项替代。

所有上表`PAIR-*`用例目前为`not_run`。实际认证还需使用[冻结基线模板](../freeswitch-compatibility/templates/baseline-profile.json)、[接口合同](../freeswitch-compatibility/templates/interface-contract.json)和[认证报告](../freeswitch-compatibility/templates/certification-report.json)，以及[既有验收标准](../freeswitch-compatibility/06-conformance-and-cutover.md)。未知、未跑、失败、跳过都不能进入通过分子。

## 证据定位

| 证据ID | 当前自研文件位置 | 审查含义 |
| --- | --- | --- |
| `sip_dispatch` | `control/internal/server/calls.go:34` | 仅分派列出的SIP请求；默认405，re-INVITE明确501。 |
| `sip_transport` | `control/internal/server/transport.go:36` | IPv4 SIP UDP/TCP/TLS双腿，连接身份和有界队列；WS/WSS、IPv6及完整SIPS路由未实现。 |
| `esl_transport` | `control/internal/esl/server.go:82` | 可选回环入站ESL、固定执行器与连接队列预算；完整原版Event Socket仍待补齐。 |
| `esl_api` | `control/internal/server/esl_api.go:45` | 有限API与按UUID索引的主循环操作，未知命令不虚报成功。 |
| `sdp_limits` | `control/internal/sip/sdp.go:153` | SDP拒绝保持方向、ICE、SRTP与RTCP复用等超出原型范围的协商。 |
| `sdp_codecs` | `control/internal/sip/sdp.go:289` | 控制面区分PCMU/PCMA、G722双时钟、Opus、G729、G726/AAL2各码率及L16；协商声明、worker接收、逐向有效媒体与跨编码转码必须分别验证。 |
| `codec_profiles` | `control/internal/codecprofile/profiles.go:22` | 测试格式列出载荷、采样率、RTP时钟、声道、ptime与fmtp；固定传输向量不是音质或原版编解码模块认证。 |
| `codec_answer` | `control/internal/sip/sdp.go:569` | 同格式透传应答检查；不接受要求PT改写、重分包或不同编码转换的应答。 |
| `http_admin` | `control/internal/server/server.go:261` | 当前TCP入口为自有HTTP管理服务，不是Event Socket监听。 |
| `config` | `control/internal/config/config.go:114` | 运行配置是自有JSON结构，没有FreeSWITCH模块加载及XML执行入口。 |
| `xml_export` | `control/internal/server/fs_config.go:22` | 官方XML仅作为编辑/导出产物，配置字段不进入运行功能完成分子。 |
| `native_abi` | `native/include/rustswitch_codec.h:1` | C插件使用自定义ABI，不能原样加载FreeSWITCH mod_*.so。 |
| `protocol_abi` | `native/include/rustswitch_protocol.h:1` | 原生协议适配器仅预留合同，没有实际Sofia/PJSIP提供器。 |
| `codec_mode` | `media/src/main.rs:24` | C codec往返检查与worker运行模式互斥，不在现有通话转发路径调用转码器。 |
| `codec_loader` | `media/src/codec.rs:48` | 动态库读取自有描述符入口，不提供switch_*宿主ABI。 |
| `media_commands` | `media/src/media/worker.rs:497` | 媒体命令包含分配、连接、释放、统计、关闭及按键游标/提示音；未提供混音、录音或完整IVR执行图。 |
| `media_io` | `media/src/media/io.rs:156` | Linux批量接收优化已在源码中；单个系统调用优化不能证明全面性能优于FreeSWITCH。 |
| `rtp` | `media/src/media/rtp.rs:5` | RTP/RTCP校验与转发实现，不等于终结型媒体会话、完整质量评估或DTLS/SRTP。 |
| `guard` | `control/internal/server/guard.go:223` | 自有准入策略只作用于新呼叫，语义不等于FreeSWITCH的全部limit后端。 |
| `timers` | `control/internal/server/timers.go:9` | Go内部呼叫与事务定时器，不是FreeSWITCH媒体timer插件接口。 |
| `journal` | `control/internal/journal/journal.go:18` | 自有JSONL事件模型，不是FreeSWITCH事件头或完整CSV/XML/JSON话单。 |
| `go_main` | `control/cmd/rustswitch/main.go:19` | 自有CLI、部署与日志入口，不是freeswitch或fs_cli的全部选项与服务行为。 |
| `benchmark` | `control/internal/server/benchmark_runner.go:400` | 测试创建本机独立实例，实际负载与生产容量认证分离。 |
| `callbench` | `control/cmd/callbench/main.go:475` | 发生器报告记录真实建呼与双向收包边界；不是原版FreeSWITCH成对差分结果。 |
| `e2e` | `tests/e2e.py:237` | 项目自有回归用例存在；本审计只静态枚举，不把定义计作本轮执行通过。 |
| `failure` | `media/src/media/worker.rs:217` | 控制管道断开使媒体退出；进程隔离并未实现控制面崩溃后的存量通话无损接管。 |

以上本地行号与文件SHA-256收录在[完整审计JSON](feature-audit.json)。源码引用是定位依据，不是未提供的远程链接。全量目录原始数据来自[固定源码catalog](../freeswitch-compatibility/catalog/source-catalog.json)。

## 可重复静态校验

在项目根目录执行；默认参考目录为工作区已保存的固定源码及完整Git树，也可用`--source`和`--tree`显式指定。生成器只更新本审计的Markdown/JSON。

```sh
python3 tools/build_feature_audit.py
python3 tools/build_feature_audit.py --check --self-test
```

`--check`逐文件核验参考SHA-256/Git blob、完整树未截断、145条并集唯一覆盖、538注册点分区、证据行号与所有当前运行源码哈希，并验证已提交文档与重新生成结果一致。`--self-test`确认重复/遗漏条目与注释伪模块会被检测。失败会返回非零，不会静默修改文档。

默认校验不联网、不编译FreeSWITCH、不启动服务、不发媒体，也不运行FS动态差分。`--verify-latest`可选通过官方GitHub最新发行API只读复核；发现版本变更就失败，要求人工更新冻结基线，不自动把移动版本当作已审计版本。

本轮实际完成以上静态生成和复验，定义枚举到17个项目e2e用例，但没有把用例定义冒充本轮运行。所有动态兼容、生产容量、drain/live切换与超越结论继续保持未认证。
