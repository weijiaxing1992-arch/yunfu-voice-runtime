# 基础应用、WAV播放与XML拨号计划的实际对照

本轮检查 12 组有限场景，其中通过 12 组。只验证表中实际断言，不代表 FreeSWITCH 全模块、全部协议或万路容量认证。

| 场景 | 原版 | 候选 | 对照结论 |
| --- | --- | --- | --- |
| 接通后应答、sleep与串行set、正常挂断 | passed_scoped | passed_scoped | passed_scoped |
| WAV自然结束及真实PCMU波形 | passed_scoped | passed_scoped | passed_scoped |
| uuid_break停止实际WAV播放 | passed_scoped | passed_scoped | passed_scoped |
| RTP #按键打断WAV播放 | passed_scoped | passed_scoped | passed_scoped |
| WAV文件不存在的完成响应 | passed_scoped | passed_scoped | passed_scoped |
| XML命中后顺序执行answer/set/sleep/set/park | passed_scoped | passed_scoped | passed_scoped |
| XML正常挂断和实际BYE | passed_scoped | passed_scoped | passed_scoped |
| XML park保持到uuid_kill真实挂断 | passed_scoped | passed_scoped | passed_scoped |
| 权威XML未命中返回404 | passed_scoped | passed_scoped | passed_scoped |
| uuid_setvar_multi的12个参数边界 | observed | observed | passed_scoped |
| uuid_dump的指定字段、编码格式与通道生命周期 | observed | observed | passed_scoped |
| set/unset/read真实RTP 12#回归 | passed_scoped | passed_scoped | passed_scoped |

## 已明确的差异

本次观察未发现表内场景的额外播放事件差异；未测试字段仍不视为通过。

## 范围与可追溯性

- 受限WAV只验证8kHz单声道PCM16输入到PCMU，不代表所有音频格式、采样率、URL或文件协议。
- XML只验证共享文件中的单destination_number条件和固定顺序动作；完整正则/嵌套/anti-action/动态重载等不在本次范围。
- 原版WAV使用同一共享文件的绝对路径；候选使用受限根下相对路径，两者路径合同有意不同。
- 本次单路原版对照与26项ESL/SIP探针是独立集合；均不能证明5000或10000并发通过。
- 默认*/none及read提示中uuid_break已采原版边界，候选本地回归由独立E2E覆盖；本文件只将显式#与普通playback停止列为成对验证。
- uuid_setvar_multi只覆盖表中12向量；64项上限、更广的分隔符与所有错误组合未获完整合同认证。
- 四生命周期事件只在表中常规WAV与XML park路径成对验证；文件仍在loading、尚无running确认就挂机的短窗口，以及更多失联/故障路径不授予完整事件合同通过。
- uuid_dump只比较指定字段及格式。原版自动变量、全部CHANNEL_DATA字段、两腿快照和更多参数组合不因本轮单腿通过而视为齐全。

本次活动通道的 uuid_dump：原版 JSON 有 138 个字段，候选有 12 个字段；126 个原版字段未出现在候选快照中。因此本次只授予指定字段和格式的子集通过，完整 uuid_dump 字段兼容仍未完成。

对应 JSON 保存逐场景实际观察、原始证据摘要、运行时间、双方配置/二进制/源码、共享WAV/XML及脚本哈希，并在运行前后检查文件不变。公开文档不包含认证正文、私钥或原版私有XML。
