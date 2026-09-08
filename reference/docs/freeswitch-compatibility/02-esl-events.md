# ESL、事件与 fs_cli 无改动接入标准

本章规定 RustSwitch 对 FreeSWITCH Event Socket 的**目标兼容合同**。`MUST` 表示发布验收要求，`MUST NOT` 表示禁止行为，均不表示已经实现。当前应用版本 RustSwitch v0.3、文档发行版本 1.5 已提供可选入站 ESL 的有限子集：9 个 API 入口、4 类事件、api/bgapi、基础订阅与筛选，以及 console_execute 的单 API 命令路径。原版 fs_cli 的中文回显、版本身份与 API JSON 目录三条命令已经实际通过。原版已运行，独立成对报告记录26项探针：21项限定场景通过、3项 SIP OPTIONS 能力差异、2项身份/入口发现；这不是本章完整合同通过数。出站 ESL、sendmsg 应用执行、完整事件/变量语义、控制台批处理/别名/补全及完整交互仍未完成，详见 [当前 ESL 能力](../api/esl-reference.md) 与 [成对报告](../api/conformance-report.md)。

唯一参考版本为 **FreeSWITCH v1.11.3，提交 `ef32e205295e29f034f1453ad245ba5efb07b94a`**。后续出现新版不自动改变本合同。模块启用情况、构建选项、目录认证、拨号计划和业务客户端版本必须随验收记录冻结。一个裸 TCP 服务能接受 `auth`，或者 `fs_cli -x status` 能返回文字，都不构成 ESL 全兼容。

## 1. 无感替换的判定边界

ESL-001：现有 `fs_cli`、原版 C libesl、C++ ESLconnection 及用户正在使用的语言绑定 MUST 无须修改源代码、替换库或增加代理专用参数即可接入。替换服务器地址、沿用已有 VIP/DNS 和原有端口、密码属于部署动作；把业务程序从 ESL 改成 RustSwitch HTTP API 不算无感替换。

ESL-002：验收 MUST 同时覆盖传输、命令、状态变化、事件、配置、错误和客户端等待行为。接口名称相同而桥接对象、变量作用域、应答时机、挂断原因或业务事件不同，判定失败。协议载荷中的 UUID、时间和地址允许按本章规则归一化，业务结果不得归一化掉。

ESL-003：每一项必须关联原版运行轨迹和替代实现运行轨迹。源码可确定实现入口和边界，但不能代替动态验证，特别是并发、模块回调、挂断竞态及慢消费者。未采集对照证据的项目标记 `UNVERIFIED`，不得记为通过或“不适用”。

ESL-004：ESL 是传输入口。其 `api` 可访问的全部 API、`sendmsg execute` 可访问的全部应用、CUSTOM 事件子类，以及 `event_sink` 等模块导出的 API，必须进入总接口清单逐项验收。本章不把“能转发任意字符串”当作这些业务接口已经兼容。

## 2. TCP 连接与帧格式

### 2.1 字节流解析

ESL-010：MUST 按 TCP 字节流组帧，处理任意拆包、粘包、一次读取多帧以及一次写入只能发送部分数据的情况。不能用一次 `read` 对应一次命令，不能用空行查找代替有正文的长度解析。

ESL-011：请求使用命令行、可选头字段、空行、可选正文；响应使用头字段、空行、可选正文。MUST 接受原版接受的 LF 和 CRLF 行结束，发出的格式必须能由原版 libesl 解析。头字段查找大小写行为、命令分隔空格和参数保留规则按参考版本逐项比较；禁止统一小写业务参数或对整条命令做 URL 解码。

ESL-012：`Content-Length` 计量序列化后正文的**字节数**，不是 Unicode 字符数。长度以内的 CR、LF、空行均为正文；读取恰好该长度后，后续字节属于下一帧。外层事件正文可能包含内层事件自己的 `Content-Length`，两者独立。

