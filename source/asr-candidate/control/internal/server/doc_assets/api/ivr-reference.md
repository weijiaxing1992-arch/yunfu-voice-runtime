# ESL 应用、按键收集与本地 IVR

文档版本 1.12.0。连接协议见 [ESL 接口](esl-reference.md)，整体调用链见 [AI 呼叫接入](ai-calling-reference.md)，媒体字段见 [媒体交互接口](media-interaction.md)。

当前可实际执行的应用是 `answer`、`set`、`unset`、`read`、`playback`、`sleep`、`park`、`hangup` 的限定子集；新增基础动作和XML顺序执行见 [拨号计划合同](dialplan-reference.md)。它们操作真实 SIP 通道与 Rust 媒体，不把命令受理等同于应用执行完成。本文描述当前已实现范围；不表示 FreeSWITCH 全部应用、XML 拨号计划、文件播放器或 AI 音频图已兼容。

## 连接与通道

使用已启用的 ESL 入站端点和专用凭据完成 `auth`，订阅：

```text
event json CHANNEL_CREATE CHANNEL_ANSWER CHANNEL_HANGUP CHANNEL_EXECUTE CHANNEL_EXECUTE_COMPLETE DTMF

```

通过真实 `CHANNEL_CREATE` 的 `Unique-ID` 选择通道，使用 `variable_sip_call_id` 关联自己的 SIP 呼叫。双腿桥接通话的 UUID 与变量各自独立；本地 IVR 通话只有实际存在的 A 腿 UUID。不要使用媒体会话数字编号或 SIP Call-ID 代替 UUID。

`read`、`playback` 要求通道已经接通、ACK 已处理且媒体资源可用；建立中的通道返回错误。`set`、`unset` 也可作用于已经创建但尚未接通的通道。

## 发送应用

```text
sendmsg <通道UUID>
call-command: execute
execute-app-name: set
execute-app-arg: customer=中文客户
event-lock: true
event-uuid: <本次应用UUID>

```

末尾必须有空行。`event-uuid` 省略时生成新的 UUID；传入后可通过完成事件的 `Application-UUID` 关联本次请求。`sendmsg` 后省略通道 UUID 时可以使用 `session-id` 头，两处同时填写时必须一致。

`+OK` 仅表示实际通道已找到、请求校验通过并已进入有界业务队列。应用开始时发布 `CHANNEL_EXECUTE`，结束时发布 `CHANNEL_EXECUTE_COMPLETE`。检查 `Application`、`Application-UUID`、`Application-Response` 及相关变量，不要只检查 ESL 回复或客户端退出码。

`event-lock: true` 允许当前应用完成后再执行下一条应用。同一条腿最多八个已受理任务，包含正在运行的一条。已有长应用运行时，未声明 `event-lock: true` 的并发请求会被明确拒绝；抢占和嵌套执行尚未实现。

| 字段 | 当前规则 |
| --- | --- |
| `call-command` | 只支持 `execute`。 |
| `execute-app-name` | 当前八个受限应用，名称最多 64 字节。 |
| `execute-app-arg` | 最多 4096 字节 UTF-8，不支持 `${...}` 展开。 |
| `Content-Type: text/plain` | 没有非空 `execute-app-arg` 时，可用精确 `Content-Length` 正文传参。 |
| `loops` | 省略或 `1`；无限循环和其他循环数明确拒绝。 |
| `event-uuid` | 36 字节的应用关联标识；建议标准 UUID。 |
| `event-uuid-name` | 可选关联名称，最多 128 字节。 |
| `async` | 入站目标始终进入通道队列；不新增无限后台线程。 |
| 其他控制头 | 重复字段、`hold-bleg`、优先级抢占等未实现语义明确拒绝。 |

## 基础变量

```text
execute-app-name: set
execute-app-arg: customer=中文客户
```

`set name=value` 实际写入当前腿的基础变量；`set name` 或 `set name=` 删除该变量。`unset name` 显式删除变量。可以用 `api uuid_getvar <UUID> customer` 验证结果，不存在时返回 `_undef_`。

当前仅支持基础变量空间，每腿最多 128 个变量、键值合计 64 KiB。变量名称最多 128 字节，禁止身份覆写、数组、空格及展开语法；`uuid`、`sip_call_id` 为受保护身份。设置任意变量不等于相关 FreeSWITCH 模块或变量副作用已经实现。这里明确接入收号方式的变量是 `dtmf_type=info`。

## 收号 read

```text
read <最少位数> <最多位数> <silence或提示音> <结果变量> [首位超时毫秒 [终止符 [位间超时毫秒]]]
```

例如在当前通道收取 1 至 4 位号码：

```text
sendmsg <通道UUID>
call-command: execute
execute-app-name: read
execute-app-arg: 1 4 silence digits 3000 # 1000
event-lock: true
event-uuid: <本次应用UUID>

```

