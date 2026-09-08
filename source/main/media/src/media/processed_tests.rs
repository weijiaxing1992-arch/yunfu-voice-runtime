//! 真实Graph组合边界：新语音段、独立发送时间线、迟到电话事件与丢包补偿不能互相污染。
use super::{DtmfTimestamps, Graph, DTMF_TIMESTAMP_HISTORY};
use super::{
    LocalObservationKind as RxKind, LocalObservationReason as RxReason,
    LocalSourceBoundary as RxBoundary,
};
use crate::{
    audio::{
        realtime::G711Codec,
        spec::{CodecSpec, MediaFormat},
        DecodeInput,
    },
    media::protocol::{ProcessingPlan, Stats},
};
use std::time::{Duration, Instant};

/// 按协议直接构造来源包；测试序号5刻意在6/7之后到达，仍处于真实RX回看窗口。
fn packet(sequence: u16, timestamp: u32, payload: u8, marker: bool, body: &[u8]) -> Vec<u8> {
    let mut wire = vec![0; 12 + body.len()];
    wire[0] = 0x80;
    wire[1] = payload | if marker { 0x80 } else { 0 };
    wire[2..4].copy_from_slice(&sequence.to_be_bytes());
    wire[4..8].copy_from_slice(&timestamp.to_be_bytes());
    wire[8..12].copy_from_slice(&42u32.to_be_bytes());
    wire[12..].copy_from_slice(body);
    wire
}

fn local_graph(payload: u8, now: Instant) -> Graph {
    let codec = CodecSpec::legacy(payload).unwrap();
    let format = MediaFormat {
        codec: Some(codec.clone()),
        cn_payload: Some(13),
        cn_clock_rate: Some(8000),
        dtmf_clock_rate: Some(8000),
        dtmf_events: Some("0-15".into()),
    };
    Graph::new(
        ProcessingPlan {
            topology: crate::media::protocol::ProcessingTopology::Local,
            version: 1,
            mode: "g711".into(),
            jitter_target_ms: 40,
            max_delay_ms: 120,
        },
        payload,
        &codec,
        Some(101),
        &format,
        91,
        now,
    )
    .unwrap()
}

/// 每次只驱动原有到期消费；独立take不能借旧getter再次发出同一PCM。
fn consume_local(graph: &mut Graph, origin: Instant, ms: u64, stats: &mut Stats) {
    let mut wire = [0x37; 172];
    assert!(graph
        .advance(0, origin + Duration::from_millis(ms), &mut wire, stats)
        .is_none());
    assert_eq!(wire, [0x37; 172], "RX观察不得生成反射RTP");
}

#[test]
fn rx_observation_is_opt_in_once_per_pcm_with_true_wrap_sequence_and_samples() {
    for (payload, code, sample) in [(0, 0x80, 32124i16), (8, 0xaa, 32256i16)] {
        let now = Instant::now();
        let mut graph = local_graph(payload, now);
        let mut stats = Stats::default();
        assert!(graph.local_observation_status().is_none());
        assert_eq!(graph.local_observation_storage_bytes(), 0);
        graph.enable_local_observation(true).unwrap();
        assert!(graph.local_observation_storage_bytes() > 0);
        assert!(graph.take_local_observation().is_none());
        graph.push(
            0,
            &packet(u16::MAX, u32::MAX - 159, payload, true, &[code; 160]),
            now,
            &mut stats,
        );
        let start = graph.take_local_observation().unwrap();
        assert_eq!(start.kind, RxKind::SourceBoundary);
        assert_eq!(start.boundary, Some(RxBoundary::InitialSource));
        assert_eq!(start.source.rtp_sequence, Some(131071));
        assert_eq!(start.source.expanded_timestamp, Some((1u64 << 33) - 160));
        assert_eq!(start.source.generation, 1);
        assert_eq!(start.source.ssrc, Some(42));
        assert!(start.media_time_ns.is_none());
        graph.push(
            0,
            &packet(0, 0, payload, false, &[code; 160]),
            now + Duration::from_millis(20),
            &mut stats,
        );
        for (ms, seq, ts, media_ns) in [
            (40, 131071, (1u64 << 33) - 160, 0),
            (60, 131072, 1u64 << 33, 20_000_000),
        ] {
            consume_local(&mut graph, now, ms, &mut stats);
            let value = graph.take_local_observation().unwrap();
            assert_eq!(value.kind, RxKind::Decoded);
            assert_eq!(value.samples(), &[sample; 160], "标准G711已知码字独立真值");
            assert_eq!(value.source.rtp_sequence, Some(seq));
            assert_eq!(value.source.expanded_timestamp, Some(ts));
            assert_eq!(value.media_time_ns, Some(media_ns));
            assert_eq!(value.duration_samples, Some(160));
            assert_eq!(value.source.segment, 1);
            assert_eq!(value.arrival, Some(now + Duration::from_millis(ms - 40)));
            assert_eq!(value.deadline, Some(now + Duration::from_millis(ms)));
            assert!(graph.take_local_observation().is_none());
            consume_local(&mut graph, now, ms + 1, &mut stats);
            assert!(
                graph.take_local_observation().is_none(),
                "无新消费不能重发getter缓存"
            );
        }
        assert_eq!(graph.local_observation_status().unwrap().produced, 3);
        assert_eq!(stats.processed_local_consumed_frames, 2);
        assert_eq!(stats.processed_encoded_frames, 0);
    }
}

#[test]
fn rx_observation_reorders_before_decode_and_duplicate_has_no_record() {
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let mut stats = Stats::default();
    graph.enable_local_observation(true).unwrap();
    for (seq, ts, at) in [(1, 0, 0), (3, 320, 20), (2, 160, 30), (2, 160, 32)] {
        graph.push(
            0,
            &packet(seq, ts, 0, false, &[0x80; 160]),
            now + Duration::from_millis(at),
            &mut stats,
        );
    }
    assert_eq!(
        graph.take_local_observation().unwrap().kind,
        RxKind::SourceBoundary
    );
    for (ms, seq, ts) in [(40, 65537, 0), (60, 65538, 160), (80, 65539, 320)] {
        consume_local(&mut graph, now, ms, &mut stats);
        let value = graph.take_local_observation().unwrap();
        assert_eq!(value.kind, RxKind::Decoded);
        assert_eq!(value.source.rtp_sequence, Some(seq));
        assert_eq!(value.source.expanded_timestamp.unwrap() as u32, ts);
    }
    assert!(graph.take_local_observation().is_none());
    assert_eq!(stats.processed_jitter_reordered, 1);
    assert_eq!(stats.processed_jitter_duplicates, 1);
    assert_eq!(graph.local_observation_status().unwrap().produced, 4);
}

