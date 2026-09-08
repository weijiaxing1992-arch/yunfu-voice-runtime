//! 独立核对有界丢弃、发送前抢占、真实socket背压及终态对账。
use super::*;
fn pcm(value: i16, kind: Kind) -> Record {
    let mut r = Record::empty(kind);
    r.samples = 160;
    r.body_len = 320;
    r.duration_ticks = 160;
    for p in r.body[..320].chunks_exact_mut(2) {
        p.copy_from_slice(&value.to_le_bytes());
    }
    r
}
fn u64_at(w: &[u8], at: usize) -> u64 {
    u64::from_le_bytes(w[at..at + 8].try_into().unwrap())
}
fn assert_accounted(s: &Subscription, now: Instant) {
    let v = s.status(now);
    assert_eq!(
        v.produced_events,
        v.submitted_events + v.dropped_events + v.queued_events
    );
    let queued = s
        .queue
        .iter()
        .map(|q| u64::from(q.record.samples))
        .sum::<u64>();
    assert_eq!(
        v.produced_samples,
        v.submitted_samples + v.dropped_samples + queued
    );
}

#[test]
fn full_queue_reports_exact_missing_prefix_before_fresh_audio() {
    let t = Instant::now();
    let mut s = Subscription::new(41, 77).unwrap();
    for n in 1..=9 {
        s.push(
            pcm(
                n,
                if n % 2 == 0 {
                    Kind::HistoryPlc
                } else {
                    Kind::Decoded
                },
            ),
            t,
        );
    }
    assert_eq!(s.queue.len(), 8);
    assert_accounted(&s, t);
    let mut w = [0; MAX_WIRE];
    let (n, p) = s.prepare(t, &mut w).unwrap();
    assert_eq!(n, HEADER);
    assert_eq!(&w[..4], b"RXS2");
    assert_eq!(w[4], 10);
    assert_eq!(u64_at(&w, 8), 41);
    assert_eq!(u64_at(&w, 16), 77);
    assert_eq!(u64_at(&w, 24), 1);
    assert_eq!(u64_at(&w, 88), 1);
    assert_eq!(u64_at(&w, 96), 1);
    assert_eq!(&w[104..112], &[1, 0, 0, 0, 0, 0, 0, 0]);
    assert_eq!(w[124], 1);
    // 未确认写入时，prepare不能悄悄吃掉缺口或计入submitted。
    assert_eq!(s.status(t).submitted_events, 0);
    s.submitted(p);
    let (n, p) = s.prepare(t, &mut w).unwrap();
    assert_eq!(n, HEADER + 320);
    assert_eq!(w[4], 2);
    assert_eq!(u64_at(&w, 24), 2);
    assert!(w[HEADER..HEADER + 320].chunks_exact(2).all(|x| x == [2, 0]));
    s.submitted(p);
    assert_accounted(&s, t);
    assert_eq!(s.status(t).submitted_samples, 160);
}

#[test]
fn preemption_between_encoding_and_send_expires_original_frame() {
    let t = Instant::now();
    let mut s = Subscription::new(1, 2).unwrap();
    s.push(pcm(-123, Kind::Decoded), t);
    s.push(pcm(321, Kind::Decoded), t + Duration::from_millis(90));
    let mut w = [0; MAX_WIRE];
    let (_, p) = s.prepare(t + Duration::from_millis(99), &mut w).unwrap();
    assert!(!s.still_fresh(p, t + Duration::from_millis(101)));
    let (_, p) = s.prepare(t + Duration::from_millis(101), &mut w).unwrap();
    assert_eq!(w[4], 10);
    assert_eq!(w[124], 2);
    s.submitted(p);
    let (n, p) = s.prepare(t + Duration::from_millis(101), &mut w).unwrap();
    assert_eq!(u64_at(&w, 24), 2);
    assert_eq!(&w[HEADER..HEADER + 2], &321i16.to_le_bytes());
    assert_eq!(n, HEADER + 320);
    s.submitted(p);
    assert_accounted(&s, t + Duration::from_millis(101));
    assert_eq!(s.status(t).dropped_events, 1);
}

