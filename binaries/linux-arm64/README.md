# 云蝠 Voice Runtime 预编译运行包

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
- 两份源码发布文档门禁仍有已记录阻塞：main部分证据失效，ASR候选字段字典待同步。打包二进制不授予发布、全部FS兼容或5000/10000路验收。
- 压测诊断test-02补丁和后续工作稿保留在源码包，本运行包未将它们合并进去。
- 自有项目LICENSE尚待项目方确定；第三方许可见licenses，完整对应源码另发源码包。macOS包还保留随包原生库的可重建来源。

程序来源、SHA-256、构建与验证范围见 `binary-manifest.json`、`evidence/`。执行 `python3 verify.py` 可核验解压后的文件；核验文件不启动服务。

## 本次公开许可

本次已提供Apache-2.0项目许可，G.722支持文件为LGPL-2.1-only，第三方各自原许可保留。此前历史材料中“许可待定”状态已由本次许可文件解决。对应源码获取方式见[RUNTIME_SOURCE.md](RUNTIME_SOURCE.md)。
