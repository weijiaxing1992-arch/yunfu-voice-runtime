# RustSwitch HTTP 管理接口参考

文档版本 **1.10.0**；应用版本 **0.3.0**；核对日期 **2026-09-07**。

本参考描述当前 Go 处理器的 **26 个显式 method/path 操作**：16 个观测、配置、保护、音频能力及 XML 草稿操作，加 5 个文档发布操作及 5 个压力/单路测试操作。机器契约见 [openapi.json](openapi.json)。静态资源和 Go 自动匹配的 HEAD 不计入这 26 项。1.6 新增线路注册状态及线路、本地 IVR 配置字段，沿用现有路径；新增 ESL 应用和媒体 IPC 不计入 HTTP 操作数。

这是 RustSwitch 自有 HTTP 管理协议。FreeSWITCH 对照固定为 **1.11.3**，提交 `ef32e205295e29f034f1453ad245ba5efb07b94a`。相似用途不表示相同命令、计数、返回值或线协议；原版 `fs_cli`/ESL 客户端不能用这些 HTTP 路由实现无修改替换。XML 草稿可编辑导出，当前 RustSwitch 不执行对应业务。接口目录的条目数量也不构成兼容完成率或万路认证。

## 1. 地址、版本和传输规则

默认示例地址是 `http://127.0.0.1:9080`，来自 `config/local.json`，实际地址以 `GET /v1/config` 的 `active.admin.listen` 为准。监听仅允许 IPv4 回环；没有本接口提供的 TLS、远程用户认证或登录会话。远程管理应使用已有的 SSH 隧道或明确部署的受控入口，不能仅把 CSRF 当作身份认证。

所有路由，包括文档、静态资源和错误路径，经过同一个中间件：