#[test]
fn rx_observation_six_plc_tail_has_no_invented_packet_sequence_and_one_suspend_flag() {
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let mut stats = Stats::default();
    graph.enable_local_observation(true).unwrap();
    graph.push(0, &packet(7, 8000, 0, true, &[0x80; 160]), now, &mut stats);
    graph.take_local_observation().unwrap();
    consume_local(&mut graph, now, 40, &mut stats);
    graph.take_local_observation().unwrap();
    let mut sample = 32124i16;
    for n in 1..=6 {
        consume_local(&mut graph, now, 40 + n * 20, &mut stats);
        let value = graph.take_local_observation().unwrap();
        sample = (i32::from(sample) * 3 / 4) as i16;
        assert_eq!(value.kind, RxKind::HistoryPlc);
        assert_eq!(value.reason, Some(RxReason::MissingPacket));
        assert_eq!(value.samples(), &[sample; 160]);
        assert_eq!(value.source.rtp_sequence, None);
        assert_eq!(
            value.source.expanded_timestamp.unwrap() as u32,
            8000 + n as u32 * 160
        );
        assert_eq!(value.suspended_after, n == 6);
    }
    assert!(graph.next_deadline(0).is_none());
    consume_local(&mut graph, now, 1000, &mut stats);
    assert!(graph.take_local_observation().is_none());
    assert_eq!(graph.local_observation_status().unwrap().produced, 8);
    assert_eq!(stats.processed_plc_frames, 6);
}

#[test]
fn rx_observation_cn_only_has_no_pcm_anchor_and_expired_sid_keeps_real_semantics() {
    let now = Instant::now();
    for with_audio in [false, true] {
        let mut graph = local_graph(0, now);
        let mut stats = Stats::default();
        graph.enable_local_observation(true).unwrap();
        if with_audio {
            graph.push(0, &packet(1, 0, 0, true, &[0x80; 160]), now, &mut stats);
            graph.take_local_observation().unwrap();
            consume_local(&mut graph, now, 40, &mut stats);
            graph.take_local_observation().unwrap();
        }
        let at = if with_audio { 20 } else { 0 };
        graph.push(
            0,
            &packet(2, 160, 13, false, &[17, 4, 9]),
            now + Duration::from_millis(at),
            &mut stats,
        );
        if !with_audio {
            graph.take_local_observation().unwrap();
        }
        consume_local(&mut graph, now, at + 65, &mut stats);
        let value = graph.take_local_observation().unwrap();
        assert_eq!(value.kind, RxKind::ComfortNoise);
        assert_eq!(value.reason, Some(RxReason::AuxiliaryDeadline));
        assert_eq!(value.sid(), &[17, 4, 9]);
        assert!(value.cn_applied);
        assert!(!value.suspended_after);
        assert!(value.duration_samples.is_none());
        assert!(value.samples().is_empty());
        assert_eq!(
            value.media_time_ns,
            if with_audio { Some(20_000_000) } else { None }
        );
        assert_eq!(stats.processed_aux_expired, 1);
        assert_eq!(
            stats.processed_cn_packets, 0,
            "观察不能为了报告CN而篡改旧处理图统计"
        );
        consume_local(&mut graph, now, 1000, &mut stats);
        assert!(graph.take_local_observation().is_none());
        assert_eq!(stats.processed_plc_frames, 0);
    }
}

#[test]
fn rx_observation_stale_cn_is_not_audio_gap_and_does_not_clear_plc() {
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let mut stats = Stats::default();
    graph.enable_local_observation(true).unwrap();
    for (seq, ts, ms) in [(1, 0, 0), (2, 160, 20)] {
        graph.push(
            0,
            &packet(seq, ts, 0, false, &[0x80; 160]),
            now + Duration::from_millis(ms),
            &mut stats,
        );
    }
    graph.take_local_observation().unwrap();
    for ms in [40, 60] {
        consume_local(&mut graph, now, ms, &mut stats);
        graph.take_local_observation().unwrap();
    }
    graph.push(
        0,
        &packet(3, 0, 13, false, &[20]),
        now + Duration::from_millis(61),
        &mut stats,
    );
    for ms in [80, 100] {
        consume_local(&mut graph, now, ms, &mut stats);
        graph.take_local_observation().unwrap();
    }
    consume_local(&mut graph, now, 101, &mut stats);
    let stale = graph.take_local_observation().unwrap();
    assert_eq!(stale.kind, RxKind::AuxiliaryExpired);
    assert_eq!(stale.reason, Some(RxReason::StaleComfortNoise));
    assert!(stale.duration_samples.is_none() && !stale.cn_applied);
    assert!(stale.samples().is_empty() && stale.sid().is_empty());
    consume_local(&mut graph, now, 120, &mut stats);
    assert_eq!(
        graph.take_local_observation().unwrap().kind,
        RxKind::HistoryPlc
    );
    assert_eq!(stats.processed_missing_frames, 0);
}

#[test]
fn rx_observation_audio_expiry_preserves_known_range_then_missing_has_no_pcm() {
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let mut stats = Stats::default();
    graph.enable_local_observation(true).unwrap();
    graph.push(0, &packet(1, 0, 0, true, &[0x80; 160]), now, &mut stats);
    graph.take_local_observation().unwrap();
    consume_local(&mut graph, now, 40, &mut stats);
    graph.take_local_observation().unwrap();
    consume_local(&mut graph, now, 101, &mut stats);
    let expired = graph.take_local_observation().unwrap();
    assert_eq!(expired.kind, RxKind::LocalExpired);
    assert_eq!(expired.reason, Some(RxReason::AudioDeadline));
    assert_eq!(expired.source.expanded_timestamp.unwrap() as u32, 160);
    assert!(expired.source.rtp_sequence.is_none());
    assert_eq!(expired.duration_samples, Some(320));
    assert!(!expired.suspended_after);
    consume_local(&mut graph, now, 101, &mut stats);
    let missing = graph.take_local_observation().unwrap();
    assert_eq!(missing.kind, RxKind::Missing);
    assert_eq!(missing.source.expanded_timestamp.unwrap() as u32, 480);
    assert_eq!(missing.media_time_ns, Some(60_000_000));
    assert!(missing.samples().is_empty());
    assert_eq!(missing.duration_samples, Some(160));
}

