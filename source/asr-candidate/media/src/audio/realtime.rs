//! 面向媒体工作线程的有界G.711缓冲接口；不装库、不重采样、不创建线程。
//! 调用方拥有PCM/编码输出缓冲和绝对媒体时间，实例只独占一帧有限补偿历史。
//! 本接口尚不提供G.722/Opus实时实现；原有Vec返回、16k SDK和C ABI不受影响。
use super::{g711, AudioFrameView, DecodeInput, FrameOrigin};
use anyhow::{ensure, Result};

/// 8k单声道60ms的最大样本数；G.711每样本对应一个编码字节。
pub const MAX_G711_SAMPLES: usize = 480;
/// 连续缺失的历史补偿最多120ms，按每次真实请求时长累计而非按包计数。
const MAX_CONCEALMENT_MS: u16 = 120;
/// 保持已有SDK的CN输入长度上限；借用谱参数，不因输入长度分配或复制。
const MAX_CN_BYTES: usize = 8192;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
/// 输出媒体元数据；PCM采样率与RTP时钟分开声明，不携带推测的绝对时间戳。
pub struct FrameInfo {
    /// PCM每秒样本数；当前实例固定8000。
    pub sample_rate: u32,
    /// RTP时间戳每秒刻度；与PCM率分字段，为未来G.722保留准确表示。
    pub rtp_clock_rate: u32,
    /// 当前实际PCM只有一个声道，不据此覆盖其他编码的SDP声明。
    pub channels: u8,
    /// 本次消费的媒体时间，包含没有PCM可用的缺失或CN区间。
    pub duration_ms: u16,
    /// 本次时长应推进的RTP刻度；起点由调用方RX/TX时间线提供。
    pub rtp_ticks: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
/// 只有Pcm分支写入输出切片的前samples个元素；缺失/CN绝不填充伪静音。
pub enum DecodeResult<'a> {
    /// 借用调用方的输出存储消费样本，来源标志不能在后续图分支中丢失。
    Pcm {
        info: FrameInfo,
        samples: usize,
        origin: FrameOrigin,
    },
    /// 没有历史、时长不匹配或超过补偿预算；整个输出切片保持原值。
    MissingAudio { info: FrameInfo },
    /// 当前不合成噪声；谱参数借用输入SID，其有效期不超过输入码流。
    ComfortNoise {
        info: FrameInfo,
        level_dbov: u8,
        spectral_coefficients: &'a [u8],
    },
}

impl DecodeResult<'_> {
    /// 所有输出分支都有完整时长，Missing/CN同样占据真实媒体时间。
    pub fn info(&self) -> FrameInfo {
        match self {
            Self::Pcm { info, .. }
            | Self::MissingAudio { info }
            | Self::ComfortNoise { info, .. } => *info,
        }
    }
}

/// 单worker独占G.711实例；存储固定、成功路径不分配，编码操作不修改接收补偿历史。
pub struct G711Codec {
    alaw: bool,
    history: [i16; MAX_G711_SAMPLES],
    history_samples: usize,
    lost_ms: u16,
    closed: bool,
}

impl G711Codec {
    /// 仅接受明确的PCMA/PCMU名称；不按动态PT推测编码，也不冒充原生库已接入。
    pub fn new(name: &str) -> Result<Self> {
        ensure!(
            matches!(name, "PCMA" | "PCMU"),
            "realtime G711 requires PCMA or PCMU"
        );
        Ok(Self {
            alaw: name == "PCMA",
            history: [0; MAX_G711_SAMPLES],
            history_samples: 0,
            lost_ms: 0,
            closed: false,
        })
    }