| 参数 | 当前范围与默认值 |
| --- | --- |
| 最少、最多位数 | 最少为 1 至 64，最多不小于最少且不超过 64；不支持无限收号。 |
| 提示音 | `silence` 按原版语义跳过提示文件；或者下节的单频 `tone_stream`、根目录内受限WAV（read分词不接受路径空格）。 |
| 结果变量 | 基础变量名；不能使用 `read_result`、`read_terminator_used` 作为结果变量。 |
| 首位超时 | 默认 1000 ms；接受 0 至 60000 ms，低于 1000 的值按原版 `read` 包装规则提升至 1000。 |
| 终止符 | 默认 `#`；支持 `0-9`、`*`、`#`、`A-D` 的字面集合。`none` 表示无终止符。 |
| 位间超时 | 默认沿用首位超时；显式 0 仍沿用首位超时，非零允许 1 至 60000 ms。 |

达到最大位数或收到终止符后结束。终止符不计入结果。普通终止或收满且满足最少位数时，`read_result=success`；计时到期且已有足够位数时为 `timeout`；不足最少位数时为 `failure`。有终止符时 `read_terminator_used` 保存该字符；非空号码写入结果变量。每次开始会清除旧的本次收号结果。

`read` 正常结束的 `Application-Response` 为 `_none_`，因此必须同时查看 `read_result`。媒体观测不完整、播放失败或资源不足则明确返回失败的完成事件，不把这些异常解释为用户沉默。

当前整个应用另有 60 秒硬期限，避免持续按键反复续期后长期占用任务。提示音期间收到有效按键会申请停止提示音，并等待媒体停止确认；之后继续收号或完成当前应用。首位等待时间从无提示音开始、或提示音结束后计算；收到数字后使用位间超时。

## RTP 与 SIP INFO 按键

默认收集真实、已协商的 RTP `telephone-event`。来源地址、载荷、事件集合、结束标志及重复结束包由 Rust 媒体校验；只有完整且去重后的按键交给业务。支持菜单按键 `0-9`、`*`、`#`、`A-D`。Hook flash 不作为菜单数字。

媒体层使用每工作进程固定容量的事件记录；控制层每工作进程最多一个在途批量查询。事件记录溢出、缺少结束包或无法继续可靠去重时，会让相关收号失败，并保持该通话腿的 RTP 收号禁用状态直到挂机，后续 `read` 不能重新伪装成成功或普通超时。桥接媒体与通话释放仍继续处理。

需要 SIP INFO 收号时，先在相应通道执行：

```text
execute-app-name: set
execute-app-arg: dtmf_type=info
```

此模式接收经真实 SIP 对话、来源、标签和 CSeq 校验的 `application/dtmf-relay`。模式值忽略大小写，建议使用 `info`。同一事务重传不重复收号；同时到来的 RTP 按键不重复交给业务。INFO 不要求 SDP 协商 `telephone-event`。已经禁用的 RTP 观测不会否定独立的 INFO 输入；切回默认 RTP 后，原失效状态仍需挂机解除。`Signal` 接受菜单按键及数字事件编号 10–15，`Duration` 必须显式提供 20–8191 毫秒；其他 INFO 格式和缺省 Duration 尚不支持。

两种方式都发布 `DTMF` 事件，包含 `DTMF-Digit`、`DTMF-Source` 和以 8 kHz 刻度表示的 `DTMF-Duration`。这不表示已实现带内音频 DTMF 检测。

## 真实提示音播放

```text
execute-app-name: playback
execute-app-arg: tone_stream://%(200,0,440)
```

上例向指定通道腿发送 200 ms、440 Hz 的单频音调。当前只支持零间隔、单个频率的格式；时长为 20 至 10000 ms 且必须为 20 的整数倍，频率为 200 至 2000 Hz。Rust 按 20 ms 发包，支持当前 PCMU/PCMA、8 kHz 通话。每媒体工作进程最多同时执行 64 路此类提示音。无效时长不能成功播放，调用者须检查受理回复及失败完成事件。

播放期间替换目标腿的主音频，DTMF 和 RTCP 继续处理。自然结束须媒体确认所有目标包发送；主动break/终止键则确认实际停止。两者依原版都返回 `FILE PLAYED`，须结合终止变量和媒体计数区分；实际远端收到并播放仍需端到端媒体验证。完成后恢复正常桥接音频。本地 IVR 通道也可向真实 A 腿播放，不需要虚构上游通道。

收号时附带提示音：

```text
execute-app-name: read
execute-app-arg: 1 1 tone_stream://%(2000,0,440) choice 3000 #
```

当前支持受控根目录内的[有限 WAV 文件](file-playback-reference.md)、受限 `sleep`、`answer`、`park` 与[XML 顺序应用](dialplan-reference.md)。URL、供应商 TTS 应用、多频混音、循环语法、录音、任意格式 PCM 注入、`play_and_get_digits`、会议及完整原版应用合同仍未完成。G.711 实时图和本地播放范围见[本地处理说明](processed-local-reference.md)。

## 本地 1000 IVR 呼入

在启动配置的 `sip` 中增加精确号码列表：

```json
{
  "sip": {
    "local_extensions": ["1000"]
  }
}
```