#[test]
fn rx_observation_source_restart_reports_actual_drops_and_tx_remains_independent() {
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let mut stats = Stats::default();
    graph.enable_local_observation(true).unwrap();
    let mut wire = [0; 172];
    graph
        .encode_local_pcm(&[100; 160], now, true, &mut wire, &mut stats)
        .unwrap();
    assert!(
        graph.take_local_observation().is_none(),
        "自己的TX不能冒充用户RX"
    );
    let tx = graph.local_timestamp(now + Duration::from_secs(1)).unwrap();
    for (seq, ts, ms) in [(1, 0, 0), (2, 160, 20)] {
        graph.push(
            0,
            &packet(seq, ts, 0, true, &[0x80; 160]),
            now + Duration::from_millis(ms),
            &mut stats,
        );
    }
    graph.take_local_observation().unwrap();
    consume_local(&mut graph, now, 40, &mut stats);
    graph.take_local_observation().unwrap();
    for (seq, ts, ms) in [(9, 0, 45), (10, 160, 46)] {
        let mut p = packet(seq, ts, 0, true, &[0x80; 160]);
        p[8..12].copy_from_slice(&43u32.to_be_bytes());
        graph.push(0, &p, now + Duration::from_millis(ms), &mut stats);
        if seq == 9 {
            assert!(
                graph.take_local_observation().is_none(),
                "单包候选不假确认新源"
            );
        }
    }
    let boundary = graph.take_local_observation().unwrap();
    assert_eq!(boundary.kind, RxKind::SourceBoundary);
    assert_eq!(boundary.boundary, Some(RxBoundary::SourceChanged));
    assert_eq!(boundary.source.ssrc, Some(43));
    assert_eq!(boundary.source.generation, 2);
    assert_eq!(boundary.source.rtp_sequence, Some(65546));
    assert_eq!(boundary.discarded_packets, 1);
    consume_local(&mut graph, now, 86, &mut stats);
    let value = graph.take_local_observation().unwrap();
    assert_eq!(value.kind, RxKind::Decoded);
    assert_eq!(value.source.generation, 2);
    assert!(value.media_time_ns.unwrap() >= 20_000_000);
    assert_eq!(
        graph.local_timestamp(now + Duration::from_secs(1)).unwrap(),
        tx
    );
}

#[test]
fn rx_observation_queue_overflow_is_explicit_terminal_and_disable_releases_storage() {
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let mut stats = Stats::default();
    graph.enable_local_observation(true).unwrap();
    graph.push(0, &packet(1, 0, 0, true, &[0x80; 160]), now, &mut stats);
    for n in 0..=6 {
        consume_local(&mut graph, now, 40 + n * 20, &mut stats);
    }
    assert_eq!(graph.local_observation_status().unwrap().queued, 8);
    graph.push(
        0,
        &packet(2, 1120, 0, true, &[0x80; 160]),
        now + Duration::from_millis(200),
        &mut stats,
    );
    consume_local(&mut graph, now, 240, &mut stats);
    let status = graph.local_observation_status().unwrap();
    assert!(status.failed);
    assert_eq!(status.failure, Some(RxReason::ObservationOverflow));
    assert_eq!(status.queued, 9);
    assert_eq!(status.peak_queued, 9);
    graph.enable_local_observation(true).unwrap();
    consume_local(&mut graph, now, 260, &mut stats);
    assert_eq!(
        graph.local_observation_status().unwrap().produced,
        9,
        "失败订阅不自动恢复或隐瞒中间缺口"
    );
    for seq in 1..=8 {
        assert_eq!(graph.take_local_observation().unwrap().sequence, seq);
    }
    let failed = graph.take_local_observation().unwrap();
    assert_eq!(failed.sequence, 9);
    assert_eq!(failed.kind, RxKind::Failed);
    assert!(failed.samples().is_empty());
    assert!(graph.take_local_observation().is_none());
    graph.enable_local_observation(false).unwrap();
    assert_eq!(graph.local_observation_storage_bytes(), 0);
    graph.enable_local_observation(true).unwrap();
    assert!(
        graph.take_local_observation().is_none(),
        "重订阅不能重发旧getter的PCM"
    );
    assert_eq!(graph.local_observation_status().unwrap().produced, 0);
}

#[test]
fn rx_observation_dtmf_timeout_is_auxiliary_not_lost_audio() {
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let mut stats = Stats::default();
    graph.enable_local_observation(true).unwrap();
    graph.push(
        0,
        &packet(1, 8000, 101, true, &[1, 0, 0, 160]),
        now,
        &mut stats,
    );
    graph.take_local_observation().unwrap();
    consume_local(&mut graph, now, 40, &mut stats);
    let start = graph.take_local_observation().unwrap();
    assert_eq!(start.kind, RxKind::Auxiliary);
    assert!(start.media_time_ns.is_none() && start.duration_samples.is_none());
    consume_local(&mut graph, now, 160, &mut stats);
    let timeout = graph.take_local_observation().unwrap();
    assert_eq!(timeout.kind, RxKind::AuxiliaryExpired);
    assert_eq!(timeout.reason, Some(RxReason::ToneTimeout));
    assert!(timeout.duration_samples.is_none() && timeout.samples().is_empty());
    assert!(graph.take_local_observation().is_none());
    assert_eq!(stats.processed_jitter_lost, 0);
}

#[test]
fn rx_observation_rtcp_bye_is_source_boundary_without_invented_rtp_sequence() {
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let mut stats = Stats::default();
    graph.enable_local_observation(true).unwrap();
    graph.push(0, &packet(1, 0, 0, true, &[0x80; 160]), now, &mut stats);
    graph.take_local_observation().unwrap();
    let tx = crate::media::rtp_state::TxRtpState::new(42, 1, 8000).unwrap();
    let mut wire = [0; 128];
    let len = crate::media::rtcp::write_bye(&tx, None, now, std::time::UNIX_EPOCH, "rx", &mut wire)
        .unwrap();
    graph
        .observe_rtcp(0, &wire[..len], now + Duration::from_millis(1), &mut stats)
        .unwrap();
    let bye = graph.take_local_observation().unwrap();
    assert_eq!(bye.kind, RxKind::SourceBoundary);
    assert_eq!(bye.boundary, Some(RxBoundary::SourceEnded));
    assert!(bye.source.rtp_sequence.is_none());
    assert_eq!(bye.discarded_packets, 1);
    assert!(bye.suspended_after);
    assert!(graph.next_deadline(0).is_none());
    assert!(graph.take_local_observation().is_none());
}

