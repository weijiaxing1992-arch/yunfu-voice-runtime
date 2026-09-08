# v1.12：PCM 轮次与已 ACK 外部流式入口验证

文档版本 **1.12.0**。本页是正式候选的功能验收记录；文档整合、功能测试通过和主服务实际部署分别确认。当前运行实例是否采用本版本，必须核对 `GET /v1/docs` 的 `version`、文件清单与部署回执，不能从本页标题或旧绿色推断发布成功。

本轮新增 8kHz 单声道 PCM 下行、严格轮次与样本偏移、0/20/40ms 中断，以及由真实 SIP ACK 授权的本机私有 RVA1 入口。完整合同见 [内部 PCM](pcm-turn-reference.md)、[RVA1 外部协议](rva1-reference.md) 和 [本地 G.711](processed-local-reference.md)。这提供流式媒体下行基础，尚未接通 ASR/TTS 供应商、VAD、LLM 或双轨录音，也不授予 FreeSWITCH 全量兼容或容量通过。

## 实际结果与统计口径

| 验证范围 | 实际记录 | 结论边界 |
| --- | --- | --- |
| 内部 PCM 真实媒体 | 两种 UDP 模式共 **42 方法、16 子例通过**；52 次 Release 后新鲜统计归零且端口可重新绑定 | 原始 Rust FD3/JSON、真实 G.711 RTP/RTCP；Go Pool 真链路另由 Go 回归验证，原始媒体调用不负责 SIP ACK 授权 |
| 数据 lane 故障 | UDP、connected 各 3 方法通过；每模式 2 个真实 socket/媒体方法及 1 个 FD 类型方法（3 子例） | 黑洞回执、短慢读、FD 类型边界；不是任意负载延迟保证 |
| 外部 SIP→RVA1→RTP | 两模式各 11 方法、6 子例，共 **22 方法、12 子例通过** | 已 ACK UUID 授权、token、轮次下行、收号/播放互斥、挂机与换代；固定单路场景 |
| 外部生命周期与原始音频 | **32 SIP 周期、32 次新鲜归零**；896 私有请求/回复对，304 音频 RTP、30 DTMF RTP、30 RTCP | 32 周期包含 2 个无 ACK 超时周期，不能全部称为已 ACK 通话 |
| 本轮 Go 完整竞态回归 | **899 个通过事件 = 323 顶层 + 576 子测试**，0 失败/跳过 | 含真实 Pool→Rust PCM；SDK、注册表定向用例已含在总数中，不重复相加 |
| Rust 媒体回归 | 内部 PCM 批次 **147 单元 + 1 集成 = 148 通过**，0 忽略，Clippy `-D warnings` 退出 0 | 外部批次未修改媒体源码，使用同一媒体二进制；不是外部批次又执行 148 次 |
| 既有 SIP/ESL/IVR 等回归 | 当前外部 build-02 的固定 **24 阶段、228 方法通过**，0 超时/强制清理 | 新外部 22 方法保持独立，不加入旧固定集合冒充覆盖增长 |
| 双腿 G.711 固定回归 | 当前候选另执行 **11 场景 × 2 模式 = 22 方法通过** | 保留双腿内容/时钟/释放断言，不以本地 PCM 样本计数替代 |
| 500/5000/10000 路与 72 小时 | 本轮未授予新通过；旧 500 路及历史 5000 路失败保留 | 单路功能和单元测试不能外推容量，更不能推导相对 FreeSWITCH 性能提升 |
| 后台发布 | 正式文档与机器模型需构建内嵌、部署后再以 `/v1/docs` 及页面核对 | 本页不预填 9080 发布成功；原版成对绿色由独立证据判定 |

Go 的“通过事件”包含子测试；Python 的“方法”和“子例”分开计量，各集合不能相加得到兼容率。功能执行与当前文件/二进制绑定必须同时有效，旧成功不直接授予变更后源码通过。

## 对照页的实际增量

