package resample

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"testing"
)

// 参考样本由原 Rust 逐点插零卷积实现生成，测试不调用 Go 优化路径生成期望值。
//
//go:embed testdata/rust_reference.bin
var rustReference []byte

//go:embed testdata/coefficients.f64le
var rustCoefficients []byte

type referenceCase struct {
	name   string
	input  []int16
	output []int16
}

// readReference 严格解析固定离线样本，拒绝静默截断、追加数据或无效帧长。
func readReference(t *testing.T) []referenceCase {
	t.Helper()
	if got := fmt.Sprintf("%x", sha256.Sum256(rustReference)); got != "080c4ade6dbeb777e8cdf1a751d65b842a7650ee0f22e27fc9927964b2e7fc8e" {
		t.Fatalf("Rust 参考样本指纹变化: %s", got)
	}
	r := bytes.NewReader(rustReference)
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || string(magic[:]) != "RSG1" {
		t.Fatalf("无效参考样本头: %q, %v", magic, err)
	}
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil || count != 6 {
		t.Fatalf("参考样本必须有六组: %d, %v", count, err)
	}
	cases := make([]referenceCase, 0, count)
	for i := uint32(0); i < count; i++ {
		var nameLength uint16
		if err := binary.Read(r, binary.LittleEndian, &nameLength); err != nil || nameLength == 0 || nameLength > 64 {
			t.Fatalf("无效名称长度: %d, %v", nameLength, err)
		}
		name := make([]byte, nameLength)
		if _, err := io.ReadFull(r, name); err != nil {
			t.Fatal(err)
		}
		var count uint32
		if err := binary.Read(r, binary.LittleEndian, &count); err != nil || count == 0 || count%160 != 0 || count > 1920 {
			t.Fatalf("无效输入帧长: %d, %v", count, err)
		}
		item := referenceCase{name: string(name), input: make([]int16, count), output: make([]int16, count*2)}
		if err := binary.Read(r, binary.LittleEndian, item.input); err != nil {
			t.Fatal(err)
		}
		if err := binary.Read(r, binary.LittleEndian, item.output); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, item)
	}
	if r.Len() != 0 {
		t.Fatalf("参考样本有 %d 字节尾随数据", r.Len())
	}
	return cases
}

// processFrames 只属于测试，所有输出切片分配都在被测 Process 之外。
func processFrames(c *Converter, input []int16) []int16 {
	output := make([]int16, len(input)*2)
	for n := 0; n < len(input); n += 160 {
		var frame [160]int16
		var converted [320]int16
		copy(frame[:], input[n:n+160])
		c.Process(&frame, &converted)
		copy(output[n*2:], converted[:])
	}
	return output
}

func assertSamples(t *testing.T, got, want []int16) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("样本数: got=%d want=%d", len(got), len(want))
	}
	for n := range got {
		if got[n] != want[n] {
			t.Fatalf("样本 %d: got=%d want=%d", n, got[n], want[n])
		}
	}
}

// TestRustReferenceVectors 将全部 10,880 个输出样本逐个与独立 Rust 原实现核对。
func TestRustReferenceVectors(t *testing.T) {
	for _, item := range readReference(t) {
		t.Run(item.name, func(t *testing.T) {
			assertSamples(t, processFrames(New(), item.input), item.output)
		})
	}
}

