# 云蝠 Voice Runtime

面向电话语音智能体的实时语音内核。采用 **Rust 媒体层 + Go 控制面 + C/C++ 编解码适配**，按业务优先级兼容 FreeSWITCH 接口。

**当前为开发预览版**：已提供源码、接口与架构资料、测试工具、历史验证证据和 ARM64 编译包。尚未达到完整 FreeSWITCH 兼容或 5000/10000 路生产验收；真实 ASR/TTS、完整 Agent 链路及多项协议能力仍在建设。当前状态详见 [未完成清单](13-项目未完成清单.md)。

[下载完整资源包](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases/tag/v0.1.0-preview.20260908) · [快速开始](QUICKSTART.md) · [开源说明](OPEN_SOURCE.md) · [第三方清单](THIRD_PARTY_NOTICES.md) · [完整资料目录](DELIVERY.md)

## 项目与代码

| 目录 | 内容与状态 |
|---|---|
| [source/main](source/main) | 当前主工程，文档基线 1.13.0；受限 SIP、ESL、IVR、Rust 媒体与中文后台 |
| [source/asr-candidate](source/asr-candidate) | 独立 ASR 候选，实现内部 ASR1 与 mock；尚未合并主工程 |
| [source/patches](source/patches) | 已测试的 benchmark-tested-02 和另列的未完全验证工作稿 |
| [source/design-reference](source/design-reference) | ASR1 离线协议原型与设计测试；不等于正式服务 |
| [dependencies](dependencies) | 固定版本的 Rust 依赖源码及 Opus 源码归档；Go vendor 随工程提供 |
| [delivery-tools](delivery-tools) | 离线构建与文件完整性校验工具 |
| [backlog](backlog) | 3999 条 FreeSWITCH 对照、状态与未完成工作包 |
| [requirements](requirements) | 项目方向原文与 121 条需求追踪 |
| [evidence](evidence) | 经公开信息处理的历史构建、测试和审查记录 |
| [binaries](binaries) | 运行包说明及来源；可执行文件从 Releases 下载 |

仓库保留两套完整源码，便于复核版本边界；两者不能混合启动。后续开发优先围绕主工程进行，候选与补丁通过独立评审后再合入。

## 项目资料

| 资料 | 入口 |
|---|---|
| 项目定位、功能与边界 | [01 项目说明](01-项目说明.md) |
| 部署拓扑与软件架构 | [02 拓扑图](02-项目拓扑图.md)、[03 架构图](03-软件架构图.md)、[可编辑图源](figures) |
| API 与协议 | [04 HTTP 接口](04-HTTP接口文档.md)、[05 协议与 SDK](05-协议与SDK文档.md)、[09 数据模型](09-数据模型全文.md) |
| 使用与运维 | [06 使用说明](06-使用说明.md)、[07 运维与压测](07-运维与压测说明.md) |
| 验证与专题 | [08 当前状态](08-当前能力与验证状态.md)、[10 专题目录](10-全部专题文档目录.md) |
| 交付、需求与剩余工作 | [11 交付说明](11-交付与源码说明.md)、[12 需求说明书](12-需求说明书.md)、[13 未完成清单](13-项目未完成清单.md) |

下载完整资料包后可直接打开 `index.html` 离线阅读。GitHub 上可直接阅读 Markdown。

## 下载与验证

[首个公开开发预览版](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases/tag/v0.1.0-preview.20260908) 提供：完整源码、完整项目资料与资源包、macOS ARM64 运行包、Linux ARM64 运行包，以及 SHA-256 清单。

macOS 整包要求 macOS 26+ ARM64；Linux 包为 ARM64 静态核心程序，未包含可选 G.722/Opus Linux 动态库。Linux ASR 候选整组尚未联调。本次没有 x86_64 编译包，其他平台请从源码构建。

```sh
git clone https://github.com/weijiaxing1992-arch/yunfu-voice-runtime.git
cd yunfu-voice-runtime
python3 delivery-tools/verify_delivery.py
python3 delivery-tools/build_source.py --variant main --output ../voice-runtime-build
```

构建前须自行安装 Go、Rust/Cargo、C 编译器与 Python 3.9+；依赖已随源码提供。启动步骤与工具链要求见 [快速开始](QUICKSTART.md)。仅做文件校验不会运行网络服务。

## 验收状态

发布源码与编译包不改变工程验收结果。历史快照的“本地通过”不是当前有效兼容率，测试用例数量不是业务能力数量。

- 主工程文档门禁存在 `key-api-pcm-stream` 绿色证据过期问题。
- ASR 候选存在字段字典落后于机器合同的问题。
- 真实供应商 ASR/TTS、跨机接管、SRTP/WebRTC 与完整 SIP/ESL/XML 语义仍有缺口。
- 高并发收益、5000/10000 路、72 小时混合负载和完整语音智能体闭环仍需目标 Linux 环境验收。

首版公开标签 `v0.1.0-preview.20260908` 是公开分发编号；1.13.0 是资料基线，不能据此推断所有组件的软件版本。

## 开源与贡献

项目原有代码与原创资料按 [Apache-2.0](LICENSE) 开源。FreeSWITCH 模板、SpanDSP、Opus、Go/Rust 依赖及上游材料继续适用其原有许可，详见 [开源范围](OPEN_SOURCE.md) 与 [第三方声明](THIRD_PARTY_NOTICES.md)。本项目与 FreeSWITCH 上游不存在官方背书关系。

欢迎按 [贡献说明](CONTRIBUTING.md) 提交修复、复现用例与真实互通证据。提交问题时请附配置的脱敏副本、版本和最小复现；安全问题使用 [安全报告说明](SECURITY.md)。
