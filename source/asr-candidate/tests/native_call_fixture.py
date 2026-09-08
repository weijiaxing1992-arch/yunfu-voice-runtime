#!/usr/bin/env python3
"""供新增原版成对探针使用的单通道夹具：真实SIP/RTP，有限超时，完整字节证据。"""
import base64
import datetime
import hashlib
import json
import socket
import struct
import threading
import time
import uuid

import e2e


class Evidence:
    """锁保护收发顺序；认证请求只记长度与摘要，公开结果不保存可恢复秘密。"""
    def __init__(self):
        self.rows, self.size, self.lock = [], 0, threading.Lock()

    def add(self, transport, direction, data, secret=False):
        with self.lock:
            self.size += len(data)
            assert self.size <= 8 * 1024 * 1024, '原始证据超过8MiB上限'
            row = {'transport': transport, 'direction': direction, 'bytes': len(data),
                   'sha256': hashlib.sha256(data).hexdigest(), 'monotonic_ns': time.monotonic_ns(),
                   'at': datetime.datetime.now(datetime.timezone.utc).isoformat()}
            if secret:
                row['redacted'] = '隔离认证请求不保存可恢复秘密'
            else:
                row['base64'] = base64.b64encode(data).decode()
            self.rows.append(row)


class Peer:
    """按实际Content-Length重组ESL，不以+OK代替应用执行完成。"""
    def __init__(self, port, password, evidence):
        assert password and not any(char in password for char in '\r\n\x00')
        self.evidence, self.buffer, self.events = evidence, b'', []
        self.socket = socket.create_connection(('127.0.0.1', port), 3)
        assert self.frame()[0]['content-type'] == 'auth/request'
        self.send('auth ' + password, secret=True)
        assert self.frame()[0]['reply-text'] == '+OK accepted'

    def send(self, command, secret=False):
        packet = (command + '\n\n').encode()
        self.evidence.add('ESL', 'send', packet, secret)
        self.socket.settimeout(3)
        self.socket.sendall(packet)

    def frame(self, timeout=5):
        deadline = time.monotonic() + timeout
        while True:
            end = self.buffer.find(b'\n\n')
            if end >= 0:
                assert end <= 65536
                headers = {}
                for line in self.buffer[:end].decode().splitlines():
                    key, value = line.split(':', 1)
                    assert key.lower() not in headers
                    headers[key.lower()] = value.strip()
                size = int(headers.get('content-length', '0'))
                assert 0 <= size <= 1024 * 1024
                total = end + 2 + size
                if len(self.buffer) >= total:
                    body, self.buffer = self.buffer[end + 2:total], self.buffer[total:]
                    return headers, body
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError('ESL frame timeout')
            self.socket.settimeout(remaining)
            packet = self.socket.recv(65536)
            if not packet:
                raise EOFError('ESL连接关闭')
            self.evidence.add('ESL', 'receive', packet)
            self.buffer += packet
            assert len(self.buffer) <= 2 * 1024 * 1024

    def save(self, header, body):
        assert header['content-type'] == 'text/event-json'
        assert len(self.events) < 512
        value = json.loads(body)
        self.events.append(value)
        return value

    def command(self, command):
        self.send(command)
        for _ in range(512):
            header, body = self.frame()
            if header['content-type'] == 'text/event-json':
                self.save(header, body)
            else:
                return header, body
        raise AssertionError('ESL事件过多而未收到命令响应')

    def api(self, command):
        header, body = self.command('api ' + command)
        assert header['content-type'] == 'api/response'
        return body.decode()

    def event(self, predicate, timeout=5):
        deadline = time.monotonic() + timeout
        while True:
            found = next((item for item in self.events if predicate(item)), None)
            if found is not None:
                return found
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError('未观察到指定应用事件')
            header, body = self.frame(remaining)
            self.save(header, body)

    def collect(self, duration):
        deadline = time.monotonic() + duration
        while time.monotonic() < deadline:
            try:
                header, body = self.frame(deadline - time.monotonic())
                self.save(header, body)
            except (TimeoutError, socket.timeout):
                break

    def execute(self, channel, application, arguments='', locked=True):
        token = str(uuid.uuid4())
        header, _ = self.command(f'sendmsg {channel}\ncall-command: execute\nexecute-app-name: {application}\nexecute-app-arg: {arguments}\nevent-lock: {str(locked).lower()}\nevent-uuid: {token}')
        assert header['reply-text'] == '+OK', header
        return token

    def completed(self, token, timeout=5):
        return self.event(lambda event: event.get('Application-UUID') == token and event.get('Event-Name') == 'CHANNEL_EXECUTE_COMPLETE', timeout)


