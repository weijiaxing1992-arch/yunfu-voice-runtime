# 本地运行验证与绿色状态

绿色表示所列本地断言或容量子场景真实通过，并且日志与执行前后源码指纹仍有效。原版FreeSWITCH成对结果由独立验收报告说明，不从本地测试推导。

本次覆盖 3999 个原始对照ID，当前可标绿 82 项。未映射、失败、源码变化、缺失或过期证据均不标绿。

## 运行与复查

```sh
python3 tools/build_runtime_verification.py --record-go --go /absolute/path/to/go
python3 tools/build_runtime_verification.py --check
python3 -m unittest discover -s tools -p test_runtime_verification.py
```

`--record-go` 执行新的 `go test -race -count=1 -json -timeout=3m ./...`，记录执行前后源码、日志和诊断输出SHA。它不会给旧日志补造当时版本。更新源码后重新生成将显示过期；必须重新运行才能恢复绿色。默认证据目录为工作区 `work/audio-task/runtime-verification`，发布时应归档其中收据与日志。

只绑定Go功能源码、测试、配置及XML模板；前端与生成文档内容不属于这些测试的通过范围，避免证据文档发布导致自身指纹递归变化。30天复核期限是本项目明确采用的证据新鲜度门禁，不是协议标准。

发布器调用`validate_runtime(root)`：日志/报告以有界压缩内容随JSON发布，可在没有工作目录的源码包中重验日志SHA、run/pass及包完成、源码集合和过期门禁。二进制在采集时实际读文件核对SHA，源码包离线复查保留该指纹；核对二进制本体须使用完整运行证据包。运行收据不是第三方签名或独立认证。

## 当前本地范围

