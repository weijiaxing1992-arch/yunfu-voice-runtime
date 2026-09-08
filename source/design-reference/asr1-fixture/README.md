# ASR1 下一批开发夹具

状态：**设计与离线协议模型**。本目录不属于发布 1.13 生产源码，不应嵌入后台作为新增绿色证明。依据 [NEXT-ASR-ADAPTER.md](../voice-runtime-m2-rx/NEXT-ASR-ADAPTER.md)，准备真实 SIP → Rust RX → Go → 本机独立 ASR 模拟进程的下一增量。这里没有新建 Server、Unix 监听器、真实供应商驱动或实际语音识别模型。

## 可执行内容

| 文件 | 用途 |
|---|---|
| `asr1_fixture.py` | 纯字节 ASR1 编解码、模拟供应商状态机、结果消费预期模型、写前期限及连接额度模型 |
| `fault_scenarios.py` | 18 个故障计划；给出真实实现应插入的检查点、动作及期望；只打印计划，不执行暂停或网络 |
| `test_asr1_fixture.py` | 非静音 PCM、分片、格式反例、来源/期限/结果/回收状态的离线方法 |
| `run_selftest.py` | 执行本目录测试，禁止网络/子进程审计事件，保存新目录的日志、逐方法结果及源码前后指纹 |
| `acceptance-matrix.md` | 协议及下一批实际端到端验收清单，区分当前离线范围与尚未运行部分 |

运行方式（Python 标准库，建议 3.9 及以上）：

```sh
cd work/voice-runtime-m2-asr-design
python3 -B run_selftest.py selftest-新的编号
python3 -B fault_scenarios.py
```

收据目录必须不存在。首轮 `selftest-01` 的失败原件保留；其错误说明见该目录 `runner-error.md`。`selftest-02` 已完成 51 个方法、0 失败/错误/跳过，随后独立复核补入“DTMF 辅助序号不误判音频缺口”的完整模型往返；最终冻结收据见 `selftest-03/receipt.json`。自测不调用 socket，审计钩子也禁止网络和进程操作；时钟为明确传入的虚拟单调时间，不是本轮测得的跨进程时间。

模型的 `ProtocolError` 必须由未来真实模拟进程驱动器转换为连接失败并关闭；当前单元测试可以在同一对象上反复提交反例以检验各断言，但真实进程不得错误后继续上传。`Provider` 的摘要文字 `mock-sha256:…` 只证明收到哪些 PCM，完全不证明识别准确率。

## 固定的草案协议

ASR1 是供应商中立的**本机私有 Unix stream** 协议草案，不是 FreeSWITCH、WebSocket 或云厂商标准。未来 Go 主动连接固定路径，一流一连接；代理拥有路径，适配器不能删除它。共享时钟仅允许同机、同一时间命名空间的 Linux MONOTONIC=1 或 Darwin UPTIME_RAW=2；不支持把这些期限跨网络直接比较。

本目录给出可以直接用于下一批双方实现的精确消息规则，后续任何变更必须更新版本/草案与反例，不能静默修改已冻结 RXS2/RVA1。

### 帧及身份

前缀为 `<u32 length, u8 kind, u8 version=1, u16 reserved=0>`，全小端。length 数它后面的字节，范围 4..8192，因此完整帧最多 **8196 字节**；读取先验 length，再读相应正文。模型的固定接收上限 8196 包括前四字节，明确澄清父设计“8192 缓冲”与前缀的计数口径。每次真实 read 不能超出当前剩余容量。

kind 编号：HELLO=1、READY=2、AUDIO=3、MARKER=4、FINISH=5、RESULT=6、DONE=7、ERROR=8、CANCEL=9、PING=10、PONG=11。除 AUDIO 外均为严格 UTF-8 JSON 对象，禁止重复键、未知字段、非有限数及尾随内容。u64 用规范十进制字符串，stream_token 用 32 位小写非零十六进制，不接受浮点或前导零。所有字段必填；不适用字段用约定的零/null，不能省略字段后猜测。

HELLO / READY 字段在 `HELLO_FIELDS` 固定，READY 精确回显所有身份及接受能力：原生 source_rate=8000、target_rate=8000 或 16000、channels=1、s16le、20ms、plc_policy=metadata、gap_policy=explicit。未完成 READY 不得订阅 RX 和发送音频；这一实际启动顺序仍需下一批真实 Go 测试。