// TestCoefficientProvenance 同时固定原始位指纹并独立复算窗口公式，防止误用截止频率或归一化。
func TestCoefficientProvenance(t *testing.T) {
	const fingerprint = "e7d108779e82bc025af440ac3f2fbf4434972f93298b4bdfc79c56dc50795f01"
	if got := fmt.Sprintf("%x", sha256.Sum256(rustCoefficients)); got != fingerprint || len(rustCoefficients) != 127*8 {
		t.Fatalf("系数参考指纹/长度错误: %s / %d", got, len(rustCoefficients))
	}
	var formula [127]float64
	var total float64
	for i := range formula {
		x := float64(i - 63)
		sinc := 0.45
		if x != 0 {
			sinc = math.Sin(2*math.Pi*0.225*x) / (math.Pi * x)
		}
		formula[i] = sinc * (0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/126))
		total += formula[i]
	}
	var dc float64
	for i, got := range coefficients {
		bits := binary.LittleEndian.Uint64(rustCoefficients[i*8:])
		if math.Float64bits(got) != bits {
			t.Fatalf("抽头 %d 与 Rust 位值不同", i)
		}
		// Go 与 Rust 的平台三角函数允许末位差异，冻结的实际系数则必须逐位相等。
		if math.Abs(got-formula[i]/total) > 2e-16 {
			t.Fatalf("抽头 %d 不满足参考公式", i)
		}
		if math.Abs(got-coefficients[126-i]) > 2e-16 {
			t.Fatalf("抽头 %d 不满足线性相位对称性", i)
		}
		dc += got
	}
	if math.Abs(dc-1) > 1e-15 {
		t.Fatalf("直流增益没有归一化: %.18g", dc)
	}
}

// naiveContinuous 采用完整插零序列和直接卷积，不共享生产环形历史或两相索引。
// 它仅用于验证连续信号与 160 样本切帧的等价性，没有性能承诺。
func naiveContinuous(input []int16) []int16 {
	upsampled := make([]float64, len(input)*2)
	for i, sample := range input {
		upsampled[2*i] = float64(sample) * 2
	}
	output := make([]int16, len(upsampled))
	for n := range upsampled {
		var value float64
		for tap, coefficient := range coefficients {
			if n >= tap {
				value += float64(coefficient * upsampled[n-tap])
			}
		}
		output[n] = int16(math.Max(-32768, math.Min(32767, math.Round(value))))
	}
	return output
}

func TestFrameBoundariesEqualContinuousConvolution(t *testing.T) {
	input := make([]int16, 160*25)
	var state uint32 = 0x63954da7
	for i := range input {
		state = state*1664525 + 1013904223
		input[i] = int16(state >> 16)
	}
	assertSamples(t, processFrames(New(), input), naiveContinuous(input))
}

func TestResetDoesNotLeakPreviousSource(t *testing.T) {
	var previous [160]int16
	for i := range previous {
		previous[i] = 10_000
	}
	var output [320]int16
	c := New()
	c.Process(&previous, &output)
	retained := *c
	var silent [160]int16
	retained.Process(&silent, &output)
	if output[0] == 0 {
		t.Fatal("测试前置错误：旧来源没有非零滤波历史")
	}
	c.Reset()
	c.Process(&silent, &output)
	for n, sample := range output {
		if sample != 0 {
			t.Fatalf("重置后仍有旧来源样本 %d=%d", n, sample)
		}
	}
	for i := range previous {
		previous[i] = int16(i*73 - 7000)
	}
	c.Reset()
	var fresh [320]int16
	New().Process(&previous, &fresh)
	c.Process(&previous, &output)
	assertSamples(t, output[:], fresh[:])
}

func TestImpulseDelayAndFixedFrameLength(t *testing.T) {
	if DelayNS != 63*1_000_000_000/16_000 {
		t.Fatalf("群延迟错误: %d", DelayNS)
	}
	var input [160]int16
	input[0] = 20000
	var output [320]int16
	New().Process(&input, &output)
	peak := 0
	for i, sample := range output {
		if math.Abs(float64(sample)) > math.Abs(float64(output[peak])) {
			peak = i
		}
	}
	if peak != 63 {
		t.Fatalf("没有保留 63 个输出样本的群延迟: peak=%d", peak)
	}
	for i := 127; i < len(output); i++ {
		if output[i] != 0 {
			t.Fatalf("脉冲响应超出 127 抽头: %d=%d", i, output[i])
		}
	}
}

func TestFullScaleSaturatesWithoutWrap(t *testing.T) {
	for _, level := range []int16{32767, -32768} {
		t.Run(fmt.Sprint(level), func(t *testing.T) {
			input := make([]int16, 160*4)
			for i := range input {
				input[i] = level
			}
			output := processFrames(New(), input)
			assertSamples(t, output, naiveContinuous(input))
			clipped := 0
			for _, sample := range output {
				if sample == level {
					clipped++
				}
			}
			if clipped == 0 {
				t.Fatal("满幅阶跃没有覆盖实际饱和路径")
			}
			// 稳态不可因越界整数转换反向回绕。
			for _, sample := range output[320:] {
				if float64(sample)*float64(level) <= 0 {
					t.Fatalf("满幅稳态符号反转: %d", sample)
				}
			}
		})
	}
}

