# RustSwitch 0.3.0 中文管理功能交付说明

版本：0.3.0 开发原型。日期：2026-09-05。

本轮交付六页面中文管理控制台、即时峰值保护、持久化启动配置草稿，以及 FreeSWITCH 官方配置的编辑与导出。核心架构仍是 Rust 媒体、Go 控制面与 C/C++ 原生适配接口。页面和字段编辑能力不代表 FreeSWITCH 的应用、协议、脚本或模块已实现，也不构成单机一万路容量认证。

## 1. 启动与页面

从项目根目录构建、校验并启动：

```sh
make all
bin/rustswitch -check-config -config config/local.json
bin/rustswitch -config config/local.json
```

浏览器打开 `http://127.0.0.1:9080`。默认配置采用 100 路启动并发、20 次/秒的新呼叫速率、40 次启动突发容量；固定 SIP 上游是 `127.0.0.1:5070`。页面、样式、脚本和官方 XML 随 Go 可执行文件内嵌，无需 CDN、联网加载资源或单独的前端服务。交付的本机二进制不能直接用于 Linux，Linux 必须重新构建。

| 页面 | 可使用功能 | 提交或观测的实际含义 |
| --- | --- | --- |
| 运行总览 | 活跃/接通通话、分片与进程代次、媒体包计数、趋势、日志健康、当前告警 | 每 3 秒读取真实状态，后台标签页暂停定时采集；趋势仅包含本次浏览器采样 |
| 峰值保护 | 总并发、建立中、CPS、突发容量、柔性降速和 Retry-After | 策略即时生效并保存；下方启动硬上限独立保存待重启 |
| SIP 与媒体 | 监听/对外/上游地址、CIDR、超时、端口、工作进程、缓冲、CPU 绑定、媒体压力准入 | 保存完整启动配置草稿；当前进程继续使用原配置 |
| 运维配置 | 管理地址、日志队列、持久化计数、配置快照、排空/恢复 | 配置待重启；排空与恢复即时处理新准入 |
| FreeSWITCH 配置 | 分类、搜索、文件筛选、批量字段编辑、XML、高级新增参数、用户/网关/路由模板、ZIP 导出 | 保存文本草稿，当前 RustSwitch 不执行这些 XML |
| 兼容能力 | 各项功能的已实现、部分实现、导出可用、待实现、待验收状态 | 以服务端能力清单为准，不显示未经认证的完成百分比 |

运行轮询不会重绘正在编辑的配置输入。离线时保留最后成功采样，并标记数据未更新。日志页面没有原始服务器日志接口；“日志与观察”仅显示真实累计计数与当前浏览器观察到的状态变化，不能代替持久化审计日志。

当前限流使用 `guard.limit_reason` 与 `guard.throttled`。`last_reject_reason` 是最近一次拒绝的历史原因，负载恢复后仍可能非空，页面不会据此持续报警。

## 2. 峰值保护的三个口径

| 字段 | 默认目标 | 单位与计算口径 |
| --- | --- | --- |
| `max_active_calls` | 8,000 | 路桥接通话；已预留且尚未释放的活跃资源数，包含建立阶段 |
| `max_establishing_calls` | 1,000 | 路；`max(活跃数 - 已建立数, 0)`，也包含结束后仍待释放的资源 |
| `calls_per_second` | 1,000 | 次新呼叫准入/秒；令牌补充速率，不是同一时刻的并发数 |
| `burst_calls` | 最多 1,000，且不高于启动突发上限及默认 CPS | 次；令牌桶的短时突发容量 |
| `soft_limit_ratio` | 0.8 | 任一并发保护阈值使用率达到 80% 后开始降速 |
| `minimum_rate_ratio` | 0.1 | 尚未因硬阈值拒绝时，令牌补充速率最低为基准的 10% |
| `retry_after_seconds` | 1 | 秒；拒绝新呼叫时返回的建议重试间隔 |

保护器只检查全新的初始 `INVITE`。同一事务的重传，以及已有通话的 `ACK`、`BYE`、媒体转发等，不按新呼叫再次扣减令牌。降低阈值不会遍历或挂断已有通话；即使当前通话数高于新阈值，也只是停止接纳后续新呼叫，等待正常结束释放。

该策略控制新呼叫进入系统的速度，**不限制实际 `200 OK` 接通响应的产生时间或每秒接通数**。不会把已产生的 `200` 延迟到某个节拍，也不会将被拒绝的呼叫放入服务端等待队列。达到总并发/建立中阈值，或令牌不足时，返回 `503` 与 `Retry-After`；对端决定是否按建议等待后发起新的事务。建议重试时间不保证届时一定存在容量。