| 条目 | 本地状态 | 本次限定范围 |
|---|---|---|
| `registration:api:mod_commands:uuid_break:7729` | 本地验证通过（限定范围） | 两种UDP模式下活动playback真实停音且保留通话；read仅停提示音仍等待期限。all/both/无活动播放或任意应用break尚未兼容 |
| `registration:api:mod_commands:uuid_dump:7744` | 本地验证通过（限定范围） | 两种UDP模式下真实双腿快照、txt/plain/JSON/XML与未知格式回退、中文变量隔离、结束后查无及再次呼叫媒体；不含原版完整字段和状态机 |
| `registration:api:mod_commands:uuid_exists:7745` | 本地验证通过（限定范围） | 两种UDP模式下活动/已删除通道、中文变量读写删除、双腿隔离、非法输入与媒体保持；不包含全部原版变量求值与副作用 |
| `registration:api:mod_commands:uuid_getvar:7748` | 本地验证通过（限定范围） | 两种UDP模式下活动/已删除通道、中文变量读写删除、双腿隔离、非法输入与媒体保持；不包含全部原版变量求值与副作用 |
| `registration:api:mod_commands:uuid_kill:7750` | 本地验证通过（限定范围） | 两种UDP模式下NORMAL_CLEARING实际双腿BYE、资源回收及再次呼叫；非法原因拒绝且通话保留，不代表全部挂机cause |
| `registration:api:mod_commands:uuid_send_dtmf:7774` | 本地验证通过（限定范围） | 两种UDP模式下本地已ACK A腿真实发送、非法输入/INFO模式拒绝、队列满/挂机回收及桥接双腿拒绝仍保留媒体；不是全部原版发送语法或远端终端认证 |
| `registration:api:mod_commands:uuid_setvar:7777` | 本地验证通过（限定范围） | 两种UDP模式下活动/已删除通道、中文变量读写删除、双腿隔离、非法输入与媒体保持；不包含全部原版变量求值与副作用 |
| `registration:api:mod_commands:uuid_setvar_multi:7776` | 本地验证通过（限定范围） | 两种UDP模式下批量变量实际读回、空值删除/分号转义、部分失败、预算、双腿媒体继续与再次呼叫；完整原版变量容器仍未实现 |
| `registration:app:mod_dptools:answer:6674` | 本地验证通过（限定范围） | 两种UDP模式下实际接听等待ACK、顺序sleep、park保持和收号后真实BYE；park私有事件、全部cause和参数未完全对照 |
| `registration:app:mod_dptools:hangup:6677` | 本地验证通过（限定范围） | 两种UDP模式下实际接听等待ACK、顺序sleep、park保持和收号后真实BYE；park私有事件、全部cause和参数未完全对照 |
| `registration:app:mod_dptools:park:6787` | 本地验证通过（限定范围） | 两种UDP模式下实际接听等待ACK、顺序sleep、park保持和收号后真实BYE；park私有事件、全部cause和参数未完全对照 |
| `registration:app:mod_dptools:playback:6790` | 本地验证通过（限定范围） | 两种UDP模式下受限WAV和单音真实播放/失败及挂机；不含原版所有文件后端、seek、循环或TTS |
| `registration:app:mod_dptools:read:6797` | 本地验证通过（限定范围） | 两种UDP模式下真实RTP/INFO收号、结束去重、提示音打断、缺结束失败和超时；不含完整原版缓冲/文件/参数语义 |
| `registration:app:mod_dptools:set:6690` | 本地验证通过（限定范围） | 两种UDP模式下普通变量set的真实副作用和事件；不含数组或变量展开 |
| `registration:app:mod_dptools:sleep:6664` | 本地验证通过（限定范围） | 两种UDP模式下实际接听等待ACK、顺序sleep、park保持和收号后真实BYE；park私有事件、全部cause和参数未完全对照 |
| `registration:app:mod_dptools:unset:6706` | 本地验证通过（限定范围） | 两种UDP模式下普通变量unset删除的实际效果；不含全部原版变量容器 |
| `event:CHANNEL_ANSWER` | 本地验证通过（限定范围） | 两种UDP模式下逐腿真实创建/接听/挂机、唯一身份、SIP来源、互相关联与时序；限定基本字段，不包含完整原版生命周期 |
| `event:CHANNEL_CREATE` | 本地验证通过（限定范围） | 两种UDP模式下逐腿真实创建/接听/挂机、唯一身份、SIP来源、互相关联与时序；限定基本字段，不包含完整原版生命周期 |
| `event:CHANNEL_EXECUTE` | 本地验证通过（限定范围） | 两种UDP模式下受限应用开始事件与实际任务关联；不含全部FreeSWITCH应用路径 |
| `event:CHANNEL_EXECUTE_COMPLETE` | 本地验证通过（限定范围） | 两种UDP模式下受限应用完成、变量结果及挂断取消；不是完整原版事件字段认证 |
| `event:CHANNEL_HANGUP` | 本地验证通过（限定范围） | 两种UDP模式下逐腿真实创建/接听/挂机、唯一身份、SIP来源、互相关联与时序；限定基本字段，不包含完整原版生命周期 |
| `event:CHANNEL_PARK` | 本地验证通过（限定范围） | 两种UDP模式下真实park进入、通话保持及挂机退出顺序；仅受限park路径，不含原版transfer/unpark全部路径 |
| `event:CHANNEL_UNPARK` | 本地验证通过（限定范围） | 两种UDP模式下真实park进入、通话保持及挂机退出顺序；仅受限park路径，不含原版transfer/unpark全部路径 |
| `event:DTMF` | 本地验证通过（限定范围） | 两种UDP模式下真实RTP/INFO按键的通道事件、去重及收号；完整原版字段未全部对照 |
| `event:PLAYBACK_START` | 本地验证通过（限定范围） | 两种UDP模式真实WAV/单音开始、自然结束/按键/API停音、挂机/worker故障回收与逐腿事件顺序；不含原版全部字段及loading即挂机的未观测窗口 |
| `event:PLAYBACK_STOP` | 本地验证通过（限定范围） | 两种UDP模式真实WAV/单音开始、自然结束/按键/API停音、挂机/worker故障回收与逐腿事件顺序；不含原版全部字段及loading即挂机的未观测窗口 |
| `key-api-pcm-stream` | 本地验证通过（限定范围） | 本地私有Unix控制/数据连接跨连接令牌、ACK/应用互斥/挂断和代次撤销；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。 |
| `key-api-rx-sdk` | 本地验证通过（限定范围） | 内部Go收音句柄、SIP授权、交付期限及挂断清理；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。 |
| `key-cli-callbench` | 本地验证通过（限定范围） | 发生器共享端点归属与RTP时钟/长度校验单元合同；不表示容量通过 |
| `key-config-dialplan-runtime` | 本地验证通过（限定范围） | 两种UDP模式下XML顺序、404/重传、无ACK回收和ESL关闭仍真实收号挂机；不是完整XML求值认证 |
| `key-config-export` | 本地验证通过（限定范围） | 本地XML产物编辑/ZIP导出；不代表XML业务语义兼容 |
| `key-config-parameter-edit` | 本地验证通过（限定范围） | XML参数编辑/导出及命名空间元数据保留；不执行原版配置 |
| `key-config-persistence` | 本地验证通过（限定范围） | 启动文件变化时保留并收紧已保存的保护策略 |
| `key-config-raw-xml` | 本地验证通过（限定范围） | XML草稿保存重载与重复属性拒绝；不执行原版业务 |
| `key-esl-api` | 本地验证通过（限定范围） | 两种UDP媒体模式下真实SIP/Rust双向RTP、ESL变量隔离、正常原因uuid_kill双腿拆线和再次呼叫 |
| `key-esl-bgapi` | 本地验证通过（限定范围） | 后台作业关联、真实正文、固定并行度及取消；完整Job字段和崩溃恢复不在范围内 |
| `key-esl-dtmf-send-status` | 本地验证通过（限定范围） | 两种UDP模式下自有查询读回真实发送包数/完成/队列与通道归属；无同名FreeSWITCH状态API，不作为新增原版兼容命令 |
| `key-esl-framing` | 本地验证通过（限定范围） | 有界字节分帧、中文正文、粘包及歧义长度拒绝；完整原版差分另见成对报告 |
| `key-esl-sendmsg` | 本地验证通过（限定范围） | 两种UDP模式下真实set/unset/read、提示音和挂断取消；本地只A腿无需上游，原版全部应用合同未完成 |
| `key-esl-subscriptions` | 本地验证通过（限定范围） | 真实CREATE/ANSWER/HANGUP子集与ESL断连保留现有媒体；不含完整事件字段或outbound |
| `key-guard-capacity` | 本地验证通过（限定范围） | 业务并发边界和关闭软保护时仍保留硬上限 |
| `key-guard-establishing` | 本地验证通过（限定范围） | 建立中阈值与在线降限不切断已建立呼叫状态机 |
| `key-guard-rate-burst` | 本地验证通过（限定范围） | 令牌平滑补充、策略更新不额外补满及重传不重复扣费 |
| `key-guard-retry-after` | 本地验证通过（限定范围） | 过载Retry-After及被拒绝重传处理 |
| `key-guard-soft-throttle` | 本地验证通过（限定范围） | 业务保护平滑降速与令牌补充的单元合同 |
| `key-http-authentication` | 本地验证通过（限定范围） | 自有管理接口Host、CSRF及Content-Type拒绝规则；不包含远程用户认证 |
| `key-http-codecs` | 本地验证通过（限定范围） | 音频能力JSON形状、输出边界和缺少原生后端的反馈；不证明转码 |
| `key-http-concurrency` | 本地验证通过（限定范围） | 配置版本冲突和保护策略持久化 |
| `key-http-config-read` | 本地验证通过（限定范围） | 活动/待重启配置分离及配置快照切片所有权 |
| `key-http-config-write` | 本地验证通过（限定范围） | 保存待重启草稿与持久化失败不应用 |
| `key-http-metrics` | 本地验证通过（限定范围） | 控制进程资源缓存与队列观测字段；不覆盖全部指标或生产监控 |
| `key-http-readiness` | 本地验证通过（限定范围） | 自有就绪状态跟随准入条件 |
| `key-http-status` | 本地验证通过（限定范围） | 隔离HTTP测试中慢XML响应不阻塞状态/排空；不是高压时延SLA |
| `key-ipc-allocate` | 本地验证通过（限定范围） | 取消时保留在途分配结果与非法成功回复拒绝 |
| `key-ipc-connect` | 本地验证通过（限定范围） | 媒体连接更新合并与已结束通话不应用排队更新 |
| `key-ipc-dtmf-events` | 本地验证通过（限定范围） | 两种UDP模式下真实DTMF结束去重、缺结束报错与释放；不覆盖全部事件组合 |
| `key-ipc-dtmf-send` | 本地验证通过（限定范围） | 两种UDP模式下16按键/精确duration/三结束包、PT110、Opus48k/G7228k、共享播放包序与恒定事件时戳；拒绝不修改桥接RTP/RTCP；仅内部媒体合同 |
| `key-ipc-dtmf-send-status` | 本地验证通过（限定范围） | 两种UDP模式下真实完成包数、32数字队列整批拒绝、释放清理和暂停自有worker造成明确失败；completed是UDP提交，不是远端听到 |
| `key-ipc-pcm-push` | 本地验证通过（限定范围） | 原始S16LE批次到全部实际G.711样本、偏移/容量整批拒绝后继续；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。 |
| `key-ipc-pcm-turn-begin` | 本地验证通过（限定范围） | 真实ACK授权、同轮令牌幂等、较新轮次替换及播放互斥；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。 |
| `key-ipc-pcm-turn-end` | 本地验证通过（限定范围） | 实际PCM入队后End、样本守恒与排空终态，错误最终偏移可修正；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。 |
| `key-ipc-pcm-turn-interrupt` | 本地验证通过（限定范围） | 真实立即停止和40ms淡出、旧轮拒绝、DTMF共用RTP身份继续；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。 |
| `key-ipc-pcm-turn-status` | 本地验证通过（限定范围） | 原始回应身份/计数/终态及挂断、worker代次失效后的明确拒绝；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。 |
| `key-ipc-playback-file-start` | 本地验证通过（限定范围） | 两种UDP模式下真实WAV→G.711样本和尾帧、非法文件拒绝、loading停止/释放无迟到音频；限8k单声道/30秒 |
| `key-ipc-playback-start` | 本地验证通过（限定范围） | 两种UDP模式下真实PCMU/PCMA波形和无B腿本地播放；限单音提示 |
| `key-ipc-playback-status` | 本地验证通过（限定范围） | 两种UDP模式下实际播放状态、反向音频/双腿DTMF继续与CN替代 |
| `key-ipc-playback-stop` | 本地验证通过（限定范围） | 两种UDP模式下真实停止、幂等与任务所有权边界 |
| `key-ipc-release` | 本地验证通过（限定范围） | 释放失败结束分片代次与有界提交队列关闭 |
| `key-ipc-rx-status` | 本地验证通过（限定范围） | 实际身份、事件/样本计数和失败后仍可查询；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。 |
| `key-ipc-rx-stream` | 本地验证通过（限定范围） | 真实G.711输入逐样本核对、原始期限和过期拒绝；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。 |
| `key-ipc-rx-subscribe` | 本地验证通过（限定范围） | 真实订阅及ACK前/错误来源ACK拒绝；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。 |
| `key-ipc-rx-unsubscribe` | 本地验证通过（限定范围） | 明确退订、挂断撤销、资源归零及端口重新绑定；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。 |
| `key-media-capacity` | 本地验证失败 | 5000路本地双向媒体场景；仅对应此硬件/格式/时长，不是万路或FreeSWITCH成对容量认证 |
| `key-media-dtmf` | 本地验证通过（限定范围） | 两种UDP模式下telephone-event/CN时钟与透传、真实read收号及INFO去重；不含音内检测或完整FreeSWITCH收号语义 |
| `key-media-failover` | 本地验证通过（限定范围） | 两种UDP模式下分片故障代次重启与另一分片媒体继续；受损分片通话会结束，不是活动状态无损迁移 |
| `key-media-rtcp` | 本地验证通过（限定范围） | 两种UDP模式下测试RTCP包双向原样转发和端口归属；不覆盖原版完整RTCP统计/反馈语义 |
| `key-media-rtp` | 本地验证通过（限定范围） | 两种UDP模式下固定音频向量同格式RTP透传、跨批次接收及未协商载荷拒绝；不证明解码音质、转码或容量 |
| `key-media-transcoding` | 本地验证通过（限定范围） | 两种 UDP socket 模式各完整 11 项真实双腿 G.711 PCMU/PCMA、8kHz、20ms 转码；40ms 缓冲/120ms 上限、重排/迟到/CN/DTMF、基本 RTCP 和回收。仅本地限定功能，不包含 G.722/Opus、其他帧长、本地 IVR、ASR/TTS、FreeSWITCH 原版 API 或并发容量认证。 |
| `key-sip-bye` | 本地验证通过（限定范围） | 分叉BYE清理身份、边界和定时器回收的自有状态机 |
| `key-sip-cancel` | 本地验证通过（限定范围） | 迟到分叉最终响应清理事务去重；不覆盖原版完整CANCEL合同 |
| `key-sip-invite` | 本地验证通过（限定范围） | 两种UDP模式下可信中继INVITE重传分配去重、早期媒体及无ACK资源回收；不含用户注册或原版互通 |
| `key-sip-register` | 本地验证通过（限定范围） | 真实UDP/TCP/TLS注册、64包双向Rust媒体、ACK/BYE认证和注销；固定上游REGISTER/Digest客户端、认证重试、错误来源/循环/期限与取消状态机；不代表终端注册服务器或运营商认证 |
| `key-sip-sdp` | 本地验证通过（限定范围） | 两种UDP模式下真实SIP会话的非法SDP/不支持格式拒绝与隐式转码/PT重写拒绝；不代表实际跨格式转码 |

