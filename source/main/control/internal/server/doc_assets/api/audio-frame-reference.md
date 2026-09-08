# 多采样率 AudioFrame、借用帧与离线分支

文档版本 1.12.0。统一的是样本类型、帧时长、时间位置、来源与生命周期，各节点可以保留需要的 PCM 采样率。多采样率拥有型帧与 FIR 转换属于离线 SDK；实时 G.711 worker 已使用原生 8k 借用帧进行双腿处理及本地 RX/TX。已 ACK 外部 PCM turn 是有限下行接口，ASR/TTS 供应商、VAD 和录音仍未接入。

## 入口与兼容行为

| 入口 | 行为 |
|---|---|
| `AudioFrame::new(samples)`／`from_s16le(bytes)` | 保留旧的 16 kHz 单声道、10／20 ms 语义 |
| `AudioFrame::with_sample_rate(samples, rate)`／`from_s16le_at(bytes, rate)` | 显式支持 8000、16000、24000、48000 Hz，不从长度猜采样率 |
| `AudioCodec::new(name)` | 保留旧 16 kHz PCM 入口，包括旧 G.711 2 倍采样率转换 |
| `AudioCodec::native_rate("PCMU"/"PCMA")` | 8 kHz PCM 直接压扩，不自动升到 16 kHz |
| `AudioCodec::native_rate("G722")` | 16 kHz PCM；需显式配置可信 G.722 动态库 |
| `AudioCodec::native_rate("OPUS")` | 48 kHz 单声道 PCM；需显式配置可信 Opus 动态库 |
| `AudioBranch::new(source_rate, target_rate)` | 只有两端采样率不同时才创建参考 FIR 转换器 |

`AudioCodec::encode` 要求帧采样率与实例相符；不符会报错，由调用方明确创建目标分支。24 kHz TTS 样本可转换到 8／16／48 kHz；这只是样本接口，不会调用真实 TTS 服务。其他采样率、立体声和 10／20 ms 以外的 SDK 帧明确拒绝；线上 Opus 其他包时长的透传合同保持独立。

本次不改 `rs_codec_v1` C 描述符、回调布局或 `rs_codec_get_v1` 符号。现有 C 插件检查器与这个 Rust SDK 是两个入口，不能把示例 C ABI 当作 FreeSWITCH 原生模块 ABI。

## 格式、时间与所有权

每帧提供 `sample_rate()`、`channels()`、`valid_samples()`、`duration_ms()`、`position()` 和只读 `samples()`。PCM 为单声道 signed 16-bit；字节导出明确使用 little endian，不能直接当作 RTP L16 的网络大端数据。

| PCM 采样率 | 10 ms 有效样本数 | 20 ms 有效样本数 |
|---|---:|---:|
| 8000 | 80 | 160 |
| 16000 | 160 | 320 |
| 24000 | 240 | 480 |
| 48000 | 480 | 960 |

帧长必须精确匹配，既不补零也不截断。字节入口在分配前检查长度，帧构造会移除输入 Vec 的多余容量。保留 `Vec<i16>` 本地所有权，不引入 Arc／Rc。只读访问借用样本；同率分支移动原始缓冲，不复制；显式 `clone` 会复制样本。异率参考转换会分配目标帧，编解码也仍有分配，因此不能称为整个媒体热路径无分配。

旧帧未传位置时 `position()` 返回 `None`。新分支以 `AudioBlock::new(output, position)` 定位，包括没有 PCM 的缺失／CN 块；已定位帧不能被悄悄改写到另一时间位置。`AudioPosition` 使用同一流起点以来的 `media_time_ns`，由调用方媒体时序提供，不取 UDP 到达时间或 UTC 墙钟。相邻帧通过检查溢出的 `advance(10/20)` 推进。

