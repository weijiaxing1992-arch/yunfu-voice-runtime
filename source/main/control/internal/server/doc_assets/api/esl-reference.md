# 入站 ESL 接口与 FreeSWITCH 对照

文档版本 1.9.0。目标参考 FreeSWITCH 1.11.3，固定提交 `ef32e205295e29f034f1453ad245ba5efb07b94a`。本页说明已接入 Go 控制面与 Rust 实际媒体的有限子集；完整原版命令、应用、事件和 outbound ESL 仍有缺口，不能据此宣称完整替换。应用参数见 [IVR 接口](ivr-reference.md)，新增本地电话发送按键见 [DTMF 发送接口](dtmf-send-reference.md)，组合调用见 [AI 呼叫接入](ai-calling-reference.md)。

## 启用与认证

主进程增加 `-esl-listen 127.0.0.1:8021 -esl-password-file /absolute/private/esl.secret`。默认关闭。地址必须为字面回环 IP，密码从私有普通文件读取；权限不得向组或其他用户开放，最多 256 字节，可有一个结尾换行。密码不写入命令行、管理配置或日志。

连接建立后服务发送 `Content-Type: auth/request`。客户端发送 `auth <password>` 和空行，成功返回 `command/reply`、`Reply-Text: +OK accepted`。错误密码返回 `-ERR invalid` 后断连；未认证的普通命令不执行。认证阶段有绝对时间限制。

原版 `fs_cli -x` 通过 `console_execute: true` 请求调用已实现 API 的单命令子集，支持中文正文和空白处理。双分号批处理、别名在执行任何 API 前明确拒绝；单分号按普通参数保留。其补全、日志订阅及其他默认交互命令仍依赖尚未实现的接口，不保证完整交互体验。未提供 `userauth`、角色权限、目录口令认证、远程 ESL ACL、TLS ESL 和 outbound 会话接管。

## 字节协议与资源边界

请求为命令首行、可选头、空行及可选精确长度正文。接受 LF / CRLF，`Content-Length` 按字节计算，支持分片和粘包；重复长度、负数、溢出、截断或非法头拒绝。每连接单个写入者保持完整帧，异步事件可能出现在两个命令响应之间，客户端必须独立分派。

| 项目 | 当前边界 | 对照含义 |
|---|---|---|
| 连接数 | 默认 64，内部选项最大 1024 | 不是原版所有连接策略的等价配置 |
| 请求头 / 正文 | 64KiB / 16MiB | 头预算比原版严格；不接受无限头空行 |
| 认证 / 帧接收 | 默认 5 秒 / 首字节后 10 秒 | 慢速输入不能逐字节续期 |
| 空闲连接 | 等待下一帧最多 30 分钟 | 连接保活策略仍须按客户应用复核 |
| 写入 | 默认 2 秒；有界消息及字节队列 | 慢消费者或队列溢出关闭连接，不静默伪装完整事件流 |
| API | 默认 5 秒执行预算，通道命令队列 128 | 超时与满队列明确失败；已开始的操作不承诺事务回滚 |
| bgapi | 固定 8 执行器、128 等待任务 | 不为每个作业无上限创建线程；任务不落盘 |

服务退出时停止接入、取消回调并回收连接。SIP 主循环只收到有界控制请求；通道 UUID 使用直接索引，结束时回收索引，不在每次命令上扫描全部通话。

## 已实现 API

调用 `api <command> [arguments]`，响应为 `Content-Type: api/response` 与按字节数编码的实际正文。未知 API 返回 `-ERR <command> Command not found!` 加换行。响应帧到达不等于业务成功。

