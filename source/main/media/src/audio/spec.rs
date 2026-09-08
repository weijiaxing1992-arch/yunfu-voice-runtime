//! 编码名称、PCM 采样率与 RTP 时钟分别建模；直通协商不受离线 PCM 帧长限制。
use anyhow::{ensure, Result};
use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
/// 协商中的编码描述；OPUS 的 channels=2 是 RTP 声明，SDK 可解码为单声道。
pub struct CodecSpec {
    /// 规范化的大写名称，G729A 使用同码流名称 G729。
    pub name: String,
    /// 编码名义 PCM 采样率；例如 G722 为 16000。
    pub sample_rate: u32,
    /// RTP 时间戳每秒刻度；例如 G722 为 8000，OPUS 恒为 48000。
    pub rtp_clock_rate: u32,
    /// SDP 声道数；不能用内部归一化后的单声道覆盖线上声明。
    pub channels: u8,
    /// SDP 包时长；OPUS 的 3 表示 SDP 向上取整声明的 2.5 ms。
    pub ptime_ms: u16,
    /// 已协商格式参数原文；本层不把它解释为插件加载指令。
    pub fmtp: String,
}
impl CodecSpec {
    /// 旧版 IPC 只允许静态 G.711；动态载荷必须附明确编码描述。
    pub fn legacy(payload: u8) -> Result<Self> {
        ensure!(
            payload == 0 || payload == 8,
            "dynamic payload requires codec metadata"
        );
        Ok(Self {
            name: if payload == 0 { "PCMU" } else { "PCMA" }.into(),
            sample_rate: 8000,
            rtp_clock_rate: 8000,
            channels: 1,
            ptime_ms: 20,
            fmtp: String::new(),
        })
    }
    /// 验证当前直通支持的线上组合，不加载任何解码器，不声称解码能力。
    pub fn validate(&self, payload: u8) -> Result<()> {
        ensure!(
            payload <= 127 && self.fmtp.len() <= 2048,
            "invalid payload or fmtp"
        );
        let valid = match self.name.as_str() {
            "PCMU" | "PCMA" | "G729" | "G726-16" | "G726-24" | "G726-32" | "G726-40"
            | "AAL2-G726-16" | "AAL2-G726-24" | "AAL2-G726-32" | "AAL2-G726-40" => {
                self.sample_rate == 8000 && self.rtp_clock_rate == 8000 && self.channels == 1
            }
            "G722" => {
                self.sample_rate == 16000 && self.rtp_clock_rate == 8000 && self.channels == 1
            }
            "OPUS" => {
                self.sample_rate == 48000 && self.rtp_clock_rate == 48000 && self.channels == 2
            }
            "L16" => {
                [8000, 16000, 32000, 44100, 48000].contains(&self.sample_rate)
                    && self.rtp_clock_rate == self.sample_rate
                    && [1, 2].contains(&self.channels)
            }
            _ => false,
        };
        ensure!(valid, "unsupported codec clock/channel combination");
        let valid_pt = if payload >= 96 {
            true
        } else {
            match payload {
                0 => self.name == "PCMU",
                8 => self.name == "PCMA",
                9 => self.name == "G722",
                18 => self.name == "G729",
                10 => self.name == "L16" && self.sample_rate == 44100 && self.channels == 2,
                11 => self.name == "L16" && self.sample_rate == 44100 && self.channels == 1,
                _ => false,
            }
        };
        ensure!(valid_pt, "static payload does not match codec");
        ensure!(
            match self.name.as_str() {
                "OPUS" =>
                    [3, 5].contains(&self.ptime_ms)
                        || (self.ptime_ms >= 10 && self.ptime_ms <= 120 && self.ptime_ms % 10 == 0),
                "L16" => [10, 20].contains(&self.ptime_ms),
                _ => self.ptime_ms >= 10 && self.ptime_ms <= 60 && self.ptime_ms % 10 == 0,
            },
            "unsupported packet duration"
        );
        Ok(())
    }
    /// 同编码透传不改 RTP 时钟或声道；fmtp 的方向性窄化由 SIP 协商层完成。
    pub fn same_stream(&self, other: &Self) -> bool {
        self.name == other.name
            && self.sample_rate == other.sample_rate
            && self.rtp_clock_rate == other.rtp_clock_rate
            && self.channels == other.channels
            && self.ptime_ms == other.ptime_ms
    }
    /// 将本次 PCM 时长换算到线上时间戳；不把 G.722 的 16k PCM 当作 16k RTP。
    pub fn rtp_ticks(&self, milliseconds: u16) -> u32 {
        self.rtp_clock_rate * u32::from(milliseconds) / 1000
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]
/// 展开到 Allocate/Connect 顶层的可选元数据；省略保持旧 G.711 IPC 兼容。
pub struct MediaFormat {
    /// 省略仅用于兼容旧PCMU/PCMA，不以载荷编号猜测动态编码。
    #[serde(default)]
    pub codec: Option<CodecSpec>,
    /// telephone-event 自身时钟，与主编码分别声明。
    pub dtmf_clock_rate: Option<u32>,
    /// 协商事件集合字符串，例如 0-15；默认 RFC 常用的 0-15。
    pub dtmf_events: Option<String>,
    /// 独立 RFC3389 CN 载荷类型；Opus DTX 不等同于这个载荷。
    pub cn_payload: Option<u8>,
    /// CN线上时钟必须与主音频时钟一致；静态13限定8000。
    pub cn_clock_rate: Option<u32>,
}
impl MediaFormat {
    /// 校验辅流类型不碰撞、CN 同钟以及 DTMF 事件范围；返回规范化主编码。
    pub fn validate(&self, payload: u8, dtmf: Option<u8>) -> Result<CodecSpec> {
        let codec = self
            .codec
            .clone()
            .map(Ok)
            .unwrap_or_else(|| CodecSpec::legacy(payload))?;
        codec.validate(payload)?;
        if let Some(pt) = dtmf {
            ensure!(
                (96..=127).contains(&pt) && pt != payload && Some(pt) != self.cn_payload,
                "invalid DTMF payload"
            );
            ensure!(
                [8000, 16000, 32000, 44100, 48000].contains(&self.dtmf_clock_rate.unwrap_or(8000)),
                "invalid DTMF clock"
            );
            event_mask(self.dtmf_events.as_deref().unwrap_or("0-15"))?;
        } else {
            ensure!(
                self.dtmf_clock_rate.is_none() && self.dtmf_events.is_none(),
                "DTMF metadata without payload"
            );
        }
        if let Some(pt) = self.cn_payload {
            ensure!(
                (pt == 13 || (96..=127).contains(&pt)) && pt != payload,
                "invalid CN payload"
            );
            ensure!(
                self.cn_clock_rate == Some(codec.rtp_clock_rate),
                "CN clock must match main RTP clock"
            );
            ensure!(
                pt != 13 || codec.rtp_clock_rate == 8000,
                "static CN requires 8000 clock"
            );
        } else {
            ensure!(self.cn_clock_rate.is_none(), "CN clock without payload");
        }
        Ok(codec)
    }
}
/// 严格读取 0..16 的 telephone-event 集合；不会把无效字符串扩大成全事件。
pub fn event_mask(text: &str) -> Result<u32> {
    ensure!(
        !text.is_empty() && text.len() <= 128,
        "invalid telephone-event set"
    );
    let mut mask = 0;
    for part in text.split(',') {
        let mut range = part.split('-');
        let first = range.next().unwrap();
        ensure!(
            !first.is_empty() && first.bytes().all(|b| b.is_ascii_digit()),
            "invalid event number"
        );
        let lo: u8 = first.parse()?;
        let hi = if let Some(end) = range.next() {
            ensure!(
                !end.is_empty() && end.bytes().all(|b| b.is_ascii_digit()),
                "invalid event range"
            );
            end.parse()?
        } else {
            lo
        };
        ensure!(
            range.next().is_none() && lo <= hi && hi <= 16,
            "invalid event range"
        );
        for n in lo..=hi {
            mask |= 1 << n;
        }
    }
    Ok(mask)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    /// G.722 20ms 产生 320 PCM 样本却只推进 160 RTP 刻度。
    fn clocks_and_relay_profiles() {
        let mut spec = CodecSpec::legacy(0).unwrap();
        spec.name = "G722".into();
        spec.sample_rate = 16000;
        spec.validate(9).unwrap();
        assert_eq!(spec.rtp_ticks(20), 160);
        assert!(spec.validate(0).is_err());
        for name in ["G729", "G726-32", "AAL2-G726-24"] {
            spec.name = name.into();
            spec.sample_rate = 8000;
            spec.validate(110).unwrap();
        }
        assert!(CodecSpec::legacy(110).is_err());
    }
    #[test]
    /// DTMF 事件集合必须精确，辅助载荷不能与主音频碰撞。
    fn auxiliary_contracts() {
        assert_eq!(event_mask("0-9,10-16").unwrap(), 0x1ffff);
        for text in ["", "17", "2-1", "1-2-3", "-1", "0,", " 0"] {
            assert!(event_mask(text).is_err());
        }
        let f = MediaFormat {
            cn_payload: Some(13),
            cn_clock_rate: Some(16000),
            ..Default::default()
        };
        assert!(f.validate(0, None).is_err());
    }
}
