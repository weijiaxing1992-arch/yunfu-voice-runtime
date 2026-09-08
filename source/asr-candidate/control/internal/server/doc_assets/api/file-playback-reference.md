# 受限 WAV 文件播放

本文按 1.12.0 候选更新；真实本地 WAV 文件播放始于 1.7：在受限目录中加载完整文件、校验容器与音频格式，再生成实际 G.711 RTP。`loading` 或文件打开成功均不表示音频已经发送。停止、挂断、代际更换后，迟到的加载结果不能重新开始播放。

这是自有媒体 IPC 的一个受限实现。它不等于 FreeSWITCH 全文件接口兼容，不支持任意路径、URL、压缩音频插件、TTS、重采样、立体声、文件偏移 seek 或无限长流。原版接口对照仍锁定 FreeSWITCH 1.11.3 提交 `ef32e205295e29f034f1453ad245ba5efb07b94a`；完整原版对测证据应以单独保存的运行报告为准。

## 启动配置与路径边界

在现有启动配置的 `media` 对象中加入：

```json
{"playback_root":"/srv/rustswitch/prompts"}
```

| 配置/边界 | 实际行为 |
|---|---|
| `media.playback_root` | 空值或省略关闭文件播放；不影响原有单音播放或桥接。非空必须是规范绝对路径，不能为 `/`，最长 4096 字节。 |
| 目录检查 | Go 验证配置形状；Rust 启动时打开真实目录。不存在、不可打开或根目录自身是符号链接会启动失败。 |
| 生效方式 | worker 启动时冻结，修改需重启生效；没有逐呼叫切换根目录的入口。 |
| 相对文件路径 | 最长 240 个 UTF-8 字节、最多 8 个组件，每个组件最多 128 字节；后缀 `.wav` 大小写均可。 |
| 禁止项 | 绝对路径、空组件、`.`、`..`、反斜线、冒号、控制字符和非 WAV 后缀。没有 URL、shell 或动态插件输入。 |
| 文件对象 | 每次逐级 `openat` 并使用 `O_NOFOLLOW`；拒绝中间及最终符号链接、硬链接、FIFO、设备、目录及不可读普通文件。 |

根目录在启动时固定为已打开的目录 fd，后续组件相对已打开的父目录寻找；路径检查和文件实际打开之间，不会因为符号链接被替换而跟随到根之外。加载过程不把文件内容当作命令。实际 Unix 读取权限仍由操作系统执行，本目录约束不是容器或完整文件系统沙箱。

媒体层允许普通文件名空格；上层 ESL/XML/read 解析可能采用更窄的参数规则，不能由媒体层支持推断所有上层入口都接受含空格文件名。

## 格式范围

| WAV 编码 | 格式标签 | 采样/声道 | 文件样本 | `fmt ` 要求 |
|---|---:|---|---|---|
| PCM | 1 | 8000 Hz、单声道 | 有符号 16 位、小端 | 长度 16；或长度 18 且 `cbSize=0`。 |
| A-law | 6 | 8000 Hz、单声道 | 8 位 G.711 A-law | 长度 18，`cbSize=0`。 |
| μ-law | 7 | 8000 Hz、单声道 | 8 位 G.711 μ-law | 长度 18，`cbSize=0`。 |

