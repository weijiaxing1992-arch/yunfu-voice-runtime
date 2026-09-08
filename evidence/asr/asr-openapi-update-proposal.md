# ASR 资源与配置的 OpenAPI 更新建议

这是可供正式发布时应用的修改说明，尚未改变 `project/control/internal/server/doc_assets/api/openapi.json` 或后台嵌入文档，不新增公开 ASR 操作、不修改版本号、操作数量或兼容绿色数量。现有文件为 OpenAPI 3.1.0，Config 与 Status 都声明 additionalProperties=false，当前源代码已输出的 asr_stream 必须在下一次文档发布中显式加入，不能只写一篇说明文档而让模型继续拒绝真实响应。

## 1. 新增两个模型

以下对象的两个键加入 `components.schemas`。ASRStreamConfig 对应 Go `config.ASRStream`；ASRStreamStatus 对应 Go `server.ASRStatus`。配置字段采用原始值，不把省略/0改写成有效默认值。

```json
{
  "ASRStreamConfig": {
    "type": "object",
    "additionalProperties": false,
    "required": ["socket_path"],
    "description": "固定本机ASR代理部署配置，Go类型config.ASRStream。省略关闭；有效对象必须提供socket_path。PUT完整配置时必须保留部署原值，不可启用、禁用或修改路径/采样率/预算。代理地址不是每通话参数，Runtime不创建或删除代理socket。",
    "properties": {
      "socket_path": {
        "type": "string",
        "minLength": 2,
        "maxLength": 100,
        "pattern": "^/",
        "not": {"pattern": "[\u0000\r\n]"},
        "description": "规范绝对Unix socket路径，非根目录；按UTF-8最多100字节，无NUL/CR/LF，不能经Clean折叠，也不能与pcm_stream.socket_path相同。maxLength只是字符数上限；字节数、规范化和跨字段冲突由运行时另验。实际连接核验当前用户的0700父目录、socket身份和同UID对端。"
      },
      "sample_rate": {
        "type": "integer",
        "enum": [0, 8000, 16000],
        "description": "目标单声道S16LE采样率；省略或0使用8000，16000按需转换。20ms帧及8000Hz来源RTP时钟固定。默认解析不改写配置，0值字段在响应中省略。"
      },
      "max_streams": {
        "type": "integer",
        "minimum": 0,
        "description": "含起建、工作和未知清理的全局UUID槽预算。省略或0使用min(64,limits.max_calls)，有效值必须1..limits.max_calls。跨字段上限由运行时校验；默认解析不改写配置，0值字段在响应中省略。"
      }
    },
    "x-deployment-frozen": true,
    "x-go-type": "config.ASRStream"
  },
  "ASRStreamStatus": {
    "type": "object",
    "additionalProperties": false,
    "required": ["enabled", "stopping", "max_streams", "slots", "starting", "active", "cleanup_pending", "rejected_capacity", "rejected_uuid"],
    "description": "Go server.ASRStatus的低基数资源快照；Slots=Starting+Active+CleanupPending。资源归还可早于业务取完final，不能视为识别成功数、供应商健康或逐通话结果。未启用时仍返回对象，bool为false、计数和上限为0。",
    "properties": {
      "enabled": {"type": "boolean", "description": "本进程是否启用固定ASR代理配置，不证明代理已连接或模型可用。"},
      "stopping": {"type": "boolean", "description": "管理器正在关闭或服务上下文已取消，不再接受新起建。"},
      "max_streams": {"type": "integer", "minimum": 0, "description": "实际有效槽预算；未启用为0。"},
      "slots": {"type": "integer", "minimum": 0, "description": "仍保留的UUID槽数，包括起建和未知清理。"},
      "starting": {"type": "integer", "minimum": 0, "description": "启动器尚未返回的已预留槽，已占额度。"},
      "active": {"type": "integer", "minimum": 0, "description": "不属于起建或待清理的资源槽；包括RX已停但仍等待代理final的连接，不是识别成功数。"},
      "cleanup_pending": {"type": "integer", "minimum": 0, "description": "未知启动结果、服务关闭、RX未确认清理或Closed待移除的槽；正常Finish退订过程也可能计入。"},
      "rejected_capacity": {"type": "integer", "minimum": 0, "maximum": 18446744073709551615, "description": "当前进程因额度不足拒绝启动的累计次数。JSON实际为整数，超出JavaScript安全整数后消费方须使用保真解析器。"},
      "rejected_uuid": {"type": "integer", "minimum": 0, "maximum": 18446744073709551615, "description": "当前进程因同UUID已有起建/活动/清理槽拒绝启动的累计次数。JSON实际为整数，超出JavaScript安全整数后消费方须使用保真解析器。"}
    },
    "x-go-type": "server.ASRStatus"
  }
}
```

`x-deployment-frozen` 和 `x-go-type` 是建议的文档扩展，不改变服务器行为。不要把 Config.asr_stream 标成 `readOnly: true`：管理 PUT 接收完整 Config，必须回传原部署对象；生成客户端若自动删掉 readOnly 字段，可能把原对象变成省略并触发 422。冻结语义应写在说明和扩展中。

