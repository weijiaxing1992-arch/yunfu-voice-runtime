# 原生音频依赖与离线 PCM SDK

当前真实编解码范围为 PCMU、PCMA、G.722（64 kbit/s）和 Opus。G.729、G.726/AAL2-G.726、L16 的当前支持范围为同编码 RTP 直通；它们的 `encode/decode/available` 返回 false，不能解释为已经提供转码。现有 `rs_codec_get_v1` 自定义 ABI 及 G.711 示例不变。新增音频 SDK 直接调用官方库 ABI，没有冒充 FreeSWITCH 原生模块 ABI。

## 构建与来源

从项目目录运行，`--prefix` 必须指向操作者选择的私有输出目录。脚本不安装系统软件、不修改系统动态库搜索路径。需要 Python 3、C 编译器及 make；有本地官方归档时增加 `--opus-archive /absolute/opus-1.5.2.tar.gz`，仍验证同一校验和。

```sh
python3 native/audio/build_codecs.py --prefix /absolute/private/audio-deps
```

脚本最后输出两份库的绝对路径。Linux 扩展名为 `.so`，macOS 为 `.dylib`。macOS 已安装 Command Line Tools 且系统选择器指向其他 Xcode 时，可以仅对本次命令设置 `DEVELOPER_DIR=/Library/Developer/CommandLineTools`；脚本不会接受或修改系统许可协议。受限环境的 `sysctl` 不可用时，脚本给 libtool 提供保守命令长度，避免空探测值造成链接对象丢失。