`byte_rate`、`block_align` 与位深须完全匹配所声明编码。线路输出只接受已协商 PCMU/PCMA、8 kHz、单声道，包括合法动态 RTP PT；输入先恢复 PCM，再按实际线路压扩，G.711 文件不保证保留原压扩字节的全部等价零码字。没有重采样，也不把内部 PCM 冒充网络 L16。[Microsoft WAVEFORMATEX](https://learn.microsoft.com/en-us/windows/win32/api/mmreg/ns-mmreg-waveformatex)

容器限定 little-endian `RIFF` / `WAVE`，外层长度必须与实际文件长度恰好相等。`fmt ` 和 `data` 必须各有且只有一项，`fmt ` 在 `data` 前；`fact` 可省略，出现时须唯一、恰好四字节且样本数与 `data` 一致。未知块只检查长度和对齐，不执行内容。奇数长度块要求存在零值 padding；最多 128 个块。本实现的严格子集会拒绝其他合法但尚未支持的 WAVE 变体，例如扩展 `fmt`、非零 padding、RIFX、RF64、IEEE float 和 WAVEFORMATEXTENSIBLE。[Microsoft RIFF 结构说明](https://learn.microsoft.com/en-us/windows/win32/xaudio2/resource-interchange-file-format--riff-)

文件总大小最多 1 MiB，包含头部和元数据；音频必须非空且最多 240000 个样本，即 30 秒。文件读取前后校验 inode、大小和修改元数据，读取中发生变化时不交付部分结果。

## 媒体 IPC

启动文件：

```json
{"id":30,"op":"playback_file_start","session":42,"playback_id":2,"leg":"a","path":"menu/welcome.wav"}
```

| 字段 | 含义 |
|---|---|
| `id` | 该次 RPC 的关联编号，与播放编号不同。 |
| `session` | 当前 worker 内有效媒体会话编号。 |
| `playback_id` | 非零 uint64；同会话新任务必须大于之前任务编号。 |
| `leg` | relay 可为 `a` 或 `b`，向 B 播放须已有真实 B 对端；g711 仅 local A 腿可播放，bridge 明确拒绝。 |
| `path` | 上述根目录内相对 WAV 路径。Go 对应 `media.Request.FilePath`。 |

正常入队立即回复：

```json
{"id":30,"ok":true,"type":"playback_state","session":42,"playback_id":2,"state":"loading","sent_packets":0,"total_packets":0,"message":""}
```

`ok:true` 在这里仅表示任务已进入有界加载流程。Go `Pool.Call` / `Pool.Submit` 仍复用既有串行 RPC，媒体循环不会等待磁盘。相同播放编号、相同路径和方向重复请求返回原状态，不重播；同编号参数不同、已有任务处于 loading/running 或使用旧编号均拒绝。

状态与停止沿用：

```json
{"id":31,"op":"playback_status","session":42,"playback_id":2}
{"id":32,"op":"playback_stop","session":42,"playback_id":2}
```

| `state` | `sent_packets` / `total_packets` | 含义 |
|---|---|---|
| `loading` | 0 / 0 | 尚未取得完整验证后的 PCM；不能据零分母报完成。 |
| `running` | 实发 / 名义包数 | 完整文件已装载，正在生成 RTP 或等待最后一帧的播放时间结束。 |
| `completed` | 必须相等且非零 | 全部标称包被本机 UDP 接受，最后一帧时间已结束；仍需接收端证据才能证明实际音频到达。 |
| `stopped` | 保留停止时计数 | 主动停止；加载中停止可为 0 / 0，不等于完成。 |
| `failed` | 保留失败时计数 | 加载失败/超时、调度迟到或 UDP 发送失败；`message` 给出原因。 |

输出固定 20 ms 一包、160 个 G.711 样本；RTP 序号逐包递增，时间戳每包增加 160。`total_packets = ceil(样本数/160)`，最大 1500 包。尾部不足 160 个样本补该线路的 G.711 数字静音，不丢掉剩余样本；播放时长向上补齐到 20 ms 边界。分母不因失败或少发缩小。

文件只在完整装载后才替代目标腿原主音频及 CN；loading 期间桥照常转发。反方向音频、双向 telephone-event 与 RTCP 保持原通路。完成或停止后恢复桥；已排队的 DTMF 不会被文件启动清除。媒体层负责真实按键采集，上层应用决定终止符，并发送匹配编号的 stop 实现打断。

## 有界加载、缓存与取消

| 资源 | 硬边界 |
|---|---|
| 文件线程 | 每个开启文件播放的 worker 固定 1 个；空 root 不创建线程。 |
| 待加载 / 待交付 | 两个独立队列各最多 16 项，队列满即时拒绝新启动；不生成逐通话线程。 |
| 同时交互任务 | 单音与文件的 loading/running 合计每 worker 最多 64 项，每会话最多 1 项。 |
| 加载交付期限 | 从受理算起 2 秒，排队时间包含在内；超时由媒体循环独立标记失败并取消交付。 |
| 单文件 / PCM | 输入最多 1 MiB；PCM 最多 240000 个 i16 样本，即 480000 字节。 |
| 缓存 | 每 worker 最多 8 项且 PCM 总量最多 4 MiB，LRU 淘汰。 |
| 终态保留 | 只保留最后任务的小型状态；完成、失败、停止立即归还大 PCM 引用，释放会话删除全部任务状态。 |

缓存只共享不可变 PCM。每次仍安全打开路径、检查权限与对象类型，再以设备/inode/大小/修改和变更时间匹配缓存；文件替换或元数据变化会重新读取校验。缓存淘汰不打断已有播放，活跃任务引用受 64 项上限约束。有限缓存并不代表任意多不同文件都不占内存，主动文件播放容量应另行实测。

结果交付同时检查槽位、会话 token 代际、播放编号和仍为 loading 的状态。stop/释放会设置取消位并撤销计时项；旧任务、复用槽位、超时后的结果直接丢弃。取消不依赖文件线程先完成读取，不能迟到恢复旧播放。

普通文件的底层文件系统读取可能不能由用户态即时中断；两秒期限取消的是播放任务交付，媒体线程继续转发，且不会额外生成替代加载线程。一个长期卡在文件系统中的加载线程会使后续文件任务超时或队列满，不能把这种情况报为成功。worker 退出不无限 join 该线程，进程退出统一回收它持有的 fd。部署应使用本地可用的提示文件目录，不能把此实现当作网络流播放器。

## 失败与原版应用映射

| 场景 | 实际 IPC 结果 |
|---|---|
| 空 root、无效相对路径、非 G.711 线路、无 B 对端、任务冲突、队列/并发满 | 启动直接 `ok:false,type:error`，没有开始播放。 |
| 根内相对文件不存在 | 先 loading，后 failed；`message` 包含 `file_load_failed: file_not_found`。 |
| 坏 RIFF、格式不支持、过大/过长、链接/设备/FIFO、读权限不符、读取中变更 | loading 后 failed，零已发包；不能截断播放有效前半段。 |
| 加载期限到期 | failed，`file_load_timeout`，后到结果不能播放。 |
| 已运行时调度超过一帧或 UDP 发送失败 | failed，原分母和实际发送计数保留；不补发突发、延长窗口或假报 completed。 |
| 加载或运行中 stop、挂断 | 停止或释放；无论加载结果何时返回，都不得复活。 |

原版 `mod_dptools` 的 `playback` 将正常/BREAK、找不到文件及其他失败分别映射为 `FILE PLAYED`、`FILE NOT FOUND`、`PLAYBACK ERROR`；应用层应根据实际媒体终态区分这些情况，不能在 loading 阶段提前发成功。[FreeSWITCH 1.11.3 mod_dptools](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_dptools/mod_dptools.c#L3029)

原版文件接口可接受的路径、格式、变量、偏移及播放事件比本实现更多。本页仅说明新增媒体能力；完整 `playback` / `read` / XML 动作语义和原版对测结果应结合上层接口文档及逐项兼容账本阅读。

## 可复核验证

纯状态及文件测试覆盖 16 项 WAV 解析边界、5 项安全加载与预算场景、文件尾帧静音及终态释放 PCM、加载停止/释放/旧代际迟到结果、加载截止与成功状态区分。后两类 Engine 用例需要可绑定 UDP 的环境；权限被拒绝必须保留失败证据，不能列作通过。

`tests/file_playback_e2e.py` 包含 9 项独立真实 worker 用例：PCM16→PCMU/PCMA 的实际样本与尾帧、A-law/μ-law WAV、不存在与坏文件/链接/FIFO、非法路径/空 root、加载中 stop 后新单音、加载中释放无迟到音频和资源、真实 DTMF 打断后桥恢复。应顺序运行普通和 connected UDP 两种模式；测试从实际端点读取 RTP 并独立解码，不能用文件已打开或状态 completed 代替音频证据。

这些测试不认证声卡听感、全部编解码、30 秒持续文件播放的并发容量或 FreeSWITCH 全量兼容。原版同文件对照需要另外固定 WAV SHA、双方运行版本、实际 RTP 与结束/打断事件；不能把本地功能回归冒充原版对测。

## 与流式 PCM 的发送所有权

在 [本地 G.711 图](processed-local-reference.md) 中，受限 WAV 解码出的原 PCM 直接进入图编码，与 tone、[PCM turn](pcm-turn-reference.md) 和主动 DTMF 沿用同一 TX SSRC/包序/时钟。文件路径、格式、异步磁盘预算及播放 ID 幂等规则不因新流接口扩大。

1.12 外部 SDK 在 PCM begin 时检查活动及排队的 WAV/tone TX 任务；反向启动播放也检查当前 PCM 所有权。loading、running、draining/stopping 或结果未知均不能靠本地取消标志冒充 TX 已释放。需要用原播放停止操作或 PCM INTERRUPT 取得真实结果后再交接，不自动混音或抢占。静默 read/park 可并存；read 提示音和普通 WAV 一样持有音频 TX。

`uuid_break` 与 playback_stop 只作用于原播放任务；流式 PCM 不伪造 PLAYBACK_START/STOP 或 FILE PLAYED，应通过 [RVA1](rva1-reference.md) 的逐轮状态确认。未知受理、音频格式和供应商接入的边界保持独立。
