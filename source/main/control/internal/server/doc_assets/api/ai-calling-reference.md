# AI 呼叫接入与本轮兼容范围

文档版本 1.12.0；参考 FreeSWITCH 1.11.3 固定提交。实现、实际运行结果和原版对照结果分别记录；本文件不声明完整 FreeSWITCH 替换已完成。

## 业务链路

| 环节 | 可调用能力 | 当前边界 |
| --- | --- | --- |
| 注册到运营商或 SIP 服务 | 固定上游 REGISTER、Digest、刷新、注销和状态观测 | 单上游客户端；不是允许分机注册进来的 registrar |
| 线路呼叫 | UDP/TCP/TLS 双腿 SIP、认证挑战后重试、ACK/CANCEL/BYE | 需要可信 SIP 发起方；ESL originate、完整路由和转接未实现 |
| 本地呼入 | `sip.local_extensions` 精确号码自动接听，只创建 A 腿 | 只在媒体分配成功后应答；亦可部署受限 sip.dialplan XML，见[XML合同](dialplan-reference.md) |
| 实时媒体 | 同格式双向 RTP、DTMF/RTCP；G.711 双腿/本地图，已 ACK 外部 8k PCM 轮次下行 | ASR/LLM/TTS 供应商/VAD/录音闭环和其他 Codec 实时处理未完成，见 [1.12 验收](release-validation-v1.12.md) |
| 按键采集 | RFC4733 完成事件、DTMF 事件、显式 INFO 模式、`read` | 丢结束包/事件溢出使收号失败；没有音内检测和全部长事件分段；已 ACK 本地 telephone-event 主动发送见 [发送合同](dtmf-send-reference.md) |
| IVR 应用 | `sendmsg execute` 下的 `answer`、`set`、`unset`、`read`、`playback`、`sleep`、`park`、`hangup` 的限定子集 | 调用者根据收号变量决定下一步；未执行原版菜单 XML、Lua、全部APP |

固定上游注册配置及认证算法、私有密码文件、续期和退出行为见 [线路注册说明](trunk-registration.md)。通道命令全部参数、事件和错误见 [IVR 应用说明](ivr-reference.md)。媒体层字段及预算见 [媒体交互接口](media-interaction.md)。

## 配置本地接听号码

以下是完整启动配置中的 `sip` 字段补充，不能把片段直接当作完整 `PUT /v1/config` 请求：

```json
{
  "local_extensions": ["1000", "1001"]
}
```

管理后台「SIP 与媒体」提供本地接听号码表单；保存后等待重启生效。号码只能包含数字和 `+`，每个 1–32 字符，最多 256 个，不允许重复、正则或通配符。未配置sip.dialplan时保留原固定上游桥接行为；启用XML后未命中返回404。

呼入仍必须通过信令和媒体来源白名单。命中本地号码后，服务实际分配 Rust 媒体资源，返回带 A 腿地址的 200/SDP；收到 ACK 后才允许收号和播放。此时没有 B 腿 UUID、B 腿呼叫或伪造的上游连接。RVA1 BEGIN 也须真实 ACK 后授权；仅看到 200 或 CHANNEL_ANSWER 不能代替该资格检查。没有 ACK、分配失败、最长时长到期及挂机仍沿用原有资源回收路径。当前资源分配仍预留四个端口，不把只使用 A 腿解释为容量翻倍。

此路由本身自动应答；已支持受限 `answer` 应用，但未实现完整 FreeSWITCH 会话状态机。本地媒体可协商的格式与提示音可编码的格式分别处理：收号依赖协商的 telephone-event 或显式 INFO 模式；提示音只支持 PCMU/PCMA。

## 控制一个已接通的本地通道

先按 [ESL 连接说明](esl-reference.md) 启用并认证本地 ESL，再订阅真实通道和应用事件：

```text
event json CHANNEL_CREATE CHANNEL_ANSWER CHANNEL_HANGUP CHANNEL_EXECUTE CHANNEL_EXECUTE_COMPLETE DTMF
```

从当前呼入的 `CHANNEL_CREATE` 取得其真实 `Unique-ID`，核对 SIP Call-ID，等待通话实际接通；不能使用媒体 session 数字冒充 UUID。以下 `<uuid>` 需要替换为该通道标识，每帧以空行结束：

```text
sendmsg <uuid>
call-command: execute
execute-app-name: read
execute-app-arg: 1 4 tone_stream://%(400,0,440) menu_choice 3000 #
event-lock: true

```

`+OK` 只表示受理。通过应用关联标识核对 `CHANNEL_EXECUTE_COMPLETE` 后，读取 `menu_choice`、`read_result`、`read_terminator_used`；成功收号、超时、媒体输入不完整和挂机是不同结果。完整示例及原版参数限制见 [IVR 说明](ivr-reference.md)。

