# 兼容缺口逐项推进清单

通过数只代表当前源码下的限定本地断言。目录条目并非独立功能数量；增量不是FreeSWITCH完整兼容率。

基线 1.12.0 → 当前 1.13.0：限定本地通过 77 → 82，新增 5、撤回 0。

“完整契约待验收”是177份验收定义和容量目标的实现分类；“本地未执行”是尚无绑定当前源码运行证据的条目。两者不可相加或替代。

## 当前计数

| 维度 | 状态 | 条数 |
|---|---|---|
| 实现 | `export_only` | 882 |
| 实现 | `implemented` | 19 |
| 实现 | `internal_only` | 23 |
| 实现 | `not_implemented` | 2844 |
| 实现 | `not_verified` | 178 |
| 实现 | `partial` | 53 |
| 本地执行 | `failed` | 1 |
| 本地执行 | `not_run` | 3916 |
| 本地执行 | `passed` | 82 |

## 按层剩余条目

下表按目录记录统计，原生声明、重复配置和验收定义不是独立业务能力。各列是不同维度，不能横向相加。

| 层／类别 | 全部 | 未实现入口 | 本地未执行 | 本地限定通过 |
|---|---:|---:|---:|---:|
| `acceptance_definition` | 177 | 0 | 177 | 0 |
| `fs_api` | 292 | 279 | 284 | 8 |
| `fs_application` | 222 | 214 | 214 | 8 |
| `fs_channel_variable` | 100 | 100 | 100 | 0 |
| `fs_chat_application` | 14 | 14 | 14 | 0 |
| `fs_conference_subcommand` | 90 | 90 | 90 | 0 |
| `fs_event` | 94 | 83 | 84 | 10 |
| `fs_json_api` | 10 | 10 | 10 | 0 |
| `fs_module` | 144 | 144 | 144 | 0 |
| `fs_native_function` | 1865 | 1865 | 1865 | 0 |
| `fs_sofia_subcommand` | 40 | 40 | 40 | 0 |
| `fs_vanilla_parameter` | 875 | 0 | 875 | 0 |
| `key_api` | 2 | 0 | 0 | 2 |
| `key_c_abi` | 2 | 1 | 2 | 0 |
| `key_cli` | 3 | 0 | 2 | 1 |
| `key_config` | 7 | 0 | 2 | 5 |
| `key_esl` | 7 | 1 | 1 | 6 |
| `key_guard` | 6 | 0 | 1 | 5 |
| `key_http` | 9 | 0 | 1 | 8 |
| `key_ipc` | 22 | 0 | 3 | 19 |
| `key_media` | 10 | 1 | 4 | 5 |
| `key_sip` | 8 | 2 | 3 | 5 |

## 本轮新增限定通过

| 原始ID | 名称 | 已测范围 |
|---|---|---|
| `key-api-rx-sdk` | 内部Go服务端上行音频SDK | 内部Go收音句柄、SIP授权、交付期限及挂断清理；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。 |
| `key-ipc-rx-status` | 内部上行订阅状态与对账 | 实际身份、事件/样本计数和失败后仍可查询；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。 |
| `key-ipc-rx-stream` | 内部RXS2原生音频数据流 | 真实G.711输入逐样本核对、原始期限和过期拒绝；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。 |
| `key-ipc-rx-subscribe` | 内部原生音频上行订阅 | 真实订阅及ACK前/错误来源ACK拒绝；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。 |
| `key-ipc-rx-unsubscribe` | 内部上行停止与终态 | 明确退订、挂断撤销、资源归零及端口重新绑定；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。 |

## 全量任务及推进顺序

按云蝠Voice Runtime首期范围排序：P0核心媒体与稳定性；P1电话业务兼容适配；P2按现网需求纳入或首期延后。完整3999个原始ID及状态保留；延后不标绿、不改变分母。产品阶段里程碑和性能目标见[研发方向](voice-runtime-direction.md)及[研发清单](voice-runtime-roadmap.json)。

[下载全部逐项任务 JSON](compatibility-worklist.json) · [下载全部逐项任务 CSV](compatibility-worklist.csv) · [查看计数变化及原始ID](compatibility-progress.json)

后续每轮必须先冻结上版证据，再实现、执行、记录失败或通过并重新发布；不能缩减分母、把订阅成功当事件已实现、把API目录存在当业务成功，或给未执行记录标绿。
