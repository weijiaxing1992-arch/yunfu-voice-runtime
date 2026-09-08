# FreeSWITCH 无感替换接口标准

自文档1.9起，产品首期以[云蝠 Voice Runtime电话智能体闭环](../api/voice-runtime-direction.md)为研发范围。本标准继续作为完整兼容差异与后续迁移依据；未实现项保留，首期不要求通用PBX、视频、传真或原模块宿主全部完成。完整契约认证条件不因产品范围变化而降低。

**标准编号：RS-FS-COMPAT-1.0-draft · 2026-09-05**  
**目标参考：FreeSWITCH v1.11.3，提交 `ef32e205295e29f034f1453ad245ba5efb07b94a`。**

用户要求兼容最新版本；编写时官方最新发布指向 v1.11.3。本标准固定该提交，后续发布不会自动改变基线。[官方版本](https://github.com/signalwire/freeswitch/releases/tag/v1.11.3) · [固定提交](https://github.com/signalwire/freeswitch/commit/ef32e205295e29f034f1453ad245ba5efb07b94a)

目标是：**现有 FreeSWITCH 客户端、ESL SDK、XML 配置、拨号计划、脚本、HTTP 后端、终端及业务系统保持原样；替换服务后，观察到的协议、事件、业务结果和运维行为保持等价。** 使用中的第三方模块也必须纳入验收，不能因为难以兼容而从范围中删除。

这是一份实现与验收标准，附可追溯的接口目录。**当前应用版本 RustSwitch 0.3、文档发行版本 1.5 仍未取得此认证，本文不表示已经实现 100% 兼容。** 所有未采集的运行语义、模块依赖和实测证据都明确保持未验证状态。编写标准不能代替在原版与候选版本上的差分验收。

正文定义了169个验收场景及8类独立容量档案，需按接口、参数和状态继续展开执行。目录中的完整场景保持未运行，不作为当前代码通过记录。本轮独立成对报告包含26项限定探针，其中21项通过、3项行为差异、2项身份/入口发现；这些子场景证据不自动认证上述完整场景或当前全部对照条目。可连续阅读 [合订版](FULL-STANDARD.md)，或按下面章节查看。

## 阅读入口

| 文档 | 用途 |
|---|---|
| [01 范围、基线与统一契约](01-scope-and-contract.md) | 定义“100%”分母、不改客户端的条件、响应与状态等价规则 |
| [02 ESL 与事件](02-esl-events.md) | TCP 帧、入站/出站、鉴权、api/bgapi/sendmsg、订阅与事件语义 |
| [03 配置、拨号计划与业务集成](03-config-dialplan-integrations.md) | XML、XML Curl、变量、脚本、CDR、HTTAPI 等 |
| [04 SIP、媒体与原生模块](04-sip-media-native.md) | Sofia/SDP/RTP、转码/会议/录音、模块 ABI 与切换边界 |
| [05 API 行为契约](05-api-contracts.md) | 核心命令的参数、状态、副作用、错误、事件和回归要求 |
| [06 验收与切换](06-conformance-and-cutover.md) | 差分方法、证据格式、准入门槛、排空切换与回滚 |
| [07 当前差距与实现顺序](07-current-gap-and-roadmap.md) | 对照当前源码，不把 RustSwitch 自有接口误算成 FreeSWITCH 兼容 |
| [API/应用宏注册目录](catalog/api-and-applications.md) | 按模块列出所扫描注册点、参数表达式及精确源码行 |
| [Conference/Sofia 子命令](catalog/conference-and-sofia-subcommands.md) | 84 条会议表声明及 Sofia 已核对解析路径，避免只统计顶层 API |
| [用例索引](catalog/conformance-cases.csv) | 汇总待执行场景，区分功能用例与容量档案；不是已运行成绩 |
| [机器目录](catalog/source-catalog.json) | 模块、事件、变量、原生函数与示例配置参数 |
| [基线模板](templates/baseline-profile.json) | 版本已固定；构建、模块、配置与依赖待现场采集 |
| [单接口契约模板](templates/interface-contract.json) | 每个接口统一记录输入、输出、状态、异常、测试与证据 |

## 已核实的参考目录

静态目录构建时下载并逐文件核对 Git blob 与 SHA256，共 712 个固定提交参考文件：`src/mod` 的 C/C++/头文件、公共头、核心 C、vanilla XML 配置和 ESL 入口。完整校验记录在 [来源清单](catalog/reference-source-manifest.json)。本轮已另行编译并启动固定版 FreeSWITCH，在隔离参考环境执行真实成对探针和原版 fs_cli 客户端检查；结果与范围见 [成对报告](../api/conformance-report.md) 和 [1.5 交付验证说明](../api/release-validation-v1.5.md)。

| 目录维度 | 条目数 | 数字的含义 |
|---|---:|---|
| API 注册点 | 292 | 包含不同模块及条件编译分支 |
| 拨号计划应用注册点 | 222 | 包含重名实现，不能当作唯一运行接口数 |
| JSON API / Chat 应用注册点 | 10 / 14 | 不是 REST 路由数量 |
| 模块源码目录 | 144 | 不代表目标环境全部编译或加载 |
| 事件枚举标识 | 94 | 含 `ALL` 哨兵及内部标识，不保证全部对外产生 |
| 公共头中的变量名称宏 | 100 | 不穷尽运行期变量、模块私有变量或用户变量 |
| 公共 C 函数声明 | 1865 | 1860 个不同名称；不含宏生成/普通 extern/C++ 方法等，不能替代完整符号及 ABI 清单 |
| 未被注释的 vanilla `<param>` 出现次数 | 875 | 不代表运行期生效；同名可位于不同 profile/binding，不能合并成平面配置 |

注册目录有 17 处空帮助、动态名称/语法或外部宏需要人工解释；原始表达式原样保留。这些登记信息用于防遗漏，**接口的状态变化、子命令、错误正文、动态注册、第三方扩展与 ABI 仍必须按运行基线补齐**。机器目录里没有把任何声明标成兼容通过。

## “无感”的验收结果必须具体

1. **使用兼容：**不改原客户端、配置和脚本，同样输入得到等价输出、副作用与事件。默认选用这一严格目标。
2. **切换过程：**已有通话、注册、订阅、长连接、后台作业和持久化数据分别定义连续性；常规排空滚动切换与在线迁移是独立能力。
3. **容量：**相同功能组合和流量下达到既定 SLO。G.711 透明转发通过不能代表万路转码、录音或会议通过。

本轮隔离参考构建的版本、模块和运行证据已采集；用户现网服务器的构建选项、全部已加载模块、第三方模块、脚本包、配置和依赖仍未完整给出，全部兼容合同也未通过，因此当前发布状态为 **DRAFT / NOT CERTIFIED**。用户不必先补齐这些信息才能阅读和评审标准，但这些信息不完整时不得宣布无感替换验收完成。

附 [CSV 注册表](catalog/api-and-applications.csv)、[模块](catalog/modules.csv)、[事件](catalog/events.csv)、[变量宏](catalog/channel-variable-macros.csv)、[原生函数](catalog/native-functions.csv)、[配置参数](catalog/vanilla-parameters.csv)。
