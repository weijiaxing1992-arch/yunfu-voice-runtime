# RVA1 外部 PCM 流接口（1.12.0）

本文描述 1.12.0 正式候选的本机 Unix 持久连接接口，依据 `control/internal/server/pcm_endpoint.go`、`pcm_endpoint_protocol.go`、`pcm_stream.go` 和 `control/internal/config/pcm_stream.go`。真实 SIP 外部入口、Go 和既有协议回归已取得限定结果；实际部署需以 `GET /v1/docs` 核对，见 [1.12 验证记录](release-validation-v1.12.md)。底层二进制通道与轮次状态另见 [内部 PCM 合同](pcm-turn-reference.md)。本入口是 RustSwitch 自有协议，不授予完整 FreeSWITCH 兼容或容量通过。

## 入口与信任边界

外部可信进程通过 Unix stream socket 发送 RVA1 请求，读取 RVR1 回复。音频按二进制传输；不需要每 20ms 调用 HTTP、构造 JSON 样本数组或创建通话线程。一个连接可交替操作多个通话，同一连接按请求顺序处理。控制连接和数据连接分开，防止等待数据回执阻塞独立控制入口；这不证明任意负载下的公平性或时延。

本接口的信任边界是本机同 UID 的可信进程。这个权限边界不隔离同 UID 的恶意调用者或文件系统竞争。socket 直接父目录必须存在、属于服务有效 UID，且不向组或其他用户开放；正常部署使用 0700 私有目录，socket 设置为 0600。启动拒绝已有 socket、普通文件和符号链接路径，不接管旧实例路径。配置校验只检查字符串与数值；目录权限及冲突由启动阶段检查。本接口没有用户登录、租户隔离、远程 TLS 或浏览器授权，不能复用管理 HTTP 的 CSRF token 充当音频凭据。

配置示例：

```json
{
  "pcm_stream": {
    "socket_path": "/run/user/1000/rustswitch/pcm.sock",
    "max_connections": 32,
    "max_streams": 1000
  }
}
```

这里只列出完整启动配置中的可选节点，其他既有配置仍需提供。省略整个节点或使用 null 关闭入口；非空对象必须有路径。路径为非根目录的规范绝对路径，最多 100 个 UTF-8 字节，不允许 NUL、CR、LF、重复分隔或 `.`/`..` 折叠。`max_connections` 省略或 0 使用 32，其他值仅允许 2..64 偶数；`max_streams` 省略或 0 使用 `limits.max_calls`，有效范围 1..`limits.max_calls`。整个节点由部署配置管理，管理页面 PUT 不得启用、禁用或修改其中任意值。

连接总额度固定，控制类和数据类各占一半。HELLO 从连接处理开始到完整握手共有 1 秒期限，首字节不能续期；完成握手后等待下一帧首字节最多 30 秒。后续请求收到首字节后，头与正文共有不可续期的 1 秒读取期限；每个完整回复另有 1 秒写入期限。外部命令调用的 context 当前为 1 秒，数据路径还受 Pool 的有界队列与独立 lane 约束；这些是失败边界，不能当作成功时延承诺。

## 通话资格与媒体格式

BEGIN 由 Server 唯一呼叫主循环授权。必须是当前已存在的 A 腿 UUID，且为已收到 ACK、业务仍建立、资源已分配且未释放中的本地单腿 G.711 通话。配置须启用 `media.processing=g711`，分配回执确认 `processing_topology=local`、处理版本 1；当前 Worker 健康、代次一致，并声明基本 G.711、本地 G.711 和 PCM turn 能力。PCMU/PCMA 必须为 8kHz、单声道、20ms。桥接 B 腿、未 ACK、已挂机或换代 UUID 不符合资格。

每个输入样本为 **8kHz、单声道、有符号 16 位小端 PCM**。一帧 160 样本、320 字节、20ms，一批 1..5 帧，即 160..800 样本、320..1600 字节。协议没有可变采样率、声道或编解码字段，调用者必须先提供正确的原生格式；24k/48k 音频不能仅改标签后直接提交。该入口不意味着 G.722/Opus 实时图已接入，也不自动进行外部 TTS 格式转换。

BEGIN 建立固定轮次队列，最大 50 帧/1000ms。新 turn_id 必须严格增加；同 turn_id、同配置是查询式幂等恢复，不能复活已关闭输入或重复播放历史样本。不同调用连接共享同一 UUID 的当前所有权；这个机制不等于多写者混音。

## 请求头 RVA1

