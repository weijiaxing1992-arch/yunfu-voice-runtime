# FreeSWITCH 1.11.3 与 RustSwitch 逐项接口对照

固定参考：FreeSWITCH **1.11.3**，提交 `ef32e205295e29f034f1453ad245ba5efb07b94a`。对照数据版本：`1.0.0`。

首期目标与优先级见[云蝠 Voice Runtime研发方向](voice-runtime-direction.md)：优先电话语音智能体与必要接口适配，延后的PBX能力保留原ID及缺口状态。**当前不能直接替换 FreeSWITCH。RustSwitch 的 HTTP 管理接口不是 ESL 兼容入口，可选ESL入口已支持有限单命令客户端路径；完整ESL SDK、XML业务执行和mod_*.so宿主尚未兼容。**

本包包含 **3,999 条可追踪记录**：3,923 条基线目录/验收定义，加 76 条人工关键语义对照。每条都有双方入口、原语法、语义边界、差异、验证说明与来源定位。完整逐项列表见 [comparison.json](comparison.json)，HTTP 合同见 [http-reference.md](http-reference.md) 与 [openapi.json](openapi.json)。

这里的完整是对下列固定输入集逐条覆盖，不是原版动态运行接口全集或 100% 行为兼容。712 份选定源码均校验 SHA-256；已在隔离Linux构建运行原版，采集所选模块、动态注册和成对协议子场景；第三方模块、CUSTOM全集与完整客户端轨迹仍未齐全。

## 对照口径与数据结构

`implemented` 仅表示该条所描述的 RustSwitch 自有入口已实现；它通常与原版是功能概念相近的不同协议。必须同时读取 `relationship`、双方语义、差异和验证字段，不得把这些条目计作原版兼容完成率。

| 状态 | 严格含义 |
| --- | --- |
| `implemented` | RustSwitch 自有接口已实现 |
| `partial` | 仅有部分相关能力 |
| `export_only` | 仅编辑/导出 |
| `not_implemented` | 未实现该兼容入口 |
| `internal_only` | 仅内部协议 |
| `not_verified` | 未执行对应兼容验收 |

`relationship` 区分直接对照、概念相近但不兼容线协议、仅导出、无兼容入口、仅内部合同与仅验收要求；当前没有任何一条被授予完整 FreeSWITCH 差分兼容认证。

每条记录固定 17 字段：`id/category/name/module/fs_interface/fs_syntax/fs_semantics/rustswitch_interface/rustswitch_semantics/status/relationship/differences/verification/source_url/source_path/source_line/document_path`。除 `differences` 为字符串数组、`source_line` 为正整数，其余均为字符串；本地验收定义的 `source_url` 为空，其来源定位到本项目标准。`document_path` 相对 docs 根目录，可带锚点。

## 基线逐项覆盖

| 来源 | 条数 | 条目性质与必须保留的限制 |
| --- | ---: | --- |
| 注册声明 | 538 | 292 API、222 APP、10 JSON API、14 CHAT_APP；同名不同入口独立保留；原名称/语法/标志表达式保留，未解析处明确标记 |
| 模块源码目录 | 144 | 含 SDK 辅助目录；不是已构建或已加载模块列表 |
| 事件枚举 | 94 | 包含 ALL 订阅哨兵与 CLONE；不是 94 种已验证业务事件，不含全部 CUSTOM 子类 |
| 变量名称宏 | 100 | 声明位置，含 99 个唯一宏/变量名称；不是动态或用户自定义变量全集，作用域/默认值需沿处理器追踪 |
| 原生函数声明 | 1,865 | 声明位置，含 1,860 个唯一名称；不是唯一导出符号数，未覆盖全部宏生成钩子、extern 与 C++ 绑定 |
| vanilla 参数 | 875 | 非注释 param 出现位置，重复保留；不是全部运行配置 schema，也不是管理表单的 4,304 个字段 |
| Conference/Sofia 子命令 | 130 | 84 会议静态表 + 6 全局分支 + 38 Sofia 解析候选 + 2 仅帮助候选；证据等级不能混同 |
| 兼容验收定义 | 177 | 169 场景 + 8 容量 profile；全部是未执行的兼容定义，需要实例化与原版差分 |

原始目录：[注册/模块/事件/变量/原生/参数](../freeswitch-compatibility/catalog/source-catalog.json)、[会议与 Sofia 子命令](../freeswitch-compatibility/catalog/conference-and-sofia-subcommands.md)、[验收定义](../freeswitch-compatibility/catalog/conformance-cases.json)。生成器逐条保留记录并检查唯一 ID、数量和固定源码链接。原验收目录旧行号可因文档修订过期，因此对照生成时按用例 ID 重新定位。

对于仅有声明元数据的条目，`fs_semantics` 明确只记录注册类别、源码描述、处理器或声明约束；不把未知行为写成已核实语义。原生参数文本、宏表达式与 NULL/动态语法不被静默清空。全量记录的同名字段可在 JSON 中检索；需要业务层面的成对比较时，使用下方关键合同。

## 当前验证结论

本地实际运行结果另见 [运行验证与绿色状态](runtime-verification.md)。绿色只表示当前源码下所列限定断言通过，原始实现状态和原版成对认证保持独立。源码或证据过期后不沿用旧的绿色。

本项目已记录的 Go/Rust 单元、竞态与静态检查、管理/XML 回归以及 17 个受限通话场景在两种 UDP 模式的历史回归，说明当前原型这些实现范围可工作；详细证据边界见 [0.3 管理交付说明](../management-v0.3.md)。它们没有执行原版对照，不能填充 177 条兼容验收定义为通过。C G.711 独立往返检查不代表转码通话已实现。

1.4历史代码的两轮 Linux 虚拟机 5000 路双向 PCMU 短测已通过；不能自动沿用到1.6新增注册/IVR的组合，完整数据、历次失败与硬件边界见 [5000路验证与本轮逐项复核](verification-5000-2026-09-06.md)。Linux 物理服务器和一万路验收仍待提供环境。整机 UDP 计数与实际 RTP 缺包分别记录，不能把回环透传容量迁移为转码、会议、录音、真实网卡或原版成对认证。

## 成对业务示例

### 查询运行状态：协议和返回字段不同

原版已有客户端通过 ESL 执行：

```text
api status

```

服务端以 `Content-Type: api/response`、`Content-Length` 和原版状态正文返回；原版构建相关正文必须实际采集，不能由本文件伪造。RustSwitch 当前使用：

```http
GET /v1/status HTTP/1.1
Host: 127.0.0.1:9080

```

RustSwitch 返回 HTTP JSON，包含 `active_calls/established_calls/workers/guard/journal_*/config_revision` 等。既有 `fs_cli -x status` 不能仅更改端口就解析此响应；原版 `show channels` 的 UUID 列表也没有在 `/v1/status` 中实现。

### 调整接入保护：session、桥接通话与令牌口径不同

原版 `fsctl sps`、`fsctl max_sessions` 控制其核心会话边界；原始参数和错误正文见固定源码。RustSwitch 应先 GET `/v1/config`，保留返回的完整 `guard.policy`，修改其中 `calls_per_second/max_active_calls/burst_calls` 后提交：

```text
PUT /v1/guard
Content-Type: application/json
X-RustSwitch-CSRF: <本进程 GET /v1/config 返回的 token>

{"revision": <读取的版本>, "policy": <保留其他字段的完整策略对象>}
```

尖括号表示必须从实际响应填写的值，示例不是可原样提交的 JSON。旧版本返回 409；校验失败 422；保存失败 500 且运行策略不更新。降低阈值仅拒绝后续新 INVITE，已有通话继续；过载 SIP 响应为 503/Retry-After，不延迟已经产生的 200 OK，也不在服务端排队等待。

### 保存用户或网关：XML草稿与固定上游注册配置

原版配置由目录/Sofia 模块和相应 reload/profile 路径实际消费。RustSwitch `PUT /v1/fs-config/file` 可保存 `directory/...xml` 或 `sip_profiles/...xml`，随后 ZIP 导出包含这些已保存文本；返回 `runtime_supported:false`。终端向RustSwitch发送 REGISTER 仍不支持；通过sip.trunk_auth和sip.registration可启用固定上游REGISTER客户端，详见[线路注册](trunk-registration.md)。保存用户XML不会创建认证账户。

### 执行录音：不能用日志或成功状态代替音频文件

原版 `uuid_record <uuid> start <path>` 对指定通道执行录音，必须结合原版返回、录音事件、最终音频与磁盘失败结果验证。RustSwitch 尚无此 API 或运行录音功能；会议/录音 XML 可以编辑导出，但 journal JSONL 不是音频。这里不提供会返回虚假 +OK 的桥接示例。

### 释放内部媒体：ACK 并非 ESL 命令结果

```json
{"id":7,"op":"release","session":42}
```

这是 Go→Rust 子进程 JSONL。`id` 关联操作、`session` 是 worker 内编号，成功返回内部 ack，端口随后仍可能处于隔离期。它不是 `uuid_kill`，不会提供原版通道原因、BACKGROUND_JOB 或 CHANNEL_HANGUP_COMPLETE；不可直接发送给 HTTP 或 ESL 客户端。

## 关键语义逐项对照

<a id="key-http-codecs"></a>

### 音频能力目录 · `key-http-codecs`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | show codecs / codec 模块 |
| 原版参数/帧 | api show codecs |
| 原版语义 | 列出原版实际加载的编解码实现；编码、解码、格式协商与实时转码必须分别验证。 |
| RustSwitch 入口 | GET /v1/codecs |
| 当前语义 | 读取同格式RTP配置与当前原生PCM SDK后端探测；每个控制实例缓存一次，缺库明确标记。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- HTTP JSON 不兼容 ESL show codecs 的帧与正文。
- 离线后端可用不表示通话转码已接入，不能原样加载 FreeSWITCH 模块。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/codecs.go:34。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7708](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7708)。

<a id="key-http-health"></a>

### 进程存活与版本 · `key-http-health`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | API version/status |
| 原版参数/帧 | api version [short] |
| 原版语义 | 原版 version 返回其构建/版本正文，受调用层 ESL 帧封装；status 是另一个状态命令。 |
| RustSwitch 入口 | GET /healthz |
| 当前语义 | HTTP 200 JSON 含 status=ok 与 RustSwitch 版本；只证明管理入口响应，不证明媒体分片可服务。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- HTTP 状态与 JSON 不是 api/response；不得伪装 FreeSWITCH 版本。
- 原 fs_cli 无法将此端点当作 ESL 连接。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/server.go:543。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7666](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7666)。

<a id="key-http-readiness"></a>

### 可准入状态 · `key-http-readiness`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | API status / fsctl |
| 原版参数/帧 | api status |
| 原版语义 | 原版状态正文包含其会话和运行信息；没有与本项目 /readyz 字段完全相同的内置 HTTP 合同。 |
| RustSwitch 入口 | GET /readyz |
| 当前语义 | JSON ready；未排空、日志健康、至少一个媒体分片可准入且 Guard 当前允许时为 200，否则 503。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 200 不预留下一通呼叫资源。
- 按当前保护策略预期拒接也会造成 503，不能只按进程存活判定故障。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/server.go:548。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7710](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7710)。

<a id="key-http-status"></a>

### 运行状态查询 · `key-http-status`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | API status / show channels |
| 原版参数/帧 | api status；api show channels |
| 原版语义 | status 与 show 提供原版状态及查询格式；通道记录与桥接通话数量需按原版字段区分。 |
| RustSwitch 入口 | GET /v1/status |
| 当前语义 | 返回 active_calls、established_calls、workers、guard、journal_*、配置版本等 JSON 快照；不返回原版 UUID 通道列表。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 一条 RustSwitch 桥接通话有 A/B 两腿，不能直接按原版 session/channel 数字比较。
- 轮询采样不能替代 CHANNEL_CREATE/ANSWER/HANGUP 事件。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/server.go:565。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7710](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7710)。