正式 [运行验证](runtime-verification.json) 和 [进度摘要](compatibility-progress.json) 已重新生成并离线复核：3994 个对照 ID 中，限定本地通过 **77**、未运行 **3916**、历史容量失败 **1**。从实际主服务 ZIP 冻结的 [1.11 基线](compatibility-baseline-v1.11.json) 为 71 个限定通过，本轮增加 6 个 PCM 接口/协议相关条目。实现状态与运行状态是两套口径，新增条目及原帧门禁通过不代表消除了全部未实现项。

本轮另实际运行固定 FreeSWITCH 1.11.3 的 26 个成对探针：[原版报告](conformance-report.json) / [摘要](conformance-summary.json) 为 **21 个限定通过、3 个真实差异、2 个身份/目录观测**。原版 243 个文件运行前后保持一致，虚拟机正常关闭。它不将新 RVA1 伪装为原版协议，也不授予全部 3994 项或所有通信协议通过；3 个差异继续显示。

这些数字是正式候选文档中的已验证文件结果。管理进程仍需实际采用此文档包，页面能否显示新数字须在部署后由 `/v1/docs` 和 UI 独立核对。

## 本轮接口与执行路径

```text
真实本地 SIP INVITE → 分配 local G.711 → 200 → 合法 ACK
                                             ↓
同 UID 私有 Unix 连接：RVA1 BEGIN → Server 主循环授权 UUID
                                             ↓
                         不可变 Worker/代次/session/turn 句柄
                                             ↓
RVA1 PUSH → 每 Worker 独立 Go 数据 lane → FD3 RSP1/RSR1
                                             ↓
                  有界轮次队列 → 8k PCM → 图编码 → RTP/RTCP
```

PCM 每帧 160 样本/20ms，每批最多 5 帧，队列最多 50 帧/1000ms。入队先校验完整批次，偏移严格等于 accepted_samples；新轮严格递增，同轮 begin 不重播，旧轮及关闭输入不能复活。实际发送仍按固定 160 ticks 推进，超过媒体期限时失败并取消余帧，不能突发补齐旧音频后算通过。

下行与 RX 消费独立，不回送入站音频。PCM 与活动及排队的 tone/WAV、带提示 read 互斥，静默 read/park 可并存；主动 DTMF 与 PCM 使用统一 TX 身份，但独立保留事件时间和生命周期。`uuid_break` 不自动停止 PCM，业务须明确 INTERRUPT；0 立即清待发，20/40ms 只对已经排队的至多 1/2 帧淡出，不撤回已成功提交的 RTP。

受理不等于发送，发送不等于远端听到。计数满足 accepted=sent+queued+discarded，completed/stopped 还需最后实际发送帧的 20ms 区间结束。pending/unknown 不补零、不盲重试；外部保留有限 token 控制恢复，一个 UUID 槽最多 current+previous 两个过渡身份，确定旧轮终态前不删除新轮所有权。

## 内部 PCM 与故障证据

内部原始证据位于研发工作区 `work/voice-runtime-m2-pcm/`，不是文档包内资源链接。`pcm-03/receipt.json` 与 `pcm-03-review.json` 确认 42 方法/16 子例通过，409 个源文件与该次构建一致，218 件产物哈希核验通过，52 次释放与端口复用均有真实结果。该次媒体 SHA 与外部 build-02 相同，但两批 Go/授权范围分别记录。

`faults-02/receipt.json` 与 `faults-connected-02/receipt.json` 的回执黑洞仍实际发送 50 帧/8000 样本，完整 PCM 内容正确，JSON stats 在 200ms 内回应；一秒后只隔离 PCM lane，status/interrupt/release 仍可用。短慢读恢复的 18 批各受理一次，偏移精确且无重执行。非法 FD3 的普通文件、Unix stream、IP datagram 在 ready 前拒绝。这里的“200ms”是限定故障夹具观测，不是系统时延 SLA。

队列另有独立 `work/voice-runtime-pcm-turn/queue-final-02/receipt.json`：12 个正式测试与 2 个分配实验。测量区间的正常队列 10000 帧/1600000 样本、1000 个淡出轮次共 3000 帧/480000 样本的 alloc/zeroed/realloc/free 均为零。该实验只覆盖队列已初始化后的固定存储，不包括整个 Worker、Go 每批副本、网络或供应商路径。

