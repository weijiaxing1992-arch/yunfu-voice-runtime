<div align="center">

# FreeSWITCH 平替 RustSwitch

**面向语音智能体的开源实时语音内核**

Rust 媒体 · Go 控制 · C/C++ 编解码生态

[中文](README.md) · [English](README_EN.md)

[![Development preview](https://img.shields.io/badge/status-development_preview-efb366?style=flat-square)](08-当前能力与验证状态.md)
[![Original code Apache-2.0](https://img.shields.io/badge/original_code-Apache--2.0-087f79?style=flat-square)](LICENSE_SCOPE.md)
[![GitHub stars](https://img.shields.io/github/stars/weijiaxing1992-arch/yunfu-voice-runtime?style=flat-square)](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/stargazers)

[**快速体验**](QUICKSTART.md) · [**下载源码与编译包**](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases/tag/v0.1.0-preview.20260908) · [**完整接口文档**](04-HTTP接口文档.md) · [**参与共建**](COMMUNITY.md)

![RustSwitch：面向语音智能体的 FreeSWITCH 替代项目，开发预览](community/assets/hero.svg)

</div>

RustSwitch 希望成为 **FreeSWITCH 在语音智能体场景下的替代选择**：让电话线路、实时音频、业务控制和 AI 服务有清楚的边界，让接口迁移、资源保护和性能验收都能逐项检查。

项目由云蝠智能发起，工程中也使用 **云蝠 Voice Runtime** 名称。我们公开当前源码、管理后台、测试工具、构建依赖、项目资料和缺口清单，欢迎一起把电话语音智能体的基础设施做好。

> **当前是开发预览版。**“平替”是产品方向，当前仅兼容 FreeSWITCH 的部分接口与场景；尚未完成 5000/10000 路完整容量验收，也未接通完整真实 ASR/TTS/Agent 链路。现有实现、候选代码和未完成能力分别列示，详见[当前状态](08-当前能力与验证状态.md)与[未完成清单](13-项目未完成清单.md)。

## 为什么做 RustSwitch

电话里的语音智能体需要接听、外呼、持续收音、实时播放、收号，以及在资源紧张时保护正在进行的通话。它还需要把媒体生命周期与 ASR、TTS、业务轮次连接起来。

RustSwitch 围绕这些需求构建：

| 关注点 | 工程中的做法 |
|---|---|
| 实时媒体 | Rust 独立媒体进程，固定工作分片、有界队列、端口池和 RTP/RTCP 处理 |
| 业务控制 | Go 管理呼叫生命周期、线路接入、准入保护、运维 API 与中文管理后台 |
| AI 音频接口 | 已有 G.711 实时音频图、PCM 下行与收音接口；ASR1 候选和 mock 单独提供 |
| 迁移可追踪 | 对照 FreeSWITCH 1.11.3 的固定源码版本，保留逐项差异与测试证据 |
| 可重复构建 | 主工程和候选完整源码、锁定依赖、离线构建脚本及编译记录随包提供 |
| 容量可验证 | 单路 SIP/媒体检查、隔离压测、采样与报告工具；性能结论以实际验收为准 |

## 现在可以做什么

| 能力 | 当前范围 |
|---|---|
| 电话线路 | 受限 SIP UDP/TCP/TLS 桥接、双呼叫腿生命周期、固定上游 REGISTER/Digest 客户端 |
| 媒体通路 | RTP/RTCP 校验与转发、协商后的 telephone-event 转发、G.711 实时图与 PCM/RX 接口 |
| IVR 与控制 | 本地播放、收号、受限 IVR、入站 ESL 和运行 XML 拨号计划子集 |
| 峰值保护 | 并发、建立中呼叫与 CPS 限制，按媒体健康状态控制新准入 |
| 管理后台 | 中文运行总览、保护策略、SIP/媒体配置、测试页面、接口资料与兼容对照 |
| 音频生态 | G.711；G.722/Opus 可选原生后端与独立验证工具。格式协商、透传、编解码和实时转码分别验收 |
| ASR 候选 | ASR1 内部合同、模拟供应商与生命周期测试；候选未合并，mock 不提供真实语音识别 |

通用 originate、多线路、完整 SIP/ESL/XML 语义、SRTP/WebRTC、真实 ASR/TTS、完整 Agent 编排和活动通话跨机接管仍有缺口。完整列表见[功能与验证状态](13-项目未完成清单.md)。

## 架构一览

![RustSwitch 架构：Go 控制面、Rust 媒体面、C 编解码适配与 AI 接口边界](community/assets/architecture.svg)

[部署拓扑](02-项目拓扑图.md) · [软件架构](03-软件架构图.md) · [协议与 SDK](05-协议与SDK文档.md)

## 管理后台预览

![RustSwitch 中文管理后台的峰值保护界面](reference/docs/verification-v0.3/protection-desktop.png)

*来自项目 v0.3 历史验证的真实截图。页面中的 8000 等数值为配置或资源预算，不代表已经通过该并发的性能验收；当前完整页面和字段以随包接口资料为准。*

## 快速体验

### 方式一：下载编译包

打开 [Releases 下载页](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases/tag/v0.1.0-preview.20260908)，下载匹配平台的运行包，解压后进入该包目录：

```sh
python3 verify.py
cd main
chmod +x run.sh bin/*
./run.sh -check-config
# 确认示例端口可用后启动
./run.sh
```

访问 **http://127.0.0.1:9080**，从「压力与电话测试」进入单路 SIP/媒体快速测试。

默认绑定本机，100 路硬上限、20 CPS；SIP 5060，上游 5070，后台 9080。真实线路需自行配置。单路测试是隔离模拟测试，不是浏览器麦克风电话。主版本与 ASR 候选默认端口相同，请分别运行。

| 下载包 | 适用环境与边界 |
|---|---|
| macOS ARM64 | macOS 26+、Apple Silicon，含可选 G.722/Opus 动态库 |
| Linux ARM64 | Linux aarch64 静态核心，未附可选 G.722/Opus 动态库；候选组合未完成整组联调 |
| 完整源码 | 主工程、ASR 候选、补丁、设计参考、固定依赖和构建脚本 |
| 完整项目资料 | 上述源码与编译包，以及可离线阅读的 `index.html`、图源、接口、需求和未完成清单 |

当前没有预编译 x86_64 包。安装要求、配置和源码构建详见[快速开始](QUICKSTART.md)。

### 方式二：从源码构建

准备 Go 1.23+、Rust/Cargo 1.85+、C11 编译器和 Python 3.9+。最低版本组合尚未全部实测；当前构建记录使用 Go 1.27.1、Rust 1.98.1。

```sh
git clone https://github.com/weijiaxing1992-arch/yunfu-voice-runtime.git
cd yunfu-voice-runtime
python3 delivery-tools/verify_delivery.py
python3 delivery-tools/build_source.py \
  --variant main --output ../rustswitch-build
```

依赖源码已提供，构建不需要在线拉取依赖；编译器须自行安装。输出目录须在仓库之外且尚不存在。构建后的运行目录组装见 [QUICKSTART](QUICKSTART.md)。文件校验、编译成功与完整功能验收是不同检查。

## 与 FreeSWITCH 的关系

RustSwitch 优先覆盖电话语音智能体常用路径，渐进兼容有实际需求的 FreeSWITCH 接口。现有 FreeSWITCH 模块不能直接以二进制插件加载；通用 PBX、会议、视频、传真和全部第三方模块没有被宣称完成。

[3999 条对照目录](backlog/FreeSWITCH-全部逐项状态.csv)混合了接口、声明、配置和验收条目，**不是兼容率的分母**。用户应按实际线路和业务场景验证：

| 迁移时检查 | 阅读入口 |
|---|---|
| 协议、命令和返回语义是否一致 | [FreeSWITCH 对照标准](source/main/docs/freeswitch-compatibility/README.md) |
| 本项目接口具体支持哪些参数 | [HTTP 接口](04-HTTP接口文档.md)、[协议与 SDK](05-协议与SDK文档.md) |
| 哪些只是配置编辑或导出 | [完整逐项状态](backlog/FreeSWITCH-全部逐项状态.json) |
| 历史通过证据是否仍适用于当前源码 | [当前验证状态](08-当前能力与验证状态.md)、[完整缺口](13-项目未完成清单.md) |

主工程当前文档门禁存在 `key-api-pcm-stream` 证据过期，ASR 候选存在字段字典与机器合同不同步。历史测试不会自动转化为当前版本的有效绿色状态。

## 路线图：围绕真实语音智能体推进

- **电话与媒体闭环**：逐项完善线路注册、呼叫生命周期、收音、播放、收号和资源回收。
- **真实 AI 对话**：接入 ASR/TTS 供应商，完善轮次、取消、超时、VAD/打断和音频正确性。
- **容量与稳定性**：在相同硬件、功能和音质下对比 FreeSWITCH，逐级验收 1000/5000/10000 路与长时间混合负载。
- **按需迁移**：补齐实际业务所需的协议与接口差异，并提供可复现的互通测试。

这些是研发目标。我们欢迎性能改进，也需要能证明改进成立的完整报告。具体任务与完成条件见 [121 条需求](12-需求说明书.md) 和 [未完成工作包](13-项目未完成清单.md)。

## 完整项目资料

| 你想了解 | 入口 |
|---|---|
| 项目定位与部署 | [项目说明](01-项目说明.md) · [拓扑图](02-项目拓扑图.md) · [架构图](03-软件架构图.md) |
| 开发接口 | [26 个 HTTP 操作](04-HTTP接口文档.md) · [协议与 SDK](05-协议与SDK文档.md) · [77 个数据模型](09-数据模型全文.md) |
| 使用和运维 | [使用说明](06-使用说明.md) · [运维与压测](07-运维与压测说明.md) |
| 交付与进度 | [交付清单](11-交付与源码说明.md) · [需求说明书](12-需求说明书.md) · [项目未完成清单](13-项目未完成清单.md) |
| 全部材料 | [离线资料目录](DELIVERY.md) · [专题目录](10-全部专题文档目录.md) · [开源说明](OPEN_SOURCE.md) |

## 代码在哪里

```text
source/main/             主工程：control/Go、media/Rust、native/C、配置与测试
source/asr-candidate/    独立 ASR 候选与 mock
source/patches/          已测补丁与另列的工作稿
source/design-reference/协议原型和设计测试
dependencies/           固定 Rust 依赖与 Opus 源码归档
delivery-tools/         离线构建、分发完整性校验
backlog/                FreeSWITCH 逐项差异与工作清单
requirements/           原始方向与需求追踪
evidence/               注明版本和范围的历史证据
```

## 一起建设 RustSwitch

如果这个方向对你有用，欢迎点一个 **Star**，把项目分享给正在做电话机器人、Voice Agent、SIP 或实时媒体的开发者。

真正推动项目进步的是具体反馈：一条能复现的问题、一份脱敏的线路互通报告、一项带基准的优化，或者一次文档修正。请从[社区参与指南](COMMUNITY.md)和[贡献说明](CONTRIBUTING.md)开始。

[报告问题](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues/new?template=bug_report.yml) · [提出功能需求](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues/new?template=feature_request.yml) · [提交互通报告](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues/new?template=compatibility_report.yml) · [提交 Pull Request](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/pulls)

## 开源许可与致谢

项目原创代码与原创资料采用 [Apache-2.0](LICENSE)。G.722 支持文件、SpanDSP、FreeSWITCH 模板、Opus 和 Go/Rust 依赖保留相应例外与上游许可，详见 [LICENSE_SCOPE](LICENSE_SCOPE.md)、[第三方清单](THIRD_PARTY_NOTICES.md)与[机器清单](third-party-inventory.json)。

感谢 FreeSWITCH、Rust、Go、SpanDSP、Opus 及各依赖项目。FreeSWITCH 名称用于兼容研究和来源说明；本项目是独立开源项目，没有获得其官方兼容认证或背书。