Go 对指针字段的 JSON null 解码等价于 nil，GET 会将 nil 省略。下面的 Config.asr_stream 属性用 object 引用与 null 的 oneOf 描述这一已存在的解码行为；推荐关闭配置时省略该字段。null 不使管理PUT获得禁用已有ASR配置的权限，冻结校验仍然执行。这不改变其他可选字段的现有模型或服务器行为。

## 2. 扩展现有模型

向 `components.schemas.Config.properties` 加入以下属性，Config.required 保持不变：

```json
{
  "asr_stream": {
    "oneOf": [{"$ref": "#/components/schemas/ASRStreamConfig"}, {"type": "null"}],
    "description": "可选固定ASR代理部署配置；输入null与省略均解码为关闭的nil，未启用时响应省略。管理PUT须保持原对象/启用状态不变。不是公开ASR送音或识别结果接口。"
  }
}
```

建议将 Config.description 中“响应包含全部字段”更正为“响应包含必需基础字段，可选部署对象按启用状态省略；PUT应从GET返回的完整Config构造并保留部署冻结字段”。active、desired 都已引用 Config，因而 AdminConfigResponse 不需另加重复字段。

向 `components.schemas.Status.properties` 加入以下属性，并在 Status.required 加入 `asr_stream`：

```json
{
  "asr_stream": {
    "$ref": "#/components/schemas/ASRStreamStatus",
    "description": "当前主服务ASR资源快照。新实现始终输出；禁用仍返回零值对象。旧构建若完全缺字段应视为未知，不能当零或识别成功。"
  }
}
```

当前候选响应始终含对象，因此描述当前候选的模型可将它列为 required。若文档要同时支持旧服务版本，应另列版本/能力边界，而不是用空对象通过当前模型。

不要把 `asr.Event` 或单流 `asr.Snapshot` 直接挂到任意 HTTP path；当前它们只有内部 Go SDK，且内部 JSON 的 uint64 是数字，ASR1 元数据是十进制字符串，两种契约不能混用。

## 3. 更新现有操作说明

| 位置 | 建议说明 |
| --- | --- |
| `paths./v1/status.get.description` | 返回控制、保护器、媒体分片和主服务ASR资源快照。ASR的起建、工作和清理计数不表示识别成功，也不包含逐通话结果或隔离测试实例负载。 |
| `paths./v1/config.get.description` | 读取revision、当前CSRF令牌、active/desired、Guard和持久化状态。可选ASR配置为部署原始值，省略表示本部署未启用；有效默认值见ASRStreamConfig。 |
| `paths./v1/config.put.description` | 保存完整草稿前校验配置与revision；程序、日志、放音根目录、XML拨号计划、PCM入口和ASR代理对象由部署文件管理，必须保持原值。其他设置保存不立即切换当前socket或worker。 |
| `paths./v1/config.put.responses.422` | 保留已有Validation响应，说明包含ASR格式/预算/路径冲突、冻结部署对象被修改等校验失败。无需新增HTTP错误码。 |
| `paths./metrics.get.description` | 维持text/plain; version=0.0.4及没有HELP/TYPE的现状，补充九个ASR低基数指标及gauge/counter含义，注明无UUID/token/文本标签。 |

现有 x-freeswitch 保留 `analogous_not_wire_compatible` 等真实关系。ASR1 与内部 Go方法不能据此标成 `detect_speech`、`play_and_detect_speech`、MRCP、ESL识别事件或 C ABI 兼容。未实现的原版条目不因新增状态字段变绿。

## 4. Prometheus 字典追加

| 指标 | 类型 | 说明 |
| --- | --- | --- |
| rustswitch_asr_enabled | gauge | 配置已启用=1，禁用=0。 |
| rustswitch_asr_stopping | gauge | 正在停止=1。 |
| rustswitch_asr_max_streams | gauge | 实际固定额度，禁用=0。 |
| rustswitch_asr_slots | gauge | 仍占用的UUID槽数。 |
| rustswitch_asr_starting | gauge | 起建预留槽数。 |
| rustswitch_asr_active | gauge | 工作资源槽数，可能等final。 |
| rustswitch_asr_cleanup_pending | gauge | 待清理/待确认/待移除槽数。 |
| rustswitch_asr_rejected_capacity_total | counter | 本进程额度拒绝累计数。 |
| rustswitch_asr_rejected_uuid_total | counter | 本进程重复UUID拒绝累计数。 |

不要将媒体实际消费样本、ASR代理确认样本、业务结果交付数映射到同一个“识别成功”指标。当前九项只描述资源，不新增虚构成功率。

## 5. 发布文档前的验收步骤

1. 将本目录 asr-stream-reference.md 按既有文档清单机制嵌入，并保留内部接口与模型mock的范围说明。
2. 应用以上模型/字段和操作说明，运行项目现有文档模型校验，确认新增引用均可解析；没有新增path，操作数量应不变。
3. 用同一候选构建的禁用、起建、活动、未知清理、关闭状态响应验证 Status；三类槽之和等于slots、私有身份未进入状态/指标。
4. 用GET原始Config回传其他合法修改，确认ASR原对象保留；修改ASR对象应422，保留0值/省略状态应不被客户端默认合并改写。
5. 在最终部署版本上验证后台能实际打开文档和识别资源面板。本文及已有HTTP handler测试不能替代浏览器或生产部署验收。

这是一份修改建议；本轮没有应用到嵌入OpenAPI或发布后台。基础依据与源码绑定记录见 `asr-documentation-evidence.json`。
