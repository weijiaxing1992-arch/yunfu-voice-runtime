# 上游 SIP 中继注册与认证

本接口用于向一个固定运营商／SIP 中继注册，并在出站 INVITE 收到认证挑战时完成 Digest 重试。它是注册客户端；不提供终端账户注册服务器，也不等于完整 Sofia gateway 或 FreeSWITCH 注册模块兼容。

新增项均位于启动配置的 `sip` 对象。省略 `trunk_auth` 和 `registration` 时，原有未注册、未认证模式保持不变。修改这些项需要按管理配置流程保存并重启，运行中不会重新读取密码文件。

## 配置字段

| 字段 | 默认／要求 | 含义 |
|---|---|---|
| `sip.trunk_auth` | 可省略 | 固定上游的 Digest 客户端凭据配置；可单独启用 INVITE 认证 |
| `sip.trunk_auth.username` | 必填，1～128 字节 | 认证账户；可以与注册 AOR 的用户部分不同，不接受冒号或控制字符 |
| `sip.trunk_auth.password_file` | 必填，绝对路径 | 本进程可读取的私有普通文件，权限不得向组／其他用户开放；拒绝最终文件软链接 |
| `sip.trunk_auth.realm` | 可省略，最长256字节 | 非空时只接受精确匹配的挑战域；省略时接受固定上游提供的受支持挑战 |
| `sip.registration` | 可省略 | 唯一上游注册对象；可在可信、无认证的注册器场景单独使用 |
| `sip.registration.aor` | 必填 | 简单注册身份，例如 `sip:line@carrier.example` |
| `sip.registration.registrar_uri` | 必填 | 请求 URI，例如 `sip:carrier.example` 或 `sip:carrier.example:5060`，不带用户部分 |
| `sip.registration.expires_seconds` | 300；允许30～86400 | 请求的注册有效期；最终有效期以原注册器响应为准 |
| `sip.registration.retry_seconds` | 30；允许1～3600 | 失败重试的基础间隔；连续失败指数退避，最长3600秒 |
| `sip.registration.timeout_ms` | 8000；允许500～32000 | 每轮注册的总期限，包含该轮全部认证与423重试，不能被挑战延长 |

逻辑域名只用于 SIP URI 和 Digest 输入；实际网络目的地始终是已有 `sip.upstream` 的 IPv4 地址与端口。不会根据挑战、Contact、重定向或逻辑域名改变发送目的地。

AOR 用户部分限制为已有简单 SIP 用户字符，最长64字节；AOR／registrar URI 不接受 URI 密码、查询、百分号转义、复杂参数或 `sips:`。`sip.stream.upstream_transport` 继续决定真实传输。启用 TCP/TLS 注册时必须同时配置对应本地监听，以提供可回呼的 Contact 端口。

## 完整配置示例

以下为 UDP 隔离环境示例，地址、文件和账户均需要替换为自己的部署值。它不会创建账户、证书或密码文件。

```json
{
  "sip": {
    "listen": "0.0.0.0:5060",
    "advertise": "10.20.0.20:5060",
    "upstream": "10.20.0.10:5060",
    "trusted_networks": ["10.20.0.0/24"],
    "setup_timeout_ms": 32000,
    "ack_timeout_ms": 32000,
    "max_call_seconds": 3600,
    "trunk_auth": {
      "username": "line",
      "password_file": "/etc/rustswitch/trunk.password",
      "realm": "carrier.example"
    },
    "registration": {
      "aor": "sip:line@carrier.example",
      "registrar_uri": "sip:carrier.example",
      "expires_seconds": 300,
      "retry_seconds": 30,
      "timeout_ms": 8000
    }
  },
  "media": {
    "binary": "./bin/rustswitch-media",
    "bind_ip": "0.0.0.0",
    "advertise_ip": "10.20.0.20",
    "port_start": 20000,
    "port_end": 21999,
    "workers": 2,
    "receive_buffer_bytes": 65536,
    "port_reuse_delay_ms": 2000,
    "max_packets_per_second_per_leg": 200,
    "allowed_remote_networks": ["10.20.0.0/24"],
    "cpu_cores": [],
    "adaptive_admission": true,
    "admission_cpu_high": 0.85,
    "admission_cpu_low": 0.65
  },
  "limits": {
    "max_calls": 100,
    "calls_per_second": 20,
    "burst_calls": 40,
    "max_transactions": 10000
  },
  "admin": {"listen": "127.0.0.1:9080"},
  "journal": {"path": "run/events.jsonl", "queue_capacity": 4096}
}
```