### 线性降速

取两个使用率中的较大值：

```text
u = max(活跃资源数 / 总并发保护上限,
        建立中资源数 / 建立中保护上限)

u <= 0.8：有效 CPS = 配置 CPS
u > 0.8：有效 CPS = 配置 CPS × max(0.1, 1 - min(1, (u - 0.8) / 0.2) × 0.9)
```

默认配置下，`u=0.9` 时令牌补充速率为基准的 55%。达到任一完整并发阈值时，仍会拒绝新准入，即使令牌桶尚有余额。令牌按时间补充，短时接入量还受 `burst_calls` 约束，不能把该机制解释为每个自然秒严格等量接入。

`enabled=false` 只关闭额外的动态并发阈值和柔性降速；启动并发、CPS 与突发硬上限仍然生效。日志异常、排空、媒体分片压力和实际资源分配也可以独立拒绝新呼叫。

### 启动硬上限与本地自动收缩

即时策略必须同时满足当前有效启动配置和已经保存的待重启配置的硬边界。网页不能通过热更新把本地 100 路实例提升到 8,000 路。应先保存合适的启动硬上限、端口等配置，正常重启后，再提升即时策略。反向降低启动容量时，应先将即时策略调低到新容量以内。

首次使用未保存策略的本地配置时，默认保护值自动成为：总并发 100 路、建立中 100 路、20 次/秒，保护层突发容量 20 次。启动突发上限仍为 40 次；两者是独立层次。页面“使用建议值”仅填入受启动硬上限约束的本地表单，点击“立即应用”才会提交。

本实现一条桥接通话有 A/B 两条呼叫腿，各自使用 RTP/RTCP，共四个媒体端口。启动配置校验采用以下预算：

```text
可分配端口组 = floor((port_end - port_start + 1) / 4)
所需端口组 = max_calls + ceil(calls_per_second × port_reuse_delay_ms / 1000)
要求：可分配端口组 >= 所需端口组
```

`10000–64999` 提供 13,750 组；一万路启动并发、1,000 CPS、2 秒隔离期需要 12,000 组。本地 `20000–21999` 提供 500 组，100 路、20 CPS、2 秒需要 140 组。全局预算通过不保证各分片端口周转均匀，也不证明 CPU、网卡或发生器达到所需能力。

### 容量测量与保护验收分开

Linux 一万路示例的启动硬上限是 10,000，但未保存策略时的默认业务保护仍是 8,000 路。直接发起万路请求可能得到符合策略的 503，不能据此判断性能失败。容量实验之前，应在管理页面检查总并发、建立中、CPS、突发容量和柔性降速，明确采用可承载实验目标的策略；过载保护验收则应单独保留或设定业务保护阈值。

本地编排工具提供显式 `--capacity-mode`：只关闭脚本自己启动的隔离服务的业务 Guard 额外阈值与柔性降速，保留启动并发/CPS/突发硬限制、媒体健康准入和实际资源分配校验，并在报告记录该模式。该开关不修改其他运行服务，不是通用的无限制模式。独立 `callbench` 不会更改远端服务策略。

```sh
python3 tools/local_bench.py --calls 1000 --cps 200 --seconds 60 \
  --capacity-mode --connected --endpoint-connected \
  --work-dir run/capacity-1000 --report run/capacity-1000/report.json
```

不传 `--capacity-mode` 时采用正常保护策略。容量结果和保护策略效果应使用不同验收目标与报告，避免把预期拒接误报为性能失败，也避免用关闭业务保护后的测量证明正常策略下的接纳能力。

## 3. 配置保存、重启与部署优先级

管理状态文件由**原始启动文件中的 `journal.path`** 加 `.admin.json` 得到。默认是 `run/events.jsonl.admin.json`，Linux 示例是 `/var/lib/rustswitch/events.jsonl.admin.json`。相对路径按进程工作目录解析。

单个管理文件保存共享 `revision`、原始配置摘要、完整 `desired`、即时 `policy` 和相对官方基线修改/新增的 `fs_files`。浏览器不会改写原始启动 JSON，也不会把 XML 写入任意主机路径。媒体可执行程序 `media.binary` 与日志 `journal.path` 在页面只读，后端同时拒绝对此两项进行网页修改；部署人员可在启动文件中调整。

启动文件解析后的配置摘要未改变时，下次启动采用已保存的 `desired`。人工改变启动配置后，启动文件成为新的有效配置；已有保护策略保留。如果新的启动硬上限更低，只收紧相应的总并发、建立中、CPS 和突发数值，保留开启状态、软降速比例、最低比例和重试间隔。原管理状态可读取时，XML 草稿也保留。启动载入本身不写盘，随后一次成功保存会写入新的状态。

