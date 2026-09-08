# 固定 8→16kHz FIR 参考数据

参考来自本项目 `media/src/audio/branch.rs` 的 `RateConverter`，不是 Go 两相实现自行产生的期望值。

- 原 Rust 文件 SHA-256：`56d25ffd291cc24a9f466f4b5251fa6ac8e9ff6dcbe67de02415c0ca1dbaa526`。
- 提取 `const TAPS` 至测试模块前的原函数，提取字节 SHA-256：`88067c754f460d3f152742d7a8af615c829583aa387383acfa9b3b1cfeb6f7d3`。
- 127 抽头、Hamming 窗，截止频率为 `0.45 / 2 = 0.225`，系数总和归一化为 1；插入零之前输入增益为 2。
- 这是固定有限阶带限插值，不增加 8kHz 原始音频中不存在的信息。3.9kHz 附近会按参考低通衰减。
- 系数用 `rustc 1.98.1 (48a229cea 2026-09-01)` 在 macOS arm64 从原公式生成，并固定为十六进制 f64 字面量。其他平台不重新计算平台三角函数。
- `coefficients.f64le`：127 个原始 little-endian f64，共 1016 字节；SHA-256 `e7d108779e82bc025af440ac3f2fbf4434972f93298b4bdfc79c56dc50795f01`。
- `rust_reference.bin`：六组、5440 个输入和 10880 个输出样本，共 32772 字节；SHA-256 `080c4ade6dbeb777e8cdf1a751d65b842a7650ee0f22e27fc9927964b2e7fc8e`。

样本格式是 `RSG1` 四字节标识、little-endian u32 用例数；每组为 u16 名称字节数、UTF-8 名称、u32 输入样本数、全部输入 i16le、两倍长度的全部输出 i16le。六组分别为脉冲、帧边界脉冲、固定 xorshift 噪声、正满幅、负满幅和 DC。测试固定总指纹并完整解析，不允许多余或缺失字节。

生成器和原始命令/编译日志保存在工作目录 `work/voice-runtime-m2-asr/resample-evidence/generate_reference.py` 及 `generation-01/`；这些文件是离线来源记录，不是运行时依赖。生成器使用新建目录，失败记录不会由下一次执行覆盖。

Go 实现只删除插零乘法和逐抽头取模；保留各输出相位的抽头累加顺序和 f64 乘积舍入。没有用对称求和重排，也没有把近零系数替换为零。饱和和半整数远离零舍入与原参考一致。群延迟为 63 个 16kHz 输出样本，即 3,937,500ns；不输出尾帧，不延长输入的新鲜性期限。

本包测试仅验证此固定 DSP 转换。频响及微基准不证明网络音频质量、真实 ASR 识别效果、FreeSWITCH 全面等价或五千/万路容量。