ESL-013：必须测试缺失正文、零长度、超长头、负数长度、超限长度、数值溢出、非数值、重复长度字段和正文内 NUL。v1.11.3 服务端设置了 16 MiB 正文上限，但原版采用文本事件存储，**不得据此宣称 ESL 是任意二进制透明协议**。有效文本的边界必须相同；畸形输入的原版响应、断连和副作用必须记录，任何主动收紧规则均作为显式兼容差异评审，不得悄悄改变后仍声称百分之百相同。

ESL-014：连接的命令响应、日志和异步事件 MUST 共用合法的有序字节流，不得交叉写坏帧。短写重试、输出队列和关闭过程不能切断一个随后仍被宣称完整送达的消息。实际 EOF、RST 或失败的部分发送必须可在验收证据中区分。

以上解析入口可由固定版本的 [`read_packet` 与发送分支](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c) 对照；客户端兼容性还须按原版 [`esl_recv_event`、`esl_send_recv_timed`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/libs/esl/src/esl.c) 验证。

### 2.2 帧类别

| 外层 Content-Type | 必须保留的含义 | 不能混淆的事项 |
|---|---|---|
| `auth/request` | inbound 连接认证提示 | 不是事件订阅已生效 |
| `command/reply` | 命令层回复，检查 `Reply-Text` 及附加头 | `+OK` 不普遍等于业务成功或媒体动作完成 |
| `api/response` | API 执行返回的正文 | 正文可能是 `-ERR`、多行列表或其他命令专用格式 |
| `text/event-plain` | 外层正文为 plain 事件 | 内层头、正文和长度另行解析 |
| `text/event-json` | 外层正文为 JSON 事件 | 不能转换成自创的统一 JSON 信封 |
| `text/event-xml` | 外层正文为 XML 事件 | 不能只输出与 plain 大致相似的字段 |
| `log/data` | 日志载荷及日志元数据 | 不等同于事件订阅，不得改成 CUSTOM 事件 |
| `text/disconnect-notice` | 协议层断连或 linger 提示 | notice、TCP EOF 与通道挂断是不同事实 |

用于测试长度的文本正文可取 `你好\n\nx`：UTF-8 正好 9 字节。发送端必须发送实际换行；文档中的 `\n` 只是表示法。

以下为固定参考实现的边界采样点，必须带入测试；它们不等于对任意客户端输入的安全保证，也不是可以随意移植到其他版本的通用常数：

| 项目 | v1.11.3 参考值/行为 | 替代实现的验收要求 |
|---|---|---|
| 入站正文范围检查 | 大于 16 MiB 或转换后为负数时关闭读取路径 | 对有效上下界及转换歧义单独测试，不能只测试 1 KiB 消息 |
| 命令头读取阈值 | 10 MiB 阈值进入原版解析路径；不是“所有超长头都规范回复错误” | 对照实际关闭/回复；任何更低限制标为差异 |
| inbound 认证读取、outbound 初次读取 | 调用读取函数时传入 25 秒 | 测量超时边界和不完整正文行为；不能据此承诺整个命令都有 25 秒绝对期限 |
| 每个 listener 的事件队列、日志队列 | 分别最多 100,000 项 | 比较上界、副作用和故障域；更改上界要进入部署合同 |
| 过量入队失败 | 对应丢失计数超过 500 时终止 listener；后续成功入队会清零该计数 | 区分累计历史丢失与这一计数，不能错误实现为进程全局总计 |

这些值来自同一固定提交的 [`mod_event_socket.c`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c)，必须与运行轨迹一起留证。

## 3. Inbound 连接与认证

ESL-020：MUST 读取原有 `event_socket.conf.xml` 配置对应的监听地址、端口、密码、ACL、绑定失败策略等设置，并验证 IPv4/IPv6 行为。不得在兼容部署时静默改端口、扩大监听地址或绕过原有访问控制。原版默认端口为 8021；验收以用户配置为准。

ESL-021：inbound 由客户端发起 TCP 连接。服务器发出认证提示；客户端发送 `auth <password>`。正确和错误密码、未认证先发命令、空密码、认证超时、ACL 拒绝、客户端在提示前发送数据，均必须与原版对照。原版普通密码认证的成功回复为 `+OK accepted`，错误密码返回 `-ERR invalid` 并结束该连接；不得为了方便客户端而把错误认证维持为已认证状态。