内部 `rust-03/receipt.json` 保留 148 个测试与 Clippy 结果。内部批次旧 Go 796 个通过事件与旧 228 项回归仍有原回执，但本页当前 Go/旧协议结论分别采用外部批次新 899/228 回执，不累计两批重复数量。

## 外部真实 SIP 与候选身份

外部证据位于 `work/voice-runtime-m2-pcm-external/`。`sip-02/receipt.json`、`sip-02-review.json` 和 `batch-receipt.json` 确認真实网络结果：普通 UDP 与 connected UDP 各 11 方法/6 子例，合计 32 个 SIP 周期、32 次新鲜归零，0 超时/强制清理。原始 144 件产物的 SHA 与字节数全部核验，418 个源文件在 build-02 构建与执行前后一致。

实际用例包括正确/错误/缺失 ACK、双律 PCM 与同轮 begin、严格递增新轮及旧 token、park、静默 read 的真实按键和超时、WAV/带提示 read 互斥、挂机后端口复用、Worker 换代、整批偏移/容量拒绝和跨连接控制，以及主动 DTMF 与 PCM 中断的共享 TX。存在 6 次错误 ACK 输入及 2 个完全无 ACK 的超时关闭周期。896 私有请求/回复、304 音频包、30 事件包与 30 RTCP 是该集合实际记录，不能当成吞吐成绩。

每个真实 SIP 用例一次只提交一个未决私有请求；跨连接 token 验证不等于任意并发、注册表状态或恶意请求的全部时序穷举。未知回执和并发边界另按 Go 单元/竞态及内部真实故障集合解释。没有以真实运营商、真实 TTS/ASR 供应商或完整 FreeSWITCH 模块运行替代这些限定夹具。

| build-02 功能候选 | SHA256 |
| --- | --- |
| control | `69504b59594058eb1ff4bd6739c2b2b8116c7c5de0e506f44b8e6e88363af19c` |
| media | `f6ff83c09e411fcf5078b9efb0a2033a6d58e8bad26006395d4f9d01427c0700` |
| generator | `c29e75b9b1a43cf8e14bef533791350a8d7caa0a5120e8e5ee56c6d65af7aeda` |

这些是实际执行功能验收的二进制身份。后续内嵌正式文档会改变控制二进制，须另有构建/部署回执关联，不能把新哈希回填旧测试。原始构建和草稿来源快照原样保留，不因正式文档整合重新授予旧快照当前绿色。

关键原始收据：

- `sip-02/receipt.json` SHA256：`6f1613c8103009ad2f9404d9374b5385a074c2b106da6b3e50d659314b647731`。
- `sip-02-review.json` SHA256：`7897d89a409186a932c59f3714afec297a01c489da1e919b842a06aca285b361`。
- `go-02/go-result.json`：`go-95e88ff2e35d4ee889befe0023009452`，结果文件 SHA256 `598320c1fc5723152a5fd695cde340375be1afa2abff66fd60db0994aea40568`。
- `e2e-02/receipt.json`：`e2e-1bf879ab5cb04414a87a0e8bdbb57e88`，SHA256 `985d00c25b1f05837a8202d5ce2417f6eb0f68d888ad0ee2670cec6443824539`；既有 24 阶段 228 方法当前通过。
- `processed-01/processed.receipt.json`：当前候选另执行的双腿 G.711 固定 22 方法，保持与外部单腿集合独立。

这些 `work/...` 路径仅指明研发原始存放位置。后台公开证据以文档清单实际包含的验证摘要、原帧和 SHA 为准；不向读者提供不存在的包内链接。

## 保留的失败与修复