#[test]
fn rx_observation_reports_encoded_buffer_rejection_and_late_arrival_separately() {
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let mut stats = Stats::default();
    graph.enable_local_observation(true).unwrap();
    graph.push(0, &packet(1, 0, 0, true, &[0x80; 160]), now, &mut stats);
    graph.take_local_observation().unwrap();
    // 一个音频槽与七个辅助槽占满编码缓冲；第九包不能被静默略去。
    for seq in 2..=9 {
        graph.push(
            0,
            &packet(seq, 0, 101, false, &[1, 0x80, 0, 160]),
            now,
            &mut stats,
        );
    }
    let full = graph.take_local_observation().unwrap();
    assert_eq!(full.kind, RxKind::AuxiliaryExpired);
    assert_eq!(full.reason, Some(RxReason::JitterBufferLimit));
    assert_eq!(full.source.rtp_sequence, Some(65545));
    assert_eq!(full.discarded_packets, 1);
    assert!(full.duration_samples.is_none());

    let mut graph = local_graph(0, now);
    graph.enable_local_observation(true).unwrap();
    graph.push(0, &packet(1, 0, 0, true, &[0x80; 160]), now, &mut stats);
    graph.take_local_observation().unwrap();
    for ms in [40, 60] {
        consume_local(&mut graph, now, ms, &mut stats);
        graph.take_local_observation().unwrap();
    }
    graph.push(
        0,
        &packet(3, 160, 0, false, &[0x80; 160]),
        now + Duration::from_millis(61),
        &mut stats,
    );
    let late = graph.take_local_observation().unwrap();
    assert_eq!(late.kind, RxKind::LocalExpired);
    assert_eq!(late.reason, Some(RxReason::LateArrival));
    assert_eq!(late.source.rtp_sequence, Some(65539));
    assert_eq!(late.duration_samples, Some(160));
    assert!(late.samples().is_empty());
}

#[test]
fn local_topology_is_explicit_unknown_values_fail_and_bridge_stays_default() {
    use crate::media::protocol::ProcessingTopology;
    let value =
        serde_json::json!({"version":1,"mode":"g711","jitter_target_ms":40,"max_delay_ms":120});
    assert_eq!(
        serde_json::from_value::<ProcessingPlan>(value.clone())
            .unwrap()
            .topology,
        ProcessingTopology::Bridge
    );
    for bad in [
        serde_json::json!("locla"),
        serde_json::json!(null),
        serde_json::json!(42),
    ] {
        let mut value = value.clone();
        value["topology"] = bad;
        assert!(serde_json::from_value::<ProcessingPlan>(value).is_err());
    }
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let before = graph.directions[1].tx.snapshot();
    assert!(graph
        .connect(
            0,
            &CodecSpec::legacy(0).unwrap(),
            None,
            &MediaFormat::default(),
            &mut Stats::default()
        )
        .is_err());
    assert!(graph.connected.is_none());
    assert_eq!(graph.directions[1].tx.snapshot().ssrc, before.ssrc);
}

#[test]
fn local_input_consumes_native_pcm_origin_and_energy_without_echo_then_stops_plc() {
    use crate::audio::FrameOrigin;
    for payload in [0, 8] {
        let now = Instant::now();
        let mut graph = local_graph(payload, now);
        let mut stats = Stats::default();
        let body: [u8; 160] = std::array::from_fn(|i| (i + 23) as u8);
        graph.push(0, &packet(1, 0, payload, true, &body), now, &mut stats);
        let mut wire = [0x77; 2048];
        assert!(graph
            .advance(0, now + Duration::from_millis(40), &mut wire, &mut stats)
            .is_none());
        assert_eq!(wire, [0x77; 2048], "本地消费不能准备任何反射包");
        let (frame, energy) = graph.local_input_frame().unwrap();
        let expected: Vec<_> = body
            .iter()
            .map(|v| crate::audio::g711::decode(*v, payload == 8))
            .collect();
        assert_eq!(frame.samples(), expected);
        assert_eq!(frame.origin(), FrameOrigin::Decoded);
        assert_eq!(frame.position().media_time_ns(), 0);
        assert_eq!(
            energy,
            expected
                .iter()
                .map(|v| i64::from(*v).pow(2) as u64)
                .sum::<u64>()
        );
        assert!(energy > 0);
        for n in 1..=6 {
            assert!(graph
                .advance(
                    0,
                    now + Duration::from_millis(40 + n * 20),
                    &mut wire,
                    &mut stats
                )
                .is_none());
            let (frame, _) = graph.local_input_frame().unwrap();
            assert_eq!(frame.origin(), FrameOrigin::HistoryConcealment);
            assert_eq!(frame.position().media_time_ns(), n * 20_000_000);
        }
        assert!(graph
            .advance(0, now + Duration::from_millis(180), &mut wire, &mut stats)
            .is_none());
        assert_eq!(
            graph
                .local_input_frame()
                .unwrap()
                .0
                .position()
                .media_time_ns(),
            120_000_000,
            "无新事件时只保留旧帧明确位置，不能把它重新计作当前音频"
        );
        assert!(
            graph.next_deadline(0).is_none(),
            "无声后必须撤销到期项，不能轮询补静音"
        );
        assert_eq!(stats.processed_decoded_frames, 1);
        assert_eq!(stats.processed_plc_frames, 6);
        assert_eq!(stats.processed_local_consumed_frames, 7);
        assert_eq!(stats.processed_local_consumed_samples, 1120);
        assert_eq!(stats.processed_local_nonzero_frames, 7);
        assert_eq!(stats.processed_encoded_frames, 0);
        assert_eq!(graph.directions[1].tx.snapshot().prepared_packets, 0);
    }
}