## 历史运行

历史Go race及90个端到端场景保留真实测试名和日志SHA；没有执行时源码清单时统一标记未绑定，不把当时通过等同当前版本通过。

## 新的SIP与媒体端到端采集

```sh
python3 tools/build_runtime_verification.py --record-e2e --control /absolute/path/to/rustswitch --media /absolute/path/to/rustswitch-media --build-receipt /absolute/path/to/build.json
```

固定顺序执行SIP、codec、ESL、快照、应用生命周期、变量、放音控制、WAV、拨号计划、媒体交互和本地按键发送等十二套用例的UDP/connected二十四组完整用例，每组180秒上限；用例只操作自己创建的隔离进程。捕获实际run/pass集合、完整unittest日志、退出码、前后runtime源码和二进制SHA。出现失败、跳过、超时、子进程残留或用例仅列出均不绿。两种模式全部成功才授予对应的本地RTP/RTCP/DTMF/分片隔离/可信中继/SDP子集。

构建收据build.json字段：schema_version="1.0.0"、kind="local_runtime_build"、started_at/finished_at、returncode=0、source_before/source_after（均由实际构建前后source_snapshot(root, True)采集）、binaries.control.sha256和binaries.media.sha256。先采集源码、实际构建、再采集源码与产物SHA；缺少构建收据时仍保留实际测试，但一律unbound。不得在构建完成后补造构建前清单。

