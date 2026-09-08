# RustSwitch 安全政策

本文件说明当前支持状态、安全报告流程和开发预览的部署边界，适用于 RustSwitch / 云蝠 Voice Runtime 的源码、运行包和管理接口。普通功能缺陷请使用 [GitHub Issues](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues)；可能影响凭据、隐私、访问控制、媒体完整性或服务可用性的漏洞请按下述私密流程报告。

## 当前支持状态

| 范围 | 状态 |
|---|---|
| 当前主工程与公开开发预览 | 接受安全问题报告；尚无生产安全认证或固定修复时限 |
| `source/asr-candidate/` | 独立候选，可报告问题；未合并，不能沿用主工程结论 |
| 历史源码、证据与旧资料 | 用于追溯，不构成长期维护或安全更新承诺 |
| 第三方依赖 | 保留上游许可与版本；影响本项目的集成问题可在报告中说明 |

项目尚未公布 LTS 分支、漏洞赏金计划、响应 SLA 或长期支持周期。最新公开交付见 [Releases](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases)，变化记录见 [CHANGELOG](CHANGELOG.md)。

## 私密报告

优先使用仓库的 [GitHub 私密漏洞报告入口](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/security/advisories/new)。如果该入口对你的账号不可用，请创建一个**不含漏洞细节**的联系请求，请维护者安排私下报告方式；在获得可用私密渠道之前，不要把利用方法、敏感日志、凭据或录音放入公开 Issue。

建议报告包含：

- 受影响的 Release 标签或提交、主工程/候选、二进制与原生库来源。
- 操作系统、架构和相关配置，攻击者需要具备的网络位置或权限。
- 涉及的入口，例如 SIP、ESL、HTTP、RTP、媒体 IPC、PCM/RX 或原生适配库。
- 最小复现、预期与实际行为、影响范围及复现频率。
- 脱敏的日志、协议记录或测试样例，以及临时缓解办法（如有）。

只在自己有权测试的环境复现。使用合成号码、账号、音频和测试凭据；不要测试无授权的线路、客户系统或第三方公共服务，也不要提交真实用户数据作为证明。

## 协调处理与披露

维护者会依据可复现性、影响和现有资源评估报告，可能请求补充环境或最小样例。接受报告并不代表已确认漏洞、承诺严重级别或承诺修复日期。

修复方案应说明受影响范围、变更与回归依据、可用的缓解方式，以及实际包含修复的提交或发布版本。安全敏感讨论在适合公开前保持私密；建议报告者与维护者协调披露时间，避免在修复或缓解措施准备前公开可直接利用的细节。署名与致谢应征得报告者同意。

如果凭据已经公开，应先撤销或轮换凭据，再处理删除、脱敏与影响核查。删除 GitHub 评论或修改文件不能证明所有副本和 Git 历史已消失。

## 当前部署边界

当前预览面向受控开发和验收环境。以下边界来自现有实现，完整说明见[快速开始](QUICKSTART.md)、[运维文档](07-运维与压测说明.md)和[未完成清单](13-项目未完成清单.md)：

- 管理后台默认只绑定回环地址；CSRF 校验用于写请求保护，不能替代账户认证和授权。需要远程管理时，使用自己的安全运维通道，不直接把本机入口暴露到公网。
- SIP/IP 白名单、来源校验、准入限制和媒体包预算不等同于完整公网 SBC、防攻击服务或多租户隔离。
- SIP TLS 的存在不代表媒体已加密；SRTP、WebRTC/ICE 和更完整的安全媒体能力仍有缺口。
- 外部原生库与模型适配应来自可信来源，并遵循各自部署、更新和许可要求。ASR mock 不是生产识别服务。
- 媒体进程或控制面故障可能中断相关通话；当前不承诺活动通话无损接管、零丢包或日志零损失。
- 网络报文、日志和音频都可能包含敏感信息。测试报告应遵循最小收集和脱敏原则，保留自己环境中的必要访问控制。

这些说明不是安全审计证书。实际部署方需要结合业务暴露面、网络、依赖版本和自己的数据要求完成评估。

## Security reporting in English

RustSwitch is a development preview intended for controlled development and acceptance environments. There is no published LTS policy, response SLA, bounty program, or production security certification. Main-project and separately published ASR-candidate issues should be identified separately.

Use [GitHub private vulnerability reporting](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/security/advisories/new). If that entry is unavailable, open a contact request **without vulnerability details** and ask for a private reporting channel. Do not publish exploits, credentials, customer identifiers, call recordings, or sensitive logs in public issues.

Provide the affected tag or commit, source variant, platform, entry point, required privileges, a minimal sanitized reproducer, and expected versus observed behavior. Test only systems you are authorized to assess. Coordinate disclosure with maintainers; acceptance of a report does not promise a severity rating or remediation date.

The default administration listener is loopback-only; CSRF is not authentication. SIP TLS does not imply encrypted media. Full SRTP/WebRTC support, broader authorization and isolation, and preservation of active calls across failures remain unfinished. See the [current scope](README_EN.md) and [remaining-work register](13-项目未完成清单.md) before deployment.