func TestQuantizationMatchesRustRounding(t *testing.T) {
	for _, input := range []float64{-90000, -32768.6, -32768, -32767.5, -1.5, -0.5, math.Nextafter(-0.5, 0), -0.4999, 0, 0.4999, math.Nextafter(0.5, 0), 0.5, 1.5, 32766.5, 32767, 32767.5, 90000} {
		want := int16(math.Max(-32768, math.Min(32767, math.Round(input))))
		if got := quantize(input); got != want {
			t.Fatalf("量化 %g: got=%d want=%d", input, got, want)
		}
	}
}

// toneAmplitude 使用整数周期区间的 DFT，独立测量真实量化输出的基波及插值镜像。
func toneAmplitude(samples []int16, frequency float64) float64 {
	var real, imaginary float64
	for i, sample := range samples {
		phase := 2 * math.Pi * frequency * float64(i) / 16000
		real += float64(sample) * math.Cos(phase)
		imaginary += float64(sample) * math.Sin(phase)
	}
	return 2 * math.Hypot(real, imaginary) / float64(len(samples))
}

func toneInput(frequency float64) []int16 {
	input := make([]int16, 160*20)
	for i := range input {
		input[i] = int16(math.Round(12000 * math.Sin(2*math.Pi*frequency*float64(i)/8000)))
	}
	return input
}

func TestPassbandAndImageRejection(t *testing.T) {
	for _, frequency := range []float64{100, 300, 1000, 2500, 3200} {
		t.Run(fmt.Sprintf("%.0fHz", frequency), func(t *testing.T) {
			// 丢开首帧只为测量稳态响应，不改变转换器实际输出或对外时间戳。
			output := processFrames(New(), toneInput(frequency))[320:]
			pass := toneAmplitude(output, frequency)
			image := toneAmplitude(output, 8000-frequency)
			rejection := 20 * math.Log10(pass/math.Max(image, 1e-12))
			t.Logf("frequency=%.0fHz gain=%.8f image_rejection=%.3fdB", frequency, pass/12000, rejection)
			if pass/12000 < 0.98 || pass/12000 > 1.02 {
				t.Fatalf("通带增益超出 2%%: %g", pass/12000)
			}
			if rejection < 50 {
				t.Fatalf("插值镜像抑制不足 50dB: %g", rejection)
			}
		})
	}
}

func TestTransitionDoesNotInventHighFrequencyDetail(t *testing.T) {
	output := processFrames(New(), toneInput(3900))[320:]
	gain := toneAmplitude(output, 3900) / 12000
	t.Logf("3900Hz gain=%.8f attenuation=%.3fdB", gain, -20*math.Log10(gain))
	if gain > 0.02 {
		t.Fatalf("接近输入奈奎斯特频率的过渡带没有受到足够抑制: %g", gain)
	}
}

func TestProcessAllocatesZeroPerFrame(t *testing.T) {
	c := New()
	var input [160]int16
	var output [320]int16
	for i := range input {
		input[i] = int16(i*181 - 15000)
	}
	if got := testing.AllocsPerRun(1000, func() { c.Process(&input, &output) }); got != 0 {
		t.Fatalf("每帧发生堆分配: %g", got)
	}
	if got := testing.AllocsPerRun(1000, func() { c.Reset() }); got != 0 {
		t.Fatalf("来源重置发生堆分配: %g", got)
	}
}

var benchmarkSample int16

// BenchmarkProcess8To16 只测本机单转换器热路径，不代表多路呼叫或 ASR 端到端容量。
func BenchmarkProcess8To16(b *testing.B) {
	c := New()
	var input [160]int16
	var output [320]int16
	for i := range input {
		input[i] = int16(i*181 - 15000)
	}
	b.ReportAllocs()
	b.SetBytes(160 * 2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Process(&input, &output)
	}
	benchmarkSample = output[127]
}
