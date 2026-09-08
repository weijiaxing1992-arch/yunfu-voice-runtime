# FreeSWITCH 平替 RustSwitch · 开源说明

**RustSwitch** 是面向电话外呼、呼入与语音智能体的开源实时语音内核。媒体处理采用 Rust，呼叫控制与管理后台采用 Go，通过 C ABI 衔接现有编解码生态。项目以高性能实时语音为核心，逐步提供有明确业务价值、可验证的 FreeSWITCH 接口兼容。

历史工程与资料也使用“云蝠 Voice Runtime”名称，仓库与发行文件继续保留 `yunfu-voice-runtime` 标识，方便来源追踪和既有链接使用。

## 项目状态与公开发行

首个公开开发预览版 [v0.1.0-preview.20260908](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases/tag/v0.1.0-preview.20260908) 已发布，包含完整源材料、项目资料、两个平台的编译包及校验记录。对应 Git 提交为 `54fcd571874d599b10611dc7f38f2a751a433c94`，首发时有 4,991 个文件和 8 个发行附件。

此版本适合学习、开发、接口评估、限定场景复现与社区协作。源码分发已经完成；功能发布门禁、完整兼容和容量验收仍按 [当前状态](08-当前能力与验证状态.md) 逐项推进。main 分支后续文档修改不替换冻结 Release，也不生成新的历史通过结论。

## 公开材料清单

| 材料 | 位置或分发方式 | 内容与状态 |
| --- | --- | --- |
| 主工程源码 | `source/main/` | Rust 媒体、Go 控制、管理页面、配置、构建工具及现有测试 |
| ASR 候选源码 | `source/asr-candidate/` | ASR1、Unix 客户端、PCM 处理、观测与模拟代理；独立候选，未合并主工程 |
| 诊断补丁与工作稿 | `source/patches/` | 已完成定向测试的快照与后续未复验快照分开保留 |
| 设计参考 | `source/design-reference/` | 协议夹具、离线设计与验收矩阵 |
| 项目资料 | [资料索引](DELIVERY.md)、根目录 01—13 章、`reference/` | 项目说明、拓扑、架构、接口、使用、运维、需求和缺口 |
| 固定构建依赖 | `dependencies/`、源码树中的 vendor | 39 个 Rust 固定依赖、Opus 归档、Go vendor 和 SpanDSP 子集 |
| 源码及完整资料 ZIP | GitHub Releases | 分别下载源码，或一次下载资料、源码与运行包 |
| 平台运行 ZIP | GitHub Releases | macOS ARM64、Linux ARM64；平台要求与验证范围见各自 README |
| 开源社区资料 | `COMMUNITY.md`、`CONTRIBUTING.md`、`.github/` | 社区协作、贡献规则、问题与兼容报告、Pull Request 模板 |
| 许可与来源 | `LICENSE`、`NOTICE`、`LICENSE_SCOPE.md`、`THIRD_PARTY_NOTICES.md` | 原创材料许可、第三方例外、版权与分发来源 |

源码中保留必要测试向量、配置示例、中文注释和历史证据。公开副本处理了个人开发路径与非必要运行地址，具体范围见 [公开分发说明](open-source/PUBLICATION.md)及[内容审查](open-source/content-audit.md)。

## 许可证与第三方边界

本项目有权授权的原创代码、原创文档、构建与测试工具采用 [Apache License 2.0](LICENSE)。第三方内容保留上游许可；整理、复制或生成文件不改变其中第三方材料的版权。

| 内容 | 许可依据 |
| --- | --- |
| 项目原创 Rust、Go、管理页面、文档与工具 | Apache-2.0，已列明例外除外 |
| 各源码树 `native/audio/g722_support.c` | LGPL-2.1-only，用于可替换的 G.722 共享库 |
| SpanDSP G.722 子集 | LGPL-2.1-only 及文件中保留的更具体上游声明 |
| FreeSWITCH 原始配置模板 | MPL-1.1；具体文件及上游声明优先 |
| Opus、Go 依赖、Rust 依赖和运行时 | 各自 BSD、MIT、Apache、ISC、Unicode 等许可及所附专利声明 |

完整适用关系以 [LICENSE_SCOPE.md](LICENSE_SCOPE.md)、[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)及许可证原文为准。含第三方材料的资源包不能整体视为仅适用 Apache-2.0。改变链接方式、重新分发或修改第三方文件时，应继续落实相应许可证要求。

FreeSWITCH 名称用于说明来源、迁移场景与兼容对照。RustSwitch 是独立项目，不是 FreeSWITCH 官方发行版；软件许可不授予第三方商标背书或官方认证。

## 兼容与性能的表述原则

1. 固定参考版本、模块配置和实际测试范围，再比较协议、事件、错误及运行行为。
2. 分开记录实现状态、执行结果和证据是否仍匹配源码。3,999 条目录记录包含声明、配置和验收条目，不是完整功能分母。
3. 只有实际执行且证据仍适用的场景可以写为通过；历史通过不自动扩展至新提交、平台或负载。
4. 公开构建、文件校验与业务验收各自说明结果；没有据此宣称完整 FreeSWITCH 兼容、5,000/10,000 路生产容量或完整真实 ASR/TTS/Agent 链路。

当前主工程文档门禁记录 `key-api-pcm-stream` 证据失效，ASR 候选记录字段字典与机器合同不同步；这两项是各自检查遇到的首个阻塞。具体优先级、依赖和完成条件见 [需求说明书](12-需求说明书.md)与[未完成清单](13-项目未完成清单.md)。

## 参与项目

从 [快速开始](QUICKSTART.md) 运行主工程，再按 [贡献指南](CONTRIBUTING.md) 选择适合自己的改进。欢迎可复现的问题报告、真实线路互通记录、性能与音质实验、接口测试、供应商适配和文档改进。

- 问题与需求：使用仓库 [Issues](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues)，提供版本、平台、复现步骤和预期行为。
- 代码与文档：通过 Pull Request 提交，说明改动范围及验证结果；涉及接口时同步合同与使用说明。
- 新依赖与资源：记录来源、版本、许可、校验值及修改情况，保留必要版权声明。
- 安全问题：按 [安全说明](SECURITY.md) 处理；公开报告避免包含线路密码、访问令牌和个人录音。

提交者应确保有权提交相应内容，并沿用被修改文件的许可。项目通过公开可复现的实现与证据推进兼容和性能目标。