| 组件 | 固定来源 | 许可与交付方式 |
|---|---|---|
| Opus | 官方 1.5.2 归档，SHA256 `65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1` | 保留上游 [COPYING](OPUS-COPYING)；BSD 3 条款及上游列出的专利许可。完整归档在构建目录，未改源。 |
| G.722 | [SpanDSP 固定提交](https://github.com/freeswitch/spandsp/tree/8f1e1646bdec99eac5fd2cd92c35563f736b9b89)，源码子集在 `native/vendor/spandsp` | 上游 G.722 源码与必要头文件原样保留，文件头为 LGPL 2.1；保留完整 [COPYING](../vendor/spandsp/COPYING)。独立动态库可由操作者重新构建、替换。 |
| G.722 构建适配 | `g722_support.c` | 自研堆分配/循环点积适配，不改官方 G.722 算法，不包含其他 SpanDSP 模块。 |
| G.711 | `media/src/audio/g711.rs` | 自研标准压扩与重采样实现，65536 个 i16 输入逐个检查量化误差，覆盖静音码字。 |

每个保留上游源文件的 SHA256 在 [sources.json](sources.json)。固定版本用于可复现构建，不声明为上游最新版本。Opus 官方许可说明见 [Opus license](https://opus-codec.org/license/)；G.722 源码中的版权与许可说明保持原文。分发依赖时须同时保留其来源、许可证和可重建源码。

## CLI 和输出语义

```sh
export RUSTSWITCH_G722_LIBRARY=/absolute/private/audio-deps/librustswitch_g722.dylib
export RUSTSWITCH_OPUS_LIBRARY=/absolute/private/audio-deps/opus/lib/libopus.dylib
rustswitch-media --audio-capabilities
rustswitch-media --check-audio
```

两个环境变量仅接受绝对路径，指向操作者信任的原生库。原生代码在当前探测/SDK 宿主内执行，不能视为安全沙箱。未指定库时 `--audio-capabilities` 仍退出成功并逐条报告不可用原因；`--check-audio` 要求全部 P0 真编解码通过，缺任何库输出 `passed:false` JSON 并非零退出。二者与 `--worker-config`、旧 `--check-codec` 互斥。

能力 JSON 保留 `codecs[].name/available/encode/decode/plc/reason`、规范 `pcm` 和 `live_transcoding:false`。`plc_mode` 区分 `codec_native`、`bounded_history_concealment`、`unavailable`。`relay:true` 表示 Rust RTP 路径支持该编码的协商类型，不表示已在所有设备、参数、网络或万路负载上认证。库缺失不影响纯 RTP 直通。生产管理服务可缓存此只读结果，避免每次页面轮询生成子进程。

自检对每种 P0 编码处理 40 帧（10/20 ms），检查非空编码、解码长度、PCM 小端往返、信号能量和有界延迟对齐后的相关性。还检查 PLC 标签、CN 独立语义、CN 后缺包不重放旧语音、空包不被冒充丢包。G.722 的 160 个 `fa` 字节和 Opus `f8 ff fe` 外部发生器码字均要求能解码为 320 个 16k 样本（20 ms）。这些是功能检查，不是语音主观质量评分、规范向量全覆盖或万路转码吞吐证明。

## PCM 与媒体时钟边界

Rust `AudioFrame` 只接受 160/320 个有符号 i16 样本，固定为 16 kHz、单声道、10/20 ms；`from_s16le/to_s16le` 显式进行小端转换。RTP `L16` 的大端格式不与这个内部格式混用。帧字段私有，构造后不能被调用方修改长度。

G.711 的 PCM 为 8k：进入/离开规范帧使用保留状态的 31 抽头 FIR，先低通再降采样，升采样先插零再低通。G.722 原生 PCM 为 16k，RTP 时钟仍为 8k，20ms 对应 320 样本、160 时间戳刻度。[RFC3551](https://www.rfc-editor.org/rfc/rfc3551.html) 保留了这个历史时钟规则。Opus 在线 SDP 声明 `opus/48000/2`，SDK 使用官方 API 以 16k 单声道编码/解码；声道声明不会被内部单声道 PCM 覆盖。[RFC7587](https://www.rfc-editor.org/rfc/rfc7587.html) 规定 Opus 的 RTP 48k 时钟及 SDP 声道声明。

`CodecSpec` 与内部帧长分别校验。直通支持更长的协商包时长；离线 SDK 当前每次只接收 10/20ms。SDK不提供任意立体声PCM、任意采样率重采样、RTP封包、播放调度或在线转码会话。

## 缺包、DTX、CN 与电话事件

`DecodeInput::Packet` 是真实码流，即使解码结果为零也标记 `Decoded`。`PacketLoss` 由调用方的媒体时间线判定；空字节包直接报错，不能代替丢包标记。Opus 调用官方解码器的空指针 PLC；G.711/G.722 使用上一帧乘以 0.75 的历史近似，最多连续 120ms，之后返回 `MissingAudio`。没有历史或帧时长变化时同样返回缺失。这个近似不是 G.711 附录 PLC，也不会修复 G.722 编码预测器状态；输出标签始终可区分估计与真实输入。

RFC3389 CN 载荷先通过已协商 PT 分流。SDK 保留电平与谱系数，返回 `ComfortNoise`，当前不合成噪声、不伪造静音 PCM；CN 后的缺包返回 `MissingAudio`，直到下一份真实音频包。SDK 单份 SID 上限为 8192 字节，是内存边界而非 RFC 阶数限制。[RFC3389](https://www.rfc-editor.org/rfc/rfc3389.html) 未固定谱模型阶数，且把噪声合成算法留给实现。Opus 自身 DTX 仍作为 Opus 码流处理，不能与独立 CN 类型混淆。

RTP 热路径检查 DTMF 事件四字节边界、保留位及协商的 0..16 子集，保留事件时长/结束位原值，不把事件送给音频解码器。辅助 CN 时钟须与主 RTP 时钟相同；静态 PT13 只能为 8k。DTMF 的时钟独立声明，不按主编码名猜测。

## 运行边界与复杂度

默认 worker 从未加载这些原生库；只有离线 SDK/检查命令使用它们。单编码/解码实例拥有独立状态，不为每个包生成线程。G.711 重采样开销为每输入样本固定 31 次滤波运算；每个实例只保留两份 FIR 状态和一帧历史。Opus/G.722 开销取决于官方算法，尚未完成 Linux 万路 PCM 转码验收。

RTP 每 worker 复用一个 8 包批次，单包缓冲 8192 字节。包长度达到或超过 8192 会计入拒绝，兼容缺少原始截断长度的回退系统；这个是实现包长边界，不保证所有合法 Opus 组合均小于它。普通协商下的 L16 48k 双声道20ms载荷为3840字节。协议扩展没有增加每会话收包缓冲，也没有在热路径解析 fmtp；同编码媒体字节、SSRC、序列号和时间戳原样转发。

此次能力不包括 jitter buffer、重排、在线丢包检测、实时 PCM 管道、转码调度、VAD/ASR/TTS 或 AI 会话。UI/文档必须持续区分已实现的 RTP 直通、已实现的离线 PCM SDK，以及尚未接入实时会话的转码。