按完整配置校验并重启服务后，发往 `sip:1000@服务器地址` 的 INVITE 走本地自动应答路由。号码是精确匹配，不是正则拨号计划；每项只允许数字和 `+` 字符、长度 1–32，最多 256 项，不允许重复。收到实际 A 腿的 `CHANNEL_ANSWER`、确认 ACK 已处理后，即可发送上面的 `read` 或 `playback`。未配置 XML 时未命中号码仍走固定上游；启用 XML 后未命中规则返回 404。本地呼入缺少 ACK 时按期限清理。受限 `answer`、`park` 和 XML 顺序应用已有独立合同，不代表原版完整拨号计划或任意应用兼容。

## 资源与结束语义

ESL 应用受理队列为 128 条。全局已受理任务上限按配置呼叫数计算，为 `min(max(2 × max_calls + 128, 128), 20000)`；另有 16 MiB 应用记账预算。每条腿最多八条任务，满额时显式拒绝新请求。应用等待使用可更新的定时器堆，不为每条通话创建 goroutine 或轮询任务。

真实挂断会取消活跃应用和未执行队列，释放应用额度与定时对象；媒体会话释放会停止该会话的提示音。未执行任务的失败完成事件带 `variable_rustswitch_application_state=cancelled_before_execution`，不伪造开始事件。已受理任务属于通话，ESL 客户端断开不会擅自终止通话或丢弃任务。

目前尚无完整的原版通道 DTMF 缓存：已被控制层观察但尚未开始 `read` 的按键不保留为无限输入队列；媒体记录中尚未拉取的较早按键可能在 `read` 开始后被接收。严格的预收号、`flush_dtmf`、原版完整缓存时序和多菜单队列差分仍待实现，不能宣称开始前按键已经被严格隔离。

## 可执行验证

`tests/esl_applications_e2e.py` 使用真实控制进程、Rust 媒体和 SIP/RTP 端点，提供九个有限场景：变量与菜单、真实音调及桥接恢复、提示音打断和缺结束包、INFO 去重、挂机取消、超时及未实现应用拒绝、本地无上游 IVR、本地缺 ACK 清理、未命中号码走上游。支持与既有回归一致的 `--control`、`--media`、`--connected` 参数。

`tests/esl_applications_native.py` 用于显式隔离的已启动服务，仅创建和清理一通自有呼叫，保存原始 SIP/ESL/RTP、真实 `set`/`unset`/`read silence` 结果。两端分别执行后可比较三个正常路径的结果；版本属于身份信息。脚本自测或候选自身回归不能替代原版差分结果，正常路径通过也不覆盖本文明确列出的缺口。

## WAV 与基础动作（1.7）

playback也支持根目录内受限WAV，加载中、播放失败、停止和回收均有独立状态，详见[file-playback-reference](file-playback-reference.md)。sleep按实际指定毫秒等待；park等实际挂机，不在60秒伪完成。两者仍受通话最大时长约束，完整顺序与限制见[dialplan-reference](dialplan-reference.md)。

## 播放终止与原版响应（1.7）

`playback_terminators` 缺省为 `*`；`none`禁用，`any`匹配0–9、*、#，或设置文档化的字面菜单按键集合。每次播放先清除旧 `playback_terminator_used`，实际终止按键写入该变量；API `uuid_break <uuid>`没有按键，不伪造该变量。

普通uuid_break受理返回+OK，当前playback需等待真实停止再以FILE PLAYED完成。read中的uuid_break仅停止仍在播放的提示音，继续按读号条件与期限等待；不是立即成功收号。all/both、任意应用打断和park抢占未实现并明确拒绝。

缺失WAV的完成正文为FILE NOT FOUND；损坏/不支持格式仍明确错误。XML在加载失败后停止计划并挂机，不将FILE NOT FOUND误认成可继续的成功。FILE PLAYED同时覆盖原版的自然结束和主动中断，不能代替完整音频包数检查。

## 已 ACK 的 PCM 供音与 IVR

1.12 提供自有 [RVA1 外部 PCM](rva1-reference.md)，面向同 UID 可信进程，以已 ACK 的本地 A 腿 UUID 授权 8k mono S16LE 下行；不是新的 ESL `speak`、`play_and_get_digits` 或 FreeSWITCH TTS 模块实现。接收方向仍正常处理，客户端可根据实际按键结果选择供音或明确打断。

PCM begin 与活动或已排队的 tone/WAV、带提示 read 互斥；有 PCM 所有权时启动这些播放也拒绝。`read ... silence ...` 和 park 可并存，避免为了持续收号创建假提示音。`uuid_break`、播放终止键与 read 完成不会自动执行 PCM INTERRUPT；应用需要显式以当前 token/turn_id 调用 0/20/40ms 中断，并查询实际终态。20/40ms 只使用已有待发头帧淡出，不撤回已成功提交的 RTP，不等待迟到的旧轮音频。

外部媒体资格由真实 ACK 控制；无 ACK 到期发送 A 腿 BYE 并回收的修复及 1 秒测试定时器限制见 [1.12 验证](release-validation-v1.12.md)。本轮没有接入 VAD 自动触发、ASR/TTS 供应商或双轨录音，ESL 的既有事件及受理/完成语义保持。
