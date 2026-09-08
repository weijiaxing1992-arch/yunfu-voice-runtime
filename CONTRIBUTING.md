# RustSwitch 贡献指南

欢迎改进语音智能体需要的电话接入、呼叫控制、实时音频、按键/IVR、资源保护、可观测性及测试资料。本指南适用于代码、文档、测试、兼容报告和第三方依赖变更。

先阅读[项目首页](README.md)、[文档中心](DOCUMENTATION.md)和[未完成清单](13-项目未完成清单.md)。安全漏洞使用 [SECURITY](SECURITY.md) 的私密流程；一般协作规则见 [COMMUNITY](COMMUNITY.md)。

## 1. 选择正确的修改范围

| 路径 | 用途与贡献方式 |
|---|---|
| `source/main/` | 默认功能开发与缺陷修复入口，含 Go、Rust、C 适配、后台和主工程测试 |
| `source/asr-candidate/` | 已公开的独立 ASR 候选；修改时明确候选身份，不能把验证结果直接转为主工程状态 |
| `source/patches/` | 已测补丁与工作稿分别保留；需要明确采用哪个快照以及与主工程的差异 |
| `source/design-reference/` | 协议原型与设计测试，不作为生产实现交付结论 |
| 根目录项目说明及社区文档 | 当前公开阅读入口；与中文/英文首页、版本说明保持一致 |
| `reference/`、`evidence/` | 注明版本的参考副本与历史证据；不要覆盖旧结果来表达新结论 |
| `dependencies/`、各源码树的 vendor 目录 | 固定第三方内容；变更须记录版本、来源、许可和重新验证范围 |

`source/main/` 是目录名，与 Git 分支 `main` 分别理解。合并候选时应逐项处理代码、合同、构建、文档和回归，避免用整套候选覆盖主工程。

拼写、链接等小型修正可直接提 PR。新增接口、较大架构变化或引入依赖，先在 Issue 说明问题、方案、现有行为、兼容影响及验收条件。

## 2. 准备开发环境

工具最低要求和离线构建步骤见 [QUICKSTART](QUICKSTART.md)。建议在独立分支工作，并为运行、构建和测试选择隔离目录；不要使用现有生产或重要通话服务的端口。

未修改的发布快照可先在仓库根目录校验：

```sh
python3 delivery-tools/verify_delivery.py
```

该命令核对分发文件完整性，不测试服务行为。`manifest.json`、`source/*-manifest.json` 等清单记录具体分发快照；本地修改后不再匹配属于预期现象。贡献者应提交实际修改与验证结果，发布者在形成新的分发快照时重新生成对应清单。不得改动历史构建收据或测试哈希来伪装源码未变化。

以下交付构建命令仅适用于**未修改的主工程源码快照**：

```sh
python3 delivery-tools/build_source.py \
  --variant main --output ../rustswitch-contribution-build
```

输出目录须尚不存在且位于仓库之外。`build_source.py` 会强制核对 `source/*-manifest.json`，源文件修改后会直接拒绝构建，不只是普通完整性校验提示。开发修改应使用相应源码目录自身的 Go/Cargo/Makefile 入口进行构建和定向测试，并记录遇到的文档门禁阻塞；不要篡改历史清单来绕过快照检查。具体入口与约束见本指南第 4 节。

构建只证明本次产物可编译，不等于接口、互通或容量通过验收；编译器需要自行安装。

## 3. 实现与注释

- 使用现有模块边界，明确状态所有权、资源释放、取消、超时、队列容量和进程代次。
- 新增或修改的非显然逻辑使用中文注释解释约束与原因，特别是协议兼容、实时音频和并发处理。
- 避免只为单个正常用例增加无上限队列、无超时等待或按通话数无限增加线程/协程。
- 接口变更同步字段定义、错误行为、配置说明和相应 FreeSWITCH 对照；公开 HTTP、内部 IPC 和设计接口必须分别标识。
- 遵循现有 Go、Rust、C 和前端格式。不要为无关重排或格式变化扩大 PR 范围。
- 第三方文件保留原有声明和注释；新增中文说明放在明确属于本项目的适配层或文档中。

## 4. 根据变化选择验证

| 变化 | 必要证据 |
|---|---|
| 文档与链接 | 链接目标存在、示例与现有字段一致、中文/英文范围不矛盾；有图文布局时实际渲染检查 |
| 呼叫与状态逻辑 | 能重现原问题的测试，覆盖相关取消、超时、重传、资源回收和失败路径 |
| Go 并发逻辑 | 相关测试及适当的 race/vet 检查；说明是否接入真实媒体程序 |
| Rust 媒体路径 | 相关单元/集成测试、格式与静态检查，以及受影响的音频正确性验证 |
| 编解码或原生 ABI | 目标平台构建、加载、边界输入和往返/音频检查；不能用库加载成功替代实时路径验收 |
| SIP、ESL、XML 兼容 | 固定 FreeSWITCH 版本下的成对输入、返回/事件和失败场景，或明确尚无原版运行环境 |
| 性能与容量 | 完整的环境、负载、音频路径、时长、成功/丢失计数和端到端结果，绑定源码和二进制 |

