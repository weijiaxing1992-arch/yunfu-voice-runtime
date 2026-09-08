# 管理后台接口文档交付记录

交付日期：2026-09-05。文档集版本：1.0.0；项目仍为 RustSwitch 0.3.0 开发原型。

## 后台入口

- `/#api-docs`：HTTP 操作、章节文档和数据结构三个阅读视图。
- `/#fs-comparison`：完整 FreeSWITCH 固定目录及关键语义对照。

页面直接从控制进程内嵌文档读取，不依赖外部 CDN、数据库或在线文档站点。全部新代码的主要类型、字段、函数和边界均有中文注释。

## 交付范围

| 内容 | 已交付范围 |
| --- | --- |
| HTTP 接口 | 16 个路径、20 个 method/path 操作；请求、响应、错误、写入边界和逐操作对照 |
| 数据结构 | OpenAPI 3.1、41 个复用模型、24 个媒体统计字段 |
| 非 HTTP 接口 | SIP/SDP/RTP/RTCP、媒体 JSON 管道、C 编解码 ABI、预留协议 ABI、CLI、JSONL 事件 |
| 发布文档 | 38 份原文；含原始目录、标准、验收模板、头文件和来源许可 |
| FreeSWITCH 对照 | 3,978 条记录：3,923 条目录/验收基线与 55 条人工关键语义对照 |
| 阅读功能 | 全量检索、分类和状态筛选、分页、精确条目/章节定位、嵌套结构展开、示例复制、原文和 ZIP 下载 |

FreeSWITCH 对照固定为 1.11.3 / `ef32e205295e29f034f1453ad245ba5efb07b94a`。记录分别说明原版入口/语法/语义、本项目入口/语义、实现状态、关系、全部已记录差异、验证依据及来源。

HTTP 操作中 15 个管理与观测操作直接定位相应关键对照；5 个新增文档操作定位自身中文说明，明确没有同名 FreeSWITCH HTTP 入口。

## 本次验证

| 检查 | 结果 |
| --- | --- |
| Go 全包竞态测试 | `go test -race ./...` 通过；最终发布文档另行通过 `TestDocumentation*` 竞态测试 |
| Go 静态检查与构建 | `go vet ./...`、`make control` 通过 |
| 正式文档规范 | OpenAPI 3.1 规范验证与媒体 IPC JSON Schema 结构验证通过 |
| 实际只读 HTTP | 14 个 GET 操作全部成功；其中 10 个 JSON 响应按模型校验，文本和归档按其实际格式验证 |
| 错误返回 | 缺少 path、路径穿越、未发布文件三类实际响应符合 400/404 模型；单元测试另覆盖重复查询、绝对路径、反斜线和非法编码 |
| 下载完整性 | 38 个实际文件逐一核对字节长度及 SHA-256；ZIP 中 38 份原文和 1 份清单完全一致 |
| 实际媒体管道 | 独立本机工作进程的 5 类命令及 ready/allocated/ack/stats/error 响应全部符合 Schema；未发送通话流量 |
| FreeSWITCH 来源 | 712 份固定源码哈希、3,978 个唯一 ID、原目录全量覆盖、原语法/参数和来源链接检查通过 |
| 构建防止漂移 | 原文与内嵌副本一致；Python 优化模式下缺少 `/healthz` 文档仍被显式拒绝 |
| 浏览器 | 桌面与 390 像素手机布局验证；搜索、分页、筛选恢复、中文状态、精确对照定位、章节锚点、原生头文件阅读与复制反馈通过；未发现控制台错误 |

浏览器验收修正了自动示例的无效 Host 覆盖、深层示例被过早截断为 null、对照链接使用整段说明导致无法匹配，以及长源码路径在手机端溢出的问题。自动模板只用于说明结构；部署地址、令牌、配置版本和业务参数仍须使用实际值。

只读 HTTP 与浏览器文档操作没有执行业务写入，配置 revision 保持 10；本机预览的保护上限仍为 8,000 路活跃资源和 1,000 路建立中资源。服务仅在确认活跃通话为 0 后重新加载新二进制。

本次工作没有重新执行万路容量测试，也没有运行原版 FreeSWITCH 差分认证。目录完整描述锁定源码选择集，不代表所有动态/第三方模块运行接口已采集。“自有接口已实现”不表示 FreeSWITCH 线格式兼容，XML 编辑导出也不表示 RustSwitch 已执行 XML 配置。

## 维护文件

- [文档中心说明](api/README.md)、[HTTP 参考](api/http-reference.md)、[非 HTTP 参考](api/protocol-reference.md)、[逐项语义对照](api/freeswitch-comparison.md)。
- [发布生成器](../tools/build_api_docs.py)：校验路由、引用、对照 ID、发布路径并构建内嵌副本；门禁在 Python 优化模式下仍执行。
- [对照生成器](../tools/build_interface_comparison.py)：从固定源码和目录生成对照数据，支持离线 `--check`。
- [文档读取与下载服务](../control/internal/server/documentation.go)、[文档边界测试](../control/internal/server/documentation_test.go)。
- [后台阅读器](../control/internal/server/web/docs.js)、[文档样式](../control/internal/server/web/docs.css)。

日常构建运行 `make control` 即可同步文档；检查现有副本运行 `python3 tools/build_api_docs.py --check`。修改接口后同步 OpenAPI 和相关章节，再重新构建控制进程。重建 FreeSWITCH 对照另须提供文档指定提交的本地参考源码目录。
