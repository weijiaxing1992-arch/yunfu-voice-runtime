# 完整兼容测试计划与成对协议执行器

本工具把固定 FreeSWITCH **1.11.3 / ef32e205295e29f034f1453ad245ba5efb07b94a** 对照目录的全部 3979 个原始 ID 纳入计划，并追踪 177 条验收定义、35 个业务域、30 类通信协议及外部兼容边界。完整清单已经建立；其中 26 个有限协议探针（2 项身份/目录发现、24 项成对子场景）可以实际连接两端执行。**清单完整不等于全部测试已经实现，子场景通过不等于整条接口完全兼容。**

计划、执行器自测、RustSwitch 单端验证和原版成对实测分别保留状态。没有两端环境、没有实际流量、缺少协议适配器、未选择的探针和未覆盖分支，均不会计入通过；本工具不会生成「100% 兼容」结论。

## 文件与运行入口

| 文件 | 作用 |
| --- | --- |
| `tests/conformance/plan.json` | 每个原始 ID 的来源、当前实现状态、关联有限探针、必须补充的断言组和后续步骤 |
| `tests/conformance/protocols.json` | 全部通信协议与外部兼容边界，明确每类剩余验证内容 |
| `tests/conformance/profile.example.json` | 隔离两端服务的配置示例，密码仅引用环境变量 |
| `tests/conformance/suite.py` | 有界协议接收器、真实探针、成对比较和严格状态汇总 |
| `tools/run_conformance.py` | 计划生成、计划一致性检查及真实探针执行入口 |
| `tools/test_conformance.py` | 执行器自身的边界、负例及隔离 socket 夹具测试；不计产品兼容 |

在项目根目录运行：

```sh
python3 tools/run_conformance.py --plan tests/conformance/plan.json
python3 tools/run_conformance.py --check-plan tests/conformance/plan.json
python3 tools/test_conformance.py
python3 tools/run_conformance.py --profile /path/to/isolated-profile.json --output /path/to/new-run.json
```

凭证须通过 `FS_CONFORMANCE_PASSWORD` 和 `RUSTSWITCH_CONFORMANCE_PASSWORD` 等专用环境变量提供；不要把密码写入命令行或配置 JSON。报告不会保存可恢复的认证密码，只记录该请求长度和摘要；其他报文保留原始 base64 字节。

`--probe esl.echo` 可限定本次执行的探针，但其余探针继续显示「未选择、未执行」，因此不会总通过。输出路径必须是新文件，历史失败证据不能覆盖。退出码 0 只表示本工具定义的全部有限探针通过；2 表示其中存在差异、失败、阻塞或未执行；3 表示输入或证据出错。退出码 0 仍不代表 3979 个条目的完整契约通过。

## 真实可执行探针