| 命令 | 输入 | 当前结果与限制 |
|---|---|---|
| `echo` | 任意参数文本 | 返回参数正文；不用于执行脚本 |
| `create_uuid` | 无 | 随机 RFC 4122 UUID；不是创建通话 |
| `version` | 无 | 如实返回 RustSwitch 产品身份，不伪装 FreeSWITCH |
| `status` | 无 | 当前运行秒数、活动及已接通通话数；格式与原版不同 |
| `show` | `api as json` | 实际可调用 API 目录，`row_count`/`rows`；`ikey=rustswitch`；其他 show 子命令未实现 |
| `uuid_dump` | UUID、可选格式 | 真实单腿快照，txt/plain/json/xml及未知格式回退；完整字段、资源和错误边界见[快照手册](channel-snapshot-reference.md) |
| `uuid_exists` | UUID | 当前活动通道存在为 `true`，否则 `false` |
| `uuid_getvar` | UUID、变量名 | 读取普通通道变量或内置 `uuid`/`sip_call_id`；不存在普通变量为 `_undef_` |
| `uuid_setvar` | UUID、变量名、可选值 | 写普通变量；省略值删除；每腿独立，最多 128 个且合计 64KiB；不支持数组、展开及覆盖受保护身份 |
| `uuid_setvar_multi` | UUID、分号分隔的 name=value 列表 | 实际逐项写入/删除；每批≤64项/16KiB，支持单引号、转义及有限自定义分隔。详见 [批量变量](variables-reference.md) |
| `uuid_break` | UUID | 中断当前受支持的playback/read，受理后仍等待真实停止/完成事件；all/both及其他应用未支持 |
| `uuid_send_dtmf` | UUID、`digits[@milliseconds]` | 已 ACK 本地 A 腿发送 telephone-event；1..32 个按键，默认 250 ms，可指定 50..1000 ms；`+OK` 仅表示队列受理 |
| `uuid_send_dtmf_status` | UUID | **云蝠自有扩展，非 FreeSWITCH API**；返回发送状态、会话累计按键/包计数及错误；不能据查询成功认定原版兼容 |
| `uuid_kill` | UUID、可选 `NORMAL_CLEARING` | 真实终止两腿 SIP 并释放媒体；其他原因码未完成 |

每通桥接电话的 A/B 腿拥有不同 UUID，分别映射到自身 SIP Call-ID。本地 IVR 只有实际存在的 A 腿 UUID；通过 `sip.local_extensions` 精确号码启用，配置见 [本地呼入](ai-calling-reference.md)。UUID 不是媒体槽位，不会因端口复用而指向新通话。UUID 控制接口尚未实现原版的全部变量容器、全部错误分支与通道保留时序。

### 本地按键发送与受理语义

`api uuid_send_dtmf <uuid> 12#@100` 仅用于实际收到 SIP ACK 且媒体已分配的本地 A 腿。需要协商 telephone-event，事件时钟与主音频 RTP 时钟相同，`dtmf_type` 未设或为忽略大小写的 `rfc2833`；桥接 A/B 腿、INFO 发送、带内音频按键及原版暂停/分段参数明确拒绝。每通队列含当前按键最多 32 个，每 worker 最多 64 个活动发送器。

正文 `+OK <uuid> sent DTMF 12#@100.` 加换行沿用原版的有限响应形状，表示 Rust 媒体队列已受理。随后通过自有 `uuid_send_dtmf_status` 查询 `idle/sending/failed`；`completed_digits` 只说明对应按键的三个结束包已成功提交给 UDP，不证明远端收到或业务执行。发送动作不会伪造接收侧 `DTMF` 事件。

请求非幂等，超时或 ESL 断连不能推断没有执行；同一请求重发会再次排队。应先查询并结合此前累计计数判定，不确定且必须停止时可终止通话；`uuid_break` 只作用于受支持的播放/收号提示，不取消按键发送。完整字段、调度失败、取消与原版差异见 [DTMF 发送合同](dtmf-send-reference.md)。离线 [Audio SDK](audio-reference.md) 的可变采样率不改变这条实时发送路径，也不代表实时 ASR/TTS 或 AI 轮次控制已经接入。

## sendmsg 应用执行

已认证连接可向实际通道发送 `sendmsg <UUID>`，带 `call-command: execute`、`execute-app-name`、`execute-app-arg` 和末尾空行。目前执行 `answer`、`set`、`unset`、`read`、`playback`、`sleep`、`park`、`hangup` 的文档化子集。playback 支持受限单频提示音和 WAV，完整参数见 [XML与应用](dialplan-reference.md)。这些应用真实修改通道变量、收集 RTP/INFO 按键或驱动 Rust 媒体发包；不支持的应用和控制头明确拒绝。