### 音频与非音频

AUDIO 的 112 字节头严格按父设计排序：token[16]，8 个 u64，4 个 u32，5 个 u16，6 字节保留零；正文仅 S16LE。8k 是 160 样本/320 字节，16k 是 320 样本/640 字节，完整帧分别 440 / 760 字节。rtp_clock_rate 始终 8000，16k 的 filter_delay_ns 为 3,937,500，8k 为 0。

首批 AUDIO 只允许 origin=1 Decoded，不上传 PLC。当前夹具限定本地 G.711 Decoded 必须具备四个原始位置有效位；其余未定义位拒绝。它不把“不同长度 PCM”当成格式自动协商，更不把 16k 格式夹具当作已经实现或验证 FIR。

MARKER 的精确字段集为 `MARKER_FIELDS`：

```text
stream_token rx_kind event_seq source_generation source_segment clock_domain
lower_ns expires_ns flags ssrc rtp_sequence rtp_timestamp media_ns
sample_rate rtp_clock_rate observation_age_ms arrival_age_ms
deadline_lateness_ms queue_age_ms known_duration_ticks gap sid_b64
cn_applied reason boundary discarded_packets
```

rx_kind 使用 RXS2 的 2..13 编号；sample_rate 与 rtp_clock_rate 仍是原生 8000。位置有效位、来源及原始年龄只做保留/校验，不从年龄字段计算声学端到端延迟；未声明有效的位置和年龄必须为零。现有 RXS2 Auxiliary 不包含完整 telephone-event 按键正文，本草案仅保留现有元数据，不伪造按键值，也不替代 SIP/DTMF 业务事件通道。

| RX kind | ASR1 规则 |
|---|---|
| 2 HistoryPlc | MARKER，known_duration_ticks=`"160"`；不给音频/样本计数加 160，不把补偿当说话或沉默 |
| 3 Missing / 5 LocalExpired | 保留真实原因、位置和确知时长；未知用 null，不造零 PCM |
| 4 ComfortNoise | 真实 SID 的严格 base64，1..480 字节，保留 cn_applied；不触发“用户沉默/端点” |
| 6 AuxiliaryExpired | known_duration_ticks 必须 null；不是 20ms 语音缺包 |
| 7 SourceBoundary | 新的正数 generation/segment 按序前进；新段不延续旧 utterance 或转换历史 |
| 8 InactiveSuspended | 使开放 partial 失效；恢复前必须有新 SourceBoundary；不补静音、未来 FIR 必须清历史 |
| 9 ObservationFailed / 12 ExportFailed | 识别流失败，不伪造正常 final；SIP 与下行生命周期独立 |
| 10 ExportGap | gap 对象严格为 first/last/decoded/plc/reasons，范围接续上一个 event；丢弃数不超过范围记录数；不是 RTP 丢包或时长 |
| 11 End | 仅停止输入的摘要；随后是否允许 FINISH 由适配器主动 Finish 意图和通话生命周期决定 |
| 13 Auxiliary | 元数据，不上传音频，不重复发业务 DTMF |

Gap/End/ExportFailed 的 lower/expiry=0、flags=0、known_duration=null，不声称音频位置。其余 MARKER 携带原始非零期限。除 CN 外，sid_b64=null 且 cn_applied=false。所有 MARKER 都不能增加 audio_frames 或 samples。

### 期限、结果与生命周期

原音频期限固定为 `expires=lower+100000000ns`，有效时 `0<lower<=now<expires`，不得重锚、冻结剩余年龄或在每次部分写重新给 100ms。写前预算为 min(20ms, 原剩余期限)，已写部分字节后失效是 submission_unknown，随后关闭且不重放。检查与 syscall 不是原子，不能承诺过期字节绝不进入内核；独立 provider 在完整读入后、消费前还要复核。离线 WriteFence 只验证这些检查点的数学规则。

单独检验 `expires=lower+100ms` 不能发现把两者同时平移。夹具的 `check_derivation` 将转换/待发帧与原 RX 帧逐字段绑定，只有 PCM、输出率/样本数/滤波延迟可按规则改变；8k 同率 PCM 也必须相等。同源 CN/PLC/gap 只能清滤波连续性，不能清已经接受的媒体时间下界或完整 RTP 包序/时间下界；只有真实新来源边界允许重建时间线。

