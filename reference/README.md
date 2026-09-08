# 云蝠 Voice Runtime · RustSwitch 实时语音内核

采用 **Rust实时媒体 + Go业务控制 + C/C++协议及编解码生态适配**，构建面向电话语音智能体的高性能FreeSWITCH替代产品。首期优先完成电话与AI媒体闭环，按实际业务需要兼容FreeSWITCH接口。

研发方向和验收标准见[Voice Runtime方向说明](docs/api/voice-runtime-direction.md)，阶段0至4的当前缺口见[研发里程碑](docs/api/voice-runtime-roadmap.json)。初始目标是在相同硬件、功能和音质下稳定并发提升50%，同并发CPU与内存各降低至少30%，并通过发送尾延迟、音频正确性和72小时混合负载验收。这些是待测目标，当前没有完整AI链路性能收益证明。

开发原型已提供受限SIP线路桥接／注册客户端、本地IVR、入站ESL子集、Rust媒体分片、播放和收号、保护策略及中文后台。G.711实时图、带轮次的PCM下行和中断已有；1.13增加已ACK本地A腿的有界RX收音与过期语音拒绝。供应商ASR/TTS、VAD自动打断、双轨录音、完整租户和SRTP等仍须建设。

首期不要求复制通用PBX大全。会议、视频、传真、复杂分机、完整Dialplan、任意脚本插件和原模块二进制宿主放入后续范围。既有[FreeSWITCH完整对照标准](docs/freeswitch-compatibility/README.md)、3985个原始ID及失败记录保留用于迁移，不删除分母或把延后项标绿。固定参考为FreeSWITCH1.11.3，实际语义子集逐项验证。

