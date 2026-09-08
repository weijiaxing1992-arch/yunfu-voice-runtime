# RustSwitch 接口文档中心

> 本文档包 1.13.0 包含已验证的 [本地 A 腿 RX 收音与内部 Go SDK](rx-stream-reference.md)，保留 [PCM 下行轮次](pcm-turn-reference.md) 和 [已 ACK 的 RVA1 外部 PCM](rva1-reference.md)。真实结果及部署核对方式见 [1.13 验证记录](release-validation-v1.13.md)；实际安装版本须通过 `GET /v1/docs` 及部署回执核对，不能从文档包版本推定已安装。既有 500/5000 路失败继续保留。

文档集版本：1.13.0。对应项目：云蝠 Voice Runtime（工程名RustSwitch，0.3.0开发原型）。FreeSWITCH 对照基线：1.11.3，提交 `ef32e205295e29f034f1453ad245ba5efb07b94a`。

管理后台提供「接口文档」和「FreeSWITCH 对照」两个独立入口。接口文档先完整描述可调用的现有接口，再展示关联差异；对照页按接口名称、模块、类别和实现状态查询完整固定源码目录，可查看双方语法、行为、差异、证据及原文。文档集可离线下载，页面不会自动执行文档中的命令。

## 研发定位

首期围绕电话Voice Agent闭环，优先兼容业务实际使用的FS接口。性能目标必须在同功能、音质和部署条件下测量；会议、视频、传真和完整模块宿主延后。原有对照ID与未实现状态继续保留，延后不是兼容通过。

[完整V1长期验收合同](voice-runtime-v1-acceptance.md) · [研发方向与性能验收](voice-runtime-direction.md) · [阶段里程碑](voice-runtime-roadmap.json) · [本轮交付结果](release-validation-v1.13.md)

## 阅读顺序

1. [HTTP 接口完整说明](http-reference.md)：认证边界、版本冲突、所有参数、嵌套返回模型、示例及错误；对应 [OpenAPI 3.1](openapi.json)。
2. [SIP、媒体、内部通信、C ABI 与 CLI](protocol-reference.md)：补齐非 HTTP 接口；机器可读的 [媒体 IPC Schema](media-ipc.schema.json) 与原生头文件也包含在包内。
3. [FreeSWITCH 逐项对照说明](freeswitch-comparison.md)：双方调用方式、同名参数语义差异、事件、生命周期和迁移验收；对应 [全量对照数据](comparison.json)。
4. [FreeSWITCH 固定版本兼容标准](../freeswitch-compatibility/README.md)：原有标准、全部源目录、验收定义及差分模板完整收录；可直接阅读 [合订本](../freeswitch-compatibility/FULL-STANDARD.md)。

5. [压力模拟与测试小电话](testing-reference.md)：档位、幂等作业、实时进度、停止清理与真实报告边界。
6. [通话稳定性与资源保护](stability-reference.md)：媒体顺序、故障回收、异常流量预算、管理并发限制及新增控制进程指标。
7. [完整模块与业务域审计](feature-audit.md)：完整源码树 144 个模块目录与 1 个 SDK 目录，逐项说明实现、缺口和待执行的差分；对应 [机器审计](feature-audit.json)。
8. [5000路验证与本轮逐项复核](verification-5000-2026-09-06.md)：1.4历史代码两轮 5000 路结果、3979 项审查及剩余差距；[上一轮记录](verification-2026-09-06.md)保留早期 500/1000 通过与 2000 失败。
9. [本地运行验证与绿色状态](runtime-verification.md)：逐原始对照 ID 展示当前源码绑定的通过、失败、未执行与过期状态；绿色明确限定测试范围。

10. [PCM turn 内部合同](pcm-turn-reference.md)：JSON 控制、RSP1/RSR1、固定队列、未知结果与实际发送完成。
11. [RVA1 外部供音](rva1-reference.md)：已 ACK UUID 授权、私有 Unix 连接、token、背压与轮次控制。
12. [1.12 验证记录](release-validation-v1.12.md)：真实 SIP 22 方法/12 子例、Go 899 个通过事件、既有 228 项回归和保留的失败。
13. [RX 收音合同](rx-stream-reference.md)：三个JSON控制、RXS2二进制记录、时效和样本对账、Go句柄授权及回收。
14. [1.13 验证记录](release-validation-v1.13.md)：真实SIP收音、暂停接收故障及当前完整回归范围。

## “完整”的边界

当前注册的 26 个 HTTP method/path 都有接口说明，其中包含 15 个原有管理/观测操作、5 个文档读取操作和 5 个压力/单路测试操作及 1 个编码能力操作。静态页面资源及隐式 HEAD 的行为另行说明。SIP、媒体 IPC、编解码 ABI、预留协议 ABI、CLI 与事件文件都按当前实现分别记录。新增 RVA1 和 RSP1 是独立二进制接口，不增加 HTTP 路径，也不把 PCM 样本经管理 JSON 发送。

