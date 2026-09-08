# 04 HTTP 管理接口参考

本文面向管理后台集成和运维工具开发者，说明 RustSwitch 主源码的全部 **26 个 HTTP 操作**。接口基线为 **1.13.0**，描述格式为 OpenAPI 3.1.0；77 个数据模型见[数据模型参考](09-数据模型全文.md)，机器可读合同见 [OpenAPI JSON](reference/docs/api/openapi.json)。

本文保留操作标识、参数、状态码、响应模型及 FreeSWITCH 对照。ASR 候选使用独立内部接口，未在此基线增加 HTTP 路径；候选字段建议见[ASR 模型增量](candidate/asr-openapi-update-proposal.md)。版本关系与证据范围见[文档中心](DOCUMENTATION.md)。

## 通用调用合同

| 项目 | 调用规则 |
| --- | --- |
| 服务地址 | 默认地址为 `http://127.0.0.1:9080`，以当前生效的管理监听配置为准。管理服务限定回环访问。 |
| Host 校验 | 使用精确 `localhost` 或回环 IP 字面量；自定义域名即使解析到回环也会被拒绝。 |
| 身份与请求保护 | CSRF 用于请求来源保护，不提供用户账户认证。GET / HEAD 无需 CSRF 令牌。 |
| 写请求 | 使用 `Content-Type: application/json` 和有效 `X-RustSwitch-CSRF`；非空 `Origin` 必须同源。 |
| 令牌获取 | 通过 `GET /v1/config` 取得令牌，字段和返回结构以 `getConfig` 操作为准。 |
| 并发更新 | 配置、Guard 与 XML 草稿共享 `revision`。收到 `409` 后保留本地草稿，读取新版本并处理冲突。 |
| 令牌过期 | 仅在明确收到令牌失效 `403` 且响应头为 `X-RustSwitch-CSRF-Refresh: required` 时，刷新并重试原请求一次。 |
| 超时恢复 | 写请求超时可能已产生副作用，应读取实际状态后核对；按具体操作的幂等规则决定是否重试。 |
| 完整替换 | 完整 `PUT` 不自动合并省略字段；XML 参数差量更新等例外以对应操作说明为准。 |
| 错误解析 | 常见错误使用 JSON `error` 对象；路由 `404` / `405` 可能为文本，`/readyz` 的 `503` 存在已记录的 Content-Type 例外。 |

输入校验、请求体预算、响应头和错误例外详见 [HTTP 专题](reference/docs/api/http-reference.md)。

## 接入顺序与状态判读

1. 使用 `GET /healthz` 核对进程响应与组件版本，再通过 `GET /readyz` 判断新呼叫准入是否就绪。
2. 读取 `GET /v1/status` 获取控制面和媒体状态；服务存活、媒体健康和可用容量应分别判读。
3. 需要修改配置时，先读取配置及共享版本，再按目标操作提交；区分即时生效和待重启草稿。
4. 启动测试后，按任务 ID 跟踪进度并读取最终报告；同时确认发生器结果、实际负载及资源清理。

```bash
curl --fail-with-body http://127.0.0.1:9080/healthz
curl --fail-with-body http://127.0.0.1:9080/readyz
curl --fail-with-body http://127.0.0.1:9080/v1/status
```

以上命令要求本机服务已启动。`--fail-with-body` 在非成功 HTTP 响应时保留正文并返回失败退出码，便于检查就绪失败等实际原因。

## FreeSWITCH 对照说明

每个操作后附对应入口与语义差异。RustSwitch 自有 HTTP 管理接口与 FreeSWITCH 的 ESL、CLI 或 XML 配置属于不同调用方式；迁移时须同时核对参数、响应、事件、副作用和配置生效时机。对照关系提供迁移依据，单个 HTTP 操作存在不表示对应 FreeSWITCH 模块已完整实现。

## 操作目录

