//! 转发前的轻量报文结构检查；不验证负载内容、SSRC 身份或完整 RTCP 各子类型语义。

/// 检查 RTP 基本头、CSRC 列表、扩展区与填充的长度边界后返回负载类型。
/// 全程借用输入切片，不分配、不复制，也不会因为长度字段超出输入而越界读取。
pub fn payload_type(packet: &[u8]) -> Option<u8> {
    payload(packet).map(|(pt, _)| pt)
}

/// 未解释的RTP扩展，格式编号与数据始终借用已校验的输入。
#[derive(Clone, Copy, Debug)]
pub struct ExtensionView<'a> {
    pub profile: u16,
    pub data: &'a [u8],
}

/// 解码前共用的借用视图；不分配、不复制，生命周期不能超过接收缓冲。
#[derive(Clone, Copy, Debug)]
pub struct PacketView<'a> {
    pub marker: bool,
    pub payload_type: u8,
    pub sequence: u16,
    pub timestamp: u32,
    pub ssrc: u32,
    pub payload: &'a [u8],
    pub header_len: usize,
    pub csrc_bytes: &'a [u8],
    pub extension: Option<ExtensionView<'a>>,
    pub padding: usize,
}
impl<'a> PacketView<'a> {
    /// 验证版本、CSRC、扩展与尾填充后才暴露负载；不把头部长度误作固定12字节。
    pub fn parse(packet: &'a [u8]) -> Option<Self> {
        if packet.len() < 12 || packet[0] >> 6 != 2 {
            return None;
        }
        let csrc_end = 12 + usize::from(packet[0] & 0x0f) * 4;
        if csrc_end > packet.len() {
            return None;
        }
        let mut header = csrc_end;
        let extension = if packet[0] & 0x10 != 0 {
            if packet.len() < header + 4 {
                return None;
            }
            let profile = u16::from_be_bytes([packet[header], packet[header + 1]]);
            let words = usize::from(u16::from_be_bytes([packet[header + 2], packet[header + 3]]));
            let begin = header + 4;
            header = begin + words * 4;
            if header > packet.len() {
                return None;
            }
            Some(ExtensionView {
                profile,
                data: &packet[begin..header],
            })
        } else {
            None
        };
        let padding = if packet[0] & 0x20 != 0 {
            let padding = usize::from(*packet.last()?);
            if padding == 0 || padding > packet.len() - header {
                return None;
            }
            padding
        } else {
            0
        };
        Some(Self {
            marker: packet[1] & 0x80 != 0,
            payload_type: packet[1] & 0x7f,
            sequence: u16::from_be_bytes([packet[2], packet[3]]),
            timestamp: u32::from_be_bytes(packet[4..8].try_into().ok()?),
            ssrc: u32::from_be_bytes(packet[8..12].try_into().ok()?),
            payload: &packet[header..packet.len() - padding],
            header_len: header,
            csrc_bytes: &packet[12..csrc_end],
            extension,
            padding,
        })
    }
}

/// 旧转发入口保持相同借用与边界行为；新处理模式直接复用完整PacketView。
pub fn payload(packet: &[u8]) -> Option<(u8, &[u8])> {
    PacketView::parse(packet).map(|view| (view.payload_type, view.payload))
}

/// 逐个检查复合 RTCP 包的长度、版本、类型范围与常见报告的最小长度。
/// 此检查只为安全转发提供边界保障，不生成 RTCP 报告，也不完成 SDES/BYE 内容解析。
pub fn valid_rtcp(mut packet: &[u8]) -> bool {
    if packet.is_empty() {
        return false;
    }
    while !packet.is_empty() {
        if packet.len() < 4 || packet[0] >> 6 != 2 || !(192..=223).contains(&packet[1]) {
            return false;
        }
        let len = (usize::from(u16::from_be_bytes([packet[2], packet[3]])) + 1) * 4;
        if len > packet.len() {
            return false;
        }
        let count = usize::from(packet[0] & 0x1f);
        // SR/RR 的报告块数量决定最小长度；其他类型只执行当前支持的结构下限检查。
        let minimum = match packet[1] {
            200 => 28 + 24 * count,
            201 => 8 + 24 * count,
            202 | 203 => 4 + count * 4,
            _ => 8,
        };
        if len < minimum {
            return false;
        }
        if packet[0] & 0x20 != 0 {
            // 复合包的填充只允许出现在最后一个子包，并且不能吞掉必要字段。
            if len != packet.len() {
                return false;
            }
            let padding = usize::from(packet[len - 1]);
            if padding == 0 || padding > len - minimum {
                return false;
            }
        }
        packet = &packet[len..];
    }
    true
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn borrowed_view_keeps_csrc_extension_and_padding_boundaries() {
        let packet = [
            0xb1, 0x88, 0x12, 0x34, 0, 0, 1, 2, 0, 0, 0, 9, 1, 2, 3, 4, 0xbe, 0xde, 0, 1, 9, 8, 7,
            6, 0xaa, 0xbb, 0, 2,
        ];
        let view = PacketView::parse(&packet).unwrap();
        assert!(view.marker);
        assert_eq!(view.payload_type, 8);
        assert_eq!(view.sequence, 0x1234);
        assert_eq!(view.timestamp, 258);
        assert_eq!(view.ssrc, 9);
        assert_eq!(view.header_len, 24);
        assert_eq!(view.csrc_bytes, &packet[12..16]);
        assert_eq!(view.extension.unwrap().profile, 0xbede);
        assert_eq!(view.extension.unwrap().data, &packet[20..24]);
        assert_eq!(view.padding, 2);
        assert_eq!(view.payload, &[0xaa, 0xbb]);
        assert_eq!(view.payload.as_ptr(), packet[24..].as_ptr());
        for len in 0..24 {
            assert!(PacketView::parse(&packet[..len]).is_none());
        }
    }
    #[test]
    /// 覆盖基本 RTP 头、扩展、填充和 CSRC 长度不足的拒绝路径。
    fn malformed_rtp_is_rejected() {
        let mut p = [0u8; 12];
        p[0] = 0x80;
        assert_eq!(payload_type(&p), Some(0));
        p[0] = 0x90;
        assert_eq!(payload_type(&p), None);
        p[0] = 0xa0;
        assert_eq!(payload_type(&p), None);
        p[0] = 0x8f;
        assert_eq!(payload_type(&p), None);
        assert_eq!(payload_type(&[0x80; 11]), None);
    }
    #[test]
    /// 验证完整 RR 及复合 RR 可通过，截断或报告数不一致会被拒绝。
    fn rtcp_bounds_and_compounds() {
        let rr = [0x80, 201, 0, 1, 0, 0, 0, 1];
        assert!(valid_rtcp(&rr));
        assert!(valid_rtcp(&[rr, rr].concat()));
        assert!(!valid_rtcp(&rr[..7]));
        assert!(!valid_rtcp(&[0x81, 201, 0, 1, 0, 0, 0, 1]));
        assert!(!valid_rtcp(&[]));
    }
    #[test]
    /// 用可复现的短随机字节覆盖边界组合，确认解析器不会因任意输入而 panic。
    fn arbitrary_short_packets_do_not_panic() {
        let mut state = 0x12345678u32;
        for len in 0..2048 {
            let bytes: Vec<u8> = (0..len)
                .map(|_| {
                    state ^= state << 13;
                    state ^= state >> 17;
                    state ^= state << 5;
                    state as u8
                })
                .collect();
            let _ = payload_type(&bytes);
            let _ = valid_rtcp(&bytes);
        }
    }
}