ESL-022：MUST 支持基线使用的 `userauth` 目录用户认证及 `esl-password`、`esl-allowed-api`、`esl-allowed-events`、`esl-allowed-log` 的继承与回复头。域、组、用户覆盖关系、API 包装命令的授权检查和 CUSTOM 子类限制必须逐项验收。仅实现全局密码会破坏使用目录授权的既有系统。

ESL-023：认证成功只建立本连接的身份和权限。订阅、过滤器、日志级别、myevents 绑定和未完成请求都不能从其他连接泄漏过来。重新连接需要按原客户端流程重新认证及订阅；不得在无协议协商的情况下自动套用别人的连接状态。

配置和授权入口见固定版本 [`config`、`parse_command`、`auth_api_command`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c)。认证与 ACL 的存在不表示本协议自动提供 TLS；原有隧道或 TLS 终结部署也必须纳入环境清单。

## 4. Outbound socket 应用

ESL-030：MUST 支持拨号计划 `socket` 应用，由服务器主动连接既有业务服务。原业务服务接受连接后发送 `connect`，服务器返回包含当前通道数据的连接信息帧。不得反向要求业务端先等待 inbound 的 `auth/request`。连接信息须可被原版 `ESLconnection(fd)`、`getInfo()` 正常消费。

ESL-031：`Socket-Mode`、`Control`、通道 UUID、通道变量及应用执行位置必须与原版一致。MUST 覆盖 `socket host:port`、`socket host:port async`、`socket host:port full`、`socket host:port async full` 四种组合，以及基线使用的多目标连接、IPv6、超时和连接失败继续拨号计划的行为。

ESL-032：`async` 控制事件命令进入通道执行路径的方式；它不等于另开业务会话，也不等于 `bgapi`。单条 `sendmsg` 的 `async: true`、原版客户端 `setAsyncExecute`/`executeAsync` 和 socket 应用参数必须分别验收。

ESL-033：`full` 是原版命令入口的模式标志，不能重新解释为任意自行设计的权限角色。特别是参考实现的 `sendmsg` 分支位于部分 full 限制检查之前，因此不得仅凭名称宣称 single-channel 模式已经隔离全部跨通道操作。必须用“模式 × 命令 × 目标 UUID × 授权配置”矩阵记录真实允许/拒绝行为。

ESL-034：MUST 支持 `getvar`、`myevents`、`divert_events`、`resume`、`linger`、`nolinger` 在适用连接上的行为。`resume`、`socket_resume`、转移、通道 reset 和 socket 断连后的拨号计划续行必须按状态分别测试；不能将所有 outbound 断连都统一转成挂机或统一继续执行。

ESL-035：`linger` 只改变受控通道结束后 socket 的保留行为，不是通话保活，也不等于持久消息订阅。MUST 支持无参数和指定秒数，保留 `Content-Disposition`、`Controlled-Session-UUID`、`Linger-Time` 等原版通知字段。无参数、零值、正常期限、显式 `nolinger` 和客户端主动退出要分别测量；不得把源码中未最终使用的初始值当作无参数默认期限。

以上 socket 应用和连接生命周期由固定版本 [`socket_function`、`listener_run`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c) 定义；原版客户端接管 socket 的动作见 [`esl_attach_handle`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/libs/esl/src/esl.c)。

## 5. API、后台任务与通道执行

### 5.1 三种不同的完成语义

| 操作 | 立即可观察的结果 | 业务完成判据 |
|---|---|---|
| `api <name> <args>` | 该次 API 调用返回后得到 `api/response` | 按具体 API 契约；调用返回不保证所安排的通话、定时任务已经结束 |
| `bgapi <name> <args>` | 命令层收到 Job UUID | 对应 `BACKGROUND_JOB` 表示该次后台 API 调用返回；API 创建的长期业务还要观察其通道/应用事件 |
| `sendmsg ...` 的 execute | 命令层响应，可能只是入队成功 | 正确关联的应用执行事件、最终业务状态和媒体效果共同判定 |

