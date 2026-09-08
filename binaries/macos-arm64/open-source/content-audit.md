# 公开内容独立审查报告

审查日期：2026-09-08。审查对象是当前完整项目资料 ZIP 解压后的 4,899 个文件（724,601,707 字节）。本次只读审查没有修改原交付包，也没有上传任何内容。

## 结论

未发现符合规则的私钥、GitHub/AWS/provider token、JWT 或带用户名密码的 HTTP URL。密码赋值命中均位于测试固定值或 FreeSWITCH 原版的注释配置示例。公开前应处理个人构建路径和一个历史测试内网地址；其他明确标识的第三方作者署名、示例地址和测试固定值应保留。

这是针对现有内容的静态审查结果，不是对所有秘密形式的穷尽证明。扫描没有网络验证任何凭据。

## 扫描范围与复核

- 4,858 个文本文件、41 个二进制/压缩文件。
- 对所有文件扫描高置信秘密模式与个人绝对路径。首次全量扫描还扫描了二进制内嵌文本中的赋值、URL 与邮箱；疑似密码赋值来自内嵌原版模板。
- 对 10 个 `.json.zlib` / `.jsonl.gz` 测试资料进行内存解压检查，未命中本次秘密、个人路径和内网地址规则。没有改写压缩 fixture。
- 对唯一截图 `source/main/docs/verification-v0.3/protection-desktop.png` 人工查看：为峰值保护页面，不含账户、电话记录或凭据。另两份为相同来源副本。
- Opus 上游压缩包及第三方 vendored 内容不应因作者邮箱、上游网址或示例路径而被修改；发布时应保留许可证和来源摘要。
- 补充检索 hostname、serialnumber、client_secret、refresh_token 等字段，没有在 evidence / candidate evidence / binaries evidence 中发现这类字段。

## 应处理项

| 类型 | 范围 | 最小处理 |
| --- | --- | --- |
| 个人本机 home 前缀 | 99 个文本文件，主要为 evidence receipt/build log 和源码中的历史文档 | 公开副本以中性绝对前缀替换个人 home；保留路径剩余部分及原测试数值/结论。 |
| 个人本机路径嵌入二进制 | 14 个可执行文件或 dylib | 不直接替换二进制字节；保留已验证原二进制并说明其含非秘密构建路径元数据，或重新编译独立去路径版本并重新检查。 |
| 历史压测内网白名单 | 下述 3 份 `10000.evidence.json`，各第 9、27 行 | 公开副本将地址替换为保留的文档地址 `192.0.2.160/32`，明确说明它是脱敏占位值。 |

历史白名单文件：

- `source/main/docs/benchmarks-v0.2/10000.evidence.json`
- `source/asr-candidate/docs/benchmarks-v0.2/10000.evidence.json`
- `reference/docs/benchmarks-v0.2/10000.evidence.json`

完整逐文件位置在 `redaction-policy.json` 和 `findings.json`；匹配值不保存明文凭据，仅记录 SHA-256、长度与位置。

14 个二进制位置：

- `binaries/linux-arm64/{main,asr-candidate}/bin/rustswitch`
- `binaries/linux-arm64/{main,asr-candidate}/bin/rustswitch-media`
- `binaries/macos-arm64/{main,asr-candidate}/bin/rustswitch`
- `binaries/macos-arm64/{main,asr-candidate}/bin/rustswitch-media`
- `binaries/macos-arm64/{main,asr-candidate}/lib/libopus.dylib`
- `binaries/macos-arm64/{main,asr-candidate}/lib/librustswitch_g711.dylib`
- `binaries/macos-arm64/{main,asr-candidate}/lib/librustswitch_g722.dylib`

## 已判断无需脱敏的命中

- `source/{main,asr-candidate}/control/internal/esl/server_test.go` 第 25、450 行：loopback 临时端口单元测试的固定密码，文件注明不使用现网凭据。
- `source/{main,asr-candidate}/tools/test_conformance.py` 第 186、209、231、382 行：mock.patch.dict 注入的测试环境密码，用于连接拒绝/超时等测试。
- `source/{main,asr-candidate}/control/internal/server/fs_templates/autoload_configs/hash.conf.xml` 第 4 行：原版 XML 注释内 Test1 示例。应保持原版模板及其许可来源，而不将其当成真实账户。
- `docs/api/trunk-registration.md` 及副本中的 `10.20.0.0/24` 等：正文已明示为需自行替换的隔离部署示例。
- FreeSWITCH 原版模板、Rust vendor 中的内网地址、作者邮箱及示例 `/home/...`：属于上游源代码/版权说明或测试数据，应保留。
- 本机 `127.0.0.1`、localhost、`.invalid` / `.example`：本地验收或保留测试域名，不是内部生产端点。

## 公开副本与证据完整性

1. 原始 ZIP 与原证据应在私有本地保留，公开材料明确标识“脱敏公开副本”。
2. 生成一份脱敏日志，记录路径、原文件哈希、公开文件哈希和修改类型。不要把旧测试哈希改成新哈希并伪称历史测试已验证了修改后内容。
3. 新公开包应生成新的 manifest 与校验值；旧证据中引用的原哈希可以作为历史记录保留，但应明确不适用于修改后的公开字节。
4. 文档门禁失败、未验证功能、历史高并发失败结论均须原样保留。
5. 本报告的工作区扫描原始 `summary.json` 包含私有本机前缀，用于审查定位，不宜不经处理直接复制为公开报告。此 Markdown 报告不包含该个人前缀。

## 输出

- `scan.py`：只读扫描脚本。
- `findings.json`：逐文件规则、位置、匹配哈希。
- `summary.json`：内部汇总（含本机前缀，仅本地使用）。
- `compressed-findings.json`：压缩测试资料扫描记录。
- `redaction-policy.json`：建议处理清单。
- `additional-field-review.json`：补充字段检索的位置记录。

