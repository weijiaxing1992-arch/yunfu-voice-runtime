# ASR1 独立模拟供应商

这是私有 Unix ASR1 的真实进程实现，接收实际字节并返回接收 PCM 的 SHA256 摘要。它不包含识别模型、VAD、真实云供应商鉴权或识别准确率评估。单元通过不能替代 SIP/Rust/Go/Unix 端到端验收。

## 运行合同

必须显式提供规范绝对路径：

```text
asr-mock --listen /实际私有目录/provider.sock --artifacts /不存在的新证据目录
```

socket 的直接父目录必须已经存在、为同 UID 的真实目录且无组/其他用户权限，例如 0700；socket 路径不得存在。程序不会删除其他对象，退出只移除本次创建且 inode 未变的 socket。接入时核验 Unix 对端 UID。证据目录必须不存在，创建为 0700，文件以 0600 排他新建。

本机双方必须处于同一时钟命名空间：Linux CLOCK_MONOTONIC=1 或 Darwin CLOCK_UPTIME_RAW=2。对远程代理没有时钟或 TLS 兼容承诺。

| 参数 | 默认及边界 |
|---|---|
| `--max-connections` | 64；1..64，同时连接槽，不能逐帧创建执行者 |
| `--max-total-connections` | 256；不得低于同时连接数，最多 4096，限制证据目录/文件总量 |
| `--max-bytes` | 64MiB；64KiB..1GiB，所有连接的原帧/PCM/事件共同预留预算 |
| `--per-connection-bytes` | 8MiB；至少16KiB，且不超过全局预算 |
| `--fault` | none；下表故障模式之一，显式注入不伪装成正常实现行为 |
| `--delay-ms` | 150；0..5000，仅故障暂停使用；READY 超时注入需设大于2000 |
| `--fault-after` | 1；指定第几个 AUDIO，范围1..100000 |
| `--final-gate` | false；final 前等待证据根目录 `final.release` 正规控制文件，最多2秒 |

输出预算在读/写尝试前预留整个请求尺寸，部分 I/O 不返还，所以预留量可能大于实际文件字节；这是保守预算，不能当作样本/吞吐计数。每连接收据另有 8192 字节固定上限，根 ready/run 收据也各有固定上限；总连接数同时限制这部分有界元数据。超限或原始证据保存失败会终止本次模拟进程，不会丢弃记录后继续报告完成。

每个已准入连接一个 Go 协程，另有固定接受/信号退出管理；不为每帧建协程或 OS 线程。空闲读取上限12秒；首字节到完整消息为不续期的1秒；初次 HELLO 总上限2秒；输出每条消息最多1秒。SIGTERM/中断关闭 listener 与本次连接并等待处理协程退出，不影响其他服务进程。

## 错误注入

| 模式 | 实际动作与证据 |
|---|---|
| `ready-delay` | HELLO 完成后暂停再写 READY；事件记录检查点及实际共享时钟 |
| `read-pause` | 消费第 N 个 AUDIO 后停止下一次 socket 读取，制造内核积压 |
| `consume-pause` | 完整读入第 N 个 AUDIO、保存原始消息后暂停；恢复时重新读共享时钟并验证原 expires，不能把已收字节当已消费 |
| `late-final` | final 写出前暂停；可区分正常 Finish 后合法迟到文本与真实挂断后的撤权 |
| `close-before-done` | 写过 final 后在 DONE 前断开；这是失败，不是识别流成功 |
| `wrong-token` | 首个 RESULT 换成另一个非零连接 token |
| `duplicate-final` | 第二次发送同 utterance 的 final，使用下一个 result_seq |
| `wrong-done` | 将 DONE samples 增加1，故意绕过正常编码对象校验后写出真实错误 JSON |
| `oversize` | 首个 RESULT 写出 length=8193 的前缀后断开 |
| `half-eof` | 首个 RESULT 只写17字节后断开，保留真实已写原件 |

`--final-gate` 会先写本连接 `waiting-final` 事件，随后轮询根 `final.release`；控制文件必须同 UID、正规文件、最多32字节。真实测试应在看到检查点后发送并确认真实 SIP BYE，再创建控制文件。仅调用 context.Cancel 不能冒充 BYE 证据。

## 原始证据

根目录有 `ready.json` 与退出时 `run-receipt.json`。每个准入连接独立 `connection-0001/`：

- `inbound.bin` / `outbound.bin`：包括前缀的真实 socket 字节，保留部分读/写和错误前缀。
- `final-attempted.bin`：只保存准备尝试写出的完整 final；它与系统 Write 已接受的 `outbound.bin` 前缀分开计量，不能用准备原文冒充送达。
- `consumed.pcm`：仅通过共享期限及来源检查后消费的完整 Decoded PCM，CN/PLC/辅助事件不给它补零或增加样本。
- `events.jsonl`：完整读/写的原件偏移、检查点、真实共享时钟、消费的原 lower/expiry/来源/样本和 PCM 偏移。
- `receipt.json`：实际消费前缀、连接状态/错误、原始文件 SHA、PID 与时钟域；不能据此单独推定真实供应商识别或全链路完成。

`consume-rejected` 保留完整消息被拒绝时实际用于检查的 `checked_clock_ns`、原 `lower_ns`/`expires_ns`、消息类型和错误。`final-write-attempt` 与 `final-write-result` 分别记录尝试前后共享时钟、尝试原文偏移、实际写入前缀长度和系统写错误；看到 `waiting-final` 本身不足以证明发生过迟到写尝试。测试应等待连接收据落盘后再结束模拟进程，避免提前关闭服务吞掉待验证动作。

媒体时间在整个会话内保持下界；来源换代也不能倒退。中途订阅不重放 Rust 来源边界，首个完整 Decoded 可直接用实际正 generation/segment 初始化，不能合成额外 marker；此前 PLC/CN/Aux 只保留元数据及已知媒体下界，不创建音频范围。显式来源起始允许 segment=0；NewSegment 可直接跟随 Decoded。SourceEnded 结束该 generation，允许声明同 generation 尚未播放的更高段但不注册它为音频；首观察就是 SourceEnded 时也只记结束身份。结束后必须由新的 generation 来源边界恢复；flags64 的普通消费后挂起则可由同 generation 的更高语音段恢复。PLC/CN/缺口不上传补偿 PCM；辅助 DTMF 占观察序号不自动代表音频缺口。RXS2 允许过期 CN 辅助记录携带 `cn_applied=true`，ASR1 保留此事实且仍无 SID/虚构20ms时长；未生效 CN 的单纯 AuxiliaryExpired 也不自动宣告音频缺口。

初始元数据已经声明暂停时，首个 Decoded 也必须证明同 generation、更大 segment 的真实 NewSegment；同段、倒退段及裸换代均拒绝。真实 Missing/NewSegment 可以先恢复来源身份并推进已知媒体下界，但不创建 PCM 范围，之后的完整 Decoded 才形成真实音频。观察失败终态仅继承 Rust 确实可能留下的边界、丢弃计数、时长及 CN 标志组合，混合不同观察的字段会被拒绝。

真正验收需独立重算入站音频、展开 RTP 来源、原始到期值、实际消费检查时钟、partial/final 顺序、FINISH/DONE 前缀以及通话/媒体/ASR 的实际回收。不存在匹配 DONE 的 EOF、半帧和一个 final 都不能记为 completed。这里的单元测试只检查确定性状态与本地文件，不启动 Unix 服务。