ESL-040：`api` MUST 保留原 API 的原生文本、多行结构、尾随换行、列表列名、JSON/XML 选项、错误正文以及 `console_execute` 行为。不得统一包装成 `{success:true}`，也不得给失败操作补一个假的成功文本。

ESL-041：`bgapi` MUST 支持服务器生成的 Job UUID 和客户端提供的 `Job-UUID` 头；回复中的 UUID 与最终后台事件匹配。MUST 保留 `Job-Command`、存在时的 `Job-Command-Arg`、`Job-Owner-UUID` 和正文结果。后台任务完成先后由实际工作决定，不按提交顺序伪造排序。

ESL-042：Job UUID 是关联标识，**不是协议级幂等键**。客户端重复提交同一 Job UUID，不得未经标准变更就去重、合并任务或重放缓存结果。原版是否截断异常长度、接受非规范 UUID 等边界必须记录；自动把“客户端超时重发”解释为“原任务未执行”属于错误。

ESL-043：业务端须按原来方式订阅 `BACKGROUND_JOB` 才能依赖该事件。服务端不得把所有后台结果偷偷变成当前请求的同步响应；也不能由于发起 socket 已断开就假定后台工作必然撤销。验收必须分开检查后台 API 是否继续、结果事件是否发出及该连接是否收到。

### 5.2 sendmsg 执行合同

ESL-050：MUST 保留 `sendmsg <uuid>`、`session-id` 头及 outbound 当前会话省略 UUID 的寻址规则、优先级和错误。至少覆盖 `call-command` 的 `execute`、`hangup`、`nomedia`、`unicast`、`xferext`，并按基线实际使用的应用继续扩展；接收这些头但没有实施状态变化不能通过。

ESL-051：execute MUST 支持 `execute-app-name`、`execute-app-arg`；适用情况下支持 `Content-Type: text/plain` 正文作为参数。参数中的变量展开、空值、空格、换行、循环 `loops`、`lead-frames`、`hold-bleg` 和应用错误按原版应用执行层验收，不能只在 ESL 解析层做字符串相等测试。

ESL-052：客户端提供的 `event-uuid` MUST 按参考执行路径关联到事件中的 `Application-UUID`，与通道的 `Unique-ID`、后台任务的 `Job-UUID` 分开。存在的 `event-uuid-name` 与 `Application-UUID-Name` 也须对应。未指定时使用原版等价的自动关联行为，不复用另一个正在执行的应用标识。

ESL-053：`event-lock: true`/`event-lock-pri` 是通道事件执行顺序控制，MUST 与原版私有事件队列及应用执行行为一致；不得实现成锁住全局所有通话，也不得声称它令 TCP 上的每个响应都阻塞到应用成功完成。应用内等待媒体、DTMF、嵌套执行、break、hangup、转移时必须检查锁的释放及后续命令效果。

ESL-054：对不存在的会话、已经挂机的会话、尚在结束的会话、未知应用、缺少应用名和入队失败，必须分别保留原版可观察行为。有些错误可能在应用执行阶段体现而不是立即的 `command/reply`，不能提前统一改写，也不能因为立即 `+OK` 就算用例成功。

以下为用于验证 UUID 关联和顺序的请求形式，`<U>` 和 `<E>` 分别替换为测试通道 UUID 与独立应用 UUID；实际结尾必须有空行：

```text
sendmsg <U>
call-command: execute
execute-app-name: sleep
execute-app-arg: 100
event-uuid: <E>
event-lock: true

```

通道执行的实际头解析见 [`switch_ivr_parse_event`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_ivr.c)，应用事件字段见 [`switch_core_session_exec`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_core_session.c)，客户端设置见原版 [`ESLconnection`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/libs/esl/src/esl_oop.cpp)。

## 6. 订阅、过滤、事件格式与日志

ESL-060：MUST 支持 `event plain|json|xml`、事件名列表、`ALL`、`CUSTOM` 子类，以及 `nixevent`、`noevents`。连续多次订阅的累加/删除行为、格式切换、无关键字、未知关键字、权限拒绝时已发生的部分变化，均按固定版本验证。不能自行将每次 `event` 解释成替换全部订阅。

