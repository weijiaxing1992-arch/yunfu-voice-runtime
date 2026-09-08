# 源码交付独立审计（2026-09-08）

本次只读核对原源码、私有补丁、固定测试收据与交付副本，未修改实现、合并补丁、重跑通话或压测，也未操作 9080。审计写入范围仅为本目录的报告、清单和只读检查脚本。下列“通过”均注明实际范围，不能解释为完整 FreeSWITCH 兼容、真实识别模型接入或 5000/10000 路验收通过。

## 源码身份与交付范围

| 源树 | 收录文件 | 与交付副本核对 | 状态 |
| --- | ---: | --- | --- |
| `outputs/rustswitch` | 1126 | 1126 项逐字节相同 | 主工程源码；并不表示本次安装到运行服务 |
| `work/voice-runtime-m2-asr/project` | 1171 | 1171 项逐字节相同 | 含 ASR 增量的独立候选，未覆盖主工程 |

这里的计数包括生产源码、测试、依赖、配置示例、源文档及现有嵌入文档；不包括 `bin/`、`work/`、缓存、日志或其他运行产物。两源树差异共 **59 个路径：新增 47、修改 10、缺少 2**。候选没有 `.gitignore` 与 `deploy/rustswitch.service`，这两份仍完整保存在主工程交付目录，不能把候选自身说成含有独立 systemd 示例。

路径到 SHA256 映射按键排序、紧凑 JSON 编码后计算的集合摘要如下；这不是 Git 提交号或单个二进制摘要。

- 主工程：`ede243048e0bf87b9b367ef8845090c2ecfecb01544c479139140cfb1846ab5e`
- ASR 候选：`420cc5fa1c09f19250a7266579f6f9895479cd2855d28f863c9430995f31c7e8`

审计开始和结束重新哈希，两份原源码集合未变化。逐文件身份、排除项及 59 项差异见 [source-inventory.json](source-inventory.json)；交付副本核对见 [delivery-source-binding.json](delivery-source-binding.json)。

## 已完成内容与证据是否仍匹配

| 内容 | 实际证据 | 当前源码匹配与边界 |
| --- | --- | --- |
| ASR1 严格 Go 线协议、独立同 UID Unix 模拟进程、来源/期限与错误注入 | `mock-build-04`、`client-real-02`、`server-real-02`，并进入 `full-review-01` | 已在 ASR 候选；模拟进程验证字节接收和 PCM 摘要，不进行语音识别 |
| ASR 客户端、8k 原生/127 抽头 FIR 16k、生命周期/取消/结果交付、Server 配置与容量管理、总览统计 | `full-review-01` | 收据覆盖的 **896/896 文件当前相同**；Go 原始事件重新统计为 **504 顶层、1471 含子项通过**，12 包通过，另 2 个无测试包为 skip；未发现失败测试事件 |
| 完整 Go 静态检查与构建 | `full-review-01` | vet、2 组 UI 函数验证、Darwin 与 Linux arm64 控制面构建退出码均 0；原日志 SHA 与 4 份被引用/新建二进制 SHA 均复核一致 |
| 新版 ASR 源接口说明 | `source-doc-update-01` | `docs/api/README.md`、`openapi.json`、`asr-stream-reference.md` **3/3 当前相同**；源验证已过，但没有同步嵌入资源或完成发行门禁，不并入前一项 896 文件回归范围 |
| RX 原始期限剩余量与句柄生命期 | `voice-runtime-m2-asr-staging/test-02` | 4 个补丁文件已进入候选：3 份与 staging 相同；`rx_stream.go` 后续增加 noBindingConfirmed 修复，不能冒用旧 SHA，已由新定向检查和全包回归覆盖 |
| 初始暂停/首帧来源恢复修复 | `initial-prefix-review/final-receipt.json`、`frames-provenance-evidence/test-04` | 4/4 文件当前相同；更早 `frames-provenance-evidence/candidate.patch` 是历史中间稿，不应再次应用 |
| 严格 discarded/CN/终态继承元数据、未知无绑定资源槽修复 | `rx-metadata-validation-02/final-01`、`server-no-binding-fix-01`、`full-review-01` | 已在当前候选，不能另列为尚待合入的新补丁 |

