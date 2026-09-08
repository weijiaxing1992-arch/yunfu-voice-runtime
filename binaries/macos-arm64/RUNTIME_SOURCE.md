# 运行包对应源码与第三方许可

此运行包与同次 GitHub Release 的源码包配套。完整仓库为：

https://github.com/weijiaxing1992-arch/yunfu-voice-runtime

请从发布当前运行包的同一个 Release 页面下载完整源码附件，并根据随包清单选择 `source/main` 或 `source/asr-candidate`。候选与主版本分别保留，不应混用其控制程序和配置。源码包同时提供锁定 Rust 依赖、Go vendor、模板和原生库构建源码；对应关系及验证范围见各包的 evidence、manifest 和 README。

FreeSWITCH 模板为 MPL-1.1 覆盖源码，位于对应源码树的 `control/internal/server/fs_templates`，保留原始许可和 SOURCE.json。使用者可以取得、修改并按其原许可分发。程序编辑／导出时对 5 个仅含 ASCII 的旧编码 XML 声明进行 UTF-8 规范化，原始模板仍随源码保留。

macOS 包中的 SpanDSP G.722 共享库按 LGPL-2.1-only 分发；`native-source` 附对应源码、g722_support.c 和构建脚本。`g722_support.c` 在本次公开发布中也按 LGPL-2.1-only 授权。使用者可以重新编译并替换共享库；项目不限制为调试该库修改进行的反向工程。Opus 源码归档和 COPYING 一并提供。Linux 包没有附 G.722／Opus 动态库，运行范围以 README 为准。

`licenses` 保留依赖许可，`runtime-notices` 补充 Go runtime、Rust 标准库与 Linux musl 的原始归属声明。README 中的项目 Apache-2.0 只覆盖项目有权授权的原创材料，第三方许可及 G.722 支持文件例外不变。重新分发时应继续携带相关许可、版权和对应源码获取方式。
