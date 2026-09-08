"""ASR1 离线反例：严格字节解析、真实/合成音频区分与生命周期。

这些用例不实例化 Server、Rust 或供应商网络；它们是下一批真实测试的独立预期模型。
"""
import base64
from dataclasses import replace
import hashlib
import json
import struct
import unittest

from asr1_fixture import (AUDIO, Audio, ConnectionBudget, DeadlineWindow, Decoder, KINDS, MAX_LENGTH, PERIOD_NS, ProtocolError,
                         Provider, ResultConsumer, WriteFence, packet, strict_json, u64)
from fault_scenarios import SCENARIOS, chunks

TOKEN = '123456789abcdef0123456789abcdef0'
OTHER = 'abcdef0123456789abcdef0123456789'
NOW = 10_000_000_000


def hello(rate=8000):
    return {'protocol': 'ASR1', 'stream_token': TOKEN, 'uuid': 'test-call-01', 'subscription_id': '19',
            'source_rate': 8000, 'target_rate': rate, 'channels': 1, 'format': 's16le', 'frame_ms': 20,
            'clock_domain': 2, 'plc_policy': 'metadata', 'gap_policy': 'explicit'}


def audio(seq=2, rate=8000, source=(1, 1), media_ns=None, lower=NOW):
    # 每个样本均有独立可复算数值；16k只是输入格式夹具，绝非已验证 FIR 输出。
    samples = [((index * 499 + seq * 137) % 60_001) - 30_000 for index in range(rate // 50)]
    pcm = struct.pack('<' + 'h' * len(samples), *samples)
    return Audio(bytes.fromhex(TOKEN), seq, *source, (1 << 16) + seq, (1 << 32) + seq * 160,
                 (seq - 2) * 20_000_000 if media_ns is None else media_ns, lower, lower + PERIOD_NS,
                 0x12345678, rate, 8000, 0 if rate == 8000 else 3_937_500, 15, 2, len(samples), 1, pcm)


def marker(kind=7, seq=1, source=(1, 1), **changes):
    value = {'stream_token': TOKEN, 'rx_kind': kind, 'event_seq': str(seq),
             'source_generation': str(source[0]), 'source_segment': str(source[1]),
             'clock_domain': 2, 'lower_ns': str(NOW), 'expires_ns': str(NOW + PERIOD_NS),
             'flags': 0, 'ssrc': 0, 'rtp_sequence': '0', 'rtp_timestamp': '0', 'media_ns': '0',
             'sample_rate': 8000, 'rtp_clock_rate': 8000, 'observation_age_ms': 0,
             'arrival_age_ms': 0, 'deadline_lateness_ms': 0, 'queue_age_ms': 0,
             'known_duration_ticks': None, 'gap': None, 'sid_b64': None, 'cn_applied': False,
             'reason': 0, 'boundary': 1 if kind == 7 else 0, 'discarded_packets': '0'}
    if kind in (10, 11, 12):
        value.update(lower_ns='0', expires_ns='0')
    if kind == 2:
        value['known_duration_ticks'] = '160'
    if kind == 4:
        value.update(sid_b64=base64.b64encode(b'\x35\x10\x20').decode(), cn_applied=True)
    value.update(changes)
    return value


def unpack(wire):
    records = Decoder().feed(wire)
    if len(records) != 1:
        raise AssertionError('夹具需要一个完整消息')
    return records[0]


def send(provider, kind, value, now=NOW):
    return provider.accept(*unpack(packet(kind, value)), now)


def provider(rate=8000):
    value = Provider(2)
    send(value, 'HELLO', hello(rate))
    send(value, 'MARKER', marker())
    return value


def result(seq=1, utterance=1, revision=1, kind='partial', source=(1, 1), **changes):
    value = {'stream_token': TOKEN, 'result_seq': str(seq), 'utterance_id': str(utterance),
             'revision': str(revision), 'source_generation': str(source[0]), 'source_segment': str(source[1]),
             'type': kind, 'text': '模拟结果', 'first_event_seq': '2', 'last_event_seq': '3',
             'start_media_ns': '0', 'end_media_ns': '40000000', 'coverage': 'complete'}
    value.update(changes)
    return value


def consumer(capacity=8):
    value = ResultConsumer(TOKEN, (1, 1), capacity)
    value.note_audio(audio())
    value.note_audio(audio(seq=3))
    return value


def consume(value, obj, kind='RESULT'):
    value.accept(*unpack(packet(kind, obj)))


class ProtocolCases(unittest.TestCase):
    def test_fixed_header_offsets_and_nonzero_pcm(self):
        value = audio()
        raw = value.encode()
        self.assertEqual(AUDIO.size, 112)
        self.assertEqual(len(raw), 432)
        self.assertEqual(raw[:16], bytes.fromhex(TOKEN))
        self.assertEqual(struct.unpack_from('<Q', raw, 16)[0], 2)
        self.assertEqual(struct.unpack_from('<Q', raw, 40)[0], 65538)
        self.assertEqual(struct.unpack_from('<Q', raw, 48)[0], 4294967616)
        self.assertEqual(struct.unpack_from('<Q', raw, 64)[0], NOW)
        self.assertEqual(struct.unpack_from('<Q', raw, 72)[0], NOW + PERIOD_NS)
        self.assertEqual(struct.unpack_from('<H', raw, 104)[0], 320)
        self.assertEqual(raw[106:112], bytes(6))
        self.assertEqual(struct.unpack_from('<hhhh', raw, 112), (-29726, -29227, -28728, -28229))
        self.assertEqual(Audio.decode(raw), value)

    def test_16k_is_320_samples_with_8k_rtp_clock(self):
        value = audio(rate=16000)
        self.assertEqual(len(packet('AUDIO', value.encode())), 760)
        self.assertEqual(Audio.decode(value.encode()).sample_count, 320)
        self.assertEqual(value.rtp_clock_rate, 8000)
        self.assertEqual(value.filter_delay_ns, 3_937_500)
        with self.assertRaisesRegex(ProtocolError, 'filter_delay'):
            Audio.decode(replace(value, filter_delay_ns=0).encode())

    def test_all_single_split_positions_preserve_bytes(self):
        wire = packet('AUDIO', audio().encode())
        for split in range(1, len(wire)):
            with self.subTest(split=split):
                decoder = Decoder()
                records = decoder.feed(wire[:split]) + decoder.feed(wire[split:])
                self.assertEqual(records, [(KINDS['AUDIO'], audio().encode())])
                decoder.eof()

    def test_fragmented_and_multiple_messages(self):
        wires = [packet('HELLO', hello()), packet('MARKER', marker()), packet('AUDIO', audio().encode())]
        decoder, records = Decoder(), []
        for part in chunks(b''.join(wires), [1, 2, 7, 40, 120, 500]):
            records.extend(decoder.feed(part))
        self.assertEqual([x[0] for x in records], [1, 4, 3])
        self.assertEqual(records[-1][1], audio().encode())

    def test_oversize_and_short_length_reject_before_body(self):
        for length in (0, 3, 8193, 0xffffffff):
            with self.subTest(length=length), self.assertRaisesRegex(ProtocolError, 'message_length'):
                Decoder().feed(struct.pack('<I', length))
        with self.assertRaisesRegex(ProtocolError, 'receive_buffer_limit'):
            Decoder().feed(b'x' * (MAX_LENGTH + 5))

    def test_unknown_prefix_values_rejected(self):
        for kind, version, reserved in ((0, 1, 0), (12, 1, 0), (1, 2, 0), (1, 1, 1)):
            with self.subTest(kind=kind, version=version, reserved=reserved), self.assertRaisesRegex(ProtocolError, 'prefix'):
                Decoder().feed(struct.pack('<IBBH', 4, kind, version, reserved))

    def test_half_frame_eof_is_error(self):
        for size in (1, 4, 7, 17, 100):
            with self.subTest(size=size):
                decoder = Decoder()
                decoder.feed(packet('AUDIO', audio().encode())[:size])
                with self.assertRaisesRegex(ProtocolError, 'truncated_eof'):
                    decoder.eof()

    def test_json_duplicate_unicode_nonfinite_and_trailing(self):
        for raw in (b'{"a":1,"a":2}', b'{"a":NaN}', b'{"a":Infinity}', b'[]', b'{"a":"\xff"}', b'{}{}'):
            with self.subTest(raw=raw), self.assertRaises(ProtocolError):
                strict_json(raw)
        self.assertEqual(strict_json('{"text":"你好"}'.encode()), {'text': '你好'})

    def test_u64_canonical_and_overflow(self):
        for raw in (1, True, '01', '-1', '+1', ' 1', '1.0', str(1 << 64)):
            with self.subTest(raw=raw), self.assertRaises(ProtocolError):
                u64(raw)
        self.assertEqual(u64(str((1 << 64) - 1)), (1 << 64) - 1)

    def test_hello_required_and_capabilities_exact(self):
        with self.assertRaisesRegex(ProtocolError, 'hello_required'):
            send(Provider(2), 'AUDIO', audio().encode())
        for update in ({'target_rate': 48000}, {'channels': True}, {'clock_domain': 1}, {'extra': 1},
                       {'stream_token': '0' * 32}, {'plc_policy': 'silence'}):
            with self.subTest(update=update), self.assertRaises(ProtocolError):
                send(Provider(2), 'HELLO', dict(hello(), **update))
        p = Provider(2)
        self.assertEqual(strict_json(unpack(send(p, 'HELLO', hello())[0])[1]), hello())
        with self.assertRaisesRegex(ProtocolError, 'duplicate_hello'):
            send(p, 'HELLO', hello())

    def test_audio_requires_matching_token_and_source_boundary(self):
        p = Provider(2)
        send(p, 'HELLO', hello())
        with self.assertRaisesRegex(ProtocolError, 'source_boundary_required'):
            send(p, 'AUDIO', audio(seq=1, media_ns=0).encode())
        send(p, 'MARKER', marker())
        for wrong in (replace(audio(), stream_token=bytes.fromhex(OTHER)), audio(source=(1, 2))):
            with self.subTest(wrong=wrong.source_segment), self.assertRaises(ProtocolError):
                send(p, 'AUDIO', wrong.encode())

    def test_audio_plc_origin_length_and_reserved_flags_reject(self):
        for value in (replace(audio(), origin=2), replace(audio(), flags=0), replace(audio(), flags=0x800f),
                      replace(audio(), pcm=audio().pcm[:-2]), replace(audio(), rtp_clock_rate=16000)):
            with self.subTest(origin=value.origin, flags=value.flags, size=len(value.pcm)), self.assertRaises(ProtocolError):
                Audio.decode(value.encode())
        raw = bytearray(audio().encode());raw[106] = 1
        with self.assertRaisesRegex(ProtocolError, 'audio_length_reserved'):
            Audio.decode(bytes(raw))


class MediaMappingCases(unittest.TestCase):
    def test_marker_does_not_reset_same_source_time_lower_bound(self):
        for kind in (2, 3, 4, 5, 6, 13):
            with self.subTest(kind=kind):
                p = provider();send(p, 'AUDIO', audio().encode())
                send(p, 'MARKER', marker(kind, 3))
                with self.assertRaisesRegex(ProtocolError, 'media_time_reversal'):
                    send(p, 'AUDIO', audio(seq=4, media_ns=0).encode())
        p = provider();send(p, 'AUDIO', audio().encode())
        send(p, 'MARKER', marker(10, 5, gap={'first': '3', 'last': '5', 'decoded': '1', 'plc': '1', 'reasons': 1}))
        with self.assertRaisesRegex(ProtocolError, 'media_time_reversal'):
            send(p, 'AUDIO', audio(seq=6, media_ns=0).encode())

    def test_complete_rtp_numbers_cannot_go_back_after_marker(self):
        p = provider();send(p, 'AUDIO', audio().encode());send(p, 'MARKER', marker(4, 3))
        for value in (replace(audio(seq=4), rtp_sequence=2), replace(audio(seq=4), rtp_timestamp=160)):
            with self.subTest(value=value.rtp_sequence), self.assertRaisesRegex(ProtocolError, 'expanded_rtp_reversal'):
                send(p, 'AUDIO', value.encode())

    def test_inactive_suspends_utterance_until_new_boundary(self):
        p = provider();send(p, 'AUDIO', audio().encode());send(p, 'AUDIO', audio(seq=3).encode())
        send(p, 'MARKER', marker(8, 4))
        with self.assertRaisesRegex(ProtocolError, 'source_boundary_required_after_suspend'):
            send(p, 'AUDIO', audio(seq=5).encode())
        send(p, 'MARKER', marker(7, 5, source=(1, 2), boundary=5))
        send(p, 'AUDIO', audio(seq=6, source=(1, 2), media_ns=0).encode())
        final = strict_json(unpack(send(p, 'FINISH', p.counters())[0])[1])
        self.assertEqual((final['utterance_id'], final['first_event_seq']), ('2', '6'))

    def test_partial_during_input_and_final_digest_actual_pcm(self):
        p = provider()
        self.assertEqual(send(p, 'AUDIO', audio().encode()), [])
        output = send(p, 'AUDIO', audio(seq=3).encode())
        partial = strict_json(unpack(output[0])[1])
        expected = 'mock-sha256:' + hashlib.sha256(audio().pcm + audio(seq=3).pcm).hexdigest()
        self.assertEqual(partial['type'], 'partial')
        self.assertEqual(p.state, 'streaming')
        self.assertEqual(partial['text'], expected)
        final, done = send(p, 'FINISH', p.counters())
        self.assertEqual(strict_json(unpack(final)[1])['text'], expected)
        self.assertEqual(strict_json(unpack(final)[1])['revision'], '2')
        self.assertEqual(strict_json(unpack(done)[1])['samples'], '320')

    def test_plc_cn_missing_and_dtmf_never_add_samples(self):
        p = provider()
        send(p, 'AUDIO', audio().encode())
        for seq, kind in enumerate((2, 3, 4, 5, 6, 13), 3):
            send(p, 'MARKER', marker(kind, seq))
            self.assertEqual((p.audio_frames, p.samples), (1, 160))
        send(p, 'AUDIO', audio(seq=9, media_ns=160_000_000).encode())
        final = strict_json(unpack(send(p, 'FINISH', p.counters())[0])[1])
        self.assertEqual(final['coverage'], 'discontinuous')
        self.assertEqual(final['end_media_ns'], '180000000')
        self.assertEqual(final['text'], 'mock-sha256:' + hashlib.sha256(audio().pcm + audio(seq=9, media_ns=160_000_000).pcm).hexdigest())

    def test_only_cn_has_sid_and_auxiliary_duration_is_unknown(self):
        for bad in (marker(6, 2, known_duration_ticks='160'), marker(2, 2, known_duration_ticks=None),
                    marker(13, 2, sid_b64='NRAg'), marker(4, 2, sid_b64='!'), marker(4, 2, sid_b64='')):
            with self.subTest(kind=bad['rx_kind']), self.assertRaises(ProtocolError):
                send(provider(), 'MARKER', bad)

    def test_gap_range_is_observation_count_not_silence_duration(self):
        p = provider()
        gap = marker(10, 6, source=(0, 0), gap={'first': '2', 'last': '6', 'decoded': '2', 'plc': '1', 'reasons': 3})
        send(p, 'MARKER', gap)
        self.assertEqual((p.last_seq, p.samples, p.audio_frames), (6, 0, 0))
        self.assertIsNone(p.next_media_ns)
        send(p, 'AUDIO', audio(seq=7, media_ns=900_000_000).encode())
        self.assertEqual(p.first_media_ns, 900_000_000)
        for change in ({'first': '1'}, {'decoded': '6'}, {'reasons': 8}):
            with self.subTest(change=change), self.assertRaises(ProtocolError):
                send(provider(), 'MARKER', dict(gap, gap=dict(gap['gap'], **change)))

    def test_summary_cannot_claim_audio_position_or_deadline(self):
        for wrong in (marker(11, 1, lower_ns=str(NOW)), marker(11, 1, flags=8, media_ns='20'),
                      marker(11, 1, known_duration_ticks='160'), marker(3, 2, rtp_timestamp='10')):
            with self.subTest(wrong=wrong['rx_kind']), self.assertRaises(ProtocolError):
                send(provider(), 'MARKER', wrong)

    def test_source_boundary_resets_hash_and_utterance(self):
        p = provider()
        send(p, 'AUDIO', audio().encode());send(p, 'AUDIO', audio(seq=3).encode())
        send(p, 'MARKER', marker(7, 4, source=(2, 1), boundary=2, discarded_packets='3'))
        next_audio = audio(seq=5, source=(2, 1), media_ns=0)
        send(p, 'AUDIO', next_audio.encode())
        final = strict_json(unpack(send(p, 'FINISH', p.counters())[0])[1])
        self.assertEqual(final['utterance_id'], '2')
        self.assertEqual(final['first_event_seq'], '5')
        self.assertEqual(final['text'], 'mock-sha256:' + hashlib.sha256(next_audio.pcm).hexdigest())

    def test_reversal_and_sequence_hole_are_rejected(self):
        p = provider();send(p, 'AUDIO', audio().encode())
        for wrong in (audio(seq=2), audio(seq=4), audio(seq=3, media_ns=19_999_999)):
            with self.subTest(seq=wrong.event_seq, media=wrong.media_ns), self.assertRaises(ProtocolError):
                send(p, 'AUDIO', wrong.encode())

    def test_zero_decoded_returns_no_invented_final(self):
        p = provider();send(p, 'MARKER', marker(4, 2));send(p, 'MARKER', marker(2, 3))
        output = send(p, 'FINISH', p.counters())
        self.assertEqual([unpack(x)[0] for x in output], [KINDS['DONE']])
        self.assertEqual(p.samples, 0)

    def test_observation_failure_is_terminal_not_final(self):
        for kind in (9, 12):
            p = provider()
            with self.subTest(kind=kind), self.assertRaisesRegex(ProtocolError, 'rx_observation_failed'):
                send(p, 'MARKER', marker(kind, 1 if kind == 12 else 2))
            with self.assertRaisesRegex(ProtocolError, 'provider_terminal'):
                send(p, 'FINISH', p.counters())


class FreshnessCases(unittest.TestCase):
    def test_total_handshake_and_finish_deadline_not_renewed_by_progress(self):
        for budget in (1_000_000_000, 2_000_000_000):
            with self.subTest(budget=budget):
                window = DeadlineWindow(NOW, budget)
                self.assertEqual(window.check(NOW + budget - 1), 1)
                with self.assertRaisesRegex(ProtocolError, 'total_deadline'):
                    window.check(NOW + budget)

    def test_exact_expiry_future_origin_and_wrong_domain(self):
        value = audio()
        value.check_fresh(NOW + PERIOD_NS - 1, 2)
        for now, domain in ((NOW + PERIOD_NS, 2), (NOW - 1, 2), (NOW, 1), (NOW, 0)):
            with self.subTest(now=now, domain=domain), self.assertRaises(ProtocolError):
                value.check_fresh(now, domain)

    def test_deadline_is_immutable_on_retry(self):
        value = audio();fence = WriteFence(value, value)
        self.assertEqual(fence.before_write(NOW, 2), 20_000_000)
        self.assertEqual(fence.before_write(NOW + 90_000_000, 2), 10_000_000)
        self.assertEqual(value.expires_ns, NOW + PERIOD_NS)
        with self.assertRaisesRegex(ProtocolError, 'immutable_deadline'):
            replace(value, expires_ns=NOW + 2 * PERIOD_NS).check_fresh(NOW, 2)
        with self.assertRaisesRegex(ProtocolError, 'expired_before_submission'):
            fence.before_write(NOW + PERIOD_NS, 2)

    def test_partial_submission_expires_then_cannot_resume(self):
        fence = WriteFence(audio(), audio())
        fence.before_write(NOW, 2);fence.committed(17)
        with self.assertRaisesRegex(ProtocolError, 'submission_unknown'):
            fence.before_write(NOW + 120_000_000, 2)
        self.assertEqual(fence.written, 17)
        with self.assertRaisesRegex(ProtocolError, 'write_progress'):
            fence.committed(1)
        with self.assertRaisesRegex(ProtocolError, 'writer_closed'):
            fence.before_write(NOW + 120_000_001, 2)

    def test_provider_rechecks_after_full_read_preemption(self):
        p = provider();wire = packet('AUDIO', audio().encode())
        # 已完整拿到字节后暂停；不得因为上次写前检查通过就交给模拟识别队列。
        parsed = unpack(wire)
        with self.assertRaisesRegex(ProtocolError, 'expired_audio'):
            p.accept(*parsed, NOW + 120_000_000)
        self.assertEqual((p.samples, p.audio_frames), (0, 0))

    def test_shifting_both_deadlines_cannot_hide_expired_source(self):
        original = audio()
        derived = replace(audio(rate=16000), lower_ns=NOW + 120_000_000, expires_ns=NOW + 220_000_000)
        derived.check_fresh(NOW + 120_000_001, 2)  # 数学上新配对合法，但不是原 RX 来源。
        with self.assertRaisesRegex(ProtocolError, 'derived_source_changed'):
            derived.check_derivation(original)
        with self.assertRaises(ProtocolError):
            WriteFence(derived, original).before_write(NOW + 120_000_001, 2)
        audio(rate=16000).check_derivation(original)
        with self.assertRaisesRegex(ProtocolError, 'native_pcm_changed'):
            replace(original, pcm=bytes(320)).check_derivation(original)

    def test_ordinary_marker_also_expires_but_summary_has_no_age(self):
        p = provider()
        with self.assertRaisesRegex(ProtocolError, 'marker_expired'):
            send(p, 'MARKER', marker(4, 2), now=NOW + PERIOD_NS)
        send(p, 'MARKER', marker(11, 1), now=NOW + 9 * PERIOD_NS)
        self.assertEqual(p.state, 'input_ended')


class LifecycleCases(unittest.TestCase):
    def test_auxiliary_event_number_does_not_invent_audio_gap(self):
        p = provider();c = ResultConsumer(TOKEN, (1, 1))
        first, second = audio(), audio(seq=4, media_ns=20_000_000)
        send(p, 'AUDIO', first.encode());c.note_audio(first)
        dtmf = marker(13, 3)
        send(p, 'MARKER', dtmf);c.note_marker(dtmf)
        output = send(p, 'AUDIO', second.encode());c.note_audio(second)
        c.accept(*unpack(output[0]))
        self.assertEqual(c.next()['coverage'], 'complete')
        self.assertEqual((c.audio_frames, c.samples), (2, 320))

    def test_starting_and_unknown_cleanup_retain_quota(self):
        budget = ConnectionBudget(8000, 2)
        budget.reserve(TOKEN);budget.reserve(OTHER)
        budget.active(TOKEN);budget.cleanup_unknown(TOKEN)
        with self.assertRaisesRegex(ProtocolError, 'overloaded'):
            budget.reserve('1' * 32)
        with self.assertRaisesRegex(ProtocolError, 'cleanup_unconfirmed'):
            budget.release(TOKEN, rx_stopped=False, transport_closed=True)
        self.assertEqual(len(budget.leases), 2)
        budget.release(TOKEN, rx_stopped=True, transport_closed=True)
        budget.reserve('1' * 32)
        self.assertEqual(len(budget.leases), 2)
        with self.assertRaisesRegex(ProtocolError, 'connection_limit'):
            ConnectionBudget(5, 64)

    def test_finish_prefix_must_be_exact(self):
        p = provider();send(p, 'AUDIO', audio().encode())
        with self.assertRaisesRegex(ProtocolError, 'finish_prefix_mismatch'):
            send(p, 'FINISH', dict(p.counters(), samples='161'))
        send(p, 'FINISH', p.counters())
        with self.assertRaisesRegex(ProtocolError, 'provider_terminal'):
            send(p, 'AUDIO', audio(seq=3).encode())

    def test_input_end_alone_is_not_success(self):
        p = provider();send(p, 'MARKER', marker(11, 1))
        with self.assertRaisesRegex(ProtocolError, 'unexpected_provider_eof'):
            p.eof()
        with self.assertRaisesRegex(ProtocolError, 'input_closed'):
            send(p, 'AUDIO', audio().encode())
        self.assertEqual(unpack(send(p, 'FINISH', p.counters())[0])[0], KINDS['DONE'])
        p.eof()

    def test_normal_finish_final_done_and_rx_stop_are_distinct(self):
        p = provider();send(p, 'AUDIO', audio().encode())
        c = ResultConsumer(TOKEN, (1, 1));c.note_audio(audio());c.finish_input(p.counters())
        # 音频期限已过也可以交付合法最终文本，识别结果使用通话生命周期授权。
        for wire in send(p, 'FINISH', p.counters(), now=NOW + 150_000_000):
            c.accept(*unpack(wire))
        self.assertEqual(c.next()['type'], 'final')
        self.assertIsNone(c.next())  # RX 未确认停止，不能对业务报完成。
        c.rx_stopped = True
        with self.assertRaisesRegex(EOFError, 'completed'):
            c.next()
        c.eof()

    def test_final_or_eof_without_done_cannot_complete(self):
        c = consumer();consume(c, result(kind='final'))
        with self.assertRaisesRegex(ProtocolError, 'eof_is_not_done'):
            c.eof()
        self.assertFalse(c.done)

    def test_done_before_finish_wrong_prefix_and_unfinalized_partial(self):
        finish = {'stream_token': TOKEN, 'last_event_seq': '3', 'audio_frames': '2', 'samples': '320', 'markers': '1'}
        c = consumer()
        with self.assertRaisesRegex(ProtocolError, 'done_requires_exact_finish'):
            consume(c, finish, 'DONE')
        c.finish_input(finish)
        with self.assertRaisesRegex(ProtocolError, 'done_requires_exact_finish'):
            consume(c, dict(finish, samples='321'), 'DONE')
        consume(c, result())
        with self.assertRaisesRegex(ProtocolError, 'done_before_final'):
            consume(c, finish, 'DONE')

    def test_hangup_drops_pending_and_late_transcripts(self):
        c = consumer();consume(c, result())
        c.lifetime_end();consume(c, result(seq=2, revision=2, kind='final'))
        self.assertEqual(c.late_dropped, 2)
        self.assertEqual(len(c.queue), 0)
        with self.assertRaisesRegex(ProtocolError, 'lifetime_ended'):
            c.next()

    def test_source_boundary_invalidates_partial_and_discards_old_result(self):
        c = consumer();consume(c, result())
        event = c.source_boundary((1, 2))
        self.assertEqual(event['type'], 'partial_invalidated')
        self.assertEqual(c.partial_invalidated, 1)
        consume(c, result(seq=2, revision=2, kind='final'))
        self.assertEqual(c.late_dropped, 1)
        c.note_audio(audio(seq=4, source=(1, 2), media_ns=0))
        c.note_audio(audio(seq=5, source=(1, 2), media_ns=20_000_000))
        consume(c, result(seq=3, utterance=2, source=(1, 2), first_event_seq='4', last_event_seq='5'))
        self.assertEqual(c.next()['source_segment'], '2')

    def test_wrong_identity_range_order_and_duplicate_final_fail(self):
        for wrong in (result(stream_token=OTHER), result(seq=2), result(source=(2, 1)),
                      result(last_event_seq='101'), result(start_media_ns='40000000'), result(text='x' * 4097)):
            with self.subTest(wrong=wrong), self.assertRaises(ProtocolError):
                consume(consumer(), wrong)
        c = consumer();consume(c, result(kind='final'))
        with self.assertRaisesRegex(ProtocolError, 'final_or_revision_rewritten'):
            consume(c, result(seq=2, revision=2, kind='final'))

    def test_partial_revision_and_single_open_utterance(self):
        c = consumer();consume(c, result())
        for wrong in (result(seq=2, revision=1), result(seq=2, utterance=2)):
            with self.subTest(wrong=wrong), self.assertRaises(ProtocolError):
                consume(c, wrong)

    def test_partial_coalescing_stays_bounded(self):
        c = consumer()
        for seq in range(1, 501):
            consume(c, result(seq=seq, revision=seq))
            self.assertEqual(len(c.queue), 1)
        self.assertEqual(c.partial_coalesced, 499)
        self.assertEqual(c.next()['revision'], '500')

    def test_final_overflow_is_explicit_failure(self):
        c = ResultConsumer(TOKEN, (1, 1))
        for seq in range(1, 9):
            c.note_audio(audio(seq=2 * seq));c.note_audio(audio(seq=2 * seq + 1))
            consume(c, result(seq=seq, utterance=seq, kind='final', first_event_seq=str(2 * seq),
                             last_event_seq=str(2 * seq + 1), start_media_ns=str((seq - 1) * 40_000_000),
                             end_media_ns=str(seq * 40_000_000)))
        c.note_audio(audio(seq=18));c.note_audio(audio(seq=19))
        with self.assertRaisesRegex(ProtocolError, 'result_overflow'):
            consume(c, result(seq=9, utterance=9, kind='final', first_event_seq='18', last_event_seq='19',
                             start_media_ns='320000000', end_media_ns='360000000'))
        self.assertEqual(len(c.queue), 8)
        self.assertEqual(c.error, 'result_overflow')
        with self.assertRaisesRegex(ProtocolError, 'result_overflow'):
            c.next()

    def test_provider_error_stays_failure(self):
        c = consumer()
        with self.assertRaisesRegex(ProtocolError, 'provider_error'):
            consume(c, {'stream_token': TOKEN, 'code': 'unsupported_discontinuity'}, 'ERROR')
        with self.assertRaisesRegex(ProtocolError, 'unsupported_discontinuity'):
            c.next()
        with self.assertRaisesRegex(ProtocolError, 'eof_is_not_done'):
            c.eof()

    def test_malformed_error_cannot_revive_consumer(self):
        for code in (None, '', 0, 'x' * 129):
            with self.subTest(code=code):
                c = consumer()
                with self.assertRaisesRegex(ProtocolError, 'error_code'):
                    consume(c, {'stream_token': TOKEN, 'code': code}, 'ERROR')
                self.assertEqual(c.error, 'invalid_provider_error')
                with self.assertRaisesRegex(ProtocolError, 'result_stream_terminal'):
                    consume(c, result(kind='final'))

    def test_results_require_actual_decoded_not_only_markers(self):
        c = ResultConsumer(TOKEN, (1, 1))
        c.finish_input({'stream_token': TOKEN, 'last_event_seq': '3', 'audio_frames': '0', 'samples': '0', 'markers': '3'})
        with self.assertRaisesRegex(ProtocolError, 'result_requires_decoded_range'):
            consume(c, result(kind='final'))
        c = consumer();c.source_boundary((1, 2))
        with self.assertRaisesRegex(ProtocolError, 'result_requires_decoded_range'):
            consume(c, result(kind='final', source=(1, 2)))

    def test_result_cannot_claim_missing_events_or_complete_across_gap(self):
        c = ResultConsumer(TOKEN, (1, 1));c.note_audio(audio());c.note_audio(audio(seq=4))
        with self.assertRaisesRegex(ProtocolError, 'result_requires_decoded_range'):
            consume(c, result(first_event_seq='3', last_event_seq='3', start_media_ns='20000000'))
        with self.assertRaisesRegex(ProtocolError, 'gap_claimed_complete'):
            consume(c, result(last_event_seq='4', end_media_ns='60000000'))
        consume(c, result(last_event_seq='4', end_media_ns='60000000', coverage='discontinuous'))
        self.assertEqual(c.next()['coverage'], 'discontinuous')

    def test_result_media_range_must_match_actual_input(self):
        with self.assertRaisesRegex(ProtocolError, 'result_requires_submitted_media'):
            consume(consumer(), result(start_media_ns='1'))

    def test_submitted_provenance_is_bounded_and_final_releases_ranges(self):
        c = ResultConsumer(TOKEN, (1, 1))
        for index in range(64):
            c.note_audio(audio(seq=2 + 2 * index))
        self.assertEqual(len(c.spans), 64)
        with self.assertRaisesRegex(ProtocolError, 'input_provenance_overflow'):
            c.note_audio(audio(seq=130))
        c = consumer();consume(c, result(kind='final'))
        self.assertEqual(c.spans, [])

    def test_suspension_invalidates_partial_and_blocks_old_source_until_boundary(self):
        c = consumer();consume(c, result())
        self.assertEqual(c.inactive_suspended()['type'], 'partial_invalidated')
        consume(c, result(seq=2, revision=2, kind='final'))
        self.assertEqual(c.late_dropped, 1)
        self.assertIsNone(c.next())
        with self.assertRaisesRegex(ProtocolError, 'submission_closed'):
            c.note_audio(audio(seq=4))
        c.source_boundary((1, 2));c.note_audio(audio(seq=4, source=(1, 2), media_ns=0))

    def test_fault_manifest_is_finite_and_does_not_execute_delays(self):
        self.assertEqual(len(SCENARIOS), 18)
        for name, scenario in SCENARIOS.items():
            with self.subTest(name=name):
                self.assertIn('checkpoint', scenario)
                self.assertIn('action', scenario)
                self.assertIn('expect', scenario)
        wire = packet('AUDIO', audio().encode())
        self.assertEqual(b''.join(chunks(wire, [17, 1, 3])), wire)
        with self.assertRaises(ValueError):
            chunks(wire, [0])


if __name__ == '__main__':
    unittest.main(verbosity=2)
