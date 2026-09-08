# 第三方组件、版权与源码说明

本清单按实际随包文件整理。第三方内容保留原始版权、许可证、专利声明及版本；根目录 Apache-2.0 不覆盖这些内容。机器清单见 [third-party-inventory.json](third-party-inventory.json)。文中的本地链接相对完整源码／资料包根目录；独立运行包请从同次 GitHub Release 下载对应源码附件。本清单覆盖随包依赖及已识别的运行时组件，不把构建工具或上游完整工程中的所有模块都称为本项目依赖。

## 主要组件

| 组件 | 固定版本／来源 | 本次使用范围 | 许可与保留内容 |
| --- | --- | --- | --- |
| FreeSWITCH | 1.11.3，`ef32e205295e29f034f1453ad245ba5efb07b94a` | 189 份 vanilla XML 模板、源声明与兼容对照元数据；没有分发完整原版程序 | MPL-1.1，保留 `LICENSE.freeswitch`、`SOURCE.json`、模板源码与原始版权 |
| SpanDSP | `8f1e1646bdec99eac5fd2cd92c35563f736b9b89` | G.722 C 实现与必要头文件子集，共 10 个保留文件（含 COPYING） | LGPL-2.1-only，具体文件中的原声明优先；算法源码未修改 |
| Opus | 1.5.2 | 固定源码归档；macOS 运行包附可选动态库 | BSD-3-Clause；保留上游 COPYING 原文及 Xiph.Org、Microsoft、Broadcom 专利许可链接 |
| golang.org/x/sys | v0.30.0 | Go 控制面 UNIX 系统调用支持，源码 vendor | BSD-3-Clause 与 PATENTS |
| Rust crates | 下表 39 项 | Cargo.lock 对应依赖源码；含目标平台／构建依赖 | 各包许可全文原样保留；不能用“全部 Apache”替代 |
| Go runtime／标准库 | 本次构建工具链 Go 1.27.1 | 编译 Go 程序中实际链接的运行时代码 | Go BSD 许可、PATENTS，另附标准库内 vendor 的声明 |
| Rust 标准库 | 本次构建工具链 Rust 1.98.1 | 编译 Rust 程序中的标准库与底层实现 | 上游 `COPYRIGHT-library.html`，并保留工具链版权汇总作为补充 |
| musl | Linux `aarch64-unknown-linux-musl` 静态媒体目标 | Linux Rust 程序使用的 C 运行时 | 上游完整 COPYRIGHT，含 MIT 与文件级来源声明；没有据此声称已识别 musl 的精确构建版本 |

FreeSWITCH 原始模板的 189 个 SHA256 与随包 `SOURCE.json` 全部匹配。源码模板未修改；管理程序在内存中将 5 个仅含 ASCII 字符的 XML 的 Windows-1252 声明转换为 UTF-8 后编辑或导出。文件为 `lang/pt/demo/demo-ivr-pt-PT.xml`、`lang/pt/demo/demo-ivr-pt-BR.xml`、`lang/es/demo/demo-ivr-es-MX.xml`、`lang/es/demo/demo-ivr-es-ES.xml`、`lang/en/demo/new-demo-ivr.xml`。用户继续编辑导出模板时，应保留 MPL 许可并记录自己的修改。

