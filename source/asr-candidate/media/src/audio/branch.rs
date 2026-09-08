//! 离线按需分支转换：同率移动样本，异率才启动有界FIR；不提供网络ASR/TTS调度器。
use super::{AudioFrame, AudioOutput, AudioPosition, SUPPORTED_SAMPLE_RATES};
use anyhow::{ensure, Result};

/// 带统一时间位置的音频块；缺包/CN也占据媒体时间，却没有伪造的PCM。
#[derive(Debug)]
pub struct AudioBlock {
    position: AudioPosition,
    output: AudioOutput,
}
impl AudioBlock {
    /// 显式定位输出并检查时间上界；原帧若已有位置，必须完全一致。
    pub fn new(output: AudioOutput, position: AudioPosition) -> Result<Self> {
        let duration = match &output {
            AudioOutput::Frame { frame, .. } => frame.duration_ms(),
            AudioOutput::MissingAudio { duration_ms } => *duration_ms,
            AudioOutput::ComfortNoise {
                duration_ms,
                level_dbov,
                spectral_coefficients,
            } => {
                ensure!(
                    *level_dbov < 128 && spectral_coefficients.len() <= 8191,
                    "invalid CN metadata"
                );
                *duration_ms
            }
        };
        position.advance(duration)?;
        let output = match output {
            AudioOutput::Frame { frame, origin } => AudioOutput::Frame {
                frame: frame.at_position(position)?,
                origin,
            },
            other => other,
        };
        Ok(Self { position, output })
    }
    /// 同一流的时间位置，不因目标采样率变化而变化。
    pub fn position(&self) -> AudioPosition {
        self.position
    }
    /// 保留来源类型，只读访问不会消费样本所有权。
    pub fn output(&self) -> &AudioOutput {
        &self.output
    }
    /// 消费当前块，把最后的所有权交给下游；没有隐藏队列或后台任务。
    pub fn into_output(self) -> AudioOutput {
        self.output
    }
    /// 缺包/CN也有明确时长，不能从零个样本推断为零时间。
    pub fn duration_ms(&self) -> u16 {
        match &self.output {
            AudioOutput::Frame { frame, .. } => frame.duration_ms(),
            AudioOutput::MissingAudio { duration_ms }
            | AudioOutput::ComfortNoise { duration_ms, .. } => *duration_ms,
        }
    }
}

/// 固定一组源/目标率的顺序分支；单实例只保留127抽头状态，不保留待处理帧。
/// FIR是有延迟的参考实现，未作主观语音质量、ASR效果或万路性能认证。
pub struct AudioBranch {
    source_rate: u32,
    target_rate: u32,
    converter: Option<RateConverter>,
    next_time_ns: Option<u64>,
    closed: bool,
}
impl AudioBranch {
    /// 同率不创建滤波器；不同率只接受已限定的8/16/24/48k组合。
    pub fn new(source_rate: u32, target_rate: u32) -> Result<Self> {
        ensure!(
            SUPPORTED_SAMPLE_RATES.contains(&source_rate)
                && SUPPORTED_SAMPLE_RATES.contains(&target_rate),
            "unsupported branch sample rate"
        );
        Ok(Self {
            source_rate,
            target_rate,
            converter: (source_rate != target_rate)
                .then(|| RateConverter::new(source_rate, target_rate)),
            next_time_ns: None,
            closed: false,
        })
    }
    /// 是否真实需要重采样，供上层决定节点拓扑，而非全图固定16k。
    pub fn resampling_required(&self) -> bool {
        self.converter.is_some()
    }
    /// 参考FIR的算法延迟，向上取整到纳秒；来源时间不自动减去此延迟。
    pub fn filter_delay_ns(&self) -> u64 {
        self.converter.as_ref().map_or(0, |filter| {
            (63_000_000_000u64).div_ceil(u64::from(self.source_rate) * filter.up as u64)
        })
    }
    /// 处理一个有界块。时间倒退/重叠拒绝；前向跳跃或缺包/CN清掉旧滤波历史。
    /// PCM来源标记原样保留，CN谱参数保持源模型含义，不把它转换为目标率噪声。
    pub fn process(&mut self, block: AudioBlock) -> Result<AudioBlock> {
        ensure!(!self.closed, "audio branch is closed");
        if let AudioOutput::Frame { frame, .. } = &block.output {
            ensure!(
                frame.sample_rate() == self.source_rate,
                "audio branch source rate mismatch"
            );
        }
        if let Some(next) = self.next_time_ns {
            ensure!(
                block.position.media_time_ns() >= next,
                "audio branch timeline overlap"
            );
            if block.position.media_time_ns() != next {
                if let Some(filter) = &mut self.converter {
                    filter.reset();
                }
            }
        }
        let next = block.position.advance(block.duration_ms())?.media_time_ns();
        let output = match block.output {
            AudioOutput::Frame { frame, origin } => {
                let frame = if let Some(filter) = &mut self.converter {
                    AudioFrame::with_sample_rate(filter.convert(frame.samples()), self.target_rate)?
                        .at_position(block.position)?
                } else {
                    frame
                };
                AudioOutput::Frame { frame, origin }
            }
            other => {
                if let Some(filter) = &mut self.converter {
                    filter.reset();
                }
                other
            }
        };
        self.next_time_ns = Some(next);
        Ok(AudioBlock {
            position: block.position,
            output,
        })
    }
    /// 显式关闭当前流并清掉历史；不合成尾帧、不产生额外静音、关闭后不能复用到另一通话。
    pub fn close(&mut self) {
        self.closed = true;
        self.next_time_ns = None;
        if let Some(filter) = &mut self.converter {
            filter.reset();
        }
    }
}

