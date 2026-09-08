# 本地电话 DTMF 发送与状态查询

文档版本 1.9.0。云蝠 Voice Runtime（RustSwitch）实现 FreeSWITCH 1.11.3 `uuid_send_dtmf` 的受限参数与真实 RTP telephone-event 发送路径，并提供自有 `uuid_send_dtmf_status` 查询。当前只面向已接通的本地 A 腿；不能推广为桥接外呼、所有原版 DTMF 参数或完整 FreeSWITCH 兼容。入站连接及认证见 [ESL 接口](esl-reference.md)，接收侧事件与收号见 [IVR 接口](ivr-reference.md)。

## 使用前提

通道必须是仍存活的本地 A 腿，例如通过 `sip.local_extensions` 精确号码接入，或命中受限 [XML 拨号计划](dialplan-reference.md)。必须实际收到 SIP ACK、已分配媒体且未进入释放状态；收到 `CHANNEL_ANSWER` 本身不保证 ACK 已到达。本地号码与呼入配置见 [AI 呼叫接入](ai-calling-reference.md)。

发送还需满足以下条件：

| 条件 | 当前合同 |
|---|---|
| 通道方向 | 仅本地 A 腿；桥接电话的 A 腿、B 腿均拒绝 |
| SDP | 已协商 telephone-event，待发按键属于协商的事件集合 |
| 时钟 | telephone-event 时钟必须等于主音频的 RTP 时钟；不能拿 PCM 采样率替代 RTP 时钟 |
| 发送模式 | 通道变量 `dtmf_type` 未设、为空或忽略大小写等于 `rfc2833` |
| 资源 | 通道队列、worker 活动发送器及控制 IPC 队列均有剩余预算 |

`dtmf_type=info` 的已实现能力是受限 SIP INFO **接收**，不表示可以通过此 API 发 INFO。此 API 发送 RFC4733 telephone-event 载荷，不生成带内双音频，不触发本地接收侧 `DTMF` 事件，也不会直接把所发数字灌入本地 `read`。

## 发送请求

认证后的 ESL 命令如下，每个请求用空行结束；UUID 使用实际通道 UUID，不能使用 SIP Call-ID 或媒体槽位。

```text
api uuid_send_dtmf <uuid> <digits>[@milliseconds]

```

| 参数 | 限制及默认值 |
|---|---|
| `uuid` | 一个实际通道 UUID，最多 128 字节 |
| `digits` | 1..32 个 ASCII 字符，只接受 `0–9`、`*`、`#`、大写 `A–D` |
| `milliseconds` | 每个数字持续时间；省略为 **250 ms**；显式值是 50..1000 的十进制整数，最多四位数字 |
| 分隔 | UUID 与按键串之间使用一个普通空格；不支持额外参数、按键串内空白、暂停或分段语法 |

50..1000 ms **不要求是 20 ms 的整数倍**。整串参数、按键集合和容量先校验，再原子加入队列；未知字符、未协商按键或队列放不下时，不先发送合法前缀。一次提交和通道已有待发数字合计不得超过 32 个，其中包含正在发送的数字。原版的 `w/W`、`+` 分段、`~` 标志、变量覆盖和小写 `a–d` 均不在当前子集内。

例如，发送三个持续 100 ms 的按键：

```text
api uuid_send_dtmf <uuid> 12#@100

```

成功正文形状为：

```text
+OK <uuid> sent DTMF 12#@100.
```

正文末尾有换行。外层仍为 `Content-Type: api/response`，`Content-Length` 按实际 UTF-8 字节计算。该文本沿用原版有限响应形状，但含义是 **Rust 媒体发送队列已受理**；不等待整串发送结束，不保证远端收到，更不证明对方菜单执行成功。

`bgapi uuid_send_dtmf ...` 使用相同业务实现；`BACKGROUND_JOB` 的正文仍是该受理结果。Job UUID 不是幂等键，也不是媒体发送完成凭证。

## 自有状态查询

```text
api uuid_send_dtmf_status <uuid>

```

这是 **云蝠自有扩展，FreeSWITCH 1.11.3 无对应同名 API**。它不能作为原版兼容项标绿。查询要求同一个仍存活、已 ACK 且媒体已分配的本地 A 腿；挂断后不保留可查询的历史通道状态。