| 项目 | 实际规则 |
| --- | --- |
| `Host` | 主机部分必须是精确 `localhost` 或可解析的回环 IP 字面量；自定义域名即便解析到回环也不接受，失败 403 JSON。通常由客户端根据 URL 自动生成 |
| GET/HEAD | 不要求 CSRF，不检查 Origin；不会提交运行配置。快照读取可以结算令牌的时间补充，但不消费令牌、不增加准入统计 |
| 非 GET/HEAD | 要求 `Content-Type` 的分号前部分精确等于 `application/json`，否则 415；`application/json; charset=utf-8` 可用，`Application/JSON` 或分号前额外空格不等价 |
| `X-RustSwitch-CSRF` | 必须等于 `GET /v1/config.csrf_token`；失败 403 并带 `X-RustSwitch-CSRF-Refresh: required`，表示尚未进入业务处理器。令牌随控制进程重启更换，不保存到示例或共享报告 |
| `Origin` | 写请求中不存在或空值允许；非空时 URL scheme 必须为 `http`，URL.Host 必须与请求 Host 完全相同。失败 403 |
| HTTP 限制 | 读头 2 秒、读请求 5 秒、写响应 5 秒、空闲连接 30 秒；`MaxHeaderBytes=8192`。这是服务器配置，不保证任何业务请求都在该时间成功 |
| 管理资源预算 | 在接纳连接前限制最多 64 个 TCP 连接；文档、XML、报告及完整配置保存最多 8 个并发重请求，超过时返回 503 和 `Retry-After: 1`。不改变 SIP 呼叫并发限制 |
| PUT 正文 | 通过 `decodeAdmin` 读取最多 4 MiB；超限在当前实现中返回 400，不是 413 |
| 缓存/内容保护 | `Cache-Control: no-store`、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`；页面还有同源 CSP |

正常 JSON 成功响应使用 `application/json`；管理错误先设置 `application/json; charset=utf-8`。**`GET /readyz` 的 503 是例外**：代码先提交 503 再设置 JSON 类型，实际 Content-Type 可能缺失，正文仍是 JSON `{"ready":false}`。客户端应先判断状态码，再按约定解析正文。

未知 GET 路径通常由 Go ServeMux 返回 404 文本；已存在路径上的错误方法通常为 405 文本并带 `Allow`。非 GET/HEAD 的中间件检查在路由匹配之前，因此错误方法也可能先得到 403/415。Go 对一些非规范 URL 路径有规范化跳转；客户端应直接使用本文列出的路径，不依赖跳转发起写操作。

Go 的 `GET` 路由同时接受 `HEAD`：处理器仍执行读取与校验，HTTP 服务器不发送响应体。HEAD 不是新增写入操作，也不能用于获取只在 JSON 正文中的 CSRF。

管理页面只有收到明确的令牌失效 403 和 `X-RustSwitch-CSRF-Refresh: required` 时，才重新读取令牌并用同一请求重试一次。刷新令牌不替换未提交配置草稿、原 revision 或压测 request_id；版本过期仍按 409 交给用户处理。Host/Origin 拒绝、普通 403、网络失败及不确定的写入超时不会触发自动重放。手工客户端也应保留这一边界，不能对所有失败盲目重试写操作。

## 2. JSON、错误与省略字段

OpenAPI 使用规范小写 JSON 字段名，输入模型与完整响应模型分开。实际 `encoding/json` 接受已知名称的大小写匹配；重复已知键依 Go 规则采用后值覆盖或对象合并。未知字段拒绝，额外的第二个 JSON 值拒绝。这里没有通用 PATCH，也不会把省略字段与旧配置合并。

省略字段初始化为 Go 零值。`null` 对非指针标量通常不报类型错误，而保持其初始零值；并不表示保留旧值。后续业务校验决定是否拒绝。例如：

| 输入 | 实际结果 |
| --- | --- |
| `PUT /v1/guard` 有有效 revision，但完整策略未传 `enabled` 或传 null | 解码为 false；其他必需范围通过时会关闭额外保护。始终完整回传策略 |
| 完整配置省略 `media.connect_sockets`/`adaptive_admission` | 变为 false，而不是保留当前值 |
| 省略 `media.port_reuse_delay_ms` | 变为 0，可通过范围校验；不是当前隔离期的继承 |
| `cpu_cores` 省略/null/空数组 | 不绑核；空数组和 null 在配置深比较中不同，可能影响 `restart_required` |
| `PUT /v1/fs-config` 的 `overrides` 省略/null/{} | 无 XML 差量，但仍保存并推进共享 revision |
| 省略 revision | 变为 0，通常返回 409；`PUT /v1/config` 和 `/v1/fs-config/file` 先做内容校验，可能先返回 422 |
| PUT 根正文 null | 结构保持全零，再按上述校验顺序拒绝；空正文/无法解码的 JSON 返回 400 |
| `POST /v1/drain`、`/v1/resume` | handler **不解析正文**，可空、不要求 revision；即使正文不是 JSON 文本，也不会因正文解码失败。Content-Type/CSRF/Origin 仍照常检查 |

整数不能用小数、字符串或负数冒充无符号 revision。JSON 不支持 NaN/Infinity；超出 Go 字段数值范围的输入也会解码失败。机器 schema 里的 required 表示标准成功请求所需的语义字段，不表示当前 handler 一定以 400 处理它的缺失。

错误正文通常为 `{"error":"说明"}`。版本冲突为 `{"error":"说明","revision":当前版本}`；服务正在退出导致的 resume 409 只有 error。客户端必须保留未提交草稿，重新读取后合并，不能把旧内容换一个新版本号自动重试。

| 状态码 | 来源 |
| --- | --- |
| 200 | 处理成功；注意配置草稿保存成功不等于运行切换 |
| 400 | JSON/类型/未知字段/正文大小，或 GET 文件路径查询不合法 |
| 403 | Host、Origin 或 CSRF 不通过 |
| 404 | 合法文件路径不在有效文件集合/文档清单；或 Go 未匹配路由 |
| 405 | 方法不匹配，由 Go 路由器返回；中间件可能先拒绝 |
| 409 | revision 过期；或 resume 时 stopping=true |
| 415 | 写请求 Content-Type 不符合要求 |
| 422 | 完整配置、策略、XML 或参数语义校验不通过 |
| 500 | 管理保存、内嵌文件/索引生成等失败，详见各操作 |
| 503 | `/readyz` 当前不能接入；或管理重请求额度已满，尚未读取正文/执行业务并建议 1 秒后重试。具体操作见 OpenAPI 的全部响应定义 |

SIP 层新 INVITE 的 `503` 和 `Retry-After` 是另一条协议路径，与管理 HTTP 重请求预算分开计数。HTTP 测试接口只能启动隔离模拟任务，不提供任意真实分机外呼、挂断生产 UUID 或订阅 ESL 的能力。

## 3. 配置版本、提交和重启

`revision` 为全部 JSON 配置、即时 Guard 和 XML 草稿共用的 `uint64`。未保存的新状态从 1 开始；成功写入加 1，无改动提交也会加 1。drain/resume 不使用也不推进该版本。JavaScript 的安全整数范围小于 uint64；通用客户端不应让超大版本号在浮点转换中失真。

管理文件路径由**原始启动配置的** `journal.path + ".admin.json"` 得到，包含 `format`、`revision`、原始配置摘要、完整 `desired`、`policy` 和 XML 覆盖文件。HTTP 不改写原始启动 JSON，不把 XML 写入任意部署目录；`media.binary` 和 `journal.path` 在 `PUT /v1/config` 中必须与当前运行值相同。

提交在管理互斥锁内先写同目录临时文件并 `Sync`，再重命名替换。重命名是提交点；成功后发布内存版本。之前失败不会发布新的内存版本；之后的目录同步失败只记录警告，因为文件已经替换。目录打开失败也不会回退提交。成功不等于任意断电场景下零丢失。

`PUT /v1/config` 只改变 `desired`；已监听的端口、worker、CPU 绑定、媒体配置仍采用 `active`，不会自动重启。正常重启后，原始配置摘要未改变时采用 saved desired。人工改变启动文件时，文件配置优先；已有 Guard 保留并收紧超过新硬边界的数量，保留 enabled/软阈值/最低比例/重试值，XML 草稿仍保留。启动载入本身不写盘；后续提交才保存新状态。改动原始 `journal.path` 后不会自动迁移旧路径管理文件。

`PUT /v1/guard` 在确认共享版本、当前与待启动硬边界都有效后，先持久化再原子应用策略。保存前校验失败/磁盘失败不应被客户端当作“已经应用”；响应中断或超时可能发生在提交之后，必须重新 GET 判断最终状态。降低限制只拒绝后续初始 INVITE，不主动挂断已有通话，也不会延迟已经产生的 200、ACK、BYE。

`restart_required` 仅比较 active/desired。保存 Guard 或 XML 不会因此显示需要重启；后台 XML 草稿仅供导出，不会因保存而自动执行。另有启动时载入的受限 `sip.dialplan.file` 入口，详见[拨号计划](dialplan-reference.md)。

## 4. 完整配置范围与默认值

完整 Config 的基础必填项不会自动补成仓库示例。可选 `sip.stream` 和 `sip.registration` 使用各自明确的零值／缺省规则；详见字段字典与下表。以下是仓库 `config/local.json` 示例，不是所有部署实例的默认返回；读取 active 才能确定实际值。

| 分类 | 本地示例 |
| --- | --- |
| SIP | listen/advertise `127.0.0.1:5060`，upstream `127.0.0.1:5070`，trusted_networks `127.0.0.0/8`，setup/ack 各 32000 ms，max_call_seconds 3600 |
| 启动硬限制 | max_calls 100，calls_per_second 20，burst_calls 40，max_transactions 10000 |
| 媒体地址/端口 | bind_ip/advertise_ip `127.0.0.1`，20000–21999，workers 2 |
| 媒体资源 | receive_buffer_bytes 65536，port_reuse_delay_ms 2000，max_packets_per_second_per_leg 200，allowed_remote_networks `127.0.0.0/8`，cpu_cores [] |
| 媒体压力 | adaptive_admission true，admission_cpu_high 0.85，admission_cpu_low 0.65；未提供 connect_sockets 时为 false |
| 运行入口 | admin.listen `127.0.0.1:9080`；journal.path `run/events.jsonl`，queue_capacity 4096；media.binary `./bin/rustswitch-media` |

全部字段的类型、边界和输出位置见第 9 节 schema 字典。额外的跨字段约束为：

1. 所有 SIP/管理地址是 IPv4 字面量加非零端口。上游不能等于 SIP listen/advertise。SIP advertise、upstream、media advertise_ip 不能是未指定、组播、255.255.255.255；SIP listen/media bind_ip 可按各自规则绑定。
2. 信令与媒体白名单必须非空，元素是可解析的 IPv4 CIDR。媒体 peer 还需通过实际 SDP/worker 的目的地址检查；配置校验本身不是一次呼叫连通性测试。
3. workers 在 1–64，max_calls 至少等于 workers。port_start 至少 1024 且为偶数；port_end 不超过 65535 且大于 port_start。SIP监听端口不能落入媒体范围。
4. `blocks=floor((port_end-port_start+1)/4)`；一条桥接通话占 A/B 两腿各 RTP/RTCP 四端口。要求 `max_calls + ceil(calls_per_second*port_reuse_delay_ms/1000) <= blocks`。该全局预算不保证每个分片在任意高周转分布下都有剩余端口。
5. max_transactions 至少 `2*max_calls`。实现分别约束会话+响应缓存与出站事务表，不能把它解释成所有表相加的单一对象总量。
6. adaptive_admission=true 时 `0<cpu_low<cpu_high` 且 `0.5<=cpu_high<=1`；false 时当前代码不校验这两个浮点值的该范围。cpu_cores 非空仅 Linux 接受，长度必须等于 workers，单值 0–1023；不会静态证明该核心真实存在或可使用。
7. media.binary 和 journal.path 必须非空；Validate 不检查二进制是否存在/可运行，也不检查磁盘/端口的实际可用性。HTTP另行禁止改变这两个路径。

### 1.6 线路与本地呼入配置

以下字段通过完整 `PUT /v1/config` 保存到 `desired.sip`，正常重启后才进入 `active.sip`。省略可选对象不会保留旧对象；提交前应读取并完整回传要保留的配置。凭据文件实际读取与权限检查发生在进程启动，保存成功不证明凭据可用或线路已注册。

| 字段 | 默认与范围 | 实际作用 |
| --- | --- | --- |
| `sip.stream` | 省略时使用 UDP；TCP/TLS 的连接及超时默认值见字段字典 | 真实可靠信令传输；TLS 校验证书链及名称，不提供跳过验证开关 |
| `sip.trunk_auth` | 可省略；username 为 1–128 字节，realm 可省略且最多 256 字节，password_file 必须绝对路径 | 固定上游 Digest 客户端；密码来自私有普通文件，管理响应只给路径 |
| `sip.registration` | 可省略；aor 与 registrar_uri 必填且须为受限简单 SIP URI | 向固定上游注册；不会接收终端 REGISTER 或根据逻辑域名进行 DNS 路由 |
| `sip.registration.expires_seconds` | 缺省或 0 使用 300；非零允许 30–86400 | 请求有效期；实际期限由上游响应确认 |
| `sip.registration.retry_seconds` | 缺省或 0 使用 30；非零允许 1–3600 | 失败重试基础间隔；连续失败有界退避 |
| `sip.registration.timeout_ms` | 缺省或 0 使用 8000；非零允许 500–32000 | 一轮注册总期限，挑战重试不能延长 |
| `sip.dialplan` | 可省略或null；file为绝对XML文件，context缺省default | 启动冻结，权威匹配未命中404；部署只读，后台PUT保持不变；详见[XML合同](dialplan-reference.md) |
| `media.playback_root` | 空值关闭，启用须规范绝对目录且不能为根目录 | 部署只读；用于受限8k单声道WAV，后台PUT保持不变 |
| `sip.local_extensions` | 未启用sip.dialplan时省略／空列表保留固定上游路由；最多 256 个唯一项，每项 1–32 个数字或 `+` 字符 | 精确命中号码时分配本地 A 腿并自动应答，等待 ACK 后可执行收号和提示音 |

启用 TCP/TLS 注册时需有对应本地监听以构造可回呼 Contact。本地 IVR 只产生实际 A 腿 UUID，但当前仍预留四个媒体端口。注册、认证及更多跨字段限制见 [中继注册说明](trunk-registration.md)；本地呼入、ESL 收号和真实提示音示例见 [AI 呼叫接入](ai-calling-reference.md) 与 [IVR 接口](ivr-reference.md)。这些配置不执行 XML 草稿，也不代表全部 Sofia gateway 已兼容。

### Guard 首次策略与计数

首次未保存 Guard 时：`enabled=true`，`max_active=min(8000,hard.max_calls)`，`max_establishing=min(1000,hard.max_calls)`，`cps=min(1000,hard.cps)`，`burst=min(hard.burst,cps)`，软阈值 0.8、最低比例 0.1、重试 1 秒。本地例对应 **100路 / 建立中100路 / 20 CPS / 突发20**。这与启动突发40是不同层次。策略修改时 burst 不要求小于 CPS，只要求不超过当前及待启动的对应启动硬上限。

资源总量是尚未释放的 Reserved Active；已建立是成功 ACK 且业务未结束；建立中使用 `max(active-established,0)`，包含业务结束但等待媒体释放的资源。不是 FreeSWITCH channel/session 计数，也不是累计接通过的呼叫总数。

令 `u=max(active/max_active, establishing/max_establishing)`，`s=soft_limit_ratio`，`m=minimum_rate_ratio`：

```text
u <= s: effective_cps = policy.calls_per_second
u >  s: effective_cps = policy.calls_per_second * max(m, 1-min(1,(u-s)/(1-s))*(1-m))
```

默认九成占用时补充速率为基准55%。达到任一完整并发阈值仍拒绝，即使余额充足。实际结算采用当前和上一观察速率的较小值，因此恢复负载时可能保守地慢一个采样区间。令牌桶限制补充和突发，不是每个自然秒强制相同接入数。只在首次构造填满；反复保存、启停和提高 burst 不会补满。

enabled=false 只关闭额外阈值与柔性缩速，启动并发/CPS/Burst仍在，媒体压力/日志/排空/实际资源也可拒绝。Guard不控制实际200响应时刻，不建立无限等待队列。`last_reject_reason` 是历史信息，当前阻塞应看 `limit_reason`。Guard计数只统计进入它的检查，不能替代服务器整体拒绝数。

## 5. XML 编辑与文档发布边界

XML目录基于固定 FreeSWITCH vanilla 配置叠加已保存覆盖。当前表单映射 param/variable/option 的 value、action/anti-action 的 data、condition 的 expression、load 的 module、X-PRE-PROCESS set 的变量值、ACL node 的 cidr；其他结构通过原始XML编辑。字段ID由路径和字段序号产生，不能跨revision猜测。

单文件最多 **256 KiB UTF-8字节**，已保存覆盖/新增最多512个、合计5 MiB，单参数值最多16384 UTF-8字节。完整管理JSON另有7 MiB提交上限，HTTP解码正文另有4 MiB上限，限制分别生效。JSON Schema的maxLength以字符计，不可冒充UTF-8字节限制。

XML路径仅允许1–200字节ASCII安全字符和`.xml`后缀，必须已规范化，不能绝对或穿越。XML至少一个根，可多根片段；嵌套超过128层、token超过100000、根外非空文本、DTD/实体声明、重复展开名属性、错误XML或非UTF-8声明拒绝。保留变量、注释、预处理标签，但不运行它们；不请求外部实体或网络，不验证FreeSWITCH模块的全部业务语义。第三方命名空间结构原样保留，仅通过原文编辑。

基线中五份仅含ASCII、声明Windows-1252的模板在读取/导出时改为UTF-8声明；不会把这个规则套到真实旧编码的非ASCII数据。导出包括完整conf树、来源、许可和修改说明，不包含“在RustSwitch执行成功”的证明。

文档发布入口完全只读，内嵌于二进制，与工作目录磁盘无关。`/v1/docs/file` 只允许恰好一个path查询项；缺失、重复、未知键、解码错误、绝对路径、反斜线、NUL、点段、非规范路径或超过240字节均400；有效格式但不在manifest清单为404。其查询规则**比`/v1/fs-config/file`更严格**：后者当前只用首个path，没有统一拒绝额外查询键。

Markdown返回`text/markdown; charset=utf-8`，CSV返回`text/csv; charset=utf-8`，JSON返回`application/json; charset=utf-8`，头文件/文本等返回`text/plain; charset=utf-8`。原文包含什么就返回什么，不把Markdown转成可执行HTML。文件返回inline文件名和Content-Length；清单JSON本身不承诺显式这两头。客户端下载后可按manifest SHA-256验证；当前服务加载清单检查路径、重复项、存在性和长度，并不在每次请求重新计算SHA-256。

文档ZIP仅包含`manifest.json`和清单列出的原始文件，下载名`rustswitch-interface-docs.zip`，不打包运行配置、CSRF、日志或用户XML草稿。FreeSWITCH配置ZIP是另一入口，下载名`freeswitch-1.11.3-config.zip`。

## 6. 逐操作参考

下列每项列出标准请求/响应schema和可观察错误。所有项还受第1节中间件、Go路由器与HTTP传输错误影响。示例均为文档文本，不会自动执行或提交。
### 6.1 `GET /healthz` — 读取进程存活和版本

可以响应不表示媒体健康或有新呼叫容量。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `Health` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |

FreeSWITCH对照：`status`；关系 `analogous_not_wire_compatible`。只报告本HTTP进程存活，不是FreeSWITCH status的结构和文本。

### 6.2 `GET /readyz` — 读取新呼叫准入就绪

合并排空、日志健康、Guard.limit_reason及至少一个媒体分片Admission；不检查全部私有容量状态，不能保证下一次媒体分配成功。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `Readiness` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 503 | `application/json` / `Readiness` | 暂不可接入，正文为ready=false；当前先WriteHeader再writeJSON，实际Content-Type可能缺失。 |

FreeSWITCH对照：`fsctl pause_check / status`；关系 `analogous_not_wire_compatible`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

### 6.3 `GET /v1/status` — 读取控制与媒体观测

返回控制计数、保护器、媒体分片及 `sip_registration` 只读状态；没有逐通话清单、ESL事件流或完整CDR。`sip_registration.enabled` 和 `state` 总是存在；state 为 `disabled`、`registering`、`registered`、`retrying`、`unregistered` 或 `unregister_failed`。`last_status`、`error`、`expires_at` 按实际状态省略；期限来自最后发布快照，客户端仍须与当前时间比较，不能据字段存在认定绑定仍有效。注册成功也不等于通话或媒体成功，详见 [中继状态与退出语义](trunk-registration.md)。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `Status` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |

FreeSWITCH对照：`status / show calls / show channels`；关系 `analogous_not_wire_compatible`。计数/身份/字段不同，没有实现show calls/channels的原始API。

### 6.4 `GET /metrics` — 读取Prometheus指标

text/plain; version=0.0.4，无HELP/TYPE声明；累计和即时指标须按指标字典区分。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `text/plain` / `原始正文` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |

FreeSWITCH对照：`status / 媒体模块统计`；关系 `analogous_not_wire_compatible`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

### 6.5 `GET /v1/config` — 读取运行配置和待启动草稿

读取共享revision、当前进程CSRF令牌、active/desired、持久化状态和Guard。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `AdminConfigResponse` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |

FreeSWITCH对照：`XML配置 / reloadxml`；关系 `analogous_not_wire_compatible`。配置为RustSwitch JSON，不执行FreeSWITCH XML或reloadxml。

### 6.6 `PUT /v1/config` — 保存完整待启动配置

先校验完整config再检查revision；禁止修改media.binary与journal.path；当前Guard必须适合新的硬上限。保存不切换当前socket/worker，下次启动应用desired。

正文：`application/json`；必需；模型 `ConfigUpdate`。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `AdminConfigResponse` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 415 | `application/json` / `Error` | 写请求Content-Type分号前部分不是精确application/json。 |
| 400 | `application/json` / `Error` | JSON语法/类型/未知字段/空正文/多JSON值或4 MiB读取上限错误；当前返回400而非413。 |
| 409 | `application/json` / `Error / RevisionConflict` | 共享revision过期。resume也可能因进程正在退出而返回无revision错误。 |
| 422 | `application/json` / `Error` | 配置、保护阈值、路径、XML或参数的业务校验失败。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |

FreeSWITCH对照：`XML配置 / reloadxml`；关系 `analogous_not_wire_compatible`。保存下次启动参数，不是reloadxml的运行语义。

### 6.7 `GET /v1/guard` — 读取即时峰值保护

GET会结算令牌补充，不消费额度或增加准入计数。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `GuardResponse` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |

FreeSWITCH对照：`fsctl max_sessions / fsctl sps`；关系 `analogous_not_wire_compatible`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

### 6.8 `PUT /v1/guard` — 原子更新即时峰值保护

按revision检查，并同时受active/desired硬边界约束；先持久化再应用。降限不挂断已有通话，更新和启停不补满令牌。

正文：`application/json`；必需；模型 `GuardUpdate`。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `GuardResponse` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 415 | `application/json` / `Error` | 写请求Content-Type分号前部分不是精确application/json。 |
| 400 | `application/json` / `Error` | JSON语法/类型/未知字段/空正文/多JSON值或4 MiB读取上限错误；当前返回400而非413。 |
| 409 | `application/json` / `Error / RevisionConflict` | 共享revision过期。resume也可能因进程正在退出而返回无revision错误。 |
| 422 | `application/json` / `Error` | 配置、保护阈值、路径、XML或参数的业务校验失败。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |

FreeSWITCH对照：`fsctl max_sessions / fsctl sps`；关系 `analogous_not_wire_compatible`。采用桥接资源数，新增建立中限制和软降速，不等同FreeSWITCH session计数或fsctl语法。

### 6.9 `POST /v1/drain` — 暂停新初始呼叫

只修改当前draining，不持久化、不推进revision、不挂断存量。handler不解析正文；空/未知/非JSON正文不会因正文被拒绝，但仍要求Content-Type和CSRF。

正文：`application/json`；可省略；handler不解码正文，推荐`{}`。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `DrainState` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 415 | `application/json` / `Error` | 写请求Content-Type分号前部分不是精确application/json。 |

FreeSWITCH对照：`fsctl pause`；关系 `analogous_not_wire_compatible`。只控制RustSwitch新初始INVITE，不区分FreeSWITCH inbound/outbound，也不是优雅停机命令。

### 6.10 `POST /v1/resume` — 恢复按既有策略接入

只修改当前draining，不持久化、不推进revision、不挂断存量。handler不解析正文；空/未知/非JSON正文不会因正文被拒绝，但仍要求Content-Type和CSRF。stopping=true时409。

正文：`application/json`；可省略；handler不解码正文，推荐`{}`。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `DrainState` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 415 | `application/json` / `Error` | 写请求Content-Type分号前部分不是精确application/json。 |
| 409 | `application/json` / `Error / RevisionConflict` | 共享revision过期。resume也可能因进程正在退出而返回无revision错误。 |

FreeSWITCH对照：`fsctl resume`；关系 `analogous_not_wire_compatible`。只控制RustSwitch新初始INVITE，不区分FreeSWITCH inbound/outbound，也不是优雅停机命令。

### 6.11 `GET /v1/fs-config` — 读取FreeSWITCH配置草稿目录

基线叠加保存改动后的文件和参数，仅编辑导出，不是模块运行清单。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `FSConfigResponse` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |

FreeSWITCH对照：`conf/vanilla XML`；关系 `export_artifact_only`。配置编辑与导出可用，RustSwitch不执行XML业务语义。

### 6.12 `PUT /v1/fs-config` — 按参数ID保存差量修改

只发送本次ID到值的差量，XML字符自动转义。未知ID返回422；省略/null/空映射不改XML但仍推进revision。

正文：`application/json`；必需；模型 `FSParametersUpdate`。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `FSConfigResponse` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 415 | `application/json` / `Error` | 写请求Content-Type分号前部分不是精确application/json。 |
| 400 | `application/json` / `Error` | JSON语法/类型/未知字段/空正文/多JSON值或4 MiB读取上限错误；当前返回400而非413。 |
| 409 | `application/json` / `Error / RevisionConflict` | 共享revision过期。resume也可能因进程正在退出而返回无revision错误。 |
| 422 | `application/json` / `Error` | 配置、保护阈值、路径、XML或参数的业务校验失败。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |

FreeSWITCH对照：`conf/vanilla XML属性`；关系 `export_artifact_only`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

### 6.13 `GET /v1/fs-config/file` — 读取单个XML草稿

返回包含xml字符串的JSON，不直接返回XML，不读取任意本机文件。

请求正文：无。

查询 `path`：当前只使用Query().Get的首个path；其他参数和重复path未统一拒绝。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `FSFileResponse` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 400 | `application/json` / `Error` | 查询/路径格式不合法；文档file入口比FS文件入口采用更严格的参数规则。 |
| 404 | `application/json` / `Error` | 路径语法合法，但文件不在对应目录或文档允许清单。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |

FreeSWITCH对照：`conf/vanilla XML文件`；关系 `export_artifact_only`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

### 6.14 `PUT /v1/fs-config/file` — 保存或新增XML文件

先校验路径/XML再检查revision。单文件256 KiB、覆盖/新增最多512个、合计5 MiB；无删除接口。允许多根片段，不执行预处理。

正文：`application/json`；必需；模型 `FSFileUpdate`。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `FSFileUpdated` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 415 | `application/json` / `Error` | 写请求Content-Type分号前部分不是精确application/json。 |
| 400 | `application/json` / `Error` | JSON语法/类型/未知字段/空正文/多JSON值或4 MiB读取上限错误；当前返回400而非413。 |
| 409 | `application/json` / `Error / RevisionConflict` | 共享revision过期。resume也可能因进程正在退出而返回无revision错误。 |
| 422 | `application/json` / `Error` | 配置、保护阈值、路径、XML或参数的业务校验失败。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |

FreeSWITCH对照：`conf/vanilla XML文件`；关系 `export_artifact_only`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

### 6.15 `GET /v1/fs-config/export` — 导出完整XML配置ZIP

包含conf/树、LICENSE.freeswitch、SOURCE.json和README-中文.txt。下载不部署或重载。合并集合失败返回500；ZIP构建错误当前handler直接返回，可能得到空/不完整响应，不能保证结构化500。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/zip` / `原始正文` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |

