# FreeSWITCH 迁移到 RustSwitch：评估、互通与灰度指南

更新日期：2026-09-09。适用于评估 FreeSWITCH、SIP PBX、呼叫中心与 Voice Agent 的开发和运维团队。RustSwitch 是面向语音智能体的实时语音内核，历史资料也使用“云蝠 Voice Runtime”名称；目前为开发预览，不能直接视为完整 FreeSWITCH 或 PBX 的替换安装包。

本指南采用 FreeSWITCH **1.11.3 / `ef32e205295e29f034f1453ad245ba5efb07b94a`** 作为项目固定参考，主工程位于 [`source/main/`](../source/main/)，ASR 候选位于独立的 [`source/asr-candidate/`](../source/asr-candidate/)。在线官方手册用于解释术语，查阅日期为 2026-09-09；它会更新，不能替代固定版本、已加载模块和真实线路的成对验收。

## 先判断哪些业务适合进入试验

FreeSWITCH 将终端、通道和桥接组织成呼叫，并通过目录、配置与拨号计划提供业务行为。迁移应以一次完整的业务旅程为单位，不能只比较一个同名 API 是否返回成功。[FreeSWITCH 官方核心概念](https://developer.signalwire.com/freeswitch/foundations/introduction/)

| 现有业务 | 当前建议 | 判定依据 |
| --- | --- | --- |
| 固定可信 SIP 中继、G.711 呼入、简单播放和按键收集 | 在隔离环境评估受限功能试验 | 已有 SIP、G.711、本地 A 腿、播放及 `read` 合同；仍需真实对端验证 |
| 既有 FreeSWITCH 承担复杂 PBX，新增一条 AI 媒体研发线路 | 保留原业务控制，评估独立 SIP 互通 | RustSwitch 可作为限定的 SIP 音频端点；完整真实 ASR/LLM/TTS 对话链路仍待实现 |
| 分机注册、呼叫队列、会议、语音信箱、任意 Lua 脚本 | 继续由已有系统承担 | 当前没有对应的完整运行实现 |
| 已有 ESL 应用希望只改服务器地址 | 先逐命令、逐事件核对 | 入站 ESL 只有有限子集；出站 ESL、完整 `originate` 等仍缺失 |
| 浏览器软电话、WebRTC、SRTP、复杂 SIP 转接 | 不作为当前迁移验收入口 | 当前 SDP 是单路 IPv4 `RTP/AVP` 音频；相关扩展尚未实现 |
| 直接替换生产中全部 FreeSWITCH 实例 | 当前不具备依据 | 未完成全功能成对测试、容量及活动通话故障恢复验收 |

具体范围见 [45 项 PBX 功能矩阵](pbx-feature-matrix.md)、[当前能力与验证状态](../08-当前能力与验证状态.md)和[项目未完成清单](../13-项目未完成清单.md)。矩阵用于识别迁移依赖，不为机器对照目录授予新的通过状态。

## 1. 固定环境和证据基线

为现网、隔离 FreeSWITCH 参考端、RustSwitch 候选端分别建立一份环境记录。至少保存：

1. 版本、构建提交、产物 SHA-256、CPU 架构、操作系统、内核与工具链。
2. 实际已加载模块及模块配置；“源码目录有模块”和“运行中已加载”分别记录。
3. SIP 传输、监听和发布地址、SBC/网关拓扑、可信网段、媒体端口及防火墙规则。
4. 呼叫脚本、音频样本、业务时长、并发与呼叫建立速率、编解码和包时长。
5. 管理接口、ESL、测试端点的访问边界及凭证引用方式。

配置、抓包和日志可能包含电话号码、认证信息与音频内容。对外提交可复现问题时使用测试身份并删除凭据；私有原始证据与公开脱敏副本分别保存。不要在公开 Issue 中粘贴生产注册口令或完整通话录音。

FreeSWITCH 的模块装载决定可用接口，Sofia profile 也包含独立的传输、认证及媒体配置；这些都要进入环境清单。[官方模块管理](https://developer.signalwire.com/freeswitch/configuration/module-loading/)、[Sofia SIP profiles](https://developer.signalwire.com/freeswitch/users-and-endpoints/sip-profiles/)

## 2. 盘点实际使用的接口和业务依赖

为每个业务流程填写下表，并给出正常输入、错误输入、取消路径和预期输出。只检索源代码中的字符串还不够：动态脚本、配置展开和外部控制程序也可能生成命令。

| 盘点域 | 需要收集的内容 | RustSwitch 核对入口 |
| --- | --- | --- |
| 模块与宿主 | endpoint、codec、application、API、数据库、脚本模块及加载顺序 | [完整原版逐项目录](../backlog/FreeSWITCH-全部逐项状态.json)、[模块差异](pbx-feature-matrix.md) |
| API / ESL | 入站或出站、命令参数、`api`/`bgapi`、`sendmsg`、错误正文、超时与重连 | [ESL 合同](../source/main/docs/api/esl-reference.md) |
| 通道变量 | 谁写谁读、A/B 腿、缺失值、展开、继承、写入副作用 | [变量合同](../source/main/docs/api/variables-reference.md) |
| 事件 | 名称、字段、先后关系、Job/Application UUID、订阅与过滤、断线后的补偿 | [事件生命周期](../source/main/docs/api/event-lifecycle-reference.md) |
| XML 与路由 | context、extension、condition、action、include、预处理、动态获取、重载 | [受限 XML 拨号计划](../source/main/docs/api/dialplan-reference.md) |
| 线路和号码 | gateway、REGISTER、Digest、入出方向、AOR、主被叫身份、转接和失败原因 | [中继注册](../source/main/docs/api/trunk-registration.md)、[SIP 合同](../source/main/docs/api/protocol-reference.md) |
| 音频与交互 | 两腿 codec、PT、ptime、RTP 时钟、DTMF、早期媒体、录音、保持和混音 | [音频合同](../source/main/docs/api/audio-reference.md)、[IVR 合同](../source/main/docs/api/ivr-reference.md) |
| AI 和外围 | ASR/TTS 供应商、流格式、轮次、打断、晚到结果、业务转人工、CDR/计费 | [RX 接口](../source/main/docs/api/rx-stream-reference.md)、[PCM 轮次](../source/main/docs/api/pcm-turn-reference.md)、[ASR 候选](../candidate/asr-stream-reference.md) |

FreeSWITCH 入站/出站 Event Socket、通道变量展开和 XML 拨号计划是不同的兼容面。不能把 HTTP 管理接口、变量字典或配置导出映射成这些接口的完整替代。[官方 Event Socket](https://developer.signalwire.com/freeswitch/integration/event-socket/)、[通道变量](https://developer.signalwire.com/freeswitch/reference/channel-variables/)、[XML 拨号计划](https://developer.signalwire.com/freeswitch/dialplan/xml/)

每项依赖建议记录 `业务编号 / 原版入口 / 参数与前置状态 / RustSwitch 合同 / 缺口 / 测试编号 / 证据 / 负责人`。评估结论使用“可试验、需适配、必须保留原系统、暂不纳入”四种业务决定；这些决定不替代实现状态与验证结果。

## 3. 选择一条清晰的接入路径

### 路径 A：保留 PBX，通过独立 SIP 线路评估媒体能力

建议评估拓扑为：`电话或 SIP 中继 → 现有 FreeSWITCH / SBC → 隔离 SIP 线路 → RustSwitch 本地 A 腿 → 可信本机媒体适配器`。

原 PBX 继续承担终端注册、复杂路由、队列、会议及现有 CDR。只把选中的测试号码送到 RustSwitch；RustSwitch 的本地号码必须显式配置，进入实时处理时限定 G.711、8 kHz、单声道、20 ms。固定上游与来源白名单不能替代终端账户认证。FreeSWITCH gateway 与注册属于原系统的线路配置，具体相互调用仍需按目标 SIP 报文验证。[官方 gateway 说明](https://developer.signalwire.com/freeswitch/users-and-endpoints/gateways/)、[本项目 AI 呼叫接入合同](../source/main/docs/api/ai-calling-reference.md)

这是部署建议，不是已经交付并验收的一键桥接方案。尤其应检查 FreeSWITCH/SBC 是否会带入当前不支持的 Route、Session-Expires、Require、重协商或安全媒体参数；不能只凭首个 INVITE 成功认定互通完成。没有真实供应商适配和完整轮次验收时，这条线路仅用于音频与控制研发，不宣称已经提供端到端 Voice Agent。

### 路径 B：把一个功能受限的业务流程迁到隔离 RustSwitch

选择没有终端注册、复杂转接、会议、队列及动态脚本依赖的业务。例如：已授权 SIP 来话 → 本地应答 → 等待 ACK → 播放允许的 WAV → 收集一组按键 → 读取结果 → 挂断。

使用[快速开始](../QUICKSTART.md)准备独立实例，再根据[本地实时处理](../source/main/docs/api/processed-local-reference.md)、[文件播放](../source/main/docs/api/file-playback-reference.md)和 [IVR](../source/main/docs/api/ivr-reference.md)配置流程。`read` 有限实现不意味着可直接导入完整多层 IVR 菜单；`park` 也不提供完整呼叫驻留与取回业务。

不要复用生产 SIP 监听端口、媒体范围、日志文件或测试账户注册绑定。现有后台导出的 FreeSWITCH XML 草稿与 RustSwitch 启动时实际消费的受限 XML 文件是两套用途，必须核对生效来源。

## 4. 把迁移范围转成可执行验收矩阵

下表是项目级验收模板，不是本次运行结果。仅对实际实施的路径选择场景；未实现能力记为“阻塞”，具备实现但没有证据记为“未执行”，不要跳过后合并成总通过。

| 场景 | 至少检查的输入与异常 | 必须保留的结果 |
| --- | --- | --- |
| 线路注册 | 正确与错误凭据、挑战、刷新、拒绝、超时、注销 | 原始事务、认证结果、刷新时序、注册状态和清理 |
| 正常呼入与桥接 | PCMA/PCMU、双方先挂、重复请求 | 两腿标识、状态顺序、真实双向音频、最终释放 |
| 振铃和早期媒体 | 180、183/SDP、最终成功与失败 | 音频开始时间、最终响应、媒体归属 |
| 建立阶段取消 | 振铃中 CANCEL、迟到 200、ACK 丢失、建立超时 | 事务最终态、迟到响应处理、无残留通话和端口 |
| ESL 请求与异步任务 | 分帧、中文长度、未知命令、重复提交、断线 | 逐字节响应、Job-UUID 关联、真实完成事件及副作用 |
| 变量与事件 | A/B 腿隔离、缺失变量、事件过滤、订阅断开 | 值与缺失语义、事件字段/顺序、业务补偿策略 |
| 拨号计划 | 命中、未命中、不支持语法、启动后修改 | 实际生效配置、失败是否明确、路由结果 |
| 播放与收号 | 无按键、连续按键、终止符、重复尾包、停止播放 | 音频内容、去重结果、`read_result`、完成原因 |
| DTMF 发送 | 合法与非法数字、无 ACK、未协商事件、挂断中发送 | 对端实际收号、任务状态、队列释放；不能只检查受理 |
| PCM 与轮次 | 队列满、旧轮帧、超时、打断、挂机后输入 | 可播放帧的代次与期限、过期拒绝、无旧音频串入 |
| ASR 候选 | 真实源音频、缺口、final 晚到、供应商断线 | 来源、重采样和时效；模拟代理结果与真实识别效果分别验收 |
| 媒体质量 | 丢包、重复、乱序、抖动、SSRC/时钟变化 | 唯一包、双向接收率、缺口/PLC 来源、音质和时延 |
| 限流与过载 | 并发、建立中、CPS 超限及恢复 | 准入与拒绝计数、Retry-After、已有通话质量、资源上界 |
| 进程和依赖故障 | worker 退出、日志故障、对端失联、测试取消 | 影响范围、确定的失败结果、回收期限、可重新接入 |
| 灰度回退 | 停止新流量、等待通话结束、恢复原路由 | 在途通话处理、最终资源、回退时间及业务验证 |

同一个输入要分别在固定 FreeSWITCH 参考端和 RustSwitch 候选端执行。版本标识、生成的 UUID 等动态字段按明确规则单独核对；错误正文、事件缺失和媒体失败不能通过任意归一化隐藏。

项目提供[成对测试说明](../source/main/docs/api/conformance-testing.md)、[隔离配置示例](../source/main/tests/conformance/profile.example.json)和[执行器入口](../source/main/tools/run_conformance.py)。从仓库根目录可先检查已有计划的一致性：

```sh
cd source/main
python3 tools/run_conformance.py --check-plan tests/conformance/plan.json
```

这条命令不进行完整业务验收。2026-09-09 对当前公开主工程执行该检查，返回退出码 3，提示“计划已过期或缺失条目，请重新生成”。维护者需要先核对计划与当前目录的差异，再按测试说明重新生成并审查；本页没有修改历史计划，也没有把这一失败改成通过。

真实执行需单独准备两端实例、专用凭证环境变量与新证据输出文件，具体参数见上面的测试说明。现有执行器只实现有限探针，计划覆盖全部目录不等于全部场景已有执行代码；不要对现网自动遍历任意有副作用的 API。

## 5. 对失败、取消和清理建立单独标准

呼叫“正常结束”与资源“确实回收”分别判断。至少检查控制面通话、事务与定时器、Rust 媒体会话、UDP 端口隔离状态、播放和 DTMF 队列、PCM/RX 订阅、后台作业及日志写入状态。端口进入配置的复用隔离期是正常行为，超过清理期限仍占用则必须定位原因。

`bgapi` 或 `sendmsg` 的受理回复不等于业务完成，Job-UUID 也不是重试幂等键。连接断开不自动取消已经受理的后台操作。`uuid_break` 当前只覆盖约定的播放/收号行为，不会统一取消 PCM 轮次或 DTMF 发送；应分别按各接口的停止、失效及查询合同处理。[ESL](../source/main/docs/api/esl-reference.md)、[PCM 轮次](../source/main/docs/api/pcm-turn-reference.md)、[DTMF 发送](../source/main/docs/api/dtmf-send-reference.md)

压测发生器未生成完整报告、页面读取失败、隔离端口不足或服务观测过期都属于无有效完整结果。先核对任务状态、预检和日志，再判断服务端行为；不能把缺少样本当作零丢包。端口不足应调整独立压测地址、主机或资源配置，不能借用主服务媒体范围。[测试合同](../source/main/docs/api/testing-reference.md)、[运行诊断](../source/main/docs/api/runtime-diagnostics-reference.md)、[运维说明](../07-运维与压测说明.md)

## 6. 小范围放量、排空与回退

先在无生产流量环境完成单路、负例、重复运行和故障回收。随后才考虑一小组明确选择的测试号码或业务实例，并在路由入口限制流量。并发数、每秒新呼叫数、建立中数量、通话时长和媒体质量要一起记录；提高并发档位不能代替增加验收覆盖。

在每个阶段开始前填写最大错误率、接通时延、音频缺口、资源水位和清理期限等停止条件。阈值由具体业务与参考端测量确定，本项目不提供已经验证的通用 5,000/10,000 路生产阈值。同机器对比也必须保持编码、包时长、路由、日志、音频处理与压测发生器能力一致。

回退首先停止向候选实例送入新呼叫，再按[排空与稳定性合同](../source/main/docs/api/stability-reference.md)处理已有通话，将新流量恢复到已验证的原路由。保留原配置、产物和验证过的部署步骤；回退后再验证注册、呼叫、媒体与业务数据。当前没有活动通话跨机无损迁移，因此不能用“切回地址”承诺保留已经落在故障实例上的通话。

## 7. 形成可审查的迁移结论

一次评估应交付：环境与版本清单、真实使用的接口盘点、逐项映射、缺口负责人、自动化用例与原始证据、媒体质量和容量报告、故障清理记录、灰度与回退演练结果。通过结论应写成“在指定版本、线路、编码及场景下通过”，并列出排除范围。

RustSwitch 的优先方向是电话接入、实时音频、明确的媒体时效和语音智能体集成。会议、呼叫中心、传真等未实现的通用 PBX 功能仍保留在差异目录，不能因为产品重心调整就从兼容风险中消失。后续改进请关联[需求说明](../12-需求说明书.md)、[完整未完成清单](../13-项目未完成清单.md)及[长期验收合同](../source/main/docs/api/voice-runtime-v1-acceptance.md)，以新的实现和可复核证据推进范围。