<a id="key-http-metrics"></a>

### 指标采集 · `key-http-metrics`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | API status / 启用的指标模块 |
| 原版参数/帧 | 按原版实际加载模块采集 |
| 原版语义 | 原版 status 和相关模块指标按各自字段与采集路径输出；不能假定 Prometheus 指标名一致。 |
| RustSwitch 入口 | GET /metrics |
| 当前语义 | Prometheus 文本输出 sip_*、calls_*、media/worker 和 rustswitch_guard_* 计数；计数区分累计值和即时值。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 既有监控面板需要字段映射，当前未提供原版指标别名。
- socket_rx_drops 不支持时零值不等于内核无丢包。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/server.go:577。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7710](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7710)。

<a id="key-http-authentication"></a>

### 管理鉴权与请求来源 · `key-http-authentication`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | ESL auth / ACL |
| 原版参数/帧 | auth &lt;password&gt;\n\n |
| 原版语义 | inbound ESL 先发 auth/request，再处理连接身份、ACL 和可用命令；认证状态属于 TCP 连接。 |
| RustSwitch 入口 | 回环 HTTP；GET /v1/config 获取 CSRF |
| 当前语义 | 检查 Host；写请求检查同源 Origin、application/json、X-RustSwitch-CSRF；没有多用户身份、ESL 密码或角色权限。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- CSRF token 不是登录凭据，不支持把远程服务直接暴露到公网。
- ESL 客户端认证帧不能发送到 HTTP 管理监听。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/admin.go:76。

原版定位：[src/mod/event_handlers/mod_event_socket/mod_event_socket.c:2728](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c#L2728)。

<a id="key-http-config-read"></a>

### 读取有效与待重启配置 · `key-http-config-read`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | global_getvar / 模块配置读取 |
| 原版参数/帧 | api global_getvar &lt;var&gt; |
| 原版语义 | 原版全局变量查询返回变量正文，与 XML 配置或模块已加载状态并非同一结构。 |
| RustSwitch 入口 | GET /v1/config |
| 当前语义 | 返回 revision、csrf_token、active、desired、restart_required、guard、persisted、notice；所有页共享 revision。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- active/desired 是完整启动 JSON，并非 FreeSWITCH 变量空间。
- 读取不会触发重载或自动合并旧草稿。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/admin.go:202。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7667](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7667)。

<a id="key-http-config-write"></a>

### 保存完整启动草稿 · `key-http-config-write`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | reloadxml / 模块重载 |
| 原版参数/帧 | api reloadxml |
| 原版语义 | 原版 reloadxml 处理 XML 树重载；模块是否重新读取配置及在途通话影响由各模块决定。 |
| RustSwitch 入口 | PUT /v1/config {revision,config} |
| 当前语义 | 完整校验后保存 desired；监听、端口和 worker 仍按 active 运行，下一次正常启动采用；程序和日志路径由部署文件管理。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 保存成功不等于即时应用，不能用作 reloadxml 的返回替代。
- 400/409/422/500 是 HTTP 管理错误，不是原版 +OK/-ERR 正文。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/admin.go:222。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7699](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7699)。

<a id="key-http-concurrency"></a>

### 并发提交与错误 · `key-http-concurrency`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 各原版 API 的独立并发语义 |
| 原版参数/帧 | 原版命令没有通用 HTTP revision 字段 |
| 原版语义 | 原版 API 不能被全局假设为具有统一事务版本或幂等键；错误正文和副作用需要逐命令定义。 |
| RustSwitch 入口 | 配置 PUT 的 revision |
| 当前语义 | 持管理锁比较版本；旧版本 409 并返回当前 revision；一次成功提交递增，保存失败不发布旧文件之外的新状态。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- ESL Job-UUID 是后台关联号，不能映射成配置 revision。
- 客户端不能拿新 revision 自动重发过期完整表单，否则会覆盖别人的修改。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/admin.go:214。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7699](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7699)。

<a id="key-esl-framing"></a>

### ESL 字节分帧 · `key-esl-framing`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | mod_event_socket TCP |
| 原版参数/帧 | 头行 + 空行 + Content-Length 字节正文 |
| 原版语义 | 命令响应与异步事件交错，按字节数分帧，认证和权限属于连接。 |
| RustSwitch 入口 | 可选 -esl-listen 回环入站 ESL |
| 当前语义 | Go 有界 TCP 解析器处理 LF/CRLF、分片、粘包、UTF-8 正文及认证；单写者保持帧完整。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 头上限64KiB与原版不同，正文上限16MiB。
- 仅回环密码认证，无userauth、远程ACL与outbound会话控制；实际差分见成对测试报告。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/esl/frame.go:39。