FreeSWITCH对照：`conf/vanilla配置分发`；关系 `export_artifact_only`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

### 6.16 `GET /v1/docs` — 读取文档清单

文档版本1.3.0，应用仍0.3.0。SHA-256和字节数对应原始文件，条目数不等于兼容认证通过数。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `DocsManifest` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |

FreeSWITCH对照：`无同名FreeSWITCH HTTP入口`；关系 `internal_contract_only`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

### 6.17 `GET /v1/docs/openapi.json` — 下载OpenAPI 3.1描述

本接口规范不代表FreeSWITCH原始API或ESL已实现。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `OpenAPIFile` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |
| 404 | `application/json` / `Error` | 路径语法合法，但文件不在对应目录或文档允许清单。 |

FreeSWITCH对照：`无同名FreeSWITCH HTTP入口`；关系 `internal_contract_only`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

### 6.18 `GET /v1/docs/comparison.json` — 下载完整接口对照

结合status、relationship、differences和verification解释结果，禁止把静态条目当差分验收。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/json` / `Comparison` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |
| 404 | `application/json` / `Error` | 路径语法合法，但文件不在对应目录或文档允许清单。 |

FreeSWITCH对照：`无同名FreeSWITCH HTTP入口`；关系 `internal_contract_only`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

### 6.19 `GET /v1/docs/file` — 读取允许清单内原始文档

返回原始MD/JSON/CSV/头文件/文本，不执行Markdown或HTML，不读取任意磁盘路径。

请求正文：无。

查询 `path`：仅允许恰好一个path；缺失/重复/未知参数/解码错误/绝对路径/..穿越/反斜线400；合法但不在清单404。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `text/markdown` / `原始正文`, `application/json` / `原始正文`, `text/csv` / `原始正文`, `text/plain` / `原始正文` | 原始正文，文本带UTF-8声明；具体MIME由允许文件类型决定。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 400 | `application/json` / `Error` | 查询/路径格式不合法；文档file入口比FS文件入口采用更严格的参数规则。 |
| 404 | `application/json` / `Error` | 路径语法合法，但文件不在对应目录或文档允许清单。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |

FreeSWITCH对照：`无同名FreeSWITCH HTTP入口`；关系 `internal_contract_only`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

### 6.20 `GET /v1/docs/export.zip` — 下载完整文档ZIP

包含manifest.json和documents列出的全部文件；不写配置，不执行文档。

请求正文：无。

| HTTP | 正文/类型 | 说明 |
| --- | --- | --- |
| 200 | `application/zip` / `原始正文` | 成功。 |
| 403 | `application/json` / `Error` | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 |
| 500 | `application/json` / `Error` | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 |

FreeSWITCH对照：`无同名FreeSWITCH HTTP入口`；关系 `internal_contract_only`。这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。

## 7. 静态调用示例

以下命令和代码仅供复制参考，页面或本文不会自动运行。写入前先选择要操作的实例并检查返回配置。cURL示例使用`jq`构造完整请求；不要把CSRF写入共享日志。

### cURL：读取、修改重试建议、排空和下载

```sh
base='http://127.0.0.1:9080'
curl --fail-with-body "$base/healthz"
curl --fail-with-body "$base/v1/config" -o config-snapshot.json