ESL-061：`filter`、`filter add`、按头删除、按头和值删除、`filter delete all` 的行为必须保留。**不能采用常见但不正确的“不同头 AND、同名头 OR”概括。**固定版本按过滤项遍历：至少一个正向匹配可选中事件，负向匹配可否决；仅有负向项不自动产生“默认允许”。普通值的比较、以斜杠起始的正则、前缀正负号和缺失头的行为必须以真值表固化，不能替换成 SQL 条件或另一套正则语言。

ESL-062：`myevents` 是连接与会话相关的事件模式，不是普通 `filter Unique-ID` 的别名。默认事件集合、格式选择、重复启用、无效 UUID、与 `event`/`noevents`/filter 的组合，以及由 `Job-Owner-UUID` 关联的后台事件必须测试。不得用一个全局订阅再给所有 socket 广播来代替会话隔离。

ESL-063：plain 事件 MUST 保留头名、字段缺失与空值的区别、百分号编码、数组表示、重复字段和正文。libesl 消费后的值必须与原版一致；不能双重 URL 解码，也不能把 `+` 一律当成空格。JSON 必须保留原版字符串/数组类型及 `_body` 表示，不能把数字形态字符串强制转为数字。XML 必须保留事件、头、正文结构与相应编码；不能采用自定义 SOAP 格式。

ESL-064：`Event-Name`、`Event-Subclass`、`Core-UUID`、`Event-Sequence`、`Event-Date-*`、`Unique-ID`、`Other-Leg-Unique-ID`、`Channel-*`、`Caller-*`、`variable_*` 等存在条件、作用域和语义必须进入事件样本契约。每种事件并不必然具备表中所有头；不得为了“字段齐全”补造不存在的值。扩展字段只能采用不会覆盖标准字段的命名，并检查旧客户端是否拒绝额外字段。

ESL-065：`sendevent` MUST 按原版处理事件名称、自定义子类、正文、路由相关 UUID、生成的事件标识和回复。用户自定义头不得被统一转换成只供 RustSwitch 使用的字段。发布到事件系统、转送到会话、收到 TCP 确认及终端媒体效果是四个不同阶段。

ESL-066：`log`/`nolog`、日志级别解析、授权、重复取消及日志头字段 MUST 可被原版 fs_cli 使用。服务器日志与业务事件各自的缓冲和取消操作必须保持语义；取消日志不能连带清空通道事件。日志文件名/源代码行等实现标识的差异需要明确映射，不得在报告中直接忽略。

序列化及通用事件字段以固定版本 [`switch_event.c`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_event.c) 为依据；过滤逻辑以 [`event_handler`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c) 为依据。源码中的事件枚举只代表可能的事件名称，不能替代每个启用模块产生事件的条件测试。

## 7. 事件偏序、错误与连接故障

ESL-070：一个连接上的 TCP 字节顺序必须正确；但不同线程、不同通道、不同后台任务的事件不存在本合同凭空增加的全局业务顺序。`Event-Sequence` 是进程事件元数据，**不是持久投递序号、订阅确认、断线续传游标或跨重启事务编号**。过滤造成序号间隙也不直接证明丢失。

ESL-071：验收比较因果关系及同一对象的约束。例如，成功返回的普通应用具有对应的执行开始和完成关联；应用动作发生在其执行区间内；正常终结用例的完成状态与挂断原因一致。不得把“CREATE → ANSWER → BRIDGE → HANGUP → DESTROY”强制要求为所有呼叫都出现的事件全集；未接通、早期媒体、取消、转移、嵌套应用具有不同路径。因异步派发造成的网络到达顺序须与原版观测范围对照，不能以本地生成先后直接推断到达先后。

ESL-072：普通命令响应没有通用请求 ID。保持串行请求的原版客户端必须正常工作；流水线、API 与事件交错、多个 `bgapi` 同时完成必须额外测试。原版 libesl 在等待响应时会缓存遇到的非响应消息；替代端不能依赖客户端丢掉这些消息或要求先停订阅再发命令。