每个请求由固定 **64 字节头**和 `body_length` 字节正文组成。所有整数均为无符号小端；PCM 正文样本单独按 i16 小端解释。连接必须先 HELLO。`request_id` 在该连接上非零且严格递增，不能重复或回绕后继续使用；重连后的新连接有自己的序列。

| 偏移 | 字节数 | 字段 | 含义 |
| --- | --- | --- | --- |
| 0 | 4 | magic | ASCII `RVA1` |
| 4 | 1 | operation | 操作编号 0..7 |
| 5 | 3 | reserved | 必须全零 |
| 8 | 8 | request_id | 当前连接的请求身份 |
| 16 | 16 | token | 服务返回的原始 16 字节令牌；不是 UUID 文本 |
| 32 | 8 | turn_id | 非零轮次身份；HELLO/PING 为零 |
| 40 | 8 | offset_samples | PUSH 为连续接收偏移，END 为最终样本总数 |
| 48 | 4 | body_length | 正文的字节数，解析头时先检查上限 |
| 52 | 2 | arg1 | HELLO 连接类别、BEGIN 缓冲长度或 INTERRUPT 淡出长度 |
| 54 | 2 | arg2 | BEGIN 预缓冲长度；其他操作为零 |
| 56 | 8 | reserved | 必须全零 |

| 编号 | 操作 | 连接类别 | 请求字段与正文 |
| --- | --- | --- | --- |
| 0 | HELLO | 首帧 | arg1=1 为控制类，arg1=2 为数据类；token、turn、offset、arg2、正文长度全零。不得重复握手 |
| 1 | BEGIN | 控制 | token 全零，turn_id>0，offset=0；arg1=buffer_ms，arg2=prebuffer_ms；正文为 1..128 字节 UTF-8 UUID，不含结尾 NUL |
| 2 | PUSH | 数据 | 非零 token 和匹配 turn；offset 为预期 accepted_samples；arg1/arg2=0；正文为 1..5 个完整 PCM 帧 |
| 3 | END | 控制 | 非零 token 和匹配 turn；offset 为 final_samples；无正文，arg1/arg2=0 |
| 4 | INTERRUPT | 控制 | 非零 token 和匹配 turn；arg1 仅 0/20/40ms；offset、arg2、正文长度为零 |
| 5 | STATUS | 控制 | 非零 token 和匹配 turn；offset、arg1/arg2、正文长度为零 |
| 6 | FORGET | 控制 | 字段同 STATUS；执行零淡出停止，取得真实终态后从外部注册表遗忘令牌 |
| 7 | PING | 两类均可 | token、turn、offset、arg1/arg2、正文长度全零；仅检查连接，不证明媒体或通话健康 |

BEGIN 的 `buffer_ms` 为 20..1000 且是 20 的倍数，`prebuffer_ms` 为 20..buffer_ms 且是 20 的倍数。PUSH 偏移按 160 样本对齐并与 Rust 当前 accepted_samples 严格相等，END 总量同样按 160 对齐。整批非法、乱序或超容量拒绝，不部分入队；不要从 Unix stream 写入成功推断 Rust 已受理。

## 回复头 RVR1

每个回复为固定 **96 字节头**和最多 **512 字节 UTF-8 消息**。请求编号用于关联，不是自动去重重发许可。成功 PUSH 表示原子入队，不表示 RTP 已发送或对端已听到。

| 偏移 | 字节数 | 字段 | 含义 |
| --- | --- | --- | --- |
| 0 | 4 | magic | ASCII `RVR1` |
| 4 | 1 | operation | 对应请求操作 |
| 5 | 1 | state | 下表状态编号；0 可表示该错误回复没有可用状态 |
| 6 | 2 | code | 结果类别 |
| 8 | 8 | request_id | 对应请求身份 |
| 16 | 16 | token | BEGIN 成功或已提交但未知时可返回稳定令牌 |
| 32 | 8 | turn_id | 回复所描述的轮次 |
| 40 | 8 | accepted_samples | Rust 累计原子受理样本 |
| 48 | 8 | sent_samples | 已由实际成功 UDP 发送提交的样本 |
| 56 | 8 | queued_samples | 当前待发送样本 |
| 64 | 8 | discarded_samples | 当前轮接受后因中断或失败丢弃的样本；新轮替换时计数重置 |
| 72 | 8 | oldest_age_ms | 当前最老排队帧的等待年龄 |
| 80 | 4 | queued_bytes | queued_samples×2 |
| 84 | 4 | queued_ms | queued_samples÷8 |
| 88 | 2 | buffer_ms | 当前轮缓冲预算 |
| 90 | 2 | prebuffer_ms | 当前轮预缓冲预算 |
| 92 | 2 | message_length | 紧随其后的消息字节数，最多 512 |
| 94 | 2 | reserved | 必须为零 |

