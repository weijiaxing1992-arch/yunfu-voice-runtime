# M2 识别上行候选工作检查点

## 最新整体代码检查（2026-09-07，以下记录优先于后文历史状态）

- 用户最新要求：整体检查代码，完善逻辑。目标未缩为只做 ASR，也未宣称完整 FreeSWITCH 或万路完成。
- Server 未创建 RX 绑定的未知槽泄漏已修复：仅实际任务完成且无绑定，或明确未入队，发布 noBindingConfirmed；普通取消、未知晚回执不提前归还。server-no-binding-fix-01 定向 race 98 项、vet 通过。
- 初始暂停前缀、终态继承字段矩阵、FINISH/DONE/最终交付竞态、单帧期限和上下文所有权修复已完成。combined-pure-02 为 225 项 race、vet 全通过；SDK 元数据独立 unit/race 各 78 项，frames/provenance 各 80 项。对应独立原件保留，不相加为兼容模块。
- client-real-02 使用 mock-build-04，9 例真实 Unix 通过。client-independent-audit-02 独立逐样本核验，过期消费超期 101651916ns 被拒绝；迟 final 确有 376 字节写尝试、接受 0 字节、无交付。RX Source 是测试输入，不是 SIP 挂断。
- server-real-01 五例失败源于新加测试 oracle 未计 Rust 的 SEQ_SEED=1<<16 / TS_SEED=1<<32；失败原件不覆盖。修正固定预期后 server-real-02 五例真实 SIP/Rust/ASR1 全通过，165 秒、前后源和二进制稳定。逐样本、错误 ACK 拒绝、FINISH/DONE、BYE、实际资源/端口清理均有原件；晚订阅无 kind7 回放但有 Boundary5，且 BYE 在正常完成后，不能扩大为 final 等待窗口真实挂断。
- 当前 full-review-01 正在对候选全 Go 包执行 -race -p=1；包含真实 RX、暂停、SIP 和 ASR，各证据目录独立。执行期间不改 control/config/media。其后 vet、页面函数测试、Darwin/Linux arm64 编译。只有实际 receipt 才算完成。
- root 已把独立代理真实测试的 /private/tmp 改为两平台 /tmp，并补 Makefile 的 asr-mock 与真实 ASR 验收环境，避免常规 make test 静默跳过新链路。
- ASR 资源快照已接入 /v1/status、/metrics 与总览面板。observability-review-01 独立只读检查一致；槽位计数不代表模型识别成功。
- 读取主服务三条真实历史记录：2000 路 relay 发送 2000000、唯一收到 456233，失败保持；5000 路仍受本机隔离端口上限拒绝，未启动。pressure-readonly-review-01 确认失败末尾缓存统计过旧、socket 丢包计数不支持却显示 0 两处诊断缺口，尚不能定位丢包的一跳。
- rx_freshness 正在 benchmark-fix-01/control 私有副本修复这两处，需等其冻结后核验基线再合并 project，避免破坏 full-review-01 来源记录；之后做合并范围回归，不回填旧收据。
- asr_server 在项目外写 asr-stream-reference.md 和 OpenAPI 字段修改建议；正式内嵌文档/对照发布包仍为冻结 1.13，尚未把本批新源码计入绿数。
- 主目录源码实际已是 1.13；相对 ASR baseline 只缺此前 RX clock/lifetime 两增量；运行 9080 仍为 1.12。没有在本批替换主目录源码或主服务。既有具体停服替换自动审批拒绝仍未获明确答复；本轮没有部署请求或重试。

后文保留前一阶段检查点作历史背景，其“当前进行中”已被上述状态覆盖。

本轮属于实质开发进展，不是等待回合，完整目标仍未完成。请继续推进既有完整 Voice Runtime V1 目标，不能仅凭本批适配器完成标记 goal complete。真实 ASR/TTS 模型、完整 AI 通话、Linux 5000/10000 路与灰度回退仍需各自验收。

## 运行服务与授权

`outputs/rustswitch` 和运行中的 9080 未在本轮变更。上一批冻结的 1.13 候选在 `work/voice-runtime-m2-rx/admin-build-01`，其发布与回归、后台隔离预览证据见该目录 CHECKPOINT / PUBLICATION-STATE。主服务切换命令曾被自动审批明确拒绝，原因是未获明确允许短时停止/替换当前服务；用户尚未回答此前更新许可。不能把 goal 自动继续视为同意，也不能绕过该拒绝重启主服务。无需反复提问，继续独立开发。

本轮新候选 `work/voice-runtime-m2-asr/project` 来自已验证 1.13 源码和已独立测试的 RX LifetimeDone / RemainingBudget 增量。基线见 `baseline.json`。没有将新候选的测试计入主服务绿数。现有 1.13 的 82 条有限验证、3999 条对照不是新 ASR 的证明，不能移用。

## 实际已开发内容

