// Package resample 提供 ASR 输入专用的固定 8→16kHz、单声道 S16 流式转换。
// 它不改变音频来源或时间期限；同率直通由调用方处理，无须建立转换器。
package resample

import "math"

// DelayNS 是 127 抽头线性相位 FIR 的固定群延迟：63 / 16000 秒。
// 调用方应将它作为信号处理延迟传递，不能据此延长原始音频的交付期限。
const DelayNS uint32 = 3_937_500

// Converter 只保留一个来源的滤波历史，必须由单个音频写入者持有。
// 64 个输入样本各存两份，使卷积窗口连续，避免每个抽头都做环形索引。
// 零值可直接使用；没有协程、锁、外部缓冲或每帧堆分配。
type Converter struct {
	history [128]float64
	cursor  int
}

// New 建立一次性流状态；每路重采样只需建立一次，逐帧复用。
func New() *Converter { return &Converter{} }

// Reset 清除前一来源的历史，不输出或补齐滤波尾部。
// 来源变化、缺口、PLC、CN 等边界由上层明确决定何时调用，不能跨边界借用旧话音。
func (c *Converter) Reset() { *c = Converter{} }

// Process 把一帧 20ms 的 160 个 8kHz 样本写入调用方的 320 个 16kHz 样本空间。
// 连续帧保留历史；初始和重置后的历史为零，固定延迟不被偷偷移除。
func (c *Converter) Process(input *[160]int16, output *[320]int16) {
	for n, sample := range input {
		// 插值两倍需要两倍增益。两相分别读取原 FIR 的偶、奇抽头，
		// 只省掉明确插入的零，不把 0.225 截止频率误认为严格半带滤波器。
		value := float64(sample) * 2
		c.history[c.cursor] = value
		c.history[c.cursor+64] = value
		window := c.history[c.cursor : c.cursor+64]
		var even, odd float64
		for k, sample := range window[:63] {
			// 明确将乘积舍入到 float64，禁止融合乘加改变 Rust 参考的累加语义。
			// 各相仍按原抽头顺序累加，避免对称项重组在半整数附近改变 1 LSB。
			even += float64(coefficients[2*k] * sample)
			odd += float64(coefficients[2*k+1] * sample)
		}
		even += float64(coefficients[126] * window[63])
		output[2*n] = quantize(even)
		output[2*n+1] = quantize(odd)
		c.cursor = (c.cursor + 63) & 63
	}
}

// quantize 与 Rust f64::round().clamp(-32768, 32767) 一致：半值远离零，并饱和。
// 固定有限系数和 S16 输入保证这里没有 NaN/Inf；饱和先于整数转换，防止回绕。
func quantize(value float64) int16 {
	if value >= 32767 {
		return 32767
	}
	if value <= -32768 {
		return -32768
	}
	// 不能用加减 0.5 后截断代替：半整数相邻的浮点数可能在加法中先被进位。
	return int16(math.Round(value))
}