| 探针 | 必须取得的真实结果 | 尚不能证明的范围 |
| --- | --- | --- |
| `identity.esl` | 两端真实 `api version`；原版响应必须符合固定版本 | 不比较不同产品的版本正文，不计兼容通过；远端提交/构建仍需产物证据 |
| `esl.auth.fragmented` | 逐字节认证、成功回复、UTF-8 echo | ACL、IPv6、认证超时、完整安全矩阵 |
| `esl.auth.wrong` | 错误密码拒绝、后续帧与 EOF | 空密码、封禁策略、限速；仅允许隔离 fixture |
| `esl.auth.required` | 未认证 API 返回 command not found，随后同连接合法认证和 echo | 用户级授权与所有 API 权限；仅允许隔离 fixture |
| `esl.echo` | 完整中文 UTF-8 正文及真实字节 Content-Length | 所有 API、输出格式及复杂参数 |
| `esl.console.single` | fs_cli -x 的 console_execute 包装，UTF-8、空 echo、首尾空格、单分号及 false 字面参数 | 别名、双分号批处理、交互日志/补全及全部 API；明确拒绝未实现的控制台入口 |
| `esl.api.unknown` | 固定未知命令的完整错误正文 | 所有错误命令组合 |
| `esl.frames.pipeline` | 一次写入两命令、两个独立响应、无吞帧 | 高负载下的事件/回复交织和慢消费者 |
| `esl.frames.crlf` | CRLF 分隔下认证和 echo | 所有头部格式、畸形长度及最大长度运行矩阵 |
| `esl.frames.split_positions` | 固定请求每个字节位置分别拆分后，均完整返回 | 任意无限长度输入及网络长期压力 |
| `esl.bgapi.echo` | 固定 Job-UUID、受理回复、BACKGROUND_JOB 内层长度与原始正文 | 并发作业、订阅权限、断线重连、重复作业；仅允许隔离 fixture |
| `identity.api_inventory` | 双端真实 `show api as json`，行数/字段/重复项验证，并逐个对照全部 292 个原始 API 声明 | 接口出现不表示语义兼容，不计通过 |
| `esl.api.echo_empty` | 无参数、尾空格、首尾空格三个真实原始响应 | 复杂表达式和所有编码 |
| `esl.api.create_uuid` | 两次生成格式正确、非 nil 且不同的 UUID；原值保留 | 完整 UUID 版本/配置矩阵 |
| `esl.api.uuid_exists_missing` | 固定 nil UUID 的实际不存在查询 | 存在通道和并发通道状态 |
| `esl.api.uuid_getvar_missing` | 确认 nil UUID 不存在后，比较读取变量的错误 | 真实通道的变量、数组、权限和展开 |
| `esl.api.uuid_setvar_missing` | 仅隔离 fixture，确认 nil UUID 不存在后，比较设置错误 | 正常写入和通道副作用 |
| `esl.api.uuid_kill_missing` | 仅隔离 fixture，确认 nil UUID 不存在后，比较挂断错误 | 真实拆线、原因码、事件与媒体回收 |
| `esl.api.uuid_syntax` | 四个 uuid API 缺参数时的完整 usage/错误 | 所有参数组合 |
| `esl.command.case` | 混合大小写命令、格式沿用、nixevent/noevents 状态 | 所有 ESL 命令与所有事件 |
| `esl.filter.lifecycle` | delete all 与空值清除分别执行；每次都要求实际后台事件交付 | 正则、数组、索引和多事件权限 |
| `esl.bgapi.echo.json` | JSON 作业关联、原始正文和内层 Content-Length | 原事件全部字段、数组和重复头 |
| `esl.bgapi.echo.xml` | XML headers/root-body 结构、作业关联和实际长度 | 原事件全部字段、数组和重复标签 |
| `sip.options.udp` | 一个完整 UDP OPTIONS 事务、事务标识、状态和能力头 | 注册、呼叫、重传、分叉和媒体 |
| `sip.options.tcp` | TCP 上完整 OPTIONS 响应及分帧 | 完整 TCP 呼叫、长连接复用和背压 |
| `sip.options.tls` | 受信任证书与主机名校验后完成 OPTIONS | 双向 TLS、证书轮换、安全媒体和完整通话 |

原版 `fs_cli -x` 在服务端返回错误时也可能退出 0，因此客户端验证必须检查完整 stdout/原始响应。当前控制台包装只开放已有的 9 个 API 单命令路径，复用实际 API 执行和连接期限；别名和 `;;` 批处理在执行任何动作前明确拒绝，仍是兼容缺口。产品版本和运行目录保留真实身份，不通过修改版本或扩充虚假接口取得通过。

没有 SIP TCP 或 TLS 端点时，各项单独显示阻塞；不会替换成 UDP，也不影响其他具备条件的 ESL 探针执行。原版 ESL 身份基线失联或版本不符时，成对结果整体缺少可信基线，业务探针明确阻塞。

ESL 原始响应正文逐字节比较，不在执行器内修剪空格、换行、错误内容或产品版本。真实原版 API 执行会修剪输入参数首尾 ASCII 空白，而 BACKGROUND_JOB 的 Job-Command-Arg 保留原始输入；对应边界已按原版实际报文加入测试。帧头名称大小写及排列可以规范化。BACKGROUND_JOB 仅允许明确列出的实例元数据变化：Core-UUID、FreeSWITCH 主机/地址、事件日期/时间戳、调用位置及事件序号；未知额外字段继续参与比较。原始事件始终保留，外层事件帧长度随这些已声明元数据变化，但内层真实正文的 Content-Length 必须正确。

SIP OPTIONS 核对客户端生成的 Call-ID、Via branch/地址、From tag、To URI、CSeq；仅在这些事务标识正确后，比较状态码、原因短语、Allow、Supported、Accept、Allow-Events 和正文。实例 To tag、Date、Server/User-Agent 不作为不同产品的字节相等要求。其余响应头保留原始证据但尚未逐个断言，报告范围明确限定为 OPTIONS 子场景。

## 原始 API 全目录的动态差距