class Call:
    """只拥有本脚本建立的一通电话；持续媒体输入避免原版park等待读帧而不能推进应用。"""
    def __init__(self, sip_port, esl_port, password, extension, events=''):
        self.evidence = Evidence()
        self.peer = Peer(esl_port, password, self.evidence)
        self.identity = self.peer.api('version')
        names = 'CHANNEL_CREATE CHANNEL_ANSWER CHANNEL_EXECUTE CHANNEL_EXECUTE_COMPLETE CHANNEL_HANGUP CHANNEL_PARK CHANNEL_UNPARK DTMF PLAYBACK_START PLAYBACK_STOP'
        header, _ = self.peer.command('event json ' + names + (' ' + events if events else ''))
        assert header['reply-text'].startswith('+OK')
        self.signaling, self.rtp, self.rtcp = e2e.udp(), e2e.udp(), e2e.udp()
        self.target = ('127.0.0.1', sip_port)
        self.extension, self.call_id = extension, uuid.uuid4().hex
        self.uri = f'sip:{extension}@127.0.0.1:{sip_port}'
        self.accepted, self.channel = None, None
        self.stop, self.media_lock = threading.Event(), threading.Lock()
        self.thread, self.media_errors, self.received = None, [], []
        self.sequence, self.timestamp, self.ssrc = 100, 4000, 0xD7AF
        self.jobs = {}

    def send_sip(self, packet):
        self.evidence.add('SIP', 'send', packet)
        self.signaling.sendto(packet, self.target)

    def response(self, predicate, timeout=5):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self.signaling.settimeout(max(.001, deadline - time.monotonic()))
            packet, address = self.signaling.recvfrom(65536)
            assert address == self.target
            self.evidence.add('SIP', 'receive', packet)
            if predicate(e2e.parse(packet)):
                return packet
        raise TimeoutError('未收到预期SIP响应')

    def invite(self, expected_status=200):
        self.send_sip(e2e.message('INVITE', self.uri, self.signaling.getsockname()[1], self.call_id,
                                 'z9hG4bK' + uuid.uuid4().hex, e2e.sdp(self.rtp.getsockname()[1], self.rtcp.getsockname()[1])))
        final = self.response(lambda parsed: parsed[0].startswith('SIP/2.0 ') and int(parsed[0].split()[1]) >= 200)
        start, headers, body = e2e.parse(final)
        code = int(start.split()[1])
        if code != 200:
            self.send_sip(e2e.message('ACK', self.uri, self.signaling.getsockname()[1], self.call_id,
                                     headers['via'].split('branch=', 1)[1].split(';', 1)[0], to=headers['to']))
            return {'status': code, 'start_line': start}
        self.accepted = final
        host = next(line.split()[-1] for line in body.decode().splitlines() if line.startswith('c=IN IP4 '))
        assert host == '127.0.0.1', '媒体端点必须为隔离回环地址'
        self.media_target = (host, e2e.media_ports(body)[0])
        self.send_sip(e2e.message('ACK', self.uri, self.signaling.getsockname()[1], self.call_id,
                                 'z9hG4bK' + uuid.uuid4().hex, to=headers['to']))
        self.start_media()
        created = self.peer.event(lambda event: event.get('Event-Name') == 'CHANNEL_CREATE' and event.get('variable_sip_call_id') == self.call_id)
        self.channel = created['Unique-ID']
        self.peer.event(lambda event: event.get('Event-Name') == 'CHANNEL_ANSWER' and event.get('Unique-ID') == self.channel)
        return {'status': code, 'start_line': start}

    def start_media(self):
        self.rtp.setblocking(False)
        def pump():
            deadline, tick = time.monotonic() + 30, time.monotonic()
            try:
                while not self.stop.is_set():
                    now = time.monotonic()
                    assert now < deadline, '真实媒体泵超过30秒上限'
                    if now >= tick:
                        with self.media_lock:
                            self.send_rtp(0, self.timestamp, bytes([0xff]) * 160)
                            self.timestamp = (self.timestamp + 160) & 0xffffffff
                        tick += .02
                        if now - tick > .1:
                            tick = now + .02
                    for _ in range(128):
                        try:
                            packet, address = self.rtp.recvfrom(65536)
                        except BlockingIOError:
                            break
                        assert address == self.media_target
                        self.evidence.add('RTP', 'receive', packet)
                        with self.media_lock:
                            self.received.append({'monotonic_ns': time.monotonic_ns(), 'packet': packet})
                    self.stop.wait(min(.002, max(0, tick - time.monotonic())))
            except Exception as error:
                self.media_errors.append(type(error).__name__ + ': ' + str(error))
        self.thread = threading.Thread(target=pump, name='native-media-fixture', daemon=True)
        self.thread.start()

    def send_rtp(self, payload, timestamp, body, marker=False):
        # 调用方必须持media_lock，确保音频与按键的同SSRC序号严格有序。
        self.sequence = (self.sequence + 1) & 65535
        packet = struct.pack('!BBHII', 0x80, payload | (128 if marker else 0), self.sequence, timestamp, self.ssrc) + body
        self.evidence.add('RTP', 'send', packet)
        self.rtp.sendto(packet, self.media_target)

    def digit(self, value):
        with self.media_lock:
            timestamp = self.timestamp
        for index, (ended, duration) in enumerate([(False, 160), (False, 320), (True, 480), (True, 480), (True, 480)]):
            with self.media_lock:
                self.send_rtp(101, timestamp, bytes(['0123456789*#ABCD'.index(value), 10 | (128 if ended else 0)]) + struct.pack('!H', duration), index == 0)
            time.sleep(.02)

    def execute(self, application, arguments='', locked=True):
        token = self.peer.execute(self.channel, application, arguments, locked)
        self.jobs[token] = {'application': application, 'arguments': arguments}
        return token

    def completed(self, token, timeout=5):
        event = self.peer.completed(token, timeout)
        assert event['Unique-ID'] == self.channel and event['Application'] == self.jobs[token]['application']
        return event

    def server_hangup(self, timeout=5):
        packet = self.response(lambda parsed: parsed[0].startswith('BYE '), timeout)
        self.send_sip(e2e.response(packet, port=self.signaling.getsockname()[1]))
        self.accepted = None
        event = self.peer.event(lambda value: value.get('Unique-ID') == self.channel and value.get('Event-Name') == 'CHANNEL_HANGUP')
        self.peer.collect(.1)
        return event

    def caller_hangup(self):
        if self.accepted is None:
            return
        headers = e2e.parse(self.accepted)[1]
        self.send_sip(e2e.message('BYE', self.uri, self.signaling.getsockname()[1], self.call_id,
                                 'z9hG4bK' + uuid.uuid4().hex, to=headers['to'], cseq=2))
        self.response(lambda value: value[0].startswith('SIP/2.0 200') and value[1]['cseq'].endswith('BYE'))
        self.accepted = None

    def close(self):
        cleanup = {}
        try:
            self.caller_hangup()
            cleanup['sip_dialog_closed'] = True
        except Exception as error:
            cleanup['error'] = type(error).__name__ + ': ' + str(error)
        self.stop.set()
        if self.thread:
            self.thread.join(2)
            cleanup['media_thread_exited'] = not self.thread.is_alive()
        else:
            cleanup['media_thread_exited'] = True
        for endpoint in [self.signaling, self.rtp, self.rtcp, self.peer.socket]:
            endpoint.close()
        cleanup['media_errors'] = self.media_errors
        return cleanup

    def report(self):
        return {'identity_only': self.identity, 'call_id': self.call_id, 'channel': self.channel,
                'jobs': self.jobs, 'events': self.peer.events, 'wire_bytes': self.evidence.size, 'wire': self.evidence.rows}