#[test]
fn local_cn_ends_history_and_source_restart_does_not_move_generated_tx_clock() {
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let mut stats = Stats::default();
    let mut wire = [0; 2048];
    graph.push(0, &packet(1, 8000, 0, true, &[0x55; 160]), now, &mut stats);
    graph.advance(0, now + Duration::from_millis(40), &mut wire, &mut stats);
    graph.push(
        0,
        &packet(2, 8160, 13, false, &[10]),
        now + Duration::from_millis(20),
        &mut stats,
    );
    assert!(graph
        .advance(0, now + Duration::from_millis(60), &mut wire, &mut stats)
        .is_none());
    assert!(graph.local_input_frame().is_none());
    assert_eq!(stats.processed_cn_packets, 1);
    assert_eq!(stats.processed_plc_frames, 0);
    let expected = graph.local_timestamp(now + Duration::from_secs(1)).unwrap();
    for (sequence, ts, ms) in [(3, 0, 200), (4, 160, 220)] {
        let mut data = packet(sequence, ts, 0, true, &[0x44; 160]);
        data[8..12].copy_from_slice(&43u32.to_be_bytes());
        graph.push(0, &data, now + Duration::from_millis(ms), &mut stats);
    }
    assert_eq!(stats.processed_source_resets, 1);
    assert_eq!(
        graph.local_timestamp(now + Duration::from_secs(1)).unwrap(),
        expected
    );
    assert!(graph
        .advance(0, now + Duration::from_millis(260), &mut wire, &mut stats)
        .is_none());
    let (frame, _) = graph.local_input_frame().unwrap();
    assert!(frame.position().media_time_ns() >= 20_000_000);
}

#[test]
fn local_raw_tone_wav_and_dtmf_share_tx_and_rtcp_counts_without_redecoding() {
    use crate::media::{
        dtmf_sender::Sender,
        interaction::{Leg, Playback},
    };
    use std::sync::Arc;
    for payload in [0, 8] {
        let now = Instant::now();
        let mut graph = local_graph(payload, now);
        let mut stats = Stats::default();
        let mut tone = Playback::new(1, Leg::A, 1000, 60, now, 5).unwrap();
        let samples: Arc<[i16]> = (0..323)
            .map(|i| (i * 71 - 10000) as i16)
            .collect::<Vec<_>>()
            .into();
        let mut wav = Playback::loading(
            2,
            Leg::A,
            "fixture.wav".into(),
            now,
            now + Duration::from_secs(2),
            6,
        )
        .unwrap();
        wav.loaded(Arc::clone(&samples), now + Duration::from_millis(100));
        let mut sender = Sender::new();
        sender
            .enqueue("#", 55, 0xffff, now + Duration::from_millis(20))
            .unwrap();
        let mut packets = Vec::new();
        let mut pcm = [0; 160];
        let mut wire = [0; 172];
        let mut event_ts = None;
        for ms in 0..=180 {
            let at = now + Duration::from_millis(ms);
            for playback in [&mut tone, &mut wav] {
                if at == playback.next && playback.sent < playback.total {
                    assert!(playback.pcm_frame(at, &mut pcm));
                    if playback.id == 2 {
                        for (i, sample) in pcm.iter().enumerate() {
                            assert_eq!(
                                *sample,
                                samples
                                    .get((playback.sent * 160) as usize + i)
                                    .copied()
                                    .unwrap_or(0)
                            );
                        }
                    } else {
                        for (i, sample) in pcm.iter().enumerate() {
                            let n = playback.sent * 160 + i as u64;
                            assert_eq!(
                                *sample,
                                ((std::f64::consts::TAU * 1000.0 * n as f64 / 8000.0).sin()
                                    * 8192.0)
                                    .round() as i16
                            );
                        }
                    }
                    let len = graph
                        .encode_local_pcm(&pcm, at, playback.sent == 0, &mut wire, &mut stats)
                        .unwrap();
                    for (sample, byte) in pcm.iter().zip(&wire[12..len]) {
                        assert_eq!(*byte, crate::audio::g711::encode(*sample, payload == 8));
                    }
                    graph.note_send(1, &wire[..len], at, true);
                    packets.push(wire[..len].to_vec());
                    playback.accepted();
                }
            }
            if sender.next == Some(at) {
                let raw = sender
                    .packet_on_timeline(
                        101,
                        8000,
                        graph
                            .local_timestamp(at - Duration::from_millis(20))
                            .unwrap(),
                        at,
                    )
                    .unwrap();
                let ts = timestamp(&raw);
                assert_eq!(*event_ts.get_or_insert(ts), ts);
                let len = graph.write_local_event(&raw, &mut wire).unwrap();
                graph.note_send(1, &wire[..len], at, true);
                packets.push(wire[..len].to_vec());
                sender.accepted();
            }
        }
        assert_eq!(sender.status.completed_digits, 1);
        assert_eq!(packets.len(), 11);
        assert_eq!(
            packets
                .iter()
                .filter(|p| p[1] & 127 == 101 && p[13] & 128 != 0)
                .count(),
            3
        );
        for pair in packets.windows(2) {
            assert_eq!(&pair[0][8..12], &pair[1][8..12]);
            assert_eq!(
                u16::from_be_bytes(pair[1][2..4].try_into().unwrap()),
                u16::from_be_bytes(pair[0][2..4].try_into().unwrap()).wrapping_add(1)
            );
        }
        assert_eq!(
            stats.processed_decoded_frames, 0,
            "本地播放不能先编码再经过解码器"
        );
        assert_eq!(stats.processed_encoded_frames, 6);
        let mut report = [0; 2048];
        let len = graph
            .report(0, now + Duration::from_secs(1), true, &mut report)
            .unwrap();
        assert_eq!(report[1], 200);
        assert_eq!(&report[4..8], &packets[0][8..12]);
        assert_eq!(u32::from_be_bytes(report[20..24].try_into().unwrap()), 11);
        assert_eq!(
            u32::from_be_bytes(report[24..28].try_into().unwrap()),
            6 * 160 + 5 * 4
        );
        assert_eq!(
            u32::from_be_bytes(report[16..20].try_into().unwrap()),
            graph.local_timestamp(now + Duration::from_secs(1)).unwrap()
        );
        assert_eq!(report[len - 7], 203);
        assert_eq!(&report[len - 4..len], &packets[0][8..12]);
    }
}

