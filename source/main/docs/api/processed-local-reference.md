# 本地单腿 G.711：电话 IVR 与智能体媒体入口

本页描述 1.12.0 正式候选的本地单腿合同。1.11 的 26 项本地图限定验证保留；1.12 另增加内部 PCM 轮次与已 ACK 外部流入口，实际结果和待部署状态见 [1.12 验证记录](release-validation-v1.12.md)。实现、测试通过和已发布分别确认；不能根据配置或能力名称推断完整 Voice Agent 已可用。

## 通话与音频路径

`media.processing="g711"` 时，显式本地号码或受支持 XML 拨号计划所选的本地应用可使用单腿图。呼叫仍经过 INVITE、媒体分配、200、真实 ACK 和应用执行；没有建立 B 腿，也不向上游发起第二通呼叫。ACK 前不启动 read 或播放应用；媒体分配后已可接收音频并记录按键，错误来源的 ACK 不推进状态。既有双腿呼叫仍按原桥接合同执行。

```text
来电 RTP → 来源检查 → 抖动缓冲 → G.711 解码 → 8 kHz PCM 消费者

提示音 / 受限 WAV / 已授权 PCM turn → 本地 PCM → G.711 编码 → 统一 TX RTP → 来电端
主动 telephone-event ────────────────────────────────┘
```

接收轨与发送轨相互独立。用户输入不会被回送成回声，收到的 CN 和按键也不会自动回送。接收帧保留音频位置与真实输入/PLC 来源，固定消费者检查每帧 PCM 的非零样本和能量；这一步尚不提供 ASR、VAD、录音或外部音频订阅接口。

当前为 PCMU/PCMA、8 kHz、单声道、20ms，每帧 160 样本，固定目标 40ms、上限 120ms 抖动缓冲。G.722/Opus 实时本地图、其他帧长、重采样和混音需独立开发验收。Codec SDK 可用不等于该实时图支持对应 Codec。

## 启动、分配与就绪

本地图同时要求 worker 声明 `processed_g711_v1` 和 `processed_g711_local_v1`。不能仅凭旧双腿能力接受本地处理请求。分配计划：

```json
{
  "version": 1,
  "mode": "g711",
  "jitter_target_ms": 40,
  "max_delay_ms": 120,
  "topology": "local"
}
```

`topology` 省略时沿用双腿 `bridge`；未知值拒绝。分配成功回执须同时有 `processing_version: 1`、`processing_topology: "local"`。双腿回执继续省略 topology，relay 不携带处理回执。请求与回执拓扑不同不能当作成功，不能在原会话内切换拓扑。local 会话拒绝后续 `connect` B 腿。

当前仍使用四端口分配块，回执有 A/B RTP/RTCP 四端口，本地图只连接 A 腿。不能按单腿数量自行把当前端口预算减半。释放仍执行端口复用隔离；测试应真实重新绑定端口并检查旧来源拒绝。

`GET /v1/codecs` 目录 1.2.0 在 `runtime_media` 中另列：

| 字段 | 语义 |
| --- | --- |
| `local_capability` | 精确名称 `processed_g711_local_v1` |
| `local_capable_workers` | 身份完整、健康且同时声明两项能力的 worker 数 |
| `local_available` | 全部预期 worker 满足本地能力要求 |
| `local_ready` | 当前已启用 g711 且 local_available |

原 `capability/available/ready/capable_workers` 保留双腿语义。旧目录缺少 local 字段表示尚未提供证据；不能显示本地就绪。主服务模式、测试实例模式和离线原生库自检也是不同范围。

## 播放、收号与 RTP 时间线

现有有限 `tone_stream` 和受限 WAV（PCM16、PCMA、PCMU）统一产生 PCM 后进入图编码。g711 模式仅 local A 腿允许播放；g711 bridge 禁止提示音/WAV 或带提示 read 的播放注入，relay 的既有 G.711 播放保持。新单腿独立波形验收实际覆盖 PCM16 WAV，其他文件编码不能据此自动视为新路径已逐项测试。受支持路径与大小限制继续遵循 [播放与 IVR](ivr-reference.md) 及既有应用合同；本功能不引入网络文件下载、任意音频格式或同步磁盘读取到媒体热路径。

播放与主动 RFC4733 按键使用同一会话 TX 的 SSRC、序号和媒体时钟。停止播放不会重新创建 RTP 会话。DTMF 的 payload、clock、可用事件与持续时间遵循实际 SDP 协商及有界发送队列；不能固定写死为 PT101，也不能把提交 UDP 解释为对端已确认收号。

`read`、播放终止符、超时、`uuid_break` 与 XML 顺序动作沿用现有受支持子集。`uuid_break` 仅停止当前 playback 或 read 提示音；read 继续收号至条件或期限，尚不支持 all/both/park 抢占。它不等于任意应用取消，也不自动终止新增的 PCM turn。PCM 使用独立的 turn_id、旧音频失效、0/20/40ms 中断与状态合同；须通过 [RVA1 INTERRUPT](rva1-reference.md) 明确控制，不能把 `uuid_break` 成功解释为 PCM 已停止。

