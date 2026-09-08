# 05 通信协议与 SDK 文档

本文面向电话接入、实时音频和原生适配开发者，按调用边界汇总 RustSwitch 主源码及 ASR 候选的非 HTTP 接口。主文档基线为 **1.13.0**；HTTP 管理入口另见 [04](04-HTTP接口文档.md)，版本关系见[文档中心](DOCUMENTATION.md)。

## 接口分类与适用范围

接入前先选择接口层级：SIP、ESL 等面向对端或应用；Go/Rust JSON IPC、FD3/FD4 面向受控进程；RVA1 和 ASR1 按各自本机权限及生命周期合同使用。内部接口不应直接作为公网服务暴露。下表链接的专题提供帧格式、字段、状态、错误及示例。

| 接口 | 实现范围 | 技术参考 |
|---|---|---|
| SIP/SDP、RTP/RTCP、CLI、日志 | 限定基线实现 | [通信协议总册](reference/docs/api/protocol-reference.md) |
| 上游 REGISTER / INVITE Digest | 单固定上游客户端 | [注册与认证](reference/docs/api/trunk-registration.md) |
| 入站 ESL / fs_cli 子集 | 显式开启，有限命令/事件 | [ESL 参考](reference/docs/api/esl-reference.md) |
| IVR/read/playback 等 | 本地 A 腿有限应用 | [IVR 合同](reference/docs/api/ivr-reference.md) |
| 通道变量与批量设置 | 有限变量容器与指定应用副作用 | [变量](reference/docs/api/variables-reference.md) |
| 通道快照/uuid_dump | 单腿状态快照 | [快照](reference/docs/api/channel-snapshot-reference.md) |
| 事件生命周期 | 有界事件流，明确完成关联 | [事件](reference/docs/api/event-lifecycle-reference.md) |
| 运行 XML Dialplan | 启动时冻结的条件与动作子集 | [拨号计划](reference/docs/api/dialplan-reference.md) |
| 媒体 JSON IPC | Go → Rust 进程间内部协议 | [协议总册](reference/docs/api/protocol-reference.md)、[JSON Schema](reference/docs/api/media-ipc.schema.json) |
| 播放/收号/DTMF 控制 | 受理不等于完成 | [媒体交互](reference/docs/api/media-interaction.md)、[DTMF 发送](reference/docs/api/dtmf-send-reference.md) |
| G.711 双腿/本地图 | 8 kHz/20 ms 限定实时图 | [双腿](reference/docs/api/processed-media-reference.md)、[本地](reference/docs/api/processed-local-reference.md) |
| RSP1/RSR1 PCM turn | FD3，内部控制与数据分离 | [PCM turn](reference/docs/api/pcm-turn-reference.md) |
| RVA1/RVR1 | 本机同 UID 外部 PCM 流 | [RVA1](reference/docs/api/rva1-reference.md) |
| RXS2 / Server RX SDK | FD4、已 ACK 本地 A 腿、原生收音 | [RX](reference/docs/api/rx-stream-reference.md) |
| AudioFrame/Codec SDK | 离线编解码与帧基础 | [音频](reference/docs/api/audio-reference.md) |
| C 编解码 ABI | 自有 ABI 版本 1 | [头文件](reference/native/include/rustswitch_codec.h) |
| C 协议 ABI | 预留，未完成生产提供器 | [头文件](reference/native/include/rustswitch_protocol.h) |
| ASR1 / StartASR | 独立候选，非公开 HTTP | [候选接口合同](candidate/asr-stream-reference.md)、[模型增量](candidate/asr-openapi-update-proposal.md) |

## SIP 与音频编码支持范围

初始 INVITE、ACK、CANCEL、BYE、OPTIONS 和显式 INFO 子集可用；入站 REGISTER、REFER、UPDATE、PRACK、完整 re-INVITE/保持恢复及复杂路由不能按 FreeSWITCH 原功能使用。上游 REGISTER 客户端不等于允许分机向本项目注册。

| 编码/媒体 | relay | 当前实时处理图 | AI 上行候选 |
|---|---|---|---|
| PCMU / PCMA | 有限同格式转发 | G.711 双腿/本地 8 kHz、mono、20 ms | 原生 8 kHz 或按需 16 kHz |
| G.722 | 有限协商/转发 | 可选离线 SDK，未接实时图 | 未接入 |
| Opus | 有限协商/转发 | 可选离线 SDK，未接实时图 | 未接入 |
| G.729、G.726/AAL2、L16 | 以 SDP/载荷规则为准的透传 | 不等于已具备实时编解码 | 未接入 |
| AMR-NB/WB、EVS 等 | 不作已互通承诺 | 未完成 | 未接入 |
| telephone-event | 独立 PT/clock/事件集合 | 收集与本地发送子集 | 辅助元数据，不伪造 PCM |
| CN、PLC、丢包 | 按路径区分 | G.711 图中分别表达 | 元数据/缺口，不冒充真实用户语音 |

`/v1/codecs` 的可用库、SDK 自检与实际 worker 能力是不同维度。文件中写了 codec 名称或可导出配置，不代表该编码已经在实际电话中完成转码。

## 控制与媒体报文示例

ESL 先认证、按帧分派响应与异步事件。`+OK` 表示命令受理，应用结束需匹配真实完成事件。

```text
api uuid_exists <真实通道UUID>

sendmsg <真实通道UUID>
call-command: execute
execute-app-name: read
execute-app-arg: 1 4 tone_stream://%(400,0,440) menu_choice 3000 #
event-lock: true

```

以下是内部子进程 JSONL，不能发送到 HTTP 或 ESL：

```json
{"id":101,"op":"pcm_turn_begin","session":42,"turn_id":100,"buffer_ms":200,"prebuffer_ms":40}
```

RVA1 的 BEGIN 必须以真实已 ACK 本地通道授权，再通过独立数据连接送 8 kHz mono S16LE；END 后继续确认排空，INTERRUPT 支持 0/20/40 ms。`uuid_break` 不自动等价于 PCM 轮次中断。

RXS2 每消息固定 168 字节头、正文最多 480 字节，单订阅出口队列 8 项；160 个原生样本表示 20 ms。传出/写入计数不等于业务实际消费。候选 ASR1 每帧最多 8196 字节、64 个来源区间、8 项结果队列，FINISH/DONE 精确确认完整前缀；其完整二进制偏移、JSON 类型和错误定义见候选合同。

## FreeSWITCH 迁移验证

优先检查调用入口、参数、响应字节、事件顺序、副作用、超时/取消和资源释放这七项。某一错误码或命令正向例通过，不能扩大到同模块全部语义。原版 `detect_speech`、`play_and_detect_speech`、MRCP、完整 originate 并未因内部 ASR 候选出现而实现。

完整 3,999 条原始对照、CSV 和验收定义见[专题索引](10-全部专题文档目录.md)，待完成工作见[项目未完成清单](13-项目未完成清单.md)。迁移验证应固定 FreeSWITCH 版本、目标线路及必要接口集合，逐项保存双方输入、输出和资源释放结果；延期能力保持未完成状态。

## 实现与调试入口

主源码位于 `source/main/`，候选源码位于 `source/asr-candidate/`。从对应根目录进入 `control/` 查看 Go 协议及控制逻辑，进入 `media/` 查看 Rust 媒体处理，进入 `native/include/` 查看 C ABI。

调试时应关联通道 UUID、媒体会话、worker 代次和音频轮次。信令成功、媒体收到、SDK 写入及业务实际消费是不同阶段；故障报告应明确发生阶段、实际报文、完成事件与最终释放状态。命令和日志采集方式见[运维说明](07-运维与压测说明.md)。