#[test]
fn local_output_rejects_invalid_buffer_and_overlap_before_tx_or_stats_mutation() {
    let now = Instant::now();
    let mut graph = local_graph(0, now);
    let mut stats = Stats::default();
    let mut wire = [0x33; 172];
    let initial = graph.directions[1].tx.snapshot();
    assert!(graph
        .encode_local_pcm(&[1; 159], now, true, &mut wire, &mut stats)
        .is_err());
    assert!(graph
        .encode_local_pcm(&[1; 160], now, true, &mut wire[..171], &mut stats)
        .is_err());
    assert_eq!(wire, [0x33; 172]);
    assert_eq!(
        graph.directions[1].tx.snapshot().prepared_packets,
        initial.prepared_packets
    );
    assert_eq!(stats.processed_encoded_frames, 0);
    graph
        .encode_local_pcm(&[1; 160], now, true, &mut wire, &mut stats)
        .unwrap();
    let before = wire;
    assert!(graph
        .encode_local_pcm(
            &[2; 160],
            now + Duration::from_millis(19),
            false,
            &mut wire,
            &mut stats
        )
        .is_err());
    assert_eq!(wire, before);
    assert_eq!(stats.processed_encoded_frames, 1);
    assert_eq!(
        graph.next_local_audio_at(now + Duration::from_millis(1)),
        now + Duration::from_millis(20)
    );
}

/// 逐帧非静音模式使恢复后每个真实PCM块可追到唯一来源位置；PLC不能靠总包数冒充原始内容。
fn recovery_packets() -> (Vec<Vec<u8>>, Vec<[u8; 160]>) {
    let mut input = Vec::new();
    let mut expected = Vec::new();
    let mut decoder = G711Codec::new("PCMU").unwrap();
    let encoder = G711Codec::new("PCMA").unwrap();
    for i in 0..200 {
        let payload = std::array::from_fn::<_, 160, _>(|j| {
            (16 + (i * 37 + j * 11 + (i >> (j % 8))) % 224) as u8
        });
        let mut pcm = [0; 160];
        let mut encoded = [0; 160];
        decoder
            .decode_into(DecodeInput::Packet(&payload), 20, &mut pcm)
            .unwrap();
        encoder.encode_into(&pcm, 20, &mut encoded).unwrap();
        input.push(packet((i + 1) as u16, i as u32 * 160, 0, i == 0, &payload));
        expected.push(encoded);
    }
    (input, expected)
}

/// 来源暂停与媒体线程暂停分开模拟，所有时间显式推进，不依赖操作系统调度或真实sleep。
fn check_continuous_recovery(
    source_pause: bool,
    worker_pause: bool,
    resumed_marker: bool,
) -> Stats {
    let origin = Instant::now();
    let a = CodecSpec::legacy(0).unwrap();
    let b = CodecSpec::legacy(8).unwrap();
    let plan = ProcessingPlan {
        topology: Default::default(),
        version: 1,
        mode: "g711".into(),
        jitter_target_ms: 40,
        max_delay_ms: 120,
    };
    let mut stats = Stats::default();
    let mut graph = Graph::new(
        plan,
        0,
        &a,
        None,
        &MediaFormat {
            codec: Some(a.clone()),
            ..Default::default()
        },
        42,
        origin,
    )
    .unwrap();
    graph
        .connect(
            8,
            &b,
            None,
            &MediaFormat {
                codec: Some(b.clone()),
                ..Default::default()
            },
            &mut stats,
        )
        .unwrap();
    let (mut inputs, expected) = recovery_packets();
    // 前六个补发包已被有限PLC越过；第66帧是首个仍可前进的真实来源位置，是否带marker都应保留原相位。
    if resumed_marker {
        inputs[66][1] |= 128;
    }
    let base = graph.directions[0].timestamp_offset;
    let mut next = 0;
    let mut matched = 0;
    let mut recovered = 0;
    for ms in 0..4200u64 {
        let now = origin + Duration::from_millis(ms);
        if worker_pause && (1200..1383).contains(&ms) {
            continue;
        }
        if worker_pause && ms == 1383 {
            // 当前worker恢复时先处理已到期项；这里必须真实统计过期，不能把丢弃偷偷当作通过音频。
            while graph.next_deadline(0).is_some_and(|due| due <= now) {
                let mut wire = [0; 2048];
                if let Some(len) = graph.advance(0, now, &mut wire, &mut stats) {
                    graph.note_send(0, &wire[..len], now, true);
                }
            }
        }
        if !(source_pause && (1200..1383).contains(&ms)) {
            while next < inputs.len() && next as u64 * 20 <= ms {
                graph.push(0, &inputs[next], now, &mut stats);
                next += 1;
            }
        }
        while graph.next_deadline(0).is_some_and(|due| due <= now) {
            let mut wire = [0; 2048];
            if let Some(len) = graph.advance(0, now, &mut wire, &mut stats) {
                graph.note_send(0, &wire[..len], now, true);
                if let Some(frame) = expected
                    .iter()
                    .position(|body| body.as_slice() == &wire[12..len])
                {
                    assert_eq!(
                        timestamp(&wire),
                        base.wrapping_add(frame as u32 * 160),
                        "来源第{frame}帧在恢复后被重新改写时间位置"
                    );
                    matched += 1;
                    if frame >= 72 {
                        recovered += 1;
                    }
                }
            }
        }
    }
    assert_eq!(next, 200);
    assert_eq!(
        recovered, 128,
        "恢复之后的72..199全部实际内容帧必须保持原160ticks映射"
    );
    assert_eq!(matched, stats.processed_decoded_frames as usize);
    assert_eq!(
        stats.processed_source_resets, 0,
        "连续同SSRC不能伪造源重启来改变时间线"
    );
    assert_eq!(graph.directions[0].timestamp_offset, base);
    stats
}

#[test]
fn continuous_source_pause_keeps_pcm_timestamp_mapping_with_or_without_marker() {
    for marker in [false, true] {
        let stats = check_continuous_recovery(true, false, marker);
        assert_eq!(stats.processed_decoded_frames, 194);
        assert_eq!(stats.processed_jitter_late, 6);
        assert_eq!(stats.processed_playout_expired, 0);
        assert!(
            stats.processed_plc_frames > 0,
            "来源真实缺帧仍须保留PLC证据"
        );
    }
}

#[test]
fn worker_pause_drops_expired_slots_without_changing_continuous_source_clock() {
    let stats = check_continuous_recovery(false, true, false);
    assert_eq!(stats.processed_decoded_frames, 190);
    assert_eq!(stats.processed_playout_expired, 9);
    assert_eq!(stats.processed_jitter_late, 8);
}