| 方法 | 路径 | operationId | 用途 |
|---|---|---|---|
| GET | `/healthz` | `getHealth` | 读取进程存活和版本 |
| GET | `/readyz` | `getReadiness` | 读取新呼叫准入就绪 |
| GET | `/v1/status` | `getStatus` | 读取控制与媒体观测 |
| GET | `/metrics` | `getMetrics` | 读取Prometheus指标 |
| GET | `/v1/config` | `getConfig` | 读取运行配置和待启动草稿 |
| PUT | `/v1/config` | `putConfig` | 保存完整待启动配置 |
| GET | `/v1/guard` | `getGuard` | 读取即时峰值保护 |
| PUT | `/v1/guard` | `putGuard` | 原子更新即时峰值保护 |
| POST | `/v1/drain` | `drain` | 暂停新初始呼叫 |
| POST | `/v1/resume` | `resume` | 恢复按既有策略接入 |
| GET | `/v1/fs-config` | `getFSConfig` | 读取FreeSWITCH配置草稿目录 |
| PUT | `/v1/fs-config` | `putFSConfig` | 按参数ID保存差量修改 |
| GET | `/v1/fs-config/file` | `getFSFile` | 读取单个XML草稿 |
| PUT | `/v1/fs-config/file` | `putFSFile` | 保存或新增XML文件 |
| GET | `/v1/fs-config/export` | `getFSExport` | 导出完整XML配置ZIP |
| GET | `/v1/docs` | `getDocs` | 读取文档清单 |
| GET | `/v1/docs/openapi.json` | `getOpenAPI` | 下载OpenAPI 3.1描述 |
| GET | `/v1/docs/comparison.json` | `getComparison` | 下载完整接口对照 |
| GET | `/v1/docs/file` | `getDocument` | 读取允许清单内原始文档 |
| GET | `/v1/docs/export.zip` | `getDocsExport` | 下载完整文档ZIP |
| GET | `/v1/tests` | `getTests` | 读取测试能力与任务列表 |
| POST | `/v1/tests` | `startTest` | 启动压力或单路媒体测试 |
| GET | `/v1/tests/{id}` | `getTest` | 读取指定测试进度 |
| POST | `/v1/tests/{id}/stop` | `stopTest` | 停止压力测试或挂断测试小电话 |
| GET | `/v1/tests/{id}/report` | `getTestReport` | 下载测试结果与失败证据 |
| GET | `/v1/codecs` | `getCodecs` | 读取音频传输与原生后端能力 |

## getHealth · GET /healthz

读取进程存活和版本