原 `server-real-02` 的 **5 例**真实 SIP→Rust→ASR1 验证覆盖 PCMU/PCMA × 8k/16k，以及晚订阅。独立审计核对了错误 ACK 拒绝、完整 RTP 展开周期、逐样本 PCM、FINISH/DONE、真实 BYE、32 秒计时器回收及四端口复绑。它的 BYE 在正常识别协议完成后，不能扩大为“等待 final 期间的真实 SIP 挂断竞态已验证”。模拟供应商仍不等于 ASR 模型；本批也没有万路负载结论。

完整的收据/日志/二进制与当前源对应关系在 [evidence-binding.json](evidence-binding.json)。测试计数按原始 Go 事件重算，没有直接相信收据的 `passed` 标记；这些计数不能相加为兼容模块数。

## 尚未合并的压测补丁与未实现的原子统计

`work/voice-runtime-m2-asr/benchmark-fix-01` 确有 **6 文件**私有补丁：`benchmark.go`、`benchmark_processing.go`、`benchmark_runner.go`、新 `benchmark_failure_diagnostics_test.go`、`web/tests.js` 与 `tools/test_testlab_ui.cjs`。当前主工程和 ASR 候选仍与其 6 项基线相符，尚未合并。

`files.json` 与 `unified.patch` 保存的是 **test-02 已测版本**。从原基线恢复的 6 文件哈希与该清单、test-02 完全一致。test-02 定向 unit/race 各 16 顶层 + 42 子项、3 组既有 UI 检查通过；它改进失败末尾采样与 socket 丢包计数可用性显示，但仍不能严格证明 RPC 采样开始晚于发生器退出。

当前私有工作稿在 test-02 之后又修改了 **2 文件**：`benchmark_runner.go` 和 `benchmark_failure_diagnostics_test.go`，加入实际 defer 使用的 `benchmarkContextOutcome` 及策略测试。私有树与 test-02 的 863 项闭包为 **861 相同、2 不同**；**没有 test-03 收据**。因此交付中的“已测补丁”与“当前工作稿”必须分开，不得以旧通过结果为当前 6 文件稿授予整体通过。

根因修复 B——将 Stats、RPC 请求开始/返回时刻、PID/generation、Admission 原子发布——**尚未实现**。`worker-observation-fix-01` 目录不存在；主工程、候选及压测私有副本没有新增 `stats_requested_at` 等四字段合同实现。当前 worker 分别存入 Stats 与 Admission，状态响应亦分别读取；旧统计可能配上较新时间。现有 `worker_stats_sampled_at` 不能被解释为已经完成这次修复。

以上文件及哈希见 [patch-state.json](patch-state.json)。将来应先完成原子统计与相应回归，再接入压测结束判据；不能通过放宽绿色判据掩盖旧采样。

## 早期工作目录是否漏交代码

- RX 1.13 的 `admin-build-01/source` **895/895 文件与主工程相同**，不存在该固定快照中另有未收录路径的实现。
- PCM external `build-02/source` **534 个路径全部仍存在**。52 个文件相对主工程变化，主要是后来 RX/文档演进；对变化的 Go/Rust/Python 生产文件静态提取旧函数入口，没有发现旧函数名消失。此项是静态遗漏筛查，不代替语义回归。
- ASR staging 的两项先决 SDK 边界已进入 ASR 候选，不能再次打补丁覆盖后续修复。
- `work/voice-runtime-m2-asr-design` 的 **6 份独立离线设计/错误注入文件**尚不属于两源树；6/6 与 `selftest-03` 相同，52 个离线方法通过的日志也匹配。交付包已在 `source/design-reference/asr1-fixture` 单独归档，6/6 副本哈希相同，并保留明确“仅离线协议模型”标签。其来源/暂停语义之后有实质修订，最终 Go wire/mock 与真实测试为本批实现依据，不能把这份旧模型当最终生产实现。