`+OK` 是通过通道及资源校验后的受理结果。使用 `event-uuid` 与实际 `CHANNEL_EXECUTE_COMPLETE.Application-UUID` 关联完成结果；不能把命令回复当成播放或收号成功。`event-lock: true` 允许串行排队，每腿最多八项、全局最多 20000 项且另有 16 MiB 记账预算；read/放音应用总期限60秒；sleep按实际指定时间，park等真实挂机，不使用60秒伪完成；全通话仍受max_call_seconds限制。准确的配置相关任务上限、参数默认值、DTMF 失效、挂断取消与未开始任务事件见 [IVR 详细说明](ivr-reference.md)。

## 后台命令与事件

`bgapi` 使用相同已实现命令集。服务先返回 `+OK Job-UUID: <uuid>` 与 `Job-UUID` 头，随后在订阅者连接上发布 `BACKGROUND_JOB`，事件正文是实际 API 结果。客户端可提供 `Job-UUID`，超长值按原版有限长度处理。关联号不是幂等键，同号两次提交仍分别执行。客户端断连不等同取消后台任务；进程退出会取消，未提供持久作业恢复。

| 事件 | 实际触发 | 完整兼容边界 |
|---|---|---|
| `CHANNEL_CREATE` | SIP 呼叫通过准入，实际通道身份建立；本地呼入仅 A 腿 | 不含原版全部字段及模块事件 |
| `CHANNEL_ANSWER` | 实际收到并处理接通状态 | 不替代 ACK 到达与双向语音检查 |
| `CHANNEL_HANGUP` | 通话进入结束状态 | 不是媒体已回收的 `CHANNEL_HANGUP_COMPLETE` |
| `CHANNEL_EXECUTE` | 已受理应用实际开始执行 | 不为挂断时尚未执行的排队任务伪造开始事件 |
| `CHANNEL_EXECUTE_COMPLETE` | 应用实际结束或已受理任务被取消 | 必须核对关联标识、Application-Response、read_result 等变量；失败完成仍是完成事件 |
| `DTMF` | 完整去重的 RTP 电话事件，或通过对话校验的 INFO 按键 | 接收侧事件；不由 `uuid_send_dtmf` 的受理或本地发包合成，不覆盖音内检测或全部通道缓存语义 |
| `PLAYBACK_START` / `PLAYBACK_STOP` | 当前放音的实际生命周期 | 触发与错误场景见[事件手册](event-lifecycle-reference.md)，不能把订阅成功当播放成功 |
| `CHANNEL_PARK` / `CHANNEL_UNPARK` | 进入及离开当前停泊应用 | 当前应用范围；不代表停车检索、转接和完整事件字段 |
| `BACKGROUND_JOB` | 已受理后台 API 执行结束 | 不代表业务正文成功或持久化完成 |

支持固定1.11.3目录的92个普通事件名订阅（ALL单独处理，CUSTOM子类另为未实现范围）；接收订阅不会合成尚未实现的事件。支持连接级 `event plain|json|xml`、普通字符串 `filter`、`nixevent` 和 `noevents`。plain 值按原事件百分号规则编码；JSON 正文使用 `_body`；不同格式均独立计算外层字节长度。普通过滤使用忽略大小写的正向 OR 匹配与负向排除，不支持正则、数组或索引过滤。全部选择 `ALL` 只包含当前实际产生的事件，不代表原版事件全集已经实现。

受限应用调度已接入；outbound ESL、`myevents`、完整日志与 CUSTOM 子类、`CHANNEL_HANGUP_COMPLETE`、全部原版通道字段及上述八个应用之外的执行语义仍是独立缺口。未提供的行为不能返回固定 `+OK`。提示音媒体合同与失败状态见 [媒体交互接口](media-interaction.md)。

## 验证与剩余工作

[测试用例与成对执行器](conformance-testing.md) 保留全部原始对照 ID。协议单测验证分片、粘包、认证、并发、慢消费者及回收；真实 SIP/Rust 媒体测试验证事件、变量、正常拆线和 ESL 断连时语音继续。成对探针只认证各自明确断言，不推广为整个 API、模块或通信协议已经完全兼容。