状态编号为：0 无状态/idle，1 buffering，2 playing，3 draining，4 stopping，5 completed，6 stopped，7 failed。非成功回执中的零计数可能只是缺少实际观测，不能作为“未执行、已停止、队列为空”的证据。只有完整且身份匹配的成功状态回执可用于对账。

| code | 类别 | 调用者处理 |
| --- | --- | --- |
| 0 | 已完成该操作或确定受理 | 按操作含义解释；入队不等于媒体播放完成 |
| 1 | 帧结构或连接请求序列非法 | 连接通常随之关闭；修正协议，不盲重放旧音频 |
| 2 | PCM 数据 lane 不可用 | 使用独立控制连接查询/停止；不能切换格式伪造成功 |
| 3 | 确定的业务、参数或偏移拒绝 | 保留原始偏移，根据真实状态修正；不自动改成成功 |
| 4 | 未知 token | 检查服务重启、FORGET 或退休回收；按 UUID/原 turn 明确恢复 |
| 5 | 暂忙或背压 | 包括正在建立/遗忘、同轮数据在途、Go 待发队列满及真实 Rust 队列满；退避并查询，不增加并发重试风暴 |
| 6 | 操作结果未知 | 可能已经提交；保留非零 token，禁止自动重放，按下节恢复 |
| 7 | 连接类别或全局流额度耗尽 | 降低接入或等待固定预算回收，不通过其他类别绕过 |
| 8 | handle 已退休 | 挂机、代次或服务生命周期已撤销此权限；不能继续写旧轮 |
| 9 | 连接类别不匹配 | PUSH 使用数据连接，BEGIN/END/INTERRUPT/STATUS/FORGET 使用控制连接 |
| 10 | 无法安全创建令牌 | 当前 BEGIN 未正常完成，保留错误，不用自造令牌替代 |

错误码 5 统一背压分类：真实 Rust PCMRejection 的 queue_full（内部 Code=8）映射为 5；参数/偏移拒绝仍为 3。这里的外部编号与内部 RSR1 编号不同，不能混用两份表。

## 未知结果、跨连接与轮次恢复

未知 BEGIN 在确已提交时保留 handle、token 与 MaxStreams 占位。code 6 带非零 token 只授予对该未知轮的控制恢复能力，不证明媒体轮已开始；pending 阶段的控制操作也可能暂时返回 5。待提交结果收敛后，可从另一控制连接查询真实状态或发送 INTERRUPT(0)。不得将 pending handle 用于 PUSH，不得因超时归还流额度后无条件启动更多轮。若 code 6 没有 token，也不能据零计数推断未执行；可按同 UUID、同 turn、同配置重做 BEGIN 恢复，始终使用新 request_id。

PUSH 已写出后失去回执同样是未知结果。先查询原轮 accepted_samples/sent_samples/queued_samples，必要时显式停止并等待真实终态。SDK 会保守关闭未知轮的后续输入；不要通过同 turn BEGIN 尝试复活或重放。重建正常音频流时使用严格增加的新 turn_id，并重新确认旧轮的处理结果。

token 归属于当前服务注册表，可跨该同 UID 客户端的连接使用，不绑定某条 Unix stream 连接。连接 EOF、空闲超时或回写失败只回收连接额度，当前实现不自动 FORGET 该连接曾经使用的所有音频轮。已有排队音频可能继续发送；实时源缺供时由既有 PCM 超时机制终止，不得把 socket 断开当作“旧音频已经停止”。关闭整个 Server 则会撤销句柄并进入媒体回收。

同 UUID 的 BEGIN 和 FORGET 受互斥标志保护。替换请求已发布但结果未决时，注册表一个槽最多同时保留 **current 和 previous 两个 handle/token**，不增加 MaxStreams 占用，也不允许叠加第三个未收敛候选。旧 token 暂时仍可 STATUS/INTERRUPT，权限与实际旧 turn 绑定；这不允许继续 PUSH 或通过旧 token 写入新轮。前一轮的输入关闭不会因恢复令牌而复活。