ESL-073：未知命令、未知 API、未知应用、权限拒绝、语法错误、资源不足、无效 UUID、连接关闭，必须按类别分别测试帧类型、错误内容、是否继续连接及副作用。错误拼写和尾随换行可能被旧程序直接匹配，修改为更友好的文字也属于可观察差异。

ESL-074：ESL 原生 TCP 订阅不提供全局 exactly-once、持久事件重放或自动重连恢复。本标准不得虚构这些保证。若需要审计存储、按游标重放或业务幂等服务，应作为单独接口交付；不得把历史重放事件混入旧客户端的实时流而不做协商。

ESL-075：慢消费者必须有独立、可观测的资源边界，不能使媒体线程阻塞在 ESL 输出。参考实现存在队列上限、事件/日志入队失败记录及过量丢失后断连的路径；“原版也可能丢事件”不能作为替代实现静默丢失的合格理由。兼容模式须记录边界行为差异；服务目标要求额外测试在规定负载及消费速率内零事件缺失，超载时明确暴露不完整结果，不能仍报告投递成功。

ESL-076：必须测试 FIN、RST、半关闭、超时、未读完响应即断连、bgapi 已受理但回复未收到，以及 outbound 业务进程退出。重连后应重新查询业务状态；服务端不能未经依据撤销或再次执行结果未知的操作。HA 切换若不能保留 TCP 连接及事件状态，必须作为切换能力缺口，不能因新连接成功就宣称存量 ESL 会话无感。

## 8. 原版客户端验收步骤

测试使用两个隔离环境：R 为固定版本原版 FreeSWITCH，S 为 RustSwitch；模块、配置、拨号计划、媒体样本、客户端二进制及试验输入相同。每轮创建独立租户/测试用户、呼叫 UUID 和 CUSTOM 子类后缀，避免事件串入。涉及挂机、长循环或大量事件的用例只操作本轮测试对象。

每个用例至少保存：基线版本和构建清单、输入原始字节、双向 TCP 原始记录、解析后帧、SIP/RTP 证据（适用时）、通道/后台任务关联图、错误及资源统计。报告必须标识 `PASS/FAIL/UNVERIFIED`、首次分歧位置和未采集原因。

### 8.1 必须运行的客户端组合

| 客户端 | 实际操作 | 通过条件 |
|---|---|---|
| 原版 `fs_cli` 命令模式 | 使用已有配置/凭据连接 R 和 S；运行 `status`、`version`、`show channels`、业务实际 API | 原进程可正常退出；输出结构、错误和业务结果符合冻结契约；版本身份差异有明确规定 |
| 原版 `fs_cli` 交互模式 | 自动终端输入状态命令、Tab 补全、`/event`、`/filter`、`/log`、`/nolog`、退出 | `console_complete`、`console_execute`、日志和事件交错均可用；不允许仅命令模式通过 |
| 原版 C libesl | 认证、send/recv、带正文事件、定时等待、订阅中调用 API | 不修改库；帧、超时、错误和缓存事件行为等价 |
| 原版 C++ ESLconnection | inbound 构造、outbound 已接受 fd 构造、getInfo、execute、executeAsync、bgapi、recvEventTimed | UUID 关联、等待行为和应用效果等价 |
| 用户现有语言绑定和业务服务 | 原构建产物回放生产代表性场景 | 不修改业务代码；不能以新写的测试客户端通过替代此项 |

原版 fs_cli 的交互命令并非只转发一个 `status` 字符串，其实现见 [`fs_cli.c`](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/libs/esl/fs_cli.c)。

### 8.2 正向、负向与并发用例

表中“对照”表示用同一脚本先执行 R，再执行 S，按帧与业务状态比较；不是允许实现者自选预期。`U1/U2` 为本轮存活通道，`E1/E2` 为应用关联 UUID，`J1/J2` 为后台任务关联 UUID。

