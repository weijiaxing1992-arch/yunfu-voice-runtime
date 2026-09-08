//! G.711 标准压扩与固定 2 倍采样率转换；重采样有持续滤波状态，不按帧重新置零。

/// 31 抽头低通 FIR 的每方向状态；截止频率 0.24，抑制 16k 降采样到 8k 的混叠。
pub struct Resampler {
    history: [f64; 31],
    cursor: usize,
    coefficients: [f64; 31],
}
impl Default for Resampler {
    /// 初始化对称窗函数滤波器并归一化直流增益；每实例只计算一次。
    fn default() -> Self {
        let mut coefficients = [0.; 31];
        let mut total = 0.;
        for (i, c) in coefficients.iter_mut().enumerate() {
            let x = i as f64 - 15.;
            let sinc = if x == 0. {
                0.48
            } else {
                (std::f64::consts::PI * 0.48 * x).sin() / (std::f64::consts::PI * x)
            };
            *c = sinc * (0.54 - 0.46 * (2. * std::f64::consts::PI * i as f64 / 30.).cos());
            total += *c;
        }
        for c in &mut coefficients {
            *c /= total;
        }
        Self {
            history: [0.; 31],
            cursor: 0,
            coefficients,
        }
    }
}
impl Resampler {
    /// 一次输入样本推进滤波器；饱和转换避免幅度溢出。
    fn sample(&mut self, value: f64) -> i16 {
        self.history[self.cursor] = value;
        let mut out = 0.;
        for i in 0..31 {
            out += self.coefficients[i] * self.history[(self.cursor + 31 - i) % 31];
        }
        self.cursor = (self.cursor + 1) % 31;
        out.round().clamp(-32768., 32767.) as i16
    }
    /// 先低通再隔点取样，返回精确一半样本；调用方保证输入帧长为偶数。
    pub fn down(&mut self, pcm: &[i16]) -> Vec<i16> {
        pcm.iter()
            .enumerate()
            .filter_map(|(i, &x)| {
                let y = self.sample(f64::from(x));
                (i % 2 == 1).then_some(y)
            })
            .collect()
    }
    /// 零插值后低通并补偿两倍增益，返回精确两倍样本。
    pub fn up(&mut self, pcm: &[i16]) -> Vec<i16> {
        let mut out = Vec::with_capacity(pcm.len() * 2);
        for &x in pcm {
            out.push(self.sample(f64::from(x) * 2.));
            out.push(self.sample(0.));
        }
        out
    }
}
/// 一个 PCM 样本压扩为 A-law 或 μ-law；中间值使用 i32 安全覆盖 i16::MIN。
pub fn encode(sample: i16, alaw: bool) -> u8 {
    let mut x = i32::from(sample);
    if alaw {
        let mask = if x >= 0 {
            0xd5
        } else {
            x = -x - 1;
            0x55
        };
        let segment = if x < 256 {
            0
        } else {
            (31 - x.leading_zeros() - 7).min(7)
        };
        let quant = if segment == 0 {
            (x >> 4) & 15
        } else {
            (x >> (segment + 3)) & 15
        };
        ((segment as i32 * 16 + quant) ^ mask) as u8
    } else {
        let mask = if x < 0 {
            x = -x;
            0x7f
        } else {
            0xff
        };
        x = x.min(32635) + 132;
        let segment = (31 - x.leading_zeros() - 7).min(7);
        ((((segment as i32) << 4) | ((x >> (segment + 3)) & 15)) ^ mask) as u8
    }
}
/// 从压扩字节恢复有符号 PCM；量化损失是编码本身的性质。
pub fn decode(byte: u8, alaw: bool) -> i16 {
    if alaw {
        let x = byte ^ 0x55;
        let segment = (x & 0x70) >> 4;
        let mut y = i32::from(x & 15) << 4;
        y += if segment == 0 { 8 } else { 0x108 };
        if segment > 1 {
            y <<= segment - 1;
        }
        if x & 0x80 != 0 {
            y as i16
        } else {
            -y as i16
        }
    } else {
        let x = !byte;
        let y = (((i32::from(x & 15) << 3) + 132) << ((x >> 4) & 7)) - 132;
        if x & 0x80 != 0 {
            -y as i16
        } else {
            y as i16
        }
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    /// 覆盖标准静音码字和全部 i16 输入，量化误差须保持 G.711 理论量级。
    fn companding_known_values_and_all_samples() {
        assert_eq!(encode(0, false), 0xff);
        assert_eq!(decode(0xff, false), 0);
        assert_eq!(encode(0, true), 0xd5);
        assert_eq!(decode(0xd5, true), 8);
        for alaw in [false, true] {
            for x in i16::MIN..=i16::MAX {
                let error = (i32::from(decode(encode(x, alaw), alaw)) - i32::from(x)).abs();
                assert!(error <= 1024, "{x}: {error}");
            }
        }
    }
    #[test]
    /// 帧切分不改变连续重采样输出，不能每帧重置 FIR 导致边界爆音。
    fn resampling_is_streaming() {
        let input: Vec<_> = (0..640).map(|x| (x * 41) as i16).collect();
        let mut a = Resampler::default();
        let mut b = Resampler::default();
        let all = a.down(&input);
        let parts: [Vec<i16>; 2] = [b.down(&input[..320]), b.down(&input[320..])];
        assert_eq!(all, parts.concat());
    }
}