可以响应不表示媒体健康或有新呼叫容量。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [Health](09-数据模型全文.md#health) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "status",
  "relationship": "analogous_not_wire_compatible",
  "difference": "只报告本HTTP进程存活，不是FreeSWITCH status的结构和文本。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## getReadiness · GET /readyz

读取新呼叫准入就绪

合并排空、日志健康、Guard.limit_reason及至少一个媒体分片Admission；不检查全部私有容量状态，不能保证下一次媒体分配成功。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [Readiness](09-数据模型全文.md#readiness) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 暂不可接入，正文为ready=false；当前先WriteHeader再writeJSON，实际Content-Type可能缺失。 | application/json [Readiness](09-数据模型全文.md#readiness) |

### FreeSWITCH 对照

```json
{
  "interface": "fsctl pause_check / status",
  "relationship": "analogous_not_wire_compatible",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## getStatus · GET /v1/status

读取控制与媒体观测

返回控制计数、保护器和媒体分片；没有逐通话清单、ESL事件流或完整CDR。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [Status](09-数据模型全文.md#status) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "status / show calls / show channels",
  "relationship": "analogous_not_wire_compatible",
  "difference": "计数/身份/字段不同，没有实现show calls/channels的原始API。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## getMetrics · GET /metrics

读取Prometheus指标

text/plain; version=0.0.4，无HELP/TYPE声明；累计和即时指标须按指标字典区分。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | text/plain `{"type":"string"}` |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "status / 媒体模块统计",
  "relationship": "analogous_not_wire_compatible",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## getConfig · GET /v1/config

读取运行配置和待启动草稿

读取共享revision、当前进程CSRF令牌、active/desired、持久化状态和Guard。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [AdminConfigResponse](09-数据模型全文.md#adminconfigresponse) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "XML配置 / reloadxml",
  "relationship": "analogous_not_wire_compatible",
  "difference": "配置为RustSwitch JSON，不执行FreeSWITCH XML或reloadxml。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a/conf/vanilla"
}
```

## putConfig · PUT /v1/config

保存完整待启动配置

先校验完整config再检查revision；禁止修改media.binary与journal.path；当前Guard必须适合新的硬上限。保存不切换当前socket/worker，下次启动应用desired。

**写入边界：** 遵守通用Content-Type/CSRF/Origin要求；幂等性和revision以本操作正文为准。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |
| Origin | header | false | `{"type":"string","description":""}` | 非空时须scheme=http且URL.Host与请求Host完全相同；省略允许。 |

### 请求正文

单个JSON对象；Go重复已知键采用后值覆盖/对象合并，省略按零值，不是PATCH。

必需：true.

媒体类型：`application/json`；模型：[ConfigUpdate](09-数据模型全文.md#configupdate)

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [AdminConfigResponse](09-数据模型全文.md#adminconfigresponse) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 415 | 写请求Content-Type分号前部分不是精确application/json。 | application/json [Error](09-数据模型全文.md#error) |
| 400 | JSON语法/类型/未知字段/空正文/多JSON值或4 MiB读取上限错误；当前返回400而非413。 | application/json [Error](09-数据模型全文.md#error) |
| 409 | 共享revision过期。resume也可能因进程正在退出而返回无revision错误。 | application/json `{"oneOf":[{"$ref":"#/components/schemas/Error"},{"$ref":"#/components/schemas/RevisionConflict"}]}` |
| 422 | 配置、保护阈值、路径、XML或参数的业务校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "XML配置 / reloadxml",
  "relationship": "analogous_not_wire_compatible",
  "difference": "保存下次启动参数，不是reloadxml的运行语义。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a/conf/vanilla"
}
```

## getGuard · GET /v1/guard

读取即时峰值保护

GET会结算令牌补充，不消费额度或增加准入计数。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [GuardResponse](09-数据模型全文.md#guardresponse) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "fsctl max_sessions / fsctl sps",
  "relationship": "analogous_not_wire_compatible",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## putGuard · PUT /v1/guard

原子更新即时峰值保护

按revision检查，并同时受active/desired硬边界约束；先持久化再应用。降限不挂断已有通话，更新和启停不补满令牌。

**写入边界：** 遵守通用Content-Type/CSRF/Origin要求；幂等性和revision以本操作正文为准。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |
| Origin | header | false | `{"type":"string","description":""}` | 非空时须scheme=http且URL.Host与请求Host完全相同；省略允许。 |

### 请求正文

单个JSON对象；Go重复已知键采用后值覆盖/对象合并，省略按零值，不是PATCH。

必需：true.

媒体类型：`application/json`；模型：[GuardUpdate](09-数据模型全文.md#guardupdate)

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [GuardResponse](09-数据模型全文.md#guardresponse) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 415 | 写请求Content-Type分号前部分不是精确application/json。 | application/json [Error](09-数据模型全文.md#error) |
| 400 | JSON语法/类型/未知字段/空正文/多JSON值或4 MiB读取上限错误；当前返回400而非413。 | application/json [Error](09-数据模型全文.md#error) |
| 409 | 共享revision过期。resume也可能因进程正在退出而返回无revision错误。 | application/json `{"oneOf":[{"$ref":"#/components/schemas/Error"},{"$ref":"#/components/schemas/RevisionConflict"}]}` |
| 422 | 配置、保护阈值、路径、XML或参数的业务校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "fsctl max_sessions / fsctl sps",
  "relationship": "analogous_not_wire_compatible",
  "difference": "采用桥接资源数，新增建立中限制和软降速，不等同FreeSWITCH session计数或fsctl语法。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## drain · POST /v1/drain

暂停新初始呼叫

只修改当前draining，不持久化、不推进revision、不挂断存量。handler不解析正文；空/未知/非JSON正文不会因正文被拒绝，但仍要求Content-Type和CSRF。

**写入边界：** 遵守通用Content-Type/CSRF/Origin要求；幂等性和revision以本操作正文为准。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |
| Origin | header | false | `{"type":"string","description":""}` | 非空时须scheme=http且URL.Host与请求Host完全相同；省略允许。 |
| Content-Type | header | true | `{"type":"string","description":""}` | 写接口必须application/json，可带分号参数；分号前不做大小写/空白归一化。OpenAPI工具通常由requestBody自动设置。 |

### 请求正文

正文不读取；推荐{}，不要求revision。

必需：false.

媒体类型：`application/json`；模型：`{}`

```json
{}
```

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [DrainState](09-数据模型全文.md#drainstate) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 415 | 写请求Content-Type分号前部分不是精确application/json。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "fsctl pause",
  "relationship": "analogous_not_wire_compatible",
  "difference": "只控制RustSwitch新初始INVITE，不区分FreeSWITCH inbound/outbound，也不是优雅停机命令。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## resume · POST /v1/resume

恢复按既有策略接入

只修改当前draining，不持久化、不推进revision、不挂断存量。handler不解析正文；空/未知/非JSON正文不会因正文被拒绝，但仍要求Content-Type和CSRF。stopping=true时409。

**写入边界：** 遵守通用Content-Type/CSRF/Origin要求；幂等性和revision以本操作正文为准。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |
| Origin | header | false | `{"type":"string","description":""}` | 非空时须scheme=http且URL.Host与请求Host完全相同；省略允许。 |
| Content-Type | header | true | `{"type":"string","description":""}` | 写接口必须application/json，可带分号参数；分号前不做大小写/空白归一化。OpenAPI工具通常由requestBody自动设置。 |

### 请求正文

正文不读取；推荐{}，不要求revision。

必需：false.

媒体类型：`application/json`；模型：`{}`

```json
{}
```

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [DrainState](09-数据模型全文.md#drainstate) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 415 | 写请求Content-Type分号前部分不是精确application/json。 | application/json [Error](09-数据模型全文.md#error) |
| 409 | 共享revision过期。resume也可能因进程正在退出而返回无revision错误。 | application/json `{"oneOf":[{"$ref":"#/components/schemas/Error"},{"$ref":"#/components/schemas/RevisionConflict"}]}` |

### FreeSWITCH 对照

```json
{
  "interface": "fsctl resume",
  "relationship": "analogous_not_wire_compatible",
  "difference": "只控制RustSwitch新初始INVITE，不区分FreeSWITCH inbound/outbound，也不是优雅停机命令。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## getFSConfig · GET /v1/fs-config

读取FreeSWITCH配置草稿目录

基线叠加保存改动后的文件和参数，仅编辑导出，不是模块运行清单。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [FSConfigResponse](09-数据模型全文.md#fsconfigresponse) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "conf/vanilla XML",
  "relationship": "export_artifact_only",
  "difference": "配置编辑与导出可用，RustSwitch不执行XML业务语义。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a/conf/vanilla"
}
```

## putFSConfig · PUT /v1/fs-config

按参数ID保存差量修改

只发送本次ID到值的差量，XML字符自动转义。未知ID返回422；省略/null/空映射不改XML但仍推进revision。

**写入边界：** 遵守通用Content-Type/CSRF/Origin要求；幂等性和revision以本操作正文为准。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |
| Origin | header | false | `{"type":"string","description":""}` | 非空时须scheme=http且URL.Host与请求Host完全相同；省略允许。 |

### 请求正文

单个JSON对象；Go重复已知键采用后值覆盖/对象合并，省略按零值，不是PATCH。

必需：true.

媒体类型：`application/json`；模型：[FSParametersUpdate](09-数据模型全文.md#fsparametersupdate)

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [FSConfigResponse](09-数据模型全文.md#fsconfigresponse) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 415 | 写请求Content-Type分号前部分不是精确application/json。 | application/json [Error](09-数据模型全文.md#error) |
| 400 | JSON语法/类型/未知字段/空正文/多JSON值或4 MiB读取上限错误；当前返回400而非413。 | application/json [Error](09-数据模型全文.md#error) |
| 409 | 共享revision过期。resume也可能因进程正在退出而返回无revision错误。 | application/json `{"oneOf":[{"$ref":"#/components/schemas/Error"},{"$ref":"#/components/schemas/RevisionConflict"}]}` |
| 422 | 配置、保护阈值、路径、XML或参数的业务校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "conf/vanilla XML属性",
  "relationship": "export_artifact_only",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a/conf/vanilla"
}
```

## getFSFile · GET /v1/fs-config/file

读取单个XML草稿

返回包含xml字符串的JSON，不直接返回XML，不读取任意本机文件。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |
| path | query | true | `{"type":"string","description":"安全相对 XML 路径；path.Clean必须等于原值，禁止绝对路径和穿越。","minLength":1,"maxLength":200,"pattern":"^[A-Za-z0-9_./-]+\\.xml$"}` | 当前只使用Query().Get的首个path；其他参数和重复path未统一拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [FSFileResponse](09-数据模型全文.md#fsfileresponse) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 400 | 查询/路径格式不合法；文档file入口比FS文件入口采用更严格的参数规则。 | application/json [Error](09-数据模型全文.md#error) |
| 404 | 路径语法合法，但文件不在对应目录或文档允许清单。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "conf/vanilla XML文件",
  "relationship": "export_artifact_only",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a/conf/vanilla"
}
```

## putFSFile · PUT /v1/fs-config/file

保存或新增XML文件

先校验路径/XML再检查revision。单文件256 KiB、覆盖/新增最多512个、合计5 MiB；无删除接口。允许多根片段，不执行预处理。

**写入边界：** 遵守通用Content-Type/CSRF/Origin要求；幂等性和revision以本操作正文为准。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |
| Origin | header | false | `{"type":"string","description":""}` | 非空时须scheme=http且URL.Host与请求Host完全相同；省略允许。 |

### 请求正文

单个JSON对象；Go重复已知键采用后值覆盖/对象合并，省略按零值，不是PATCH。

必需：true.

媒体类型：`application/json`；模型：[FSFileUpdate](09-数据模型全文.md#fsfileupdate)

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [FSFileUpdated](09-数据模型全文.md#fsfileupdated) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 415 | 写请求Content-Type分号前部分不是精确application/json。 | application/json [Error](09-数据模型全文.md#error) |
| 400 | JSON语法/类型/未知字段/空正文/多JSON值或4 MiB读取上限错误；当前返回400而非413。 | application/json [Error](09-数据模型全文.md#error) |
| 409 | 共享revision过期。resume也可能因进程正在退出而返回无revision错误。 | application/json `{"oneOf":[{"$ref":"#/components/schemas/Error"},{"$ref":"#/components/schemas/RevisionConflict"}]}` |
| 422 | 配置、保护阈值、路径、XML或参数的业务校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "conf/vanilla XML文件",
  "relationship": "export_artifact_only",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a/conf/vanilla"
}
```

## getFSExport · GET /v1/fs-config/export

导出完整XML配置ZIP

包含conf/树、LICENSE.freeswitch、SOURCE.json和README-中文.txt。下载不部署或重载。合并集合失败返回500；ZIP构建错误当前handler直接返回，可能得到空/不完整响应，不能保证结构化500。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/zip `{"type":"string","format":"binary"}` |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `200` 的头定义：

```json
{
  "Content-Disposition": {
    "schema": {
      "type": "string",
      "description": ""
    },
    "example": "attachment; filename=\"freeswitch-1.11.3-config.zip\""
  },
  "Content-Length": {
    "schema": {
      "type": "integer",
      "description": "非负累计数或即时数量。",
      "minimum": 0
    }
  }
}
```


响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "conf/vanilla配置分发",
  "relationship": "export_artifact_only",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a/conf/vanilla"
}
```

## getDocs · GET /v1/docs

读取文档清单

文档版本1.0.0，应用仍0.3.0。SHA-256和字节数对应原始文件，条目数不等于兼容认证通过数。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [DocsManifest](09-数据模型全文.md#docsmanifest) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "无同名FreeSWITCH HTTP入口",
  "relationship": "internal_contract_only",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a"
}
```

## getOpenAPI · GET /v1/docs/openapi.json

下载OpenAPI 3.1描述

本接口规范不代表FreeSWITCH原始API或ESL已实现。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [OpenAPIFile](09-数据模型全文.md#openapifile) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |
| 404 | 路径语法合法，但文件不在对应目录或文档允许清单。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `200` 的头定义：

```json
{
  "Content-Disposition": {
    "schema": {
      "type": "string",
      "description": "inline文件名，由mime.FormatMediaType编码。"
    }
  },
  "Content-Length": {
    "schema": {
      "type": "integer",
      "description": "非负累计数或即时数量。",
      "minimum": 0
    }
  }
}
```


响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "无同名FreeSWITCH HTTP入口",
  "relationship": "internal_contract_only",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a"
}
```

## getComparison · GET /v1/docs/comparison.json

下载完整接口对照

结合status、relationship、differences和verification解释结果，禁止把静态条目当差分验收。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/json [Comparison](09-数据模型全文.md#comparison) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |
| 404 | 路径语法合法，但文件不在对应目录或文档允许清单。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `200` 的头定义：

```json
{
  "Content-Disposition": {
    "schema": {
      "type": "string",
      "description": "inline文件名，由mime.FormatMediaType编码。"
    }
  },
  "Content-Length": {
    "schema": {
      "type": "integer",
      "description": "非负累计数或即时数量。",
      "minimum": 0
    }
  }
}
```


响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "无同名FreeSWITCH HTTP入口",
  "relationship": "internal_contract_only",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a"
}
```

## getDocument · GET /v1/docs/file

读取允许清单内原始文档

返回原始MD/JSON/CSV/头文件/文本，不执行Markdown或HTML，不读取任意磁盘路径。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |
| path | query | true | `{"type":"string","description":"从documents[].path原样取得。","minLength":1,"maxLength":240}` | 仅允许恰好一个path；缺失/重复/未知参数/解码错误/绝对路径/..穿越/反斜线400；合法但不在清单404。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 原始正文，文本带UTF-8声明；具体MIME由允许文件类型决定。 | text/markdown `{"type":"string"}`；application/json `{}`；text/csv `{"type":"string"}`；text/plain `{"type":"string"}` |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 400 | 查询/路径格式不合法；文档file入口比FS文件入口采用更严格的参数规则。 | application/json [Error](09-数据模型全文.md#error) |
| 404 | 路径语法合法，但文件不在对应目录或文档允许清单。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `200` 的头定义：

```json
{
  "Content-Disposition": {
    "schema": {
      "type": "string",
      "description": "inline; filename=安全basename。"
    }
  },
  "Content-Length": {
    "schema": {
      "type": "integer",
      "description": "非负累计数或即时数量。",
      "minimum": 0
    }
  }
}
```


响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "无同名FreeSWITCH HTTP入口",
  "relationship": "internal_contract_only",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a"
}
```

## getDocsExport · GET /v1/docs/export.zip

下载完整文档ZIP

包含manifest.json和documents列出的全部文件；不写配置，不执行文档。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 成功。 | application/zip `{"type":"string","format":"binary"}` |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 500 | 持久化、内嵌文件或目录处理错误；不可假定请求已生效。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `200` 的头定义：

```json
{
  "Content-Disposition": {
    "schema": {
      "type": "string",
      "description": ""
    },
    "example": "attachment; filename=\"rustswitch-interface-docs.zip\""
  },
  "Content-Length": {
    "schema": {
      "type": "integer",
      "description": "非负累计数或即时数量。",
      "minimum": 0
    }
  }
}
```


响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "无同名FreeSWITCH HTTP入口",
  "relationship": "internal_contract_only",
  "difference": "这是RustSwitch自有HTTP契约，不兼容原版ESL/fs_cli的传输、命令与输出。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a"
}
```

## getTests · GET /v1/tests

读取测试能力与任务列表

只读返回当前控制进程的测试能力、唯一运行任务和最近20个终态摘要。当前任务附最近至多300条成功采样，历史摘要的samples为空数组；所有记录只在内存，重启后消失。档位不是实测通过容量，实际FD、隔离端口和二进制检查在显式创建后执行。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 当前能力和内存任务索引；current可能为null，history最多20个摘要，每个摘要samples为空数组。 | application/json [TestOverview](09-数据模型全文.md#testoverview) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "无同名HTTP测试作业入口；originate/status仅为原版呼叫与观测",
  "relationship": "analogous_not_wire_compatible",
  "difference": "作业由本项目启动本机隔离实例、模拟两端并验证真实RTP，不兼容ESL/bgapi，不认证FreeSWITCH运行能力。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## startTest · POST /v1/tests

启动压力或单路媒体测试

仅创建本机独立实例，不接受任意可执行文件、主机或路径。202只表示受理，后续资源预检仍可能失败。request_id在当前控制进程内去重，最多4096个键；同键同参数返回200，同键异参、有运行任务、服务退出或清理冻结返回409；报告淘汰后原键返回410且不重新执行，键数达上限的新请求返回429。总执行上下文期限T=ceil(concurrency/cps)+duration_seconds+90秒，到期进入终止清理，报告发布时间可晚于T。

**写入边界：** 遵守通用Content-Type/CSRF/Origin要求；幂等性和revision以本操作正文为准。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 请求正文



必需：true.

媒体类型：`application/json`；模型：[TestRequest](09-数据模型全文.md#testrequest)

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 202 | 已原子占用唯一测试槽并异步执行预检；返回完整任务记录，不保证已经启动或能够达到目标并发。 | application/json [TestRun](09-数据模型全文.md#testrun) |
| 200 | 当前进程中同request_id、同参数的既有任务；不会重复执行，报告保留范围内可恢复实际状态。 | application/json [TestRun](09-数据模型全文.md#testrun) |
| 409 | 已有测试运行、服务正在退出、资源清理失败导致冻结，或同request_id对应不同参数。 | application/json [Error](09-数据模型全文.md#error) |
| 410 | 该幂等键对应的报告已从最近20份内存历史淘汰；键仍保留，明确拒绝重新执行。 | application/json [Error](09-数据模型全文.md#error) |
| 422 | 模式、并发、CPS、时长或载荷越界。 | application/json [Error](09-数据模型全文.md#error) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 400 | JSON类型、未知字段、空正文或拼接JSON无效。 | application/json [Error](09-数据模型全文.md#error) |
| 415 | 必须为application/json。 | application/json [Error](09-数据模型全文.md#error) |
| 429 | 本控制进程已保存4096个幂等键，拒绝新增任务；既有键仍按去重规则处理，重启后内存记录清空。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "无同名HTTP测试作业入口；originate/status仅为原版呼叫与观测",
  "relationship": "analogous_not_wire_compatible",
  "difference": "作业由本项目启动本机隔离实例、模拟两端并验证真实RTP，不兼容ESL/bgapi，不认证FreeSWITCH运行能力。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## getTest · GET /v1/tests/{id}

读取指定测试进度

返回保留中的任务阶段、发生器进度、最近至多300条成功采样和清理状态。采样错误不会被填成零值；全程峰值在最终报告的evidence中。历史报告最多20份且只保留在内存，淘汰或重启后返回404。终态字段本身不是生产容量通过证明。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |
| id | path | true | `{"type":"string"}` | 创建或列表实际返回的任务ID，不能作为文件路径。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 保留中的完整任务；samples最多300条，只包含成功读取的真实状态。 | application/json [TestRun](09-数据模型全文.md#testrun) |
| 404 | 未知或已淘汰任务。 | application/json [Error](09-数据模型全文.md#error) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "无同名HTTP测试作业入口；originate/status仅为原版呼叫与观测",
  "relationship": "analogous_not_wire_compatible",
  "difference": "作业由本项目启动本机隔离实例、模拟两端并验证真实RTP，不兼容ESL/bgapi，不认证FreeSWITCH运行能力。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## stopTest · POST /v1/tests/{id}/stop

停止压力测试或挂断测试小电话

只取消指定任务拥有的控制、Rust媒体和发生器实例，不停止主服务。202表示已请求停止，继续读取cleanup确认资源；200表示指定任务已有终态，重复停止不再次执行。最终报告在有界清理完成后发布，提前停止不形成完整通过结论。记录淘汰或进程重启后返回404。

**写入边界：** 遵守通用Content-Type/CSRF/Origin要求；幂等性和revision以本操作正文为准。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |
| id | path | true | `{"type":"string"}` | 创建或列表实际返回的任务ID，不能作为文件路径。 |

### 请求正文



必需：true.

媒体类型：`application/json`；模型：`{"type":"object","additionalProperties":false}`

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 202 | 停止请求已受理，任务进入stopping；资源仍可能处于清理中，需继续读取任务。 | application/json [TestRun](09-数据模型全文.md#testrun) |
| 200 | 指定任务已经形成终态报告，返回原任务，不再次停止或重新执行。 | application/json [TestRun](09-数据模型全文.md#testrun) |
| 404 | 未知或已淘汰任务。 | application/json [Error](09-数据模型全文.md#error) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 400 | JSON类型、未知字段、空正文或拼接JSON无效。 | application/json [Error](09-数据模型全文.md#error) |
| 415 | 必须为application/json。 | application/json [Error](09-数据模型全文.md#error) |
| 422 | 停止正文必须为一个空JSON对象；null或非空对象返回422。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "无同名HTTP测试作业入口；originate/status仅为原版呼叫与观测",
  "relationship": "analogous_not_wire_compatible",
  "difference": "作业由本项目启动本机隔离实例、模拟两端并验证真实RTP，不兼容ESL/bgapi，不认证FreeSWITCH运行能力。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## getTestReport · GET /v1/tests/{id}/report

下载测试结果与失败证据

成功、失败及停止任务在资源清理结束并发布后均可下载，不可变报告只在本进程内存保留最近20份。运行或清理中返回409，已淘汰或重启后返回404。controller_tail和generator_tail各保留最近64KiB原始日志字节；result未实际生成或未通过有界读取时为null。evidence逐字段记录预检、部分二进制摘要、采样峰值、日志和最后收敛状态；缺失/null不表示通过。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |
| id | path | true | `{"type":"string"}` | 创建或列表实际返回的任务ID，不能作为文件路径。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 资源清理流程结束后的完整JSON报告，含实际已有的成功或失败证据；通过仍需核对run、result、日志收敛和cleanup。 | application/json [TestReport](09-数据模型全文.md#testreport) |
| 404 | 未知或已淘汰任务。 | application/json [Error](09-数据模型全文.md#error) |
| 409 | 测试仍在运行或清理中，没有可发布的完整报告。 | application/json [Error](09-数据模型全文.md#error) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求并发已达8个，尚未读取正文或执行业务；Retry-After为1秒，按原逻辑请求重试。状态、停止、排空及保护策略不占重请求槽。64个管理TCP连接全部占用时，新连接仍可能在系统队列等待，轻量入口也不能绕过连接上限。 | application/json [Error](09-数据模型全文.md#error) |

响应 `503` 的头定义：

```json
{
  "Retry-After": {
    "schema": {
      "type": "string",
      "const": "1"
    },
    "description": "建议一秒后重试。"
  }
}
```


### FreeSWITCH 对照

```json
{
  "interface": "无同名HTTP测试作业入口；originate/status仅为原版呼叫与观测",
  "relationship": "analogous_not_wire_compatible",
  "difference": "作业由本项目启动本机隔离实例、模拟两端并验证真实RTP，不兼容ESL/bgapi，不认证FreeSWITCH运行能力。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```

## getCodecs · GET /v1/codecs

读取音频传输与原生后端能力

读取生效配置及当前worker握手；runtime_media每次读取，所有预期分片健康且支持精确能力后，g711模式才live_transcoding=true。实时图为G711 8kHz/20ms双腿桥接或本地A腿PCM及有限播放/按键，分别核验就绪；G722/Opus实时转码、ASR/TTS/VAD和录音尚待接入。native_audio独立缓存SDK自检（至多一次、2秒、各64KiB输出），失败仍返回200、null和native_probe_error；SDK的live_transcoding=false描述其离线入口，不否定独立的实时G711图。503为管理预算不足。 目录1.2.0新增runtime_media.local_capability/local_available/local_ready/local_capable_workers；双腿ready不能代替本地单腿ready，旧版本缺字段按未知处理。

**写入边界：** 只读。

### 路径、查询与头参数

| 名称 | 位置 | 必需 | 类型/模型 | 说明 |
|---|---|---|---|---|
| Host | header | true | `{"type":"string","description":""}` | 客户端通常自动设置。主机部分须为精确localhost或回环IP字面量，自定义域名即便解析到回环也拒绝。 |

### 全部已定义响应

| 状态 | 说明 | 正文类型与模型 |
|---|---|---|
| 200 | 能力目录或可解释的原生探测失败。 | application/json [CodecCatalog](09-数据模型全文.md#codeccatalog) |
| 403 | Host不是localhost/回环字面量，或写请求Origin/CSRF校验失败。 | application/json [Error](09-数据模型全文.md#error) |
| 503 | 管理重请求预算耗尽；稍后重试。 | application/json [Error](09-数据模型全文.md#error) |

### FreeSWITCH 对照

```json
{
  "interface": "show codecs / 编解码模块接口",
  "relationship": "analogous_not_wire_compatible",
  "difference": "HTTP目录为本项目能力探测，不兼容ESL正文、不代表mod_*.so可直接装载，亦不认证实时转码。",
  "status": "implemented",
  "reference_url": "https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_commands/mod_commands.c"
}
```
