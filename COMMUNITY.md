# RustSwitch 社区指南

RustSwitch 是面向电话语音智能体的开源实时语音项目，采用 Rust 媒体、Go 控制和 C/C++ 适配层，逐步兼容有实际迁移需求的 FreeSWITCH 接口。当前处于开发预览阶段，协作以可复现的问题、清楚的接口合同和可核对的测试证据为基础。

本指南说明参与入口、讨论与决策方式，以及共同维护项目的约定。技术贡献流程见 [CONTRIBUTING](CONTRIBUTING.md)，文档与版本规则见 [DOCUMENTATION](DOCUMENTATION.md)，安全问题见 [SECURITY](SECURITY.md)。

## 参与入口

| 目的 | 入口 | 提交时说明 |
|---|---|---|
| 安装、配置或接口使用提问 | [GitHub Issues](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues) | 版本、平台、阅读过的文档、期望与实际结果 |
| 报告缺陷 | [缺陷模板](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues/new?template=bug_report.yml) | 最小复现、脱敏配置、关键日志、主工程或候选 |
| 提议功能或设计 | [功能模板](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues/new?template=feature_request.yml) | 业务场景、现有缺口、接口变化、完成条件 |
| 提交 FreeSWITCH 互通结果 | [兼容报告模板](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues/new?template=compatibility_report.yml) | 两端版本、成对场景、报文、实际差异、测试范围 |
| 改进代码或文档 | [Pull Requests](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/pulls) | 问题、行为变化、测试与文档、未覆盖范围 |
| 报告安全问题 | [私密报告说明](SECURITY.md) | 按说明提交，不在公开讨论中放入敏感漏洞细节 |
| 查阅进度 | [未完成清单](13-项目未完成清单.md)、[变更记录](CHANGELOG.md) | 同时核对实现、验证状态与对应源码 |

中文和英文的问题、文档改进与技术讨论均可提交。当前以本仓库 Issues 和 Pull Requests 为公开协作入口，尚未公布独立聊天群、固定会议、响应时限或付费支持方案。

## 第一次参与

不需要先理解整个媒体内核。可以选择一个独立、可验证的改进：

| 方向 | 第一份贡献示例 | 参考 |
|---|---|---|
| 安装体验 | 在明确的系统与工具链上运行快速开始，记录失败步骤并修正说明 | [QUICKSTART](QUICKSTART.md) |
| 文档与翻译 | 修复链接、解释术语、翻译一个小节，保留相同的能力边界 | [文档中心](DOCUMENTATION.md)、[英文首页](README_EN.md) |
| 线路与按键 | 提供脱敏 SIP/DTMF 最小场景，标明预期事件和超时条件 | SIP-01 至 SIP-05、TEL-01 |
| 压测诊断 | 为缺失统计、取消或过期样本增加可重复的失败案例 | DIA-01 至 DIA-03 |
| FreeSWITCH 对照 | 选择一个命令、事件或拨号计划场景，记录固定原版与本项目结果 | FS-05、[对照资料](backlog) |
| Agent 音频 | 提议一个供应商适配、轮次取消或音频来源检查的独立设计 | AI-01 至 AI-08 |

工作包编号及验收条件见[未完成清单](13-项目未完成清单.md)。上述是任务方向，不代表已创建、分配或承诺完成相应 Issue。小型文档修正可直接提 PR；多人协作或范围较大的工作建议先在 Issue 说明意向，确认是否已有相关工作。

真实线路、供应商和容量验收需要相应环境。没有环境时可以贡献协议夹具、离线测试或文档，并标明真实链路尚未验证。

## 问题报告与技术讨论

一份可复现的报告应包括：

1. **版本**：Release 标签或 Git 提交、使用 `source/main/` 还是 `source/asr-candidate/`、二进制来源。
2. **环境**：系统、架构、相关工具链与依赖；性能问题另附 CPU、内存、网卡和网络布局。
3. **场景**：注册、呼叫、播放、收号、实时音频或压测中的具体步骤。
4. **结果**：期望行为、实际行为、是否稳定复现，以及最小配置和操作。
5. **证据与范围**：脱敏报文、日志、失败任务结果和未测试部分。