| ID | 操作和边界 | 必须检查的结果 |
|---|---|---|
| ESL-T01 | inbound 正确密码；按 1 字节分片发送认证 | 原版客户端成功；只有该连接获得身份 |
| ESL-T02 | 错密码、空值、未认证 `api status`、认证超时 | 回复/断连与 R 一致；无业务副作用 |
| ESL-T03 | ACL 放行/拒绝；IPv4/IPv6；端口被占用 | 监听、拒绝和进程可用性符合原配置 |
| ESL-T04 | `userauth` 域/组/用户覆盖，受限 API 与嵌套包装 API | 相同授权结果及 Allowed 头；事件/日志不越权 |
| ESL-T05 | LF、CRLF；两个命令一次写入；在每个字节位置拆开一次 | 响应个数及帧边界正确；无多执行 |
| ESL-T06 | 9 字节 UTF-8 正文含空行，紧接下一条命令 | 正文完整；下一帧不被吞入 |
| ESL-T07 | 正文长度 0、1、16 MiB、16 MiB+1；负数、溢出、重复长度 | 有效上界与畸形输入逐项对照；无失控分配/跨连接影响 |
| ESL-T08 | 只发送部分正文后 FIN/RST；超长未结束的头 | 不执行半条业务消息；资源回收与关闭证据完整 |
| ESL-T09 | `api` 正常/未知/错误参数/无输出/多行/JSON 输出 | Content-Type、正文及错误层次一致 |
| ESL-T10 | `api` 带 `console_execute`，原版 fs_cli Tab 补全 | 交互客户端无需修改；输出和补全契约正确 |
| ESL-T11 | `bgapi` 自动 UUID 与指定 J1；订阅 BACKGROUND_JOB | 回复与事件 UUID 对应，正文为 API 结果 |
| ESL-T12 | 并行 J1/J2，一个慢一个快；重复 J1 | 按实际任务完成；不串结果、不擅自幂等去重 |
| ESL-T13 | bgapi 受理后立即断开，再建新连接 | 记录任务真实结果与事件缺口；不虚构重放/撤销 |
| ESL-T14 | 四种 outbound socket 模式，用原版 fd 构造客户端 | connect/getInfo、Socket-Mode、Control、当前 UUID 正确 |
| ESL-T15 | outbound 目标拒绝、超时、多目标、业务进程退出 | 呼叫状态及拨号计划后续动作与 R 一致 |
| ESL-T16 | 每种模式中向当前 U1、另一 U2、不存在 UUID 发 sendmsg | 形成实际模式权限矩阵；不将 full 误当完整隔离 |
| ESL-T17 | sendmsg 命令行 UUID、session-id、无 UUID 的优先级组合 | 仅目标通道发生动作；失败路径无误操作 |
| ESL-T18 | execute sleep，指定 E1，随后执行带 E2 的 set/log | 两组 Application-UUID、开始/完成、变量效果对应 |
| ESL-T19 | executeAsync 与默认 execute；event-lock 开/关 | 原版等待行为与应用顺序对照；TCP 回复不当完成证据 |
| ESL-T20 | 锁内等待 DTMF/媒体；同时 break、hangup、转移 | 无死锁；后续执行、挂断原因和结束事件一致 |
| ESL-T21 | 应用参数用头/正文；空参数、Unicode、空行、变量展开 | 应用收到相同参数与作用域；不双解码 |
| ESL-T22 | loops=0/1/2/负值，随后中断；lead-frames、hold-bleg | 循环数、关联事件、中断与 B 腿状态符合 R |
| ESL-T23 | 未知应用、无应用名、无效/已挂机/正在结束 UUID | 区分命令接收与执行失败；错误及副作用一致 |
| ESL-T24 | sendmsg hangup、nomedia、unicast、xferext 的有效/无效参数 | 不只检查回复；核验 SIP、媒体和拨号计划效果 |
| ESL-T25 | event 两次累加、ALL 后 nixevent、noevents 后重订阅 | 对照收到的事件集合和队列清理边界 |
| ESL-T26 | 订阅中切换 plain/json/xml；正文含百分号、加号、中文 | 原版解析值、数组、空值、正文均等价 |
| ESL-T27 | filter 正值、两种不同头、两个同名头、负值、仅负值、缺头 | 对照完整真值表，特别排除错误的跨头 AND 实现 |
| ESL-T28 | filter 正则及正负前缀；删除单值、单头、全部 | 匹配语言与删除范围正确；其他连接不受影响 |
| ESL-T29 | myevents U1、同时 U2 活动；重复启用及无效 UUID | 事件集合、错误和 Job-Owner 例外均对照 |
| ESL-T30 | myevents 与 filter/event/noevents 交叉切换 | 会话绑定与订阅状态不被简单别名化 |
| ESL-T31 | sendevent CUSTOM，指定业务头、正文和 Unique-ID | 发布/路由/返回标识及接收者内容与 R 一致 |
| ESL-T32 | log 全级别、非法级别、nolog 两次、受限目录用户 | 级别和错误准确；不损坏事件订阅 |
| ESL-T33 | linger 无参数/0/2 秒、nolinger，通道正常/异常挂机 | notice 头、保留时间和 TCP 关闭对照 |
| ESL-T34 | resume/socket_resume 开/关，socket 断开及显式退出 | 后续拨号计划只按正确状态执行 |
| ESL-T35 | 多 socket 同时控制 U1；U1/U2 并行执行相同应用 | 不串通道、UUID、回复；只验证原版支持的因果约束 |
| ESL-T36 | 一个连接持续事件流中顺序调用 API/bgapi | 原版 sendRecv 能取得回复，并在后续 recv 中取回交错事件 |
| ESL-T37 | 一个消费者暂停读，另一个正常读，同时媒体承载 | 资源有界；慢连接影响及丢失/断连可观测；正常连接和媒体符合 SLO |
| ESL-T38 | consumer 重连、服务端重启、Core-UUID 变化 | 重新认证订阅；不复用旧 epoch，也不伪造断档的事件 |
| ESL-T39 | 1 万双腿通话（通常约 2 万 channel），另冻结 ESL 连接/订阅数；统计事件总量/延迟/队列 | 全量事件与窄订阅分别验收；媒体负载与事件负载均需证据 |
| ESL-T40 | 用户原业务产物完成呼叫、DTMF、播放、转接、挂断流程 | 业务代码零改动，输出/CDR/事件/媒体结果均合格 |