A 腿 RTCP 使用真实 RX 与 TX 状态产生 RR/SR/SDES，释放产生 BYE。基础 RTCP 边界沿用 [实时媒体图](processed-media-reference.md)，没有新增 rtcp-mux、SRTCP 或完整扩展反馈。

## 运行总览与统计

`GET /v1/status` 中各 worker 的 `stats` 提供五项本地指标。声明新能力时五项必须齐全；缺字段、负值、非安全整数、帧样本关系错误都会使该采样无效。

| 字段 | 统计口径 |
| --- | --- |
| `processed_local_active_calls` | 本地图已分配会话，属于全部 processed_active_calls 子集 |
| `processed_local_consumed_frames` | 接收后实际被 PCM 消费者处理的帧，含有来源标记的有限 PLC |
| `processed_local_consumed_samples` | consumed_frames × 160 |
| `processed_local_nonzero_frames` | 至少一个样本非零的已消费帧，不是语音帧/VAD |
| `processed_local_energy_max` | 单帧 sum(sample²) 历史最大值，最高 171,798,691,840 |

累计值按 worker 代次重置，挂机只使活动会话归零。汇总帧数和样本数可以求和；energy_max 必须取各 worker 最大值，不能相加。非零或能量不证明 ASR 成功，PLC 也不能冒充收到真实用户语音。

页面仅对完整、健康、身份唯一且新鲜的主服务采样显示聚合值；缺任一 worker、数据过期或服务离线显示未知。压测在隔离实例运行，继续显示在总览的测试实例区域，不能把它的数值写入主服务统计。双腿 G.711 压测仍使用既有 22 项处理统计和原质量门槛，不用本地消费指标填平丢包、错误音频或发送超期。

## FreeSWITCH 对照与验收边界

| 使用需求 | 本版本对应内容 | 仍需独立验证或开发 |
| --- | --- | --- |
| 本地来电应答与挂机 | 受支持 SIP/SDP、ACK 后应用、BYE 回收 | 目标线路互通、完整原版会话语义 |
| IVR 提示音、WAV、收号 | 既有 ESL/应用子集进入真实本地图 | 原版全部文件协议、全部应用参数 |
| 主动/被动 DTMF | 已协商 telephone-event 与本地应用衔接 | 线路带内检测、其他 DTMF 机制 |
| `uuid_break` | 停止当前播放或 read 提示音，read 继续等待收号；PCM 由独立 INTERRUPT 控制 | 原版全部抢占语义、VAD 自动触发 |
| 流式 PCM 轮次 | 自有内部队列与已 ACK 的 RVA1 下行，旧轮拒绝、0/20/40ms 淡出 | 真实 TTS 供应商、FreeSWITCH speak/模块线格式兼容 |
| AudioFrame | 原生 8k PCM 和时间/来源状态 | 流式 ASR/TTS、VAD、双轨异步录音 |
| 媒体 IPC | RustSwitch 内部分配与能力握手 | 不存在可直接替换的 FreeSWITCH JSON IPC/原生 ABI |

独立 `tests/media_processed_local_e2e.py` 为两种 UDP 模式定义 13 类场景：真实接通、双律 PCM 与无回声、独立波形核对、播放/按键共享时钟、收号/超时/终止符、来源校验、实际 RTCP、端口复用、XML/ACK 生命周期及不支持格式拒绝。应保存真实 SIP/ESL/RTP/RTCP、构建和执行前后源码/二进制指纹以及释放后的新采样。该集合的数量不能加入旧双腿固定 22 项，也不能直接等同绿色接口数量。

## 1.12 PCM 下行与 TX 所有权

[内部 PCM](pcm-turn-reference.md) 要求额外 `pcm_turn_v1` 能力；外部 [RVA1](rva1-reference.md) 由主循环检查真实 ACK、A 腿 UUID、local 分配与 Worker 代次后才授予句柄。接收轨不因下行供音而停止，也不反射为回声。每批最多 5 个原生 8k/20ms 帧，队列最多 50 帧；24/48k 音频不能仅改格式标签后提交。

同一时刻仅有一个音频 TX 所有者。PCM begin 与活动、正在加载或已排队的 tone/WAV/带提示 read 互斥；启动旧播放也要检查 PCM。静默 read、park 与主动 telephone-event 可与 PCM 并存。连续 PCM 用固定 160 ticks 推进；主动 DTMF 共享包序和 SSRC，但保持事件自己的固定起点。成功入队、实际 UDP 发送和最后 20ms 尾帧完成分别统计，不将供音超时补成静音。

本地 `processed_local_*` 五项继续只描述 RX 消费，不把下行 PCM 计入 consumed/nonzero/energy。TX 进入原 `processed_encoded_*` 与发送统计，逐轮进度通过 PCM status/RVA1 查询。发送期限违例及发送前直方图的区别见 [内部 PCM 时间语义](pcm-turn-reference.md)；这些有限功能通过不改变双腿 500 路失败。
