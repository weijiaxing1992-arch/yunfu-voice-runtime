//! 处理模式的本机RTCP终结：真实收发统计生成SR/RR与CNAME，绝不沿用对端发送身份。
use super::rtp_state::{ReceiverReport, RxRtpState, TxRtpState};
use anyhow::{bail, ensure, Result};
use std::time::{Instant, SystemTime, UNIX_EPOCH};

const MAX_COMPOUND_BYTES: usize = 2048;
const MAX_COMPOUND_PACKETS: usize = 32;
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct RtcpObservation {
    pub sender_reports: usize,
    pub receiver_reports: usize,
    pub source_descriptions: usize,
    pub bye_packets: usize,
    pub report_blocks: usize,
    pub remote_bye: bool,
}
/// 发送过RTP才生成SR；零发送用RR。接收报告块对应反向RX，CNAME属于本TX身份。
pub fn write_report(
    tx: &TxRtpState,
    rx: Option<ReceiverReport>,
    now: Instant,
    wall: SystemTime,
    cname: &str,
    out: &mut [u8],
) -> Result<usize> {
    write_compound(tx, rx, now, wall, cname, out, false)
}
/// 释放时输出一次完整报告+SDES+BYE；是否重试由调用方拥有的有界网络队列决定。
pub fn write_bye(
    tx: &TxRtpState,
    rx: Option<ReceiverReport>,
    now: Instant,
    wall: SystemTime,
    cname: &str,
    out: &mut [u8],
) -> Result<usize> {
    write_compound(tx, rx, now, wall, cname, out, true)
}
fn write_compound(
    tx: &TxRtpState,
    rx: Option<ReceiverReport>,
    now: Instant,
    wall: SystemTime,
    cname: &str,
    out: &mut [u8],
    bye: bool,
) -> Result<usize> {
    ensure!(
        !cname.is_empty() && cname.len() <= 255 && !cname.chars().any(char::is_control),
        "invalid RTCP CNAME"
    );
    let value = tx.snapshot();
    let sender = value.sent_packets > 0;
    let report_size = if sender { 28 } else { 8 } + if rx.is_some() { 24 } else { 0 };
    let sdes_size = (4 + 4 + 2 + cname.len() + 1).next_multiple_of(4);
    let len = report_size + sdes_size + if bye { 8 } else { 0 };
    ensure!(len <= out.len(), "short RTCP output buffer");
    let ntp = if sender { Some(to_ntp(wall)?) } else { None };
    out[..len].fill(0);
    out[0] = 0x80 | u8::from(rx.is_some());
    out[1] = if sender { 200 } else { 201 };
    write16(out, 2, (report_size / 4 - 1) as u16);
    write32(out, 4, value.ssrc);
    if let Some(ntp) = ntp {
        out[8..16].copy_from_slice(&ntp.to_be_bytes());
        let elapsed = value.clock_at.map_or(0, |at| {
            (now.saturating_duration_since(at).as_nanos() * u128::from(value.clock_rate)
                / 1_000_000_000) as u32
        });
        write32(out, 16, value.clock_timestamp.wrapping_add(elapsed));
        write32(out, 20, value.sent_packets as u32);
        write32(out, 24, value.sent_octets as u32);
    }
    if let Some(rx) = rx {
        write_receiver(out, if sender { 28 } else { 8 }, rx);
    }
    let at = report_size;
    out[at] = 0x81;
    out[at + 1] = 202;
    write16(out, at + 2, (sdes_size / 4 - 1) as u16);
    write32(out, at + 4, value.ssrc);
    out[at + 8] = 1;
    out[at + 9] = cname.len() as u8;
    out[at + 10..at + 10 + cname.len()].copy_from_slice(cname.as_bytes());
    if bye {
        let at = report_size + sdes_size;
        out[at] = 0x81;
        out[at + 1] = 203;
        write16(out, at + 2, 1);
        write32(out, at + 4, value.ssrc);
    }
    Ok(len)
}
fn to_ntp(wall: SystemTime) -> Result<u64> {
    let elapsed = wall.duration_since(UNIX_EPOCH)?;
    let seconds = elapsed.as_secs().wrapping_add(2_208_988_800) as u32;
    let fraction = ((u64::from(elapsed.subsec_nanos()) << 32) / 1_000_000_000) as u32;
    Ok((u64::from(seconds) << 32) | u64::from(fraction))
}
fn write16(out: &mut [u8], at: usize, value: u16) {
    out[at..at + 2].copy_from_slice(&value.to_be_bytes());
}
fn write32(out: &mut [u8], at: usize, value: u32) {
    out[at..at + 4].copy_from_slice(&value.to_be_bytes());
}
fn read16(packet: &[u8], at: usize) -> u16 {
    u16::from_be_bytes([packet[at], packet[at + 1]])
}
fn read32(packet: &[u8], at: usize) -> u32 {
    u32::from_be_bytes(packet[at..at + 4].try_into().unwrap())
}
fn write_receiver(out: &mut [u8], at: usize, rx: ReceiverReport) {
    write32(out, at, rx.ssrc);
    out[at + 4] = rx.fraction_lost;
    let lost = rx.cumulative_lost.clamp(-8388608, 8388607).to_be_bytes();
    out[at + 5..at + 8].copy_from_slice(&lost[1..]);
    write32(out, at + 8, rx.extended_highest_sequence);
    write32(out, at + 12, rx.jitter);
    write32(out, at + 16, rx.last_sr);
    write32(out, at + 20, rx.delay_since_last_sr);
}
/// 首轮完整校验后才更新LSR，尾部畸形包不会留下部分已接受的控制状态。
/// 有效未知RTCP类型按长度跳过；本阶段只解释SR/RR/SDES/BYE，不冒充反馈或加密支持。
pub fn observe(packet: &[u8], rx: &mut RxRtpState, now: Instant) -> Result<RtcpObservation> {
    ensure!(
        !packet.is_empty() && packet.len() <= MAX_COMPOUND_BYTES && packet.len() % 4 == 0,
        "invalid RTCP compound size"
    );
    let mut at = 0;
    let mut count = 0;
    while at < packet.len() {
        let (part, wire_len) = part_at(packet, at)?;
        if at == 0 {
            ensure!(
                matches!(part[1], 200 | 201),
                "RTCP compound must start with SR/RR"
            );
        }
        validate_part(part)?;
        count += 1;
        ensure!(
            count <= MAX_COMPOUND_PACKETS,
            "too many RTCP compound packets"
        );
        at += wire_len;
    }
    let mut result = RtcpObservation::default();
    at = 0;
    while at < packet.len() {
        let (part, wire_len) = part_at(packet, at)?;
        let count = usize::from(part[0] & 31);
        match part[1] {
            200 => {
                result.sender_reports += 1;
                result.report_blocks += count;
                rx.note_sender_report(
                    read32(part, 4),
                    u64::from_be_bytes(part[8..16].try_into().unwrap()),
                    now,
                );
            }
            201 => {
                result.receiver_reports += 1;
                result.report_blocks += count;
            }
            202 => result.source_descriptions += count,
            203 => {
                result.bye_packets += 1;
                for i in 0..count {
                    if rx.ssrc() == Some(read32(part, 4 + i * 4)) {
                        result.remote_bye = true;
                    }
                }
            }
            _ => {}
        }
        at += wire_len;
    }
    Ok(result)
}
fn part_at(packet: &[u8], at: usize) -> Result<(&[u8], usize)> {
    ensure!(packet.len() - at >= 4, "truncated RTCP header");
    let header = &packet[at..];
    ensure!(
        header[0] >> 6 == 2 && (192..=223).contains(&header[1]),
        "invalid RTCP header"
    );
    let wire_len = (usize::from(read16(header, 2)) + 1) * 4;
    ensure!(wire_len <= header.len(), "truncated RTCP body");
    let mut len = wire_len;
    if header[0] & 0x20 != 0 {
        ensure!(
            at + wire_len == packet.len(),
            "RTCP padding before final packet"
        );
        let padding = usize::from(header[wire_len - 1]);
        ensure!(
            padding > 0 && padding <= wire_len - 4,
            "invalid RTCP padding"
        );
        len -= padding;
    }
    Ok((&header[..len], wire_len))
}
fn validate_part(packet: &[u8]) -> Result<()> {
    let count = usize::from(packet[0] & 31);
    match packet[1] {
        200 => ensure!(packet.len() == 28 + 24 * count, "invalid RTCP SR length"),
        201 => ensure!(packet.len() == 8 + 24 * count, "invalid RTCP RR length"),
        202 => {
            let mut at = 4;
            for _ in 0..count {
                ensure!(
                    packet.len().saturating_sub(at) >= 4,
                    "truncated RTCP SDES source"
                );
                at += 4;
                loop {
                    ensure!(at < packet.len(), "unterminated RTCP SDES chunk");
                    let kind = packet[at];
                    at += 1;
                    if kind == 0 {
                        while at % 4 != 0 {
                            ensure!(
                                at < packet.len() && packet[at] == 0,
                                "invalid RTCP SDES alignment"
                            );
                            at += 1;
                        }
                        break;
                    }
                    ensure!(at < packet.len(), "truncated RTCP SDES item");
                    let len = usize::from(packet[at]);
                    at += 1;
                    ensure!(
                        len <= packet.len().saturating_sub(at),
                        "truncated RTCP SDES value"
                    );
                    at += len;
                }
            }
            ensure!(at == packet.len(), "trailing RTCP SDES bytes");
        }
        203 => {
            ensure!(
                count > 0 && packet.len() >= 4 + count * 4,
                "invalid RTCP BYE sources"
            );
            let at = 4 + count * 4;
            if at < packet.len() {
                let end = at + 1 + usize::from(packet[at]);
                ensure!(
                    end <= packet.len()
                        && packet.len() - end <= 3
                        && packet[end..].iter().all(|v| *v == 0),
                    "invalid RTCP BYE reason"
                );
            }
        }
        204 => ensure!(packet.len() >= 12, "invalid RTCP APP length"),
        205 | 206 => ensure!(packet.len() >= 12, "invalid RTCP feedback length"),
        207 => ensure!(packet.len() >= 8, "invalid RTCP XR length"),
        192..=223 => {}
        _ => bail!("invalid RTCP type"),
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::media::rtp::PacketView;
    fn receive(rx: &mut RxRtpState, now: Instant) {
        let mut p = [0; 13];
        p[0] = 0x80;
        p[8..12].copy_from_slice(&55u32.to_be_bytes());
        rx.observe(PacketView::parse(&p).unwrap(), true, now);
    }
    #[test]
    fn rr_sr_sdes_bye_use_actual_counts_and_independent_clock() {
        let now = Instant::now();
        let wall = UNIX_EPOCH + std::time::Duration::from_secs(100);
        let mut tx = TxRtpState::new(55, 1, 8000).unwrap();
        let mut out = [0; 512];
        let n = write_report(&tx, None, now, wall, "test", &mut out).unwrap();
        assert_eq!(out[1], 201);
        assert_eq!(n, 24);
        tx.note_sent(160, 1000, now);
        tx.note_clock(1000, now);
        tx.note_sent(4, 1000, now + std::time::Duration::from_millis(100));
        let mut rx = RxRtpState::new(8000).unwrap();
        receive(&mut rx, now);
        let r = rx.report(now);
        let n = write_bye(
            &tx,
            r,
            now + std::time::Duration::from_secs(1),
            wall,
            "test",
            &mut out,
        )
        .unwrap();
        assert_eq!(out[1], 200);
        assert_eq!(read32(&out, 16), 9000);
        assert_eq!(read32(&out, 20), 2);
        assert_eq!(read32(&out, 24), 164);
        let observed = observe(&out[..n], &mut rx, now).unwrap();
        assert_eq!(observed.sender_reports, 1);
        assert_eq!(observed.report_blocks, 1);
        assert_eq!(observed.source_descriptions, 1);
        assert!(observed.remote_bye);
        let report = rx.report(now + std::time::Duration::from_secs(2)).unwrap();
        assert_eq!(report.last_sr, ((to_ntp(wall).unwrap() >> 16) as u32));
        assert_eq!(report.delay_since_last_sr, 131072);
    }
    #[test]
    fn truncated_tail_does_not_update_sender_report_and_short_output_is_atomic() {
        let now = Instant::now();
        let mut tx = TxRtpState::new(55, 1, 8000).unwrap();
        tx.note_sent(1, 0, now);
        let mut out = [0; 512];
        let n = write_report(&tx, None, now, UNIX_EPOCH, "test", &mut out).unwrap();
        let mut rx = RxRtpState::new(8000).unwrap();
        receive(&mut rx, now);
        out[n - 1] = 1;
        assert!(observe(&out[..n], &mut rx, now).is_err());
        assert_eq!(rx.report(now).unwrap().last_sr, 0);
        let mut short = [0x7a; 8];
        assert!(write_report(&tx, None, now, UNIX_EPOCH, "test", &mut short).is_err());
        assert_eq!(short, [0x7a; 8]);
    }
    #[test]
    fn malformed_corpus_never_panics_or_partially_accepts() {
        let now = Instant::now();
        let tx = TxRtpState::new(55, 1, 8000).unwrap();
        let mut out = [0; 512];
        let n = write_bye(&tx, None, now, UNIX_EPOCH, "a", &mut out).unwrap();
        for end in 0..n {
            let mut rx = RxRtpState::new(8000).unwrap();
            let _ = observe(&out[..end], &mut rx, now);
        }
        for i in 0..n {
            for value in [0, 0x20, 0x7f, 0xff] {
                let mut p = out;
                p[i] = value;
                let mut rx = RxRtpState::new(8000).unwrap();
                let _ = observe(&p[..n], &mut rx, now);
            }
        }
        assert!(write_report(&tx, None, now, UNIX_EPOCH, "", &mut out).is_err());
        assert!(write_report(&tx, None, now, UNIX_EPOCH, &"a".repeat(256), &mut out).is_err());
    }
}