外部业务可按 `menu_choice` 的实际值选择下一次提示或收号，形成多步菜单。未实现的应用明确返回错误，不能把 `play_and_get_digits`、`ivr`、`bridge`、`record_session` 或 TTS 命令当作本版本可执行入口。

## INFO 模式

通过 `uuid_setvar <uuid> dtmf_type info` 或受限 `set` 应用为指定通道显式启用 INFO。此时 read 不要求 SDP telephone-event，RTP 电话事件不会同时重复送入该通道收号。

```text
Content-Type: application/dtmf-relay

Signal=5
Duration=100
```

只接受已建立对话、正确来源地址/连接、标签和递增 CSeq。相同事务重传只返回原响应，不重复收号。`Signal` 支持 0–9、`*`、`#`、A–D，以及 10–15 数字事件编号；`Duration` 必须显式提供 20–8191 毫秒。未启用返回 488，错误类型 415，错误正文 400，错误对话 481，未实现扩展 501。原版对缺省时长、无效文本及其他 INFO 类型的宽松行为不在此子集内。

SIP 注册与事务依据 [RFC3261](https://www.rfc-editor.org/rfc/rfc3261.html)，Digest 算法扩展参考 [RFC8760](https://www.rfc-editor.org/rfc/rfc8760.html)，RTP 电话事件参考 [RFC4733](https://www.rfc-editor.org/rfc/rfc4733.html)。精确原版行为继续以固定源码和实际对照报告为准。

## 压力页面读取恢复

页面不再保留已被淘汰或服务重启后不存在的任务选择。记录过期只清除该任务并重新读取当前任务；单条详情临时失败不会误报整个测试服务离线。网络错误保留上次数据，恢复后继续更新。服务明确以 `X-RustSwitch-CSRF-Refresh: required` 拒绝过期令牌时，页面只更新令牌并用同一请求重试一次，不覆盖配置草稿或幂等键；不确定的网络超时不会自动重放写入。

后台历史仍是当前控制进程最近 20 条内存记录，跨重启需要事先下载报告。读取恢复通过不等于容量验收通过。容量必须同时核对实际建立、逐方向足量媒体、错误、缺包和清理；历史 5000 路成绩不能自动继承给新源码。

## 验证入口和未完成范围

- `tests/esl_applications_e2e.py`：真实 SIP、Rust 媒体及 ESL 应用，在两种 UDP 模式中检查变量、收号、提示音、打断和清理。
- `tests/media_interaction_e2e.py`：核对实际 RTP 波形、两个方向、来源拒绝、重复结束包、缺结束与资源释放。
- `control/internal/server/trunk_wire_test.go`：真实 UDP/TCP/TLS 注册、认证呼叫、双向媒体和注销。
- `tests/esl_applications_native.py`：对隔离原版与候选分别发起实际通话，再比较限定应用结果，所有失败证据保留。
- `tools/test_testlab_ui.cjs`、`tools/test_admin_request_ui.cjs`：读取/重启恢复与明确拒绝后的有界重试。

最新实际执行结果由 [运行验证](runtime-verification.md) 和原版对照报告展示。全部模块宿主、终端注册服务器、多网关、完整 XML/拨号计划、originate/转接/重协商、完整文件后端/录音/会议、WebRTC/SRTP/ICE、实时 ASR/TTS 与全部原版应用仍需继续实现和验证。

## 外部实时供音的已完成部分

当前可由本机同 UID 可信进程使用 [RVA1](rva1-reference.md)：控制连接 BEGIN 取得已 ACK 本地通道的 token，独立数据连接提交原生 8k mono S16LE 完整帧，END 排空或 INTERRUPT(0/20/40ms) 使旧轮音频失效。固定队列、严格样本偏移和未知结果恢复见 [PCM turn](pcm-turn-reference.md)。无需每 20ms 使用 HTTP JSON，也不创建每通话数据线程。

静默 read 与 park 可以并行，真实主动 DTMF 共享 RTP 身份。带音频提示的 read/tone/WAV 与 PCM 相互排斥，业务应明确交接 TX；`uuid_break` 不自动停止 PCM。这个接口提供流式媒体下行基础，不代表已接通 TTS/ASR 供应商、LLM、VAD 自动打断或双轨录音。

本轮真实外部 22 方法/12 子例、Go 899 个通过事件与既有 228 项协议回归按各自范围留证；不是所有 FreeSWITCH 原版模块认证。500 路处理失败与历史 5000 路失败继续保留，详见 [候选验证](release-validation-v1.12.md)。
