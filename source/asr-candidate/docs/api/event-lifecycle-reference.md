# 应用与媒体生命周期事件（1.12 受限合同）

本页说明当前已实现的 `PLAYBACK_START`、`PLAYBACK_STOP`、`CHANNEL_PARK`、`CHANNEL_UNPARK`。合同依据固定 FreeSWITCH 1.11.3 的 `src/switch_ivr_play_say.c`、`src/switch_ivr.c` 及独立原版报文。支持事件订阅、单个子场景通过、整个事件模块完全兼容，是三个不同结论；本页不宣称后者。

配合 [ESL 接口](esl-reference.md)、[IVR 应用](ivr-reference.md)、[XML 拨号计划](dialplan-reference.md) 使用。应用受理 `+OK` 只证明进入有界执行队列；生命周期事件反映后续真实执行。

## 订阅与关联

```text
event json CHANNEL_EXECUTE CHANNEL_EXECUTE_COMPLETE PLAYBACK_START PLAYBACK_STOP CHANNEL_PARK CHANNEL_UNPARK CHANNEL_HANGUP
```

四个事件均使用当前执行腿的 `Unique-ID`。与原版一致，不添加顶层 `Application` 或 `Application-UUID`；客户端应在同一通道匹配应用的 `CHANNEL_EXECUTE` 到 `CHANNEL_EXECUTE_COMPLETE` 区间内关联。当前执行器每腿串行，最多 8 个已受理任务（含正在执行的 1 个）。

| 字段 | 当前含义与范围 |
| --- | --- |
| `Unique-ID` / `variable_uuid` | 当前真实通道腿 UUID；用户同名变量不能覆盖 |
| `Call-Direction` | A 腿 `inbound`，B 腿 `outbound` |
| `Other-Leg-Unique-ID` | 真实桥接的另一腿；本地单腿呼入不生成此字段 |
| `Channel-Name` | 候选实际身份 `rustswitch/<方向>/<SIP Call-ID>`，不冒充 Sofia 名称 |
| `Channel-State` | 当前应用执行状态 `CS_EXECUTE` |
| `Answer-State` | 已接通执行为 `answered`；挂断回收期间为 `hangup` |
| `variable_sip_call_id` | 当前腿的 SIP Call-ID |
| `variable_current_application` / `variable_current_application_data` | 当前应用名称与完整参数 |
| `variable_<名称>` | 当前腿真实变量快照，沿用变量预算 |

ESL 发布器提供事件名称、时间戳与序号等通用包装。尚未逐字段复刻原版完整 caller profile、codec 状态、文件偏移和播放时长变量；客户端不能据此把整个事件报文当成原版字节级等价。

## 播放开始与停止

适用于受限 `playback`，以及 `read` 中实际存在的 WAV/tone 提示音。纯 `read ... silence ...` 没有播放器，不发这两个事件。

| 触发 | 事件及顺序 |
| --- | --- |
| 文件加载中 | 不发 `PLAYBACK_START`；`loading` 不是播放成功 |
| 媒体确认音源准备成功并进入 `running` | 一次 `PLAYBACK_START` |
| 短音频首次查询已经完整发送 | 根据 `completed` 及完整包计数依次发布 START、STOP |
| 正常自然结束 | `PLAYBACK_STOP`，`Playback-Status: done` |
| `uuid_break` 或有效终止键 | 媒体确认停止后发布 STOP，`Playback-Status: break` |
| 播放中挂断 | 确认媒体会话释放或原 worker 已终止后 STOP，状态 `done`，然后应用 COMPLETE |
| 已开始播放后媒体确认失败 | STOP 状态 `done`，应用完成回应保留失败 |
| 缺失或无效文件在加载阶段失败 | 无 START/STOP；例如缺文件仍是 `FILE NOT FOUND` |

`Playback-File-Path` 是实际传给受限媒体 API 的相对 WAV 名称，或完整 `tone_stream://...` URI。`read` 事件取提示音参数，不取整条收号参数。tone 额外带 `Playback-File-Type: tone_stream`，WAV 不添加该字段。当前相对路径基于配置的 `media.playback_root`；原版由 `sounds_dir`、绝对路径、语言等规则解析的完整文件定位方式仍存在范围差异。

