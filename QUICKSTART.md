# 快速开始

本次公开内容是云蝠 Voice Runtime 的开发预览：Rust 媒体、Go 控制面、管理后台，以及保留 C/C++ 生态的音频适配层。先使用 `main`；`asr-candidate` 是独立候选版本，包含 ASR1 接口与模拟供应商，没有真实识别模型，也没有可直接调用的公开 StartASR HTTP 接口。

## 1. 选择源码或运行包

| 入口 | 内容与适用范围 |
|---|---|
| 当前仓库的 `source/main/` | 主工程源码，包含内嵌管理后台与测试工具 |
| `source/asr-candidate/` | ASR 候选源码，与主工程分别构建、分别使用 |
| GitHub Releases 的 macOS ARM64 运行包 | macOS 26 及以上、Apple Silicon；带 G.722/Opus 可选原生库，开发构建未做 Developer ID 公证 |
| GitHub Releases 的 Linux ARM64 运行包 | Linux aarch64；核心程序为静态 ELF，未包含可选 G.722/Opus 动态库 |
| GitHub Releases 的完整资料包 | 项目说明、架构与拓扑、接口、使用说明、需求、未完成清单、源码及构建材料 |

当前没有预编译 Linux x86_64 包。Linux 主工程保留限定探针的历史运行记录；Linux ASR 候选的整组联调尚未完成。

## 2. 使用预编译运行包

下载与平台匹配的 Release 资产并解压，在包含 `verify.py`、`main/` 和 `asr-candidate/` 的目录执行：

```sh
# Python 3.9 或以上；只核对文件完整性。
python3 verify.py

cd main
chmod +x run.sh bin/*
./run.sh -check-config
```

出现 `configuration valid` 表示配置校验通过，尚未启动 SIP、HTTP 或媒体服务。要使用 ASR 候选，将 `cd main` 改为 `cd asr-candidate`。两者默认端口相同，不要同时启动。

确认本机没有服务占用示例端口后，在同一目录启动：

```sh
./run.sh
```

打开 <http://127.0.0.1:9080>。控制程序会启动自己的 Rust 媒体工作进程。首次 Ctrl+C 进入排空流程；再次中断可能强制结束存量通话。

默认配置只绑定本机：SIP `5060`、固定上游 `5070`、后台 `9080`、媒体端口 `20000–21999`，100 路并发上限、20 CPS、2 个媒体工作进程。默认没有真实 SIP 上游，须自行接入可信线路；这些默认值不是万路容量配置。后台“小电话”提供单路隔离 SIP/媒体模拟测试，不是浏览器麦克风电话。

实际线路地址、访问白名单、认证文件等配置方法见 [使用说明](06-使用说明.md)。每次修改 `config/local.json` 后先执行 `./run.sh -check-config`。后台默认仅供本机访问；远程访问应通过自己的安全运维通道，CSRF 校验不能代替账户认证。

Linux 可选 G.722/Opus 动态库需要在目标环境构建；详见 [原生音频后端](source/main/native/audio/README.md)。G.711 PCMA/PCMU 核心路径不依赖这些动态库。自建原生库可通过绝对路径的 `RUSTSWITCH_G722_LIBRARY`、`RUSTSWITCH_OPUS_LIBRARY` 环境变量指定，Linux 使用 `.so`，macOS 使用 `.dylib`。

## 3. 从源码离线编译

本机需要以下工具；源码包包含依赖源码，不包含编译器本身。

| 工具 | 最低要求与说明 |
|---|---|
| Python | 3.9，交付构建与校验脚本使用 `Path.is_relative_to` |
| Go | 1.23，依赖固定在 `control/vendor/` |
| Rust 与 Cargo | 1.85，项目与固定的 clap 依赖均声明此最低版本 |
| C 编译器 | 支持 C11，以及本平台动态库链接；macOS 需开发工具与系统 SDK，Linux 需对应开发环境 |
| 可选原生音频后端 | 另外需要 `make`；Opus 官方归档已在 `dependencies/` 中提供 |

以上最低版本来自源码声明与脚本用法，尚未逐个最低版本重建。现有离线构建记录使用 Go 1.27.1 和 Rust 1.98.1。只支持在匹配的类 Unix 目标环境按本脚本本机构建；Windows 不在当前运行包范围内。

在仓库根目录执行，输出目录必须不存在，并且必须位于仓库目录之外：

```sh
python3 delivery-tools/build_source.py \
  --variant main \
  --output ../voice-runtime-build-main
```

候选版本使用 `--variant asr-candidate` 并指定另一个新的输出目录。脚本禁止在线依赖下载，不启动服务；各步骤日志与结果保存到输出目录，最后查看 `receipt.json` 中的 `passed` 与 `source_unchanged`。

构建会生成控制程序、压测发生器、Rust 媒体和 G.711 C ABI 示例插件。Rust 媒体位于 `cargo-target/release/rustswitch-media`；ASR 候选还会生成 `asr-mock`。G.722/Opus 是额外的可选原生后端，未包含在此基础构建步骤中。

要使用这次主工程构建，可在仓库根目录组装独立运行目录：

```sh
mkdir -p ../voice-runtime-local/bin ../voice-runtime-local/config
cp ../voice-runtime-build-main/rustswitch ../voice-runtime-local/bin/
cp ../voice-runtime-build-main/callbench ../voice-runtime-local/bin/
cp ../voice-runtime-build-main/cargo-target/release/rustswitch-media ../voice-runtime-local/bin/
cp source/main/config/local.json ../voice-runtime-local/config/
cd ../voice-runtime-local
./bin/rustswitch -config config/local.json -check-config
```

配置通过后执行 `./bin/rustswitch -config config/local.json` 启动。配置中的相对媒体与日志路径按当前工作目录解析，因此应始终从这个运行目录启动。

## 4. 理解验收状态

基础编译、配置校验、文件哈希校验分别验证不同范围。离线交付构建复用现有内嵌文档，不等于重新通过项目发布门禁。

当前两份源码的文档发布门禁仍有阻塞，保留原始记录：

- 主工程：`绿色已失效：key-api-pcm-stream`。
- ASR 候选：`字段字典落后于机器合同`。

这是门禁遇到的首个错误，不代表仅有这两项待办。`make all` 会进入文档构建步骤，当前不能据此宣布发布验收通过。应按 [未完成清单](13-项目未完成清单.md) 修复与重新验收。

项目尚未证明完全兼容 FreeSWITCH，也没有通过 5000/10000 路完整容量验收。对照表中的历史通过记录只覆盖注明的有限断言。真实 ASR/TTS 供应商、完整 Voice Agent、活动通话高可用迁移仍需要后续工作。

继续阅读 [项目说明](01-项目说明.md)、[HTTP 接口文档](04-HTTP接口文档.md)、[协议与 SDK](05-协议与SDK文档.md)、[需求说明书](12-需求说明书.md) 和 [项目未完成清单](13-项目未完成清单.md)。