密码文件只放一份真实线路密码，可带末尾 LF 或 CRLF。密码有效内容为1～4096字节，不含控制字符；内部空格和末尾空格不被修剪。文件可使用 `0600` 或 `0400` 权限。配置 JSON、管理状态和事件日志只保留路径／固定状态类别，不写密码、nonce、Authorization 或派生摘要。停机关闭后会尽力清零持有的密码缓冲。

改为 TCP 时，在 `sip` 增加：

```json
"stream": {
  "upstream_transport": "tcp",
  "tcp_listen": "0.0.0.0:5060",
  "max_connections": 256,
  "frame_timeout_ms": 5000,
  "idle_timeout_ms": 300000,
  "write_timeout_ms": 1000
}
```

改为 TLS 时，替换上述 `stream`，并将 `sip.upstream` 改为对端实际 TLS 地址／端口：

```json
"stream": {
  "upstream_transport": "tls",
  "tls_listen": "0.0.0.0:5061",
  "tls_cert_file": "/etc/rustswitch/server-chain.pem",
  "tls_key_file": "/etc/rustswitch/server-key.pem",
  "tls_ca_file": "/etc/rustswitch/carrier-ca.pem",
  "tls_server_name": "sip.carrier.example",
  "max_connections": 256,
  "frame_timeout_ms": 5000,
  "idle_timeout_ms": 300000,
  "write_timeout_ms": 1000
}
```

TLS 必须验证证书链和名称，不提供跳过验证开关。以上仅加密 SIP 信令，不代表已提供 SRTP 或 WebRTC。Digest 的 MD5 系列用于已有中继互通；支持 SHA-256／SHA-512-256 的上游可按其挑战协商这些算法。

## 注册、刷新与注销

启动接收器后发送 REGISTER，整个注册生命周期保持同一 Call-ID 和 From tag。认证、有效期调整、刷新、注销使用递增 CSeq 和新 Via branch；同事务重传复用完整原报文。Contact 使用 AOR 的用户部分、本机宣告 IP 与实际传输监听端口。

只有真实上游来源、同一可靠连接代次、Call-ID、branch、CSeq、From tag 与注册 AOR 均匹配的响应，才可推进状态。200 类响应还必须确认本 Contact 的有效期，不能把账户其他绑定的有效期误认为本机注册成功。响应中重复或无效的 expires 会失败。请求有效期可以被注册器缩短；刷新按扣除网络往返时间后剩余有效期的80%安排。

有效的423 `Min-Expires` 可在单轮内调高请求值一次，最多86400秒。401/407 最多接受四次挑战，受整轮固定期限限制。错误密码、无效挑战、其他错误响应和超时进入有界退避；合法 `Retry-After` 可延长等待，但最长3600秒。可靠连接关闭后清除本机可达状态并退避重注册；不会把旧事务迁移到新的连接代次。

正常停止时，在排空已有通话的同时发送本 Contact 的 `expires=0` 注销，最多等待三秒，认证重试不延长该期限。即使当前没有通话，也会等待注销结果或期限结束。强制取消进程上下文不保证可以完成网络注销；注销失败必须保留失败状态，不能写成成功。

UDP 使用500毫秒开始、最大4秒间隔的有界非 INVITE 重传；TCP/TLS 不重传请求。注册状态只有一个操作对象和最多三个可替换定时器，不随呼叫量创建注册协程。

## 出站 INVITE 认证

支持401 `WWW-Authenticate` 与407 `Proxy-Authenticate`，分别生成 Authorization 与 Proxy-Authorization。支持 MD5、MD5-sess、SHA-256、SHA-256-sess、SHA-512-256、SHA-512-256-sess；支持 `qop=auth` 和已有设备使用的无 qop 形式。不支持 auth-int、AKA、userhash 或其他字符集模式，不会悄悄回退为错误算法。

收到有效挑战后，先用旧 INVITE 的 branch／CSeq 发送 ACK，再保持 Call-ID、From 和原 SDP，递增 CSeq、生成新 branch 重试。两类挑战最多各持有一个状态，单通电话最多四次认证重试。已认证后只有带新 nonce 的 `stale=true` 才允许替换同类别挑战。旧挑战重传只重发已保存的 ACK；不会再次拨号。原呼叫建立超时不会因认证被延长，取消或已经结束的电话不会因挑战恢复拨号。

