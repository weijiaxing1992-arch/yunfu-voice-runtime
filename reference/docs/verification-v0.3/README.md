# 0.3.0 实际验证记录

日期：2026-09-05。本目录保留本轮原始测试输出，结果只适用于列出的本机负载与实现范围。

- [Go 全包竞态与静态检查](go-test-race-vet.log)：最终源码通过；包含保护策略、持久化和 XML 定位加固。
- [格式、Clippy 与本机构建](format-clippy-build.log)：通过。Linux amd64 Go 全包另行交叉构建退出 0；没有执行 Linux 网络验收。
- [Rust 与 C 编解码](rust-codec.log)：6 项 Rust 测试与原生 G.711 往返通过。
- [普通 UDP 通话回归](e2e-udp.log)和[连接式 UDP 通话回归](e2e-connected.log)：各 15 项，包含在线降低并发上限后已有通话继续传音。
- [百路结果](capacity-100-report.json)与[证据](capacity-100-evidence.json)：100 路、10 秒、显式 capacity-mode，发送/唯一接收均为 99,992 包；活动资源归零，日志无丢失。清理后再次读取持久化策略确认 enabled=true，容量模式未污染下一次默认测试。
- [页面验收记录](ui-verification.json)：策略、参数/XML、业务模板、重启配置、排空/恢复、版本冲突和响应式页面。页面导出的 ZIP 共 192 条目，包含 189 份 XML 与来源许可；完整性检查通过。
- [页面截图](protection-desktop.png)：测试实例设定一万路启动上限、八千路保护上限，截图中的容量数字是配置值。
- [构建清单](build-manifest.json)：记录实际本机二进制和最终 Go 源文件 SHA-256。

本轮没有重新进行千路或万路持续媒体容量认证，也没有通过 FreeSWITCH 全接口差分验收。原型的完整可靠性边界见[管理功能说明](../management-v0.3.md)。