`identity.api_inventory` 从两端实际运行实例读取 `show api as json`。每个原始 `fs_api` ID 都保留来源模块、原版实际注册字段、候选实际注册字段和下一步，不按同名命令删除源声明，也不直接调用目录中的业务命令。

| 状态 | 含义 |
| --- | --- |
| `blocked_reference_inventory` / `blocked_candidate_inventory` | 未取得完整可解析的该端运行目录；不能把失联当空列表 |
| `blocked_reference_module` | 原版当前启用模块没有该命令，需要先补原版构建/模块 fixture |
| `missing_candidate_entrypoint` | 原版实例提供命令，候选实例未提供入口 |
| `requires_semantic_verification` | 双端都有同名入口，仍须验证该原始接口完整行为；不标为通过 |

源码模块与运行 `ikey` 同时保留，core 包装、同名覆盖和模块宿主一致性需要继续验证。新增运行时命令也单独列出，不能藏进现有分母。目录条目不是任意命令的自动执行许可。

## 全协议与外部边界

协议矩阵显式包含：

- SIP UDP/TCP/TLS/WS/WSS、IPv4/IPv6、REGISTER/Digest、INVITE/ACK/CANCEL/BYE、reINVITE/UPDATE、PRACK/100rel、session timer、REFER/Replaces、MESSAGE/SUBSCRIBE/NOTIFY/PUBLISH。
- ESL inbound/outbound、API/bgapi、sendmsg、linger、订阅、fs_cli/libesl 客户端兼容。
- RTP、RTCP、RTCP-mux、SRTP/SRTCP、DTLS-SRTP、ICE/STUN/TURN、WebRTC/Verto、DTMF、CN/DTX/PLC/FEC，以及原版实际构建编码清单。
- T.38/UDPTL、传真、FSK、H.323/SCCP 等实际端点模块。
- XML configuration/directory/dialplan/XML Curl、HTTP/HTTPS/HTTAPI、CDR/事件投递、数据库/缓存、Lua/JavaScript/Python、原生 switch_* 与 libesl ABI。
- IVR、会议、录音、队列、语音信箱、ASR/TTS、视频/文件、准入、时钟、故障恢复和容量。这些属于业务及外部兼容边界，不把它们都称为核心通信协议。

每个原始条目仍必须展开合法输入、错误/边界/权限、并发/背压/重传、超时/取消/恢复、实际业务副作用和资源清理。未实例化成真实输入、未同时执行两端、未保留原始媒体/状态/文件结果的断言，都保持未完成。既有 5000 路 PCMU 单端容量结果不自动填充这些成对模块测试。

## 隔离、限额与证据

测试器只包含固定白名单命令，不接受任意 ESL/API 指令，不拼接 shell，不执行 originate、删除文件或生产通道动作。uuid_setvar/uuid_kill 仅在显式隔离 fixture 中对刚查询确认不存在的固定 nil UUID 验证错误，缺参数用例也不携带有效通道标识；不操作真实通道。bgapi、错误鉴权和未认证请求要求 `fixture.isolated=true`。远端目标另外要求 `allow_remote=true`；地址采用数字 IP 或 localhost，TLS 证书名单独用 `server_name`，避免 DNS 阻塞突破探针时限。

默认连接 2 秒、每端每探针总时限 8 秒、帧头 64 KiB、正文 1 MiB、原始证据 2 MiB。每个探针连接均在成功、失败、超时后关闭；多次分片连接共享总时限。达到证据限额即失败，不截断后判通过。UDP 数据报不能跨报文拼接补齐正文；TCP 接收缓冲保留下一帧。

JSON 报告保存 profile SHA-256、执行器/产品源码前后快照、明确指定二进制/配置/源码清单的哈希、实际收发报文、两端差异、执行时间和全部 3979 个条目的后续步骤。运行中源码变化会使原子场景通过状态失效。指定文件哈希只是本地可追溯证据，**不是远端运行进程的自动证明**；完整认证还需要冻结原版构建和启动记录、模块清单、配置、客户端版本及完整观察结果。

执行器当前 46 个自测覆盖：错版本/错提交、缺端点/缺凭证、空或未知探针、没流量、未选择项、字节差异、连接拒绝/超时、源码变化、重复/非法/溢出长度、UTF-8 空行正文、管线帧、EOF、证据超限、UDP 不完整/额外正文、错误事务标识和实际回环 TCP/UDP 收发。自测通过只证明这些执行器逻辑经过检查，不是原版 FreeSWITCH 的运行结果。