未来 Go `SetWriteDeadline` 应先取 `time.Now()`，再调用 `RemainingBudget()`，用先取的时间加 min(20ms, remaining) 得到保守绝对期限。若先得到 budget、暂停后才重新取 now 并加上旧 budget，会错误增加宽限。此处仅补实施约束；实际 SDK/调度器和发送边界仍待下一批实测。

FINISH 和 DONE 字段固定为 token、last_event_seq、实际完整提交的 audio_frames、samples、markers。DONE 必须精确回显 FINISH。RESULT 字段在 `RESULT_FIELDS` 固定：token、递增 result_seq、utterance/revision、来源、partial/final、text、输入/媒体范围及 coverage。部分结果可修订；final 不可改写；单流只允许一个开放 utterance。零 Decoded 可以有 DONE，不能捏造空 final。

结果队列最多 8 项，可合并尚未交付的同 utterance partial；final 满队列明确失败。来源变化使开放 partial 失效，模型返回本地 `partial_invalidated` 事件；此前 final 保持原值，迟到旧段消息只计数不发布。真实流必须在最后交付检查通话 LifetimeDone；模型的 `lifetime_end()` 即刻清空未读结果并丢弃后续消息。

本轮独立审查补入有界来源预期模型：`note_audio` 只登记完整实际提交的 Decoded，连续范围合并，最多 64 个元数据区间，不保存 PCM。RESULT 的开始/结束必须落在真实 Decoded 的帧边界，媒体时间必须精确对应，跨真实缺口只能报 discontinuous。`note_marker` 单独登记辅助事件；telephone-event 占用观察序号并不等于音频缺口，因此前后连续音频仍可 complete。final 裁掉已完成前缀，新来源清理历史；超出区间预算明确失败，不能静默丢证据。这是草案预期模型的有限预算，生产实现仍需评审其容量与结果迟到策略，不能仅复制“事件号不超过上界”的检查。只有 marker 或新来源尚无音频时，不能接收虚构 final。

ERROR 的 code 必须为 1..128 字节的小写字母/数字/下划线标识且以字母开头；无效 code 也进入明确失败终态，不能因 null/空值恢复处理。

主动 Finish 可以先结束收音，通话仍活跃时继续等待超过音频期限的合法 final。文本不使用 PCM 的 100ms 判过期。DONE、输入停止、RX 实际停止、结果全部交付是四件事；EOF、一个 final 或 End 摘要都不能独立视为 completed。真实 BYE/worker 死亡/订阅替换与主动 Finish 必须使用不同的 SDK 生命周期信号。

## 下一批实现最小接线

采用父设计中的 Go 独立模拟服务 `control/cmd/asr-mock`，将本目录 18 个脚本的 checkpoint/action 映射为实际延迟、发送分片、受控关闭及错误字段。真实网络端口/路径必须由新运行目录分配，不能占主服务资源。模拟服务可先用一个 selector/事件循环或有界 Go 协程实现；本目录不写一个未经运行的 Python Unix 监听器冒充已完成接入。

生产文件范围仍是父设计列出的内部 asr、server/asr_stream、config 接线及 RX 生命周期/共享时钟小边界，不把网络处理塞进 Rust worker。默认停用，max_streams=64 且不超过 max_calls；起建和 cleanup_unknown 都计入额度；最多 3 Go 协程/流，不逐帧创建线程或进程。

真实模拟服务需要固定的 socket 父目录与同 UID peer 验证，不删除已存在路径；输出必须新建、固定预算、满额失败。它必须保存原样 PCM、原始协议和实际时钟，不能只保存 SHA 或构造一个 `passed=true`。真实发布校验必须重新计算样本、输入前缀、来源、期限、结果与退出证据；本目录的摘要及纯模型收据不能代替这些证据。

## 当前不能宣称

尚未实现/实测真实 Unix 连接、实际 Go ASR Manager、真实供应商、实际识别率、16k FIR、VAD/endpointing、TTS/LLM、自动打断、双轨录音、5000/10000 路容量或新增 FreeSWITCH 兼容。18 个故障计划中需要实际阻塞、SIP、调度器或多流的步骤均仍待执行；离线推进虚拟时间不是实测背压。