RXS2 是内部独立接收通道，Server RX SDK 只能在控制进程内调用；没有新的公开音频下载或ASR HTTP端点。接收底座通过不代表已接入识别供应商，也不增加原版FreeSWITCH应用数量。

FreeSWITCH 对照覆盖锁定版本中已提取的全部源码目录条目，并另外维护关键功能的人工语义对照。源码注册、头文件声明、示例配置和验收定义各有不同证据强度。运行时动态模块、宏计算后的注册、脚本动态扩展、第三方模块及完整返回行为尚未完成运行采集；不能把静态目录数量称为 FreeSWITCH 全部运行接口或 100% 兼容证明。

当前 RustSwitch HTTP 与 FreeSWITCH ESL 使用不同传输、认证、返回和事件机制，即使目的相近也不具备线格式直接替换性。XML 编辑导出不代表 XML 已被 RustSwitch 执行。文档明确区分已实现、部分实现、仅导出、内部接口、待实现和待验证；不生成兼容百分比。

## 文档发布 API

- `GET /v1/docs`：文档清单、分类、哈希与版本。
- `GET /v1/docs/openapi.json`：全部 HTTP 接口模型。
- `GET /v1/docs/comparison.json`：逐项 FreeSWITCH 对照数据。
- `GET /v1/docs/file?path=<清单中的相对路径>`：读取原始 Markdown、JSON、CSV 或头文件。
- `GET /v1/docs/export.zip`：下载完整文档、原始目录、头文件与来源许可。

以上入口只读并使用现有回环管理边界。下载包不包含运行 CSRF 令牌、业务事件日志、管理持久化文件或用户尚未保存的 XML。运行验证数据可包含专用隔离测试的压缩原始日志，用于离线复核绿色依据。

## 维护和防止漂移

源码文档维护在 `docs/api`，参考标准在 `docs/freeswitch-compatibility`。`tools/build_interface_comparison.py` 从锁定目录重建逐项对照；`tools/build_api_docs.py` 检查 HTTP 路由覆盖、本地 OpenAPI 引用、对照唯一 ID 和发布路径，再生成后台内嵌文件和可下载包。新建或变更 HTTP 接口时，必须同步 OpenAPI；缺少文档的注册路由会使检查失败。

```sh
python3 tools/build_interface_comparison.py --reference-root /path/to/freeswitch-1.11.3
python3 tools/build_api_docs.py
python3 tools/build_api_docs.py --check
python3 tools/build_api_docs.py --zip run/rustswitch-interface-docs.zip
make control
```

重建 FreeSWITCH 对照时，将上述参考目录替换为锁定提交的本地源码路径；生成器会核对来源哈希。日常构建直接使用已交付的对照数据，不需要重新下载或扫描 FreeSWITCH 源码。发布器会离线核对完整功能审计绑定的本地源码哈希；运行源码变更后，须使用锁定源码与完整 Git 树重新运行 `tools/build_feature_audit.py` 并人工复核差异，不能沿用旧结论。

原文与内嵌副本均随源码交付，Go 构建不依赖外网。更新静态文档不会改动通话策略；服务按部署流程重新构建和启动后采用新的内嵌文档。