核对明细见 [earlier-source-binding.json](earlier-source-binding.json)。未遍历所有历史压测运行目录；本结论限定于上述明确的完成候选与私有补丁来源。

## 源码、依赖、许可与排除边界

应保留以下构建所需内容：`control/cmd`、`control/internal`（含页面、嵌入文档、FS 模板）、`control/go.mod`、`go.sum`、完整 `control/vendor`；`media/src`、`media/tests`、`Cargo.toml`、`Cargo.lock`；`native/include`、`examples`、`audio`、保留的 SpanDSP 源；`tests`、测试向量和固定验证夹具；`tools`、Makefile、配置示例、部署示例、源文档及其引用。

交付包已补入 `dependencies/rust-vendor` **39 个 crate**，与 Cargo.lock 的 39 个 registry 项逐项闭合；所有 `.cargo-checksum.json` 所列文件哈希均一致，没有额外文件。`dependencies/opus-1.5.2.tar.gz` 的 SHA 为 `65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1`。Go 固定依赖为 `golang.org/x/sys v0.30.0`，已有 vendor。旧 README 中“Go 控制面无第三方模块依赖”的句子已过时，应在交付说明中明确纠正，不能据此删去 vendor。

需同时保留实际存在的 `control/vendor/golang.org/x/sys/LICENSE`、FS 模板 `LICENSE.freeswitch` 与参考许可、`native/vendor/spandsp/COPYING`、`native/audio/OPUS-COPYING`、来源清单及 Rust vendor 中的上游许可。**未找到项目自研部分独立 LICENSE 文件**，不能自行赋予开源许可，交付方需明确自有代码使用范围。

源码包应排除：原 `bin/` 和旧构建可执行文件、`work/` 中运行配置及凭据、私钥/令牌/`.env`、服务状态与 PID/socket、真实通话录音/原始媒体、临时压测数据、Go/Rust 缓存、`target/`、`__pycache__`、`.DS_Store`。独立证据包可选择保留脱离运行目录的固定报告，但不应把全部原始业务数据混入源码。正常源码中的测试向量（例如 `rust_reference.bin`、`.f64le`、`.json.zlib`）属于测试输入，不能仅按扩展名当旧二进制删除。

本次对两份 config JSON 的字段名检查没有发现 password/token/secret/credential 类字段；对收录源码做凭据文件名与 PEM 私钥头检查没有发现私钥。不打印任何字段值。该检查不证明任意文本都绝无敏感信息；已知运行目录仍按排除规则整体隔离。第三方 FS 公共示例配置与许可应保留来源。

## 离线构建通过与发行门禁失败分别保留

根代理用交付包实际执行了两次离线构建，本审计复核其原日志 SHA、源副本哈希与编译输出存在性：

- `build-main-01`：Go 控制面、callbench、Rust release、C G.711 ABI 动态库编译退出码均 0。
- `build-asr-candidate-01`：上述产物及 asr-mock 编译退出码均 0。

构建使用 Go vendor、`GOPROXY=off` 与 Cargo `--locked --offline`，输出在交付包之外；不需要把工作区缓存当作依赖分发。它们没有运行网络或压力验收，也没有替换历史测试中的二进制身份。当前输出的独立 SHA 记录在 [delivery-source-binding.json](delivery-source-binding.json)，仅作为本次观测，不回写历史收据。

原始只读文档发行门禁 **两树都失败，退出码均 1**，必须在交付说明中保留：

1. 主源码：`绿色已失效：key-api-pcm-stream`。原能力证据在当前来源闭包下不能继续授予该绿色；本审计没有重测或修改它。
2. ASR 候选：`字段字典落后于机器合同`。源接口说明已更新，嵌入文档/字段字典与最终发行验证尚未同步。

新 `delivery-tools/build_source.py` 的直接离线编译和既有 `make` 文档发行门禁是两个不同范围。前者通过不能抹去后者失败；交付是可核查源码和资料的交接，不是生产安装完成或无差异发布证明。