若新候选被明确拒绝并退休、旧 handle 仍有效，固定回收器或下一次 BEGIN 的短锁核对会删新 token、恢复旧 handle/token，仍保留原槽。若新轮实际替换成功，或 SDK 将结果未知的新轮绑定为当前所有者，旧 SDK handle 会退休，随后只清理 previous token。两者都退休时才按一次槽回收处理。过渡中的 STATUS 可能返回退休码 8，完成回收后旧 token 返回未知码 4；不要把两者误判为新轮已完成。

FORGET 在写锁内再次核对 token。使用旧 token、或当前槽仍保留未退休 previous 时返回 5，调用者可先针对选定 turn 发 INTERRUPT；不能以旧轮停止为由删除新轮槽。过渡收敛后，FORGET 只有获得 completed/stopped/failed 真终态才删除记录；未知结果保留可查询身份。下一次 BEGIN 先做退休状态核对，未收敛的 previous 仍存在时返回 5，不叠加第三个候选。服务重启、Call 结束或 Worker 换代使旧 handle 退休，固定回收器每 250ms 最多检查 256 个槽；满载新 UUID 的 BEGIN 直接拒绝，不在每个请求下全表扫描。

## 实际完成语义与典型顺序

轮内必须保持 `accepted_samples = sent_samples + queued_samples + discarded_samples`。合法新轮会丢弃旧待发内容并将当前计数从零开始；当前协议没有持久旧轮历史，不能把新轮快照当作旧轮最终账本。END 的 final_samples 必须等于当时 accepted_samples；低于预缓冲阈值的完整短音频可以明确结束并排空，不补造静音。draining 仍有待播或尾帧时间，completed 需最后一帧实际成功发送后再经过 20ms；它也不等于声卡或远端终端已完成播放确认。

INTERRUPT(0) 关闭输入并清待发队列。20/40ms 淡出只保留至多已有队头 1/2 帧，按实际保留 N 样本从原第一个样本 gain=1 线性降至最后一个样本 0，不重复已发帧、不等待新音频。重复淡出不重新计时或延长旧帧；0 可升级为立即停止。RTP 已经成功提交的包不能撤回。

典型客户端先建立一个控制连接和一个数据连接并各自 HELLO，然后：

1. 用已 ACK 的 A 腿 UUID、turn_id=1、buffer_ms=200、prebuffer_ms=40 发送 BEGIN，确认 code 0 和非零 token。
2. PUSH 第一个 160 样本，offset=0；确认真实受理后 PUSH 下一个 160 样本，offset=160。客户端不并发提交同轮不同偏移。
3. 所有 PUSH 已对账后发送 END(final_samples=320)。若仍有数据在途导致 5，先收齐该数据结果再重试 END；不要重发音频。
4. 使用控制连接 STATUS，观察实际 draining→completed，或 INTERRUPT 请求停止并等待真实终态。
5. 无需保留该轮状态时 FORGET 回收注册表条目。后续轮使用严格更大的 turn_id。

实时下行走原 PCM→G.711 encoder→统一 RTP 身份，主动 DTMF 使用同一 TX SSRC/序号/时钟；上行仍独立解码消费，不反射成回声。连续音频固定每帧 20ms/160 ticks；并行 DTMF 的事件包也占 RTP 序号，不能仅从音频子流误判包序缺失。单腿首次供音 2 秒、已开始后的空队列缺口 100ms、发送至少迟到 20ms 即失败等限制见 [内部 PCM 合同](pcm-turn-reference.md)，外部请求不修改阈值。

PCM 与正在执行或已排队的 tone/WAV、带提示音 read 互斥，不自动抢占或混音；静默 read 和 park 可以与 PCM 并存。`uuid_break` 和 read 终止键仍只控制既有提示音/收号，**不会自动停止当前 PCM turn**。要使旧 PCM 音频失效或淡出，业务必须明确调用本接口 INTERRUPT 并检查真实终态；详见 [IVR](ivr-reference.md) 与 [应用生命周期](event-lifecycle-reference.md)。

## SIP、ESL 与未实现范围

SIP 负责真实通话和 ACK 生命周期；ESL 已有 UUID、播放、收号和控制能力按各自合同继续工作。RVA1/RVR1 是 RustSwitch 自有二进制接口，既不是 FreeSWITCH ESL 报文，也不是其模块 ABI，不可因为通过外部 PCM 用例就将 `speak`、第三方 TTS 或完整 FreeSWITCH 模块标绿。