原版定位：[src/mod/event_handlers/mod_event_socket/mod_event_socket.c:1599](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c#L1599)。

<a id="key-esl-api"></a>

### 同步 API 调用 · `key-esl-api`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | ESL api |
| 原版参数/帧 | api &lt;command&gt; [args]\n\n |
| 原版语义 | 返回api/response帧与实际命令正文，完成传输不等于业务成功。 |
| RustSwitch 入口 | 入站ESL api：echo/create_uuid/version/status/show api as json/uuid_exists/uuid_getvar/uuid_setvar/uuid_setvar_multi/uuid_break/uuid_kill/uuid_dump/uuid_send_dtmf及自有uuid_send_dtmf_status |
| 当前语义 | 有限命令分派；UUID控制串行进入通话主循环并按UUID索引定位。未知命令返回-ERR，不伪造FreeSWITCH版本。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- status为RustSwitch实际状态格式；不是原版status输出。
- 普通变量受128个/64KiB预算限制；无数组、展开、全原因码、originate、conference或任意应用。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/server/esl_api.go:45。

原版定位：[src/mod/event_handlers/mod_event_socket/mod_event_socket.c:1599](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c#L1599)。

<a id="key-esl-bgapi"></a>

### 后台 API 与作业完成 · `key-esl-bgapi`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | ESL bgapi |
| 原版参数/帧 | bgapi &lt;command&gt; [args]；Job-UUID |
| 原版语义 | 受理与BACKGROUND_JOB完成分别回应，关联号不具备天然幂等语义。 |
| RustSwitch 入口 | 入站ESL bgapi及BACKGROUND_JOB |
| 当前语义 | 固定8执行器、有界128任务队列；Job-UUID两处确认，实际命令结果作为事件正文；重复关联号分别执行。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 只运行当前有限API集合；后台受理不保证业务成功。
- 任务不持久化；进程退出取消，未提供崩溃后任务恢复或原版所有作业字段。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/esl/server.go:567。

原版定位：[src/mod/event_handlers/mod_event_socket/mod_event_socket.c:1580](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c#L1580)。

<a id="key-esl-sendmsg"></a>

### 通道应用执行 · `key-esl-sendmsg`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | ESL sendmsg |
| 原版参数/帧 | sendmsg &lt;uuid&gt;；call-command: execute；execute-app-name / execute-app-arg |
| 原版语义 | 请求在指定通道执行应用，event-lock、执行 UUID 和 CHANNEL_EXECUTE_COMPLETE 等行为需联合对照。 |
| RustSwitch 入口 | sendmsg &lt;uuid&gt; / call-command: execute |
| 当前语义 | 已提供有界串行answer/set/unset/read/playback/sleep/park/hangup应用，真实执行及完成事件；read支持终止键、超时、提示音打断，挂断取消排队任务。 |
| 状态/关系 | `partial` / `direct_comparison` |

- 并非全部应用、sendmsg命令或全部并发/锁语义；仅支持受限WAV文件，仍无TTS和完整菜单XML。
- 仅支持列出的参数子集，错误/完成事件必须与实际媒体和变量结果联合验证。

验证：当前源码的限定用例证据见运行验证；完整原版合同、运营商互通和容量组合尚未全部通过。

原版定位：[src/mod/event_handlers/mod_event_socket/mod_event_socket.c:2180](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c#L2180)。

<a id="key-esl-subscriptions"></a>

### 事件订阅与过滤 · `key-esl-subscriptions`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | event / filter / myevents |
| 原版参数/帧 | event plain\|json\|xml &lt;events&gt;；filter &lt;header&gt; &lt;value&gt; |
| 原版语义 | 连接级订阅、头过滤、事件编码及慢消费者边界须联合验证。 |
| RustSwitch 入口 | 入站ESL事件订阅、普通值过滤、nixevent/noevents |
| 当前语义 | 从真实通话、应用及按键发布CHANNEL_CREATE/ANSWER/HANGUP、CHANNEL_EXECUTE/COMPLETE、PLAYBACK_START/STOP、CHANNEL_PARK/UNPARK和DTMF；后台任务发布BACKGROUND_JOB；本地呼入仅A腿。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 仅上述实际事件源；未完成CHANNEL_HANGUP_COMPLETE、CUSTOM、日志及myevents。
- 正则过滤、完整原版字段和多值头仍未实现；输出队列溢出断开连接以显式报告流中断。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/esl/server.go:604。

原版定位：[src/mod/event_handlers/mod_event_socket/mod_event_socket.c:1961](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c#L1961)。

<a id="key-esl-outbound"></a>

### Outbound ESL 会话与 linger · `key-esl-outbound`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | socket 应用 / outbound ESL |
| 原版参数/帧 | socket &lt;host:port&gt; [async] [full]；connect；linger |
| 原版语义 | 原版由通道连接业务服务；连接初始化、权限、断连与通话生命周期以及 linger 保留期均是可观察合同。 |
| RustSwitch 入口 | 无 outbound ESL |
| 当前语义 | 固定 SIP 上游不是 ESL 业务连接；管理 HTTP 客户端断开不会创建或接管原版通道应用。 |
| 状态/关系 | `not_implemented` / `no_compatible_entrypoint` |

- 原业务服务监听 socket 不能接入当前 RustSwitch。
- 活动 ESL TCP 状态在线迁移尚未实现。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/server/calls.go:53。

原版定位：[src/mod/event_handlers/mod_event_socket/mod_event_socket.c:2431](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c#L2431)。

<a id="key-config-runtime-xml"></a>

### XML 运行与预处理 · `key-config-runtime-xml`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | XML 树 / X-PRE-PROCESS |
| 原版参数/帧 | conf/freeswitch.xml 与 include/set 预处理 |
| 原版语义 | FreeSWITCH 在相应配置加载路径解析 XML，预处理变量与模块配置查找参与实际运行。 |
| RustSwitch 入口 | GET/PUT /v1/fs-config |
| 当前语义 | XML 仅作草稿解析、字段展示和导出；保留 X-PRE-PROCESS 文本，不执行 include、set 或模块加载。 |
| 状态/关系 | `export_only` / `export_artifact_only` |

- 响应明确 mode=export_only、runtime_supported=false。
- 表单覆盖不等于 XML 业务执行兼容。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/fs_config.go:182。

原版定位：[src/switch_xml.c:2537](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_xml.c#L2537)。

<a id="key-config-parameter-edit"></a>

### 逐字段编辑与原文保留 · `key-config-parameter-edit`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版 param/variable/action/condition 等 XML |
| 原版参数/帧 | 原始属性值和 $${...} 表达式 |
| 原版语义 | 不同标签、节点位置及执行顺序决定语义；相同 name 在不同 profile/绑定中可能重复出现。 |
| RustSwitch 入口 | PUT /v1/fs-config {revision,overrides:{ID:value}} |
| 当前语义 | 按当前字段 ID 从后往前替换属性值并做 XML 转义；保留未编辑文本；重复属性拒绝，命名空间元数据不混入字段。 |
| 状态/关系 | `export_only` / `export_artifact_only` |

- ID 是当前文件内字段序号，不能从原版行号或本对照 id 猜测。
- 结构修改后必须重新取目录及 revision。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/fs_config.go:378。

原版定位：[conf/vanilla/vars.xml:1](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/conf/vanilla/vars.xml#L1)。

<a id="key-config-raw-xml"></a>

### 高级 XML 保存 · `key-config-raw-xml`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | FreeSWITCH XML 文件 |
| 原版参数/帧 | 相对 XML 路径与原始内容 |
| 原版语义 | 原版配置最终是否可运行取决于模块 schema、预处理与环境；XML 语法正确不保证业务正确。 |
| RustSwitch 入口 | PUT /v1/fs-config/file {revision,path,xml} |
| 当前语义 | 支持修改/新增，多根片段；禁止目录穿越、DTD、非 UTF-8 声明、重复属性；256 KiB/文件、512 文件、5 MiB 总草稿。 |
| 状态/关系 | `export_only` / `export_artifact_only` |

- 未知文件 GET 404，非法 XML/路径 422；高级保存不会写入真实 FreeSWITCH conf 目录。
- 不会做全模块参数类型、凭证可用性或拨号计划结果验证。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/admin.go:405。

原版定位：[src/switch_xml.c:2537](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_xml.c#L2537)。

<a id="key-config-export"></a>

### 配置 ZIP 导出 · `key-config-export`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版 vanilla 配置树 |
| 原版参数/帧 | conf/ 目录 |
| 原版语义 | 原版配置包含模块、profile、目录与拨号计划，实际运行仍依赖对应构建和资源。 |
| RustSwitch 入口 | GET /v1/fs-config/export |
| 当前语义 | 导出已保存基线合并草稿的 ZIP，带许可与来源；189 份参考 XML，五份仅含 ASCII 的旧编码声明统一 UTF-8。 |
| 状态/关系 | `export_only` / `export_artifact_only` |

- 下载未部署、未 reloadxml，未保存的浏览器草稿不进入 ZIP。
- 4,304 个管理字段不等于 4,304 个不同功能。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/fs_config.go:440。

原版定位：[conf/vanilla/freeswitch.xml:1](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/conf/vanilla/freeswitch.xml#L1)。

<a id="key-config-directory"></a>

### 用户、网关与拨号计划 · `key-config-directory`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | directory / Sofia gateway / XML dialplan |
| 原版参数/帧 | user id；gateway；condition/action bridge |
| 原版语义 | 原版目录认证、网关注册和拨号计划执行分别由对应运行模块处理，并产生信令、通道及事件副作用。 |
| RustSwitch 入口 | 用户/网关/路由 XML 草稿和导出 |
| 当前语义 | 可生成查看XML文本但不执行；独立sip.registration可注册固定上游，local_extensions可精确本地接听，均不是XML配置的运行映射。 |
| 状态/关系 | `export_only` / `export_artifact_only` |

- 生成一个用户 XML 不会让 REGISTER 成功。
- 保存 bridge action 不会改变当前 SIP 上游或创建动态桥接。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/server/calls.go:53。

原版定位：[src/mod/applications/mod_dptools/mod_dptools.c:6822](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_dptools/mod_dptools.c#L6822)。

<a id="key-config-persistence"></a>

### 持久化与启动文件优先级 · `key-config-persistence`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版配置文件与模块重载 |
| 原版参数/帧 | 模块各自的配置加载合同 |
| 原版语义 | FreeSWITCH 不采用本项目的 desired/policy/fs_files 管理状态封装；不能假定相同重启覆盖顺序。 |
| RustSwitch 入口 | journal.path + .admin.json |
| 当前语义 | 临时文件同步后原子重命名；下次启动按启动文件摘要选 desired。部署文件改变时采用新 base，保留策略并只按新硬上限收紧，保留可读取的 XML 草稿。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 更改 journal.path 会改变状态文件位置，不自动查找迁移旧草稿。
- 目录 fsync 失败会警告；不承诺任何断电条件下零配置丢失。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/admin_store.go:63。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7699](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7699)。

<a id="key-guard-capacity"></a>

### 总并发硬边界与业务阈值 · `key-guard-capacity`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | fsctl max_sessions |
| 原版参数/帧 | fsctl max_sessions [value] |
| 原版语义 | 原版核心 session 上限作用于其会话资源；一通桥接常涉及多个通道，不能与桥接通话数直接换算。 |
| RustSwitch 入口 | PUT /v1/guard policy.max_active_calls |
| 当前语义 | 在线阈值不能超过 active/desired 的 limits.max_calls；计数为预留且尚未释放的桥接资源，包括建立阶段。降低阈值不会挂断已有通话。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- max_active_calls 与原版 max_sessions 口径不同。
- 当前存量大于新阈值时只拒绝新 INVITE，不削减存量。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/guard.go:207。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7663](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7663)。

<a id="key-guard-establishing"></a>

### 建立中保护 · `key-guard-establishing`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 核心会话/准入限制 |
| 原版参数/帧 | 须按原版具体 profile 与模块配置采集 |
| 原版语义 | 原版建立中会话统计及限制需要按已加载模块和通道状态确定；没有已证明等价的统一 establishing 字段。 |
| RustSwitch 入口 | policy.max_establishing_calls |
| 当前语义 | 采用 max(active_calls-established_calls,0)，包括结束后仍等待资源释放的呼叫；达到阈值拒绝新 INVITE。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 不能解释为每秒产生 200 OK 的最大数。
- 瞬时原子快照采用饱和减法，统计不是原版通道状态全集。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/guard.go:162。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7663](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7663)。

<a id="key-guard-rate-burst"></a>

### CPS 与突发令牌 · `key-guard-rate-burst`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | fsctl sps |
| 原版参数/帧 | fsctl sps [value] |
| 原版语义 | 原版核心每秒会话限制与其计数/定时实现关联；不能从名称 sps 推断相同令牌补充算法。 |
| RustSwitch 入口 | policy.calls_per_second / burst_calls |
| 当前语义 | 新 INVITE 通过准入时扣一个令牌；按单调时间补充并限制容量；重传、ACK、BYE 不重复扣令牌，保存不自动补满。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- CPS 是新准入速率，不是并发数或严格自然秒接通数。
- 瞬时突发可大于一秒平均量，但不越配置 burst。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/guard.go:223。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7663](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7663)。

<a id="key-guard-soft-throttle"></a>

### 负载接近阈值时缩速 · `key-guard-soft-throttle`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | fsctl min_idle_cpu / 原版准入策略 |
| 原版参数/帧 | 按原版核心及 profile 配置 |
| 原版语义 | 原版 CPU/会话准入需要按其实现与配置验证，不等同于本项目按两个资源比例进行线性缩速。 |
| RustSwitch 入口 | policy.soft_limit_ratio / minimum_rate_ratio |
| 当前语义 | 以 active/max_active 与 establishing/max_establishing 的较大值计算有效补充速率；enabled=false 仍保留启动硬容量/CPS/burst。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 默认 0.8 后开始缩速，0.9 时为基准的 55%；到完整阈值仍拒绝。
- 不调度或延迟已经产生的 200 OK。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/guard.go:170。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7663](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7663)。

<a id="key-guard-retry-after"></a>

### 过载拒绝与重试 · `key-guard-retry-after`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | SIP 过载响应与 profile 限制 |
| 原版参数/帧 | 503 / Retry-After，精确原因按原版场景采集 |
| 原版语义 | 原版不同资源/认证/路由失败可能有不同状态、头和原因映射；须冻结各场景的外部响应。 |
| RustSwitch 入口 | 新 INVITE 的 503 + Retry-After |
| 当前语义 | 保护、启动容量或无媒体可准入时使用当前建议间隔；同一事务缓存响应；对端自行决定新事务重试，无服务端等待队列。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- Retry-After 不保证届时一定存在容量。
- 其他 SIP 错误不能统一变成此过载响应。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/calls.go:184。

原版定位：[src/mod/endpoints/mod_sofia/mod_sofia.c:6854](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L6854)。

<a id="key-guard-drain-resume"></a>

### 排空与恢复新接入 · `key-guard-drain-resume`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | fsctl pause/resume/shutdown |
| 原版参数/帧 | pause/resume [inbound\|outbound]；shutdown elegant 等 |
| 原版语义 | 原版区分入向/出向暂停与不同退出模式，其任务、模块和通道退出条件需逐模式验证。 |
| RustSwitch 入口 | POST /v1/drain；POST /v1/resume |
| 当前语义 | 排空停止新准入，保留已有通话且不自动退出；恢复不要求 revision，已进入停止流程返回 409。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 没有原版全部方向选择、shutdown cancel/restart 模式。
- 排空完成不等于活动呼叫、ESL TCP 状态在线迁移。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/admin.go:133。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7663](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7663)。

<a id="key-sip-invite"></a>

### 可信中继两腿呼叫 · `key-sip-invite`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | Sofia endpoint / bridge/originate |
| 原版参数/帧 | INVITE + SDP；路由依 profile/dialplan |
| 原版语义 | 原版终端、网关、拨号计划和 originate 可选择不同目的地、变量与多腿路径，并产生原版通道事件。 |
| RustSwitch 入口 | 固定上游桥接；本地精确号码或sip.dialplan XML |
| 当前语义 | 启用XML后按context匹配且未匹配404；未启用时保留固定上游/精确号码。本地只有A腿，Rust分配成功200、正确ACK后执行应用或授权私有PCM流；200未获ACK到期向A发送BYE并释放。 |
| 状态/关系 | `partial` / `direct_comparison` |

- XML仅支持明确列出的条件和动作，无完整求值、Lua或FreeSWITCH originate。
- 已接入本地G.711流式PCM下行；完整ASR/LLM/TTS服务、非G.711实时转码、转接与完整路由仍未实现，私有PCM不等于原版speak。

验证：当前源码的限定用例证据见运行验证；完整原版合同、运营商互通和容量组合尚未全部通过。

原版定位：[src/mod/endpoints/mod_sofia/mod_sofia.c:6854](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L6854)。

<a id="key-sip-register"></a>

### 用户注册与 Digest · `key-sip-register`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | Sofia REGISTER / Digest |
| 原版参数/帧 | REGISTER；401/407 challenge；Authorization |
| 原版语义 | 原版注册路径处理注册位置、到期、认证、联系人及 profile/目录相关行为。 |
| RustSwitch 入口 | sip.trunk_auth + sip.registration |
| 当前语义 | 固定上游REGISTER客户端支持认证、Contact到期刷新、423调整、失败退避与注销；INVITE认证挑战先ACK再递增CSeq重试。 |
| 状态/关系 | `partial` / `direct_comparison` |

- 尚无终端注册服务器、位置数据库、多账户网关、DNS发现或原Sofia配置执行。
- 只支持已文档化Digest算法与qop=auth/旧式无qop，认证错误不能推导运营商线路已互通。

验证：当前源码的限定用例证据见运行验证；完整原版合同、运营商互通和容量组合尚未全部通过。

原版定位：[src/mod/endpoints/mod_sofia/sofia_reg.c:2305](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/sofia_reg.c#L2305)。

<a id="key-sip-transports"></a>

### 传输与 WebSocket · `key-sip-transports`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | Sofia profile传输配置 |
| 原版参数/帧 | SIP UDP/TCP/TLS/WS/WSS按构建启用 |
| 原版语义 | 原版传输涉及连接复用、证书、重连与SIP事务，不能只检查监听端口。 |
| RustSwitch 入口 | IPv4 SIP UDP及可选TCP/TLS双腿 |
| 当前语义 | sip.stream启用可靠传输；真实连接身份进入事务与缓存，固定队列和连接预算；TLS校验CA/名称。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 尚无WS/WSS、IPv6、DNS/SRV、客户端证书认证及完整SIPS路由。
- TLS仅保护信令，不表示SRTP/WebRTC已实现；INVITE 2xx端到端ACK重传仍保留。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/server/transport.go:36。

原版定位：[src/mod/endpoints/mod_sofia/mod_sofia.c:6854](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L6854)。

<a id="key-sip-renegotiation"></a>

### reINVITE/UPDATE 与媒体重协商 · `key-sip-renegotiation`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | Sofia 对话内重协商 |
| 原版参数/帧 | reINVITE / UPDATE + SDP |
| 原版语义 | 原版对话内更新可影响保持、编解码、远端地址、会话计时与媒体状态；竞态结果须按场景比较。 |
| RustSwitch 入口 | 对话内 INVITE → 501 |
| 当前语义 | 已有 To-tag 的重 INVITE 明确拒绝；内部 Connect 存在并不代表 SIP 重协商接口已支持。UPDATE 不在允许方法列表。 |
| 状态/关系 | `not_implemented` / `no_compatible_entrypoint` |

- hold/resume、codec 切换与 NAT 重协商不能无感沿用。
- 内部请求 id 不提供 SIP CSeq 对应的协商版本控制。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/server/calls.go:62。

原版定位：[src/mod/endpoints/mod_sofia/mod_sofia.c:6854](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L6854)。

<a id="key-sip-extensions"></a>

### PRACK、REFER、订阅与扩展 · `key-sip-extensions`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | Sofia SIP 扩展 |
| 原版参数/帧 | 100rel/PRACK；REFER；SUBSCRIBE/NOTIFY；Session-Expires |
| 原版语义 | 原版可靠临时响应、转接、订阅通知与会话刷新分别具有对话、计时和事件合同。 |
| RustSwitch 入口 | 受限 SIP 方法与扩展检查 |
| 当前语义 | PRACK/REFER/SUBSCRIBE/NOTIFY 等不在当前方法集；不支持的必需扩展/部分 SDP 情况明确拒绝。 |
| 状态/关系 | `not_implemented` / `no_compatible_entrypoint` |

- 当前早期媒体不意味着实现 PRACK/100rel。
- 没有盲转、咨询转接、会话刷新或订阅状态接管。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/server/calls.go:73。

原版定位：[src/mod/endpoints/mod_sofia/mod_sofia.c:6854](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L6854)。

<a id="key-sip-cancel"></a>

### CANCEL 与迟到成功应答 · `key-sip-cancel`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | Sofia INVITE/CANCEL 事务 |
| 原版参数/帧 | CANCEL；487；迟到 2xx 后 ACK/BYE |
| 原版语义 | 原版须区分事务取消、对话成功和分叉胜出腿，不能收到 CANCEL 就撤销所有成功会话。 |
| RustSwitch 入口 | 匹配初始 INVITE 的 CANCEL |
| 当前语义 | 匹配分支取消；最终应答已发则只确认 CANCEL；上游迟到 200 使用 ACK/BYE 清理。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 现有回归只覆盖固定上游受限两腿场景。
- 多目标 fork/CANCEL race 与原版完整允许结果集合仍未差分。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/calls.go:570。

原版定位：[src/mod/endpoints/mod_sofia/mod_sofia.c:6854](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L6854)。

<a id="key-sip-bye"></a>

### 正常拆线与原因码 · `key-sip-bye`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | SIP BYE / uuid_kill |
| 原版参数/帧 | BYE；api uuid_kill &lt;uuid&gt; [cause] |
| 原版语义 | 原版可由 SIP 或 API 指定挂机原因，影响 SIP/Reason、通道原因和挂机事件。 |
| RustSwitch 入口 | SIP BYE 与内部结束原因 |
| 当前语义 | 支持受限双腿 BYE/事务清理和内部 journal 原因；无 uuid_kill API，也没有完整 Q.850/SWITCH_CAUSE 映射。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 当前 reason 文本不能替代 hangup_cause/originate_disposition。
- 收到 200 不等于已完成所有原版 CDR/录音/事件副作用。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/calls.go:592。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7750](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7750)。

<a id="key-sip-sdp"></a>

### SDP 与编解码协商 · `key-sip-sdp`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | Sofia/core media SDP |
| 原版参数/帧 | m=audio；rtpmap/fmtp；方向；传输与安全属性 |
| 原版语义 | 原版 SDP 能力按编解码、媒体安全及 profile 配置组合选择；多媒体、方向和重协商均需验证。 |
| RustSwitch 入口 | sip.ParseSDP / RenderSDP |
| 当前语义 | relay保留同编码同PT协商；g711显式建立独立两腿报价，保留A腿G.711动态映射，B腿选择已报价静态0/8，183收紧与200恢复辅助集合不重置语音。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 不是完整 SDP offer/answer 实现，不能复用全部 FreeSWITCH 终端配置。
- 媒体目的 CIDR 校验不等于 ICE 或加密身份认证。

验证：候选限定实现与实际运行结果见processed-media-verification.json；生成目录不会授予绿色，当前不声明FreeSWITCH完整成对认证或已发布主服务。

原版定位：[src/mod/endpoints/mod_sofia/mod_sofia.c:6854](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L6854)。

<a id="key-media-rtp"></a>

### 主流音频同格式 RTP 转发 · `key-media-rtp`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版 RTP / proxy/bypass media |
| 原版参数/帧 | 按 SDP 与媒体模式建立 RTP |
| 原版语义 | 原版媒体模式可能处理、代理或绕过 RTP；序列号、SSRC、时间戳、jitterbuffer 与 RTP 重写依场景而定。 |
| RustSwitch 入口 | 默认relay；可选g711终结型双向媒体图 |
| 当前语义 | relay校验后转发原数据报；g711模式逐腿解码/编码，使用新的SSRC、包序和时间映射，缺失/迟到/重复分别处理。本地RX不反射，tone/WAV/流式PCM和主动DTMF共享本地TX身份，换turn不重置SSRC/包序/时钟。Linux批量接收继续保留。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 两种模式使用不同媒体合同，不能概括为全部FreeSWITCH proxy/bypass/normal行为。
- 本地UDP提交和功能回归不构成远端零丢包或万路性能承诺。

验证：候选限定实现与实际运行结果见processed-media-verification.json；生成目录不会授予绿色，当前不声明FreeSWITCH完整成对认证或已发布主服务。

原版定位：[src/switch_rtp.c:1](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_rtp.c#L1)。

<a id="key-media-rtcp"></a>

### RTCP 转发与统计 · `key-media-rtcp`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版 RTCP |
| 原版参数/帧 | 独立/复用 RTCP 按协商配置 |
| 原版语义 | 原版可处理 RTCP 报告及媒体质量相关行为，具体生成、转发、复用依模式与协商。 |
| RustSwitch 入口 | relay原包转发；g711模式逐腿SR/RR/SDES/BYE |
| 当前语义 | 处理图用实际RX/TX计数生成复合报告，保持SR媒体时钟不被DTMF或迟到CN回拨；本地流式PCM使用同一TX计数和时钟，单个turn结束不结束RTP会话，Release尝试发送BYE。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 只使用独立RTCP端口；rtcp-mux、SRTCP、完整反馈、质量事件与带宽自适应周期未完成。
- 本地成对端点实测与FreeSWITCH原版成对认证是不同证据。

验证：候选限定实现与实际运行结果见processed-media-verification.json；生成目录不会授予绿色，当前不声明FreeSWITCH完整成对认证或已发布主服务。

原版定位：[src/switch_rtp.c:1](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_rtp.c#L1)。

<a id="key-media-dtmf"></a>

### DTMF 的媒体与业务层 · `key-media-dtmf`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | telephone-event / SIP INFO / 应用收号 |
| 原版参数/帧 | RTP telephone-event；INFO；应用 DTMF 队列 |
| 原版语义 | 原版支持的 DTMF 传输、生成、检测及应用收号会影响媒体、通道队列和业务事件。 |
| RustSwitch 入口 | RTP telephone-event/显式INFO接收；本地A腿RTP按键发送 |
| 当前语义 | 接收按协商源/PT/clock/events去重完成按键；INFO要求有效对话和显式Duration。发送支持有界digits[@ms]子集并共用本地播放SSRC/序号；不修改透明桥接包。 |
| 状态/关系 | `partial` / `direct_comparison` |

- 不提供音内检测、桥接/B腿或INFO发送、完整长事件分段和全部INFO格式。
- read缓冲时序、全部原版事件字段和运营商终端差异仍需逐项对照。

验证：当前源码的限定用例证据见运行验证；完整原版合同、运营商互通和容量组合尚未全部通过。

原版定位：[src/mod/applications/mod_dptools/mod_dptools.c:6790](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_dptools/mod_dptools.c#L6790)。

<a id="key-media-transcoding"></a>

### 转码通话 · `key-media-transcoding`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | FreeSWITCH codec interfaces |
| 原版参数/帧 | 协商两腿不同 codec/ptime |
| 原版语义 | 原版通过编解码实现与媒体路径连接不同格式，需对帧时长、丢包处理、缓冲及质量进行测试。 |
| RustSwitch 入口 | media.processing=g711：PCMU/PCMA 8k/20ms，bridge/local独立握手 |
| 当前语义 | 真实双腿图重排→8k原生PCM→目标G.711编码→独立RTP封装；本地图将tone/WAV或有界流式PCM编码到A，活动供音互斥且共享按键TX身份。可独立订阅A侧原生8k RX，输出真实解码/PLC/CN/缺口元数据；按需分支重采样的外部ASR适配仍未接入，默认relay继续透传。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- G.711双腿处理、本地播放、流式PCM与原生RX各有独立验收材料，不能互相传播通过状态。G.722/Opus实时处理、双腿混入、外部ASR/TTS适配、VAD与录音仍未接入。
- 内部RX与单路SIP逐样本验证不等于原版媒体钩子/录音兼容、原版成对音质或5000/10000路Agent容量；详见api/rx-stream-reference.md。

验证：候选限定实现与实际运行结果见processed-media-verification.json；生成目录不会授予绿色，当前不声明FreeSWITCH完整成对认证或已发布主服务。

原版定位：[src/include/switch_core.h:1707](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_core.h#L1707)。

<a id="key-media-security"></a>

### SRTP、DTLS 与 ICE · `key-media-security`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版安全媒体/WebRTC |
| 原版参数/帧 | SRTP/DTLS-SRTP；ICE/STUN/TURN 按模块配置 |
| 原版语义 | 原版安全媒体需要协商密钥、认证报文与连接候选，并按安全配置管理生命周期。 |
| RustSwitch 入口 | 当前普通 UDP RTP |
| 当前语义 | 未实现加解密、DTLS 握手、ICE 候选检查或 TURN；可信网段和固定来源过滤不提供等价安全保证。 |
| 状态/关系 | `not_implemented` / `no_compatible_entrypoint` |

- 不能接替需要 WebRTC/SRTP 的现有终端。
- 不得把媒体 IP 白名单称为媒体身份认证。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 media/src/media/worker.rs:1481。

原版定位：[src/switch_rtp.c:1](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch_rtp.c#L1)。

<a id="key-media-nat"></a>

### NAT 对端学习与迁移 · `key-media-nat`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | Sofia NAT / symmetric RTP |
| 原版参数/帧 | profile NAT 设置与实际源学习 |
| 原版语义 | 原版 NAT 行为依 profile、终端 Contact、SDP、连接及媒体来源策略共同确定。 |
| RustSwitch 入口 | 固定协商对端 + 可选 connected UDP |
| 当前语义 | 只接受协商并经过 CIDR 校验的来源；不自动学习新 NAT 源地址。对端变化时清理旧待发队列。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- NAT 端口改变会被拒绝，不能声称移动终端无感。
- connected socket 由内核提前过滤的包不会全部反映在应用 source_rejected 中。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 media/src/media/worker.rs:1481。

原版定位：[src/mod/endpoints/mod_sofia/mod_sofia.c:6854](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L6854)。

<a id="key-media-conference"></a>

### 会议与成员控制 · `key-media-conference`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | conference API / application |
| 原版参数/帧 | conference &lt;name&gt; &lt;subcommand&gt; [args] |
| 原版语义 | 原版会议维护房间、成员、混音/视频、成员权限及会议事件；静态表包含 84 个分发项。 |
| RustSwitch 入口 | 会议 XML 编辑/导出 |
| 当前语义 | 没有运行会议对象、成员管理、混音或会议事件；不能使用原 conference 命令控制媒体 worker。 |
| 状态/关系 | `export_only` / `export_artifact_only` |

- 130 子命令目录中的会议/Sofia 项均未成为当前运行接口。
- 会议容量与点对点透传容量必须分别验收。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/server/fs_config.go:426。

原版定位：[src/mod/applications/mod_conference/mod_conference.c:4037](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/mod_conference.c#L4037)。

<a id="key-media-recording"></a>

### 录音 API、事件和文件 · `key-media-recording`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | uuid_record / record_session |
| 原版参数/帧 | uuid_record &lt;uuid&gt; start\|stop\|mask\|unmask &lt;path&gt; ... |
| 原版语义 | 原版录音合同包括文件内容、声道、格式、错误、停止时机、变量和相关事件，不能只返回命令成功。 |
| RustSwitch 入口 | 录音 XML 编辑/导出；无录音执行 |
| 当前语义 | 现有 journal 是文本事件日志，不是通话音频文件；没有录音句柄、掩码、录音事件或音频产物。 |
| 状态/关系 | `export_only` / `export_artifact_only` |

- 不能用 journal_written_events 证明录音成功。
- 磁盘满、转接跟随、文件完整性与原后端对接尚未实现。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/server/fs_config.go:426。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7770](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7770)。

<a id="key-media-capacity"></a>

### 一万路功能组合验收 · `key-media-capacity`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版负载基线 + 业务场景 |
| 原版参数/帧 | 并发、CPS、ptime、codec、功能组合、时长与故障条件 |
| 原版语义 | 原版实际构建、配置和业务组合决定容量，必须采集真实媒体及控制负载后比较。 |
| RustSwitch 入口 | callbench / local_bench.py / collect_linux.py |
| 当前语义 | 1.9新增本地按键发送与可变采样率SDK后尚无5000路完整Agent组合验收；上轮1.5最新5000路复测未通过：最新仅提供48.14184%标称媒体量，另有8次发生器发送写错误。1.4两轮成功仅为历史记录，不沿用当前容量绿色；万路、物理服务器及转码/会议/录音组合仍未验证。 |
| 状态/关系 | `not_verified` / `analogous_not_wire_compatible` |

- 并发信令会话数不能替代双向媒体 100 万包/秒量级的真实提交与接收核对。
- 透传、转码、会议、录音和事件订阅需分 profile 验收。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/cmd/callbench/main.go:472。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7710](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7710)。

<a id="key-media-failover"></a>

### 故障隔离与活动状态迁移 · `key-media-failover`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版恢复/切换合同 |
| 原版参数/帧 | 按实际部署定义故障与恢复行为 |
| 原版语义 | 是否支持恢复取决于原版模块、状态存储与切换方式，不能假定任何服务器可无损复制活动 SIP/媒体/ESL 状态。 |
| RustSwitch 入口 | Rust worker 分进程监管与重启 |
| 当前语义 | 失败分片上的呼叫会异常结束并清理；其他分片可继续。控制进程退出会结束媒体子进程，尚无活动会话或 ESL TCP 在线迁移。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 进程重启恢复新服务能力不等于恢复同一通活动呼叫。
- 排空旧呼叫后切换与不停话在线迁移必须分别验收。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/internal/server/calls.go:730。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7663](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7663)。

<a id="key-config-dialplan-runtime"></a>

### XML 本地拨号计划执行 · `key-config-dialplan-runtime`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | mod_dialplan_xml / mod_dptools |
| 原版参数/帧 | context → extension → condition → action |
| 原版语义 | 原版XML支持条件、应用、预处理和模块绑定；实际匹配和执行产生SIP/媒体副作用。 |
| RustSwitch 入口 | sip.dialplan {file,context} |
| 当前语义 | 启动冻结受限XML；destination_number首个RE2条件命中后顺序执行answer/set/unset/read/playback/sleep/park/hangup；未匹配404，ACK后继续，关闭ESL仍可运行。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 仅单条件且首动作为answer，无预处理、外部include、反向动作、变量/捕获展开、continue或reloadxml。
- park不可处理原版私有抢占事件；无bridge/originate/transfer及全部原版应用。

验证：限定真实场景见dialplan-reference与本地/原版成对记录。；RustSwitch 源码 control/internal/server/dialplan.go:1。

原版定位：[src/mod/applications/mod_dptools/mod_dptools.c:6674](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_dptools/mod_dptools.c#L6674)。

<a id="key-ipc-playback-file-start"></a>

### 内部 WAV 文件放音 · `key-ipc-playback-file-start`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版核心/endpoint 内部调用 |
| 原版参数/帧 | FreeSWITCH 内部 C 调用，不存在该 JSONL 指令 |
| 原版语义 | 原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。 |
| RustSwitch 入口 | {id,op:playback_file_start,session,playback_id,leg,path} |
| 当前语义 | 根内8k单声道PCM16/PCMA/PCMU WAV有界加载，1MiB/30秒与固定队列/缓存/任务预算；loading不代表已发包。本地处理图直接编码原PCM；活动PCM轮次与WAV双向互斥，过期加载结果不复活旧任务。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。
- 请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。

验证：静态合同已定位；内部PCM与外部SIP授权分别取证。外部候选sip-02的22方法、Go race 899项和旧24阶段228方法是独立结果，不能相加为本条完整兼容认证；当前源码绿色由独立runtime证据门禁判定，生成目录不授予通过状态

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-dtmf-events"></a>

### 内部按键事件游标 · `key-ipc-dtmf-events`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版核心/endpoint 内部调用 |
| 原版参数/帧 | FreeSWITCH 内部 C 调用，不存在该 JSONL 指令 |
| 原版语义 | 原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。 |
| RustSwitch 入口 | {id,op:dtmf_events,after_seq,limit} |
| 当前语义 | 按worker读取固定1024事件环，最多64条；结束包去重，缺失结束/溢出明确报告，不能当作用户沉默。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。
- 请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 media/src/media/protocol.rs:126。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-playback-start"></a>

### 内部提示音启动 · `key-ipc-playback-start`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版核心/endpoint 内部调用 |
| 原版参数/帧 | FreeSWITCH 内部 C 调用，不存在该 JSONL 指令 |
| 原版语义 | 原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。 |
| RustSwitch 入口 | {id,op:playback_start,session,playback_id,leg,frequency_hz,duration_ms} |
| 当前语义 | PCMU/PCMA真实20ms单音，200..2000Hz、20..10000ms，每worker最多64路；本地A无需B。处理图直接编码原PCM，共用本地TX身份；活动PCM轮次与tone双向互斥，受理或真正启动前都不能抢走既有TX所有权。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。
- 请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。

验证：静态合同已定位；内部PCM与外部SIP授权分别取证。外部候选sip-02的22方法、Go race 899项和旧24阶段228方法是独立结果，不能相加为本条完整兼容认证；当前源码绿色由独立runtime证据门禁判定，生成目录不授予通过状态

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-playback-status"></a>

### 内部提示音状态 · `key-ipc-playback-status`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版核心/endpoint 内部调用 |
| 原版参数/帧 | FreeSWITCH 内部 C 调用，不存在该 JSONL 指令 |
| 原版语义 | 原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。 |
| RustSwitch 入口 | {id,op:playback_status,session,playback_id} |
| 当前语义 | 读取实际sent_packets/total_packets及loading/running/completed/stopped/failed，不把启动确认当播放完成。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。
- 请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 media/src/media/protocol.rs:147。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-playback-stop"></a>

### 内部提示音停止 · `key-ipc-playback-stop`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版核心/endpoint 内部调用 |
| 原版参数/帧 | FreeSWITCH 内部 C 调用，不存在该 JSONL 指令 |
| 原版语义 | 原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。 |
| RustSwitch 入口 | {id,op:playback_stop,session,playback_id} |
| 当前语义 | 仅停止匹配playback_id的传统tone/WAV，有界幂等；本地停止后无旧播放输出，桥接路径按既有合同恢复音频。不会停止另一PCM turn，流式供音必须使用pcm_turn_interrupt；主动停止不标为完整播放。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。
- 请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。

验证：静态合同已定位；内部PCM与外部SIP授权分别取证。外部候选sip-02的22方法、Go race 899项和旧24阶段228方法是独立结果，不能相加为本条完整兼容认证；当前源码绿色由独立runtime证据门禁判定，生成目录不授予通过状态

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-ready"></a>

### 内部启动握手 · `key-ipc-ready`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版核心/endpoint 内部调用 |
| 原版参数/帧 | FreeSWITCH 内部 C 调用，不存在该 JSONL 指令 |
| 原版语义 | 原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。 |
| RustSwitch 入口 | Ready {id:0,ok:true,type:ready,protocol_version:1,worker_id,pid,capabilities} |
| 当前语义 | worker固定id=0、protocol_version=1、真实worker_id/pid及精确能力；双腿需processed_g711_v1，本地另需processed_g711_local_v1。独立FD3可用时声明pcm_turn_v1；独立FD4且共享时钟域受支持时才声明rx_g711_local_v2，不接受旧RXS1能力。Go另核验健康代次、真实分配和正确ACK；ready不代表ASR/TTS可用或容量通过。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。
- 请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。

验证：源码与独立验收材料已关联：Rust06的RXS2普通UDP/connected各12方法；Server SDK03与Rust08的server-sip-03含PCMU/PCMA×两种UDP模式四个真实SIP子例，检查正确ACK授权、逐样本RX、BYE撤读、真实资源归零与端口复绑。两批候选身份分别保留；本地绿色由独立运行证据门禁判定，目录生成不等于执行或原版认证；RustSwitch 源码 media/src/media/protocol.rs:208。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-allocate"></a>

### 内部分配媒体会话 · `key-ipc-allocate`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版核心/endpoint 内部调用 |
| 原版参数/帧 | FreeSWITCH 内部 C 调用，不存在该 JSONL 指令 |
| 原版语义 | 原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。 |
| RustSwitch 入口 | {id,op:allocate,session,a,payload,codec,processing?,…} |
| 当前语义 | 处理计划分配时固定；Go核验ready及processing_version=1，local另需processing_topology=local且禁止B Connect。分配不创建PCM轮次、不授权SIP供音；begin和独立PCM能力另检。同session同参数返回原分配，冲突拒绝；relay省略处理计划。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。
- 请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。

验证：静态合同已定位；内部PCM与外部SIP授权分别取证。外部候选sip-02的22方法、Go race 899项和旧24阶段228方法是独立结果，不能相加为本条完整兼容认证；当前源码绿色由独立runtime证据门禁判定，生成目录不授予通过状态

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-connect"></a>

### 内部设置 B 腿 · `key-ipc-connect`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版核心/endpoint 内部调用 |
| 原版参数/帧 | FreeSWITCH 内部 C 调用，不存在该 JSONL 指令 |
| 原版语义 | 原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。 |
| RustSwitch 入口 | {id,op:connect,session,b:{rtp,rtcp},dtmf_payload} |
| 当前语义 | 处理模式B腿独立G.711协商；相同目标可幂等确认，辅助格式可收紧恢复，已连接主格式与对端不能随意改变。relay保持既有合同。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。
- 请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。

验证：候选限定实现与实际运行结果见processed-media-verification.json；生成目录不会授予绿色，当前不声明FreeSWITCH完整成对认证或已发布主服务。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-release"></a>

### 内部资源释放 · `key-ipc-release`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版核心/endpoint 内部调用 |
| 原版参数/帧 | FreeSWITCH 内部 C 调用，不存在该 JSONL 指令 |
| 原版语义 | 原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。 |
| RustSwitch 入口 | {id,op:release,session} |
| 当前语义 | 不存在会话也ack，允许重试；清理本会话PCM/RX队列和定时项、旧播放/按键、观察图及socket，处理模式尝试RTCP BYE。RX末尾摘要不阻塞释放，端口按隔离期复用；Go先撤销读写权，再由实际Release或原代次死亡确认媒体所有权消失，旧token/订阅不能进入新会话。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。
- 请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。

验证：源码与独立验收材料已关联：Rust06的RXS2普通UDP/connected各12方法；Server SDK03与Rust08的server-sip-03含PCMU/PCMA×两种UDP模式四个真实SIP子例，检查正确ACK授权、逐样本RX、BYE撤读、真实资源归零与端口复绑。两批候选身份分别保留；本地绿色由独立运行证据门禁判定，目录生成不等于执行或原版认证；RustSwitch 源码 media/src/media/protocol.rs:124。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-stats"></a>

### 内部 worker 统计 · `key-ipc-stats`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版核心/endpoint 内部调用 |
| 原版参数/帧 | FreeSWITCH 内部 C 调用，不存在该 JSONL 指令 |
| 原版语义 | 原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。 |
| RustSwitch 入口 | {id,op:stats} → {id,ok,type:stats,stats:{...}} |
| 当前语义 | 返回真实进程累计量与即时资源；新增rx_active_subscriptions/rx_queued_events/rx_observation_storage_bytes/rx_observation_failed分别表示活动订阅、在队观察、已分配观察存储及Graph失败状态。local consumed含真实解码和有限PLC，不是下行供音。每订阅生产/提交/丢弃由rx_status对账，Go读取/拒绝由Snapshot另计；PCM轮次由pcm_turn_status查询。重启分代归零，提交不等于远端接收或ASR识别。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。
- 请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。

验证：源码与独立验收材料已关联：Rust06的RXS2普通UDP/connected各12方法；Server SDK03与Rust08的server-sip-03含PCMU/PCMA×两种UDP模式四个真实SIP子例，检查正确ACK授权、逐样本RX、BYE撤读、真实资源归零与端口复绑。两批候选身份分别保留；本地绿色由独立运行证据门禁判定，目录生成不等于执行或原版认证；RustSwitch 源码 media/src/media/protocol.rs:179。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-shutdown"></a>

### 内部 worker 停止 · `key-ipc-shutdown`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版核心/endpoint 内部调用 |
| 原版参数/帧 | FreeSWITCH 内部 C 调用，不存在该 JSONL 指令 |
| 原版语义 | 原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。 |
| RustSwitch 入口 | {id,op:shutdown} → ack |
| 当前语义 | 确认后退出事件循环，不等待活动呼叫自然结束；正常业务排空由 Go 控制面协调。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。
- 请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 media/src/media/protocol.rs:190。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-c_abi-codec"></a>

### 自定义编解码 ABI · `key-c_abi-codec`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | switch_codec_interface_t / codec init |
| 原版参数/帧 | 原版 codec 模块与核心编解码调用 |
| 原版语义 | 原版编解码依赖模块注册、内存池、codec 结构及核心调用约定；头文件名或算法相同不产生二进制兼容。 |
| RustSwitch 入口 | rs_codec_get_v1 → rs_codec_v1 |
| 当前语义 | 版本/结构大小、int16 PCM、create/destroy/encode/decode、显式容量与输出长度；库需保持加载，上下文精确销毁，异常不可跨 ABI。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 示例 G.711 往返已验证，当前通话路径不调用此插件。
- 不导出 switch_* 宿主符号，不可原样装载 mod_*.so。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 native/include/rustswitch_codec.h:21。

原版定位：[src/include/switch_core.h:1707](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_core.h#L1707)。

<a id="key-c_abi-protocol-provider"></a>

### C 协议提供器预留边界 · `key-c_abi-protocol-provider`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版 endpoint 模块 ABI |
| 原版参数/帧 | switch_endpoint_interface_t 与 Sofia 提供器生命周期 |
| 原版语义 | 原版 endpoint 依赖会话回调、核心结构、模块注册和运行上下文；仅保留 C 链接约定不足以兼容。 |
| RustSwitch 入口 | rs_protocol_v1 {create,submit,poll_event,destroy} |
| 当前语义 | 仅有自定义头文件；实际装载入口、JSON schema、版本协商与 Sofia/PJSIP 适配器尚未实现。poll_event 的 1/0/负值仅为预留契约。 |
| 状态/关系 | `not_implemented` / `no_compatible_entrypoint` |

- 不存在已可运行的 C 协议提供器或原版 Sofia 二进制宿主。
- C/C++ 库复用必须独立说明 N-LIB/N-SRC/N-BIN/N-HOST 范围。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 native/include/rustswitch_protocol.h:18。

原版定位：[src/include/switch_loadable_module.h:64](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L64)。

<a id="key-cli-fs-cli"></a>

### 原版 fs_cli 不改动连接 · `key-cli-fs-cli`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | fs_cli / libesl |
| 原版参数/帧 | fs_cli -x 'status'；交互命令/补全/日志/事件 |
| 原版语义 | 原版客户端依赖 ESL 认证、帧、命令、补全与事件处理，交互模式比单条 status 请求覆盖更多能力。 |
| RustSwitch 入口 | 可选 ESL 监听：fs_cli -x 的 console_execute 单 API 子集 |
| 当前语义 | 已接入现有14个API的单命令包装（包含1个自有DTMF状态扩展）；版本保持RustSwitch身份，status为自有摘要。批处理、别名与完整交互尚未实现。 |
| 状态/关系 | `partial` / `analogous_not_wire_compatible` |

- 仅限定单命令路径；双分号批处理和别名在执行任何API前拒绝。
- 原客户端成功退出不能代替正文与副作用验证；完整交互、补全、日志和所有API仍待补齐。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/esl/console.go:39。

原版定位：[libs/esl/fs_cli.c:1](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/libs/esl/fs_cli.c#L1)。

<a id="key-cli-config-check"></a>

### 启动与无监听校验 · `key-cli-config-check`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | freeswitch 启动 CLI |
| 原版参数/帧 | 原版启动参数与退出选项按现场冻结 |
| 原版语义 | 原版启动选项控制目录、模块、控制台、前后台和进程退出，其安装/service 合同需单列验证。 |
| RustSwitch 入口 | bin/rustswitch -config &lt;json&gt; [-check-config] |
| 当前语义 | 读取启动 JSON 及对应管理状态；校验模式不绑定 socket/启动 worker；普通模式首次信号排空，第二次信号强制取消。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 不会接受全部 freeswitch 原参数、PID/目录/service 约定。
- 校验通过不证明目标端口当前可绑定或达到容量 SLO。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/cmd/rustswitch/main.go:21。

原版定位：[src/switch.c:1](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/switch.c#L1)。

<a id="key-cli-callbench"></a>

### 压测生成器与通过条件 · `key-cli-callbench`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版系统观测/外部负载发生器 |
| 原版参数/帧 | 状态查询不是负载验收 |
| 原版语义 | 原版 status 不会独立证明端到端媒体正确；需同一负载下核对发生器、网络和服务资源证据。 |
| RustSwitch 入口 | bin/callbench --calls ... --cps ... --seconds ... |
| 当前语义 | 模拟主被叫，核对 SSRC、源地址、时间戳、载荷、唯一接收和拆线。passed 要求负载≥98%、全部建立、零缺包/重复/非法/写错/意外 BYE/拆线失败。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 乱序与 late_ticks 当前只报告，不单独判失败；direct-media 仅诊断发生器/网络。
- socket-groups、spread、connected 等改变负载形态，报告必须保留这些字段。

验证：实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过；RustSwitch 源码 control/cmd/callbench/main.go:472。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7710](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7710)。

<a id="key-esl-dtmf-send-status"></a>

### 本地按键发送进度查询 · `key-esl-dtmf-send-status`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | uuid_send_dtmf及核心媒体发送队列（仅概念对应） |
| 原版参数/帧 | api uuid_send_dtmf &lt;uuid&gt; &lt;dtmf_data&gt; |
| 原版语义 | 原版API提交发送数据；不存在这里定义的同名状态扩展或JSONL内部合同。实际线协议与RTP副作用需分开验证。 |
| RustSwitch 入口 | api uuid_send_dtmf_status &lt;uuid&gt; |
| 当前语义 | 返回实际媒体受理/完成/失败/排队数字、发送包数和错误；这是RustSwitch自有扩展，完成仅表示UDP提交成功。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 仅支持已ACK本地A腿；桥接、B腿和SIP INFO发送拒绝。
- 受理不等于远端收到或播放，超时在途请求需查询状态或挂断清理；不是完整FreeSWITCH发送语法。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/server/esl_send_dtmf.go:48。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7774](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7774)。

<a id="key-ipc-dtmf-send"></a>

### 内部按键发送 · `key-ipc-dtmf-send`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | uuid_send_dtmf及核心媒体发送队列（仅概念对应） |
| 原版参数/帧 | api uuid_send_dtmf &lt;uuid&gt; &lt;dtmf_data&gt; |
| 原版语义 | 原版API提交发送数据；不存在这里定义的同名状态扩展或JSONL内部合同。实际线协议与RTP副作用需分开验证。 |
| RustSwitch 入口 | {id,op:dtmf_send,session,leg:a,digits,duration_ms} |
| 当前语义 | 对本地A腿整批受理1..32个协商按键；每会话32数字、每worker64活动发送器，调度迟到或发送失败明确结束。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅支持已ACK本地A腿；桥接、B腿和SIP INFO发送拒绝。
- 受理不等于远端收到或播放，超时在途请求需查询状态或挂断清理；不是完整FreeSWITCH发送语法。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 media/src/media/dtmf_sender.rs:92。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7774](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7774)。

<a id="key-ipc-dtmf-send-status"></a>

### 内部按键发送进度 · `key-ipc-dtmf-send-status`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | uuid_send_dtmf及核心媒体发送队列（仅概念对应） |
| 原版参数/帧 | api uuid_send_dtmf &lt;uuid&gt; &lt;dtmf_data&gt; |
| 原版语义 | 原版API提交发送数据；不存在这里定义的同名状态扩展或JSONL内部合同。实际线协议与RTP副作用需分开验证。 |
| RustSwitch 入口 | {id,op:dtmf_send_status,session} |
| 当前语义 | dtmf_send_state返回媒体队列真实计数；不可能的完成/包数和部分受理回执由Go拒绝，释放后不能继续查询已删除会话。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅支持已ACK本地A腿；桥接、B腿和SIP INFO发送拒绝。
- 受理不等于远端收到或播放，超时在途请求需查询状态或挂断清理；不是完整FreeSWITCH发送语法。

验证：静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告；RustSwitch 源码 control/internal/media/interaction.go:20。

原版定位：[src/mod/applications/mod_commands/mod_commands.c:7774](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c#L7774)。

<a id="key-ipc-pcm-turn-begin"></a>

### 内部PCM轮次启动 · `key-ipc-pcm-turn-begin`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版endpoint/媒体内部接口（仅层次参考） |
| 原版参数/帧 | FreeSWITCH没有pcm_turn_* JSONL或RSP1/RSR1指令 |
| 原版语义 | 固定源码用于定位原版内部媒体接口层次；对象、回调及锁模型与本项目二进制队列不同，不存在逐消息兼容关系。 |
| RustSwitch 入口 | {id,op:pcm_turn_begin,session,turn_id,buffer_ms,prebuffer_ms} → pcm_turn_state |
| 当前语义 | turn_id非零且新轮严格递增；同turn同配置幂等返回原状态、不复活。buffer为20..1000ms且步长20，prebuffer为20..buffer且步长20，物理队列至多50帧。新轮原子取消旧队列；与running/loading的tone/WAV互斥。依赖pcm_turn_v1与有效FD3，预缓冲2秒不足明确失败。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅本地G.711 8k mono 20ms图；不是FreeSWITCH同名命令、speak、录音或ASR上行接口。
- 内部session不是SIP UUID；Rust媒体层不拥有SIP ACK，外部Go入口另行核验正确ACK、分配及worker代次。
- 接受、关闭输入、Go句柄退休、Rust实际终态和远端收到是不同事实；结果未知需用原身份查询/停止，不盲重发音频。

验证：静态合同已定位；内部PCM与外部SIP授权分别取证。外部候选sip-02的22方法、Go race 899项和旧24阶段228方法是独立结果，不能相加为本条完整兼容认证；当前源码绿色由独立runtime证据门禁判定，生成目录不授予通过状态；RustSwitch 源码 media/src/media/protocol.rs:151。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-pcm-turn-end"></a>

### 内部PCM生产结束 · `key-ipc-pcm-turn-end`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版endpoint/媒体内部接口（仅层次参考） |
| 原版参数/帧 | FreeSWITCH没有pcm_turn_* JSONL或RSP1/RSR1指令 |
| 原版语义 | 固定源码用于定位原版内部媒体接口层次；对象、回调及锁模型与本项目二进制队列不同，不存在逐消息兼容关系。 |
| RustSwitch 入口 | {id,op:pcm_turn_end,session,turn_id,final_samples} → pcm_turn_state |
| 当前语义 | final_samples必须等于本轮accepted_samples，且为160整倍数；错误偏移不结束输入。成功关闭输入，未达预缓冲也排空已有整帧，空轮可立即completed；非空仅最后一帧实际提交UDP后满20ms才completed。无部分尾帧或隐式padding。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅本地G.711 8k mono 20ms图；不是FreeSWITCH同名命令、speak、录音或ASR上行接口。
- 内部session不是SIP UUID；Rust媒体层不拥有SIP ACK，外部Go入口另行核验正确ACK、分配及worker代次。
- 接受、关闭输入、Go句柄退休、Rust实际终态和远端收到是不同事实；结果未知需用原身份查询/停止，不盲重发音频。

验证：静态合同已定位；内部PCM与外部SIP授权分别取证。外部候选sip-02的22方法、Go race 899项和旧24阶段228方法是独立结果，不能相加为本条完整兼容认证；当前源码绿色由独立runtime证据门禁判定，生成目录不授予通过状态；RustSwitch 源码 media/src/media/protocol.rs:158。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-pcm-turn-interrupt"></a>

### 内部PCM打断与有限淡出 · `key-ipc-pcm-turn-interrupt`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版endpoint/媒体内部接口（仅层次参考） |
| 原版参数/帧 | FreeSWITCH没有pcm_turn_* JSONL或RSP1/RSR1指令 |
| 原版语义 | 固定源码用于定位原版内部媒体接口层次；对象、回调及锁模型与本项目二进制队列不同，不存在逐消息兼容关系。 |
| RustSwitch 入口 | {id,op:pcm_turn_interrupt,session,turn_id,fade_ms?} → pcm_turn_state |
| 当前语义 | fade默认为0，仅0/20/40ms；立即关闭旧轮输入。0丢弃剩余队列并形成实际stopped屏障；淡出只保留已收到队首至多1/2帧、线性衰减到0，不重播已发帧、不重新排期超期音频。重复淡出不延后原计划，stopping可升级为0立即停。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅本地G.711 8k mono 20ms图；不是FreeSWITCH同名命令、speak、录音或ASR上行接口。
- 内部session不是SIP UUID；Rust媒体层不拥有SIP ACK，外部Go入口另行核验正确ACK、分配及worker代次。
- 接受、关闭输入、Go句柄退休、Rust实际终态和远端收到是不同事实；结果未知需用原身份查询/停止，不盲重发音频。

验证：静态合同已定位；内部PCM与外部SIP授权分别取证。外部候选sip-02的22方法、Go race 899项和旧24阶段228方法是独立结果，不能相加为本条完整兼容认证；当前源码绿色由独立runtime证据门禁判定，生成目录不授予通过状态；RustSwitch 源码 media/src/media/protocol.rs:164。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-pcm-turn-status"></a>

### 内部PCM真实轮次状态 · `key-ipc-pcm-turn-status`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版endpoint/媒体内部接口（仅层次参考） |
| 原版参数/帧 | FreeSWITCH没有pcm_turn_* JSONL或RSP1/RSR1指令 |
| 原版语义 | 固定源码用于定位原版内部媒体接口层次；对象、回调及锁模型与本项目二进制队列不同，不存在逐消息兼容关系。 |
| RustSwitch 入口 | {id,op:pcm_turn_status,session,turn_id} → pcm_turn_state |
| 当前语义 | 只查询当前匹配轮次；返回buffering/playing/draining/stopping/completed/stopped/failed和实际样本/容量/年龄/error。恒有accepted=sent+queued+discarded；sent仅完整UDP提交样本，状态查询不代发音频。playing且队列空时按last_accepted_at+20ms+100ms判断断供，已超期push/end不能复活。Release后无旧会话可查，失败错误不能伪装完成。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅本地G.711 8k mono 20ms图；不是FreeSWITCH同名命令、speak、录音或ASR上行接口。
- 内部session不是SIP UUID；Rust媒体层不拥有SIP ACK，外部Go入口另行核验正确ACK、分配及worker代次。
- 接受、关闭输入、Go句柄退休、Rust实际终态和远端收到是不同事实；结果未知需用原身份查询/停止，不盲重发音频。

验证：静态合同已定位；内部PCM与外部SIP授权分别取证。外部候选sip-02的22方法、Go race 899项和旧24阶段228方法是独立结果，不能相加为本条完整兼容认证；当前源码绿色由独立runtime证据门禁判定，生成目录不授予通过状态；RustSwitch 源码 media/src/media/protocol.rs:171。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-pcm-push"></a>

### 内部PCM二进制供音 · `key-ipc-pcm-push`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版endpoint/媒体内部接口（仅层次参考） |
| 原版参数/帧 | FreeSWITCH没有pcm_turn_* JSONL或RSP1/RSR1指令 |
| 原版语义 | 固定源码用于定位原版内部媒体接口层次；对象、回调及锁模型与本项目二进制队列不同，不存在逐消息兼容关系。 |
| RustSwitch 入口 | 独立继承FD3 UnixDatagram：RSP1固定44字节头+S16LE → RSR1固定96字节回执 |
| 当前语义 | 非JSON操作；请求携request_id/session/turn_id/offset，8k mono每批160..800样本且整160，offset准确等于accepted_samples。整批验证后入队，满队列或偏移错误不部分接受；RSP1 code8是queue_full，可按真实偏移恢复。lane仅保留一个待发回复，WouldBlock/ENOBUFS有一秒总期限，超时隔离PCM lane而非阻塞RTP/控制。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 仅本地G.711 8k mono 20ms图；不是FreeSWITCH同名命令、speak、录音或ASR上行接口。
- 内部session不是SIP UUID；Rust媒体层不拥有SIP ACK，外部Go入口另行核验正确ACK、分配及worker代次。
- 接受、关闭输入、Go句柄退休、Rust实际终态和远端收到是不同事实；结果未知需用原身份查询/停止，不盲重发音频。

验证：静态合同已定位；内部PCM与外部SIP授权分别取证。外部候选sip-02的22方法、Go race 899项和旧24阶段228方法是独立结果，不能相加为本条完整兼容认证；当前源码绿色由独立runtime证据门禁判定，生成目录不授予通过状态；RustSwitch 源码 media/src/media/pcm_transport.rs:32。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-api-pcm-stream"></a>

### 私有RVA1流式PCM API · `key-api-pcm-stream`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | FreeSWITCH endpoint媒体层（无同名私有API） |
| 原版参数/帧 | 原版没有RVA1/RVR1、流token或对应HTTP路径 |
| 原版语义 | 仅比较应用向媒体提供音频这一层次；原版ESL、模块ABI与speak/录音应用并不实现本项目的帧或令牌合同。 |
| RustSwitch 入口 | 可选pcm_stream.socket_path：同UID Unix stream RVA1/RVR1；HELLO/BEGIN/PUSH/END/INTERRUPT/STATUS/FORGET/PING |
| 当前语义 | 非HTTP/非ESL的持久二进制接口，默认关闭；私有0700父目录、0600 socket。控制/数据连接分别有界，UUID流槽由max_streams限制，未知替换最多保留当前+旧两个handle/token。BEGIN由Go核验正确ACK、本地G.711分配与健康同代三能力；未知结果保留token供对账。数据连接断开不自动取消媒体；PUSH/END串行防超车，确定偏移拒绝可恢复，错误5合并Go/Rust背压。park与静默read可并行，tone/WAV或带提示read与PCM互斥；挂机/代次失败撤销。 |
| 状态/关系 | `implemented` / `analogous_not_wire_compatible` |

- 这是自有部署可选API；64字节RVA1请求头、96字节RVR1回复头和各自错误码不兼容ESL、HTTP或内部RSP1/RSR1。
- 新轮成功替换旧token；未知替换必须保留受限旧控制身份直到结果收敛。输入关闭/退休不等于真实停止，只有实际终态确认后FORGET才删除；原ASR/TTS服务、录音与桥接注入仍未实现。
- 单路22项验收不证明完整并发连接/故障组合、原版兼容或5000/10000路Agent容量；旧失败证据保留。

验证：静态合同已定位；内部PCM与外部SIP授权分别取证。外部候选sip-02的22方法、Go race 899项和旧24阶段228方法是独立结果，不能相加为本条完整兼容认证；当前源码绿色由独立runtime证据门禁判定，生成目录不授予通过状态；RustSwitch 源码 control/internal/server/pcm_endpoint.go:76。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-rx-subscribe"></a>

### 内部原生音频上行订阅 · `key-ipc-rx-subscribe`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版endpoint/媒体内部接口（仅职责层次参考） |
| 原版参数/帧 | FreeSWITCH没有rx_* JSONL、FD4或RXS2数据报合同 |
| 原版语义 | 原版内部媒体读取由其会话、回调和模块对象管理；该源码锚点仅说明层次，不表示本项目实现了对应原生符号或逐消息映射。 |
| RustSwitch 入口 | {id,op:rx_subscribe,session,subscription_id} → rx_state |
| 当前语义 | session/subscription_id须为非零u64；每会话最多一个活动订阅。同ID返回原状态，不复活、不重锚时钟；新ID严格递增且旧订阅须已停止/失败。需local图与可用独立FD4；新订阅先校准共享时钟，失败不改观察图。每订阅8槽，未订阅不分配导出观察队列。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 只接收本地G.711 PCMU/PCMA 8k、单声道、20ms图的A腿原生音频；不默认转16k，不含TX回音、双腿混音或外部ASR服务适配。
- 这是自有进程间合同，session/subscription_id不是SIP UUID或ESL Job-UUID；Rust本身不判断SIP ACK，服务端Go SDK另行授权。
- 真实解码、历史PLC、CN、按键辅助、缺包、源边界与导出缺口分开表达；非零能量和socket提交都不是VAD、用户发声或ASR识别结论。

验证：源码与独立验收材料已关联：Rust06的RXS2普通UDP/connected各12方法；Server SDK03与Rust08的server-sip-03含PCMU/PCMA×两种UDP模式四个真实SIP子例，检查正确ACK授权、逐样本RX、BYE撤读、真实资源归零与端口复绑。两批候选身份分别保留；本地绿色由独立运行证据门禁判定，目录生成不等于执行或原版认证；RustSwitch 源码 media/src/media/worker_rx.rs:142。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-rx-status"></a>

### 内部上行订阅状态与对账 · `key-ipc-rx-status`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版endpoint/媒体内部接口（仅职责层次参考） |
| 原版参数/帧 | FreeSWITCH没有rx_* JSONL、FD4或RXS2数据报合同 |
| 原版语义 | 原版内部媒体读取由其会话、回调和模块对象管理；该源码锚点仅说明层次，不表示本项目实现了对应原生符号或逐消息映射。 |
| RustSwitch 入口 | {id,op:rx_status,session,subscription_id} → rx_state |
| 当前语义 | 只查询精确会话/订阅，返回active/stopped/failed、produced/submitted/dropped/queued事件数、样本数、oldest_age_ms与error。produced_events=submitted_events+dropped_events+queued_events；样本按产生、提交、丢弃及在队内容对账。查询会执行过期淘汰，不代发音频。submitted只证明Rust交给内核，缺口/结束摘要不计为新的生产观察；Release后旧会话不可查。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 只接收本地G.711 PCMU/PCMA 8k、单声道、20ms图的A腿原生音频；不默认转16k，不含TX回音、双腿混音或外部ASR服务适配。
- 这是自有进程间合同，session/subscription_id不是SIP UUID或ESL Job-UUID；Rust本身不判断SIP ACK，服务端Go SDK另行授权。
- 真实解码、历史PLC、CN、按键辅助、缺包、源边界与导出缺口分开表达；非零能量和socket提交都不是VAD、用户发声或ASR识别结论。

验证：源码与独立验收材料已关联：Rust06的RXS2普通UDP/connected各12方法；Server SDK03与Rust08的server-sip-03含PCMU/PCMA×两种UDP模式四个真实SIP子例，检查正确ACK授权、逐样本RX、BYE撤读、真实资源归零与端口复绑。两批候选身份分别保留；本地绿色由独立运行证据门禁判定，目录生成不等于执行或原版认证；RustSwitch 源码 media/src/media/worker_rx.rs:189。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-rx-unsubscribe"></a>

### 内部上行停止与终态 · `key-ipc-rx-unsubscribe`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版endpoint/媒体内部接口（仅职责层次参考） |
| 原版参数/帧 | FreeSWITCH没有rx_* JSONL、FD4或RXS2数据报合同 |
| 原版语义 | 原版内部媒体读取由其会话、回调和模块对象管理；该源码锚点仅说明层次，不表示本项目实现了对应原生符号或逐消息映射。 |
| RustSwitch 入口 | {id,op:rx_unsubscribe,session,subscription_id} → rx_state |
| 当前语义 | 仅停止匹配订阅，重复停止返回原终态；关闭观察图、丢弃未提交音频并保留真实对账，可尽力发出缺口/结束摘要。控制回执不等待消费者读取，不停止下行PCM、提示音或按键；旧ID不会复活，结果未知须继续使用原身份查询或停止。会话释放与订阅停止是不同生命周期。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 只接收本地G.711 PCMU/PCMA 8k、单声道、20ms图的A腿原生音频；不默认转16k，不含TX回音、双腿混音或外部ASR服务适配。
- 这是自有进程间合同，session/subscription_id不是SIP UUID或ESL Job-UUID；Rust本身不判断SIP ACK，服务端Go SDK另行授权。
- 真实解码、历史PLC、CN、按键辅助、缺包、源边界与导出缺口分开表达；非零能量和socket提交都不是VAD、用户发声或ASR识别结论。

验证：源码与独立验收材料已关联：Rust06的RXS2普通UDP/connected各12方法；Server SDK03与Rust08的server-sip-03含PCMU/PCMA×两种UDP模式四个真实SIP子例，检查正确ACK授权、逐样本RX、BYE撤读、真实资源归零与端口复绑。两批候选身份分别保留；本地绿色由独立运行证据门禁判定，目录生成不等于执行或原版认证；RustSwitch 源码 media/src/media/worker_rx.rs:189。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-ipc-rx-stream"></a>

### 内部RXS2原生音频数据流 · `key-ipc-rx-stream`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | 原版endpoint/媒体内部接口（仅职责层次参考） |
| 原版参数/帧 | FreeSWITCH没有rx_* JSONL、FD4或RXS2数据报合同 |
| 原版语义 | 原版内部媒体读取由其会话、回调和模块对象管理；该源码锚点仅说明层次，不表示本项目实现了对应原生符号或逐消息映射。 |
| RustSwitch 入口 | 独立继承FD4 UnixDatagram：RXS2固定168字节头，正文最多480字节 |
| 当前语义 | 仅Rust写、Go读；Decoded/HistoryPlc各160个S16LE样本，CN保留原SID，其余标记无PCM。携带订阅、事件序号、源代次/分段、展开RTP包序/时间、真实年龄和原因。普通观察以共享单调时钟下界+100ms冻结到期值，重试/序列化不续期；Gap/End/ExportFailed两个时间字段为0。仅Linux MONOTONIC/macOS UPTIME_RAW同机同域可用；Go接收、出队及Server交付检查点复核，后续适配器使用前仍须CheckFresh。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 只接收本地G.711 PCMU/PCMA 8k、单声道、20ms图的A腿原生音频；不默认转16k，不含TX回音、双腿混音或外部ASR服务适配。
- 这是自有进程间合同，session/subscription_id不是SIP UUID或ESL Job-UUID；Rust本身不判断SIP ACK，服务端Go SDK另行授权。
- 真实解码、历史PLC、CN、按键辅助、缺包、源边界与导出缺口分开表达；非零能量和socket提交都不是VAD、用户发声或ASR识别结论。
- Rust每订阅8槽，队列满或观察年龄达到100ms丢旧记录并报告连续缺口；无法保真对账则显式失败。写入连续一秒无进展隔离RX，控制与TX继续；Go每订阅8槽独立限流，慢读或过期明确失败，不把OS排队后的旧PCM当作新音频。

验证：源码与独立验收材料已关联：Rust06的RXS2普通UDP/connected各12方法；Server SDK03与Rust08的server-sip-03含PCMU/PCMA×两种UDP模式四个真实SIP子例，检查正确ACK授权、逐样本RX、BYE撤读、真实资源归零与端口复绑。两批候选身份分别保留；本地绿色由独立运行证据门禁判定，目录生成不等于执行或原版认证；RustSwitch 源码 media/src/media/rx_export.rs:15。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

<a id="key-api-rx-sdk"></a>

### 内部Go服务端上行音频SDK · `key-api-rx-sdk`

| 项目 | 合同 |
| --- | --- |
| FreeSWITCH 入口 | FreeSWITCH内部媒体访问（无同名Go SDK） |
| 原版参数/帧 | 原版没有Server.SubscribeRX/RXHandle或对应HTTP/Unix监听接口 |
| 原版语义 | 仅比较应用消费入站音频的职责；不提供FreeSWITCH模块ABI、ESL命令、媒体钩子符号或外部识别服务合同。 |
| RustSwitch 入口 | Server.SubscribeRX(ctx,uuid,subscriptionID) → RXHandle；Read/Status/Unsubscribe/Snapshot/Retired |
| 当前语义 | 进程内Go调用，未暴露HTTP或Unix服务。只授权真实正确ACK、已接通且已分配的本地G.711 A腿及同一健康worker代次，绑定UUID/worker/generation/session/subscriptionID。固定4个控制协程、64个请求/在途预算；音频直接经独立SDK读取。同ID返回原句柄、不重开读权；新ID须确认旧远端终态。未知受理保留不可读清理句柄；挂断先撤读并唤醒，实际Release/代次死亡后退休。Read在交付检查点复核期限与读权，Snapshot是Go接收视图，Status查询Rust生产状态。 |
| 状态/关系 | `internal_only` / `internal_contract_only` |

- 这是服务内部SDK，没有供外部ASR直接连接的HTTP/Unix协议、供应商适配器、自动重采样、VAD或识别结果事件。
- Go撤读/退休、Rust终态、内核已提交和SDK交付分别取证；调用者暂停后再使用音频须复核原到期值，100ms从Graph观察开始，并非声学采集到识别的总延迟。
- 真实四子例检查单路SIP授权和媒体生命周期，不证明全FreeSWITCH兼容、Linux运行、5000/10000路Agent容量或ASR识别效果。

验证：源码与独立验收材料已关联：Rust06的RXS2普通UDP/connected各12方法；Server SDK03与Rust08的server-sip-03含PCMU/PCMA×两种UDP模式四个真实SIP子例，检查正确ACK授权、逐样本RX、BYE撤读、真实资源归零与端口复绑。两批候选身份分别保留；本地绿色由独立运行证据门禁判定，目录生成不等于执行或原版认证；RustSwitch 源码 control/internal/server/rx_stream.go:280。

原版定位：[src/include/switch_loadable_module.h:122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/include/switch_loadable_module.h#L122)。

## 差分闭合与无感切换要求

最终无改动替换需先冻结现网构建、模块、配置、客户端、脚本及 C/C++ 依赖；补采动态接口和返回/错误/事件/文件轨迹；将每个强制定义展开成可执行的成对测试。任一未运行、未知、跳过或不支持项都不能计入通过。完整门槛见 [差分验收与切换标准](../freeswitch-compatibility/06-conformance-and-cutover.md)。

已有呼叫自然排空后切换可避免主动挂断，但需要等待并正确路由旧对话；活动呼叫、ESL TCP 连接、作业、订阅及录音状态在线迁移是独立目标。当前尚未实现后者，不能用排空按钮或子进程重启作为无损接管证明。

## 可复现生成

从项目根目录运行：

```sh
python3 tools/build_interface_comparison.py --reference-root /绝对路径/freeswitch-1.11.3
python3 tools/build_interface_comparison.py --reference-root /绝对路径/freeswitch-1.11.3 --check
```

生成器只读固定源码与已有标准，写本页和 comparison.json；`--check` 不写文件，比较现有产物与重新生成字节。没有网络、运行时探测或服务副作用。若输入数量、源码哈希、ID、链接或人工证据锚点不匹配则失败，不能静默减少覆盖分母。
