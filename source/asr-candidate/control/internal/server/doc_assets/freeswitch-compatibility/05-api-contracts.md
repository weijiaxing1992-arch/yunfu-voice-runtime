# 05 · API 命令与业务行为契约

## 5.1 接口层次

必须区分 ESL 传输命令、FreeSWITCH 注册 API、拨号计划应用、JSON API 和 Chat 应用。同名不代表同参数或同执行上下文。例如 API `sched_hangup` 显式传 UUID，拨号计划应用使用当前会话；两者必须建立不同记录。[固定注册来源](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7645) · [应用注册](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_dptools/mod_dptools.c#L6628)

本章加上 [538 个宏注册位置目录](catalog/api-and-applications.md) 定义 API 覆盖工作的入口。静态目录尚不是 538 份经过原版运行验证的完整语义合同。每个候选接口必须展开子命令和配置条件，填写 [契约模板](templates/interface-contract.json) 并关联实测。

## 5.2 语法不可以只靠 help 推断

注册表中的语法是原版帮助元数据，参数最终由处理函数解析。固定版本中 `create_uuid` 注册帮助引用了 `UUID_SYNTAX`，其文本是两个 UUID 参数，而实际 `uuid_function` 直接生成 UUID；`uuid_bridge` 注册帮助为空，处理函数却要求 UUID，并有备用目标分支。替换实现既要保持原帮助输出，又要保持实际执行语法，不能将帮助文本直接当作唯一校验器。[帮助注册](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7653) · [UUID 生成](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L3192) · [桥接解析](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L4720)

必须分别记录：公布语法、实际接受语法、无效输入处理。`NULL`/空帮助不表示无参数；宏或运行期拼接不表示可任意透传；HTTP 的 `json` 输出选项不表示该模块提供 OpenAPI/REST 服务。

## 5.3 通用 API 要求

| 编号 | 强制要求 |
|---|---|
| API-R01 | 入口大小写、前后空格、引号、反斜杠、Unicode、分隔符、可选尾参和变量展开保持原行为；不全局套用 shell 或 JSON 解析规则 |
| API-R02 | 同步 API 的返回正文与调用层状态分开；正文可以包含错误，模块处理函数的成功状态不能被误解为业务成功 |
| API-R03 | api、ESL bgapi、API 名为 bgapi 的包装、脚本 API.execute、JSON API 和 HTTP 包装各自测试；不能默认不同入口的鉴权/展开/返回都相同 |
| API-R04 | 无通道、正在结束通道、跨进程通道、A/B腿、已桥接/未桥接、未应答/已应答分别验证；不能只在单路正常接通状态下测试 |
| API-R05 | 先明确线性化点与并发竞态的原版允许结果集合，再比较命令完成、相关事件及最终状态；不擅自增加全局串行顺序 |
| API-R06 | 调用超时、断连和重试不代表未执行；按原版说明任务是否继续及如何关联，不增加未经协议约定的幂等键 |
| API-R07 | 参数长度、数字范围、缺值、空值、无效目标、权限、资源不足等错误分别保留；不得统一 +OK、HTTP 200 或一个泛化 -ERR |
| API-R08 | 查询命令必须读取实时对应状态，控制命令必须产生真实副作用；模块列表、统计与事件不能使用静态假数据 |
| API-R09 | 破坏性命令的语义仍要兼容，但只在隔离实验基线运行，不能把清单采集器变成 load/unload、重启或挂机脚本 |

## 5.4 核心命令族与必验行为

语法逐项见注册目录；下表补充不能由语法推断的合同。表中入口为代表，所在模块的其余注册和子命令仍然强制收录。

| 命令族 | 入口 | 必须验收的行为 |
|---|---|---|
| 发现与状态 | version/status/uptime/banner/help/show/console_complete | 原 fs_cli/SDK 可解析；short、CSV/XML/JSON、排序、表头、空列表、未知类型、实例身份与启动时间 |
| 原版控制 | fsctl/load/unload/reload/reloadxml/reloadacl | 所有子命令；新旧呼叫配置可见点、重载失败处理、模块在用时卸载结果、事件与资源生命周期 |
| UUID | create_uuid/uuid_exists/uuid_dump | 配置的 UUID 版本、唯一性、查询值、通道不存在/结束中行为；dump字段与可选格式 |
| 发起呼叫 | originate | originate字符串语法、A/B腿变量、串行/并行目标、origination_uuid、caller ID、早期媒体、超时、取消、失败原因、返回对象 |
| 桥接与转接 | uuid_bridge/uuid_transfer/uuid_dual_transfer/uuid_simplify | 目标腿选择、已桥接腿处理、备用目标、上下文、拨号计划入口、转接后续行与旧腿销毁 |
| 接通与结束 | uuid_answer/uuid_pre_answer/uuid_ring_ready/uuid_outgoing_answer/uuid_kill/hupall | 180/183/200与媒体状态；重复调用、指定cause、过滤范围；受理后真实挂断与话单完成 |
| 会话变量 | uuid_getvar/uuid_setvar/uuid_setvar_multi | 未定义/空字符串/删除、数组索引、多值变量、重复键、顺序更新、部分非法数据与副作用 |
| 全局及目录变量 | global_getvar/global_setvar/domain_data/user_data/user_exists | 查询来源、缓存、作用域、权限、域隔离、空值与未找到结果 |
| 表达式与包装 | eval/expand/escape/url_decode/url_encode/regex/cond/coalesce/json/xml_wrap | 原表达式与编码规则、嵌套命令授权、参数边界、输出类型；不能让包装绕过权限 |
| 放音与中断 | uuid_broadcast/uuid_displace/uuid_fileman/uuid_break | 路径或应用语法、A/B/both、排队、停止当前或全部、文件句柄、完成/打断事件、真实音频 |
| DTMF | uuid_send_dtmf/uuid_recv_dtmf/uuid_flush_dtmf/uuid_drop_dtmf | 发送与注入不同；持续时间、队列、mask、转发/检测模式、一次用户按键的业务效果 |
| 保持与媒体变更 | uuid_hold/uuid_pause/uuid_media/uuid_media_3p/uuid_media_reneg | SIP保持、媒体暂停、bypass/proxy、重新协商、失败回退；不将不同功能映射到单一暂停标志 |
| 录音 | uuid_record、相应 record_session 应用 | start/stop/mask/unmask、路径与变量、方向、限制；文件与事件、通话结束自动关闭、I/O故障 |
| 调度 | sched_api/sched_hangup/sched_transfer/sched_broadcast/sched_del/unsched_api | 相对/绝对/周期时间、组与任务ID、取消竞争、结束后的任务处理、时钟变化和重启边界 |
| 资源与数据 | limit*/hash*/db*/group*/fifo* | 所用后端和SQL语义、原子操作、TTL/配额、多实例作用域、错误恢复，不能用内存占位替代持久行为 |
| SIP管理 | sofia/sofia_contact/sofia_count_reg/sofia_presence_data 等 | profile/gateway/注册/contact/状态/trace/恢复子命令；命令层结果与SIP网络变化对应 |
| 会议 | conference 与相关应用 | 创建、加入、成员ID、mute/deaf/kick、播放、录音、转移、布局/视频、事件与XML/JSON状态 |
| 呼叫中心 | callcenter_config 与 callcenter 应用 | queue/agent/tier全部子命令、代理status/state、队列分配、超时、恢复、SQL与CUSTOM事件 |
| 语音信箱与业务 | voicemail*/ivr/say/phrase、所用传真/TTS/ASR模块 | 用户目录与邮箱、原存储格式、提示音、收号分支、外部引擎结果与失败行为 |
| 对外消息 | chat/uuid_send_message/uuid_send_info、AMQP/事件类API | 目的路由、内容类型、返回与交付区别、权限、事件/消息头、重试与故障域 |

以上是验收族划分，不把名称相似但未注册的字面字符串视为真实入口；带 `*` 和“等”表示按固定版本目录和现场运行清单展开。模块缺失时原版的错误也是该 profile 的行为；全模块产品目标还需在启用该模块的 profile 验证正向功能。

## 5.5 几个可明确到字节的源码契约

下列结果来源于固定源码。本轮已运行原版并对缺失 UUID、语法及有限 API 分支取得成对证据，具体已执行输入见 [成对报告](../api/conformance-report.md)；不能把这些分支的通过扩大为表中全部状态、存在通道的副作用或完整 API 合同已验证。`\n` 表示一个 LF 字节，客户端应比较真实字节而非反斜杠文本。

| 输入与前提 | 参考 API 正文 | 必须保留的区别 |
|---|---|---|
| `uuid_exists U`，U 不存在 | `false` | 没有 +OK，源码不追加 LF |
| `uuid_exists U`，U 存在 | `true` | 不是 JSON 布尔信封，不等于该通道已接通 |
| `uuid_getvar U missing_key`，U 存在且无该变量 | `_undef_` | 不替换成 null、空字符串或 404 |
| `uuid_getvar U x`，U 不存在 | `-ERR No such channel!\n` | 与变量不存在不同 |
| `uuid_setvar U key`，U 存在 | 调用成功时 `+OK\n`，缺值进入删除/空值的原 setter 语义 | 必须继续读取变量，不能只验 +OK |
| `uuid_kill U`，U 不存在 | `-ERR No such channel!\n` | 不能因幂等设计自行改为成功 |

来源：[exists/getvar/setvar](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L6144) · [kill](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L2880)

`uuid_setvar_multi` 逐段处理变量，不应凭命令名自行实现为全有或全无的数据库事务；坏片段可能与成功片段并存。必须构造混合有效/无效输入观察多行返回和部分写入，不偷偷“修正”为整体回滚。[源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L6197)

示例：在确定 U 不存在的隔离场景，客户端请求是 `api uuid_exists U\n\n`；服务器外层为 `api/response`，正文长度 5，正文 `false`。外层帧要求见 ESL 章节，不能把正文的 false 当作 TCP、鉴权或解析失败。

## 5.6 originate 的独立合同

必须建立字符串文法回归集，覆盖括号、花括号、方括号、分隔符、转义和变量展开。保持超时值单位，endpoint拨号串、网关、循环回环、user路由和应用尾部参数各自解析；不能只按空格拆开。基线模块支持的并行/顺序fork需展开胜出、早期媒体、迟到200和全部失败场景。

`origination_uuid` 是通道关联，ESL Job UUID 是后台任务关联；两者不能互相代替。调用返回、通道应答、应用完成、B腿结束和CDR交付是独立时点。拒接/忙线/超时后的API正文、hangup_cause、originate_disposition、SIP码和事件必须一致。[调用入口](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L5107) · [originate核心](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_ivr_originate.c)

## 5.7 子命令与动态接口闭合

conference、sofia、callcenter_config、fsctl、limit、hash、db、voicemail及模块管理命令均可能只注册一个顶层名称，再由处理函数分派多种命令。目录必须递归到实际可执行分支：作用域、命令路径、参数、选项与输出格式形成不同用例，不以顶层注册数代替子命令数。

源码候选与参考机 `show api/application/interfaces`、`help` 和实际模块调用清单合并；条件编译、生效模块、接口允许列表、别名、JSON与语言扩展再补齐。对未出现但配置可达的功能主动构造输入，不用一次业务抓包证明不存在。

已补充 [Conference与Sofia子命令目录](catalog/conference-and-sofia-subcommands.md)：会议静态表84条声明与6个全局入口、Sofia 38个解析路径候选及2个仅帮助文本候选。它们保留分发名与帮助名差异，仍需逐处理器展开参数及运行验收。

## 5.8 API 验收用例

每个用例族按适用的全部命令展开，表中 20 项不是只运行 20 次即能认证。

| ID | 场景 | 判定 |
|---|---|---|
| API-T01 | 全部发现接口及所有输出格式，含空集合/未知命令 | 实际注册与帮助/列表结构一致，未声明接口零漏项 |
| API-T02 | 每个命令的合法输入、缺参、多参、空值、Unicode和边界 | 原生响应及副作用匹配，不由统一解析器改变文法 |
| API-T03 | uuid查询存在/不存在/结束中，变量未定义/空值/数组 | 正文逐字节、值类型与通道状态一致 |
| API-T04 | 同一变量多次写、删除、多值、批量混合非法片段 | 部分成功与最终值符合参考，无隐式事务增强 |
| API-T05 | 100个不同originate Job UUID/通道UUID混合并发 | 一一关联，无串腿、假完成或自动重复去重 |
| API-T06 | originate忙线/拒接/超时/早期媒体/取消及迟到接通 | API/事件/原因码/实际媒体与最终资源一致 |
| API-T07 | 顺序与并行originate目标、特殊字符和变量作用域 | 胜出腿、剩余腿清理及变量传播一致 |
| API-T08 | 两腿和多腿桥接，目标消失、备用目标、重复桥接 | 返回目标及真实桥接对象一致，无旧腿残留 |
| API-T09 | A/B/both转接、不同context/dialplan、咨询转接失败 | 业务执行位置和后续通道关联保持一致 |
| API-T10 | 同一腿执行放音、break、转接、挂机竞争 | 只接受基线允许结果；无死锁与完成事件伪造 |
| API-T11 | DTMF生成/注入/清队列/mask与应用收号 | 数字、时长、终止键、业务分支和媒体匹配 |
| API-T12 | 录音全部动作，路径与磁盘故障、转接跟随 | 声道、文件、事件、API错误与生命周期匹配 |
| API-T13 | sched全部时间模式，取消边界、时钟变更 | 任务ID、执行次数、取消后的事件与业务结果一致 |
| API-T14 | reloadxml/profile/模块忙碌重载及无效配置 | 既有/新呼叫可见点一致，不静默丢配置 |
| API-T15 | 受限目录API与eval/expand/json等嵌套包装 | 与基线同权限；不能通过包装越权 |
| API-T16 | API返回前/后断连，客户端按旧策略重试 | 实际执行与重复副作用可对账，无虚假幂等 |
| API-T17 | conference全部成员与媒体子命令 | 状态、事件、输出和实际多方音频均匹配 |
| API-T18 | callcenter/voicemail/FIFO/limit全部启用子命令 | 原数据库/后端、状态机、结果与故障恢复一致 |
| API-T19 | API、脚本、JSON/HTTP相同底层操作的入口组合 | 各入口独立满足参数、权限、等待、格式与完成语义 |
| API-T20 | 万路双腿媒体期间运行现网API与全量/窄事件订阅 | 控制延迟、拒接、事件缺口、媒体SLO和资源均有证据 |
