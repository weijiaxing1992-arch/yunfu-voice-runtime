# RustSwitch 预编译运行包

本文说明已公开的 [v0.1.0-preview.20260908 运行附件](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/releases/tag/v0.1.0-preview.20260908)。当前 Git 仓库仅保留说明、配置与证据，实际可执行文件和动态库需从该 Release 下载。main 上的说明修订不替换冻结附件，也不改变其构建和历史验证身份。

适用平台：**Linux ARM64 / aarch64**。`main` 是主工程，`asr-candidate` 包含已完成ASR候选代码；选择一个目录运行，二者默认使用相同端口。

## 直接运行

```sh
cd main
chmod +x run.sh bin/*
./run.sh -check-config
./run.sh
```

随后打开 `http://127.0.0.1:9080`。ASR候选将第一行改为 `cd asr-candidate`；模拟代理的参数见该目录 `asr-mock-README.md`，默认配置未开启ASR入口。Rust媒体程序由Go控制程序拉起，不要自行空参数启动worker。

默认是本地实验配置：SIP5060、上游5070、HTTP9080、媒体20000–21999、100路硬上限。实际线路需修改 `config/local.json` 并重新 `-check-config`；主服务端口已占用时先处理部署安排，不要同时启动两份。退出时Ctrl+C进入排空；不把本地默认配置当万路配置。

## 内容与范围

- `bin/rustswitch`：Go控制及内嵌后台；`bin/rustswitch-media`：Rust媒体；`bin/callbench`：压测发生器。
- `asr-candidate/bin/asr-mock`：真实Unix协议/PCM摘要模拟代理，不含识别模型。
- 核心程序是静态链接ELF aarch64。main保留既有Alpine虚拟机限定探针运行证据；ASR候选由同源已编译组件组装，未在Linux做整组ASR联调。本包未包含可选G.722/Opus Linux动态库，须从源码包按目标环境构建；内核PCMA/PCMU不依赖这些库。
- 两份源码的功能文档门禁仍有历史记录中的阻塞：main部分证据失效，ASR候选字段字典待同步。开发预览附件已公开，但其分发不表示完整 FreeSWITCH 兼容、5000/10000路容量或功能生产验收通过。
- 压测诊断test-02补丁和后续工作稿保留在源码包，本运行包未将它们合并进去。
- 原创材料已采用 Apache-2.0；G.722 支持文件与 SpanDSP、FreeSWITCH 模板及其他依赖保留各自许可。完整对应源码通过同一 Release 的源码包提供；许可范围及来源见随包 LICENSE_SCOPE.md、THIRD_PARTY_NOTICES.md 和 RUNTIME_SOURCE.md。

程序来源、SHA-256、构建与验证范围见 `binary-manifest.json`、`evidence/`。执行 `python3 verify.py` 可核验解压后的文件；核验文件不启动服务。

## 本次公开许可

首个公开预览版已提供 Apache-2.0 原创材料许可，G.722 支持文件采用 LGPL-2.1-only，第三方内容保留原许可。历史材料中的“许可待定”描述仅反映当时状态；当前适用范围以随包许可证为准。对应源码获取方式见 [RUNTIME_SOURCE.md](RUNTIME_SOURCE.md)。