#[test]
fn shared_deadline_is_fixed_across_serialization_retry_and_terminal() {
    let mut s = Subscription::new(1, 2).unwrap();
    let observed_at = Instant::now();
    let mut r = pcm(123, Kind::Decoded);
    r.observed_at = Some(observed_at);
    // 即便观察到导出之间已经过去80ms，期限也不能从入队时刻重新计算。
    let enqueue_at = observed_at + Duration::from_millis(80);
    s.push(r, enqueue_at);
    let mut first = [0; MAX_WIRE];
    let mut retry = [0; MAX_WIRE];
    s.prepare(enqueue_at, &mut first).unwrap();
    s.prepare(observed_at + Duration::from_millis(99), &mut retry)
        .unwrap();
    let lower = u64_at(&first, 152);
    assert_ne!(lower, 0);
    assert_eq!(&first[150..152], &clock::DOMAIN.to_le_bytes());
    assert_eq!(u64_at(&first, 160), lower + 100_000_000);
    assert_eq!(&first[152..168], &retry[152..168], "重试不得续期");
    assert_eq!(s.status(enqueue_at).oldest_age_ms, 80);
    let (_, gap) = s.prepare(observed_at + MAX_AGE, &mut retry).unwrap();
    assert_eq!(retry[4], Kind::ExportGap as u8);
    assert_eq!(&retry[152..168], &[0; 16], "缺口没有可交付音频的有效期");
    assert_eq!(s.status(enqueue_at).dropped_samples, 160);
    s.submitted(gap);
    s.stop();
    s.prepare(observed_at + MAX_AGE, &mut retry).unwrap();
    assert_eq!(retry[4], Kind::End as u8);
    assert_eq!(&retry[152..168], &[0; 16]);
    assert_accounted(&s, enqueue_at);
}

#[test]
fn pending_gap_merges_contiguous_full_and_age_drops_without_losing_plc_counts() {
    let t = Instant::now();
    let mut s = Subscription::new(1, 2).unwrap();
    for n in 0..10 {
        s.push(
            pcm(
                n,
                if n % 2 == 0 {
                    Kind::Decoded
                } else {
                    Kind::HistoryPlc
                },
            ),
            t,
        );
    }
    s.expire(t + MAX_AGE);
    assert!(s.queue.is_empty());
    assert_accounted(&s, t);
    let mut w = [0; MAX_WIRE];
    let (_, p) = s.prepare(t + MAX_AGE, &mut w).unwrap();
    assert_eq!(u64_at(&w, 88), 1);
    assert_eq!(u64_at(&w, 96), 10);
    assert_eq!(&w[104..112], &[5, 0, 0, 0, 5, 0, 0, 0]);
    assert_eq!(w[124], 3);
    s.submitted(p);
    assert!(s.prepare(t + MAX_AGE, &mut w).is_none());
    s.push(pcm(42, Kind::Decoded), t + MAX_AGE);
    assert_eq!(s.prepare(t + MAX_AGE, &mut w).unwrap().0, HEADER + 320);
    assert_eq!(u64_at(&w, 24), 11);
}

#[test]
fn stop_is_terminal_without_consumer_and_does_not_accept_late_audio() {
    let t = Instant::now();
    let mut s = Subscription::new(1, 2).unwrap();
    s.push(pcm(1, Kind::Decoded), t);
    s.push(pcm(2, Kind::HistoryPlc), t);
    s.stop();
    s.push(pcm(3, Kind::Decoded), t);
    s.stop();
    let v = s.status(t);
    assert_eq!(v.state, State::Stopped);
    assert_eq!(v.produced_events, 2);
    assert_eq!(v.queued_events, 0);
    assert_eq!(v.dropped_samples, 320);
    assert_accounted(&s, t);
    let mut w = [0; MAX_WIRE];
    let (_, p) = s.prepare(t, &mut w).unwrap();
    assert_eq!(w[4], 10);
    assert_eq!(w[124], 4);
    s.submitted(p);
    let (_, p) = s.prepare(t, &mut w).unwrap();
    assert_eq!(w[4], 11);
    assert_eq!(u64_at(&w, 24), 2);
    s.submitted(p);
    assert!(!s.pending());
    assert_accounted(&s, t);
}