## Linux 5000路证据合同

在证据目录保存 `*.receipt.json`，`schema_version=1.0.0`、`kind=capacity_5000`、唯一id、带时区started_at/finished_at、`binding_method=source_before_build_and_after_run`、`build_returncode=0`、完整source_before/source_after（使用本工具source_snapshot(root, True)）及runtime_os=linux/environment。

`binaries.control/media/generator` 和 `artifacts.build/report/status_samples/cleanup/journal` 均为 `{path,sha256}`，路径必须在同一证据目录内。build是实际构建前后收据，来源清单与三个产物SHA必须相同且构建完成早于采样；report是原始callbench JSON；status_samples为扁平JSON数组，每项含at、run_id、active_calls、established_calls和完整workers；run_id由采集器从自己实例日志读取后关联，并非原status接口字段。收据controller_run_id与全部采样、事件一致。cleanup必须包含complete/controller_exited/generator_exited/media_processes_exited/ports_released/work_dir_removed六个真实true；journal为原始JSONL事件。

门禁要求5000全部建立、至少10秒双向负载、名义包量至少98%、每方向minimum_received_per_flow达到98%×50×持续秒数且全部唯一包收到、generator_reader_errors及其他读写/重复/拆线错误为0、真实5000活跃采样、最终分片健康归零和journal_healthy且lost=0、Linuxsocket丢包计数可见且为零、日志并发峰值和完整回收。完整门禁未满足保留失败，不调阈值。

即使5000路通过，只给`key-media-capacity`附加“5000路本地场景通过”标识；原来的万路目标仍未验证，不能修改其实现状态或作为FreeSWITCH成对性能结论。
