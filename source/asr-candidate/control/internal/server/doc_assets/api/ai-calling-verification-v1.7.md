# 基础应用、WAV播放与XML拨号计划的实际对照

本轮检查 11 组有限场景，其中通过 11 组。只验证表中实际断言，不代表 FreeSWITCH 全模块、全部协议或万路容量认证。

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
| set/unset/read真实RTP 12#回归 | passed_scoped | passed_scoped | passed_scoped |

## 已明确的差异

- playback-eos：PLAYBACK_START/PLAYBACK_STOP 未完全一致；此场景只可标应用/媒体子集通过，不能标完整事件合同通过。
- playback-stop：PLAYBACK_START/PLAYBACK_STOP 未完全一致；此场景只可标应用/媒体子集通过，不能标完整事件合同通过。
- playback-dtmf：PLAYBACK_START/PLAYBACK_STOP 未完全一致；此场景只可标应用/媒体子集通过，不能标完整事件合同通过。
- xml-sequence：CHANNEL_PARK/CHANNEL_UNPARK 未完全一致；此场景只可标指定应用结果通过，不能标完整停驻事件合同通过。
- xml-park：CHANNEL_PARK/CHANNEL_UNPARK 未完全一致；此场景只可标指定应用结果通过，不能标完整停驻事件合同通过。

## 范围与可追溯性

- 受限WAV只验证8kHz单声道PCM16输入到PCMU，不代表所有音频格式、采样率、URL或文件协议。
- XML只验证共享文件中的单destination_number条件和固定顺序动作；完整正则/嵌套/anti-action/动态重载等不在本次范围。
- 原版WAV使用同一共享文件的绝对路径；候选使用受限根下相对路径，两者路径合同有意不同。
- 本次单路原版对照与26项ESL/SIP探针是独立集合；均不能证明5000或10000并发通过。
- 默认*/none及read提示中uuid_break已采原版边界，候选本地回归由独立E2E覆盖；本文件只将显式#与普通playback停止列为成对验证。
- uuid_setvar_multi只覆盖表中12向量；64项上限、更广的分隔符与所有错误组合未获完整合同认证。

对应 JSON 保存逐场景实际观察、原始证据摘要、运行时间、双方配置/二进制/源码、共享WAV/XML及脚本哈希，并在运行前后检查文件不变。公开文档不包含认证正文、私钥或原版私有XML。