#[test]
fn confirmed_source_and_timestamp_restarts_still_reanchor_forward() {
    for changed_ssrc in [false, true] {
        let origin = Instant::now();
        let codec = CodecSpec::legacy(0).unwrap();
        let format = MediaFormat {
            codec: Some(codec.clone()),
            ..Default::default()
        };
        let plan = ProcessingPlan {
            topology: Default::default(),
            version: 1,
            mode: "g711".into(),
            jitter_target_ms: 40,
            max_delay_ms: 120,
        };
        let mut stats = Stats::default();
        let mut graph = Graph::new(plan, 0, &codec, None, &format, 42, origin).unwrap();
        graph.connect(0, &codec, None, &format, &mut stats).unwrap();
        graph.push(
            0,
            &packet(1, 8000, 0, true, &[0x55; 160]),
            origin,
            &mut stats,
        );
        let before = emit(&mut graph, origin, 40, &mut stats).unwrap();
        let mut first = packet(2, 0, 0, true, &[0x55; 160]);
        let mut second = packet(3, 160, 0, false, &[0x55; 160]);
        if changed_ssrc {
            first[8..12].copy_from_slice(&43u32.to_be_bytes());
            second[8..12].copy_from_slice(&43u32.to_be_bytes());
        }
        graph.push(0, &first, origin + Duration::from_millis(200), &mut stats);
        assert_eq!(stats.processed_source_resets, 0, "单个异常包不能重启活动源");
        graph.push(0, &second, origin + Duration::from_millis(220), &mut stats);
        assert_eq!(stats.processed_source_resets, 1);
        let after = emit(&mut graph, origin, 260, &mut stats).unwrap();
        assert!(
            timestamp(&after).wrapping_sub(timestamp(&before)) as i32 >= 160,
            "真实来源重启后输出时间不能倒退"
        );
        assert_eq!(&before[8..12], &after[8..12], "接收源重启不改变独立TX身份");
        assert_eq!(&before[12..], &after[12..], "真实新鲜内容在源重启后被污染");
        assert_eq!(stats.processed_decoded_frames, 2);
    }
}

/// 只向图提交确定的单调时间；真实UDP接受结果在worker端到端另验，不把本方法称作网络发送。
fn emit(graph: &mut Graph, origin: Instant, ms: u64, stats: &mut Stats) -> Option<Vec<u8>> {
    let mut wire = [0; 2048];
    let now = origin + Duration::from_millis(ms);
    let len = graph.advance(0, now, &mut wire, stats)?;
    graph.note_send(0, &wire[..len], now, true);
    Some(wire[..len].to_vec())
}

fn timestamp(wire: &[u8]) -> u32 {
    u32::from_be_bytes(wire[4..8].try_into().unwrap())
}

#[test]
fn dtmf_timestamp_history_stays_bounded_and_eviction_never_guesses_old_mapping() {
    let mut history = DtmfTimestamps::new();
    let storage = history.entries.as_ptr();
    let bytes = std::mem::size_of_val(&history.entries);
    let count = DTMF_TIMESTAMP_HISTORY * 4;
    for index in 0..count {
        let timestamp = index as u64 * 160;
        assert_eq!(
            history.translate(42, timestamp, 1000),
            Some(timestamp as u32 + 1000)
        );
        assert!(history.valid <= DTMF_TIMESTAMP_HISTORY);
        assert!(history.next < DTMF_TIMESTAMP_HISTORY);
        assert_eq!(
            history.entries.as_ptr(),
            storage,
            "存储不能随按键数量重新分配"
        );
    }
    assert_eq!(history.valid, DTMF_TIMESTAMP_HISTORY);
    assert_eq!(std::mem::size_of_val(&history.entries), bytes);
    let cursor = history.next;
    assert_eq!(
        history.translate(42, 0, 9999),
        None,
        "已淘汰旧事件不能按新偏移重新映射"
    );
    assert_eq!(history.next, cursor, "拒绝旧事件不得驱逐现存有效映射");
    for index in count - DTMF_TIMESTAMP_HISTORY..count {
        let timestamp = index as u64 * 160;
        assert_eq!(
            history.translate(42, timestamp, 9999),
            Some(timestamp as u32 + 1000)
        );
    }
    println!(
        "DTMF每方向固定历史：{}项，记录区{}字节，状态整体{}字节",
        DTMF_TIMESTAMP_HISTORY,
        bytes,
        std::mem::size_of_val(&history)
    );
}

#[test]
fn dtmf_timestamp_history_separates_wrap_epochs_and_source_generations() {
    let mut history = DtmfTimestamps::new();
    let before_wrap = (1u64 << 32) - 80;
    let after_wrap = before_wrap + 160;
    let first = history.translate(42, before_wrap, 100).unwrap();
    assert_eq!(first, 20);
    assert_eq!(history.translate(42, after_wrap, 200), Some(280));
    assert_eq!(history.translate(42, before_wrap, 9999), Some(first));
    // 原始u32值一轮后相同，但展开来源位置不同，必须建立新映射且保留上一轮已知结束副本。
    let next_epoch = before_wrap + (1u64 << 32);
    assert_eq!(history.translate(42, next_epoch, 600), Some(520));
    assert_eq!(history.translate(42, before_wrap, 9999), Some(first));
    assert_eq!(
        history.translate(43, before_wrap, 700),
        Some(620),
        "新SSRC不得复用旧来源缓存"
    );
    assert_eq!(history.valid, 1);
    // 同SSRC确认重启也必须显式clear；底层只保存合法代际内的映射。
    history.clear();
    assert_eq!(history.valid, 0);
    assert_eq!(history.source, None);
    assert_eq!(history.translate(43, before_wrap, 800), Some(720));
}

