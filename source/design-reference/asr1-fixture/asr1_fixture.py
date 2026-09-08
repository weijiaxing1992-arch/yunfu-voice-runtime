"""ASR1 设计夹具：有界字节协议和确定性模拟结果，不是生产适配器或识别模型。

本文件不创建 socket、不启动线程。时钟必须显式传入，便于在每个接收/写入边界注入暂停。
"""
from collections import deque
from dataclasses import dataclass
import base64
import hashlib
import json
import re
import struct

MAX_LENGTH = 8192
PERIOD_NS = 100_000_000
KINDS = {name: number for number, name in enumerate(
    ('HELLO', 'READY', 'AUDIO', 'MARKER', 'FINISH', 'RESULT', 'DONE', 'ERROR', 'CANCEL', 'PING', 'PONG'), 1)}
AUDIO = struct.Struct('<16s8Q4I5H6s')
assert AUDIO.size == 112


class ProtocolError(ValueError):
    """明确协议/生命周期失败；调用方不能把异常转换为识别完成。"""


def require(ok, reason):
    if not ok:
        raise ProtocolError(reason)


def u64(text):
    """JSON u64 使用规范十进制字符串，不接受浮点、负值或前导零。"""
    require(isinstance(text, str) and re.fullmatch(r'0|[1-9][0-9]{0,19}', text), 'u64_format')
    value = int(text)
    require(value < 1 << 64, 'u64_overflow')
    return value


def token(text):
    require(isinstance(text, str) and re.fullmatch('[0-9a-f]{32}', text) and int(text, 16), 'token')
    return bytes.fromhex(text)


def fields(value, expected):
    require(isinstance(value, dict) and set(value) == set(expected.split()), 'json_fields')


def strict_json(raw):
    """小型元数据使用严格 JSON；重复键/非对象/非有限数/尾随内容均拒绝。"""
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, 'duplicate_json_key')
            result[key] = value
        return result
    try:
        result = json.loads(raw.decode('utf-8'), object_pairs_hook=pairs,
                            parse_constant=lambda _: (_ for _ in ()).throw(ProtocolError('nonfinite_json')))
    except (UnicodeError, json.JSONDecodeError, RecursionError) as error:
        raise ProtocolError('invalid_json') from error
    require(isinstance(result, dict), 'json_object_required')
    return result


def packet(kind, value):
    """统一前缀 length 只数其后字节；PCM 保持二进制，元数据才编码 JSON。"""
    kind = KINDS[kind] if isinstance(kind, str) else kind
    require(kind in KINDS.values(), 'kind')
    body = value if isinstance(value, bytes) else json.dumps(value, ensure_ascii=False, separators=(',', ':'), allow_nan=False).encode()
    require(4 + len(body) <= MAX_LENGTH, 'message_limit')
    return struct.pack('<IBBH', 4 + len(body), kind, 1, 0) + body


class Decoder:
    """一次 read 至多剩余固定容量；消息分片不会延长期限或导致无限积累。"""
    def __init__(self):
        self.buffer = bytearray()

    def feed(self, data):
        require(len(self.buffer) + len(data) <= MAX_LENGTH + 4, 'receive_buffer_limit')
        self.buffer.extend(data)
        records = []
        while len(self.buffer) >= 4:
            length, = struct.unpack_from('<I', self.buffer)
            require(4 <= length <= MAX_LENGTH, 'message_length')
            if len(self.buffer) < length + 4:
                break
            _, kind, version, reserved = struct.unpack_from('<IBBH', self.buffer)
            require(kind in KINDS.values() and version == 1 and reserved == 0, 'prefix')
            body = bytes(self.buffer[8:length + 4])
            del self.buffer[:length + 4]
            records.append((kind, body))
        return records

    def eof(self):
        require(not self.buffer, 'truncated_eof')