[管理后台](http://127.0.0.1:9080)内嵌[完整接口文档](docs/api/README.md)，覆盖26个HTTP操作及SIP、ESL、媒体IPC、原生SDK和部署工具；运行总览分别观察主服务与隔离压测实例。使用压力页500至5000及自定义档位、小电话测试时，需同目录的callbench和已配置Rust媒体程序。

长期目标逐项关闭依据见[完整V1验收合同](docs/api/voice-runtime-v1-acceptance.md)：保留17项V1能力和M1—M4全部门槛，逐级运行1000/5000/10000路真实媒体处理与完整Agent负载。

本批功能、回归与兼容证据见[1.13验证记录](docs/api/release-validation-v1.13.md)，内部收音接口见[RX合同](docs/api/rx-stream-reference.md)；实际安装版本以管理后台文档接口为准。历史[1.12交付记录](docs/api/release-validation-v1.12.md)、[1.9方向调整](docs/api/release-validation-v1.9.md)和[5000路记录](docs/api/verification-5000-2026-09-06.md)保留当时范围，不沿用为当前代码、完整AI业务或万路能力认证。

## 0.3 管理功能

启动后打开 [http://127.0.0.1:9080](http://127.0.0.1:9080)，使用运行总览、峰值保护、SIP 与媒体、运维配置、FreeSWITCH 配置、兼容能力、编解码、测试与文档等十个中文页面。前端随 Go 可执行文件内嵌，不依赖 CDN 或单独的前端服务。

- **即时保护：**默认目标为总并发 8,000 路、建立中 1,000 路、新呼叫准入 1,000 次/秒；本地配置会收缩到 100 路、100 路、20 次/秒。达到任一并发保护阈值的 80% 后线性降低令牌补充速率，默认最低比例为 10%；超过阈值或令牌不足，返回 `503` 和 `Retry-After`，不排队、不延迟已经产生的 `200` 接通响应。
- **待重启配置：**表单覆盖当前原型配置；保存不会重启。媒体程序路径与日志路径只读。完整待重启配置、即时保护策略和 XML 草稿保存在原始 `journal.path` 后追加 `.admin.json` 的文件中。
- **FreeSWITCH 工作区：**内嵌固定 1.11.3 官方基线的 189 份 XML，初始映射 4,304 个可编辑字段，包含重复语言条目。支持分类、搜索、批量编辑、用户/网关/路由 XML 模板与 ZIP 导出；字段数量不等于独立功能数量或功能兼容率，XML工作区只负责编辑／导出；另有受限运行XML拨号计划，二者不等价。
- **编辑保护：**共享配置版本与 CSRF 校验，冲突返回 `409` 并保留本地草稿；运行轮询不覆盖输入。所有运行告警取自真实状态，浏览器观察记录不冒充完整服务器日志。

操作语义、参数口径、持久化与接口示例见 [0.3 管理功能交付说明](docs/management-v0.3.md)。已有的[可靠性与容量审查风险](docs/optimization-review-2026-09-05.md)仍须逐项处理，本轮页面和保护策略不代表它们已经全部修复。

## 当前实现

| 层 | 实现 |
|---|---|
| Rust | 独立媒体工作进程；固定槽位与带代次的就绪标识；Linux recvmmsg 批量收包；可选连接式 UDP；RTP/RTCP 校验与透明转发 |
| Go | 呼叫与双呼叫腿管理、受限 SIP UDP/TCP/TLS 协议提供器及有限入站 ESL、可取消定时器堆、负载准入、优先处理连接/释放、媒体进程监管、中文管理页面、配置持久化、即时峰值保护、HTTP 指标与事件日志 |
| C/C++ | 版本化编解码 C ABI；可编译运行的 C G.711 插件；Rust 动态加载与编解码往返验证；原生协议提供器的预留 ABI |

实际支持：

- 可信中继之间的 IPv4 SIP UDP/TCP/TLS `INVITE / ACK / CANCEL / BYE / OPTIONS`。
- A、B 两条独立 SIP 对话、事务重传、200 响应重传、取消与接通竞争的清理。
- 单音频流，同编码且相同 ptime；PCMU/PCMA、G.722、Opus 等受限格式协商与透明媒体转发，实际编解码后端能力单独探测；SDP offer/answer；183 早期媒体。
- 双向 RTP、分端口 RTCP、协商后的 RFC 4733 telephone-event 数据转发。
- 精确来源地址校验、信令及媒体 IP 白名单、每路媒体包速率预算。
- 全局并发/CPS/突发限制、分片容量、四端口整组分配、占用端口跳过和端口释放隔离期。
- 有界媒体控制队列及有期限的发送缓冲；明确统计超时、队列满及发送错误。
- 按媒体进程 CPU、已观测丢包、控制积压与统计新鲜度暂停新呼叫准入，连续三个健康样本后恢复。
- 接通后取消建立定时器；结束后立即删除长通话定时器，保留协议所需的有限事务清理窗口。
- 媒体进程故障只影响所属分片；自动重启并递增进程代次，旧代次命令不能操作新进程。
- HTTP 排空与恢复新准入入口；首次终止信号等待现有呼叫结束，再次信号强制结束；退出流程不能被网页恢复取消。
- 事件文件单写者锁、批量持久化、启动时修复末尾不完整记录。

**尚未实现或尚不完整：**正式 Sofia-SIP/PJSIP 提供器、终端注册服务器与多网关、SIP WS/WSS、Route/Record-Route、PRACK、会话刷新、re-INVITE/保持恢复、复杂分叉、SRTP/WebRTC/ICE、实时转码／ASR／TTS、双轨录音、完整IVR／ESL／XML、话单恢复对账和跨机状态接管。固定上游REGISTER/Digest客户端与受限本地IVR已单独实现，不等同这些完整合同。未支持的请求/协商会明确拒绝。RTCP 当前为校验后的透明转发，不生成完整终结型 RTCP 会话报告。

C/C++ 兼容指经适配接口复用原生库。FreeSWITCH 的既有模块不能直接二进制加载。C 插件示例已经实际运行；Sofia-SIP/PJSIP 接口目前是扩展契约，未声称已完成接入。G.711 转发路径不调用编解码器；插件验证是独立的 ABI 连通性验证。

## 工程结构

```text
control/             Go 控制面、开发 SIP 提供器、管理页面、官方配置模板与流量发生器
media/               Rust 媒体事件循环、端口池、RTP/RTCP、C ABI 宿主
native/include/      C/C++ 稳定适配接口
native/examples/     C G.711 动态库示例
config/              本地配置与 Linux 一万路目标配置
tests/               双向通话、异常流程和进程故障回归
tools/               本地压测编排、Linux 主机与应用指标采集
deploy/              Linux 服务模板
docs/                架构、能力边界、验证记录和测量结果
```

## 构建与运行

需要 Rust stable（包含 rustfmt/clippy）、Go 1.23 或更高、C 编译器、Python 3。Rust 依赖版本由 `media/Cargo.lock` 固定；Go 控制面无第三方模块依赖。

```sh
make all
bin/rustswitch -check-config -config config/local.json
bin/rustswitch -config config/local.json
```

默认 SIP 为 `127.0.0.1:5060`，固定上游为 `127.0.0.1:5070`，管理页面与 API 为 `127.0.0.1:9080`，启动最大并发 100 路、20 CPS。配置路径中的相对文件名按进程工作目录解析，应从项目根目录启动。默认管理状态文件为 `run/events.jsonl.admin.json`；基线未变化时，重启采用此前保存的待重启配置。人工修改基线配置会优先采用文件值；已有保护策略保留，仅在新硬上限降低时收紧相应阈值，在原管理状态可读取时保留 XML 草稿。

交付目录中的 `bin/` 为本机 macOS ARM64 构建。Linux 上请从源码重新构建，不要复制 Mac 二进制部署。

## 验证

```sh
make test
make check
python3 tools/local_bench.py --calls 100 --cps 100 --seconds 10 --capacity-mode
python3 tools/local_bench.py --calls 1000 --cps 200 --seconds 60 --capacity-mode \
  --connected --endpoint-connected --work-dir run/connected-1000 \
  --report run/connected-1000/report.json
```

上方示例使用 `--capacity-mode` 进行容量测量：仅关闭脚本自己启动的隔离实例的业务峰值保护，保留启动并发/CPS/突发硬限制和媒体健康准入，并在报告中记录模式。不加此参数时保留正常默认保护，用于核对过载拒接与存量通话保护；两类结果应分别判断。默认 8,000 路保护会阻止继续准入到一万路，因此万路实验必须先明确调整策略，不能把预期的策略拒接当成性能故障。

`local_bench.py` 自动启动隔离配置的服务和模拟两端，发送真实双向音频包，校验内容、来源、序列和资源清理，再停止本次启动的服务。另存配置、三个二进制哈希、并发采样、事件日志重建的峰值已建立会话数以及媒体计数。日志无法验证目标并发或尚未同步时不会通过。日志默认放在 `run/bench/`，各次测试建议使用独立目录。容量配置会随测试参数调整；该脚本仍受同机端口范围、文件描述符及发生器能力限制。

也可独立运行流量发生器，适合把压测机与被测服务器分开：

```sh
bin/callbench --server 198.51.100.10:5060 \
  --uas-listen 198.51.100.20:5070 \
  --bind-ip 198.51.100.20 --advertise-ip 198.51.100.20 \
  --calls 1000 --cps 100 --seconds 60 --connect-media \
  --media-port-start 10000 --media-port-end 64999 --output report.json
```

独立 `callbench` 不会自动更改被测服务器的保护策略；先在管理页核对实际并发、建立中、CPS 和突发阈值，将容量测量与默认保护下的过载测试分开。将示例地址替换为实际地址，并让服务的固定上游指向压测 UAS。此工具默认每个呼叫使用四个媒体 socket，需给压测机预留端口和文件描述符。显式端口范围默认采用固定种子打散分配；`--sequential-media-ports` 用于顺序端口压力对照。`--socket-groups 512` 可让发生器共享端口并按 SSRC 校验，不能与 `--connect-media` 同用；服务端仍为每路分配四个端口，报告会注明共享模式。

默认在 20ms 中按 20 个相位分散发包，`--phase-slots` 可调整。编排脚本的 `--burst` 对应发生器的 `--spread=false`；脚本的 `--direct` 对应 `--direct-media`，仅用于绕过媒体服务器的直连诊断。旧版 `--go-relay` 因未实现实际路径已删除，对应历史报告已标记无效。

`media.connect_sockets=true` 在协商完成后将服务端 UDP socket 连接到精确对端；发生器独立用 `--connect-media` 开启。它适用于当前固定端点中继模式，不实现 NAT 地址学习。内核可能提前过滤错误来源包，应用的来源拒绝计数不再覆盖这些包。

报告的 `sent_packets` 是发生器成功提交的包数，`received_unique_packets` 是接收端验证后的唯一包数。`nominal_packets` 是名义目标，`offered_load_ratio` 显示实际发生量。通过条件包含至少 98% 的名义负载，所有请求接通，实际提交的包全部收到，无内容错误或异常掉话。该结果不直接定位丢包所在层，也不代表物理网卡线速性能。

## 运维入口

| 入口 | 行为 |
|---|---|
| `GET /` | 十页面中文管理控制台 |
| `GET /healthz` | HTTP 管理服务存活状态 |
| `GET /readyz` | 未排空、日志健康、至少一个媒体分片可准入且峰值保护当前允许接纳时返回 200 |
| `GET /v1/status` | 呼叫、工作进程与媒体计数、日志、当前保护及配置版本 |
| `GET /metrics` | Prometheus 文本指标，含保护速率与拒绝计数 |
| `GET /v1/config`、`PUT /v1/config` | 读取有效/待重启配置及 CSRF；保存完整待重启配置 |
| `GET /v1/guard`、`PUT /v1/guard` | 读取保护快照；即时保存并应用策略 |
| `POST /v1/drain`、`POST /v1/resume` | 排空或恢复新准入；已有通话保持原生命周期 |
| `GET /v1/fs-config`、`PUT /v1/fs-config` | 读取字段目录；保存本次参数修改 |
| `GET /v1/fs-config/file`、`PUT /v1/fs-config/file` | 读取单个 XML；保存修改或新增文件 |
| `GET /v1/fs-config/export` | 下载已保存的标准 XML 配置树 ZIP |

管理接口强制监听回环地址；所有写请求都必须带 `Content-Type: application/json` 与 `X-RustSwitch-CSRF`。旧的无 token 排空请求不再适用。以下示例先读取 token，再执行排空；把 `action` 改为 `resume` 可恢复正常排空后的新准入：

```sh
python3 - <<'PYTHON'
import json
import urllib.request

base_url = "http://127.0.0.1:9080"
action = "drain"
with urllib.request.urlopen(base_url + "/v1/config", timeout=10) as response:
    snapshot = json.load(response)
request = urllib.request.Request(
    base_url + "/v1/" + action,
    data=b"{}", method="POST",
    headers={"Content-Type": "application/json",
             "X-RustSwitch-CSRF": snapshot["csrf_token"]},
)
with urllib.request.urlopen(request, timeout=10) as response:
    print(response.read().decode())
PYTHON
```

CSRF token 不代替远程用户认证；远程操作应通过 SSH 隧道访问本机管理地址。`readyz` 只表示瞬时可接入信号，不保证下一次请求仍有空闲端口。媒体统计每秒刷新，可能暂时落后于控制面计数，重启后以进程代次区分。`guard.limit_reason` / `throttled` 表示当前状态，`last_reject_reason` 是历史记录。

Linux 才能读取本实现的 `SO_RXQ_OVFL` socket 丢包计数；macOS 会明确报告不支持。网卡和内核全局丢包仍需结合 Linux 主机指标观察。API 字段、错误码、XML 限制和配置更新示例见 [管理说明](docs/management-v0.3.md)。

## 数据与故障边界

日志异步写入并按约 20ms 周期批量 fsync；调度、队列和磁盘延迟会扩大实际持久化窗口。SIP 接通成功并不代表所有事件已同步落盘，不能据此承诺 RPO=0。日志故障或积压会关闭新呼叫准入；已建立媒体继续运行。磁盘长期故障期间的事件可能丢失，指标会记录。

媒体进程崩溃时，该分片的通话会中断，控制面清理会话并重启进程，其他分片继续服务。整个 Go 控制进程崩溃时，子进程会因控制管道关闭而退出；本版不具备控制面崩溃后的存量通话保持。整机故障同样需要后续主备设计。

端口隔离期降低旧 UDP 数据串入新会话的概率，不能替代 SRTP 身份认证。当前版本面向受控实验环境和可信中继。配置白名单并不等同于完整 SIP 用户认证或公网 SBC 防护。

本轮中文注释覆盖自研 Go、Rust、C/C++ 适配头文件与 C 示例、JavaScript、Python 的关键入口、结构、函数与复杂逻辑；HTML/CSS 区域也有中文说明。第三方 FreeSWITCH XML 保留原注释、许可证和来源校验清单，不把原版注释改写成自研内容。

查看 [0.3 管理交付与验证状态](docs/management-v0.3.md)、[0.2 历史测量](docs/optimization-v0.2.md)、[未完成的优化审查项](docs/optimization-review-2026-09-05.md)、[架构](docs/architecture.md)、[Linux 验收步骤](docs/linux-validation.md) 和 [0.1 历史记录](docs/verification.md)。0.1/0.2 历史源码包不包含 0.3 管理功能，应以当前同版本源码构建。