FreeSWITCH 模板源码位于 [source/main/control/internal/server/fs_templates](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/source/main/control/internal/server/fs_templates)，候选版本相同目录也保留对应文件。其版权与出处为上游 Anthony Minessale II 和相应贡献者，详见原始许可。MPL 覆盖源码随源码发布包和仓库提供，编译包使用者可从同次发布下载对应源码；今后的重新分发也须继续满足源码可获得性及修改说明要求。[MPL 1.1 原文](https://www.mozilla.org/en-US/MPL/1.1/)。

SpanDSP 子集位于 [source/main/native/vendor/spandsp](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/source/main/native/vendor/spandsp)，版权原文包括 Steve Underwood 与 `fast_convert.h` 中的 Erik de Castro Lopo。10 个保留文件与 [native/audio/sources.json](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/blob/v0.1.0-preview.20260908/source/main/native/audio/sources.json) 的 SHA256 全部匹配。LGPL 库源码、`g722_support.c`、`build_codecs.py` 同时提供；macOS 包另有 `native-source/`，可用相同源重新构建并替换动态库，库路径由 `RUSTSWITCH_G722_LIBRARY` 选择。使用者为调试对该库的修改进行反向工程不受项目额外禁止。G.722 的独立库适用 LGPL-2.1，外部程序保留自己的许可；未来改变链接方式时须重新检查相应义务。[LGPL 2.1 原文](https://www.gnu.org/licenses/old-licenses/lgpl-2.1.html)。

Opus 归档 SHA256 为 `65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1`。归档中的 COPYING 与随包 `OPUS-COPYING` 一致。软件许可与相关专利声明同时保留；本项目不扩大上游专利授权范围。[Opus 官方许可说明](https://opus-codec.org/license/)。

## Rust 锁定依赖

下表从每个 vendor 包的 Cargo.toml 读取；完整许可和版权以包内文件为准。`unicode-ident` 的 Unicode-3.0 是额外条件；`crossbeam-channel` 还保留 `LICENSE-THIRD-PARTY`；这些不能在选择 MIT 或 Apache 时删除。`wasi` 的许可选择原样呈现，不强行归并。

| 组件 | 固定版本 | 上游许可表达式 | 随包源码 |
| --- | --- | --- | --- |
| anstream | 1.0.0 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/anstream](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/anstream) |
| anstyle | 1.0.14 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/anstyle](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/anstyle) |
| anstyle-parse | 1.0.0 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/anstyle-parse](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/anstyle-parse) |
| anstyle-query | 1.1.5 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/anstyle-query](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/anstyle-query) |
| anstyle-wincon | 3.0.11 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/anstyle-wincon](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/anstyle-wincon) |
| anyhow | 1.0.104 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/anyhow](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/anyhow) |
| cfg-if | 1.0.4 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/cfg-if](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/cfg-if) |
| clap | 4.6.6 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/clap](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/clap) |
| clap_builder | 4.6.6 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/clap_builder](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/clap_builder) |
| clap_derive | 4.6.4 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/clap_derive](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/clap_derive) |
| clap_lex | 1.1.0 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/clap_lex](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/clap_lex) |
| colorchoice | 1.0.5 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/colorchoice](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/colorchoice) |
| crossbeam-channel | 0.5.16 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/crossbeam-channel](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/crossbeam-channel) |
| crossbeam-utils | 0.8.22 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/crossbeam-utils](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/crossbeam-utils) |
| heck | 0.5.0 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/heck](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/heck) |
| ipnet | 2.12.1 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/ipnet](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/ipnet) |
| is_terminal_polyfill | 1.70.2 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/is_terminal_polyfill](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/is_terminal_polyfill) |
| itoa | 1.0.18 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/itoa](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/itoa) |
| libc | 0.2.189 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/libc](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/libc) |
| libloading | 0.8.9 | `ISC` | [dependencies/rust-vendor/libloading](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/libloading) |
| log | 0.4.34 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/log](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/log) |
| memchr | 2.8.3 | `Unlicense OR MIT` | [dependencies/rust-vendor/memchr](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/memchr) |
| mio | 1.2.3 | `MIT` | [dependencies/rust-vendor/mio](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/mio) |
| once_cell_polyfill | 1.70.2 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/once_cell_polyfill](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/once_cell_polyfill) |
| proc-macro2 | 1.0.107 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/proc-macro2](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/proc-macro2) |
| quote | 1.0.47 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/quote](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/quote) |
| serde | 1.0.229 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/serde](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/serde) |
| serde_core | 1.0.229 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/serde_core](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/serde_core) |
| serde_derive | 1.0.229 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/serde_derive](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/serde_derive) |
| serde_json | 1.0.151 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/serde_json](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/serde_json) |
| socket2 | 0.6.5 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/socket2](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/socket2) |
| strsim | 0.11.1 | `MIT` | [dependencies/rust-vendor/strsim](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/strsim) |
| syn | 3.0.5 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/syn](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/syn) |
| unicode-ident | 1.0.24 | `(MIT OR Apache-2.0) AND Unicode-3.0` | [dependencies/rust-vendor/unicode-ident](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/unicode-ident) |
| utf8parse | 0.2.2 | `Apache-2.0 OR MIT` | [dependencies/rust-vendor/utf8parse](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/utf8parse) |
| wasi | 0.11.1+wasi-snapshot-preview1 | `Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT` | [dependencies/rust-vendor/wasi](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/wasi) |
| windows-link | 0.2.1 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/windows-link](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/windows-link) |
| windows-sys | 0.61.2 | `MIT OR Apache-2.0` | [dependencies/rust-vendor/windows-sys](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/windows-sys) |
| zmij | 1.0.23 | `MIT` | [dependencies/rust-vendor/zmij](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/tree/v0.1.0-preview.20260908/dependencies/rust-vendor/zmij) |

## 分发配套

1. 源码包保留全部上游许可证、版权和来源哈希；不删除 vendor 包内的 NOTICE、PATENTS、COPYING 或附加条款。
2. 运行包带 `licenses/`、`runtime-notices/`、本说明及源码获取说明。macOS 包同时包含 G.722 可重建子集和 Opus 对应归档；其它完整源码可从同次 GitHub Release 的源码附件取得。
3. 对 LGPL 库或 MPL 覆盖文件的修改继续遵守原许可，并保留修改说明。Apache 原创代码的修改与再分发遵守其第 4 条和随附 NOTICE。[Apache 2.0 原文](https://www.apache.org/licenses/LICENSE-2.0)。
4. 仓库中的 FreeSWITCH 对照表不是模块许可证汇总；某个 codec 或协议只在规划／对照表出现，并不表示该实现、许可或专利权已随本项目交付。
5. 第三方名称仅用于来源标注，不能据此声称上游对本产品提供支持、背书或兼容认证。