# 保留全部策略，只改一个值；revision来自同一份快照。
jq '{revision, policy: (.guard.policy | .retry_after_seconds = 2)}' \
  config-snapshot.json > guard-request.json
csrf="$(jq -r .csrf_token config-snapshot.json)"
curl --fail-with-body -X PUT "$base/v1/guard" \
  -H 'Content-Type: application/json' -H "X-RustSwitch-CSRF: $csrf" \
  --data-binary @guard-request.json

# 排空不需要revision，也不会结束已有通话；恢复需另发POST /v1/resume。
curl --fail-with-body -X POST "$base/v1/drain" \
  -H 'Content-Type: application/json' -H "X-RustSwitch-CSRF: $csrf" --data '{}'

# 查询路径来自文档清单，--data-urlencode处理转义。
curl --fail-with-body "$base/v1/docs" -o manifest.json
curl --fail-with-body --get "$base/v1/docs/file" \
  --data-urlencode 'path=api/http-reference.md' -o http-reference.md
curl --fail-with-body "$base/v1/docs/export.zip" -o rustswitch-interface-docs.zip
```

409时不要自动重试以上PUT。重新读取配置并人工合并后再决定提交；若令牌因重启失效，重新读取CSRF和revision。部署了不同管理端口时，更换base，不能只伪造Host。

### Python：完整快照更新与失败处理

```python
import json
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:9080"