成功 ACK 原样携带该 INVITE 的凭据。后续 CANCEL 使用当前 INVITE 序号；正常 BYE 使用递增序号，并为已有认证上下文计算 BYE 的新摘要。对话内 BYE 等请求收到新认证挑战后的重试尚未实现。当前 B 腿 From 仍是原有 RustSwitch 本机 Contact，尚未实现全部 gateway `from-user`／`from-domain`／主叫身份策略；要求这些身份映射的运营商需另行适配和验收。

## 管理状态

`GET /v1/status` 的 `sip_registration` 返回只读原子快照；它不是原版 `sofia status` 的完整输出：

```json
{
  "enabled": true,
  "state": "registered",
  "last_status": 200,
  "expires_at": "2026-09-06T12:00:00Z"
}
```

| state | 含义 |
|---|---|
| `disabled` | 未配置注册对象 |
| `registering` | 注册、认证、刷新或注销请求正在等待结果 |
| `registered` | 本 Contact 已得到有限有效期的确认；不代表呼叫或媒体已验证 |
| `retrying` | 本轮失败，等待退避重试；`error` 为固定错误类别 |
| `unregistered` | 已得到注销成功的响应 |
| `unregister_failed` | 注销拒绝、认证失败、断线或超时；不能视为对端绑定已删除 |

`expires_at` 是仍有效的最近绑定期限；没有确认绑定或上次发布快照时已到期则省略；读取不重新结算期限，客户端须与当前时间比较。失败时可能仍保留此前绑定的剩余期限，状态仍为失败。`last_status` 不存在时省略，`error` 不包含对端提供的任意正文或密码。

## 验收与边界

本地自动化已经通过 UDP、TCP、TLS 三条真实线缆场景：REGISTER 401→407、有效期刷新、INVITE 407 认证、成功 ACK、64 个双向 Rust RTP 包、认证 BYE 和正常停机注销。摘要测试使用 RFC 公布向量及独立预计算向量，检查旧式无 qop 和 sess 算法。

主要回归入口：

- `TestTrunkRegistrationAndInviteRealRustMedia/{udp,tcp,tls}`：真实传输与 Rust 媒体闭环；缺少真实媒体程序时明确跳过，不能计为通过。
- `TestRegistrationRejectsForeignResponsesAndLoops`：错来源／连接／事务／AOR、重复挑战、错误密码与状态保密。
- `TestRegistration423ExpiryTimeoutAndShutdown`、`TestRegistrationShutdownTimeoutAndOldTimers`、`TestRegistrationContactExpiryValidation`：有效期、超时、注销失败和旧计时器隔离。
- `TestRegistrationStaleChallengeBound`：恶意 stale 循环的上限。
- `TestInviteDigestRetriesPreserveACKAndCancellation`、`TestInviteAuthenticationInvalidChallengesFailClosed`：认证事务与取消、恶意挑战。
- `TestTrunkSecretFilesAndConfigCopies`、`TestTrunkConfigurationBounds`：私有文件、管理深拷贝与静态配置边界。
- `TestDigestPublishedVectors`、`TestDigestLegacyAndSessionVectors`、`TestDigestRejectsAmbiguousChallenges`：摘要及解析器边界。

本页面的本地回归不替代原版成对证据，也不代表5000／10000路、全部运营商、全部 SIP 请求或全模块兼容通过。仍缺少多个 gateway／registrar、终端注册服务、完整 DNS/NAPTR/SRV 和 Route/Record-Route、Service-Route、SIP Outbound/Path、Authentication-Info nextnonce、复杂 Contact 语义、REGISTER 重定向、双向 TLS 客户端认证、WS/WSS、注册状态跨进程接管等能力。

注册成功不会自行创建本地 AI 应用。接收注册 Contact 的呼入时，应结合显式本地分机／应用路由配置；未配置本地路由的呼叫仍沿既有固定上游 B2BUA 路径处理。隔离压测实例始终清除注册与线路认证配置，不能使用主服务凭据注册真实线路。

参考依据：[RFC 3261 注册与认证](https://www.rfc-editor.org/rfc/rfc3261.html)、[RFC 8760 SIP Digest 算法扩展](https://www.rfc-editor.org/rfc/rfc8760.html)、[RFC 7616 摘要计算与公开向量](https://www.rfc-editor.org/rfc/rfc7616.html)。本地对照源固定为 FreeSWITCH 1.11.3 提交 `ef32e205295e29f034f1453ad245ba5efb07b94a` 的 `sofia_reg.c` 注册生命周期与 `sofia.c` 认证分派；参考源码不能代替运行验收。