如果部署人员改动 `journal.path`，派生出的管理状态文件路径也会改变，旧文件不会被自动搜索和迁移。需要按明确的部署步骤处理状态迁移，不能将“保留 XML”理解为跨任意路径自动查找旧草稿。

提交先写同目录临时文件并同步，然后原子重命名；文件替换成功后才发布内存版本。目录同步失败会记录警告，因为文件已经替换，不能把这次提交当作从未发生。该流程不构成任意断电场景下零数据丢失的承诺。

所有配置页共享一个乐观并发版本。版本过期返回 `409`；页面保留本地输入，可导出本地草稿，再重新载入并人工合并。不会自动带新版本重试旧表单。自己的保存成功仅推进同一旧版本的本地状态；已经过期的其他草稿仍保留旧版本。提交过程中锁定编辑入口，防止返回响应覆盖新输入。

## 4. FreeSWITCH 配置覆盖与限制

固定参考为 FreeSWITCH **1.11.3**，commit `ef32e205295e29f034f1453ad245ba5efb07b94a`，来自官方 [conf/vanilla](https://github.com/signalwire/freeswitch/tree/ef32e205295e29f034f1453ad245ba5efb07b94a/conf/vanilla)。源码与逐文件 SHA-256 保存在 [SOURCE.json](../control/internal/server/fs_templates/SOURCE.json)，许可见 [LICENSE.freeswitch](../control/internal/server/fs_templates/LICENSE.freeswitch)。

未修改基线包含 **189 份 XML、4,304 个可编辑字段**。这个统计包含不同文件和不同语言中的重复条目，不能解释为 4,304 个独立功能，更不能作为 FreeSWITCH 兼容完成率。用户增加文件或改变结构后，文件和字段计数随之变化。

表单覆盖 `param/variable/option` 的值、`action/anti-action` 参数、`condition` 表达式、`load` 模块名、`X-PRE-PROCESS set` 的取值以及 ACL CIDR。其他结构、顺序、include、嵌套条件等通过 XML 编辑器修改。分类包含 Sofia/SIP、目录、拨号计划、语言与提示音、编解码、录音话单、会议、脚本、控制接口和访问控制。

用户、Sofia 网关和拨号计划生成器将输入转义后生成可查看的 XML 草稿。生成不等于保存，保存不等于运行；include 位置、模块依赖、号码路由和完整业务语义仍需在对应的 FreeSWITCH 环境验证。配置响应始终带 `mode: "export_only"` 与 `runtime_supported: false`。

参数修改按相对路径与字段次序构成的 ID 提交，因此必须绑定目录读取时的版本。返回的 `parameters.value` 已包含已保存修改，`overrides` 返回空对象作为本次编辑入口；提交时只发送这次修改的 ID/值，后端保留其他已保存配置。XML 结构保存后重新生成字段目录；不能跨版本猜测旧 ID。

高级编辑支持多根 XML 片段、变量表达式和预处理标签，但不会执行预处理、加载模块、读取外部实体或发出网络请求。拒绝 DTD/实体声明、目录穿越和非法路径；单文件最多 256 KiB，最多保存 512 份文件修改，XML 草稿合计最多 5 MiB。XML 草稿使用 UTF-8；基线中五份声明 Windows-1252、实际仅含 ASCII 的文件，在读取/导出时统一为 UTF-8 声明，原始模板与来源清单仍保留。

导出 ZIP 包含已保存的 `conf/` 配置树、许可证及来源信息。本地未保存的参数和 XML 不进入导出包。批量参数修改尽量保留原文本；高级 XML 编辑以提交内容为准，不保证人工重新序列化后的空白排版与官方逐字一致。

## 5. 管理 API

服务仅监听回环地址，检查请求 Host；浏览器写请求还检查同源 Origin。CSRF token 通过 `GET /v1/config` 获得，进程重启后应重新读取。CSRF 不是用户登录认证，不提供多用户账户或权限管理；远程管理应使用 SSH 隧道访问回环地址。

| 方法与路径 | 请求要点 | 响应/效果 |
| --- | --- | --- |
| `GET /` | 无 | 内嵌中文管理页面；静态资源为 `/app.css`、`/app.js` |
| `GET /healthz` | 无 | HTTP 入口存活和版本 |
| `GET /readyz` | 无 | `ready`；排空、日志异常、无可准入分片或当前保护拒绝时为 503 |
| `GET /v1/status` | 无 | 呼叫、进程、媒体、日志、保护、配置版本、`restart_required` |
| `GET /metrics` | 无 | Prometheus 文本，包括有效 CPS、阈值与分原因拒绝累计数 |
| `GET /v1/config` | 无 | `revision`、`csrf_token`、`active`、`desired`、`restart_required`、`guard`、`persisted`、`notice` |
| `PUT /v1/config` | `{revision, config: 完整配置}` | 保存待重启配置；返回新的配置快照 |
| `GET /v1/guard` | 无 | `{revision, guard}`，包括策略、有效速率、令牌、当前原因、累计拒绝 |
| `PUT /v1/guard` | `{revision, policy: 完整策略}` | 先保存成功，再更新当前准入；返回新版本与保护快照 |
| `POST /v1/drain` | `{}` | 停止新呼叫准入，保留已有通话，不自动退出 |
| `POST /v1/resume` | `{}` | 恢复正常排空后的新准入；收到停止信号后的退出流程返回 409 |
| `GET /v1/fs-config` | 无 | 文件、分类、字段、版本、修改文件数、能力与仅导出标记 |
| `PUT /v1/fs-config` | `{revision, overrides: {字段ID: 值}}` | 保存本次参数修改；不加载 XML |
| `GET /v1/fs-config/file?path=…` | 已知相对 `.xml` 路径 | `{revision, path, xml}`；未知路径返回 404 |
| `PUT /v1/fs-config/file` | `{revision, path, xml}` | 校验并保存/新增 XML；返回新版本及 `runtime_supported: false` |
| `GET /v1/fs-config/export` | 无 | 下载当前已保存配置的 ZIP |

所有写请求都必须使用 `Content-Type: application/json` 和 `X-RustSwitch-CSRF`。配置类 `PUT` 共享递增版本；排空/恢复改变运行状态，不要求配置版本。正文最多 4 MiB，只接受单个 JSON 对象并拒绝未知字段。常见错误包括 400 请求格式、403 Host/Origin/token、409 版本冲突或退出中禁止恢复、415 正文类型、422 配置/XML 校验、500 读取或保存失败；响应提供 `error`，配置版本冲突还提供当前 `revision`。

`readyz` 是瞬时信号；返回 200 不预留下一次呼叫的媒体端口。当前准入原因为空也不等于所有依赖、资源和网络均可用。保护计数只覆盖经过保护器的检查，不能代替所有 SIP 拒绝统计。

### 正确发送带 CSRF 的排空请求

旧的 `curl -X POST .../v1/drain` 不再适用。以下示例不依赖 jq，运行后会排空当前实例；把 `action` 改为 `resume` 可恢复正常排空后的准入：

```sh
python3 - <<'PYTHON'
import json
import urllib.request

base_url = "http://127.0.0.1:9080"
action = "drain"
with urllib.request.urlopen(base_url + "/v1/config", timeout=10) as response:
    config = json.load(response)
request = urllib.request.Request(
    base_url + "/v1/" + action, data=b"{}", method="POST",
    headers={"Content-Type": "application/json",
             "X-RustSwitch-CSRF": config["csrf_token"]},
)
with urllib.request.urlopen(request, timeout=10) as response:
    print(response.read().decode())
PYTHON
```

### 即时保存一组受硬上限约束的保护值

以下示例保留现有其他策略字段，将三种容量目标调整到当前/待重启硬上限以内，再以读取到的版本提交。若其他操作者同时保存导致 409，应重新读取并检查冲突，不自动重试：

```sh
python3 - <<'PYTHON'
import json
import urllib.request

base_url = "http://127.0.0.1:9080"
with urllib.request.urlopen(base_url + "/v1/config", timeout=10) as response:
    config = json.load(response)
active = config["active"]["limits"]
desired = config["desired"]["limits"]
policy = dict(config["guard"]["policy"])
policy.update(
    enabled=True,
    max_active_calls=min(8000, active["max_calls"], desired["max_calls"]),
    max_establishing_calls=min(1000, active["max_calls"], desired["max_calls"]),
    calls_per_second=min(1000, active["calls_per_second"], desired["calls_per_second"]),
    burst_calls=min(1000, active["burst_calls"], desired["burst_calls"]),
)
request = urllib.request.Request(
    base_url + "/v1/guard",
    data=json.dumps({"revision": config["revision"], "policy": policy}).encode(),
    method="PUT", headers={"Content-Type": "application/json",
                           "X-RustSwitch-CSRF": config["csrf_token"]},
)
with urllib.request.urlopen(request, timeout=10) as response:
    print(response.read().decode())
PYTHON
```

## 6. 中文注释与来源边界

自研 Go、Rust、C/C++ ABI 头文件及 C 示例、JavaScript、Python 的入口、关键结构、函数和复杂控制逻辑补充中文注释；HTML 区域和 CSS 布局也有中文说明。注释解释实现行为和边界，不改变 ABI 符号、SIP 字段、配置键或第三方协议名称，也不表示每一行都已获得完整正确性证明。

第三方 FreeSWITCH XML 保留上游原注释、许可证与来源校验信息。参数文案与管理页面是本项目补充内容，不把原版 XML、模块或文档声明为自研实现。Rust 依赖继续由锁文件固定，原生库仍通过自定义 ABI 适配；现有 FreeSWITCH 模块不能直接二进制加载。

## 7. 验证记录与仍未完成的工作

以下记录针对本轮代码，由主开发任务执行并回报；功能回归通过不等于容量或 FreeSWITCH 兼容验收。

| 验证项 | 当前结果 | 证据边界 |
| --- | --- | --- |
| Go 全包竞争检测 `go test -race ./...` | 已通过 | 不检测所有协议次序或外部系统问题 |
| Go 静态检查 `go vet ./...` | 已通过 | 不等同于完整行为认证 |
| Rust 单元测试 | 6 项通过 | 当前媒体基础行为范围 |
| Rust Clippy | 已通过 | 按工程检查参数执行 |
| C 示例与 Rust 宿主编解码往返 | 已通过 | 独立 G.711 ABI 验证，不是转码通话 |
| 原 14 项通话端到端场景，两种 UDP 模式 | 均通过 | 保留原功能回归范围 |
| 新增第 15 项在线降限并保留已有通话，两种 UDP 模式 | 均通过；合计 30 个场景 | 已有通话、热更新拒接与释放，不是高并发实测 |
| 前端 JavaScript | 语法与 16 项隔离逻辑检查通过 | DOM 桩逻辑检查不替代真实浏览器交互 |
| 管理接口加固 | 全包竞态回归通过 | 覆盖重启保留人工收紧策略、重复 XML 属性、命名空间及 Unicode 属性错改 |
| 实际浏览器交互 | 已通过本轮操作验收 | 在线策略、参数/XML 保存、路由模板、待重启配置、排空/恢复、409 保留草稿、移动菜单；390/1280 像素无横向溢出 |
| 100 路、10 秒双向媒体 | 99,992 包发送并全部接收；零异常挂断，结束后资源为零 | macOS ARM64、连接式 UDP；显式容量模式，结束恢复原保护开关，不能外推万路 |
| Go Linux amd64 交叉构建 | 通过 | 仅构建，不是 Linux 运行验收 |
| Linux 一万路持续媒体、混合持续建拆与故障验收 | 未完成 | 目标 Linux 服务器尚待提供 |
| 完整 FreeSWITCH 无感替换认证 | 未完成 | 标准与模板已经交付，运行功能仍存在大量缺口 |

本轮原始记录见 [verification-v0.3](verification-v0.3/README.md)，包含最终 Go 检查、Rust/编解码检查、两种通话回归、百路测试结果、页面检查记录和构建哈希。两种通话模式顺序运行，页面使用独立空闲实例，不将演示实例的一万路配置视为已承载一万路。

0.2 历史最终千路 60 秒完整收发 5,999,904 个 RTP 包；万路共享发生器端点测试曾建立一万个会话，但负载不足、收包和清理严重失败，因此不能作为万路媒体通过证据。前后失败和通过记录都保留在[历史测量报告](optimization-v0.2.md)，本轮没有用管理页计数替代重测。

[既有代码与容量审查](optimization-review-2026-09-05.md)中的“媒体失败后未接通 B 腿遗漏 CANCEL”已在本轮修复并加入回归。仍待逐项闭环的问题包括媒体命令执行次序、控制查询超时扩大为分片故障、控制面重启时媒体续存、端口隔离与分片选择、日志积压恢复，以及发送调度预算。管理页面和峰值保护不能自动消除这些风险。

当前容量工具仍有分阶段建呼叫/发媒体/拆呼叫、实时延迟和逐路负载测量不足等限制，需补持续混合业务模型与独立 Linux 发生器/主机证据。整机故障接管、控制面崩溃后保留通话、持久化零数据损失、满载零丢包仍没有实现或验证保证。

FreeSWITCH 目录、拨号计划、ESL、模块、脚本、CDR 等完整运行语义仍须按照[无感替换接口标准](freeswitch-compatibility/README.md)逐项实现和差分验收。配置编辑与导出是本轮已交付能力，完整替换 FreeSWITCH 仍是后续目标。
