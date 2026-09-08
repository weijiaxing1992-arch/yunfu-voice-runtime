//! 多采样率单声道帧、不可变样本所有权与显式媒体时间位置。
use super::FrameOrigin;
use anyhow::{ensure, Context, Result};

/// 当前SDK帧与分支转换接受的采样率；24k用于离线TTS样本入口。
pub const SUPPORTED_SAMPLE_RATES: [u32; 4] = [8_000, 16_000, 24_000, 48_000];

/// 源RTP时间戳与其每秒刻度，独立于解码后的PCM采样率。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RtpPosition {
    timestamp: u32,
    clock_rate: u32,
}
impl RtpPosition {
    /// 这里只建立时间单位；具体编码与时钟组合仍须通过CodecSpec协商校验。
    pub fn new(timestamp: u32, clock_rate: u32) -> Result<Self> {
        ensure!(
            [8_000, 16_000, 24_000, 32_000, 44_100, 48_000].contains(&clock_rate),
            "unsupported RTP clock"
        );
        Ok(Self {
            timestamp,
            clock_rate,
        })
    }
    /// 读取原始32位RTP时钟位置，可正常跨过回绕边界。
    pub fn timestamp(self) -> u32 {
        self.timestamp
    }
    /// 读取线上每秒刻度，不能当成PCM采样率。
    pub fn clock_rate(self) -> u32 {
        self.clock_rate
    }
    /// 按有效SDK帧时长推进线上时钟；回绕是RTP正常行为。
    pub fn advance(self, duration_ms: u16) -> Result<Self> {
        ensure!(
            [10, 20].contains(&duration_ms),
            "unsupported frame duration"
        );
        Ok(Self {
            timestamp: self
                .timestamp
                .wrapping_add(self.clock_rate * u32::from(duration_ms) / 1000),
            ..self
        })
    }
}

/// 同一媒体流的单调时间位置；可选RTP字段描述来源，不是重采样后新生成的报文头。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AudioPosition {
    media_time_ns: u64,
    rtp: Option<RtpPosition>,
}
impl AudioPosition {
    /// 位置由调度器或离线调用方给定；不从墙钟或收到UDP的时刻猜测音频时间。
    pub fn new(media_time_ns: u64, rtp: Option<RtpPosition>) -> Self {
        Self { media_time_ns, rtp }
    }
    /// 同一流起点以来的纳秒数，不表示UTC时间。
    pub fn media_time_ns(self) -> u64 {
        self.media_time_ns
    }
    /// 保留源RTP时钟，不把G.722的16k样本数加到8k时钟上。
    pub fn rtp(self) -> Option<RtpPosition> {
        self.rtp
    }
    /// 精确推进10/20ms，纳秒溢出必须报错，不能跳回媒体起点。
    pub fn advance(self, duration_ms: u16) -> Result<Self> {
        ensure!(
            [10, 20].contains(&duration_ms),
            "unsupported frame duration"
        );
        Ok(Self {
            media_time_ns: self
                .media_time_ns
                .checked_add(u64::from(duration_ms) * 1_000_000)
                .context("audio timeline overflow")?,
            rtp: self.rtp.map(|rtp| rtp.advance(duration_ms)).transpose()?,
        })
    }
}

/// 实时图的只读借用帧；样本、媒体时间和真实/补偿来源作为一个整体传递。
/// 不拥有Vec，不创建Arc/Rc，不分配；缺失/CN没有PCM，不能构造成此类型。
#[derive(Clone, Copy, Debug)]
pub struct AudioFrameView<'a> {
    samples: &'a [i16],
    sample_rate: u32,
    duration_ms: u16,
    position: AudioPosition,
    origin: FrameOrigin,
}

impl<'a> AudioFrameView<'a> {
    /// 精确接收8/16/24/48k单声道10/20ms；来源RTP时钟可与PCM采样率不同。
    /// 校验整帧结束位置，禁止纳秒溢出；连续帧单调性由持有流状态的图/调度器核对。
    pub fn new(
        samples: &'a [i16],
        sample_rate: u32,
        position: AudioPosition,
        origin: FrameOrigin,
    ) -> Result<Self> {
        ensure!(
            SUPPORTED_SAMPLE_RATES.contains(&sample_rate),
            "unsupported AudioFrameView sample rate"
        );
        let per_ms = sample_rate as usize / 1000;
        ensure!(
            [per_ms * 10, per_ms * 20].contains(&samples.len()),
            "AudioFrameView requires exactly 10 or 20 ms of mono samples"
        );
        let duration_ms = (samples.len() / per_ms) as u16;
        position.advance(duration_ms)?;
        Ok(Self {
            samples,
            sample_rate,
            duration_ms,
            position,
            origin,
        })
    }