def get_json(path):
    with urllib.request.urlopen(BASE + path, timeout=5) as response:
        return json.load(response)

def set_retry_after(seconds):
    snapshot = get_json("/v1/config")
    policy = dict(snapshot["guard"]["policy"])
    policy["retry_after_seconds"] = seconds
    payload = {"revision": snapshot["revision"], "policy": policy}
    request = urllib.request.Request(
        BASE + "/v1/guard", method="PUT",
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json",
                 "X-RustSwitch-CSRF": snapshot["csrf_token"]},
    )
    try:
        with urllib.request.urlopen(request, timeout=5) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        detail = error.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {error.code}: {detail}") from error

# 本示例没有调用写函数；需要时由操作者显式调用 set_retry_after(2)。
```

完整Config更新同样先GET，将`active`或确认过的`desired`完整复制到请求的`config`字段；只发送一两个字段会把其他字段变为零值并可能关闭可选功能。XML参数更新则是例外：`overrides`明确采用差量，但ID必须来自同一revision目录。

## 8. 指标字典与观测限制

`/metrics`输出Prometheus文本0.0.4；现有代码没有HELP/TYPE行，下面的“计数/即时”是语义说明。所有控制计数随进程重启归零，媒体计数随worker代次重启归零；异常窗口需同时观察健康、代次和采样错误。

状态接口不是事务一致的全局快照。媒体监督通常每秒请求stats；页面通常每3秒轮询，不代表HTTP端点缓存TTL。首次媒体采样前`worker.stats={}`；重启窗口可能短暂保留旧采样，不能假定当前generation与stats天然一致。`socket_drop_counter_supported=false`时socket_rx_drops为零也不证明无内核丢包。tx_packets只证明发送调用接受，不能证明网卡发送或对端收到。

控制与Guard指标没有标签。媒体压力指标带`worker`，阻塞指标额外带`reason`；媒体stats数值带`worker`、`generation`。`socket_drop_counter_supported`是JSON布尔值，当前不会输出为Prometheus数值指标。

| 指标 | 语义 | 类型 |
| --- | --- | --- |
| `rustswitch_sip_received_total` | 收到的SIP UDP报文 | 累计 |
| `rustswitch_sip_malformed_total` | 解析失败 | 累计 |
| `rustswitch_sip_untrusted_total` | 来源白名单拒绝 | 累计 |
| `rustswitch_sip_queue_drops_total` | 输入队列满丢弃 | 累计 |
| `rustswitch_sip_send_errors_total` | 信令发送错误 | 累计 |
| `rustswitch_calls_accepted_total` | 完成资源预留的新呼叫，不等于接通 | 累计 |
| `rustswitch_calls_rejected_total` | 容量/保护拒绝的新呼叫，不包括所有语法错误 | 累计 |
| `rustswitch_calls_completed_total` | 业务结束呼叫 | 累计 |
| `rustswitch_calls_abnormal_total` | 异常结束呼叫 | 累计 |
| `rustswitch_active_timers` | 活跃定时器 | 即时 |
| `rustswitch_active_calls` | 仍持有预留资源 | 即时 |
| `rustswitch_established_calls` | 成功ACK且尚未业务结束 | 即时 |
| `rustswitch_journal_written_total` | 已编码到缓冲事件 | 累计 |
| `rustswitch_journal_synced_total` | 已完成Flush/Sync事件 | 累计 |
| `rustswitch_journal_lost_total` | 被拒绝/写入失败事件 | 累计 |
| `rustswitch_guard_effective_cps` | 当前Guard补充速率 | 即时 |
| `rustswitch_guard_active_limit` | 保存策略的max_active_calls；enabled=false时实际用hard_limits | 策略值 |
| `rustswitch_guard_establishing_limit` | 保存策略的建立中阈值；enabled=false时不应用额外阈值 | 策略值 |
| `rustswitch_guard_rejected_total` | 仅Guard拒绝数 | 累计 |
| `rustswitch_guard_rejected_active_total` | Guard总并发拒绝 | 累计 |
| `rustswitch_guard_rejected_establishing_total` | Guard建立中拒绝 | 累计 |
| `rustswitch_guard_rejected_rate_total` | Guard令牌不足拒绝 | 累计 |
| `rustswitch_media_admission_blocked` | worker/reason维度；非空原因1，否则0 | 即时 |
| `rustswitch_media_cpu_cores_used` | worker维度CPU差分占用核心数 | 采样 |
| `rustswitch_media_user_cpu_seconds` | 进程所有线程累计用户态 CPU 秒数。 | 进程累计 |
| `rustswitch_media_system_cpu_seconds` | 进程所有线程累计内核态 CPU 秒数。 | 进程累计 |
| `rustswitch_media_peak_resident_bytes` | 进程启动以来的常驻内存峰值，统一换算为字节。 | 进程历史峰值 |
| `rustswitch_media_connected_sockets` | 统计时已经连接对端的 socket 数量。 | 即时采样 |
| `rustswitch_media_peer_changed_drops` | 对端变更时清理发送队列而丢弃的包数。 | 进程累计 |
| `rustswitch_media_active_calls` | 当前已分配的媒体会话数，不限定 SIP 是否已经接通。 | 即时采样 |
| `rustswitch_media_available_blocks` | 当前可尝试绑定的四端口块数，不含仍在隔离期的块。 | 即时采样 |
| `rustswitch_media_receive_syscalls` | 包括 WouldBlock 在内的接收调用总数。 | 进程累计 |
| `rustswitch_media_receive_batches` | 成功返回的接收批次数，可结合 rx_packets 估算实际批次大小。 | 进程累计 |
| `rustswitch_media_max_receive_batch` | 启动以来单次接收到的最大包数，不能替代平均批次指标。 | 进程历史峰值 |
| `rustswitch_media_rx_packets` | 从 socket 读取的报文总数，包含之后被校验拒绝的报文。 | 进程累计 |
| `rustswitch_media_rx_bytes` | 内核报告的数据报字节总数，Linux 上可包括超出缓冲的原始长度。 | 进程累计 |
| `rustswitch_media_tx_packets` | 被发送调用完整接受的包数，不能证明已越过网卡或到达对端。 | 进程累计 |
| `rustswitch_media_tx_bytes` | 与 tx_packets 对应的累计发送字节。 | 进程累计 |
| `rustswitch_media_invalid_packets` | 长度、RTP/RTCP 结构或协商负载类型校验失败的包数。 | 进程累计 |
| `rustswitch_media_source_rejected` | 源地址不匹配的拒绝数；connected UDP 被内核先过滤的包不会计入。 | 进程累计 |
| `rustswitch_media_peer_not_ready` | B 腿尚未设置时到达、无法转发的包数。 | 进程累计 |
| `rustswitch_media_rate_limited` | 超出对应 RTP/RTCP 令牌桶额度的包数。 | 进程累计 |
| `rustswitch_media_send_errors` | 非 WouldBlock 发送失败或异常发送长度次数。 | 进程累计 |
| `rustswitch_media_receive_errors` | 排除 WouldBlock/Interrupted 的接收错误次数。 | 进程累计 |
| `rustswitch_media_send_queue_drops` | 发送队列达到固定长度后拒绝的新包数。 | 进程累计 |
| `rustswitch_media_send_expired` | 重试前已经超过短发送期限的包数。 | 进程累计 |
| `rustswitch_media_socket_rx_drops` | 从 Linux SO_RXQ_OVFL 辅助字段累计得到的 socket 接收溢出增量。 | 进程累计 |

## 9. 完整schema字段字典

全部命名模型、每个字段、必需/可空规则、嵌套约束与完整JSON定义见 [完整接口字段字典](schema-reference.md)。字典直接从OpenAPI生成，并由发布门禁验证同步；后台“数据结构”页也逐模型展示同一机器合同。

## 10. 静态资源、FreeSWITCH关系与来源

静态入口：`GET /`返回HTML，`GET /app.css`与`/docs.css`返回CSS，`GET /app.js`与`/docs.js`返回JavaScript。均采用UTF-8类型说明，受Host中间件约束，内嵌资源缺失返回500 JSON；不接受任意文件路径。它们属于页面资源，不计入26个显式API操作。文档原始Markdown/CSV/头文件从受控文档入口读取，不作为可执行页面加载。

每个OpenAPI operation均含`x-freeswitch`：`interface`为可比较的原版入口/配置面，`relationship`说明关系，`difference`列具体差异，`status`说明本RustSwitch入口状态，`reference_url`为固定参考来源。自有HTTP入口的`implemented`不意味着原版对应命令已兼容；`export_artifact_only`尤其只表示可导出的文档/配置产物。

当前事实来自 `control/internal/server/{server,admin,admin_store,guard,fs_config,documentation}.go`、`control/internal/config/config.go`、`control/internal/media/{pool,pressure}.go` 和 `media/src/media/protocol.rs`。OpenAPI命名、错误状态与schema以这些处理器当前实现为准；没有为预期将来能力伪造运行入口。

FreeSWITCH相近命令存在性及语义参考官方固定源码：

- [mod_commands：status、fsctl、reloadxml](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c)
- [FreeSWITCH 1.11.3 vanilla配置树](https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a/conf/vanilla)
- [mod_event_socket：原版事件套接字实现](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/event_handlers/mod_event_socket/mod_event_socket.c)

完整兼容要求和未实现范围应结合文档清单中的FreeSWITCH兼容标准及comparison.json阅读；文档发布、配置编辑、单元测试通过均不替代原版客户端差分测试、Linux万路实机验证或故障接管验收。

## 压力与电话测试扩展

后台 `/#tests` 使用新增的五个操作：`GET /v1/tests`、`POST /v1/tests`、`GET /v1/tests/{id}`、`POST /v1/tests/{id}/stop`、`GET /v1/tests/{id}/report`。完整请求、状态、结果和 FreeSWITCH 差异见[压力模拟与测试小电话接口](testing-reference.md)，机读模型与全部错误状态在[OpenAPI](openapi.json)发布。静态资源另增加 `/tests.js`、`/tests.css`。

