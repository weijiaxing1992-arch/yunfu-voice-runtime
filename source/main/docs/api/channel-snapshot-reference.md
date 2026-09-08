# 通道快照 uuid_dump

文档版本1.8.0。`api` 和 `bgapi` 均可分派此入口，要求先完成入站 ESL 认证。

```text
api uuid_dump <uuid> [txt|plain|json|xml]
```

参考固定 FreeSWITCH 1.11.3 的 `mod_commands.c` 中 `uuid_dump_function`，注册条目 `registration:api:mod_commands:uuid_dump:7744`。当前是实际通道快照的限定兼容入口，不是原版完整字段认证。

| 参数／结果 | 当前行为与原版对照 |
|---|---|
| uuid | 当前真实通道的一条腿；双腿UUID、SIP Call-ID和变量空间分别独立 |
| 不传格式／txt | `Name: value`逐行正文及末尾空行；值使用原版百分号规则，空格为%20、已有大写%HH保留 |
| plain | `Name: [原值]`；不要把原始换行当成新的ESL外层帧，外层仍按Content-Length读取 |
| json／JSON | 字符串键值对象，不带额外事件正文；支持大小写不敏感的格式名 |
| 无参数 | `-USAGE: <uuid> [format]`及换行 |
| UUID不存在／已挂机 | `-ERR No such channel!`及换行 |
| xml／XML | event/headers结构，头值百分号编码；与入站事件XML序列化一致 |
| 未知格式 | 按原版回落到txt编码文本 |
| 多余参数 | 明确拒绝；原版完整参数解析仍需逐分支核对 |

当前字段为`Event-Name=CHANNEL_DATA`、查询时刻`Event-Date-Timestamp`（微秒）、`Unique-ID`、`Call-Direction`、`Channel-State`、`Answer-State`、`Channel-Name`、`variable_uuid`、`variable_sip_call_id`，以及本腿已实际存储的`variable_*`。两腿通话包含`Other-Leg-Unique-ID`，只有A腿的本地IVR不虚构另一条腿。

`Channel-Name`保留`rustswitch/方向/SIP Call-ID`的真实实现身份。`Channel-State`只表达当前实现的`CS_INIT`／`CS_EXECUTE`；不模拟FreeSWITCH全部状态机、caller profile、Core-UUID、codec字段、计费字段或变量数组。返回快照不会发布`CHANNEL_DATA`业务事件，不能把这个查询入口当作已实现相应事件源。

快照在通话主循环中按UUID定位，只遍历该腿的变量，不扫描全部通话或额外创建线程。调用参数最多128字节、输出最多160个字段、原始字段总长度最多80KiB；用户变量沿用128项／64KiB总额度。编码扩展仍有界，取消且尚未执行的查询不再消耗编码预算。合法中文变量名可用于四种格式；XML仍拒绝未声明命名空间前缀及非法元素名。预算或文本头名称不合法时明确失败，不截断成看似成功的记录。

验证入口：`TestChannelSnapshot*`、`TestChannelDataSerializationBounds`和`tests/esl_channel_snapshot_e2e.py`。最终是否通过，以[运行验证证据](runtime-verification.md)及[本轮实测记录](release-validation-v1.8.md)为准，不以本页存在作为验收证据。