    /// 先验证全部参数与写入容量，再更新输出和历史；错误返回不会部分写样本或推进补偿预算。
    /// 真实包允许10/20/30/40/50/60ms，必须精确等于时长对应的G.711字节数。
    pub fn decode_into<'a>(
        &mut self,
        input: DecodeInput<'a>,
        duration_ms: u16,
        output: &mut [i16],
    ) -> Result<DecodeResult<'a>> {
        ensure!(!self.closed, "realtime codec is closed");
        let info = frame_info(duration_ms)?;
        let samples = info.rtp_ticks as usize;
        match input {
            DecodeInput::Packet(bytes) => {
                ensure!(bytes.len() == samples, "G711 packet duration mismatch");
                ensure!(output.len() >= samples, "PCM output buffer too small");
                for ((out, history), byte) in output[..samples]
                    .iter_mut()
                    .zip(&mut self.history[..samples])
                    .zip(bytes)
                {
                    let value = g711::decode(*byte, self.alaw);
                    *out = value;
                    *history = value;
                }
                // 新短帧不能保留上一长帧的尾部；复用固定数组，不克隆整帧。
                if self.history_samples > samples {
                    self.history[samples..self.history_samples].fill(0);
                }
                self.history_samples = samples;
                self.lost_ms = 0;
                Ok(DecodeResult::Pcm {
                    info,
                    samples,
                    origin: FrameOrigin::Decoded,
                })
            }
            DecodeInput::PacketLoss => {
                let lost_ms = self.lost_ms.saturating_add(duration_ms);
                if self.history_samples != samples || lost_ms > MAX_CONCEALMENT_MS {
                    // 缺失成功消耗媒体时间但不写用户缓冲；清除历史防止之后切回旧时长重放旧话音。
                    self.clear_history();
                    self.lost_ms = lost_ms;
                    return Ok(DecodeResult::MissingAudio { info });
                }
                ensure!(output.len() >= samples, "PCM output buffer too small");
                for (out, history) in output[..samples]
                    .iter_mut()
                    .zip(&mut self.history[..samples])
                {
                    // 这是有限历史衰减近似，不是G.711附录PLC，也不标记为真实收到的音频。
                    *history = (i32::from(*history) * 3 / 4) as i16;
                    *out = *history;
                }
                self.lost_ms = lost_ms;
                Ok(DecodeResult::Pcm {
                    info,
                    samples,
                    origin: FrameOrigin::HistoryConcealment,
                })
            }
            DecodeInput::ComfortNoise(sid) => {
                ensure!(
                    !sid.is_empty() && sid.len() <= MAX_CN_BYTES && sid[0] & 0x80 == 0,
                    "invalid RFC3389 SID"
                );
                self.clear_history();
                self.lost_ms = 0;
                Ok(DecodeResult::ComfortNoise {
                    info,
                    level_dbov: sid[0],
                    spectral_coefficients: &sid[1..],
                })
            }
        }
    }

    /// 输入是当前实例的8k单声道PCM；时长和长度必须一致，前置校验失败不写输出。
    /// 只写入返回字节数对应的前缀；编码过程不改变解码历史，收发状态不会相互污染。
    pub fn encode_into(&self, pcm: &[i16], duration_ms: u16, output: &mut [u8]) -> Result<usize> {
        ensure!(!self.closed, "realtime codec is closed");
        let samples = frame_info(duration_ms)?.rtp_ticks as usize;
        ensure!(pcm.len() == samples, "G711 PCM duration mismatch");
        ensure!(output.len() >= samples, "encoded output buffer too small");
        for (out, sample) in output[..samples].iter_mut().zip(pcm) {
            *out = g711::encode(*sample, self.alaw);
        }
        Ok(samples)
    }

    /// 带时间和来源的借用帧进入编码节点；只接受本实例8k mono，重采样须由前置分支明确完成。
    /// 帧保留的是来源RTP位置，可能来自另一种时钟；输出RTP头仍由调用方TX时间线生成。
    pub fn encode_frame(&self, frame: &AudioFrameView<'_>, output: &mut [u8]) -> Result<usize> {
        ensure!(
            frame.sample_rate() == 8000 && frame.channels() == 1,
            "G711 encoder requires an 8kHz mono AudioFrameView"
        );
        self.encode_into(frame.samples(), frame.duration_ms(), output)
    }

    /// 源身份或时间线改变时由调用方明确重置；关闭的实例不能通过reset复活。
    pub fn reset(&mut self) -> Result<()> {
        ensure!(!self.closed, "realtime codec is closed");
        self.clear_history();
        self.lost_ms = 0;
        Ok(())
    }

    /// 幂等清理会话音频历史；关闭后拒绝所有编解码操作，避免释放后的旧结果复用状态。
    pub fn close(&mut self) {
        self.clear_history();
        self.lost_ms = 0;
        self.closed = true;
    }

    /// 归还逻辑历史并擦除旧样本，不涉及堆释放或跨线程同步。
    fn clear_history(&mut self) {
        self.history.fill(0);
        self.history_samples = 0;
    }
}