测试作业使用 `request_id` 幂等键和任务 ID，POST 沿用 CSRF，不读取或递增配置 revision。测试实例只在本机独立启动，不把当前业务服务的 CPS/并发阈值改成测试档位。创建返回 202 不等于测试通过；报告及 cleanup 给出最终结果。

压力页面恢复的是读取状态，不是恢复已结束的测试进程或跨重启历史。服务仅在当前进程保留最近 20 份报告；报告淘汰或重启后，详情返回 404 时清除对应选择并重新读取当前任务。列表读取后若详情恰好变成 404／410，也只使该记录过期；其他单条详情错误不应误报整个后台离线。网络中断保留上次观测，恢复后继续读取。报告需要跨重启保存时必须事先下载。

同一进程中，已淘汰报告的原 `request_id` 返回 410，不重新运行；进程重启后这些内存幂等键也会丢失。因此读取恢复不自动重发旧压测创建，网络超时也不自动重放写入。明确的 CSRF 拒绝可按第 1 节只刷新令牌并重试一次，保持原 request_id。清理未确认仍冻结新任务入口，不能通过刷新页面跳过资源清理。

## 11. 控制进程资源与管理预算（1.2 新增）

完整行为及限制见[稳定性说明](stability-reference.md)。`Status.controller` 为必填；旧压力报告的 Controller 可以缺省或为 null。以下各字段在对象存在时均返回。