#[test]
fn late_cn_after_plc_keeps_original_payload_without_rewinding_sender_report_clock() {
    let origin = Instant::now();
    let codec = CodecSpec::legacy(0).unwrap();
    let format = MediaFormat {
        codec: Some(codec.clone()),
        cn_payload: Some(13),
        cn_clock_rate: Some(8000),
        ..Default::default()
    };
    let plan = ProcessingPlan {
        topology: Default::default(),
        version: 1,
        mode: "g711".into(),
        jitter_target_ms: 40,
        max_delay_ms: 120,
    };
    let mut stats = Stats::default();
    let mut graph = Graph::new(plan, 0, &codec, None, &format, 42, origin).unwrap();
    graph.connect(0, &codec, None, &format, &mut stats).unwrap();
    graph.push(0, &packet(1, 0, 0, false, &[0x55; 160]), origin, &mut stats);
    emit(&mut graph, origin, 40, &mut stats).unwrap();
    let plc = emit(&mut graph, origin, 60, &mut stats).unwrap();
    assert_eq!(stats.processed_plc_frames, 1);
    // CN证明160位置本来是静默，但该位置的PLC已经输出；可以保留真实SID起点，不能倒推媒体时钟。
    graph.push(
        0,
        &packet(2, 160, 13, false, &[42]),
        origin + Duration::from_millis(61),
        &mut stats,
    );
    let mut report = [0; 2048];
    graph
        .report(1, origin + Duration::from_millis(90), false, &mut report)
        .unwrap();
    assert_eq!(report[1], 200, "必须读取真实RTCP发送报告");
    let before = u32::from_be_bytes(report[16..20].try_into().unwrap());
    let cn = emit(&mut graph, origin, 101, &mut stats).unwrap();
    assert_eq!(cn[1] & 127, 13);
    assert_eq!(&cn[12..], &[42]);
    assert_eq!(
        timestamp(&cn),
        timestamp(&plc),
        "不可通过伪造CN采样时间躲过时钟回拨检测"
    );
    graph
        .report(1, origin + Duration::from_millis(102), false, &mut report)
        .unwrap();
    let after = u32::from_be_bytes(report[16..20].try_into().unwrap());
    assert_eq!(
        after.wrapping_sub(before),
        96,
        "12ms经过应按8k时钟前进96ticks，迟到CN不得回拨SR"
    );
    assert_eq!(stats.processed_cn_packets, 1);
    assert_eq!(stats.processed_missing_frames, 0);
}

#[test]
fn late_event_end_after_reanchored_voice_and_next_event_keeps_timeline_and_hold() {
    let origin = Instant::now();
    let codec = CodecSpec::legacy(0).unwrap();
    let format = MediaFormat {
        codec: Some(codec.clone()),
        dtmf_clock_rate: Some(8000),
        dtmf_events: Some("0-15".into()),
        ..Default::default()
    };
    let plan = ProcessingPlan {
        topology: Default::default(),
        version: 1,
        mode: "g711".into(),
        jitter_target_ms: 40,
        max_delay_ms: 120,
    };
    let mut stats = Stats::default();
    let mut graph = Graph::new(plan, 0, &codec, Some(101), &format, 42, origin).unwrap();
    graph
        .connect(0, &codec, Some(101), &format, &mut stats)
        .unwrap();
    // 非静音独立PCM码字包含正负幅度；重锚前后的新鲜音频必须真实走编解码。
    let voice = std::array::from_fn::<_, 160, _>(|i| [0x55, 0xd5, 0x35, 0xb5][i % 4]);
    graph.push(0, &packet(1, 0, 0, false, &voice), origin, &mut stats);
    graph.push(
        0,
        &packet(2, 160, 101, true, &[1, 0, 0, 160]),
        origin + Duration::from_millis(20),
        &mut stats,
    );
    let first_voice = emit(&mut graph, origin, 40, &mut stats).unwrap();
    graph.push(
        0,
        &packet(3, 160, 101, false, &[1, 0x80, 1, 144]),
        origin + Duration::from_millis(40),
        &mut stats,
    );
    let first_event = emit(&mut graph, origin, 60, &mut stats).unwrap();
    let first_end = emit(&mut graph, origin, 80, &mut stats).unwrap();
    assert_eq!(timestamp(&first_event), timestamp(&first_end));

    // 新语音起点比单调输出时钟落后，确实触发offset变化；之后的新事件不能让旧事件映射失忆。
    graph.push(
        0,
        &packet(6, 600, 0, true, &voice),
        origin + Duration::from_millis(200),
        &mut stats,
    );
    graph.push(
        0,
        &packet(7, 760, 101, true, &[2, 0, 0, 160]),
        origin + Duration::from_millis(220),
        &mut stats,
    );
    graph.push(
        0,
        &packet(5, 160, 101, false, &[1, 0x80, 1, 144]),
        origin + Duration::from_millis(225),
        &mut stats,
    );
    let resumed_voice = emit(&mut graph, origin, 240, &mut stats).unwrap();
    let second_event = emit(&mut graph, origin, 260, &mut stats).unwrap();
    let late_end = emit(&mut graph, origin, 265, &mut stats).unwrap();
    let spurious_voice = emit(&mut graph, origin, 266, &mut stats);

    assert!(
        timestamp(&resumed_voice).wrapping_sub(timestamp(&first_voice)) > 600,
        "测试没有真正触发语音发送重锚"
    );
    assert_eq!(
        &resumed_voice[12..],
        &first_voice[12..],
        "相同新鲜来源音频在重锚后内容被改变"
    );
    assert_eq!(second_event[12], 2);
    assert_eq!(
        &late_end[12..],
        &[1, 0x80, 1, 144],
        "正常迟到结束副本不可被过滤或改写"
    );
    assert!(
        timestamp(&late_end) == timestamp(&first_event) && spurious_voice.is_none(),
        "迟到旧事件污染新时间线：旧起点={} 迟到结束={} 不应存在的音频={:?} PLC={} Missing={}",
        timestamp(&first_event),
        timestamp(&late_end),
        spurious_voice.as_ref().map(|p| p[1] & 127),
        stats.processed_plc_frames,
        stats.processed_missing_frames
    );
    assert_eq!(stats.processed_decoded_frames, 2);
    assert_eq!(stats.processed_plc_frames, 0);
    assert_eq!(stats.processed_missing_frames, 0);
    // 媒体与两个事件共用新的连续输出包序，即使事件时间戳必须保持较早的恒定起点。
    let packets = [
        first_voice,
        first_event,
        first_end,
        resumed_voice,
        second_event,
        late_end,
    ];
    for adjacent in packets.windows(2) {
        let left = u16::from_be_bytes(adjacent[0][2..4].try_into().unwrap());
        let right = u16::from_be_bytes(adjacent[1][2..4].try_into().unwrap());
        assert_eq!(right, left.wrapping_add(1));
        assert_eq!(
            &adjacent[0][8..12],
            &adjacent[1][8..12],
            "重锚不应偷偷重置TX身份"
        );
    }
}