本批不假称已经接入流式 ASR/TTS 供应商、VAD 自动打断、LLM 实时对话、双轨异步录音、G.722/Opus 实时处理、WebRTC/SRTP 或租户授权。当前真实 SIP 入口已完成下述固定场景；未知回复、慢读与注册表并发须按各自单元/故障回执解释，不能从这组每次仅一个未决请求的 SIP 用例推导任意并发协议穷举。既有 24 阶段 e2e-02 已取得完整回执，228 方法通过、0 超时和强制清理；其范围保持原固定回归集合。本文不声明整个 Worker 零堆分配、5000/10000 并发通过或调度延迟达标；旧失败历史与未验证范围保持独立。


## build-02 实际功能候选验收

原始证据位于研发工作区 `work/voice-runtime-m2-pcm-external/`，并非本页下载包中的资源链接。已核对 `sip-02/receipt.json`、`sip-02-review.json`、`go-02/go-result.json` 和 `build-02/runtime-build.json` 的结果、身份与前后源清单；这是一组独立的新候选结果，不沿用原审查草稿的工作哈希作为最终证明。

- SIP：普通 UDP 与 connected UDP 各 11 个方法、6 个子例，共 **22/22 方法与 12 子例通过**，0 超时/强制清理。范围是真实 SIP ACK 授权→私有 RVA1→Go SDK→FD3→Rust 本地 G.711→RTP。
- 生命周期：共 **32 个 SIP 周期、32 次新鲜归零**。其中 2 个完全无 ACK 的周期通过超时 BYE 关闭；另包含 6 次明确错误 ACK 发包。因此不能把 32 个周期都称为已 ACK 通话。
- 原始交互：**896 个私有回复、304 个音频 RTP、30 个 telephone-event RTP、30 个 RTCP**。144 件原始产物的 SHA 完整性已由独立 review 核验，源清单共 418 件，构建与运行前后一致。数据包计数不是吞吐或质量容量证明。
- Go：`go-02/go-result.json` 的全量 race 为 **899 个通过事件 = 323 顶层 + 576 子测试**，0 失败/跳过，包含生产 Pool→真实 Rust FD3/JSON→PCMU UDP。
- `e2e-02/receipt.json` 已实际完成：既有 24 阶段 228 方法通过，收据 `e2e-1bf879ab5cb04414a87a0e8bdbb57e88`；退出 0、无超时/强制清理，完整 418 源码仍与 build-02 相同。它是既有固定功能回归，不是所有 FreeSWITCH 模块认证。

此候选控制二进制 SHA256 为 `69504b59594058eb1ff4bd6739c2b2b8116c7c5de0e506f44b8e6e88363af19c`；媒体二进制为 `f6ff83c09e411fcf5078b9efb0a2033a6d58e8bad26006395d4f9d01427c0700`，与上一批相同。此处列明实际二进制身份，不将旧媒体回执自动扩展到新 Go/SIP 入口。

### 首轮无 ACK 失败与修复

`sip-01/receipt.json` 保留为失败：两种模式各运行 11 方法、10 方法通过，`test_no_ack_timeout_reclaims_and_old_uuid_cannot_begin` 各发生一次等待 A 腿 BYE 的 TimeoutError；实际 SIP INVITE/200/BYE/200 闭环检查因此也不完整。不能因进程最终退出或媒体释放而把这两个无 ACK 场景改写为通过。

生产 `control/internal/server/calls.go` 的 `uas_expire` 分支补入 `sendByeA(c)`：已向 A 腿发送 200 后，ACK 超时也要发送 BYE 通知该腿，再结束业务和释放媒体。依据 [RFC 3261 §13.3.1.4](https://www.rfc-editor.org/rfc/rfc3261.html#section-13.3.1.4)，UAS 在重传 2xx 达 64*T1 仍未收到 ACK 时，应以 BYE 终止会话。**本测试使用既有可配置的 1 秒 ACK 定时器，不证明当前定时策略完整等价于 RFC 的默认 64*T1 行为**；此次实际修复并验证的是到期关闭时 A 腿 BYE 的缺失。

新的 `sip-02` 在两个无 ACK 周期均实际观察到 BYE 与对应 200、valid_ack=0，随后各有一次新鲜归零。首轮原始 wire、错误和旧构建身份仍保留，未覆写、未降低该闭环断言。

该组是本机隔离的单路功能验收，每次只有一个未决私有流请求；跨连接 token 复用不等于任意并发时序穷举。它没有验证真实运营商电话、ASR/TTS 供应商、VAD、录音、完整 FreeSWITCH 原版成对或 5000/10000 并发。本文完成正式文档整合，不据此预填后台部署成功。部署回执与 `/v1/docs` 版本须独立核对；完整批次记录见 [1.12 验证](release-validation-v1.12.md)。
