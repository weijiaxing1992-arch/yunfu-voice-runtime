<div align="center">

# FreeSWITCH 平替 RustSwitch

**面向语音智能体的开源实时语音内核 · FreeSWITCH / PBX 迁移研究**

Rust 媒体 · Go 控制 · C/C++ 编解码生态

[中文](README.md) · [English](README_EN.md)

[![Development preview](https://img.shields.io/badge/status-development_preview-efb366?style=flat-square)](08-当前能力与验证状态.md)
[![Original code Apache-2.0](https://img.shields.io/badge/original_code-Apache--2.0-087f79?style=flat-square)](LICENSE_SCOPE.md)
[![GitHub stars](https://img.shields.io/github/stars/weijiaxing1992-arch/yunfu-voice-runtime?style=flat-square)](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/stargazers)

[**快速开始**](QUICKSTART.md) · [**产品使用说明书**](14-产品使用说明书.md) · [**下载预览版**](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases/tag/v0.1.0-preview.20260908) · [**文档中心**](DOCUMENTATION.md) · [**接口文档**](04-HTTP接口文档.md) · [**FreeSWITCH / PBX 专题**](community/freeswitch-pbx-guide.md) · [**参与贡献**](CONTRIBUTING.md)

![RustSwitch：面向语音智能体的 FreeSWITCH 替代项目，开发预览](community/assets/hero.svg)

</div>

RustSwitch 是面向电话语音智能体的实时语音基础设施项目，工程和交付材料中也使用 **云蝠 Voice Runtime** 名称。项目采用 Rust 媒体内核、Go 呼叫控制和 C/C++ 原生适配层，围绕电话接入、持续收音、实时播放、按键交互、资源保护和可观测性构建。

项目希望成为 **FreeSWITCH 在语音智能体场景下的替代选择**，优先兼容实际迁移所需的接口与协议行为。仓库公开主工程、独立 ASR 候选、测试工具、固定依赖、完整项目资料和未完成清单；源码可以直接查看，编译程序与资源包通过 GitHub Releases 分发。

> **当前为开发预览。**“平替”是产品方向，当前并非可直接替换所有 FreeSWITCH 部署的完整实现。项目尚未完成 5000/10000 路完整容量验收，也未完成真实 ASR–LLM–TTS 业务闭环。已有实现、限定验证和研发目标分别列示，详见[能力与验证状态](08-当前能力与验证状态.md)。

## FreeSWITCH、PBX 与 SIP 电话系统

如果你正在寻找 **FreeSWITCH 替代方案、开源 PBX、IP PBX、SIP 媒体服务器或 AI 电话接入内核**，可以从这里了解 RustSwitch 的适用范围。PBX（Private Branch Exchange，专用交换系统）常涉及分机、线路、拨号计划、自动总机、队列、录音和转接；本项目优先建设其中服务语音智能体的电话与媒体路径，完整 PBX 能力仍按[功能矩阵](community/pbx-feature-matrix.md)追踪。

| 你正在评估的场景 | RustSwitch 当前可评估内容 | 深入阅读 |
| --- | --- | --- |
| FreeSWITCH alternative / migration | 固定版本下的接口差异与迁移验收；部分入口实现 | [FreeSWITCH 迁移指南](community/freeswitch-migration.md) |
| 开源 PBX / IP PBX / 软交换 | 呼叫控制与媒体基础；分机注册服务、会议、队列、语音信箱等仍有缺口 | [PBX 功能与模块矩阵](community/pbx-feature-matrix.md) |
| SIP Trunk / 线路注册 | 单固定上游 REGISTER / Digest 客户端；受限 SIP UDP/TCP/TLS | [线路注册合同](reference/docs/api/trunk-registration.md) |
| 呼入 / 呼出 / Call control | 双呼叫腿桥接、有限本地接听与释放；通用 `originate` 待实现 | [AI 呼叫接入](reference/docs/api/ai-calling-reference.md) |
| IVR / 自动总机 / DTMF | 本地 A 腿播放、收号、telephone-event 与有限 IVR | [IVR 接口](reference/docs/api/ivr-reference.md) |
| ESL / Event Socket / fs_cli | 入站命令与事件子集；需逐条核对返回、顺序及应用完成语义 | [ESL 参考](reference/docs/api/esl-reference.md) |
| XML Dialplan / 拨号计划 | 配置编辑导出与有限运行拨号计划分别提供 | [拨号计划合同](reference/docs/api/dialplan-reference.md) |
| RTP / RTCP / G.711 / PCM | 媒体转发、G.711 实时图、PCM 下行与 RX 收音 | [协议与音频 SDK](05-协议与SDK文档.md) |
| G.722 / Opus / 音频转码 | 可选离线后端与部分协商转发；完整实时编解码路径待验收 | [编码与音频范围](community/pbx-feature-matrix.md) |
| Voice Agent / 语音机器人 | 音频轮次、播放中断、收音与独立 ASR 候选；真实模型闭环待接入 | [AI 媒体架构](03-软件架构图.md) |
| 呼叫中心 / Contact center | 可研究电话媒体组件；ACD 排队、坐席分配、录音等业务须分别建设 | [PBX 选型与常见问题](community/freeswitch-pbx-guide.md) |
| 并发呼叫 / CPS / 压力测试 | 峰值保护、单路小电话、隔离负载与失败报告；容量按实际结果验证 | [运维与压测](07-运维与压测说明.md) |

**English:** RustSwitch is an open-source Rust/Go voice runtime pursuing a FreeSWITCH alternative for SIP telephony and Voice Agents. Explore PBX / IP PBX migration, SIP trunks, ESL event socket, XML dialplan, IVR, DTMF, RTP/RTCP and real-time PCM in the [English PBX guide](community/freeswitch-pbx-guide.en.md). This development preview offers subsets and integration foundations; full PBX replacement and production-scale capacity remain unverified.

### FreeSWITCH 与 PBX 常见问题

- **能直接替换现有 FreeSWITCH 吗？** 先盘点实际使用的线路、命令、事件、XML、模块和媒体能力，再完成逐项验收。当前不能承诺任意部署无感替换，步骤见[迁移指南](community/freeswitch-migration.md)。
- **分机可以向 RustSwitch 注册吗？** 已有能力是向固定上游注册的客户端；完整终端注册服务器及多网关仍待实现，不能将这两类注册混用。
- **可以沿用 ESL、fs_cli 和 XML 吗？** 入站 ESL 和运行 XML 拨号计划仅支持明确列出的子集；编辑或导出 XML 不代表相关模块已经执行。
- **是否包含会议、呼叫队列、语音信箱、传真和 WebRTC？** 这些是迁移评估的重要依赖，当前均不能按完整已交付模块使用，详见[模块矩阵](community/pbx-feature-matrix.md)。
- **能否承载 ASR、TTS 和实时对话？** 主工程提供媒体接口基础，ASR1 候选用于流协议和生命周期验证；真实识别、合成、VAD 与 Agent 编排仍需接入和验收。
- **支持多少并发呼叫？** 5,000 / 10,000 路是阶段验收目标。转发、实际编解码和完整 Agent 负载分别测试，不能用配置上限代替实测容量。

## 项目与版本

| 标识 | 含义 |
|---|---|
| RustSwitch / 云蝠 Voice Runtime | 本项目名称；仓库名为 `yunfu-voice-runtime` |
| [`v0.1.0-preview.20260908`](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases/tag/v0.1.0-preview.20260908) | 已公开的首批开发预览交付；源码和运行包以各自校验清单为准 |
| `source/main/` | 主工程源码目录；不是同名 Git 分支的别称 |
| `source/asr-candidate/` | 独立 ASR 候选源码，已随预览公开，尚未合并到主工程 |
| `1.13.0` | 主工程文档基线；与 Release 标签、Go 程序 `0.3.0` 和 Rust crate `0.2.0` 分别管理 |
| FreeSWITCH `1.11.3` | 固定兼容研究基线；不表示当前上游最新版本或官方认证 |

后续文档修改和交付变化见 [CHANGELOG](CHANGELOG.md)。复现问题时请提供 Release 标签或 Git 提交，并注明使用主工程还是候选；不要仅凭页面版本号判断整套源码和运行程序是否一致。

## 适合哪些工作

- **语音智能体开发**：研究电话接入与实时音频接口，逐步接入自己的 ASR、TTS 和业务编排。
- **FreeSWITCH 迁移评估**：围绕真实线路、命令、事件及媒体场景进行逐项对照与互通验证。
- **媒体与系统开发**：改进分片媒体处理、资源回收、过载控制和压力测试的可重复性。
- **运维与测试**：在受控环境使用中文后台、单路测试、隔离压测和日志工具核验行为。

首次评估建议从主工程的单路 SIP/媒体模拟测试开始。当前实现面向受控开发和验收环境，部署边界与安全报告方式见 [SECURITY](SECURITY.md)。

## 核心设计

| 关注点 | 实现方向 |
|---|---|
| 媒体与控制分离 | Rust 独立媒体进程处理 RTP 与媒体状态；Go 管理呼叫、准入和进程生命周期 |
| 显式资源预算 | 固定工作分片、有界队列、端口池以及并发、建立中呼叫和 CPS 上限 |
| 面向 Agent 的音频边界 | G.711 实时音频图、PCM 下行、RX 收音及会话、代次和轮次约束 |
| 原生生态复用 | 通过版本化 C ABI 接入编解码库，并区分格式协商、透传、编解码与实时处理能力 |
| 可观测与可验收 | 管理 API、状态采样、故障记录和隔离压测；结论绑定源码、环境和实际负载 |
| 渐进兼容 | 固定 FreeSWITCH 参考版本，保留差异、未实现项和失败证据 |

高并发、低资源消耗和稳定长通话是研发目标。当前没有完整同条件测试证明项目在容量或资源使用上优于 FreeSWITCH。

## 当前能力与边界

| 领域 | 已提供的范围 | 仍需完成的部分 |
|---|---|---|
| 电话接入 | 受限 IPv4 SIP UDP/TCP/TLS 桥接、双呼叫腿生命周期、固定上游 REGISTER/Digest 客户端 | 通用 originate、多线路、完整注册服务与更多 SIP 对话语义 |
| 媒体传输 | RTP/RTCP 校验与转发、协商后的 telephone-event 转发 | SRTP、WebRTC/ICE、完整终结型 RTCP 等 |
| 音频与交互 | G.711 实时图、本地播放与收号、PCM 下行/中断、RX 收音、受限 IVR | 完整 IVR 语义、全部实时音频路径与双轨录音 |
| 编解码生态 | G.711；可选 G.722/Opus 原生后端及独立验证工具 | 每条实时路径的编解码覆盖与音频质量验收；G.729/AMR/EVS 等扩展 |
| 控制接口 | 自有 HTTP API、入站 ESL 和运行 XML 拨号计划子集 | 完整 FreeSWITCH 命令、事件、拨号计划与插件语义 |
| 运行与管理 | 中文后台、峰值保护、媒体进程监管、日志、单路测试与压测工具 | 更完整的认证授权、可靠统计和活动通话跨机接管 |
| ASR 候选 | 独立 ASR1 内部合同、mock 供应商、生命周期与音频来源检查 | 候选合并、真实识别模型、TTS/LLM 集成及完整 Agent 验收 |

ASR mock 不执行真实语音识别。G.722/Opus 后端存在不表示所有实时路径已经支持对应编解码。逐项完成条件见[项目未完成清单](13-项目未完成清单.md)。

## 架构一览

![RustSwitch 架构：Go 控制面、Rust 媒体面、C 编解码适配与 AI 接口边界](community/assets/architecture.svg)

实线表示主线已有路径，虚线表示候选或待接入能力；连线不表示全部语义已经验收。图源与详细说明见[部署拓扑](02-项目拓扑图.md)、[软件架构](03-软件架构图.md)及[协议与 SDK](05-协议与SDK文档.md)。

## 管理后台

![RustSwitch 中文管理后台的峰值保护界面](reference/docs/verification-v0.3/protection-desktop.png)

*此图来自项目 v0.3 历史验证。8000 等数值为配置或资源预算，不代表通过对应并发验收；当前字段与行为以随包接口资料为准。*

管理页面内嵌在 Go 程序中，不需要独立前端服务。页面覆盖运行总览、峰值保护、SIP/媒体配置、测试、接口文档和 FreeSWITCH 对照等工作。

## 快速开始

### 使用预编译运行包

从 [预览版 Releases](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases/tag/v0.1.0-preview.20260908) 下载匹配平台的运行包，解压后在包的根目录执行：

```sh
python3 verify.py
cd main
chmod +x run.sh bin/*
./run.sh -check-config
# 配置通过且示例端口空闲后启动
./run.sh
```

打开[本机管理后台](http://127.0.0.1:9080)，从「压力与电话测试」开始单路隔离 SIP/媒体模拟测试。该测试不使用浏览器麦克风，默认也没有真实 SIP 线路。

| 运行包 | 环境与注意事项 |
|---|---|
| macOS ARM64 | Apple Silicon、macOS 26+；包含可选 G.722/Opus 动态库，开发程序未做 Developer ID 公证 |
| Linux ARM64 | Linux aarch64 静态核心；未附可选 G.722/Opus 动态库，ASR 候选组合尚未完成整组联调 |

当前没有预编译 x86_64 包。默认配置绑定本机，SIP `5060`、固定上游 `5070`、后台 `9080`，100 路并发上限、20 CPS。主工程与候选使用相同示例端口，应分别运行；这些默认值不是容量验收结果。完整步骤见 [QUICKSTART](QUICKSTART.md)。

### 从源码构建

准备 Python 3.9+、Go 1.23+、Rust/Cargo 1.85+ 和 C11 编译环境。最低版本来自源码声明，尚未逐个组合实测；当前构建记录使用 Go 1.27.1、Rust 1.98.1。

```sh
git clone https://github.com/weijiaxing1992-arch/yunfu-voice-runtime.git
cd yunfu-voice-runtime
python3 delivery-tools/verify_delivery.py
python3 delivery-tools/build_source.py \
  --variant main --output ../rustswitch-build
```

固定依赖源码已提供，交付构建无需在线拉取依赖；编译器须自行安装。输出目录必须位于仓库之外且尚不存在。该脚本核对源码快照清单，仅用于未修改的交付源码；修改源文件后会拒绝构建，应使用源码自身的 Go/Cargo/Makefile 开发入口，并如实记录文档门禁阻塞。修改文件后对应快照校验清单不再匹配属于预期现象，不应修改历史清单来绕过检查。运行目录组装见[快速开始](QUICKSTART.md)，开发流程见 [CONTRIBUTING](CONTRIBUTING.md)。

## FreeSWITCH 兼容与验收

RustSwitch 优先覆盖电话语音智能体常用路径，渐进兼容有实际需求的 FreeSWITCH 接口。现有 FreeSWITCH 模块不能直接二进制加载；通用 PBX、会议、视频、传真和全部第三方模块不属于本预览已完成的范围。

[3999 条对照记录](backlog/FreeSWITCH-全部逐项状态.csv)包括接口、声明、配置和验收条目，**不能直接作为兼容率分母**。判断迁移是否可行，需要同时核对：

| 检查内容 | 依据 |
|---|---|
| 需要的协议、命令和返回语义 | [固定版本对照标准](source/main/docs/freeswitch-compatibility/README.md) |
| 接口、配置及错误行为 | [HTTP 接口](04-HTTP接口文档.md) · [协议与 SDK](05-协议与SDK文档.md) |
| 实现状态与配置导出的区别 | [逐项机器状态](backlog/FreeSWITCH-全部逐项状态.json) |
| 测试覆盖与当前源码的对应关系 | [验证状态](08-当前能力与验证状态.md) · [未完成清单](13-项目未完成清单.md) |

当前主工程文档门禁记录 `key-api-pcm-stream` 证据过期，候选记录字段字典与机器合同不同步；它们是已遇到的阻塞，并不表示只有这两项待办。文件完整性、编译、功能测试、成对互通和容量验收属于不同层级。历史通过数量不会自动变成当前有效绿色状态，详见[文档与证据使用规则](DOCUMENTATION.md)。

## 开发重点

1. 修复交付门禁和统计一致性，更新与具体源码绑定的测试证据。
2. 完善线路注册、呼叫、收音、播放、按键和资源回收，覆盖正常及失败路径。
3. 接入真实 ASR/TTS，完善轮次、取消、超时、VAD/打断和音频正确性。
4. 按实际业务补齐 SIP、ESL、XML 和编解码差异，增加可复现的成对互通测试。
5. 在同硬件、同功能、同音质条件下与 FreeSWITCH 对照，逐级验收 1000/5000/10000 路和长时间混合负载。

上述为研发方向，没有承诺发布日期或已达成性能收益。需求优先级和完成条件见[需求说明书](12-需求说明书.md)及[未完成工作包](13-项目未完成清单.md)。

## 项目资料与源码

| 读者 | 建议入口 |
|---|---|
| 首次评估 | [产品使用说明书](14-产品使用说明书.md) · [快速开始](QUICKSTART.md) · [项目说明](01-项目说明.md) · [当前状态](08-当前能力与验证状态.md) |
| 开发与集成 | [架构](03-软件架构图.md) · [26 个 HTTP 操作](04-HTTP接口文档.md) · [77 个数据模型](09-数据模型全文.md) · [协议与 SDK](05-协议与SDK文档.md) |
| 运维与测试 | [使用说明](06-使用说明.md) · [运维与压测](07-运维与压测说明.md) |
| 项目交接 | [交付说明](11-交付与源码说明.md) · [需求](12-需求说明书.md) · [未完成清单](13-项目未完成清单.md) |
| 开源与社区 | [开源说明](OPEN_SOURCE.md) · [许可范围](LICENSE_SCOPE.md) · [社区指南](COMMUNITY.md) · [变更记录](CHANGELOG.md) |

[文档中心](DOCUMENTATION.md)提供完整阅读导航。完整资料包还包含可离线打开的 `index.html`、图源和专题资料，参见[交付目录](DELIVERY.md)。

```text
source/main/              主工程：Go 控制、Rust 媒体、C 适配、配置和测试
source/asr-candidate/     独立 ASR 候选与 mock
source/patches/           已测补丁与另列的工作稿
source/design-reference/ 协议原型和设计测试
dependencies/            固定 Rust 依赖与 Opus 源码归档
delivery-tools/          离线构建与分发完整性校验
backlog/                 FreeSWITCH 逐项差异和工作清单
requirements/            项目方向与需求追踪
evidence/                注明版本和范围的历史证据
```

## 参与社区

欢迎提交能复现的缺陷、脱敏互通报告、带完整条件的基准结果，以及代码与文档改进。小范围修正可以直接提交 Pull Request；较大接口或架构变化先通过 Issue 讨论范围和验收方法。参与流程见[社区指南](COMMUNITY.md)和[贡献说明](CONTRIBUTING.md)。

[报告缺陷](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues/new?template=bug_report.yml) · [提出需求](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues/new?template=feature_request.yml) · [提交互通报告](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues/new?template=compatibility_report.yml) · [安全报告](SECURITY.md)

如果项目对你有帮助，欢迎 **Star** 或分享给电话系统和 Voice Agent 开发者。具体场景、可复现的结果与持续贡献同样能帮助项目成长。

## 许可与致谢

项目有权授权的原创代码与原创资料采用 [Apache-2.0](LICENSE)。G.722 支持文件、SpanDSP、FreeSWITCH 模板、Opus 和 Go/Rust 依赖保留相应例外与上游许可。再分发前请查阅 [LICENSE_SCOPE](LICENSE_SCOPE.md)、[第三方声明](THIRD_PARTY_NOTICES.md)及[机器清单](third-party-inventory.json)。

感谢 FreeSWITCH、Rust、Go、SpanDSP、Opus 及各依赖项目。FreeSWITCH 名称用于兼容研究和来源说明；RustSwitch 为独立项目，不代表 FreeSWITCH 官方发行版、兼容认证或背书。