可选 `RtpPosition` 独立保存源 RTP 的 32 位时间戳与 `clock_rate`，按正常 RTP 规则回绕。G.722 在 20 ms 内产生 320 个 16 kHz PCM 样本，却只推进 160 个 8 kHz RTP 刻度；Opus 的线上时钟固定为 48 kHz。采样率转换保留来源时间，重新发包应由 packetizer 建立输出流自己的时间戳。[RFC 3551](https://www.rfc-editor.org/rfc/rfc3551)、[RFC 7587](https://www.rfc-editor.org/rfc/rfc7587)。

## 按需分支与缺包语义

```rust
use rustswitch_media::audio::{AudioCodec, AudioPosition, DecodeInput};
use rustswitch_media::audio::branch::{AudioBlock, AudioBranch};

// 保留电话侧原生8k，只有目标分支显式请求16k时才转换。
let mut decoder = AudioCodec::native_rate("PCMU")?;
let output = decoder.decode(DecodeInput::Packet(&[0xff; 160]), 20)?;
let block = AudioBlock::new(output, AudioPosition::new(0, None))?;
let mut branch = AudioBranch::new(8_000, 16_000)?;
let converted = branch.process(block)?;
// converted仍保留原始来源分类；这里没有调用ASR、VAD或TTS服务。
branch.close();
Ok::<(), anyhow::Error>(())
```

`AudioOutput::Frame` 保留 `Decoded`、`SuppliedPcm`、`CodecPlc` 或 `HistoryConcealment`。`SuppliedPcm` 表示调用方直接给定的样本；离线 SDK 和本地 PCM 发送路径都不得将其冒充网络解码或真实 TTS 服务调用。转换不会把 PLC 变成正常收包。G.711／G.722 历史补偿只有一帧，连续缺失按实际时长累计最多 120 ms；切换帧长不能延长历史重放。Opus 调用真实库的原生 PLC。没有历史可估计时使用 `MissingAudio`，不会生成静音 PCM。

`ComfortNoise` 保留 SID 电平和谱参数，当前没有噪声合成。CN 谱参数仍是源模型，不能把透传元数据理解为已经合成目标采样率噪声。缺包、CN 或前向时间跳跃会清掉重采样历史，防止旧话音进入后续真实音频。缺包不是用户沉默，消费者仍必须处理这一差异。

分支拒绝倒退／重叠的时间块和错误源采样率。`close()` 清除历史，之后拒绝处理；不会补尾部静音或把旧分支复用到另一通话。没有内部帧队列、线程或后台任务，调用方仍须对分支数、上游排队、取消和整体内存设置预算。

## 参考滤波器与验收边界

异率转换使用连续 127 抽头 Hamming 窗低通参考 FIR，支持本页 4 种率的全部 12 种异率组合；先插值、低通，再抽取。中间率最高 48 kHz，比率因子最大为 6；只在实际输出点计算卷积。每实例持有固定历史和系数，每次最多输出 960 样本，不累计旧帧。

滤波有启动瞬态和算法延迟。`filter_delay_ns()` 报告 63 个中间采样间隔，向上取整到纳秒；来源时间位置不自动减去延迟。流结束和不连续时不合成尾帧，旧滤波尾部会丢弃。生产 AudioGraph 需自行对齐不同分支的延迟。

测试包括：全部率对 10／20 ms 帧切分一致性、脉冲峰值延迟、恒定信号增益、48→16 kHz 的 1 kHz 保留与 12 kHz 衰减、真实 G.711 字节逐样本还原、可信 G.722／Opus 库编解码、24→48 kHz 样本接入 Opus、错误 Opus 时长拒绝后解码状态保持、RTP 回绕、缺包／CN／关闭及旧入口。

这些是有限的确定性音频测试，不是听感评分、ASR 识别率、所有频率抗混叠认证或 5000／10000 路实时转码性能验收。原生库测试必须实际配置并执行；未配置库时不能把离线单元测试汇总当作原生库验收。完整最新结果以本轮实测记录为准。

## 实时借用帧与轮次

`AudioFrameView<'a>` 将借用的 `&[i16]`、sample_rate、duration_ms、AudioPosition 与 FrameOrigin 一起交给节点；声道固定为 1，有效样本数由切片长度给出。构造时校验 8/16/24/48k 与精确 10/20ms 长度，PCM 采样率和可选 RTP clock 分别保存，不要求二者相等。Missing/CN 没有 PCM，不构造成伪静音借用帧。`AudioFrame::as_view(origin)` 要求帧已有位置。

`G711Codec::encode_frame` 校验 8k/mono 后写入调用方输出缓冲；底层 encode_into/decode_into 仍支持 10–60ms 完整包，实时图和 PCM turn 当前固定 20ms。借用视图不创建 Vec、Rc 或 Arc，同一 Worker 同步消费调用方存储，不能跨越该存储的生命周期。离线 AudioFrame Clone、转换输出和 Go 数据批次仍可能分配，不能由借用接口推断整个媒体服务零分配。

[内部 PCM turn](pcm-turn-reference.md) 另以不可复用的轮次和偏移管理外部供音，将实际媒体位置传入本地编码节点；它不把 turn_id 塞进 RTP clock，也不将采样率统一提高到 16k。连续音频按 160 ticks 推进，旧队列中断与淡出只影响未发送帧。[RVA1](rva1-reference.md) 的网络入口接受 8k，离线 SDK 的 24k 支持不扩大该线协议格式。