| 原始记录 | 真实问题 | 修复及新验证 |
| --- | --- | --- |
| 内部 `faults-01` | macOS Unix datagram 回执缓冲满返回 ENOBUFS，旧逻辑立即隔离；2 测试中 1 failure、1 error，属于黑洞场景执行异常及清理失败 | ENOBUFS 与 EAGAIN/EINTR 使用同一 pending 回复及原 1 秒总期限，不重新执行 PCM；两模式各 3 个故障方法通过 |
| 内部 `pcm-02` | 42 方法中 41 通过；UDP RTCP 方法只解码 5/6 帧，随后未执行显式 Release 触发 remaining_sessions=[1]；同一方法共 2 个失败记录 | 原夹具相对等待累积漂移 39.945ms、jitter_late=1；只改夹具为绝对 20ms，保留发生器至少迟到 20ms 失败及必须解码 6 帧断言，pcm-03 两模式完整通过 |
| 外部 `sip-01` | 22 方法中 20 通过，两模式各一个无 ACK 用例等待 A 腿 BYE 超时；缺少完整 INVITE/200/BYE/200 关闭证据 | `calls.go` 的 `uas_expire` 补 `sendByeA(c)`，与原 B 腿关闭及 finish 共同执行；测试源码未改，sip-02 两个无 ACK 周期均见 BYE/200、valid_ack=0 和新鲜归零 |

所有旧 wire、回执、构建身份和失败状态保留；未走到的断言不由后续结果补写。内部 pcm-03 的 RTCP 夹具最大发送检查迟到分别为 UDP 9.830ms、connected 7.980ms，6 帧完整解码；这是输入夹具的实际观测，不能宣称媒体调度、端到端延迟或容量达标。

ACK 修复依据 [RFC 3261 §13.3.1.4](https://www.rfc-editor.org/rfc/rfc3261.html#section-13.3.1.4)：成功应答重传到 64*T1 仍无 ACK 时，UAS 应用 BYE 终止会话。**本次实际用例使用既有可配置 1 秒 ACK 定时器，不证明当前计时策略完整等价 RFC 默认 64*T1**；本次证明的是到期后 A 腿关闭报文缺失已经修复。`sip-01/receipt.json` 原 SHA 为 `d0642e506fe40a056bcc4b46ccdc3ccf824be855122cf04295eba72f4b242680`，不改写为通过。

## 容量、原版兼容与下一步

[v1.10](release-validation-v1.10.md) 的 G.711 100 路 30 秒通过和 500 路失败保持：500 路有效唯一源内容 1498372/1500000，时间戳错误 0，但仍有 988 内容异常、499 过密到达、2 序号异常及 3306 发生器迟发。不能凭这些相关观测将原因归于 GC、操作系统或某个线程。历史 [v1.5](release-validation-v1.5.md) 的 5000 路发生器发送不足失败也保留；更早的 5000 成功不沿用为本候选容量通过。上述功能批次与后台发布后的页面压测分别留证，不能混合为容量提升。

后台发布后另完成一次 **100 路、30 秒、100 CPS、PCMU relay** 界面/API 联调：300000 发送与唯一接收全部对应，主服务通话始终为 0；119 次 HTTP 读取无失败，任务完整回收。原件为 `work/voice-runtime-m2-pcm-external/ui-pressure-01/run-02/receipt.json`，SHA256 `53b8c8f06b0e17633e7e70f95511a0a35fd73a5fa7559cc076fd2f1785d4f578`。该记录绑定 admin-build-01，页面截图采集时任务已经完成，只证明历史结果显示，不能冒充运行中的100路截图。内部另有一次 worker 统计缺项诊断，与启动初期相符，但没有独立错误时刻；发生器123次迟发计数保留。以上不是流式PCM、物理Linux或高并发容量验收。

新增 PCM JSON/RSP1 条目是内部接口，RVA1 是自有外部协议。真实原帧门禁可以给予对应限定本地验证结果，但不能将 `speak`、录音、供应商模块或所有 FreeSWITCH API 自动标绿，也不替代原版成对差异。完整目录、未实现项和 17 项长期 V1 验收合同继续保留，见 [路线图](voice-runtime-roadmap.json) 和 [完整长期合同](voice-runtime-v1-acceptance.md)。

后续仍需实现 RX 向真实 ASR 服务输出、TTS 供应商与格式适配、VAD/轮次协同、异步对齐双轨录音，并完成 G.722/Opus 实时路径、安全媒体、真实线路、物理 Linux 等价容量及长稳验收。1.12 当前只完成本文限定的 PCM 下行及控制生命周期。
