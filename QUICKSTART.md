# RustSwitch 快速开始

本指南帮助你完成下载、文件校验、配置校验、启动和首次单路模拟测试。RustSwitch（历史名称“云蝠 Voice Runtime”）当前为开发预览：Rust 媒体、Go 控制面、内嵌管理后台及 C/C++ 编解码适配已经随工程提供，完整生产验收仍在推进。

首次使用选择 `main`。`asr-candidate` 是已公开的独立候选快照，包含 ASR1 接口与模拟供应商；没有真实识别模型，也没有可直接调用的公开 StartASR HTTP 接口。下面的启动和测试命令由操作者在自己的实验环境执行，会创建进程、占用端口并写入运行日志。

## 1. 选择源码或运行包

| 入口 | 内容与适用范围 |
|---|---|
| 当前仓库的 `source/main/` | 主工程源码，包含内嵌管理后台与测试工具 |
| `source/asr-candidate/` | ASR 候选源码，与主工程分别构建、分别使用 |
| [Releases](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases) 的 macOS ARM64 运行包 | macOS 26 及以上、Apple Silicon；带 G.722/Opus 可选原生库，开发构建未做 Developer ID 公证 |
| Releases 的 Linux ARM64 运行包 | Linux aarch64；核心程序为静态 ELF，未包含可选 G.722/Opus 动态库 |
| GitHub Releases 的完整资料包 | 项目说明、架构与拓扑、接口、使用说明、需求、未完成清单、源码及构建材料 |

当前没有预编译 Linux x86_64 包。Linux 主工程保留限定探针的历史运行记录；Linux ASR 候选的整组联调尚未完成。

固定预览发行版为 [v0.1.0-preview.20260908](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases/tag/v0.1.0-preview.20260908)。需要复现某次结果时使用其固定标签或明确提交，记录所用资产名与 SHA-256，不要把不同版本的控制程序、媒体程序和配置混用。

## 2. 使用预编译运行包

下载与平台匹配的 Release 资产并解压，在包含 `verify.py`、`main/` 和 `asr-candidate/` 的目录执行：

```sh
# Python 3.9 或以上；只核对文件完整性。
python3 verify.py

cd main
chmod +x run.sh bin/*
./run.sh -check-config
```

出现 `configuration valid` 表示配置校验通过，尚未启动 SIP、HTTP 或媒体服务。此检查不保证真实端口可绑定、线路可注册或媒体可达。若同一日志路径下已经有保存的管理状态，校验也会读取它的待启动配置；首次试用应使用独立运行目录。要使用 ASR 候选，将 `cd main` 改为 `cd asr-candidate`。两者默认端口相同，不要同时启动。

确认本机没有服务占用示例端口后，在同一目录启动：

```sh
./run.sh
```

打开 <http://127.0.0.1:9080>。控制程序会启动自己的 Rust 媒体工作进程。另开终端可只读检查：

```sh
curl --fail --silent --show-error http://127.0.0.1:9080/healthz
curl --fail --silent --show-error http://127.0.0.1:9080/v1/docs
```

前者检查进程响应，后者查询内嵌文档版本；两者均不是通话或容量验收。首次 Ctrl+C 进入排空流程，再次中断会强制取消尚未完成的工作。

默认配置只绑定本机：SIP `5060`、固定上游 `5070`、后台 `9080`、媒体端口 `20000–21999`，100 路并发上限、20 CPS、2 个媒体工作进程。默认没有真实 SIP 上游，须自行接入可信线路；这些默认值不是万路容量配置。后台“小电话”提供单路隔离 SIP/媒体模拟测试，不是浏览器麦克风电话。

在「压力与电话测试」页先运行单路检查，分别查看任务状态、实际建立、双向媒体结果及挂断清理。模拟测试成功表示该隔离场景通过，不代表真实运营商线路已接通。较大并发档位会额外消耗本机端口、CPU 和内存，应在读完 [压力测试说明](07-运维与压测说明.md) 后执行。

实际线路地址、访问白名单、认证文件等配置方法见 [使用说明](06-使用说明.md)。每次修改 `config/local.json` 后先执行 `./run.sh -check-config`。本版本要求管理监听为回环地址；远程访问使用自己的 SSH 运维通道，CSRF 校验不能代替账户认证。