成功正文为 JSON，固定包含以下字段：

| 字段 | 口径 |
|---|---|
| `uuid` | 本次查询的实际 A 腿 UUID |
| `state` | `idle`、`sending` 或 `failed` |
| `accepted_digits` | 当前媒体会话累计受理的数字数 |
| `completed_digits` | 累计完成三个结束包 UDP 提交的数字数 |
| `failed_digits` | 发送失败时累计清除的未完成数字数，包括当时正在发送的数字 |
| `queued_digits` | 当前未完成数字数，包括正在发送的一位；0..32 |
| `sent_packets` | 累计成功提交的 telephone-event RTP 包数，包括结束包；不含音频包 |
| `last_error` | 最近发送失败原因；没有失败时为空字符串；新请求受理不清除历史错误 |

`idle` 表示未发送过或队列已排空；`sending` 表示仍有待发数字；`failed` 表示当前发送因错误终止且余队已清空。显式新请求可以使失败后的发送器再次进入 `sending`，因此应同时看当前状态、累计计数和 `last_error`，不能只根据错误字段非空判断本批仍在失败。

下面是无历史失败、无并发提交、只发送过 `12#@100` 且完成时的**示例**，不是某次部署的实际结果：

```json
{
  "uuid": "实际通道 UUID",
  "state": "idle",
  "accepted_digits": 3,
  "completed_digits": 3,
  "failed_digits": 0,
  "queued_digits": 0,
  "sent_packets": 21,
  "last_error": ""
}
```

这些计数按会话累计，不是最近一次 API 的批次结果。当前没有发送批次 ID、turn ID 或每批完成事件；需要归属一次业务操作时，由单一协调方记录提交前后计数，避免多个调用方同时发送导致归属不清。`completed_digits` 的“完成”仅指本机 UDP 提交成功，不能作为远端收听、接收或 IVR 跳转确认。

## RTP 时序与资源预算

当前发送器使用有界定时调度，不为每个通话创建发送线程。只有活动发送器进入调度集合，每次驱动最多处理 64 个到期发送器；资源不足返回错误，不无上限创建任务。

| 项目 | 当前限制或行为 |
|---|---|
| 单通道队列 | 最多 32 位，包含正在发送的一位；整批加入或整批拒绝 |
| 每 worker 活动发送器 | 最多 64 个；不是系统总通话容量 |
| 常规发包间隔 | 20 ms；最后的持续时间按请求毫秒值精确收尾 |
| 持续时间 | 最终 `duration` 为请求毫秒数乘以协商时钟再除以 1000 的刻度值；8 kHz 下 100 ms 为 800 ticks |
| 结束包 | 每个数字发送三个带结束标志的包，最终 duration 相同，RTP 序号各自递增，间隔 20 ms |
| 数字间隔 | 下一数字时间起点距离上一数字结束时间 100 ms；跨 API 提交仍保持此间隔 |
| 迟到处理 | 距本次计划发送时刻迟到达到 20 ms 即失败并清除剩余队列，不追赶补发一串过期包 |
| 发送错误 | UDP 提交失败即终止当前发送并清余队，错误和失败计数可查询 |

同一数字的 RTP 事件时间戳保持不变，首包带 marker，结束重传使用新的 RTP 序号。按键与本地提示音/WAV 播放共享本地发送 SSRC、序号及绝对 RTP 时钟，播放不会重置发送中的按键时间线。首次启用本地发送时间线后，媒体层拒绝把该会话 `connect` 成透明桥，重复 `connect` 也拒绝，避免混入另一端的 RTP 序号与 RTCP 状态。

同格式桥接 RTP 仍走原透传路径，不因为新增发送器变成混音或转码。可变采样率 [Audio SDK](audio-reference.md) 属于离线入口，这条按键发送路径尚未接入实时 ASR/TTS、Audio Graph、完整 turn 取消、淡入淡出或多分支音频调度。

## 超时、错误与取消

ESL API 有默认 5 秒预算，控制请求和媒体 IPC 队列也有上限。**超时或连接断开时，操作可能已经被媒体受理。** 当前请求非幂等，相同文本重试会再次入队；不要把超时解释成未发送，也不要仅凭原 Job UUID 重发。