/// 时长边界在任何输出或状态变化前检查，乘法仅在小范围内执行。
fn frame_info(duration_ms: u16) -> Result<FrameInfo> {
    ensure!(
        (10..=60).contains(&duration_ms) && duration_ms % 10 == 0,
        "G711 duration must be 10..60ms in 10ms steps"
    );
    Ok(FrameInfo {
        sample_rate: 8000,
        rtp_clock_rate: 8000,
        channels: 1,
        duration_ms,
        rtp_ticks: u32::from(duration_ms) * 8,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::audio::{AudioPosition, RtpPosition};
    use std::{
        alloc::{GlobalAlloc, Layout, System},
        cell::Cell,
    };

    #[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
    /// 只统计当前测试线程明确开启的区间，避免其他并行单测污染分配证据。
    struct AllocationCounts {
        alloc: usize,
        zeroed: usize,
        realloc: usize,
        dealloc: usize,
    }

    thread_local! {
        /// 常量TLS初始化不创建堆对象；分配器回调只读写整数Cell，不递归分配。
        static TRACK_ALLOCATIONS: Cell<bool> = const { Cell::new(false) };
        static ALLOCATION_COUNTS: Cell<AllocationCounts> = const {
            Cell::new(AllocationCounts { alloc: 0, zeroed: 0, realloc: 0, dealloc: 0 })
        };
    }

    struct MeasuredAllocator;

    /// 测试二进制继续使用系统分配器，只增加当前线程的可关闭计数。
    #[global_allocator]
    static TEST_ALLOCATOR: MeasuredAllocator = MeasuredAllocator;

    /// TLS退出期间访问可能失效，此时不计数；不改变任何分配器操作的语义。
    fn count_allocation(change: impl FnOnce(&mut AllocationCounts)) {
        if TRACK_ALLOCATIONS.try_with(Cell::get).unwrap_or(false) {
            let _ = ALLOCATION_COUNTS.try_with(|counter| {
                let mut counts = counter.get();
                change(&mut counts);
                counter.set(counts);
            });
        }
    }

    // SAFETY: 所有请求及释放参数原样交给System，计数不改变指针、布局或分配生命周期。
    unsafe impl GlobalAlloc for MeasuredAllocator {
        unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
            count_allocation(|count| count.alloc += 1);
            // SAFETY: layout来自GlobalAlloc调用方，原样满足System的分配前置条件。
            unsafe { System.alloc(layout) }
        }

        unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
            count_allocation(|count| count.zeroed += 1);
            // SAFETY: layout原样委托System，返回的内存保持全零初始化约定。
            unsafe { System.alloc_zeroed(layout) }
        }

        unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, size: usize) -> *mut u8 {
            count_allocation(|count| count.realloc += 1);
            // SAFETY: 调用方保证原指针、布局和新长度有效；此处不修改或预先释放指针。
            unsafe { System.realloc(ptr, layout, size) }
        }

        unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
            count_allocation(|count| count.dealloc += 1);
            // SAFETY: 调用方保证指针归属及布局有效；System只执行一次对应释放。
            unsafe { System.dealloc(ptr, layout) }
        }
    }

    /// 即使用例panic也恢复计数开关，防止后续测试或测试框架被意外计入。
    struct AllocationScope;
    impl Drop for AllocationScope {
        fn drop(&mut self) {
            TRACK_ALLOCATIONS.with(|tracking| tracking.set(false));
        }
    }

    fn measured<T>(operation: impl FnOnce() -> T) -> (T, AllocationCounts) {
        ALLOCATION_COUNTS.with(|counter| counter.set(AllocationCounts::default()));
        TRACK_ALLOCATIONS.with(|tracking| tracking.set(true));
        let scope = AllocationScope;
        let result = operation();
        drop(scope);
        (result, ALLOCATION_COUNTS.with(Cell::get))
    }

    /// 固定码字/PCM金样覆盖极值、正负静音及小幅值，不从被测函数产生期望值。
    #[test]
    fn golden_audio_and_all_packet_durations() {
        for (name, codes, values, canonical) in [
            (
                "PCMU",
                [0xff, 0x7f, 0x80, 0x00, 0xfe, 0x7e],
                [0, 0, 32124, -32124, 8, -8],
                [0xff, 0xff, 0x80, 0x00, 0xfe, 0x7e],
            ),
            (
                "PCMA",
                [0xd5, 0x55, 0xaa, 0x2a, 0xd4, 0x54],
                [8, -8, 32256, -32256, 24, -24],
                [0xd5, 0x55, 0xaa, 0x2a, 0xd4, 0x54],
            ),
        ] {
            let mut codec = G711Codec::new(name).unwrap();
            for duration in [10, 20, 30, 40, 50, 60, 50, 40, 30, 20, 10] {
                let count = duration as usize * 8;
                let mut payload = [0; MAX_G711_SAMPLES];
                let mut pcm = [123; MAX_G711_SAMPLES + 4];
                for (index, byte) in payload[..count].iter_mut().enumerate() {
                    *byte = codes[index % codes.len()];
                }
                let decoded = codec
                    .decode_into(DecodeInput::Packet(&payload[..count]), duration, &mut pcm)
                    .unwrap();
                assert_eq!(
                    decoded,
                    DecodeResult::Pcm {
                        info: FrameInfo {
                            sample_rate: 8000,
                            rtp_clock_rate: 8000,
                            channels: 1,
                            duration_ms: duration,
                            rtp_ticks: count as u32,
                        },
                        samples: count,
                        origin: FrameOrigin::Decoded,
                    }
                );
                for (index, &sample) in pcm[..count].iter().enumerate() {
                    assert_eq!(sample, values[index % values.len()]);
                }
                assert!(pcm[count..].iter().all(|&sample| sample == 123));
                assert!(codec.history[count..].iter().all(|&sample| sample == 0));
                let mut output = [19; MAX_G711_SAMPLES + 4];
                assert_eq!(
                    codec
                        .encode_into(&pcm[..count], duration, &mut output)
                        .unwrap(),
                    count
                );
                for (index, &byte) in output[..count].iter().enumerate() {
                    assert_eq!(byte, canonical[index % canonical.len()]);
                }
                assert!(output[count..].iter().all(|&byte| byte == 19));
            }
        }
    }

    /// 连续不同频率的真实PCM经A/μ律往返；按独立样本能量量测误差，避免静音回环假通过。
    #[test]
    fn continuous_two_tone_audio_has_bounded_quantization_error() {
        for name in ["PCMA", "PCMU"] {
            let encoder = G711Codec::new(name).unwrap();
            let mut decoder = G711Codec::new(name).unwrap();
            let mut input = [0i16; MAX_G711_SAMPLES];
            let mut encoded = [0; MAX_G711_SAMPLES];
            let mut output = [0; MAX_G711_SAMPLES];
            let mut phase = 0;
            let (mut signal_energy, mut error_energy) = (0f64, 0f64);
            for duration in [10, 20, 30, 40, 50, 60].into_iter().cycle().take(60) {
                let count = duration as usize * 8;
                for sample in &mut input[..count] {
                    let t = phase as f64 / 8000.;
                    *sample = ((std::f64::consts::TAU * 437. * t).sin() * 10000.
                        + (std::f64::consts::TAU * 913. * t).sin() * 6000.)
                        .round() as i16;
                    phase += 1;
                }
                encoder
                    .encode_into(&input[..count], duration, &mut encoded)
                    .unwrap();
                let result = decoder
                    .decode_into(
                        DecodeInput::Packet(&encoded[..count]),
                        duration,
                        &mut output,
                    )
                    .unwrap();
                assert_eq!(result.info().rtp_ticks, count as u32);
                for (&expected, &actual) in input[..count].iter().zip(&output[..count]) {
                    signal_energy += f64::from(expected).powi(2);
                    error_energy += (f64::from(expected) - f64::from(actual)).powi(2);
                    assert!((i32::from(expected) - i32::from(actual)).abs() <= 1024);
                }
            }
            let snr_db = 10. * (signal_energy / error_energy).log10();
            println!("{name} 连续双音样本={phase} 量化信噪比={snr_db:.4}dB");
            assert!(snr_db > 34., "{name}: {snr_db}");
        }
    }

    /// 有效历史仅按相同时长衰减最多120ms；超限后和没有历史时不生成静音或旧样本。
    #[test]
    fn concealment_budget_is_media_time_and_missing_preserves_buffer() {
        for duration in [10, 20, 30, 40, 50, 60] {
            let count = duration as usize * 8;
            let mut codec = G711Codec::new("PCMU").unwrap();
            let mut output = [777; MAX_G711_SAMPLES];
            assert!(matches!(
                codec
                    .decode_into(DecodeInput::PacketLoss, duration, &mut output)
                    .unwrap(),
                DecodeResult::MissingAudio { .. }
            ));
            assert!(output.iter().all(|&value| value == 777));
            codec
                .decode_into(
                    DecodeInput::Packet(&[0x80; MAX_G711_SAMPLES][..count]),
                    duration,
                    &mut output,
                )
                .unwrap();
            let mut expected = 32124i16;
            for _ in 0..120 / duration {
                expected = (i32::from(expected) * 3 / 4) as i16;
                assert!(matches!(
                    codec
                        .decode_into(DecodeInput::PacketLoss, duration, &mut output)
                        .unwrap(),
                    DecodeResult::Pcm {
                        origin: FrameOrigin::HistoryConcealment,
                        ..
                    }
                ));
                assert!(output[..count].iter().all(|&value| value == expected));
            }
            output.fill(777);
            assert!(matches!(
                codec
                    .decode_into(DecodeInput::PacketLoss, duration, &mut output)
                    .unwrap(),
                DecodeResult::MissingAudio { .. }
            ));
            assert!(output.iter().all(|&value| value == 777));
            assert!(codec.history.iter().all(|&value| value == 0));
        }
    }

    /// 时长切换、CN和明确reset都使旧语音历史失效；CN谱参数仍借用原输入，无PCM填充。
    #[test]
    fn duration_change_cn_reset_and_close_cannot_replay_old_audio() {
        let mut codec = G711Codec::new("PCMA").unwrap();
        let packet = [0xaa; 160];
        let mut output = [555; MAX_G711_SAMPLES];
        codec
            .decode_into(DecodeInput::Packet(&packet), 20, &mut output)
            .unwrap();
        assert!(matches!(
            codec
                .decode_into(DecodeInput::PacketLoss, 10, &mut [])
                .unwrap(),
            DecodeResult::MissingAudio { .. }
        ));
        assert!(matches!(
            codec
                .decode_into(DecodeInput::PacketLoss, 20, &mut [])
                .unwrap(),
            DecodeResult::MissingAudio { .. }
        ));
        codec
            .decode_into(DecodeInput::Packet(&packet), 20, &mut output)
            .unwrap();
        let sid = [42, 129, 4, 0, 255];
        output.fill(555);
        let result = codec
            .decode_into(DecodeInput::ComfortNoise(&sid), 30, &mut output)
            .unwrap();
        match result {
            DecodeResult::ComfortNoise {
                info,
                level_dbov,
                spectral_coefficients,
            } => {
                assert_eq!(info.duration_ms, 30);
                assert_eq!(level_dbov, 42);
                assert_eq!(spectral_coefficients, &sid[1..]);
                assert_eq!(spectral_coefficients.as_ptr(), sid[1..].as_ptr());
            }
            _ => panic!("CN不能变成PCM"),
        }
        assert!(output.iter().all(|&value| value == 555));
        assert!(matches!(
            codec
                .decode_into(DecodeInput::PacketLoss, 20, &mut [])
                .unwrap(),
            DecodeResult::MissingAudio { .. }
        ));
        codec
            .decode_into(DecodeInput::Packet(&packet), 20, &mut output)
            .unwrap();
        codec.reset().unwrap();
        assert!(matches!(
            codec
                .decode_into(DecodeInput::PacketLoss, 20, &mut [])
                .unwrap(),
            DecodeResult::MissingAudio { .. }
        ));
        codec
            .decode_into(DecodeInput::Packet(&packet), 20, &mut output)
            .unwrap();
        codec.close();
        codec.close();
        assert!(codec.history.iter().all(|&value| value == 0));
        assert!(codec.reset().is_err());
        output.fill(555);
        assert!(codec
            .decode_into(DecodeInput::Packet(&packet), 20, &mut output)
            .is_err());
        assert!(output.iter().all(|&value| value == 555));
        let mut encoded = [77; 160];
        assert!(codec.encode_into(&[0; 160], 20, &mut encoded).is_err());
        assert_eq!(encoded, [77; 160]);
    }

    /// 参数错误和不足缓冲都保持整个用户输出及codec历史/补偿预算不变。
    #[test]
    fn invalid_input_is_atomic_for_state_and_output() {
        let mut codec = G711Codec::new("PCMU").unwrap();
        let mut output = [123; MAX_G711_SAMPLES];
        codec
            .decode_into(DecodeInput::Packet(&[0x80; 160]), 20, &mut output)
            .unwrap();
        codec
            .decode_into(DecodeInput::PacketLoss, 20, &mut output)
            .unwrap();
        let previous = (codec.history, codec.history_samples, codec.lost_ms);
        for duration in [0, 1, 9, 15, 61, 120, u16::MAX] {
            output.fill(123);
            assert!(codec
                .decode_into(DecodeInput::Packet(&[0xff; 160]), duration, &mut output)
                .is_err());
            assert!(output.iter().all(|&value| value == 123));
            assert_eq!(
                (codec.history, codec.history_samples, codec.lost_ms),
                previous
            );
        }
        let oversized_sid = [0; MAX_CN_BYTES + 1];
        for input in [
            DecodeInput::Packet(&[]),
            DecodeInput::Packet(&[0xff; 159]),
            DecodeInput::Packet(&[0xff; 161]),
            DecodeInput::ComfortNoise(&[]),
            DecodeInput::ComfortNoise(&[0x80]),
            DecodeInput::ComfortNoise(&oversized_sid),
        ] {
            output.fill(123);
            assert!(codec.decode_into(input, 20, &mut output).is_err());
            assert!(output.iter().all(|&value| value == 123));
            assert_eq!(
                (codec.history, codec.history_samples, codec.lost_ms),
                previous
            );
        }
        for input in [DecodeInput::Packet(&[0xff; 160]), DecodeInput::PacketLoss] {
            output.fill(123);
            assert!(codec.decode_into(input, 20, &mut output[..159]).is_err());
            assert!(output.iter().all(|&value| value == 123));
            assert_eq!(
                (codec.history, codec.history_samples, codec.lost_ms),
                previous
            );
        }
        let mut encoded = [77; MAX_G711_SAMPLES];
        for (pcm, duration, capacity) in [
            (&[0; 160][..], 15, 480),
            (&[0; 159][..], 20, 480),
            (&[0; 161][..], 20, 480),
            (&[0; 160][..], 20, 159),
        ] {
            assert!(codec
                .encode_into(pcm, duration, &mut encoded[..capacity])
                .is_err());
            assert_eq!(encoded, [77; MAX_G711_SAMPLES]);
            assert_eq!(
                (codec.history, codec.history_samples, codec.lost_ms),
                previous
            );
        }
        for name in ["", "pcmu", "G722", "OPUS", "G729"] {
            assert!(G711Codec::new(name).is_err());
        }
    }

    /// 包括构造、真实解码、编码、PLC、CN、缺失、reset/close的成功路径实测零堆操作。
    #[test]
    fn reusable_success_paths_perform_no_heap_operations() {
        let (_, counts) = measured(|| {
            for name in ["PCMA", "PCMU"] {
                let mut codec = G711Codec::new(name).unwrap();
                let mut pcm = [0; MAX_G711_SAMPLES];
                let mut encoded = [0; MAX_G711_SAMPLES];
                let packet = [0xaa; MAX_G711_SAMPLES];
                for index in 0..1000 {
                    let duration = (index % 6 + 1) * 10;
                    let count = duration as usize * 8;
                    std::hint::black_box(
                        codec
                            .decode_into(DecodeInput::Packet(&packet[..count]), duration, &mut pcm)
                            .unwrap(),
                    );
                    std::hint::black_box(
                        codec
                            .encode_into(&pcm[..count], duration, &mut encoded)
                            .unwrap(),
                    );
                    // 图消费10/20ms借用片段；完整10–60ms包仍由上面的into路径实际编码。
                    let position = AudioPosition::new(
                        u64::from(index) * 60_000_000,
                        Some(RtpPosition::new(u32::from(index) * 480, 8000).unwrap()),
                    );
                    let view = AudioFrameView::new(
                        &pcm[..count.min(160)],
                        8000,
                        position,
                        FrameOrigin::Decoded,
                    )
                    .unwrap();
                    std::hint::black_box(codec.encode_frame(&view, &mut encoded).unwrap());
                    for _ in 0..120 / duration + 1 {
                        std::hint::black_box(
                            codec
                                .decode_into(DecodeInput::PacketLoss, duration, &mut pcm)
                                .unwrap(),
                        );
                    }
                    std::hint::black_box(
                        codec
                            .decode_into(DecodeInput::ComfortNoise(&[42, 5]), duration, &mut [])
                            .unwrap(),
                    );
                    std::hint::black_box(
                        codec
                            .decode_into(DecodeInput::PacketLoss, duration, &mut pcm)
                            .unwrap(),
                    );
                    codec.reset().unwrap();
                }
                codec.close();
            }
        });
        println!("G711复用缓冲2000组成功路径：{counts:?}");
        assert_eq!(counts, AllocationCounts::default());
    }

    /// 用实际Box分配/释放验证计数器会观察到堆操作，防止失效计数器误报零分配。
    #[test]
    fn allocation_meter_observes_a_real_heap_allocation() {
        let (_, counts) = measured(|| {
            let probe = std::hint::black_box(Box::new([17u8; 1024]));
            std::hint::black_box(probe.as_ptr());
            drop(probe);
        });
        assert!(counts.alloc > 0, "{counts:?}");
        assert!(counts.dealloc > 0, "{counts:?}");
    }

    /// 编码节点消费完整帧元数据，保留来源位置；不能把来源48kRTP时钟误当作当前8kPCM率。
    #[test]
    fn frame_encoder_keeps_metadata_and_rejects_wrong_pcm_rate_atomically() {
        let codec = G711Codec::new("PCMU").unwrap();
        let position = AudioPosition::new(20_000_000, Some(RtpPosition::new(960, 48000).unwrap()));
        let samples = [32124; 960];
        for origin in [
            FrameOrigin::Decoded,
            FrameOrigin::SuppliedPcm,
            FrameOrigin::CodecPlc,
            FrameOrigin::HistoryConcealment,
        ] {
            let view = AudioFrameView::new(&samples[..160], 8000, position, origin).unwrap();
            let mut encoded = [77; 164];
            assert_eq!(codec.encode_frame(&view, &mut encoded).unwrap(), 160);
            assert_eq!(encoded[..160], [0x80; 160]);
            assert_eq!(encoded[160..], [77; 4]);
            assert_eq!(view.position(), position);
            assert_eq!(view.origin(), origin);
            encoded.fill(77);
            assert!(codec.encode_frame(&view, &mut encoded[..159]).is_err());
            assert_eq!(encoded, [77; 164]);
        }
        for rate in [16000, 24000, 48000] {
            let view = AudioFrameView::new(
                &samples[..rate as usize / 50],
                rate,
                position,
                FrameOrigin::SuppliedPcm,
            )
            .unwrap();
            let mut encoded = [77; 960];
            assert!(codec.encode_frame(&view, &mut encoded).is_err());
            assert_eq!(encoded, [77; 960]);
        }
    }
}