- `control/internal/asr`：真实私有 Unix 客户端、HELLO/READY、8k/16k送音、来源/时钟证明、8条结果队列、FINISH/DONE、部分写未知、取消和物理清理。每流三个固定 I/O 协程，Server共享四个清理执行者。
- `control/internal/asr/resample`：固定127抽头8→16k FIR、按需转换、固定历史；10880样本与独立Rust参考一致，约4.6µs/帧仅是本机DSP微基准，不代表路数。
- `control/internal/asr/wire` 与 `control/cmd/asr-mock`：ASR1严格有限协议、实际共享时钟、同UID检查、独立有界模拟进程/故障模式/原始字节和PCM证据。模拟进程不是识别模型。
- `control/internal/config/asr_stream.go`、`server/asr_stream.go` 及最小配置/admin/Close接线：部署默认关闭；起建/活动/未知清理均占UUID与容量；真实Pool关闭后才能证明未知RX已退休。管理页面配置草稿不允许改部署固定的代理地址。
- 候选接口说明在 `ASR-API-DRAFT.md`，未发布到后台。

## 已执行验证

- DSP最终证据 `resample-evidence/final-receipt.json`，unit/race各23项，10880样本独立Rust oracle。
- 帧/来源层此前冻结 `frames-provenance-evidence/test-03`，unit/race各63项。之后的初始暂停前缀审查发现需进一步修复，见“当前进行中”，不要把旧证据绑定到新源码。
- wire/mock03：60个race通过，两平台构建，`mock-build-03/asr-mock` SHA `f3a103a5d076244a3735f28abf77c6961f1dff48c424e6a5ddbb8d04f0c5b48e`。
- Server管理器32项race，完整配置79项race，config/server vet，`server-unit-01/HANDOFF.md`。
- root纯状态smoke依次保留 `project/root-unit-01..04.jsonl`，没有覆盖历史。
- 独立生命周期审查新增 `client-review-tests-02` 20项race，源码前后稳定；修复Finish预留、最终交付、DONE绝对期限、Gap写总预算与负停止回执。
- `client-real-01/receipt.json`：真实Unix客户端和独立mock，9个案例全部首次通过，前后相关源/二进制稳定，自有9进程都正常TERM/Wait，socket都清理。RX输入明确是测试Source，不能冒称真实SIP/Rust。原生8k、16k、中途首Decoded、错DONE、缺DONE、错token、重复final、消费暂停、等待final期间撤权。
- 独立audit发现上述等待final撤权例清理过快：可能在provider实际final写尝试前SIGTERM，仅证明等待final期间拒绝旧结果。下一版测试已改为先等provider连接receipt和final-write-attempt再停止，需用mock04重新执行。
- `combined-pure-01`：180个纯race用例全过、0fail/skip、源稳定；整体gate未通过，因为vet指出writer读窗口cancel所有权。root已改为固定readWindow对象明确close/renew，随后实际vet已过，需最终新目录重跑合并gate。不能覆盖01失败结论。

## 当前进行中

1. rx_freshness代理在自己四个frames/provenance文件内修复“初始PLC/Missing带flags64后，裸首Decoded的changeSource清掉暂停”的实证缺口。保留失败负例；恢复必须有同源更大段的真实Boundary5，也支持Missing先带新段、随后Decoded无重复边界。不要自行同时编辑这些文件。
2. rx_suite代理先保存client-real01独立raw审查，然后将mock同上述暂停规则同步，增加final-write-attempt及消费拒绝精确检查时钟，冻结mock-build04；不要重用03为修后证据。
3. asr_server代理独占 `server/asr_stream_real_test.go`，准备5例真实SIP/Rust/StartASR：PCMA/PCMU×8/16k，及实际晚订阅。计划每例真实32秒GC，总约170秒，只写/编译尚未启动网络。主机测试必须统一排队，不重叠；所有端口<10000，保护主服务媒体范围。环境名以其最终交接为准，当前 `RUSTSWITCH_REAL_MEDIA`、`RUSTSWITCH_ASR_MOCK_BIN`、`RUSTSWITCH_ASR_SIP_OUTPUT`。

## root下一步

- 等上述暂停修复/新mock冻结，运行 `run-pure.py combined-pure-02`（实际环境见functions.store.env）。纯包筛去显式真实Unix用例；不能将skip当通过。
- 运行 `run-client-real.py client-real-02 mock-build-04`。该脚本真实启动临时Unix子进程，需要相同本机网络隔离许可，先前client-real01已被允许。不含主服务操作。输入9例、绑定客户端本地依赖+mock/vendor，前后不变才通过。
- 接着统一启动Server真实5例，独立新输出，保留所有失败和实际进程/端口清理，实际Go test超时至少300秒；要继续每60秒内向用户简要更新。
- 对通过的真实链路补正式接口文档/内部能力对照/精确证明再做候选发布门禁。主服务授权仍待回复。

## 已知下层差异，单独修复

Rust Push拒收/迟到可发 LocalExpired(reason10/11,discarded=1,duration160)、AuxiliaryExpired(reason10,discarded=1,duration0)。现SDK与ASR wire把非SourceBoundary的discarded一概拒绝。ObservationQueue溢出/序号耗尽还会将原记录转kind9 reason8/9并保留边界/计数，现校验也有冲突。必须依据实际kind/reason严格定义允许矩阵，不能把任意计数全放开；本批尚未宣称此路径端到端通过。

## 工具与环境

根目录 `/workspace/yunfu-voice-runtime`。无Git，不要操作 `outputs/rustswitch 2`。使用work/toolchain的Go1.27.1、Rust1.98.1、bundled Python3.12；函数store的env包含完整PATH/SDK/GOCACHE/CARGO_TARGET_DIR。历史32秒GC和原生媒体二进制可复用实际1.13构建，但必须验证路径/SHA，不可猜测PID或用老进程号清理。