| 字段 | 类型 | 含义 |
|---|---|---|
| `sampled_at` | `string` | 运行时资源采样时刻；HTTP读取复用最多一秒的缓存。 |
| `goroutines` | `integer` | 当前Go控制进程协程数，不是操作系统线程数。 |
| `peak_sampled_goroutines` | `integer` | 本控制进程历次管理观测的协程峰值；采样间隙的峰值可能遗漏。 |
| `runtime_threads` | `integer / null` | Go运行时拥有的存活线程数；旧Go版本不支持时为null，并非零线程。 |
| `gomaxprocs` | `integer` | 可同时执行Go代码的线程额度，不是进程总线程数。 |
| `heap_allocated_bytes` | `integer` | Go堆对象占用字节。 |
| `go_memory_bytes` | `integer` | Go映射内存扣除已归还堆页，不等于进程RSS或主机内存。 |
| `user_cpu_seconds` | `number / null` | 本Go控制进程累计用户态CPU秒数；采集失败时为null。 |
| `system_cpu_seconds` | `number / null` | 本Go控制进程累计内核态CPU秒数；采集失败时为null。 |
| `cpu_cores_used` | `number / null` | 相邻成功采样间消耗的CPU核数，允许超过1；首次采样或取样失败为null。 |
| `normal_queue_length` | `integer` | 当前普通信令队列中的待处理包数。 |
| `normal_queue_capacity` | `integer` | 实际普通信令队列容量。 |
| `critical_queue_length` | `integer` | 当前已有对话优先信令队列中的包数。 |
| `critical_queue_capacity` | `integer` | 实际已有对话优先队列容量。 |
| `media_results_queue_length` | `integer` | 当前媒体控制完成结果等待主循环处理的数量。 |
| `media_results_queue_capacity` | `integer` | 实际媒体结果队列容量。 |
| `admin_connections` | `integer` | HTTP已经接纳且尚未关闭的管理TCP连接数。 |
| `admin_connection_limit` | `integer` | 固定管理TCP连接额度；在Accept之前取得，额外连接等待系统监听队列。 |
| `admin_heavy_requests` | `integer` | 正在构造/传输文档、XML、报告或保存完整配置的重请求数。 |
| `admin_heavy_request_limit` | `integer` | 固定重请求并发预算，不影响呼叫并发上限。 |
| `admin_heavy_rejected_total` | `integer` | 因重请求额度不足返回503的累计次数，不是呼叫拒绝数。 |