@dataclass(frozen=True)
class Audio:
    """固定头和原样 PCM；16k仅验证线协议尺寸，不在夹具里冒充已验证重采样。"""
    stream_token: bytes
    event_seq: int
    source_generation: int
    source_segment: int
    rtp_sequence: int
    rtp_timestamp: int
    media_ns: int
    lower_ns: int
    expires_ns: int
    ssrc: int
    sample_rate: int
    rtp_clock_rate: int
    filter_delay_ns: int
    flags: int
    clock_domain: int
    sample_count: int
    origin: int
    pcm: bytes

    def encode(self):
        values = [getattr(self, name) for name in self.__dataclass_fields__ if name != 'pcm']
        return AUDIO.pack(*values, len(self.pcm), b'\0' * 6) + self.pcm

    @classmethod
    def decode(cls, raw):
        require(len(raw) >= AUDIO.size, 'audio_header')
        values = AUDIO.unpack_from(raw)
        require(values[-1] == b'\0' * 6 and values[-2] == len(raw) - AUDIO.size, 'audio_length_reserved')
        value = cls(*values[:-2], raw[AUDIO.size:])
        require(value.stream_token != b'\0' * 16 and value.event_seq > 0 and value.source_generation > 0 and value.source_segment > 0, 'audio_identity')
        require(value.sample_rate in (8000, 16000) and value.rtp_clock_rate == 8000
                and value.sample_count == value.sample_rate // 50 and len(value.pcm) == value.sample_count * 2, 'audio_format')
        require(value.origin == 1 and value.flags & 15 == 15 and value.flags & ~0x3ff == 0, 'decoded_origin_required')
        require(value.filter_delay_ns == (0 if value.sample_rate == 8000 else 3_937_500), 'filter_delay')
        return value

    def check_fresh(self, now, domain):
        require(domain in (1, 2) and self.clock_domain == domain, 'clock_domain')
        require(0 < self.lower_ns <= now and self.lower_ns < (1 << 64) - PERIOD_NS
                and self.expires_ns == self.lower_ns + PERIOD_NS, 'immutable_deadline')
        require(now < self.expires_ns, 'expired_audio')

    def check_derivation(self, original):
        """转换可改变采样格式，不能把原来源位置和 lower/expiry 一起平移续期。"""
        for field in self.__dataclass_fields__:
            if field not in ('sample_rate', 'sample_count', 'filter_delay_ns', 'pcm'):
                require(getattr(self, field) == getattr(original, field), 'derived_source_changed')
        require(original.sample_rate == 8000, 'original_native_rate')
        require(self.sample_rate != 8000 or self.pcm == original.pcm, 'native_pcm_changed')


class WriteFence:
    """只模拟发送检查点；不把检查与 syscall 当原子操作，部分提交后明确未知。"""
    def __init__(self, audio, original):
        self.audio = audio
        self.original = original
        self.total = len(packet('AUDIO', audio.encode()))
        self.written = 0
        self.closed = False

    def before_write(self, now, domain):
        require(not self.closed, 'writer_closed')
        try:
            self.audio.check_derivation(self.original)
            self.audio.check_fresh(now, domain)
        except ProtocolError:
            self.closed = True
            raise ProtocolError('submission_unknown' if self.written else 'expired_before_submission')
        return min(20_000_000, self.audio.expires_ns - now)

    def committed(self, amount):
        require(not self.closed and 0 < amount <= self.total - self.written, 'write_progress')
        self.written += amount


class DeadlineWindow:
    """握手/完整帧/结束等待的总预算；首字节或部分进展不能重新计时。"""
    def __init__(self, now, budget_ns):
        require(type(now) is int and now >= 0 and type(budget_ns) is int and budget_ns > 0, 'deadline_input')
        self.expires = now + budget_ns

    def check(self, now):
        require(now < self.expires, 'total_deadline')
        return self.expires - now


class ConnectionBudget:
    """有限连接额度模型：starting 和未知清理均占位；只有确证回收才能归还。"""
    def __init__(self, max_calls, maximum=64):
        require(type(max_calls) is int and type(maximum) is int and 0 < maximum <= max_calls, 'connection_limit')
        self.maximum = maximum
        self.leases = {}

    def reserve(self, identity):
        token(identity)
        require(identity not in self.leases, 'duplicate_lease')
        require(len(self.leases) < self.maximum, 'overloaded')
        self.leases[identity] = 'starting'

    def active(self, identity):
        require(self.leases.get(identity) == 'starting', 'lease_not_starting')
        self.leases[identity] = 'active'

    def cleanup_unknown(self, identity):
        require(identity in self.leases, 'unknown_lease')
        self.leases[identity] = 'cleanup_pending'

    def release(self, identity, *, rx_stopped, transport_closed):
        require(identity in self.leases and rx_stopped and transport_closed, 'cleanup_unconfirmed')
        del self.leases[identity]


HELLO_FIELDS = 'protocol stream_token uuid subscription_id source_rate target_rate channels format frame_ms clock_domain plc_policy gap_policy'
MARKER_FIELDS = ('stream_token rx_kind event_seq source_generation source_segment clock_domain lower_ns expires_ns '
                 'flags ssrc rtp_sequence rtp_timestamp media_ns sample_rate rtp_clock_rate '
                 'observation_age_ms arrival_age_ms deadline_lateness_ms queue_age_ms '
                 'known_duration_ticks gap sid_b64 cn_applied reason boundary discarded_packets')
FINISH_FIELDS = 'stream_token last_event_seq audio_frames samples markers'
RESULT_FIELDS = ('stream_token result_seq utterance_id revision source_generation source_segment type text '
                 'first_event_seq last_event_seq start_media_ns end_media_ns coverage')


class Provider:
    """一个供应商流的有限状态；摘要文字只证明收到哪些 PCM，不是 ASR 转录。"""
    def __init__(self, domain):
        require(domain in (1, 2), 'unsupported_clock')
        self.domain = domain
        self.state = 'starting'
        self.hello = None
        self.last_seq = self.audio_frames = self.samples = self.markers = 0
        self.source = None
        self.next_media_ns = None
        self.minimum_media_ns = None
        self.last_rtp = None
        self.suspended = False
        self.hash = hashlib.sha256()
        self.result_seq = self.utterance = self.revision = 0
        self.first_seq = self.first_media_ns = None
        self.last_audio_seq = self.last_audio_end_ns = 0
        self.discontinuous = False

    def counters(self):
        return {'stream_token': self.hello['stream_token'], 'last_event_seq': str(self.last_seq),
                'audio_frames': str(self.audio_frames), 'samples': str(self.samples), 'markers': str(self.markers)}

    def _result(self, kind):
        self.result_seq += 1
        self.revision += 1
        return packet('RESULT', {'stream_token': self.hello['stream_token'], 'result_seq': str(self.result_seq),
            'utterance_id': str(self.utterance), 'revision': str(self.revision),
            'source_generation': str(self.source[0]), 'source_segment': str(self.source[1]),
            'type': kind, 'text': 'mock-sha256:' + self.hash.hexdigest(),
            'first_event_seq': str(self.first_seq), 'last_event_seq': str(self.last_audio_seq),
            'start_media_ns': str(self.first_media_ns), 'end_media_ns': str(self.last_audio_end_ns),
            'coverage': 'discontinuous' if self.discontinuous else 'complete'})

    def accept(self, kind, raw, now):
        require(self.state not in ('completed', 'cancelled', 'failed'), 'provider_terminal')
        if kind == KINDS['HELLO']:
            require(self.state == 'starting', 'duplicate_hello')
            value = strict_json(raw)
            fields(value, HELLO_FIELDS)
            token(value['stream_token'])
            require(value['protocol'] == 'ASR1' and u64(value['subscription_id']) > 0, 'hello_identity')
            require(isinstance(value['uuid'], str) and 0 < len(value['uuid'].encode()) <= 128 and not re.search(r'\s', value['uuid']), 'uuid')
            require(all(type(value[k]) is int for k in ('source_rate', 'target_rate', 'channels', 'frame_ms', 'clock_domain')),
                    'hello_integer_types')
            require(value['source_rate'] == 8000 and value['target_rate'] in (8000, 16000)
                    and value['channels'] == 1 and value['format'] == 's16le' and value['frame_ms'] == 20
                    and value['clock_domain'] == self.domain and value['plc_policy'] == 'metadata'
                    and value['gap_policy'] == 'explicit', 'hello_capabilities')
            self.hello = value
            self.state = 'streaming'
            return [packet('READY', value)]
        require(self.state != 'starting', 'hello_required')
        if kind == KINDS['AUDIO']:
            require(self.state == 'streaming', 'input_closed')
            audio = Audio.decode(raw)
            audio.check_fresh(now, self.domain)
            require(audio.stream_token == token(self.hello['stream_token']) and audio.sample_rate == self.hello['target_rate'], 'audio_stream')
            require(audio.event_seq == self.last_seq + 1, 'observation_sequence')
            require(self.source == (audio.source_generation, audio.source_segment), 'source_boundary_required')
            require(not self.suspended, 'source_boundary_required_after_suspend')
            require(self.minimum_media_ns is None or audio.media_ns >= self.minimum_media_ns, 'media_time_reversal')
            require(self.last_rtp is None or (audio.rtp_sequence > self.last_rtp[0]
                    and audio.rtp_timestamp > self.last_rtp[1]), 'expanded_rtp_reversal')
            if self.next_media_ns is not None and audio.media_ns != self.next_media_ns:
                self.discontinuous = True
            self.next_media_ns = audio.media_ns + 20_000_000
            self.minimum_media_ns = self.next_media_ns
            self.last_rtp = (audio.rtp_sequence, audio.rtp_timestamp)
            if self.first_seq is None:
                self.utterance += 1
                self.first_seq, self.first_media_ns = audio.event_seq, audio.media_ns
            self.last_seq = self.last_audio_seq = audio.event_seq
            self.last_audio_end_ns = self.next_media_ns
            self.audio_frames += 1
            self.samples += audio.sample_count
            self.hash.update(audio.pcm)
            return [self._result('partial')] if self.audio_frames % 2 == 0 else []
        value = strict_json(raw)
        require(value.get('stream_token') == self.hello['stream_token'], 'stream_token')
        if kind == KINDS['MARKER']:
            require(self.state == 'streaming', 'input_closed')
            fields(value, MARKER_FIELDS)
            event, rxkind = u64(value['event_seq']), value['rx_kind']
            require(type(rxkind) is int and 2 <= rxkind <= 13, 'marker_kind')
            flags = value['flags']
            require(type(flags) is int and 0 <= flags <= 0x3ff, 'marker_flags')
            require(value['sample_rate'] == value['rtp_clock_rate'] == 8000, 'marker_native_rate')
            for key in ('ssrc', 'observation_age_ms', 'arrival_age_ms', 'deadline_lateness_ms', 'queue_age_ms'):
                require(type(value[key]) is int and 0 <= value[key] < 1 << 32, 'marker_u32')
            for key, bit in (('ssrc', 0), ('rtp_timestamp', 1), ('rtp_sequence', 2), ('media_ns', 3)):
                number = value[key] if key == 'ssrc' else u64(value[key])
                require(flags & (1 << bit) or number == 0, 'invalid_position_not_zero')
            require(flags & 256 or value['arrival_age_ms'] == 0, 'unknown_arrival_not_zero')
            require(flags & 512 or value['deadline_lateness_ms'] == 0, 'unknown_deadline_not_zero')
            require(type(value['cn_applied']) is bool and type(value['reason']) is int and 0 <= value['reason'] <= 65535, 'marker_flags')
            require(type(value['boundary']) is int and 0 <= value['boundary'] <= 6, 'marker_boundary')
            u64(value['discarded_packets'])
            lower, expires = u64(value['lower_ns']), u64(value['expires_ns'])
            require(value['clock_domain'] == self.domain, 'marker_clock')
            if rxkind in (10, 11, 12):
                require(lower == expires == 0, 'summary_deadline')
                require(flags == 0 and value['known_duration_ticks'] is None, 'summary_is_not_audio')
            else:
                require(0 < lower <= now < expires and expires == lower + PERIOD_NS, 'marker_expired')
            if rxkind == 10:
                gap = value['gap'];fields(gap, 'first last decoded plc reasons')
                first, last = u64(gap['first']), u64(gap['last'])
                require(first == self.last_seq + 1 and last == event and first <= last
                        and u64(gap['decoded']) + u64(gap['plc']) <= last - first + 1, 'gap_accounting')
                require(type(gap['reasons']) is int and 1 <= gap['reasons'] <= 7, 'gap_reason')
            elif rxkind in (11, 12):
                require(event == self.last_seq and value['gap'] is None, 'terminal_sequence')
            else:
                require(event == self.last_seq + 1 and value['gap'] is None, 'observation_sequence')
            if value['known_duration_ticks'] is not None:
                u64(value['known_duration_ticks'])
            require(rxkind != 2 or value['known_duration_ticks'] == '160', 'plc_known_duration')
            require(rxkind != 6 or value['known_duration_ticks'] is None, 'auxiliary_is_not_20ms_silence')
            if rxkind == 4:
                try:
                    sid = base64.b64decode(value['sid_b64'], validate=True)
                except (ValueError, TypeError) as error:
                    raise ProtocolError('cn_sid') from error
                require(1 <= len(sid) <= 480, 'cn_sid')
            else:
                require(value['sid_b64'] is None and not value['cn_applied'], 'sid_only_for_cn')
            source = (u64(value['source_generation']), u64(value['source_segment']))
            if rxkind == 7:
                require(all(source) and (self.source is None or source > self.source), 'new_source_identity')
                self.source, self.next_media_ns = source, None
                self.minimum_media_ns = self.last_rtp = None
                self.suspended = False
                self.first_seq = self.first_media_ns = None
                self.hash, self.revision = hashlib.sha256(), 0
            elif rxkind not in (10, 11, 12):
                require(source == self.source, 'marker_source')
            if rxkind not in (7, 13):
                self.discontinuous = True
                self.next_media_ns = None
            if rxkind == 8:
                # 暂停结束开放 utterance；恢复必须先有新 SourceBoundary。
                self.suspended = True
                self.first_seq = self.first_media_ns = None
                self.hash, self.revision = hashlib.sha256(), 0
            self.last_seq = event
            self.markers += 1
            if rxkind in (9, 12):
                self.state = 'failed'
                raise ProtocolError('rx_observation_failed')
            if rxkind == 11:
                self.state = 'input_ended'
            return []
        if kind == KINDS['FINISH']:
            fields(value, FINISH_FIELDS)
            require(value == self.counters(), 'finish_prefix_mismatch')
            result = [self._result('final')] if self.first_seq is not None else []
            self.state = 'completed'
            return result + [packet('DONE', self.counters())]
        if kind == KINDS['CANCEL']:
            fields(value, 'stream_token reason')
            require(isinstance(value['reason'], str) and len(value['reason'].encode()) <= 128, 'cancel_reason')
            self.state = 'cancelled'
            return []
        if kind == KINDS['PING']:
            fields(value, 'stream_token nonce');u64(value['nonce'])
            return [packet('PONG', value)]
        raise ProtocolError('client_message_kind')

    def eof(self):
        require(self.state in ('completed', 'cancelled'), 'unexpected_provider_eof')


class ResultConsumer:
    """下一批适配器的离线验收 oracle；真实生产代码尚未采用此实现。"""
    def __init__(self, stream_token, source, capacity=8):
        require(1 <= capacity <= 8, 'result_budget')
        self.token, self.source, self.capacity = stream_token, source, capacity
        self.queue = deque()
        self.input_finished = self.rx_stopped = self.done = self.cancelled = False
        self.result_seq = self.utterance = self.revision = self.final_utterance = 0
        self.closed_utterance = 0
        self.partial_invalidated = 0
        self.late_dropped = self.partial_coalesced = 0
        self.submitted_event = 0
        self.error = None
        self.expected_finish = None
        self.spans = []
        self.audio_frames = self.samples = 0
        self.last_final_event = 0
        self.suspended = False
        self.pending_discontinuity = False

    def note_audio(self, audio):
        """仅完整提交后登记；合并连续区间，最多64段元数据，不保存PCM或无限历史。"""
        require(not self.input_finished and not self.cancelled and not self.suspended, 'submission_closed')
        require((audio.source_generation, audio.source_segment) == self.source, 'submitted_source')
        require(audio.event_seq > self.submitted_event, 'submitted_order')
        require(not self.spans or audio.media_ns >= self.spans[-1][3], 'submitted_media_reversal')
        if self.spans and audio.event_seq == self.spans[-1][1] + 1 and audio.media_ns == self.spans[-1][3] and not self.pending_discontinuity:
            self.spans[-1][1] = audio.event_seq
            self.spans[-1][3] = audio.media_ns + 20_000_000
        else:
            if len(self.spans) == 64:
                self.error = 'input_provenance_overflow'
                raise ProtocolError(self.error)
            broken = (self.pending_discontinuity or audio.event_seq != self.submitted_event + 1
                      or bool(self.spans and audio.media_ns != self.spans[-1][3]))
            self.spans.append([audio.event_seq, audio.event_seq, audio.media_ns, audio.media_ns + 20_000_000, broken])
        self.submitted_event = audio.event_seq
        self.pending_discontinuity = False
        self.audio_frames += 1
        self.samples += audio.sample_count

    def note_marker(self, value):
        """记录已完整提交 marker 的语义；辅助事件占序号但不必然形成音频缺口。"""
        require(not self.input_finished and not self.cancelled, 'submission_closed')
        fields(value, MARKER_FIELDS)
        require(value['stream_token'] == self.token, 'submitted_marker_token')
        event, kind = u64(value['event_seq']), value['rx_kind']
        require(type(kind) is int and 2 <= kind <= 13 and event >= self.submitted_event, 'submitted_marker')
        if kind == 13:
            require(event == self.submitted_event + 1 and
                    (u64(value['source_generation']), u64(value['source_segment'])) == self.source, 'auxiliary_order')
        else:
            self.pending_discontinuity = True
        self.submitted_event = event

    def _validate_input_range(self, value):
        first, last = u64(value['first_event_seq']), u64(value['last_event_seq'])
        begin = next((span for span in self.spans if span[0] <= first <= span[1]), None)
        end = next((span for span in self.spans if span[0] <= last <= span[1]), None)
        require(begin is not None and end is not None and first > self.last_final_event, 'result_requires_decoded_range')
        require(u64(value['start_media_ns']) == begin[2] + (first - begin[0]) * 20_000_000
                and u64(value['end_media_ns']) == end[2] + (last - end[0] + 1) * 20_000_000,
                'result_requires_submitted_media')
        # 可有由telephone-event隔开的两个真实音频区间；仅实质缺口要求discontinuous。
        first_index, last_index = self.spans.index(begin), self.spans.index(end)
        require(not any(span[4] for span in self.spans[first_index + 1:last_index + 1])
                or value['coverage'] == 'discontinuous', 'gap_claimed_complete')

    def finish_input(self, expected):
        require(not self.cancelled and not self.input_finished, 'input_already_closed')
        fields(expected, FINISH_FIELDS)
        require(u64(expected['audio_frames']) == self.audio_frames and u64(expected['samples']) == self.samples
                and u64(expected['last_event_seq']) >= self.submitted_event, 'finish_submitted_audio')
        self.expected_finish = dict(expected)
        self.submitted_event = u64(expected['last_event_seq'])
        self.input_finished = True

    def lifetime_end(self):
        self.cancelled = True
        self.late_dropped += len(self.queue)
        self.queue.clear()

    def source_boundary(self, source):
        """边界使开放 partial 失效；已经提交的 final 记录保持原值。"""
        require(source > self.source and all(source), 'consumer_source_boundary')
        event = None
        if self.utterance > self.closed_utterance:
            event = {'type': 'partial_invalidated', 'utterance_id': str(self.utterance),
                     'source_generation': str(self.source[0]), 'source_segment': str(self.source[1])}
            self.partial_invalidated += 1
            self.closed_utterance = self.utterance
        self.queue = deque(item for item in self.queue if item['type'] == 'final')
        self.source = source
        self.spans.clear()
        self.last_final_event = 0
        self.suspended = False
        self.pending_discontinuity = False
        return event

    def inactive_suspended(self):
        """无新来源时先撤销开放 partial；只有后续新边界可恢复音频和结果。"""
        self.suspended = True
        self.queue = deque(item for item in self.queue if item['type'] == 'final')
        if self.utterance > self.closed_utterance:
            self.closed_utterance = self.utterance
            self.partial_invalidated += 1
            return {'type': 'partial_invalidated', 'utterance_id': str(self.utterance)}
        return None

    def accept(self, kind, raw):
        if self.cancelled:
            self.late_dropped += 1
            return
        require(self.error is None and not self.done, 'result_stream_terminal')
        value = strict_json(raw)
        require(value.get('stream_token') == self.token, 'result_token')
        if kind == KINDS['DONE']:
            fields(value, FINISH_FIELDS)
            require(self.input_finished and value == self.expected_finish, 'done_requires_exact_finish')
            require(self.utterance == self.closed_utterance, 'done_before_final')
            self.done = True
            return
        if kind == KINDS['ERROR']:
            # 即便错误帧本身不合法也锁定失败，不能让 null code 复活接收状态。
            self.error = 'invalid_provider_error'
            fields(value, 'stream_token code')
            require(isinstance(value['code'], str) and re.fullmatch('[a-z][a-z0-9_]{0,127}', value['code']), 'error_code')
            self.error = value['code']
            raise ProtocolError('provider_error')
        require(kind == KINDS['RESULT'], 'result_kind')
        fields(value, RESULT_FIELDS)
        seq, utterance, revision = (u64(value[k]) for k in ('result_seq', 'utterance_id', 'revision'))
        require(seq == self.result_seq + 1 and utterance > 0 and revision > 0, 'result_sequence')
        source = (u64(value['source_generation']), u64(value['source_segment']))
        require(all(source) and source <= self.source, 'future_source_result')
        require(0 < u64(value['first_event_seq']) <= u64(value['last_event_seq']) <= self.submitted_event, 'result_input_range')
        require(u64(value['start_media_ns']) < u64(value['end_media_ns']), 'result_time_range')
        require(value['type'] in ('partial', 'final') and value['coverage'] in ('complete', 'discontinuous')
                and isinstance(value['text'], str) and len(value['text'].encode()) <= 4096, 'result_value')
        if source < self.source or self.suspended:
            # 本地已跨来源边界；消费协议序号但永不发布旧分段转录。
            self.result_seq = seq
            self.late_dropped += 1
            return
        require(utterance > self.closed_utterance and utterance >= self.utterance
                and (utterance != self.utterance or revision > self.revision), 'final_or_revision_rewritten')
        require(utterance == self.utterance or self.utterance == self.closed_utterance, 'multiple_open_utterances')
        self._validate_input_range(value)
        self.result_seq, self.utterance, self.revision = seq, utterance, revision
        if value['type'] == 'final':
            self.final_utterance = utterance
            self.closed_utterance = utterance
            self.last_final_event = u64(value['last_event_seq'])
            # 已 final 的输入不再需要保留；若最后一个连续段还有新输入，只裁已完成前缀。
            self.spans = [span for span in self.spans if span[1] > self.last_final_event]
            if self.spans and self.spans[0][0] <= self.last_final_event:
                span = self.spans[0]
                span[2] += (self.last_final_event + 1 - span[0]) * 20_000_000
                span[0] = self.last_final_event + 1
        # 只合并尚未交付的同utterance partial；final满队列必须明确失败，不能默默丢失。
        if value['type'] == 'partial':
            for index, old in enumerate(self.queue):
                if old['type'] == 'partial' and old['utterance_id'] == value['utterance_id']:
                    self.queue[index] = value
                    self.partial_coalesced += 1
                    return
        if len(self.queue) >= self.capacity:
            self.error = 'result_overflow'
            raise ProtocolError(self.error)
        self.queue.append(value)

    def next(self):
        require(not self.cancelled, 'lifetime_ended')
        require(self.error is None, self.error or 'result_failed')
        if self.queue:
            return self.queue.popleft()
        if self.done and self.rx_stopped:
            raise EOFError('completed')
        return None

    def eof(self):
        require(self.done and not self.cancelled and self.error is None, 'eof_is_not_done')
