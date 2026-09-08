# 03 · 配置、拨号计划与业务集成兼容契约

本章定义“更换服务程序后，既有配置文件、业务脚本、HTTP 后端和话单消费者继续工作”的验收标准。固定参考是 FreeSWITCH **v1.11.3，提交 `ef32e205295e29f034f1453ad245ba5efb07b94a`**。版本、编译选项、依赖库和实际加载模块共同决定接口集合；本章的重点契约不替代逐模块接口清单。

**当前应用版本 RustSwitch 0.3（文档发行版本 1.5）仍未完成本章所列 FreeSWITCH XML 运行语义、dialplan、脚本、CDR、HTTAPI 或 AMQP 兼容。现有 XML 模板编辑与受限导入/导出、自有 JSON 配置、HTTP 管理接口及有限 ESL 事件，不能作为本章完整接口已经兼容的证据。** 下文“必须”指目标要求；用例均为待实现、待运行的验收定义。

## 3.1 兼容结果与配置输入

同一基准配置、脚本和受控外部响应，必须得到同一业务路由、呼叫腿关系、变量值、应用执行次序、外部请求及最终话单。不得要求用户把 XML 改写为 RustSwitch JSON、把 Lua 重写成 Go，或修改原有 HTTP 后端字段后才称为无感替换。Rust/Go/C++ 如何划分属于内部实现。

每一项配置参数必须登记：模块与文件、XML 路径、参数名及大小写规则、类型、单位、有效范围、源码默认、随包默认、重复值处理、未知值处理、启动或运行时生效点、依赖项、失败表现、对应验收用例。**“源码默认”和“vanilla 配置默认”必须分列**，不能合并为一个默认值。

| 兼容面 | 必须保留的输入与行为 | 验收观察点 |
|---|---|---|
| XML 注册表 | `document/section` 与配置模块的查询键；`configuration`、`directory`、`dialplan`、`phrases`、`languages` 等实际使用 section；节点/属性/文本/实体解析 | 查询得到的目标节点及未命中结果；XML 语法错误与配置缺项的区分 |
| 主配置及包含文件 | `freeswitch.xml`、`vars.xml`、`autoload_configs/`、`directory/`、`dialplan/`、`sip_profiles/`、`lang/`；相对目录、通配符、包含次序及重复定义 | 预处理后的 XML 与实际路由顺序 |
| 预处理指令 | `X-PRE-PROCESS` 的 `set`、`include`、`exec`、`exec-set`、`env-set`、`stun-set`；该版本支持的注释式预处理写法应分别取样 | 设置/读取时点、相对工作目录、标准输出处理、失败与递归上限 |
| 两阶段变量 | `$${name}` 的配置展开与 `${name}` 的运行时展开；转义、嵌套、未定义值和内联 API | 修改全局变量前后、`reloadxml` 前后、同一通话不同执行时点 |
| 模块加载 | `pre_load_modules.conf.xml`、`modules.conf.xml`、`post_load_modules.conf.xml` 与 `load/reload/unload` 的依赖顺序 | XML provider 在消费者初始化前可用；加载失败不得报成功 |
| 热更新 | `reloadxml`、模块自己的 reload/rescan、目录缓存清理各自的范围 | 配置重新解析不等价于所有模块都已重新加载；在途呼叫、新呼叫分别观察 |