新增 Prometheus 指标如下，无标签。可空字段在未知时不输出指标。队列容量通过 JSON 字段读取。

| 指标 | 对应 controller 字段 |
|---|---|
| `rustswitch_controller_goroutines` | `goroutines` |
| `rustswitch_controller_peak_sampled_goroutines` | `peak_sampled_goroutines` |
| `rustswitch_controller_gomaxprocs` | `gomaxprocs` |
| `rustswitch_controller_heap_allocated_bytes` | `heap_allocated_bytes` |
| `rustswitch_controller_go_memory_bytes` | `go_memory_bytes` |
| `rustswitch_controller_runtime_threads` | `runtime_threads` |
| `rustswitch_controller_user_cpu_seconds` | `user_cpu_seconds` |
| `rustswitch_controller_system_cpu_seconds` | `system_cpu_seconds` |
| `rustswitch_controller_cpu_cores_used` | `cpu_cores_used` |
| `rustswitch_controller_normal_queue_length` | `normal_queue_length` |
| `rustswitch_controller_critical_queue_length` | `critical_queue_length` |
| `rustswitch_controller_media_results_queue_length` | `media_results_queue_length` |
| `rustswitch_admin_connections` | `admin_connections` |
| `rustswitch_admin_connection_limit` | `admin_connection_limit` |
| `rustswitch_admin_heavy_requests` | `admin_heavy_requests` |
| `rustswitch_admin_heavy_request_limit` | `admin_heavy_request_limit` |
| `rustswitch_admin_heavy_rejected_total` | `admin_heavy_rejected_total` |


## 12. 音频能力与资源预检（1.3 新增）

`GET /v1/codecs`（operationId `getCodecs`）返回全部测试预设、本机原生音频后端及当前 worker 实时处理能力。输入无正文，无业务参数；响应为 `CodecCatalog`，正常 200、Host 检查失败 403、管理重请求预算耗尽 503。后端缺库或探测失败仍为200，分别用原生条目不可用或 `native_audio=null/native_probe_error` 说明，不能理解为实时转码可用。探测缓存与 CLI 约束详见 [音频完整合同](audio-reference.md)。

FreeSWITCH 对应概念是实际模块目录与 `show codecs`，但本接口 JSON、传输和能力边界不同，不能让 ESL 客户端直接替换调用。

`MediaConfig/MediaConfigInput.excluded_port_blocks` 是可省略的四端口块起点数组；每块相对 `port_start` 四对齐、在完整块范围内且不得重复。受保护块不参与分配，每个分片独立验证剩余容量；配置仍遵循保存后重启规则。

测试扩展：`TestCapabilities.codec_profiles/port_capacity`、`TestRun.failure`、`TestEvidence.failure`、`TestPorts.excluded_port_blocks`、`TestResourcePreflight.port_capacity/required_coordinator_fds`、`TestGeneratorResult.codec/validation_scope`。资源预检尚未启动时没有发生器结果，不能根据空结果判定媒体丢包。完整字段见 [字段字典](schema-reference.md) 和 OpenAPI。

## 13. 实时处理与测试模式（1.10）

`media.processing` 支持 `relay` 与 `g711`，省略或空值沿用 relay；配置保存后按正常重启流程生效。`/v1/codecs.runtime_media` 每次读取预期 worker 的进程身份、代次、健康和 ready 能力；只有启用 g711 且全部分片就绪时 `live_transcoding=true`。原生 SDK 的缓存自检独立保留，不能替代运行状态。

`POST /v1/tests.media_processing` 独立冻结隔离实例的媒体模式。g711 限 PCMU/PCMA、8k 单声道、20ms 双腿异律处理。新 `TestSample.processing`、`ProcessedGeneratorResult`、`ProcessedDiagnostics` 以及拆线后统计证据均在 [OpenAPI](openapi.json) 中完整列出；页面操作、错误和 FreeSWITCH 对照见[处理压测合同](processed-testing-reference.md)。
