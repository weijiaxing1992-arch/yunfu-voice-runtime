# 真实 SIP / Rust / ASR 第二轮独立审计

5 例的原始断言全部通过；92 件运行原件前后指纹一致。范围是 PCMU/PCMA、8k/16k 输出、普通 UDP/connected UDP，以及 PCMU 的晚订阅。没有运行新网络测试，也没有修改候选源码。

## 音频、结果与授权

- 原始 SIP INVITE/200、错误来源 ACK、OPTIONS 屏障、正确 ACK、BYE/200 的来源端口、目标、Call-ID、CSeq、From、To-tag、Via 分支及 SDP 媒体端口已独立关联。全部媒体端口位于 4000–4799。
- 错误 ACK 后每例确实建立了仅含 HELLO/READY 的短供应商握手，但没有取得 RX、没有 AUDIO 或 PCM，额度恢复为零；正确 ACK 后才取得同 UUID、同订阅的真实 RX。
- 共 44 个原始 RTP 包，其中 4 个属于晚订阅前预先送音。ASR 实际提交 40 帧，独立 G.711 解码核验原生 6400 样本；供应商实际消费总计 8960 样本，逐字节与指定 8k/16k 输出一致。16k 使用固定系数的 Python 直接插零卷积，没有调用生产 Go Converter。
- 完整 RTP 展开同时核验低位及种子周期：普通例序列 131068–131075，晚订阅例 131088–131095，时间戳按起始完整周期和 160 tick 增量核对。测试第一轮漏计种子造成的失败原件未改写。
- 每例 8 次供应商消费检查都使用原始 lower/expiry，实际检查点位于区间内；RX SDK 捕获时钟也在同一原期限内。FINISH/DONE 的帧数、样本数、最后事件及 marker 前缀一致。供应商 4 个 partial 与唯一 final 的 PCM 摘要、来源与覆盖范围正确；客户端实际交付的是合并后的 partial 和 final。

## 资源清理与身份

BYE 后真实新鲜媒体统计中通话、处理图、RX 订阅、排队事件、观察存储及失败计数全部为零；四个媒体端口实际复绑成功。约 32 秒 SIP 缓存与定时器自然到期后，通话、定时器及 ASR 额度归零，Server 正常 drain。所有自有供应商进程收到 SIGTERM 后 wait 成功、无强杀、连接 goroutine 全部 join、socket 删除。

三份实际二进制 SHA 已核对。外层运行收据与套件内层收据的 898 件源文件清单完全相同且运行前后不变。Rust 实际二进制与 media-build-08 一致，其 38 件媒体来源闭包匹配；mock-build-04 的 340 件来源闭包也匹配。

运行结束后，根任务把 `control/internal/asr/stream_real_test.go` 的临时目录由 `/private/tmp` 改为 `/tmp`。本审计准确保留该单文件当前差异，未覆盖历史来源，旧测试结果不认证修改后的完整项目。

## 不应扩大宣称的范围

- 晚订阅第一条确实是 Decoded、没有重放 kind7，但带真实 NewSegment（Boundary5）；尚不能说“真实 SIP 的首条 Decoded 无 Boundary5”也通过。
- BYE 发生在 ASR 正常 completed 之后；本套没有验证真实 SIP 挂断打断待返回 final。第二轮客户端的 late-final 仍来自模拟 LifetimeDone，不能代替此项。
- 原始 RTP 和 ASR1 字节完整保留，中间 RX 证据来自真实 Server SDK 返回帧，未另行抓取原始 RXS2 socket 数据。
- Rust 使用现有 supervisor 的 Kill+Wait 结束，不能宣称 Rust 进程正常 exit 0。
- 供应商是独立进程的传输校验模拟器，没有识别模型；不证明真实供应商效果、生产 Linux 容量、全部 Codec、全部协议或 FreeSWITCH 100% 等价。

可复算脚本及全部原件指纹见本目录 `audit.py`、`receipt.json`。审计只读取既有证据，不重新绑定任何网络端口。