    /// 只借用有效音频范围；原缓冲仍由调用方拥有，帧不能比缓冲存活更久。
    pub fn samples(&self) -> &'a [i16] {
        self.samples
    }

    /// 当前实际样本数，固定mono，因此也是每声道样本数。
    pub fn valid_samples(&self) -> usize {
        self.samples.len()
    }

    /// 当前PCM采样率；不代表帧来源RTP的时钟。
    pub fn sample_rate(&self) -> u32 {
        self.sample_rate
    }

    /// 当前借用帧仅支持mono，不能将交错双声道数据冒充mono。
    pub fn channels(&self) -> u8 {
        1
    }

    /// 由真实样本数精确计算的图帧时长，仅10/20ms，不缩小codec包接口的10–60ms范围。
    pub fn duration_ms(&self) -> u16 {
        self.duration_ms
    }

    /// 必须存在的媒体时间位置；不从墙钟、输出成功次数或PCM率猜测源RTP时间。
    pub fn position(&self) -> AudioPosition {
        self.position
    }

    /// 真实解码、显式提供PCM和补偿来源完整保留，后续节点不能默认当作真实收到。
    pub fn origin(&self) -> FrameOrigin {
        self.origin
    }
}

/// 统一格式/时长/所有权，不强制统一采样率；样本均为单声道有符号16位。
/// 保留原Vec所有权与只读借用，不引入Arc/Rc；clone会复制样本，每帧最多960个有效样本。
#[derive(Clone, Debug)]
pub struct AudioFrame {
    samples: Vec<i16>,
    sample_rate: u32,
    duration_ms: u16,
    position: Option<AudioPosition>,
}
impl AudioFrame {
    /// 保持原SDK入口的16k语义，仅接受160或320样本。
    pub fn new(samples: Vec<i16>) -> Result<Self> {
        Self::with_sample_rate(samples, 16_000)
    }
    /// 新入口明确传入8/16/24/48k；有效样本数必须恰好等于10或20ms，无隐式填零。
    /// 丢弃调用方可能附带的多余Vec容量，持有样本容量不超过当前帧的有效长度。
    pub fn with_sample_rate(samples: Vec<i16>, sample_rate: u32) -> Result<Self> {
        ensure!(
            SUPPORTED_SAMPLE_RATES.contains(&sample_rate),
            "unsupported AudioFrame sample rate"
        );
        let per_ms = sample_rate as usize / 1000;
        ensure!(
            [per_ms * 10, per_ms * 20].contains(&samples.len()),
            "AudioFrame requires exactly 10 or 20 ms of mono samples"
        );
        Ok(Self {
            duration_ms: (samples.len() / per_ms) as u16,
            samples: samples.into_boxed_slice().into_vec(),
            sample_rate,
            position: None,
        })
    }
    /// 旧版小端字节入口保持16k，不自动猜测码流或采样率。
    pub fn from_s16le(bytes: &[u8]) -> Result<Self> {
        Self::from_s16le_at(bytes, 16_000)
    }
    /// 分配前先校验精确字节上限；字节序为小端，与RTP L16的大端格式不同。
    pub fn from_s16le_at(bytes: &[u8], sample_rate: u32) -> Result<Self> {
        ensure!(
            SUPPORTED_SAMPLE_RATES.contains(&sample_rate),
            "unsupported s16le sample rate"
        );
        let per_ms = sample_rate as usize / 1000;
        ensure!(
            [per_ms * 20, per_ms * 40].contains(&bytes.len()),
            "invalid s16le frame size"
        );
        Self::with_sample_rate(
            bytes
                .chunks_exact(2)
                .map(|b| i16::from_le_bytes([b[0], b[1]]))
                .collect(),
            sample_rate,
        )
    }
    /// 为新帧附加明确时间位置；已定位帧不能被悄悄改写到别的位置。
    pub fn at_position(mut self, position: AudioPosition) -> Result<Self> {
        position.advance(self.duration_ms)?;
        ensure!(
            self.position.is_none() || self.position == Some(position),
            "audio frame already has a different position"
        );
        self.position = Some(position);
        Ok(self)
    }
    /// 只读样本视图；不会把容量外的数据当作有效音频。
    pub fn samples(&self) -> &[i16] {
        &self.samples
    }
    /// 有效样本数，每声道计；本版本只有一个声道。
    pub fn valid_samples(&self) -> usize {
        self.samples.len()
    }
    /// PCM每秒样本数，与源RTP时钟独立。
    pub fn sample_rate(&self) -> u32 {
        self.sample_rate
    }
    /// 固定单声道；立体声需在未来版本显式建模，不能交错塞入此帧。
    pub fn channels(&self) -> u8 {
        1
    }
    /// SDK帧精确时长，仅10或20毫秒。
    pub fn duration_ms(&self) -> u16 {
        self.duration_ms
    }
    /// 老入口没有位置时为None，新分支处理必须由AudioBlock附加位置。
    pub fn position(&self) -> Option<AudioPosition> {
        self.position
    }
    /// 借用已有帧进入实时图；旧无时间入口必须先定位，不能用零时间假装位置完整。
    /// 原AudioFrame不记录来源，由调用方传入解码器/供音接口的真实来源。
    pub fn as_view(&self, origin: FrameOrigin) -> Result<AudioFrameView<'_>> {
        AudioFrameView::new(
            &self.samples,
            self.sample_rate,
            self.position
                .context("AudioFrameView requires an explicit audio position")?,
            origin,
        )
    }
    /// 所有宿主输出同一种有符号16位小端表示。
    pub fn to_s16le(&self) -> Vec<u8> {
        self.samples.iter().flat_map(|x| x.to_le_bytes()).collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 借用保持原样本地址，所有PCM率和来源标志保留；来源RTP可用不同的时钟并正常回绕。
    #[test]
    fn borrowed_views_keep_samples_origins_and_independent_rtp_clocks() {
        let samples = [177; 960];
        for rate in SUPPORTED_SAMPLE_RATES {
            for duration in [10, 20] {
                for clock in [8000, 24000, 48000] {
                    for origin in [
                        FrameOrigin::Decoded,
                        FrameOrigin::SuppliedPcm,
                        FrameOrigin::CodecPlc,
                        FrameOrigin::HistoryConcealment,
                    ] {
                        let position = AudioPosition::new(
                            10_000_000,
                            Some(RtpPosition::new(u32::MAX - 9, clock).unwrap()),
                        );
                        let count = rate as usize * duration as usize / 1000;
                        let view =
                            AudioFrameView::new(&samples[..count], rate, position, origin).unwrap();
                        assert_eq!(view.samples().as_ptr(), samples.as_ptr());
                        assert_eq!(view.valid_samples(), count);
                        assert_eq!(view.sample_rate(), rate);
                        assert_eq!(view.channels(), 1);
                        assert_eq!(view.duration_ms(), duration);
                        assert_eq!(view.position(), position);
                        assert_eq!(view.origin(), origin);
                        let end = view.position().advance(view.duration_ms()).unwrap();
                        assert_eq!(
                            end.media_time_ns(),
                            10_000_000 + u64::from(duration) * 1_000_000
                        );
                        assert_eq!(
                            end.rtp().unwrap().timestamp(),
                            (u32::MAX - 9).wrapping_add(clock * u32::from(duration) / 1000)
                        );
                    }
                }
            }
        }
    }

    /// 图帧不截断、不补零；样本率、精确帧长和结束位置必须先通过校验。
    #[test]
    fn borrowed_views_reject_invalid_shape_and_overflow() {
        let samples = [17; 960];
        let position = AudioPosition::new(0, None);
        for rate in [0, 1, 12000, 32000, 44100, u32::MAX] {
            assert!(
                AudioFrameView::new(&samples[..160], rate, position, FrameOrigin::SuppliedPcm)
                    .is_err()
            );
        }
        for count in [0, 1, 79, 81, 159, 161, 240, 480, 960] {
            assert!(AudioFrameView::new(
                &samples[..count],
                8000,
                position,
                FrameOrigin::SuppliedPcm
            )
            .is_err());
        }
        let exact = AudioPosition::new(u64::MAX - 20_000_000, None);
        assert!(
            AudioFrameView::new(&samples[..160], 8000, exact, FrameOrigin::SuppliedPcm).is_ok()
        );
        let overflow = AudioPosition::new(u64::MAX - 19_999_999, None);
        assert!(
            AudioFrameView::new(&samples[..160], 8000, overflow, FrameOrigin::SuppliedPcm).is_err()
        );
        assert_eq!(samples, [17; 960]);
    }

    /// 旧所有权帧只有明确定位后才能借用为图帧，借用不复制Vec或抹除传入的补偿来源。
    #[test]
    fn owned_frame_requires_position_before_borrowed_graph_view() {
        let frame = AudioFrame::new(vec![19; 320]).unwrap();
        assert!(frame.as_view(FrameOrigin::Decoded).is_err());
        let position = AudioPosition::new(7, Some(RtpPosition::new(100, 8000).unwrap()));
        let frame = frame.at_position(position).unwrap();
        let view = frame.as_view(FrameOrigin::HistoryConcealment).unwrap();
        assert_eq!(view.samples().as_ptr(), frame.samples().as_ptr());
        assert_eq!(view.sample_rate(), 16000);
        assert_eq!(view.duration_ms(), 20);
        assert_eq!(view.position(), position);
        assert_eq!(view.origin(), FrameOrigin::HistoryConcealment);
    }
    #[test]
    /// 采样率/时长/有效长度形成唯一对应；旧入口仍固定16k。
    fn rates_lengths_endian_and_legacy() {
        for rate in SUPPORTED_SAMPLE_RATES {
            for ms in [10u16, 20] {
                let samples = vec![i16::MIN; rate as usize * usize::from(ms) / 1000];
                let frame = AudioFrame::with_sample_rate(samples.clone(), rate).unwrap();
                assert_eq!(
                    (
                        frame.sample_rate(),
                        frame.channels(),
                        frame.valid_samples(),
                        frame.duration_ms()
                    ),
                    (rate, 1, samples.len(), ms)
                );
                let decoded = AudioFrame::from_s16le_at(&frame.to_s16le(), rate).unwrap();
                assert_eq!(decoded.samples(), samples);
                assert_eq!(&frame.to_s16le()[..2], &[0, 128]);
                assert!(
                    AudioFrame::with_sample_rate(samples[..samples.len() - 1].to_vec(), rate)
                        .is_err()
                );
                assert!(AudioFrame::from_s16le_at(&frame.to_s16le()[1..], rate).is_err());
            }
        }
        assert_eq!(AudioFrame::new(vec![0; 160]).unwrap().sample_rate(), 16_000);
        assert!(AudioFrame::new(vec![0; 80]).is_err());
        for rate in [0, 1, 12_000, 44_100, u32::MAX] {
            assert!(AudioFrame::with_sample_rate(vec![0; 160], rate).is_err());
        }
    }
    #[test]
    /// 同样20ms在G.722产生320样本但只推进160RTP刻度，Opus则推进960。
    fn media_and_rtp_time_are_independent_and_checked() {
        for (rate, clock, count, ticks) in [
            (16_000, 8_000, 320, 160),
            (48_000, 48_000, 960, 960),
            (8_000, 8_000, 160, 160),
        ] {
            let rtp = RtpPosition::new(u32::MAX - 30, clock).unwrap();
            let position = AudioPosition::new(1_000_000, Some(rtp));
            let frame = AudioFrame::with_sample_rate(vec![0; count], rate)
                .unwrap()
                .at_position(position)
                .unwrap();
            assert_eq!(frame.position(), Some(position));
            let next = position.advance(20).unwrap();
            assert_eq!(next.media_time_ns(), 21_000_000);
            assert_eq!(
                next.rtp().unwrap().timestamp(),
                (u32::MAX - 30).wrapping_add(ticks)
            );
            assert!(frame.at_position(next).is_err());
        }
        assert!(AudioPosition::new(u64::MAX, None).advance(10).is_err());
        assert!(RtpPosition::new(0, 0).is_err());
    }
    #[test]
    /// 保持旧Vec所有权：克隆独立复制，释放原帧后仍能安全消费克隆。
    fn frame_clone_retains_independent_ownership() {
        let frame = AudioFrame::new(vec![123; 160]).unwrap();
        let second = frame.clone();
        assert!(!std::ptr::eq(
            frame.samples().as_ptr(),
            second.samples().as_ptr()
        ));
        drop(frame);
        assert_eq!(second.samples(), &[123; 160]);
        let mut oversized = Vec::with_capacity(100_000);
        oversized.resize(160, 1);
        let bounded = AudioFrame::new(oversized).unwrap();
        assert_eq!(bounded.samples.capacity(), 160);
    }
}