出现不确定结果时，先查询存活通道的状态并与提交前计数比较；如果还有其他发送方，不能仅靠累计差值断言某批是否执行。业务必须立即停止且无法确认时，可通过 `uuid_kill <uuid> NORMAL_CLEARING` 终止真实通话，代价是该通话结束。没有独立的 DTMF 队列取消 API；`uuid_break` 仅中断当前受支持的播放/收号提示，不取消已排队按键。

挂断和媒体会话释放会撤销剩余数字，计入 worker 的 `dtmf_send_cancelled_digits` 后回收发送器。通道销毁后状态查询返回找不到会话；不能期望查询到历史 `cancelled` 状态。媒体 IPC 超时可能触发现有 worker 失联和重启策略，相关通话也可能终止；这同样不能证明此前一个包都没发过。

| 场景 | 典型正文或状态 |
|---|---|
| 缺少 UUID/按键串 | `-USAGE: <uuid> <dtmf_data>` 加换行 |
| 查询缺少 UUID 或含额外参数 | `-USAGE: <uuid>` 加换行 |
| 非法时长 | `-ERR DTMF duration must be 50..1000 ms` 加换行 |
| 非法按键或串长 | `-ERR DTMF requires 1..32 digits from 0-9*#A-D` 加换行 |
| 通道不存在或已结束 | `-ERR Cannot locate session!` 加换行 |
| 桥接通话或非本地 A 腿 | `-ERR DTMF send supports local A leg only` 加换行 |
| 未 ACK、未分配媒体或正在释放 | `-ERR channel media is not established` 加换行 |
| 模式为 INFO 等非 rfc2833 值 | `-ERR DTMF send requires rfc2833 mode` 加换行 |
| 未协商事件或事件时钟不匹配 | `-ERR telephone-event with the audio RTP clock was not negotiated` 加换行 |
| 控制/媒体队列不能受理 | `-ERR DTMF command was not queued: <原因>`，或媒体返回的 `-ERR <原因>` |
| 已受理后调度迟到 | `state=failed`，`last_error=dtmf_send_scheduler_late` |
| 已受理后 UDP 发送失败 | `state=failed`，`last_error=dtmf_udp_send_failed` |

worker 统计的 `dtmf_send_packets`、`dtmf_send_errors`、`dtmf_send_cancelled_digits` 和 `dtmf_send_active` 用于观测实际包数、错误、取消及活动发送器；它们与会话级状态不同，不能直接相加或当作对端确认。

## 与 FreeSWITCH 的已知边界

原版对照固定 FreeSWITCH 1.11.3。实际原版抓包确认 `12#@100` 产生 21 个 telephone-event 包，每位三个结束包、800 ticks 最终 duration；这些是可逐包验证的有限参数断言，不能推广为全部按键语法和线路兼容。

同一轮本地 1600 ms 提示音与按键交叠观察存在明确时序差异：原版输出 74 个音频包，按键位置出现 800 ticks 间隔；候选持续输出 80 个音频包。候选保留连续音频的当前行为，不能宣称与原版逐包时序完全一致。远端对并行音频和 telephone-event 的行为仍需实际终端及线路验收。

| 原版或目标能力 | 当前状态 |
|---|---|
| 本地 A 腿 `digits[@milliseconds]` | 已实现上述有界子集；需核对实际部署测试证据 |
| 桥接通话、任意 A/B 腿发送 | 未实现，明确拒绝 |
| 暂停、分段、特殊标志、所有变量覆盖 | 未实现，明确拒绝 |
| INFO/带内 DTMF 发送 | 未实现；现有 INFO 是接收子集 |
| 接收 DTMF 事件及 read | 已有独立接收路径与有限 IVR 合同，不与本地发送成功混算 |
| 发送完成和对端业务成功 | 查询只证明本地发包提交；没有远端确认或业务应答合同 |
| `uuid_send_dtmf_status` | 自有扩展，不能记为原版同名接口通过 |
| 实时 Voice Agent 音频分支 | 尚未接入；离线 SDK、按键控制与完整 AI 音频流程分别验收 |

部署应成套更新 Go 控制面与 Rust 媒体；旧媒体握手通过不能证明支持新增操作。媒体字段见 [IPC Schema](media-ipc.schema.json)，所有兼容缺口继续保留在 [FreeSWITCH 对照](freeswitch-comparison.md) 与 [验证账本](verification-ledger.md)。