#[test]
fn invalid_or_exhausted_source_fails_without_panicking_or_wrapping() {
    let t = Instant::now();
    let mut s = Subscription::new(1, 2).unwrap();
    let mut malformed = Record::empty(Kind::Decoded);
    malformed.body_len = MAX_BODY + 1;
    s.push(malformed, t);
    assert_eq!(s.status(t).state, State::Failed);
    assert_eq!(s.status(t).error, "invalid_observation");
    let mut s = Subscription::new(1, 3).unwrap();
    s.status.produced_events = u64::MAX;
    s.status.submitted_events = u64::MAX;
    s.push(pcm(0, Kind::Decoded), t);
    assert_eq!(s.status(t).error, "sequence_exhausted");
    assert_accounted(&s, t);
}

#[test]
fn confirmed_stopped_subscription_survives_later_lane_failure() {
    let t = Instant::now();
    let mut s = Subscription::new(1, 2).unwrap();
    s.stop();
    let mut w = [0; MAX_WIRE];
    let (_, p) = s.prepare(t, &mut w).unwrap();
    s.submitted(p);
    assert!(!s.pending());
    s.fail("rx_lane_unavailable");
    assert_eq!(s.status(t).state, State::Stopped);
    assert_eq!(s.status(t).error, "");
    assert!(!s.pending());
    assert_accounted(&s, t);
}

#[test]
fn actual_datagram_backpressure_has_bounded_retry_and_retains_independent_control_state() {
    let (socket, peer) = std::os::unix::net::UnixDatagram::pair().unwrap();
    socket.set_nonblocking(true).unwrap();
    let mut lane = Lane {
        socket,
        blocked_since: None,
        retry_at: None,
        failed: false,
    };
    let t = Instant::now();
    let w = [17; MAX_WIRE];
    let mut blocked = false;
    for _ in 0..65536 {
        if matches!(lane.send(&w, t), Send::Blocked) {
            blocked = true;
            break;
        }
    }
    assert!(blocked, "必须真实填满OS数据报队列，不能仅设置伪背压标志");
    assert!(lane.available());
    assert_eq!(lane.retry_at(), Some(t + Duration::from_millis(1)));
    assert!(matches!(lane.send(&w, t), Send::Blocked));
    assert!(matches!(lane.send(&w, t + STALL), Send::Failed));
    assert!(!lane.available());
    // 失败只在lane对象；媒体控制持有的订阅快照仍可独立终止与查询。
    let mut s = Subscription::new(1, 2).unwrap();
    s.push(pcm(7, Kind::Decoded), t);
    s.fail("rx_lane_unavailable");
    s.stop();
    assert_eq!(s.status(t).state, State::Failed);
    assert_accounted(&s, t);
    drop(peer);
}

#[test]
fn source_metadata_and_signed_pcm_have_fixed_wire_positions() {
    let t = Instant::now();
    let mut s = Subscription::new(9, 10).unwrap();
    let mut r = pcm(-300, Kind::Decoded);
    r.flags = 0x39f;
    r.ssrc = 0x12345678;
    r.rtp_timestamp = 0x100000080;
    r.rtp_sequence = 65538;
    r.media_time_ns = 20000000;
    r.source_generation = 2;
    r.source_segment = 3;
    r.boundary = 5;
    r.observed_at = Some(t);
    r.arrival = Some(t - Duration::from_millis(40));
    r.deadline = Some(t);
    r.discarded_packets = 4;
    s.push(r, t);
    let mut w = [0xff; MAX_WIRE];
    let (n, _) = s.prepare(t + Duration::from_millis(7), &mut w).unwrap();
    assert_eq!(n, HEADER + 320);
    assert_eq!(u64_at(&w, 32), 2);
    assert_eq!(u64_at(&w, 40), 3);
    assert_eq!(u64_at(&w, 48), 0x100000080);
    assert_eq!(u64_at(&w, 56), 65538);
    assert_eq!(u64_at(&w, 128), 4);
    assert_eq!(&w[136..148], &[7, 0, 0, 0, 47, 0, 0, 0, 7, 0, 0, 0]);
    assert_eq!(w[148], 5);
    assert_eq!(&w[150..152], &clock::DOMAIN.to_le_bytes());
    assert_ne!(u64_at(&w, 152), 0);
    assert_eq!(u64_at(&w, 160) - u64_at(&w, 152), 100_000_000);
    assert_eq!(&w[HEADER..HEADER + 2], &(-300i16).to_le_bytes());
}