OpenAPI 使用 [官方 3.1 规范](https://spec.openapis.org/oas/v3.1.1.html)。FreeSWITCH 语义优先引用固定提交源码；网页手册可能重定向或随上游改变，应以文档列出的参考版本和证据为准。

## 音频与逐项验证（1.3）

- [音频接口、PCM SDK 和 FreeSWITCH 对照](audio-reference.md)
- [逐项验证账本](verification-ledger.md)：保留每个原始对照ID、证据等级和未验证原因。
- `GET /v1/codecs`：真实原生后端能力及实时透传范围。

账本覆盖率只是逐项审查覆盖，不是原版运行兼容通过率。

## 本地验证状态（1.4）

对照页同时展示实现状态与本地执行状态，并支持按验证状态筛选。绿色要求实际运行通过、必需断言齐全、日志哈希正确、执行前后与当前源码一致且未超过复查期限。`tools/build_runtime_verification.py` 采集或重新判定证据，文档发布器离线复核；历史未绑定结果与仅有用例定义的条目保持非绿色。所有原版成对兼容结论独立保留。

1.4 历史代码的 5000 路容量绿色来自 8 vCPU / 2 GiB 的本机隔离 Linux 虚拟机，实际经过 SIP 与 Rust 媒体服务。当前 macOS 管理页的压测按钮仍遵守本机端口预检，不会自动把作业转交虚拟机；容量报告不改变执行环境或越过主服务媒体范围。历史结果见 [5000路验证](verification-5000-2026-09-06.md)。1.5 新代码的本轮复测因发生器发送不足未通过完整负载检查，旧成功不沿用为当前容量绿色；详情见 [1.5 验证交付说明](release-validation-v1.5.md)。

## 原版成对验证（1.5）

新增 [入站ESL接口](esl-reference.md)、[完整用例计划与执行器](conformance-testing.md) 和 [真实成对结果](conformance-report.md)。已在隔离Linux运行原版1.11.3，动态API目录逐项覆盖292个原始API注册。原版未加载、候选缺入口和入口存在但语义未齐分别记录。对照页的成对绿色只表示有限探针通过，与原条目完整认证、本地回归和容量结果分开。

本轮新增IPv4 SIP TCP/TLS双腿，真实媒体测试验证正常拆线与连接故障回收；入站ESL具有有限API、真实通道事件和有界作业队列。WS/WSS、注册鉴权、XML运行、应用执行及原生模块宿主仍是明确缺口。原始构建、配置树、报文指纹随报告留证。

## 1.6 AI 呼叫接入

- [线路、本地接听与实时收号总览](ai-calling-reference.md)
- [固定上游注册与 Digest 鉴权](trunk-registration.md)
- [ESL 应用、IVR 与按键采集](ivr-reference.md)
- [Rust 媒体按键与提示音协议](media-interaction.md)
- [原版 FreeSWITCH 三应用成对结果](ai-calling-verification.md)
- [本轮实际检查与剩余缺口](release-validation-v1.6.md)

## 1.7 增量记录

- [XML 拨号计划与基础应用](dialplan-reference.md)：实际顺序执行、404、ACK、sleep/park/hangup。
- [受限 WAV 放音](file-playback-reference.md)：真实媒体、加载与取消、格式和资源预算。
- [批量变量命令](variables-reference.md)：uuid_setvar_multi 参数、逐项副作用和差异。
- [1.7 逐项验证报告](release-validation-v1.7.md)：当前构建的测试、原版对照和剩余缺口。

## 历史增量（1.8）

- [全量缺口逐项推进清单](compatibility-worklist.md)：每个原始ID的下一步、剩余差异及本轮新增/撤回。
- [通道快照 uuid_dump](channel-snapshot-reference.md)：真实单腿状态、变量及四种输出格式。
- [放音与停泊生命周期](event-lifecycle-reference.md)：事件触发、终止与错误边界。
- [1.8 实现与逐项验证报告](release-validation-v1.8.md)：实际测试与未完成范围。

## 历史增量（1.9）

- [总览压测观测](overview-testing-reference.md)：独立实例实际采样、主服务分离、轮询故障与历史结果。
- [本地按键发送](dtmf-send-reference.md)：uuid_send_dtmf受限适配、自有完成查询、IPC和真实失败。
- [多采样率音频帧](audio-frame-reference.md)：按分支转换的离线SDK与时间/所有权，不冒称实时ASR/TTS。
- [1.9逐项结果](release-validation-v1.9.md)：66→70限定绿色、保留原ID和完整Agent剩余重点。

## 历史增量（1.10）

- [真实音频处理压测与能力目录](processed-testing-reference.md)：选择透传或异律 G.711 处理，真实 worker 握手、PCM 帧数与有界错误诊断。
- [实时 G.711 图](processed-media-reference.md)：原生 8k PCM、媒体时钟、抖动缓冲、辅助媒体与恢复边界。
- [本轮实际验证和剩余缺口](release-validation-v1.10.md)：原始失败、修复后复测与逐项证据。

## 历史增量（1.11）

- [本地单腿媒体](processed-local-reference.md)：RX PCM 消费与有限 IVR 原 PCM 编码，统一 RTP/RTCP。
- [Go 运行时诊断](runtime-diagnostics-reference.md)：实际日志格式解析与观测边界。
- [1.11 验证记录](release-validation-v1.11.md)：原 26 项本地图、既有功能及页面观察，历史失败不覆盖。

## 历史增量（1.12）

- [PCM turn](pcm-turn-reference.md)：8k/20ms，最大 50 帧，严格偏移与旧轮失效，0/20/40ms 淡出，数据 lane 背压与失联隔离。
- [RVA1](rva1-reference.md)：部署 opt-in 的同 UID Unix 持久连接；真实 ACK 授权，多通话连接复用，有限 token 过渡与未知结果恢复。
- [1.12 验证](release-validation-v1.12.md)：内部 PCM 与外部 SIP 全链路分开验收，ACK 超时缺少 A 腿 BYE 的真实修复，历史源码、二进制、测试与部署回执分别记录。

这些接口提供流式下行基础，不是 ASR/TTS 供应商、VAD 自动打断或双轨录音完成。`uuid_break` 仍控制既有播放，PCM 必须明确 INTERRUPT；自有接口不得替代 FreeSWITCH 原版成对证据授予绿色。