预处理必须复现 FreeSWITCH 的读取流程，不可仅用通用 XML 解析器替代；例如包含包装和预处理指令的处理次序会改变结果。源码入口为 [`switch_xml.c` 的预处理实现](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_xml.c#L1462)。模块装载配置见[官方模块加载说明](https://developer.signalwire.com/freeswitch/configuration/module-loading/)。

配置解析与认证/计费有关的异常必须显式记录，不能悄悄套用一份新默认配置。若为了隔离进程或运行权限调整 `exec`、脚本目录、外部库访问等行为，该项必须登记为兼容差异；不能在验收中排除后仍声称全量兼容。

## 3.2 Directory 与租户边界

| 对象 | 字段契约 | 必须覆盖的语义 |
|---|---|---|
| Domain | `name`、`params`、`variables`、`profile-variables`、`groups/users` | 域名解析与匹配；同名分机在不同域内分离 |
| Group | `name`、用户列表、用户指针及继承参数 | 分组查询、用户指针解析、重复用户的查找次序 |
| User | `id`、`number-alias`、`type`、`cacheable`、`params`、`variables` | 用户不存在、存在但无认证参数、别名命中、静态与动态目录结果 |
| 认证/业务参数 | `password`、`a1-hash`、`dial-string`、`vm-password` 等实际模块使用值 | 消费者读取方式；Digest/注册的线协议见信令章节，目录不得自行改变认证域 |
| 业务变量 | `user_context`、`domain_name`、`accountcode`、主叫显示变量及用户自定义字段 | 进入呼叫腿的时点、覆盖优先级、桥接传播和话单归属 |

用户合并时，同名参数/变量以 user 已有值优先，再补 group，再补 domain；`params`、`variables`、`profile-variables` 要分别处理。动态目录的缓存属于核心用户查询层，不是 `mod_xml_curl` 通用 HTTP 缓存。`cacheable` 数值按毫秒计算，非空非数字走无期限缓存分支，不能把字符串 `false` 想当然解释为不缓存。必须测试过期边界、清缓存、reload 与同名跨域用户。依据：[`switch_xml_merge_user` 与用户缓存实现](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_xml.c#L2051)。

RustSwitch 内部租户键必须包括域/用户/所需 profile 维度；外部仍保持原有 FreeSWITCH 字段。域名并非自动涵盖所有资源的安全隔离：共享全局变量、ESL 管理权限、脚本、缓存、录音目录、队列和会议的原有命名方式都要纳入迁移清单。验收须使用两个租户的相同分机号、相同业务名及不同密码，检查认证、目录缓存、路由、媒体、录音、事件和 CDR 均无串用。

## 3.3 XML Dialplan 是执行语义，不只是 XML 格式

| 项目 | 必须复现的契约 |
|---|---|
| Context/extension | 选择 context，按文档顺序遍历 extension；extension 的 `continue` 控制后续 extension 是否继续评估 |
| Condition | 字段读取、`${}` 展开、表达式、日期时间条件、时区、无 field 的条件；多 regex 的 `all/any/xor` 与捕获值 |
| `break` | 缺省 `on-false`；`on-true`、`on-false`、`always`、`never` 分别基于该条件结果控制后续处理；不能实现为普通语言的全局 break |
| action/anti-action | 当前条件结果选择分支；保持文档次序；非空元素文本优先于 `data`；`loop` 的默认与零/负/非数字边界 |
| `inline` | 路由评估时立即执行；仅允许模块注册标志包含 `SAF_ROUTING_EXEC` 的应用；未注册应用与已注册但禁止 inline 的失败行为分别测试 |
| 普通执行 | 路由阶段构建应用队列，运行阶段执行；不能把所有应用都提前执行，或把全部 `${}` 都冻结为同一个时间点 |
| 捕获与展开 | 条件捕获 `$1…`、`DP_MATCH`、变量及内联 API 不是一种替换；保持捕获覆盖、空值、转义及展开时点 |
| 嵌套 | condition 的直接子 condition 递归评估；读取父 condition 的 `require-nested`，默认 true；嵌套失败、break 和 anti-action 共同影响结果 |
| 路由改变 | `transfer`、`execute_extension`、bridge 返回、hangup 后可执行动作及重入限制；保留 caller profile/callflow 历史 |

实现必须以参考解释器的控制流程为准：先评估条件并处理 action/anti-action，再检查 break，只有未被 break 截断且 `proceed` 非零时才进入嵌套条件。该版本 anti-action 分支也可能把 `proceed` 设为非零。因此不能把嵌套规则简化成“父条件正则为真才会执行子条件”，也不能把 peer condition 无条件归约为一次布尔 AND。具体依据：[`parse_exten`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/dialplans/mod_dialplan_xml/mod_dialplan_xml.c#L467)。

另一个需要差分的边界：不存在的 inline 应用会产生 `DESTINATION_OUT_OF_ORDER` 挂断路径；存在但没有 `SAF_ROUTING_EXEC` 的应用在 `exec_app` 中走拒绝执行分支，不能自动当成同一种挂断。依据：[`exec_app`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/dialplans/mod_dialplan_xml/mod_dialplan_xml.c#L47)。

建议路由兼容执行器输出仅供测试的执行轨迹：`context → extension → condition序号 → 判断结果 → 捕获值 → inline/queued应用 → 当时变量快照 → 状态改变`。比较时 UUID 使用双射映射，不能删除主被叫、租户、失败原因或应用参数等真实业务差异。

若参考构建加载 `mod_sms`，还必须适配独立的 XML chatplan：消息事件头、正文/内容类型、发送方与接收方、context、应用/anti-action、inline 与投递结果均入契约。不能直接套用音频 dialplan 的嵌套解释器；该版本 chatplan 遇到子 condition 会记录禁止嵌套并退出相应解析路径。依据：[`mod_sms` 条件处理](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_sms/mod_sms.c#L142)。

## 3.4 常用变量与应用的最低契约

以下为优先实现集合，**不是“只兼容这些就算 100%”**。完整目标还包含参考构建已加载模块注册的所有应用/API、模块配置及用户自定义变量。

| 变量组 | 代表字段 | 值与生效点要求 |
|---|---|---|
| 身份与关联 | `uuid`、`call_uuid`、`bleg_uuid`、`bridge_uuid`、`channel_name`、`direction` | 呼叫腿/逻辑通话/桥接关联分开；创建、分叉、转接及桥接解除后均核对；不得把 SIP Call-ID 当作所有 UUID |
| 路由/租户 | `destination_number`、`context`、`domain_name`、`user_context`、`accountcode` | 保持缺省、目录注入、transfer 后取值和跨腿传播 |
| 主叫/被叫 | `caller_id_name`、`caller_id_number`、`effective_caller_id_*`、`origination_caller_id_*`、`callee_id_*` | caller profile 与 channel variable 分开；修改变量后何时影响 SIP/事件/CDR 由执行路径决定 |
| 超时/桥接结果 | `call_timeout`、`originate_timeout`、`leg_timeout`、`continue_on_fail`、`hangup_after_bridge`、`ignore_early_media`、`originate_disposition`、`bridge_hangup_cause` | 秒/毫秒不能混用；原因列表、布尔值、分叉和早期媒体对计时的影响逐项验收 |
| 媒体 | `absolute_codec_string`、`codec_string`、`bypass_media`、`proxy_media`、`rtp_secure_media`、`read_codec`、`write_codec` | 设置时点与实际媒体模式对应；返回字符串不能替代转码/安全媒体能力 |
| DTMF/录音 | `playback_terminators`、`playback_terminator_used`、`read_terminator_used`、`RECORD_STEREO`、`recording_follow_transfer` | 消费端读取的大小写规则、终止键是否进入结果、录音跨转接时的文件和声道 |
| 结算时间 | `start_stamp`、`answer_stamp`、`end_stamp`、`duration`、`billsec`、`mduration`、`billmsec`、`hangup_cause` | 未接通、早挂断、长呼叫、时间精度/取整和时区按相应生成器取值；不得用内部事件日志估算代替 |
| 执行钩子 | `execute_on_answer`、`api_on_answer`、`api_hangup_hook`、`session_in_hangup_hook` | 钩子作用的呼叫腿、执行时点、可读状态及失败传播 |

变量处理必须覆盖全局、呼叫腿、caller profile、应用局部作用域和新建腿参数。`set/unset`、`export`、`export nolocal:`、`bridge_export`、`{…}`、`[…]`、`<…>`、`local_var_clobber` 各自记录覆盖顺序；不可把它们全部实现为同一张可变 map。用户任意自定义字段必须原样贯穿其应有的作用域。参考：[`switch_channel` 的变量与 export 实现](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_channel.c#L1255)、[`switch_ivr_originate`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_ivr_originate.c#L3105)。

| 应用族 | 代表入口 | 除参数可解析外，必须验证 |
|---|---|---|
| 变量/条件 | `set`、`unset`、`multiset`、`export`、`bridge_export`、`set_global`、`set_profile_var` | 空值/删除、分隔符、作用域、路由时与运行时可见性 |
| 接通/结束 | `ring_ready`、`pre_answer`、`answer`、`hangup` | 180/183/200 与媒体状态、重复执行、挂断原因及事件 |
| 桥接/路由 | `bridge`、`transfer`、`execute_extension`、`park` | 双腿生命期、串行/并行目标、超时/取消竞争、返回后下一应用是否执行 |
| 播放/收号 | `playback`、`read`、`play_and_get_digits`、`sleep`、`break` | 真实音频、打断、终止符、位间/总超时、重试、无输入/无效输入 |
| 录音 | `record`、`record_session`、`stop_record_session`、`record_session_pause/resume` | 文件格式、暂停时间轴、停止/挂断关闭、双声道、事件与失败变量 |
| 定时 | `sched_hangup`、`sched_transfer`、`sched_broadcast`、`sched_cancel` | 相对/绝对时间、任务 ID、归属组、取消竞争、通话结束后的任务处理 |
| 业务模块 | `conference`、`callcenter`、`fifo`、`voicemail`、`ivr`、`phrase`、`say`、ASR/TTS/传真等已启用入口 | 会议成员、队列代理、语音信箱、状态数据库、事件与音频效果；不可用普通 bridge 冒充 |

应用注册与可执行标志以固定版 [`mod_dptools`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_dptools/mod_dptools.c#L6628) 及各业务模块为准。按基线逐路径验证应有的状态、副作用与事件存在条件；无副作用应用、拒绝、未开始和异常中断不强制套用同一完成事件模板。需要实际业务效果的应用不能用空函数成功返回替代。

## 3.5 `mod_xml_curl` 的 HTTP 契约

### 请求

| 字段/设置 | 契约 |
|---|---|
| 传输 | 默认 POST；`Content-Type: application/x-www-form-urlencoded`；`User-Agent: freeswitch-xml/1.0` |
| 核心字段 | `hostname`、`section`、`tag_name`、`key_name`、`key_value`；hostname 来自 switchname；可空查询键保留空字符串 |
| 查询上下文 | 核心目录查询会增加 `key`、`user`、`domain`、`ip` 中实际存在的字段；呼叫方还可带 `action`、SIP 认证、Caller-*、variable_* 等事件头。它们不是所有请求都必填 |
| `enable-post-var` | 对事件参数应用允许列表；核心五字段不受该列表过滤；不得因为客户端不认识自定义字段就擅自删去 |
| 编码 | 五个核心字段由固定格式直接拼接，额外事件的值由 `switch_event_build_param_string` URL 编码，键保留原名；必须按抓包验证空格、`+`、`%`、`&`、UTF-8 与重复键，不得假设所有字段统一经过标准 form encoder |
| 非 POST method | 配置的 method 保留；参数附加到 URL 查询串，已有 `?` 时使用 `&`；动态 URL 展开及 `file:` 读取路径另行覆盖 |
| 请求选项 | URL、凭据、auth scheme、cookie、证书/主机校验、客户端证书、本地绑定、timeout、重定向、响应大小上限全部纳入基准配置 |

请求构造依据：[`xml_url_fetch`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/xml_int/mod_xml_curl/mod_xml_curl.c#L141) 和 [`switch_event_build_param_string`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_event.c#L2555)。基准必须同时保存原始 HTTP 和解码后的多值字段，禁止仅比较最后一个同名值而漏掉编码/重复键差异。

该版本五字段的 `basic_data` 缓冲为 512 字节，hostname 临时缓冲为 256 字节；长字段边界须留单独样例。若候选修复长度截断、歧义编码或其他既有缺陷，要登记差异与受影响的输入，不能既改变外部请求又标成该样例字节兼容。

### 返回、错误与 fallback

**本章采用固定源码行为。官方新版 XML Curl 手册中“绑定 section 后排他、不回退本地”的叙述，与该版本 `switch_xml_locate` 的实现不一致，不能据此设计替代程序。** 手册页为[XML Curl and HTTAPI](https://developer.signalwire.com/freeswitch/integration/xml-curl/)，判定依据为 [`switch_xml_locate`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_xml.c#L1795)。

| 外部响应 | 固定参考路径 | 必须验证 |
|---|---|---|
| HTTP 200 + 可解析且含目标的 XML | provider 返回 XML，核心找到指定 section/tag/key | 按返回节点工作 |
| HTTP 200 + `section name="result"` / `result status="not found"` | 核心继续查后续匹配 binding；无结果时查本地 XML root | 第二 HTTP 后端命中、本地命中、本地也未命中三种结果 |
| 非 200、无法解析的 XML、回调无结果 | provider 返回 NULL 或核心拒绝错误 XML；继续 provider 链，最终可查本地 | 204/404/500/连接拒绝/超时/空正文/非法 XML，分别捕获后续请求次序 |
| 可解析 XML，但缺所查 section 或 tag/key | 选中该 XML 后，查找失败转本地 XML root；不会为该情况再恢复遍历后续 binding | 必须与明确 not-found 区分，不允许统一实现成 HTTP failover |
| 超过响应限制/临时文件写失败 | fetch 无可用结果，后续由核心查找路径决定 | 默认限制 1 MiB、限制前后各一字节、写失败、错误计数及本地结果 |

`mod_xml_curl` 只将 HTTP **200** 的正文送入解析路径，不能把“任意 2xx 都成功”照搬过来。接收到 200 但传输中途出错的情况，仍需按 curl 结果、文件完整性与 XML 解析的组合做差分，不能仅依据状态码。模块内没有通用“所有 section 永久缓存”；Directory 用户缓存见 3.2。以上 fetch 行为依据 [`mod_xml_curl` 返回处理](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/xml_int/mod_xml_curl/mod_xml_curl.c#L296)。

## 3.6 脚本兼容层

脚本必须在原入口、原参数和原对象模型下运行。C/C++ 生态保留不等于 FreeSWITCH 语言模块的二进制可直接装载：其底层 `switch_*` 与会话对象依赖需要兼容适配，必须逐接口验证。

| 模块 | 调用入口 | 对象/运行契约 |
|---|---|---|
| Lua / `mod_lua` | dialplan `lua`；同步 API `lua`；后台 API `luarun` | `session`、`argv`、`stream`、`freeswitch.API/Session/Event/EventConsumer`、日志、全局变量；XML handler 的 `XML_STRING`、section/tag/key/params；事件 hook/startup script |
| JavaScript / `mod_v8` | `javascript`、`jsapi`、`jsrun`；版本中的 `jsps/jsmon/jskill` | V8/绑定版本、`Session` 和事件/API 对象、参数/异常/后台任务、XML handler；不可假设 Node.js 环境等价 |
| Python 3 / `mod_python3` | `python`、`pyrun` | `freeswitch` 绑定、handler/API 函数调用约定、模块查找、GIL/回调交互、异常及输出；不是简单命令行启动同名文件 |
| 其他已启用语言 | Perl/Java 等实际构建入口 | 单独纳入注册清单，不能因本表只详列三种语言而豁免 |

入口已核对固定源码：[Lua](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/languages/mod_lua/mod_lua.cpp#L687)、[V8](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/languages/mod_v8/mod_v8.cpp#L1578)、[Python 3](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/languages/mod_python3/mod_python3.c#L619)。

绑定最小验收应覆盖 `Session(uuid/dialstring)`、`ready/answered/hangupCause`、`answer/preAnswer/hangup`、`execute`、`setVariable/getVariable`、`streamFile/getDigits/playAndGetDigits`、`sleep`、输入回调/挂断钩子、`API.execute/executeString` 和 Event/EventConsumer 的订阅、取出、超时及销毁。各语言不保证具有完全相同的拼写、返回类型或入口参数，必须从本语言绑定生成清单。公共 C++ 绑定基准见 [`switch_cpp.h`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_cpp.h)。

挂断与脚本回调并发时，保持可读取的数据和失效对象的可观察行为；不得暴露已释放的 Rust/Go 对象。原有脚本的 `require/import`、数据库驱动、文件读写、路径、环境变量和加载的原生扩展都属于无感迁移依赖。用独立进程隔离脚本可以作为内部方案，但通信方式不能改变阻塞、超时、返回值或回调时序。`luarun` 返回成功仅代表后台启动的相应结果，不能伪造脚本已经成功结束。

## 3.7 CDR 与外部计费系统

CDR 兼容单位是**呼叫腿 + 输出模块 + 输出目标/模板**。A/B 两腿不能不经配置合并成一条；多个 CDR 模块同时启用也不能内部去重成一个模块。生成时点应对应 reporting 状态处理，而非仅收到 BYE 时抓取字段。各输出的分腿开关名称不同：CSV 使用 `legs`；XML/JSON 使用 `log-b-leg` 并有 `force_process_cdr` 路径，不能提供一个通用 `legs` 参数冒充全部模块。参考：[CSV](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_cdr_csv/mod_cdr_csv.c#L180)、[XML](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/xml_int/mod_xml_cdr/mod_xml_cdr.c#L185)、[JSON](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_json_cdr/mod_json_cdr.c#L439)。

| 输出面 | 字段/封装契约 | 关键边界 |
|---|---|---|
| CSV | 按原模板展开变量；列序、引号、分隔符、空值、空格、行末；`Master.csv`、账户文件、`cdr_csv_base` | 不可用通用 CSV 库重新格式化后声称字节兼容；rotate、SIGHUP、文件权限和并发写入 |
| XML CDR | 原 XML 节点层级、变量编码、callflow/times/app_log；文件后缀与 `a_` 前缀；`xml_cdr_base` | `encode=textxml` 发 `text/xml` 原文；其他模式使用 `cdr=`，分别是 URL 编码、Base64 或原文 |
| JSON CDR | `core-uuid`、`switchname`、`channel_data`、`callStats`、`variables`、可选 `app_log`、`callflow`；`json_cdr_base` | 原生 JSON 类型、可选字段/空对象/缺省字段；`encode-values` 与整个 HTTP body 编码分开 |
| HTTP XML URL | URL 上加入 `uuid=`，已有查询串使用 `&`；A-leg 前缀配置影响相应值 | 重试不换业务 UUID；后端按查询串识别记录的方式保持 |
| HTTP JSON URL | 固定源码按 `configuredURL + ?uuid=…` 构造 | 已含查询串的 URL 要保留专门差分样例，不能擅自套用 XML 模块的拼接规则 |
| HTTP JSON body | `encode=false` 为 `application/json` 原 JSON；URL/Base64 模式为 `cdr=` 对应编码 | Base64 不能误当成表单 URL 编码；Unicode 与特殊字符双重编码问题 |
| HTTP 交付 | XML/JSON 的 HTTP 2xx 判为成功；`retries`、`delay`、timeout、URL 轮转、错误目录与磁盘回退 | 成功与连接中断的歧义；多 URL 并非默认每台广播；日志落盘与 HTTP 成功条件分开 |
| 队列 | JSON `queue-capacity`、队列满的 backup 路径与关机处理 | 内存队列不能被描述成断电后自动可靠恢复；计数器和积压上界需观测 |
| 数据库/其他模块 | ODBC、SQLite、PostgreSQL、`mod_format_cdr` 等已启用模块的 schema、SQL模板、事务、错误转存 | 原查询/报表无需改动；数据库驱动和 schema 迁移另行验证 |

JSON 中 callflow 的 `times.*` 在固定版生成器中使用十进制**字符串**；不要重写成 JSON 浮点数而损失精度。应用日志数组顺序不可排序。CDR 可出现“字段缺失”“空字符串”“0”三种不同状态，验收必须区分。结构依据：[`switch_ivr_generate_json_cdr`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_ivr.c#L3335)。HTTP 封装依据：[XML CDR 交付](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/xml_int/mod_xml_cdr/mod_xml_cdr.c#L281)、[JSON CDR 交付](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_json_cdr/mod_json_cdr.c#L309)。

交付可靠性与格式兼容需同时定义。标准要求替代实现维护持久化交付记录，内部去重键至少含实例身份、leg UUID、输出模块和目标/模板标识，且不把内部键强加为后端必填字段。HTTP 响应丢失可造成重复提交；跨 HTTP 和数据库无法仅靠更换软交换承诺端到端“恰好一次计费”。必须用后端原有幂等机制或实际验证过的适配层处理重复；若原后端没有幂等能力，应在验收结果中报告，不能删除重试记录伪造零重复。持久化增强的默认与显式选项不得暗改原系统可观察到的交付时点和重试配置。

## 3.8 HTTAPI、事件输出与文件/服务接口

`mod_httapi` 是会话中的多轮 HTTP 业务控制，不能等同于 XML Curl 的配置查询。每轮请求携带 session_id；`HTTAPI_SESSION_ID` 的来源、cookie、持久参数与 one-time 参数、method、URL 和 action/temp-action 更新必须一致。返回的 `params`、`variables`、`work` 有不同处理权限与生效位置；work 子节点按文档顺序执行。依据：[`parse_xml` 与请求构造](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_httapi/mod_httapi.c#L1141)。

HTTAPI 契约至少包含 `execute`、`dial`、`answer/preAnswer/ringReady`、`hangup`、`playback/pause`、`record/recordCall`、`getVariable`、`conference`、`voicemail`、`sms`、`say/speak`、`continue/break`，以及媒体上传、下载缓存和输入回传。profile 的 set/get/expand 变量、应用执行、API、拨号等权限允许列表不得遗漏。音频播完后的下一轮、DTMF 无输入/无效输入、临时 action 仅一次、录音文件上传失败、返回未知节点、权限拒绝、超时和挂断竞争都必须抓 HTTP 并同时验证通话行为。标签注册见 [`mod_httapi` 固定入口](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_httapi/mod_httapi.c#L3234)。

| 可选外部集成 | 必须进入验收清单的契约 |
|---|---|
| `mod_amqp` producer | 原 profile/connection 参数、AMQP 0-9-1、exchange/routing key、事件过滤、JSON 结构、消息属性、断连重连与队列满处理 |
| AMQP commands/logging | 原命令消息、响应 exchange/key 头与响应字段；日志字段/级别/路由；不得只支持事件发布却把整个模块标为兼容 |
| 其他事件/监控模块 | Erlang、multicast、SNMP、syslog/Graylog 等实际已启用出口；消息结构、订阅与故障行为独立登记 |
| `fs_cli` 与进程启动 | 原二进制入口可由兼容启动器提供；参数、退出码、标准输出/错误、PID 与 `freeswitch.pid`、服务用户、工作目录、前后台模式及信号行为 |
| 文件路径 | `-conf/-cfgname/-log/-run/-db/-mod/-scripts/-storage/-sounds` 等参考参数；相关全局目录变量；录音、语音信箱、CDR、数据库、证书和声音文件位置 |
| 运行维护 | reload、日志轮转、排空/关机、重启、端口绑定失败、PID 冲突；原 systemd/容器启动、日志收集与监控探针可直接执行 |

AMQP 不能默认承诺任意 broker 故障下零丢失；须记录 producer 队列满及熔断路径的基准结果，并单独验证增强交付模式。依据：[固定 producer 源码](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_amqp/mod_amqp_producer.c)、[官方 AMQP 模块参考](https://developer.signalwire.com/freeswitch/module-reference/event-handlers/mod_amqp/)。进程参数和信号入口依据：[`switch.c`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch.c#L418)。

原有 FreeSWITCH `.so` 模块依赖核心 ABI；只把文件放进原 `mod_dir` 并不能使其运行。原数据库格式、录音/语音信箱内容以及升级/回退时的可读性也必须验证。替换运行程序与“正在通话中途无中断接管”是不同验收：后者还需要信令、媒体、脚本、队列和计费状态接管，本章接口标准不能单独证明该能力。

## 3.9 可执行验收用例定义

每例在固定 FreeSWITCH 基准与 RustSwitch 候选各运行一次以上，收集配置哈希、版本/模块清单、原始请求、事件轨迹、最终变量、文件及外部副作用。表内“相同”允许的 UUID/时间归一化规则必须事先登记，失败原因、字节编码、类型和业务次序不得被归一化抹掉。

| ID | 输入/故障 | 核心断言 |
|---|---|---|
| CFG-001 | 两个 include 文件有先后依赖，混用 `$${}` 与 `${}`，修改变量后 reload | 预处理、后续呼叫与既有通话的值分别一致 |
| CFG-002 | 缺少包含文件、非法 XML、递归 include、重复参数、未知参数 | 退出/保留旧配置/日志行为按基准匹配；无静默新默认 |
| CFG-003 | XML provider 早加载与晚加载两种配置 | 依赖模块初始化请求次序和失败行为一致 |
| DIR-001 | user/group/domain 三层同名与独有变量 | 合并后的每个参数一致；用户值不被域值覆盖 |
| DIR-002 | 同分机号跨两个域，静态/动态目录混合 | 正确密码与账户归属；无跨域缓存命中 |
| DIR-003 | `cacheable=100`、非数字值、缺失；查询/修改/过期/清缓存 | HTTP 次数与返回用户值一致；毫秒单位正确 |
| DP-001 | 真/假条件分别组合四种 break 与 continue | extension/condition/应用轨迹完整一致 |
| DP-002 | 三层 nested，require-nested true/false；父 false 加 anti-action、break never | 不按简化布尔模型执行；递归与反向动作顺序一致 |
| DP-003 | 普通 set 与 inline set 后立刻检查 `${}`；捕获 `$1` 被后续 regex 覆盖 | route-time 与 run-time 的差异及捕获范围一致 |
| DP-004 | inline 未注册应用、已注册但不允许 inline 应用、loop=0/-1 | 拒绝、挂断原因及后续应用是否执行一致 |
| DP-005 | A/B 变量冲突，export/nolocal/bridge_export、每腿参数及 local_var_clobber | A 与全部 B 腿最终值及事件/CDR 传播一致 |
| DP-006 | 启用 chatplan，消息正文/事件头含特殊字符；普通/inline/反向动作及嵌套输入 | 路由、投递报告、正文与嵌套错误路径符合 chatplan 基准 |
| APP-001 | 早期媒体→接通→bridge 对端忙线/拒绝/超时/主动挂断 | continue_on_fail、hangup_after_bridge、原因与执行位置一致 |
| APP-002 | 播放中收 DTMF，首位/位间/总超时，终止键，挂断与收号竞争 | 实际音频、数字、终止变量及完成事件一致 |
| APP-003 | 录音开始/暂停/恢复/转接/停止，文件打不开、磁盘满 | 声道、文件可读性、时间轴、失败表现与话单一致 |
| XML-001 | 中文/空格/`+%&`/重复字段；开关 enable-post-var；GET/custom method | 原始 HTTP 和多值解码结果均符合参考 |
| XML-002 | 首 binding not-found，第二命中；两者 not-found 且本地命中 | 请求先后和最终路由一致，证实 fallback |
| XML-003 | 首 binding 返回有效但缺目标 XML，第二后端和本地均可命中 | 不误走第二后端；本地目标选择符合参考 |
| XML-004 | 204/404/500、超时、空/坏/超大 XML、200 传输截断 | 请求次数、错误类别、最终来源及呼叫结果一致 |
| SCRIPT-001 | 同一 Lua/JS/Python 脚本从会话、同步 API、后台入口执行 | 参数、session 可用性、返回值、输出和完成时点一致 |
| SCRIPT-002 | 输入回调、hangup hook、XML handler、EventConsumer 超时/销毁 | 对象生命期、状态可见性、事件与异常行为一致 |
| CDR-001 | 接通/未接通/拒接/转接，A/B 全部组合，CSV/XML/JSON 并用 | 每模块正确数量；UUID 关联、字段类型、精度、模板字节一致 |
| CDR-002 | URL 带查询串；三种 JSON 编码、四种 XML 编码、特殊字符 | 原始请求 URL/body/Content-Type 逐模式一致 |
| CDR-003 | 后端 500、超时、已入账但响应丢失；多 URL；错误目录不可写 | 先比对基线的尝试次数、路由与可能重复提交；可靠计费另以原后端幂等或已验证适配层为前提验收，不得偷偷吞重试 |
| CDR-004 | 队列满、进程退出、磁盘满、日志轮转 | 缺失/备份/重复计数可解释；可靠性增强有独立测试 |
| HTT-001 | playback 收号→临时 action→下一轮→dial→挂断 | session_id、cookie、一次性参数及 work 次序一致 |
| HTT-002 | 变量/应用权限拒绝、录音上传失败、未知标签、超时 | 不越权执行，不误报成功；后续 HTTP/媒体行为一致 |
| INT-001 | AMQP 路由/过滤、断连/恢复、满队列、命令响应 | 消息头/正文与错误行为一致；未测试类型不标兼容 |
| OPS-001 | 原启动脚本和 fs_cli、PID 冲突、路径覆盖、信号/轮转/排空 | 脚本无需修改；退出码、文件、在途呼叫结果符合基准 |

通过本章需要全部适用用例、实际用户脚本与实际后端契约都通过；以“不使用某模块”排除范围必须来自冻结的参考构建/使用清单，不能在测试失败后缩小分母。当前这些用例尚未运行，不能将标准的完整性写成产品已通过率。