Linux 可选 G.722/Opus 动态库需要在目标环境构建；详见 [原生音频后端](source/main/native/audio/README.md)。G.711 PCMA/PCMU 核心路径不依赖这些动态库。可选后端用于独立编解码探测和离线 SDK，不表示已经支持所有实时通话路径的 G.722/Opus 转码。

自建库使用绝对路径的 `RUSTSWITCH_G722_LIBRARY`、`RUSTSWITCH_OPUS_LIBRARY` 环境变量，Linux 扩展名为 `.so`，macOS 为 `.dylib`。随包 `run.sh` 发现自带 G.722 库时会同时设置这两个变量；需要使用外部指定库时，应在运行目录设置变量后直接执行 `./bin/rustswitch -config config/local.json`，避免启动脚本覆盖路径。

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

尚未取得源码时，可先下载完整源码包，或检出仓库的固定预览标签：

```sh
git clone --branch v0.1.0-preview.20260908 \
  https://github.com/weijiaxing1992-arch/yunfu-voice-runtime.git
cd yunfu-voice-runtime
python3 delivery-tools/verify_delivery.py
```

如果已解压源码或已位于仓库，不要重复克隆，直接在包含 `source/`、`dependencies/` 和 `delivery-tools/` 的根目录校验。文件校验针对发行快照；主动修改文件后出现哈希差异时，应记录改动并使用开发流程验证，不能把清单错误当成编译器问题。

在仓库根目录执行构建，输出目录必须不存在，并且必须位于仓库目录之外：

```sh
python3 delivery-tools/build_source.py \
  --variant main \
  --output ../voice-runtime-build-main
```

候选版本使用 `--variant asr-candidate` 并指定另一个新的输出目录。脚本禁止在线依赖下载，不启动服务；各步骤日志与结果保存到输出目录，最后查看 `receipt.json` 中的 `passed` 与 `source_unchanged`。

构建会生成控制程序、压测发生器、Rust 媒体和 G.711 C ABI 示例插件。控制程序及 `callbench` 位于输出根目录，Rust 媒体位于 `cargo-target/release/rustswitch-media`；ASR 候选还会生成 `asr-mock`。G.722/Opus 是额外的可选原生后端，未包含在此基础构建步骤中。

要使用这次主工程构建，可在仓库根目录组装新的独立运行目录。下列目录应为本次试用专用；不要将文件直接覆盖到运行中的安装目录：

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

## 4. 常见首次运行问题

| 现象 | 检查方式 |
|---|---|
| 文件校验失败 | 确认完整解压、资产与平台正确，检查是否改过被清单覆盖的文件 |
| 平台不匹配或无法执行 | ARM64 包不能在 x86_64 使用；macOS 与 Linux 产物不能互换 |
| 输出目录已存在 | 给构建脚本指定新的仓库外目录，保留原日志以便比较 |
| 配置校验通过但启动失败 | 查启动日志中的媒体路径、端口绑定、文件目录和服务用户权限 |
| 后台能打开但真实线路无呼叫 | 默认只提供回环示例上游，需接入可信 SIP 对端并配置白名单 |
| 原生音频自检失败 | 检查可选库路径与架构；缺库不等于 G.711 核心路径不可用 |
| 实际配置与 JSON 不同 | 查看 `/v1/config` 的 `active`、`desired` 和管理状态文件，不要直接删除运行状态来试错 |

## 5. 理解验收状态

基础编译、配置校验、文件哈希校验分别验证不同范围。离线交付构建复用现有内嵌文档，不等于重新通过项目发布门禁。

当前两份源码的文档发布门禁仍有阻塞，保留原始记录：

- 主工程：`绿色已失效：key-api-pcm-stream`。
- ASR 候选：`字段字典落后于机器合同`。

这是门禁遇到的首个错误，不代表仅有这两项待办。`make all` 会进入文档构建步骤，当前不能据此宣布发布验收通过。应按 [未完成清单](13-项目未完成清单.md) 修复与重新验收。

项目尚未证明完全兼容 FreeSWITCH，也没有通过 5000/10000 路完整容量验收。对照表中的历史通过记录只覆盖注明的有限断言。真实 ASR/TTS 供应商、完整 Voice Agent、活动通话高可用迁移仍需要后续工作。

继续阅读 [项目说明](01-项目说明.md)、[HTTP 接口文档](04-HTTP接口文档.md)、[协议与 SDK](05-协议与SDK文档.md)、[需求说明书](12-需求说明书.md) 和 [项目未完成清单](13-项目未完成清单.md)。
