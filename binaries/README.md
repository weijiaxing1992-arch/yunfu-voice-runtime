# RustSwitch 预编译程序说明

预编译可执行文件和动态库通过 [GitHub Releases](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases/tag/v0.1.0-preview.20260908) 分发。Git 仓库的本目录保留平台说明、配置、启动脚本、来源记录和许可材料；下载仓库源码后，需要自行构建或另取对应运行包。

| 平台 | 说明 | 使用要求 |
| --- | --- | --- |
| macOS ARM64 | [平台与运行说明](macos-arm64/README.md) | Apple Silicon、macOS 26+；包含可选原生编解码动态库。 |
| Linux ARM64 | [平台与运行说明](linux-arm64/README.md) | Linux aarch64 静态核心；不含可选 G.722 / Opus 动态库。 |

每个平台的运行包分别提供主工程与 ASR 候选。选择一套程序启动，避免混用版本或占用相同默认端口。ASR 候选的模拟服务用于协议验证，未提供真实语音识别模型。

安装、完整性校验和首次运行见[快速开始](../QUICKSTART.md)。源码位置和分发包的对应关系见[交付与源码说明](../11-交付与源码说明.md)。固定发行包保留原内容；本目录说明的后续修订记录在 Git 提交中，不表示既有二进制发生变化。
