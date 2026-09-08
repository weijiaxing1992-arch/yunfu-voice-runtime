# FreeSWITCH 成对协议验证报告

本报告只记录已经实际执行的子场景。原始条目完整兼容、全部协议和全面超越均未通过认证。

原始条目：3999；完整条目通过：0；真实成对子场景通过：21。

| 探针 | 本次结果 | 严格范围 |
| --- | --- | --- |
| identity.esl | identity_observed | 读取两端真实版本并核实 reference 发行版本；不比较版本字符串 |
| esl.auth.fragmented | passed_scoped | 认证请求逐字节发送、成功身份和固定 echo 正文 |
| esl.auth.wrong | passed_scoped | 一个错误密码的回复及随后关闭；不包含 ACL、锁定、空密码、认证超时 |
| esl.auth.required | passed_scoped | 认证前 api echo 被拒绝且不获取身份，同连接随后有效认证和 echo 成功 |
| esl.echo | passed_scoped | api echo 固定 UTF-8 正文、Content-Length 字节数 |
| esl.console.single | passed_scoped | fs_cli -x 的 console_execute 单命令包装：UTF-8、空 echo、首尾空格、单分号和 false 字面参数；不代表别名、批处理或交互客户端 |
| esl.api.unknown | passed_scoped | 单个固定未知 API 命令的错误响应 |
| esl.frames.pipeline | passed_scoped | 一次写入两条 echo；逐帧读取并核对两个正文；不代表事件洪峰 |
| esl.frames.crlf | passed_scoped | CRLF 命令分隔的认证和 echo；不代表所有头部/长度边界 |
| esl.frames.split_positions | passed_scoped | 固定 echo 请求在每个字节边界拆成两次发送，每次独立连接 |
| esl.bgapi.echo | passed_scoped | 固定 Job-UUID、bgapi echo 回复及 BACKGROUND_JOB 结果；不包含并发作业/重连重放 |
| identity.api_inventory | inventory_observed | 真实 show api as json 注册目录；逐个对照全部 fs_api 原始 ID，接口存在不表示语义通过 |
| esl.api.echo_empty | passed_scoped | echo 无参数、尾空格、首尾空格的完整原始响应；不预设空白归一化 |
| esl.api.create_uuid | passed_scoped | 连续两次生成合法且不同 UUID，保留原字节；仅随机 UUID 值可规范化 |
| esl.api.uuid_exists_missing | passed_scoped | 固定 nil UUID 实际不存在时返回 false；不代表存在通道完整契约 |
| esl.api.uuid_getvar_missing | passed_scoped | 先核实固定 nil UUID 不存在，再读取变量并比较完整错误 |
| esl.api.uuid_setvar_missing | passed_scoped | 仅隔离 fixture，确认 nil UUID 不存在后尝试设置并比较错误；不修改真实通道 |
| esl.api.uuid_kill_missing | passed_scoped | 仅隔离 fixture，确认 nil UUID 不存在后调用并比较错误；不挂断真实通道 |
| esl.api.uuid_syntax | passed_scoped | uuid_exists/getvar/setvar/kill 缺参数的真实错误/usage；不执行有效通道动作 |
| esl.command.case | passed_scoped | 混合大小写 AUTH/API/EVENT、格式沿用及 nixevent/noevents 开关 |
| esl.filter.lifecycle | passed_scoped | filter delete all、空值清除、noevents/nixevent；以真实后台事件交付证明过滤已清除 |
| esl.bgapi.echo.json | passed_scoped | JSON BACKGROUND_JOB、固定 UUID、原始正文与内层字节长度 |
| esl.bgapi.echo.xml | passed_scoped | XML BACKGROUND_JOB、固定 UUID、headers/root-body 结构、原始正文与内层字节长度 |
| sip.options.udp | failed_difference | 一个 OPTIONS 事务、状态码/能力头和回显事务标识；不代表完成呼叫 |
| sip.options.tcp | failed_difference | TCP 连接上一个 OPTIONS 请求及完整响应；不代表 TCP 呼叫/复用/背压 |
| sip.options.tls | failed_difference | 验证证书的 TLS 连接上一个 OPTIONS；不允许跳过证书验证 |

实际 API 目录：原版 189 个入口，候选 14 个入口；逐个对照 292 个原始 API 声明。存在入口仍需语义验证。

完整原始请求/响应（密码帧脱敏）、差异、逐条后续步骤和哈希见同名 JSON。

源码和指定二进制/配置文件哈希是可追溯材料；远端正在运行进程与这些文件的对应关系仍需原构建/启动记录证明。