原版 `Playback-Status: done` 表示非 `SWITCH_STATUS_BREAK` 退出，**不能单独证明成功播放全部内容**。判断结果需结合应用响应、实际媒体包和变量。自然结束和主动打断的 `playback` 应用响应均可为 `FILE PLAYED`；终止键另看 `playback_terminator_used`。挂机取消的候选应用响应目前保留明确 `-ERR channel ended: ...`，不宣称与原版所有失败回应一致。

`read` 提示音 STOP 不表示收号 COMPLETE；例如 `uuid_break` 只停止提示音，随后继续等待有效按键或读号期限。

## park 进入与离开

真实进入 `park` 并占用当前腿执行槽后发一次 `CHANNEL_PARK`；不启动每通话线程，也不在 60 秒后伪造成功。实际挂断或故障退出槽位时，先发一次 `CHANNEL_UNPARK`，再发该应用 `CHANNEL_EXECUTE_COMPLETE`。正常挂断下 park 完成回应为 `_none_`。重复 ACK、释放和取消不会重复这两个事件。

当前未实现通过 `uuid_transfer`、`uuid_break all` 或完整拨号计划控制流主动离开 park 后恢复下一段执行的原版全部语义。已排队但尚未开始的 park 被挂断取消时，不发虚假的 PARK/UNPARK。

## 故障、背压与证据边界

媒体 RPC 失联不等于停止成功。播放任务保留单个执行槽，每腿最多一个在途 RPC；使用 100/200/400/800/1000 毫秒退避，附播放编号产生的 0–36 毫秒错峰。每次主循环最多推进 256 个应用定时项，不续期既有 60 秒硬期限。到期仍无法确认停止时，真实结束通话，再等媒体所有权释放。释放失败继续保留应用、内存和媒体资源计数，不能提前发布 STOP/COMPLETE。

挂机时，已经提交的播放查询与媒体释放回复可能通过不同队列到达；执行器等待两者收敛，防止迟到 START 出现在 COMPLETE 后。仅复用原任务预算，不额外创建无界取消队列或每通话协程。

仍需保留一个观测边界：异步文件加载返回 `loading` 后，如果媒体在下一次状态查询前已经开始、又立即遇到会话释放，当前 IPC 没有独立持久播放事件日志。控制面没有可证明开始的回执时不会补造 START/STOP；这类极短窗口的全事件完备性尚未验证，不能标为完全等价。当前验收覆盖实际收到音频和开始确认后的停止/挂机，以及加载失败无伪事件。

独立测试入口是 `tests/event_lifecycle_e2e.py`（真实 SIP/Rust RTP）及 `control/internal/server/application_lifecycle_test.go`（状态、乱序、失联、预算与去重负例）。单元测试自身通过不计产品兼容通过；原版双端报文及构建指纹应以管理后台最终证据报告为准。

## PCM 轮次的独立生命周期

1.12 的 [RVA1](rva1-reference.md) / [内部 PCM](pcm-turn-reference.md) 不是 ESL 应用执行器。它不伪造 PLAYBACK_START/STOP、CHANNEL_EXECUTE_COMPLETE、FILE PLAYED 或 ASR/TTS 事件；调用方使用 token/turn_id 和完整状态回执关联 buffering、playing、draining、stopping、completed、stopped、failed。

accepted_samples 是整批入队，sent_samples 是实际 UDP 完整成功返回；完成还须最后 20ms 尾帧区间结束。中断保留真实已发送量，丢弃未发送帧，所有计数守恒；队列关闭或 Go 句柄退休本身不证明媒体终态。数据结果未知时保留控制恢复身份，不自动补发或发布成功事件。

静默 read、park 和按键采集可与 PCM 并存，各自事件仍只说明各自应用。`uuid_break` 不会停止 PCM；需要明确 INTERRUPT。连接 EOF 不自动 FORGET 已授权轮，通话挂机、Worker 换代或 Server 关闭会撤销旧句柄并按实际媒体回收结束，不能让迟到旧数据重新发音。