发布前移除真实密码、令牌、客户号码、个人录音及无关业务信息。用回环或文档示例地址代替生产地址，同时保留与复现相关的字段关系。发现敏感信息已被公开时，应立即撤销相应凭据并按[安全说明](SECURITY.md)联系维护者。

## 协作与决策

项目不以下载量、Star 数或单个测试成功作为接受变更的依据。评审主要判断问题是否明确、实现是否符合合同、资源与故障边界是否清楚，以及证据是否覆盖声称的行为。

| 角色 | 职责 |
|---|---|
| 报告者与使用者 | 描述场景、提供必要证据、补充复现反馈；不要求具备代码贡献经验 |
| 贡献者 | 控制修改范围、说明设计与迁移影响、提供适当验证并回应评审 |
| 评审参与者 | 围绕具体行为、测试和接口提出可执行意见，明确哪些结论尚不确定 |
| 仓库维护者 | 根据仓库权限评审合并、维护发布内容、组织缺陷与安全报告处理 |

这里描述的是协作职责，不代表已设立独立委员会、固定团队席位或轮值安排。贡献与评审记录以 GitHub 提交、Issue 和 PR 为准。

对重大接口、数据模型、兼容范围或依赖引入的调整，先在 Issue 记录问题、方案、替代方案和验收方式，再提交聚焦的 PR。重要决定与理由应保留在关联讨论中；发布后的对外变化记录到 [CHANGELOG](CHANGELOG.md)。有分歧时先明确可验证的断言和证据，不以重复争论替代实验。

## 共同约定

- 尊重参与者，讨论代码和具体行为；不接受骚扰、人身攻击、仇恨表达或未经同意披露个人信息。
- 不把候选、设计原型、历史报告或 mock 当作主工程的完整生产能力。
- 不删除失败记录、延长证据有效期或重分类条目来制造“全绿”。
- 不以未经验证的容量、零丢包、零故障损失或完整 FreeSWITCH 等价能力宣传项目。
- 不要求 Star、捐赠或商业采购作为提交问题或参与讨论的条件。
- 提交内容需有权公开，尊重第三方代码、报文、图像及音频的许可与隐私。

维护者可以按仓库权限对偏离主题、泄露敏感内容或违反上述约定的内容进行引导、隐藏、锁定或移除，并在适合公开时说明处理原因。不要在公开线程转发敏感细节；安全相关情况使用私密报告流程。

## 传播与认可

如果 RustSwitch 对你有帮助，欢迎 Star 仓库、分享项目链接，或发布注明版本、环境和限制的实测文章。能够复现的互通案例、解释清楚的失败分析和有条件对照的基准更有助于其他人判断项目是否适用。

不使用刷星、互星交换、批量账户、虚构客户或未经证实的性能数字推广项目。贡献记录来自实际提交和评审；安全修复的公开时间应避免提前暴露利用细节。

## 许可与项目关系

原创贡献适用本项目 [Apache-2.0](LICENSE)，已有第三方文件适用各自许可与例外，详见 [LICENSE_SCOPE](LICENSE_SCOPE.md) 和 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES.md)。

RustSwitch 独立开发。FreeSWITCH 名称用于兼容研究和来源说明，不代表其官方认证、合作关系或背书。

## For English-speaking contributors

English issues and pull requests are welcome. Use the linked bug, feature, or interoperability templates and identify the release or commit, source variant, environment, expected behavior, actual behavior, and a sanitized reproducer. Small fixes may go directly to a pull request; discuss significant interface or architecture changes in an issue first.

Most detailed documents remain in Chinese. [README_EN](README_EN.md) describes the current scope. Please preserve the distinction between implemented paths, separately published candidates, historical evidence, and unverified goals. Read [CONTRIBUTING](CONTRIBUTING.md) for contribution requirements and [SECURITY](SECURITY.md) for private reporting. No response SLA, support subscription, or fixed governance committee is established by this guide.