主工程已有检查入口见 [Makefile](source/main/Makefile)、[测试目录](source/main/tests)和[工具目录](source/main/tools)。例如，在 `source/main/` 下执行文档门禁只读检查：

```sh
python3 tools/build_api_docs.py --check
```

当前发布快照的该门禁已有阻塞：主工程记录 `key-api-pcm-stream` 证据过期，候选记录字段字典落后于机器合同。这些错误应保留并单独说明，不等同于本 PR 新引入的失败，也不能直接忽略。`make all`、`make test` 和 `make check` 可能先受文档门禁影响，应报告实际执行到了哪一步。

`go test` 的有限单元检查不能替代需要 `RUSTSWITCH_REAL_MEDIA` 等环境的真实媒体回归。`make test` 中存在共享测试端口和真实收音要求；先查看测试约束，按需要顺序运行，在新目录保存证据。纯文档修正无需无关的全量通话压测。

所有测试结果注明通过、失败、跳过或未执行及其原因。mock 验证、编译成功和历史通过应使用各自名称，不写成完整业务验收。

## 5. 更新合同、文档与证据

修改接口时先定位主工程的合同源与生成工具，更新源定义并检查生成结果，避免只修改内嵌页面副本。对应规则见 [DOCUMENTATION](DOCUMENTATION.md)。

新验证结果保存为新记录，至少包含源码/二进制版本、环境、命令或步骤、场景、结果和未覆盖范围。保持历史原始记录不变，通过新说明解释证据适用范围。不得通过修改到期时间、跳过断言、把未运行标绿或缩小对照分母来提高兼容统计。

公开材料应移除凭据、真实客户号码、个人录音、生产地址和无关机器信息。保留影响复现的数据关系；必要时使用合成音频、协议夹具或最小脱敏样例。

## 6. 提交 Pull Request

一个 PR 聚焦一个可以独立评审的变化，按照[现有模板](.github/PULL_REQUEST_TEMPLATE.md)写明：

1. 具体问题、触发条件与关联 Issue/工作包。
2. 修改的是主工程、候选还是文档；变化前后的行为。
3. 验证环境、步骤、实际结果及未验证范围。
4. 接口、配置、数据或兼容语义的变化，以及必要的迁移/回退说明。
5. 新增第三方内容的来源、版本与许可。

提交前检查差异中没有运行凭据、生产配置、客户数据、缓存、构建产物、无关日志或不必要的大文件。编译程序和大型资源按发布流程放入 Release 资产，不默认加入源码提交。

评审可能要求缩小范围、补充失败路径或调整合同。讨论围绕可复现行为进行；提交 PR 不保证合并时限、发版日期或商业支持。影响使用者的变化应提供适合写入 [CHANGELOG](CHANGELOG.md) 的说明。

## 7. 许可与第三方内容

提交者应有权公开提交的代码和材料。原创贡献按本项目 [Apache-2.0](LICENSE) 提供，已有第三方文件及明确例外按 [LICENSE_SCOPE](LICENSE_SCOPE.md) 处理；不要覆盖其版权和许可。

引入或更新第三方内容时保留版权与许可证、记录可核对的上游来源，并更新 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES.md) 和相关机器清单。无法确认来源的代码、语音、图像或测试数据不应直接纳入公开提交。

## English contribution checklist

English issues and pull requests are welcome. Work in `source/main/` by default; identify ASR-candidate, patch, and design changes explicitly. Small corrections can be submitted directly, while substantial interface or architecture changes should be discussed in an issue first.

Include the problem, before/after behavior, exact source variant, relevant tests, actual outcomes, and untested boundaries. Explain non-obvious implementation constraints in Chinese comments to match the codebase, and update affected contracts and documents. Preserve third-party notices, omit private data, and never rewrite historical results to make compatibility checks pass.

Snapshot-integrity failures after local edits are expected; they do not establish a functional regression. The delivery builder enforces source manifests and rejects modified source. Use the source project's Go/Cargo/Makefile entry points for development builds and targeted tests, without rewriting historical manifests to bypass checks. The current documentation gates also have recorded pre-existing blockers. Report which checks actually ran, without treating mock, build, or historical results as full runtime acceptance. Read [QUICKSTART](QUICKSTART.md), [DOCUMENTATION](DOCUMENTATION.md), and the existing PR template before submission.