const TAPS: usize = 127;
/// 通用有理数采样率转换，先插零再低通再抽取，最大中间率48k、比率因子不超过6。
struct RateConverter {
    history: [f64; TAPS],
    coefficients: [f64; TAPS],
    cursor: usize,
    phase: usize,
    up: usize,
    down: usize,
}
impl RateConverter {
    /// 只对上层已验证的固定采样率求最大公约数与低通系数，启动后不再计算三角函数。
    fn new(source: u32, target: u32) -> Self {
        let (mut a, mut b) = (source, target);
        while b != 0 {
            (a, b) = (b, a % b);
        }
        let up = (target / a) as usize;
        let down = (source / a) as usize;
        let cutoff = 0.45 / up.max(down) as f64;
        let mut coefficients = [0.; TAPS];
        let mut total = 0.;
        for (i, coefficient) in coefficients.iter_mut().enumerate() {
            let x = i as f64 - 63.;
            let sinc = if x == 0. {
                2. * cutoff
            } else {
                (2. * std::f64::consts::PI * cutoff * x).sin() / (std::f64::consts::PI * x)
            };
            *coefficient =
                sinc * (0.54 - 0.46 * (2. * std::f64::consts::PI * i as f64 / 126.).cos());
            total += *coefficient;
        }
        for coefficient in &mut coefficients {
            *coefficient /= total;
        }
        Self {
            history: [0.; TAPS],
            coefficients,
            cursor: 0,
            phase: 0,
            up,
            down,
        }
    }
    /// 逐样本维护跨帧状态；只在真实输出点做卷积，不为不会输出的插值点计算滤波和分配内存。
    fn convert(&mut self, samples: &[i16]) -> Vec<i16> {
        let mut output = Vec::with_capacity(samples.len() * self.up / self.down);
        for &sample in samples {
            for slot in 0..self.up {
                self.history[self.cursor] = if slot == 0 {
                    f64::from(sample) * self.up as f64
                } else {
                    0.
                };
                if self.phase == 0 {
                    let mut filtered = 0.;
                    for i in 0..TAPS {
                        filtered +=
                            self.coefficients[i] * self.history[(self.cursor + TAPS - i) % TAPS];
                    }
                    output.push(filtered.round().clamp(-32768., 32767.) as i16);
                }
                self.cursor = (self.cursor + 1) % TAPS;
                self.phase = (self.phase + 1) % self.down;
            }
        }
        output
    }
    /// 缺失区间不能借用之前话音填充；重新进入时只有短暂的滤波启动过程。
    fn reset(&mut self) {
        self.history.fill(0.);
        self.cursor = 0;
        self.phase = 0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::audio::{FrameOrigin, RtpPosition};
    fn block(samples: Vec<i16>, rate: u32, time: u64, origin: FrameOrigin) -> AudioBlock {
        AudioBlock::new(
            AudioOutput::Frame {
                frame: AudioFrame::with_sample_rate(samples, rate).unwrap(),
                origin,
            },
            AudioPosition::new(time, Some(RtpPosition::new(160, 8_000).unwrap())),
        )
        .unwrap()
    }
    #[test]
    /// 每个率对都在10/20ms切分下保持长度与连续滤波，不产生帧边界重置。
    fn all_rate_pairs_preserve_duration_and_streaming() {
        for source in SUPPORTED_SAMPLE_RATES {
            for target in SUPPORTED_SAMPLE_RATES {
                let input: Vec<_> = (0..source as usize / 50)
                    .map(|n| {
                        ((n as f64 * 2. * std::f64::consts::PI * 440. / source as f64).sin()
                            * 10_000.) as i16
                    })
                    .collect();
                let mut full = AudioBranch::new(source, target).unwrap();
                let mut split = AudioBranch::new(source, target).unwrap();
                let expected = full
                    .process(block(input.clone(), source, 0, FrameOrigin::Decoded))
                    .unwrap();
                let first = split
                    .process(block(
                        input[..input.len() / 2].to_vec(),
                        source,
                        0,
                        FrameOrigin::Decoded,
                    ))
                    .unwrap();
                let second = split
                    .process(block(
                        input[input.len() / 2..].to_vec(),
                        source,
                        10_000_000,
                        FrameOrigin::Decoded,
                    ))
                    .unwrap();
                if let (
                    AudioOutput::Frame { frame: a, .. },
                    AudioOutput::Frame { frame: b, .. },
                    AudioOutput::Frame { frame: c, .. },
                ) = (expected.output(), first.output(), second.output())
                {
                    assert_eq!(a.samples(), [b.samples(), c.samples()].concat());
                    assert_eq!(
                        (a.sample_rate(), a.valid_samples(), a.duration_ms()),
                        (target, target as usize / 50, 20)
                    );
                    assert_eq!(a.position().unwrap().rtp().unwrap().clock_rate(), 8_000);
                } else {
                    panic!("real audio became missing");
                }
            }
        }
    }
    #[test]
    /// 同率分支移动原始只读缓冲，既不复制也不改变PLC来源。
    fn same_rate_moves_samples_and_preserves_origin() {
        let input = block(vec![17; 160], 8_000, 0, FrameOrigin::HistoryConcealment);
        let pointer = match input.output() {
            AudioOutput::Frame { frame, .. } => frame.samples().as_ptr(),
            _ => unreachable!(),
        };
        let mut branch = AudioBranch::new(8_000, 8_000).unwrap();
        assert!(!branch.resampling_required());
        assert_eq!(branch.filter_delay_ns(), 0);
        let output = branch.process(input).unwrap();
        assert!(
            matches!(output.output(), AudioOutput::Frame{frame,origin:FrameOrigin::HistoryConcealment} if frame.samples().as_ptr()==pointer)
        );
    }
    #[test]
    /// 缺包和CN保持显式空音频，且后续真实静音不能重放旧FIR中的话音。
    fn missing_cn_and_lifecycle_do_not_fabricate_silence_or_replay() {
        for cn in [false, true] {
            let mut branch = AudioBranch::new(24_000, 16_000).unwrap();
            branch
                .process(block(vec![12_000; 480], 24_000, 0, FrameOrigin::Decoded))
                .unwrap();
            let gap = if cn {
                AudioOutput::ComfortNoise {
                    duration_ms: 20,
                    level_dbov: 37,
                    spectral_coefficients: vec![4, 5],
                }
            } else {
                AudioOutput::MissingAudio { duration_ms: 20 }
            };
            let gap = AudioBlock::new(gap, AudioPosition::new(20_000_000, None)).unwrap();
            let result = branch.process(gap).unwrap();
            assert!(if cn {
                matches!(result.output(), AudioOutput::ComfortNoise{level_dbov:37,spectral_coefficients,..} if spectral_coefficients==&[4,5])
            } else {
                matches!(
                    result.output(),
                    AudioOutput::MissingAudio { duration_ms: 20 }
                )
            });
            let result = branch
                .process(block(
                    vec![0; 480],
                    24_000,
                    40_000_000,
                    FrameOrigin::Decoded,
                ))
                .unwrap();
            assert!(
                matches!(result.output(), AudioOutput::Frame{frame,origin:FrameOrigin::Decoded} if frame.samples().iter().all(|&n|n==0))
            );
            assert!(branch
                .process(block(
                    vec![0; 480],
                    24_000,
                    40_000_000,
                    FrameOrigin::Decoded
                ))
                .is_err());
            branch.close();
            assert!(branch
                .process(block(
                    vec![0; 480],
                    24_000,
                    60_000_000,
                    FrameOrigin::Decoded
                ))
                .is_err());
        }
    }
    #[test]
    /// 真实连续正弦样本验证参考低通：48k的1k保留、12k在降到16k时显著衰减。
    fn downsample_signal_energy_and_alias_rejection() {
        let measure = |frequency: f64| {
            let mut converter = RateConverter::new(48_000, 16_000);
            let mut energy = 0.;
            let mut count = 0;
            for frame in 0..20 {
                let input: Vec<_> = (0..960)
                    .map(|n| {
                        ((2. * std::f64::consts::PI * frequency * (frame * 960 + n) as f64
                            / 48_000.)
                            .sin()
                            * 10_000.) as i16
                    })
                    .collect();
                let output = converter.convert(&input);
                if frame > 0 {
                    for x in output {
                        energy += f64::from(x).powi(2);
                        count += 1;
                    }
                }
            }
            (energy / count as f64).sqrt()
        };
        let pass = measure(1000.);
        let stop = measure(12_000.);
        assert!((6800. ..7300.).contains(&pass), "passband RMS: {pass}");
        assert!(stop < pass * 0.01, "alias RMS: {stop}, passband: {pass}");
    }
    #[test]
    /// 24k TTS可按需去8/16/48k，错误源率不推进时间；未知率/无效缺包时长拒绝。
    fn tts_rate_validation_and_discontinuity() {
        let mut branch = AudioBranch::new(24_000, 48_000).unwrap();
        assert!(branch
            .process(block(vec![0; 160], 8_000, 0, FrameOrigin::Decoded))
            .is_err());
        let output = branch
            .process(block(vec![1000; 480], 24_000, 0, FrameOrigin::CodecPlc))
            .unwrap();
        assert!(
            matches!(output.output(),AudioOutput::Frame{frame,origin:FrameOrigin::CodecPlc} if frame.valid_samples()==960)
        );
        let output = branch
            .process(block(
                vec![0; 480],
                24_000,
                100_000_000,
                FrameOrigin::Decoded,
            ))
            .unwrap();
        assert!(
            matches!(output.output(),AudioOutput::Frame{frame,..} if frame.samples().iter().all(|&x|x==0))
        );
        assert!(AudioBranch::new(44_100, 16_000).is_err());
        assert!(AudioBlock::new(
            AudioOutput::MissingAudio { duration_ms: 0 },
            AudioPosition::new(0, None)
        )
        .is_err());
    }
    #[test]
    /// 脉冲峰值验证公开的FIR延迟，恒定输入验证不同采样比的增益归一化。
    fn filter_delay_and_dc_gain_match_samples() {
        for source in SUPPORTED_SAMPLE_RATES {
            for target in SUPPORTED_SAMPLE_RATES {
                if source == target {
                    continue;
                }
                let mut branch = AudioBranch::new(source, target).unwrap();
                let mut impulse = vec![0; source as usize / 50];
                impulse[0] = 20_000;
                let output = branch
                    .process(block(impulse, source, 0, FrameOrigin::Decoded))
                    .unwrap();
                let AudioOutput::Frame { frame, .. } = output.output() else {
                    panic!("impulse missing")
                };
                let peak = frame
                    .samples()
                    .iter()
                    .enumerate()
                    .max_by_key(|(_, x)| i32::from(**x).abs())
                    .unwrap()
                    .0 as f64;
                let expected = branch.filter_delay_ns() as f64 * f64::from(target) / 1_000_000_000.;
                assert!(
                    (peak - expected).abs() <= 1.,
                    "{source}->{target}: peak {peak}, expected {expected}"
                );
                let output = branch
                    .process(block(
                        vec![10_000; source as usize / 50],
                        source,
                        20_000_000,
                        FrameOrigin::Decoded,
                    ))
                    .unwrap();
                let AudioOutput::Frame { frame, .. } = output.output() else {
                    panic!("DC missing")
                };
                assert!(
                    frame
                        .samples()
                        .iter()
                        .skip(target as usize / 100)
                        .all(|&n| (i32::from(n) - 10_000).abs() < 100),
                    "{source}->{target} changed DC gain"
                );
            }
        }
    }
}
