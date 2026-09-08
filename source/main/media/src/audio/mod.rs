//! 离线音频 SDK：规范 PCM、真实 P0 编解码和显式缺包/CN 语义。
//! 本模块尚未接入实时 Session；RTP worker 继续同编码直通，不隐式转码或生成静音。
pub mod branch;
mod frame;
pub(crate) mod g711;
mod native;
/// 原生8k G.711的可复用缓冲底层；实时调度和RTP收发由调用方负责。
pub mod realtime;
pub use frame::{AudioFrame, AudioFrameView, AudioPosition, RtpPosition, SUPPORTED_SAMPLE_RATES};
pub mod spec;
use anyhow::{ensure, Context, Result};
use serde::Serialize;
use serde_json::{json, Value};
use std::path::PathBuf;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
/// 每一帧都保留来源语义，PLC 生成的近似语音不能冒充真实收到的音频。
pub enum FrameOrigin {
    /// 真正收到的码流经过解码，可能本来就是静音或编码内DTX。
    Decoded,
    /// 调用方直接提供的PCM，例如离线TTS样本；不冒充收到并解码的网络音频。
    SuppliedPcm,
    /// Opus原生丢包隐藏的估计结果，不能标为正常收到。
    CodecPlc,
    /// 有界历史衰减补偿，未实现G.711附录PLC或G.722预测器重建。
    HistoryConcealment,
}
/// 输入方明确区分真实编码包、预计到期仍未收到的包，以及独立 CN SID。
pub enum DecodeInput<'a> {
    /// 一份已经移除RTP头和填充的真实音频码流。
    Packet(&'a [u8]),
    /// 调用方在正确的媒体时间线上判定本帧缺失；SDK不自行猜测UDP丢包。
    PacketLoss,
    /// 已按协商CN类型分流的单声道SID，不送入主编码解码器。
    ComfortNoise(&'a [u8]),
}
/// SDK 输出允许没有 PCM；没有历史时的缺包与未合成的 CN 都不能补成静音帧。
#[derive(Debug)]
pub enum AudioOutput {
    /// 可消费的规范PCM帧及其真实/补偿来源。
    Frame {
        frame: AudioFrame,
        origin: FrameOrigin,
    },
    /// 无可用估计，不输出伪造静音；消费者应保留缺失语义。
    MissingAudio { duration_ms: u16 },
    /// 噪声模型元数据，当前SDK不生成PCM噪声；电平值表示负dBov。
    ComfortNoise {
        duration_ms: u16,
        level_dbov: u8,
        spectral_coefficients: Vec<u8>,
    },
}
/// 编码器实现与库状态，每方向均由单一调用方顺序访问。
enum Backend {
    G711 {
        alaw: bool,
        down: Option<Box<g711::Resampler>>,
        up: Option<Box<g711::Resampler>>,
    },
    Native(native::NativeCodec),
}
/// 一个编码/解码对；历史 PLC 仅保留一帧，不形成无限音频积压。
pub struct AudioCodec {
    /// 已确认的P0编码名，控制Opus原生PLC与历史近似的选择。
    name: String,
    /// 本实例的 PCM 处理采样率；它不代替线上 RTP 时钟。
    sample_rate: u32,
    /// 互斥后端实例；状态不跨线程共享。
    backend: Backend,
    /// 最近输出的一帧，后续历史补偿会继续衰减它。
    last: Option<AudioFrame>,
    /// 连续缺失的累计毫秒，按真实10/20ms时长相加，不能用次数乘当前时长。
    lost_ms: u32,
    /// CN后没有真实音频包时不调用残留语音状态的PLC，防止重新输出DTX前话音。
    in_comfort_noise: bool,
}
impl AudioCodec {
    /// 显式加载 P0 编码；G729/G726 等仅支持 RTP relay，SDK 调用明确返回不可用。
    pub fn new(name: &str) -> Result<Self> {
        Self::with_rate(name, 16_000)
    }
    /// 新分支保留编码原生 PCM 率：G.711 为8k，G.722 为16k，Opus 为48k。
    /// 兼容入口 new 仍处理16k；任意目标率转换交给显式 AudioBranch。
    pub fn native_rate(name: &str) -> Result<Self> {
        let rate = match name {
            "PCMU" | "PCMA" => 8_000,
            "G722" => 16_000,
            "OPUS" => 48_000,
            _ => anyhow::bail!("{name} has no native PCM encoder/decoder; relay only"),
        };
        Self::with_rate(name, rate)
    }
    /// 返回本实例实际 PCM 采样率，调用方不能从 RTP 时钟推导此值。
    pub fn sample_rate(&self) -> u32 {
        self.sample_rate
    }
    /// 返回编码线上时钟；G.722 的16k PCM每20ms只对应160个8k刻度。
    pub fn rtp_clock_rate(&self) -> u32 {
        if self.name == "OPUS" {
            48_000
        } else {
            8_000
        }
    }
    /// 只由两个已验证构造器选择处理率，避免打开未经验证的编码组合。
    fn with_rate(name: &str, sample_rate: u32) -> Result<Self> {
        let backend = match name {
            "PCMU" | "PCMA" => Backend::G711 {
                alaw: name == "PCMA",
                down: (sample_rate == 16_000).then(Box::default),
                up: (sample_rate == 16_000).then(Box::default),
            },
            "G722" | "OPUS" => {
                let key = if name == "G722" {
                    "RUSTSWITCH_G722_LIBRARY"
                } else {
                    "RUSTSWITCH_OPUS_LIBRARY"
                };
                let path = PathBuf::from(
                    std::env::var_os(key).with_context(|| format!("{key} is not configured"))?,
                );
                ensure!(path.is_absolute(), "codec library path must be absolute");
                Backend::Native(native::NativeCodec::load(name, &path, sample_rate)?)
            }
            _ => anyhow::bail!("{name} has no PCM encoder/decoder; relay only"),
        };
        Ok(Self {
            name: name.into(),
            sample_rate,
            backend,
            last: None,
            lost_ms: 0,
            in_comfort_noise: false,
        })
    }
    /// 把规范 PCM 编成一份有效码流；不附 RTP 头，不推进会话序号。
    pub fn encode(&mut self, frame: &AudioFrame) -> Result<Vec<u8>> {
        ensure!(
            frame.sample_rate() == self.sample_rate,
            "PCM rate mismatch; explicitly convert the target branch"
        );
        match &mut self.backend {
            Backend::G711 { alaw, down, .. } => {
                if let Some(down) = down {
                    Ok(down
                        .down(frame.samples())
                        .into_iter()
                        .map(|x| g711::encode(x, *alaw))
                        .collect())
                } else {
                    Ok(frame
                        .samples()
                        .iter()
                        .map(|&x| g711::encode(x, *alaw))
                        .collect())
                }
            }
            Backend::Native(codec) => codec.encode(frame.samples()),
        }
    }
    /// 仅接受单个 10/20ms SDK 帧。CN 原样保留谱参数，当前不实现 RFC3389 噪声合成。
    /// G.711/G.722 使用逐包衰减历史近似；Opus 使用官方 PLC，不把丢失包当作静音。
    pub fn decode(&mut self, input: DecodeInput<'_>, duration_ms: u16) -> Result<AudioOutput> {
        ensure!(
            [10, 20].contains(&duration_ms),
            "SDK supports 10/20ms frames"
        );
        let count = usize::from(duration_ms) * (self.sample_rate as usize / 1000);
        if let DecodeInput::ComfortNoise(sid) = input {
            ensure!(
                !sid.is_empty() && sid.len() <= 8192 && sid[0] & 0x80 == 0,
                "invalid RFC3389 SID"
            );
            // 显式 DTX/CN 中断语音历史，后续缺包不重放 CN 前的话音。
            self.last = None;
            self.lost_ms = 0;
            self.in_comfort_noise = true;
            return Ok(AudioOutput::ComfortNoise {
                duration_ms,
                level_dbov: sid[0],
                spectral_coefficients: sid[1..].to_vec(),
            });
        }
        let (pcm, origin) = match input {
            DecodeInput::Packet(bytes) => {
                ensure!(!bytes.is_empty(), "empty packet is not packet loss");
                let pcm = match &mut self.backend {
                    Backend::G711 { alaw, up, .. } => {
                        ensure!(
                            bytes.len() == usize::from(duration_ms) * 8,
                            "invalid G711 frame length"
                        );
                        let decoded: Vec<_> =
                            bytes.iter().map(|b| g711::decode(*b, *alaw)).collect();
                        if let Some(up) = up {
                            up.up(&decoded)
                        } else {
                            decoded
                        }
                    }
                    Backend::Native(codec) => codec.decode(Some(bytes), count)?,
                };
                self.lost_ms = 0;
                self.in_comfort_noise = false;
                (pcm, FrameOrigin::Decoded)
            }
            DecodeInput::PacketLoss => {
                if self.in_comfort_noise {
                    return Ok(AudioOutput::MissingAudio { duration_ms });
                }
                self.lost_ms = self.lost_ms.saturating_add(u32::from(duration_ms));
                if self.name == "OPUS" {
                    if let Backend::Native(codec) = &mut self.backend {
                        (codec.decode(None, count)?, FrameOrigin::CodecPlc)
                    } else {
                        unreachable!()
                    }
                } else if let Some(last) = &self.last {
                    // 历史只有一帧，时长切换或超过120ms连续损失明确返回缺失，防止无限重复语音。
                    if last.duration_ms() != duration_ms || self.lost_ms > 120 {
                        return Ok(AudioOutput::MissingAudio { duration_ms });
                    }
                    (
                        last.samples()
                            .iter()
                            .map(|x| (i32::from(*x) * 3 / 4) as i16)
                            .collect(),
                        FrameOrigin::HistoryConcealment,
                    )
                } else {
                    return Ok(AudioOutput::MissingAudio { duration_ms });
                }
            }
            DecodeInput::ComfortNoise(_) => unreachable!(),
        };
        let frame = AudioFrame::with_sample_rate(pcm, self.sample_rate)?;
        self.last = Some(frame.clone());
        Ok(AudioOutput::Frame { frame, origin })
    }
}

/// 各 codec 独立探测，缺失库只影响对应条目；此 CLI 不宣称实时转码可用。
pub fn capabilities() -> Value {
    let codecs: Vec<Value> = ["PCMU", "PCMA", "G722", "OPUS", "G729", "G726-16", "G726-24", "G726-32", "G726-40", "AAL2-G726-16", "AAL2-G726-24", "AAL2-G726-32", "AAL2-G726-40", "L16"].iter().map(|name| {
        let result = AudioCodec::new(name);
        let available = result.is_ok();
        let native = AudioCodec::native_rate(name);
        json!({"name":name,"available":available,"encode":available,"decode":available,"plc":available,"reason":result.err().map(|e|e.to_string()).unwrap_or_default(),"plc_mode":if !available{"unavailable"}else if *name=="OPUS"{"codec_native"}else{"bounded_history_concealment"},"relay":true,"native_pcm":{"available":native.is_ok(),"sample_rate":native.as_ref().ok().map(AudioCodec::sample_rate),"rtp_clock_rate":native.as_ref().ok().map(AudioCodec::rtp_clock_rate),"reason":native.err().map(|e|e.to_string()).unwrap_or_default()}})
    }).collect();
    json!({"codecs":codecs,"pcm":{"sample_rate":16000,"channels":1,"sample_format":"s16le","frame_ms":[10,20]},"audio_frame":{"sample_rates":SUPPORTED_SAMPLE_RATES,"channels":1,"sample_format":"s16le","frame_ms":[10,20],"maximum_samples":960,"explicit_branch_resampling":true,"live_audio_graph":false},"live_transcoding":false,"comfort_noise":"RFC3389 SID parsed; no noise synthesis","scope":"offline PCM SDK; legacy 16k entry retained; native-rate decoding and explicit branch conversion are independent of RTP relay"})
}
/// 用连续信号验证真实编码/解码、规范 PCM、PLC 来源和 CN 语义；缺少任意 P0 库判失败。
/// 这是功能自检而非语音主观质量评分，也不是实时会话转码或万路转码性能证明。
pub fn check() -> Result<()> {
    let mut checks = Vec::new();
    let mut passed = true;
    for name in ["PCMU", "PCMA", "G722", "OPUS"] {
        let result = check_one(name);
        match result {
            Ok(mut detail) => match check_one_at(name, true) {
                Ok(native) => {
                    detail["native_pcm"] = native;
                    checks.push(detail);
                }
                Err(error) => {
                    passed = false;
                    checks.push(json!({"name":name,"passed":false,"error":error.to_string()}));
                }
            },
            Err(error) => {
                passed = false;
                checks.push(json!({"name":name,"passed":false,"error":error.to_string()}));
            }
        }
    }
    println!(
        "{}",
        json!({"passed":passed,"checks":checks,"live_transcoding":false,"pcm_sample_rate":16000,"native_pcm_rates":[8000,16000,48000],"branch_sample_rates":SUPPORTED_SAMPLE_RATES,"pcm_channels":1,"pcm_sample_format":"s16le"})
    );
    ensure!(passed, "P0 audio self-check failed");
    Ok(())
}
/// 对每种编码串行处理10/20ms并核对输出非空、长度及显式的缺包/CN标签。
fn check_one(name: &str) -> Result<Value> {
    check_one_at(name, false)
}
/// 在指定兼容/原生模式下分别实际编解码，不以CodecSpec静态声明冒充原生率可用。
fn check_one_at(name: &str, native_rate: bool) -> Result<Value> {
    let mut codec = if native_rate {
        AudioCodec::native_rate(name)?
    } else {
        AudioCodec::new(name)?
    };
    let sample_rate = codec.sample_rate();
    let per_ms = sample_rate as usize / 1000;
    let mut encoded_bytes = 0;
    let mut energy = 0u64;
    let mut input_signal = Vec::new();
    let mut decoded_signal = Vec::new();
    for duration in [10u16, 20] {
        for index in 0..20 {
            let pcm = (0..usize::from(duration) * per_ms)
                .map(|n| {
                    ((2. * std::f64::consts::PI
                        * 440.
                        * (n + index * usize::from(duration) * per_ms) as f64
                        / f64::from(sample_rate))
                    .sin()
                        * 12000.) as i16
                })
                .collect();
            let frame = AudioFrame::with_sample_rate(pcm, sample_rate)?;
            ensure!(
                AudioFrame::from_s16le_at(&frame.to_s16le(), sample_rate)?.samples()
                    == frame.samples(),
                "PCM endian mismatch"
            );
            input_signal.extend_from_slice(frame.samples());
            let packet = codec.encode(&frame)?;
            encoded_bytes += packet.len();
            match codec.decode(DecodeInput::Packet(&packet), duration)? {
                AudioOutput::Frame {
                    frame,
                    origin: FrameOrigin::Decoded,
                } => {
                    ensure!(
                        frame.samples().len() == usize::from(duration) * per_ms,
                        "wrong PCM length"
                    );
                    decoded_signal.extend_from_slice(frame.samples());
                    energy += frame
                        .samples()
                        .iter()
                        .map(|x| i64::from(*x).unsigned_abs())
                        .sum::<u64>();
                }
                _ => anyhow::bail!("wrong decoded output"),
            }
        }
    }
    ensure!(
        energy > 100000,
        "decoded test signal has insufficient energy"
    );
    // 编解码与FIR有算法延迟，在有界范围内对齐后核对信号相关性，不要求有损字节完全相同。
    let correlation = (0..per_ms * 20)
        .map(|lag| {
            let mut cross = 0.;
            let mut a2 = 0.;
            let mut b2 = 0.;
            for (&a, &b) in input_signal
                .iter()
                .zip(decoded_signal.iter().skip(lag))
                .skip(per_ms * 40)
            {
                let a = f64::from(a);
                let b = f64::from(b);
                cross += a * b;
                a2 += a * a;
                b2 += b * b;
            }
            cross / (a2 * b2).sqrt().max(1.)
        })
        .fold(0f64, f64::max);
    ensure!(
        correlation > 0.85,
        "decoded signal correlation too low: {correlation}"
    );
    let plc = codec.decode(DecodeInput::PacketLoss, 20)?;
    ensure!(
        matches!(
            plc,
            AudioOutput::Frame {
                origin: FrameOrigin::CodecPlc | FrameOrigin::HistoryConcealment,
                ..
            }
        ),
        "PLC provenance missing"
    );
    ensure!(
        matches!(
            codec.decode(DecodeInput::ComfortNoise(&[37, 4]), 20)?,
            AudioOutput::ComfortNoise { level_dbov: 37, .. }
        ),
        "CN was confused with silence"
    );
    ensure!(
        matches!(
            codec.decode(DecodeInput::PacketLoss, 20)?,
            AudioOutput::MissingAudio { .. }
        ),
        "loss after CN replayed earlier speech"
    );
    // 外部发生器使用的合法20ms码字独立验证，避免只验证自己刚编码的输出。
    if name == "OPUS" || name == "G722" {
        let fixture = if name == "OPUS" {
            vec![0xf8, 0xff, 0xfe]
        } else {
            vec![0xfa; 160]
        };
        let mut fresh = if native_rate {
            AudioCodec::native_rate(name)?
        } else {
            AudioCodec::new(name)?
        };
        ensure!(
            matches!(fresh.decode(DecodeInput::Packet(&fixture),20)?,AudioOutput::Frame{frame,..} if frame.samples().len()==per_ms*20),
            "fixture did not decode to 20ms"
        );
    }
    ensure!(
        codec.decode(DecodeInput::Packet(&[]), 20).is_err(),
        "empty packet became loss"
    );
    Ok(
        json!({"name":name,"passed":true,"sample_rate":sample_rate,"rtp_clock_rate":codec.rtp_clock_rate(),"frames":40,"durations_ms":[10,20],"encoded_bytes":encoded_bytes,"decoded_absolute_energy":energy,"aligned_signal_correlation":correlation,"plc":true,"cn_is_separate":true,"loss_after_cn_is_missing":true,"generator_fixture_20ms":name=="OPUS"||name=="G722"}),
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    /// 固定帧长度、小端字节与丢包来源必须可验证；首次缺包不能伪造正常静音。
    fn canonical_frame_and_loss_semantics() {
        let frame = AudioFrame::new(vec![0x1234; 160]).unwrap();
        assert_eq!(&frame.to_s16le()[..2], &[0x34, 0x12]);
        assert!(AudioFrame::new(vec![0; 80]).is_err());
        let mut codec = AudioCodec::new("PCMU").unwrap();
        assert!(matches!(
            codec.decode(DecodeInput::PacketLoss, 20).unwrap(),
            AudioOutput::MissingAudio { .. }
        ));
        let bytes = codec
            .encode(&AudioFrame::new(vec![1000; 320]).unwrap())
            .unwrap();
        codec.decode(DecodeInput::Packet(&bytes), 20).unwrap();
        assert!(matches!(
            codec.decode(DecodeInput::PacketLoss, 20).unwrap(),
            AudioOutput::Frame {
                origin: FrameOrigin::HistoryConcealment,
                ..
            }
        ));
        assert!(matches!(
            codec.decode(DecodeInput::ComfortNoise(&[30]), 20).unwrap(),
            AudioOutput::ComfortNoise { .. }
        ));
        assert!(matches!(
            codec.decode(DecodeInput::PacketLoss, 20).unwrap(),
            AudioOutput::MissingAudio { .. }
        ));
        assert!(codec
            .decode(DecodeInput::ComfortNoise(&[0x80]), 20)
            .is_err());
        assert!(AudioCodec::new("G729").is_err());
    }
    #[test]
    /// 无外部依赖的 G.711 全流程自检必须一直运行，不能因缺少原生库而跳过。
    fn g711_real_roundtrip() {
        for name in ["PCMU", "PCMA"] {
            check_one(name).unwrap();
            check_one_at(name, true).unwrap();
        }
    }
    #[test]
    /// 显式配置原生库时执行真编解码；未配置时由 check-audio 严格验收缺失，不假装已验证。
    fn configured_native_roundtrip() {
        for (name, key) in [
            ("G722", "RUSTSWITCH_G722_LIBRARY"),
            ("OPUS", "RUSTSWITCH_OPUS_LIBRARY"),
        ] {
            if std::env::var_os(key).is_some() {
                check_one(name).unwrap();
                check_one_at(name, true).unwrap();
            }
        }
    }
    #[test]
    /// 原生G.711码字直接还原8k样本，不能先偷偷升到16k再降回；错误输入率不写编码状态。
    fn native_g711_keeps_eight_kilohertz_samples() {
        for name in ["PCMU", "PCMA"] {
            let alaw = name == "PCMA";
            let packet: Vec<u8> = (0..160).map(|n| (n * 29) as u8).collect();
            let mut codec = AudioCodec::native_rate(name).unwrap();
            assert_eq!(
                (codec.sample_rate(), codec.rtp_clock_rate()),
                (8_000, 8_000)
            );
            let output = codec.decode(DecodeInput::Packet(&packet), 20).unwrap();
            let AudioOutput::Frame { frame, origin } = output else {
                panic!("missing decoded frame")
            };
            assert_eq!(origin, FrameOrigin::Decoded);
            assert_eq!(
                frame.samples(),
                packet
                    .iter()
                    .map(|&n| g711::decode(n, alaw))
                    .collect::<Vec<_>>()
            );
            assert_eq!(frame.valid_samples(), 160);
            assert_eq!(codec.encode(&frame).unwrap().len(), 160);
            assert!(codec
                .encode(&AudioFrame::new(vec![1; 320]).unwrap())
                .is_err());
            assert!(codec
                .decode(DecodeInput::Packet(&packet[..159]), 20)
                .is_err());
        }
    }
    #[test]
    /// 原生Opus在48k真解码；错误10/20ms声明预检失败后，正确包解码与全新状态完全相同。
    fn configured_opus_native_duration_error_preserves_decoder() {
        if std::env::var_os("RUSTSWITCH_OPUS_LIBRARY").is_none() {
            return;
        }
        let mut encoder = AudioCodec::native_rate("OPUS").unwrap();
        let mut decoder = AudioCodec::native_rate("OPUS").unwrap();
        let mut untouched = AudioCodec::native_rate("OPUS").unwrap();
        let input: Vec<_> = (0..960)
            .map(|n| {
                ((n as f64 * 2. * std::f64::consts::PI * 1000. / 48_000.).sin() * 12_000.) as i16
            })
            .collect();
        let packet = encoder
            .encode(&AudioFrame::with_sample_rate(input, 48_000).unwrap())
            .unwrap();
        assert!(decoder.decode(DecodeInput::Packet(&packet), 10).is_err());
        let a = decoder.decode(DecodeInput::Packet(&packet), 20).unwrap();
        let b = untouched.decode(DecodeInput::Packet(&packet), 20).unwrap();
        match (a, b) {
            (AudioOutput::Frame { frame: a, .. }, AudioOutput::Frame { frame: b, .. }) => {
                assert_eq!(a.samples(), b.samples());
                assert_eq!(a.valid_samples(), 960);
            }
            _ => panic!("packet failed to decode"),
        }
    }
    #[test]
    /// 24k的离线TTS样本只在明确的目标分支上转成48k，再由真实Opus库编码与解码。
    fn configured_tts_twenty_four_k_to_opus() {
        if std::env::var_os("RUSTSWITCH_OPUS_LIBRARY").is_none() {
            return;
        }
        let mut branch = branch::AudioBranch::new(24_000, 48_000).unwrap();
        let mut codec = AudioCodec::native_rate("OPUS").unwrap();
        let samples: Vec<_> = (0..480)
            .map(|n| {
                ((n as f64 * 2. * std::f64::consts::PI * 800. / 24_000.).sin() * 12_000.) as i16
            })
            .collect();
        let input = branch::AudioBlock::new(
            AudioOutput::Frame {
                frame: AudioFrame::with_sample_rate(samples, 24_000).unwrap(),
                origin: FrameOrigin::SuppliedPcm,
            },
            AudioPosition::new(0, None),
        )
        .unwrap();
        let AudioOutput::Frame { frame, .. } = branch.process(input).unwrap().into_output() else {
            panic!("converted frame missing")
        };
        let packet = codec.encode(&frame).unwrap();
        let output = codec.decode(DecodeInput::Packet(&packet), 20).unwrap();
        assert!(
            matches!(output,AudioOutput::Frame{frame,origin:FrameOrigin::Decoded} if frame.valid_samples()==960 && frame.samples().iter().any(|&n|n.abs()>100))
        );
    }
    #[test]
    /// 交替10/20ms的连续损失也按累计时长耗尽120ms预算，不能切换帧长延长历史重放。
    fn mixed_duration_loss_uses_elapsed_budget() {
        let mut codec = AudioCodec::native_rate("PCMU").unwrap();
        codec.decode(DecodeInput::Packet(&[0x80; 160]), 20).unwrap();
        for _ in 0..5 {
            codec.decode(DecodeInput::PacketLoss, 20).unwrap();
        }
        assert!(matches!(
            codec.decode(DecodeInput::PacketLoss, 10).unwrap(),
            AudioOutput::MissingAudio { duration_ms: 10 }
        ));
        assert!(matches!(
            codec.decode(DecodeInput::PacketLoss, 20).unwrap(),
            AudioOutput::MissingAudio { duration_ms: 20 }
        ));
        assert_eq!(codec.lost_ms, 130);
    }
}
