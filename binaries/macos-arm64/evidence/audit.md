# 已编译产物交付审计

只读核对现成文件、Mach-O/ELF 元数据与原收据；没有新编译、加载音频库、启动服务或网络测试。完整路径、二进制 SHA、`file`/`otool`/签名原始输出与来源匹配见 [audit.json](audit.json)。

## macOS ARM64

`work/project-delivery-2026-09-08/build-main-01` 与 `build-asr-candidate-01` 的 `rustswitch`、`callbench`、`cargo-target/release/rustswitch-media` 可交付；候选另有 `asr-mock`。各自原构建收据为对应目录的 `receipt.json`。两组源与已交付源副本相同，来源核验另见 `work/project-delivery-2026-09-08/delivery-source-binding.json`。

- 都是 Apple Silicon ARM64，核心加载依赖只有 Apple 系统库/Framework，没有工作区或 Homebrew 的外部 `LC_LOAD_DYLIB`。
- Go 程序声明最低 macOS **13.0**；Rust 媒体声明最低 **11.0**。核心整组可按 **macOS 13+ ARM64** 标注二进制要求；这不是对所有 macOS 版本的实机认证。
- 本次 C G.711 插件及既有 G.722/Opus 动态库均声明最低 **macOS 26.0**。如果包括这些库，应明确“可选插件需 macOS 26+”，不能以核心程序的 13.0 代替插件限制。
- 各 Mach-O 磁盘签名验证退出码 0；这不是 Apple 公证或其他机器 Gatekeeper 放行证明。
- dylib 的 `LC_ID_DYLIB` 保留原构建绝对路径，但其实际加载依赖只有系统库。现有 Rust 使用 `Library::new(path)` 显式路径加载，安装目录改变后应传入新路径；旧 install ID 本身不是需要一同复制的外部依赖。

## 可选已建音频后端

| 文件 | 当前 SHA256 |
| --- | --- |
| `work/audio-deps/librustswitch_g722.dylib` | `ccd3e3b911d291feba5cf3f128bc94ce803a68ceb40cb2577182350463bfdb39` |
| `work/audio-deps/opus/lib/libopus.0.dylib` | `39d7bce655ec29da6e0fb16b1206504ec9774d7e2d509865289a58c36762f0a0` |

两库都与 `work/voice-runtime-m1/rust-final-verification.json` 的实际测试库 SHA 相同。来源为 `native/audio/sources.json` 固定的 SpanDSP 提交 `8f1e1646bdec99eac5fd2cd92c35563f736b9b89` 与 Opus 1.5.2；保留的 10 份 SpanDSP 源/许可哈希全部匹配，Opus 官方归档也与固定 SHA 匹配。应一并保留来源和上游许可。

可作为 macOS 26+ 可选后端交付，并按新目录设置 `RUSTSWITCH_G722_LIBRARY` / `RUSTSWITCH_OPUS_LIBRARY`；Opus 可指向实际 `libopus.0.dylib`，不必依赖原目录 symlink。现存证据证明库身份及历史实际测试，不构成对这两库独立编译过程的完整可重复构建收据，也不替代当前交付整组的重新载入测试。

## Linux ARM64 完整核心组

以下三份是同批已完成产物，都是静态 ELF aarch64；不是只有 Go 的交叉编译文件：

| 文件 | SHA256 |
| --- | --- |
| `work/linux-vm/share/voice-runtime-m2-rx/rustswitch` | `fb4285ae0e35acc0e41f72fc0f87519023e2c4e38bc96d4357ef54c2868e4c99` |
| `work/linux-vm/share/voice-runtime-m2-rx/rustswitch-media` | `5868122d3119e4aeb0ce6074519dd265f8b78df906200a27a1f8466d6101861d` |
| `work/linux-vm/share/voice-runtime-m2-rx/callbench` | `12209449f2e773d4f1e6412c2505a7916a8771f056711b523907b4c23c9da556` |

同目录 `build-receipt.json` / `runtime-build.json` 记录的 529 / 758 项输入全部与当前主源码相同，原二进制 SHA 均匹配。`paired-vm-receipt.json` 证明这一组实际在 Alpine Linux ARM64 VM 运行过固定 26 个 FreeSWITCH 成对探针：21 项有限通过、2 项观察、3 项 SIP OPTIONS 差异，整体返回 2。可交付为已构建且有限实跑的主工程 Linux 核心组；不能改写为完整兼容或容量通过。不提供 Linux 原生 G.722/Opus 库，也不将 macOS dylib 混入 Linux 包。

## Linux ASR 候选组的准确标签

- 控制面：`work/voice-runtime-m2-asr/full-review-01/rustswitch-linux-arm64`，SHA `9d89b063054aa9e7562b5fa65e06c274bdebe573ba39e0c45cec090bdbd9e0cc`。`full-review-01/receipt.json` 的 896 源文件仍与候选相同，Linux 构建退出码 0；本文件是 `CGO_ENABLED=0` 的静态 ARM64 ELF。
- 模拟进程：`work/voice-runtime-m2-asr/mock-build-04/asr-mock-linux-arm64`，SHA `fd2ca585d2bbb00c13010a00b8fa12d44f2d8d788192ea77da4cd23da030e54a`。同目录 `receipt.json` 绑定 340 源文件及二进制，也是静态 ARM64 ELF。
- Rust 媒体、callbench 可复用上表 Linux 产物：原收据的 39 个媒体输入及发生器源码与 ASR 候选一致，不能把 macOS 媒体混配进去。

这构成**组件均已构建、源码逐组件匹配的 Linux ASR 候选组**。尚无该整组的 Linux SIP→Rust→ASR 实际联调，已通过的 ASR 真实链路测试发生在 macOS；不得套用主工程的 Linux 26 探针结果给 ASR 整组授予通过。未提供或宣称 Linux x86_64 完整运行包。