T07、T22、T37、T39 必须设置外部超时和资源上限；这属于可重复测试的运行约束，不得把超时终止的测试记为通过。涉及参考实现边界缺陷时，必须留下完整分歧记录，并将拟采取的修复列为标准差异。

## 9. 比较规则与发布门槛

ESL-080：字节比较首先验证长度、分隔符、帧类别和原版客户端可解析性；语义比较再使用显式允许清单处理本来就动态的值。UUID 必须保持全程一一映射及 A/B 腿、Job、Application 三类关系；时间只能调整环境偏移，不能掩盖先后关系或超时差异。

ESL-081：字段存在性、空字符串、`_undef_`、`_none_`、数字形态字符串、数组、失败原因和返回格式不能全局删除后再比较。主机名、IP、Core UUID、源文件/行号及真实产品版本属于需要逐项决定的实现身份字段。兼容产品不得仅伪装版本字符串来欺骗客户端探测；如果既有客户端严格依赖这些值，必须在兼容合同中明确映射及影响。

ESL-082：一次“命令成功”的验收至少包含接受结果、最终业务状态、关联事件及副作用四类证据中适用的全部项。一次“无感切换”的验收还包括现有客户端产物零修改和原部署配置生效。SIP 接通率、媒体无丢包和 ESL 事件完整率分别统计，不得互相替代。

发布为“v1.11.3 ESL 全兼容”之前，本章所有适用条款、总清单中的 API/应用/事件合同和用户业务回归必须全部通过；任何 `FAIL` 或 `UNVERIFIED` 都阻止该宣称。当前状态是**接口标准已定义，有限 ESL 实现及限定探针已实测；完整 ESL 实现与全部合同对照验收仍未完成**。
