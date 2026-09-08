#!/usr/bin/env python3
"""仅使用 Python 标准库的本地 SIP/UDP、媒体互通与故障回归。

每项测试启动自己的控制进程和媒体 worker，只操作这些测试进程；测试不是容量验收。
固定媒体范围使本文件的两个运行模式必须顺序执行，避免测试之间争抢端口。
"""
import argparse
import json
import os
from pathlib import Path
import select
import signal
import socket
import struct
import subprocess
import tempfile
import time
import unittest
import urllib.request
import uuid

# 路径基于项目目录；可由 --control 选择待测控制二进制，媒体仍取项目默认二进制。
ROOT = Path(__file__).resolve().parents[1]
CONTROL = ROOT / "bin/rustswitch"
MEDIA = ROOT / "bin/rustswitch-media"
# 两种 UDP 模式运行相同业务断言，只有测试配置中的 connect_sockets 不同。
CONNECTED = False

# 创建有限接收超时的本机 UDP 端点；调用方负责登记和关闭资源。
def udp():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(("127.0.0.1", 0))
    s.settimeout(2)
    return s

# 解析本测试构造的简单 SIP 报文，不是可处理全部重复头域和扩展的生产 SIP 解析器。
def parse(data):
    head, body = data.split(b"\r\n\r\n", 1)
    lines = head.decode().split("\r\n")
    h = {}
    for line in lines[1:]:
        key, value = line.split(":", 1)
        h[key.lower()] = value.strip()
    return lines[0], h, body

# 构造测试端点请求并以字节数填写 Content-Length；有正文时本套测试只发送 SDP。
def message(method, uri, source, call_id, branch, body=b"", to="<sip:100@local>", from_="<sip:alice@local>;tag=alice", cseq=1):
    headers = [f"{method} {uri} SIP/2.0", f"Via: SIP/2.0/UDP 127.0.0.1:{source};branch={branch};rport", f"From: {from_}", f"To: {to}", f"Call-ID: {call_id}", f"CSeq: {cseq} {method}", "Max-Forwards: 70", f"Contact: <sip:alice@127.0.0.1:{source}>"]
    if body:
        headers.append("Content-Type: application/sdp")
    headers += [f"Content-Length: {len(body)}", "", ""]
    return "\r\n".join(headers).encode() + body

# 根据原请求生成匹配事务的响应；100 以外的响应在缺少 To-tag 时补测试标签。
def response(request, status=200, reason="OK", body=b"", port=5070, tag="bob"):
    _, h, _ = parse(request)
    to = h["to"]
    if ";tag=" not in to and status > 100:
        to += ";tag=" + tag
    headers = [f"SIP/2.0 {status} {reason}", f"Via: {h['via']}", f"From: {h['from']}", f"To: {to}", f"Call-ID: {h['call-id']}", f"CSeq: {h['cseq']}", f"Contact: <sip:bob@127.0.0.1:{port}>"]
    if body:
        headers.append("Content-Type: application/sdp")
    headers += [f"Content-Length: {len(body)}", "", ""]
    return "\r\n".join(headers).encode() + body

# 提供独立 RTP/RTCP 端口、20 ms 音频和 telephone-event 的最小 SDP 测试向量。
def sdp(rtp, rtcp, payload=0):
    return (f"v=0\r\no=test 1 1 IN IP4 127.0.0.1\r\ns=test\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio {rtp} RTP/AVP {payload} 101\r\na=rtcp:{rtcp} IN IP4 127.0.0.1\r\na=rtpmap:101 telephone-event/8000\r\na=ptime:20\r\n").encode()

# 从本原型输出的单音频 SDP 中提取两个监听端口，不推断其他媒体类型或 rtcp-mux。
def media_ports(body):
    rtp = rtcp = None
    for line in body.decode().splitlines():
        if line.startswith("m=audio "):
            rtp = int(line.split()[1])
        if line.startswith("a=rtcp:"):
            rtcp = int(line.split()[0].split(":")[1])
    return rtp, rtcp

# 在总等待窗口内筛选预期 SIP 消息，允许跳过事务重传或其他临时响应。
def receive(sock, predicate, timeout=3):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        sock.settimeout(max(0.01, end - time.monotonic()))
        data, peer = sock.recvfrom(65536)
        if predicate(parse(data)):
            return data, peer
    raise AssertionError("expected SIP message did not arrive")

# 端到端夹具拥有服务进程、临时配置及所有测试 socket，避免用例依赖前一项状态。
class Integration(unittest.TestCase):
    # 创建双腿测试端点和隔离配置；等待 readiness 后才允许测试发送业务请求。
    def setUp(self):
        self.resources = []
        self.tmp = tempfile.TemporaryDirectory(prefix="rustswitch-e2e-")
        self.upstream = self.sock()
        self.a = self.sock()
        self.ar, self.ac, self.br, self.bc = [self.sock() for _ in range(4)]
        self.port = self.free_tcp_port()
        sip_probe = self.sock()
        sip_port = sip_probe.getsockname()[1]
        sip_probe.close()
        self.address = ("127.0.0.1", sip_port)
        cfg = json.loads((ROOT / "config/local.json").read_text())
        cfg["sip"].update(listen=f"127.0.0.1:{sip_port}", advertise=f"127.0.0.1:{sip_port}", upstream=f"127.0.0.1:{self.upstream.getsockname()[1]}", ack_timeout_ms=1000)
        # 独立低位媒体范围避开本地管理服务的10000..64999；顺序用例沿用同一隔离窗口。
        cfg["media"].update(connect_sockets=CONNECTED, binary=str(MEDIA), port_start=7200, port_end=8199)
        cfg["limits"].update(max_calls=2, calls_per_second=10, burst_calls=10)
        if self._testMethodName == "test_reused_port_rejects_previous_peer":
            # 仅端口复用用例缩小范围并取消隔离期，使新旧会话确定性复用同一端口。
            cfg["media"].update(port_end=7207,port_reuse_delay_ms=0)
        cfg["admin"]["listen"] = f"127.0.0.1:{self.port}"
        cfg["journal"]["path"] = str(Path(self.tmp.name) / "events.jsonl")
        conf = Path(self.tmp.name) / "config.json"
        conf.write_text(json.dumps(cfg))
        self.log = open(Path(self.tmp.name) / "server.log", "w+")
        self.process = subprocess.Popen([str(CONTROL), "-config", str(conf)], cwd=ROOT, stdout=self.log, stderr=self.log)
        end = time.monotonic() + 8
        while time.monotonic() < end:
            try:
                if self.get("/readyz")["ready"]:
                    return
            except Exception:
                pass
            if self.process.poll() is not None:
                self.log.seek(0)
                self.fail(self.log.read())
            time.sleep(0.03)
        self.fail("startup timed out")

    # 先请求正常退出，超时后再次终止本测试服务；最后关闭端点和删除临时目录。
    def tearDown(self):
        if hasattr(self, "process") and self.process.poll() is None:
            self.process.send_signal(signal.SIGTERM)
            try:
                self.process.wait(1)
            except subprocess.TimeoutExpired:
                self.process.send_signal(signal.SIGTERM)
                self.process.wait(4)
        for s in self.resources:
            s.close()
        if hasattr(self, "log"):
            self.log.close()
        self.tmp.cleanup()

    # 统一登记 UDP 资源，成功或失败用例结束后都能关闭。
    def sock(self):
        s = udp()
        self.resources.append(s)
        return s

    # 探测本机管理端口候选；探测 socket 关闭后不再保留该端口。
    def free_tcp_port(self):
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            return s.getsockname()[1]

    # 只读取本测试服务的管理 JSON 接口；网络或解析错误交由调用测试处理。
    def get(self, path):
        with urllib.request.urlopen(f"http://127.0.0.1:{self.port}{path}", timeout=1) as r:
            return json.load(r)

    # 与管理页面使用同一写接口，在测试中验证即时策略确实到达真实 SIP/媒体服务。
    def put_guard(self, policy):
        current = self.get("/v1/config")
        request = urllib.request.Request(
            f"http://127.0.0.1:{self.port}/v1/guard", method="PUT",
            data=json.dumps({"revision": current["revision"], "policy": policy}).encode(),
            headers={"Content-Type": "application/json", "X-RustSwitch-CSRF": current["csrf_token"]},
        )
        with urllib.request.urlopen(request, timeout=2) as response:
            return json.load(response)

    # 通话接通后在线降低并发限制：新呼叫收到重试提示，原媒体仍双向可用且能正常挂断。
    def test_hot_peak_guard_preserves_established_call(self):
        _, outgoing, accepted = self.establish()
        policy = self.get("/v1/guard")["guard"]["policy"]
        policy.update(max_active_calls=1, retry_after_seconds=3)
        self.assertEqual(self.put_guard(policy)["guard"]["policy"]["max_active_calls"], 1)
        new = message("INVITE", "sip:100@local", self.a.getsockname()[1], uuid.uuid4().hex,
                      "z9hG4bK" + uuid.uuid4().hex, sdp(self.ar.getsockname()[1], self.ac.getsockname()[1]))
        self.a.sendto(new, self.address)
        rejected, _ = receive(self.a, lambda m: m[0].startswith("SIP/2.0 503"))
        self.assertEqual(parse(rejected)[1]["retry-after"], "3")
        a_port, b_port = media_ports(parse(accepted)[2])[0], media_ports(parse(outgoing)[2])[0]
        packet = struct.pack("!BBHII", 0x80, 0, 7, 1120, 123) + bytes(160)
        for sender, receiver, port in [(self.ar, self.br, a_port), (self.br, self.ar, b_port)]:
            sender.sendto(packet, ("127.0.0.1", port))
            self.assertEqual(receiver.recvfrom(2048)[0], packet)
        self.hangup(accepted)
        self.assertGreaterEqual(self.get("/v1/guard")["guard"]["rejected_active"], 1)

    # 等待控制面活动呼叫数收敛；超时附带最后状态，避免用固定睡眠掩盖资源泄漏。
    def wait_active(self, count, timeout=4):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            if self.get("/v1/status")["active_calls"] == count:
                return
            time.sleep(0.02)
        self.fail(f"active_calls did not reach {count}: {self.get('/v1/status')}")

    # 发起 A 腿 INVITE 并取得 B 腿请求，核对 B2BUA 确实生成独立的 Call-ID。
    def invite(self, sock=None):
        sock = sock or self.a
        call_id = uuid.uuid4().hex
        branch = "z9hG4bK" + uuid.uuid4().hex
        wire = message("INVITE", "sip:100@local", sock.getsockname()[1], call_id, branch, sdp(self.ar.getsockname()[1], self.ac.getsockname()[1]))
        sock.sendto(wire, self.address)
        receive(sock, lambda m: m[0].startswith("SIP/2.0 100"))
        outgoing, peer = receive(self.upstream, lambda m: m[0].startswith("INVITE "))
        self.assertNotEqual(parse(outgoing)[1]["call-id"], call_id)
        return wire, outgoing, peer

    # 完成双腿接通；ack=False 专供模拟主叫丢失最终 ACK 的故障路径。
    def establish(self, ack=True):
        original, outgoing, peer = self.invite()
        answer = response(outgoing, body=sdp(self.br.getsockname()[1], self.bc.getsockname()[1]), port=self.upstream.getsockname()[1])
        self.upstream.sendto(answer, peer)
        receive(self.upstream, lambda m: m[0].startswith("ACK "))
        accepted, _ = receive(self.a, lambda m: m[0].startswith("SIP/2.0 200"))
        if ack:
            h = parse(accepted)[1]
            self.a.sendto(message("ACK", "sip:rustswitch@local", self.a.getsockname()[1], h["call-id"], "z9hG4bK"+uuid.uuid4().hex, to=h["to"]), self.address)
        return original, outgoing, accepted

    # 从 A 腿正常挂断，并回答 B 腿 BYE；直到活动呼叫归零才算清理完成。
    def hangup(self, accepted):
        h = parse(accepted)[1]
        bye = message("BYE", "sip:rustswitch@local", self.a.getsockname()[1], h["call-id"], "z9hG4bK"+uuid.uuid4().hex, to=h["to"], cseq=2)
        self.a.sendto(bye, self.address)
        receive(self.a, lambda m: m[0].startswith("SIP/2.0 200") and m[1]["cseq"].endswith("BYE"))
        outgoing, peer = receive(self.upstream, lambda m: m[0].startswith("BYE "))
        self.upstream.sendto(response(outgoing, port=self.upstream.getsockname()[1]), peer)
        self.wait_active(0)

    # 同时校验双向 RTP、DTMF 与 RTCP 的字节内容和服务器源端口，再正常拆呼。
    def test_bidirectional_rtp_rtcp_dtmf_and_cleanup(self):
        _, outgoing, accepted = self.establish()
        aports = media_ports(parse(accepted)[2])
        bports = media_ports(parse(outgoing)[2])
        rtp = struct.pack("!BBHII", 0x80, 0, 1, 160, 0x1234) + bytes(range(160))
        dtmf = struct.pack("!BBHII", 0x80, 101, 2, 160, 0x1234) + bytes([1, 0x80, 0, 160])
        rtcp = bytes([0x80, 201, 0, 1, 0, 0, 0, 1])
        for sender, receiver, target, source, packet in [
            (self.ar, self.br, aports[0], bports[0], rtp),
            (self.br, self.ar, bports[0], aports[0], rtp),
            (self.ar, self.br, aports[0], bports[0], dtmf),
            (self.ac, self.bc, aports[1], bports[1], rtcp),
            (self.bc, self.ac, bports[1], aports[1], rtcp),
        ]:
            sender.sendto(packet, ("127.0.0.1", target))
            receiver.settimeout(2)
            data, peer = receiver.recvfrom(2048)
            self.assertEqual(data, packet)
            self.assertEqual(peer[1], source)
        self.hangup(accepted)

    # 连发 80 包跨越多个接收批次与单轮预算，验证继续调度到排空且序列未丢失。
    def test_receive_burst_across_multiple_batches(self):
        _, _, accepted = self.establish()
        port = media_ports(parse(accepted)[2])[0]
        for sequence in range(1,81):
            packet=struct.pack("!BBHII",0x80,0,sequence,sequence*160,77)+bytes(160)
            self.ar.sendto(packet,("127.0.0.1",port))
        received=[]
        self.br.settimeout(2)
        for _ in range(80): received.append(struct.unpack("!H",self.br.recvfrom(2048)[0][2:4])[0])
        self.assertEqual(received,list(range(1,81)))
        self.hangup(accepted)

    # 复用相同服务器端口后更换双方端点，旧源必须被拒绝，新源必须能够正常转发。
    def test_reused_port_rejects_previous_peer(self):
        _, _, first = self.establish()
        first_port = media_ports(parse(first)[2])[0]
        old_sender = self.ar
        self.hangup(first)
        self.ar,self.ac,self.br,self.bc=[self.sock() for _ in range(4)]
        _, _, second = self.establish()
        second_port=media_ports(parse(second)[2])[0]
        self.assertEqual(first_port,second_port)
        packet=struct.pack("!BBHII",0x80,0,1,160,77)+bytes(160)
        old_sender.sendto(packet,("127.0.0.1",second_port))
        self.br.settimeout(.15)
        with self.assertRaises(socket.timeout): self.br.recvfrom(2048)
        self.ar.sendto(packet,("127.0.0.1",second_port))
        self.br.settimeout(2)
        self.assertEqual(self.br.recvfrom(2048)[0],packet)
        self.hangup(second)

    # 同事务 INVITE 重传不能额外占用一套媒体资源或增加活动呼叫数。
    def test_duplicate_invite_does_not_allocate_again(self):
        original, _, _ = self.invite()
        self.a.sendto(original, self.address)
        receive(self.a, lambda m: m[0].startswith("SIP/2.0 100"))
        self.assertEqual(self.get("/v1/status")["active_calls"], 1)

    # 人为占住首个端口，验证分配器跳过整块并选择下一完整四端口块。
    def test_busy_port_block_is_skipped(self):
        busy=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
        busy.bind(("127.0.0.1",7200));self.resources.append(busy)
        _,_,accepted=self.establish()
        self.assertEqual(media_ports(parse(accepted)[2])[0],7204)
        self.hangup(accepted)

    # 陌生源和已知源的非法 RTP 都不得到达对端；不依赖平台是否能计量内核过滤。
    def test_wrong_source_and_invalid_rtp_are_dropped(self):
        _, _, accepted = self.establish()
        port = media_ports(parse(accepted)[2])[0]
        rogue = self.sock()
        rogue.sendto(struct.pack("!BBHII", 0x80, 0, 1, 1, 1)+b"test", ("127.0.0.1", port))
        self.ar.sendto(b"invalid", ("127.0.0.1", port))
        self.br.settimeout(0.15)
        with self.assertRaises(socket.timeout):
            self.br.recvfrom(2048)
        self.hangup(accepted)

    # 构造 CANCEL 已完成但 B 腿迟到 200 的竞争，仍须 ACK 后 BYE 清理已成立的 B 腿。
    def test_cancel_then_late_200_is_acked_and_torn_down(self):
        original, outgoing, peer = self.invite()
        self.upstream.sendto(response(outgoing, 180, "Ringing", port=self.upstream.getsockname()[1]), peer)
        receive(self.a, lambda m: m[0].startswith("SIP/2.0 180"))
        _, h, _ = parse(original)
        self.a.sendto(message("CANCEL", "sip:100@local", self.a.getsockname()[1], h["call-id"], h["via"].split("branch=")[1].split(";")[0]), self.address)
        receive(self.a, lambda m: m[0].startswith("SIP/2.0 200") and m[1]["cseq"].endswith("CANCEL"))
        receive(self.a, lambda m: m[0].startswith("SIP/2.0 487"))
        cancel, _ = receive(self.upstream, lambda m: m[0].startswith("CANCEL "))
        self.upstream.sendto(response(cancel), peer)
        # 接通响应故意放在取消之后，确认服务不会把迟到成功响应直接丢弃而遗留呼叫。
        self.upstream.sendto(response(outgoing, body=sdp(self.br.getsockname()[1], self.bc.getsockname()[1]), port=self.upstream.getsockname()[1]), peer)
        receive(self.upstream, lambda m: m[0].startswith("ACK "))
        bye, _ = receive(self.upstream, lambda m: m[0].startswith("BYE "))
        self.upstream.sendto(response(bye), peer)
        self.wait_active(0)

    # 主叫不发最终 ACK 时，超时后应关闭已接通的 B 腿并释放媒体。
    def test_missing_ack_reclaims_media(self):
        self.establish(ack=False)
        bye, peer = receive(self.upstream, lambda m: m[0].startswith("BYE "), timeout=3)
        self.upstream.sendto(response(bye), peer)
        self.wait_active(0)

    # 183 阶段即能传输媒体；最终 200 延续同一 A 腿 To-tag 并可正常拆呼。
    def test_early_media_before_final_answer(self):
        _, outgoing, peer = self.invite()
        self.upstream.sendto(response(outgoing,183,"Session Progress",sdp(self.br.getsockname()[1],self.bc.getsockname()[1]),self.upstream.getsockname()[1]),peer)
        early,_ = receive(self.a,lambda m:m[0].startswith("SIP/2.0 183"))
        packet=struct.pack("!BBHII",0x80,0,1,160,99)+bytes(160)
        self.br.sendto(packet,("127.0.0.1",media_ports(parse(outgoing)[2])[0]))
        self.ar.settimeout(2)
        self.assertEqual(self.ar.recvfrom(2048)[0],packet)
        self.upstream.sendto(response(outgoing,body=sdp(self.br.getsockname()[1],self.bc.getsockname()[1]),port=self.upstream.getsockname()[1]),peer)
        receive(self.upstream,lambda m:m[0].startswith("ACK "))
        accepted,_=receive(self.a,lambda m:m[0].startswith("SIP/2.0 200"))
        h=parse(accepted)[1]
        self.assertEqual(h['to'],parse(early)[1]['to'])
        self.a.sendto(message("ACK","sip:rustswitch@local",self.a.getsockname()[1],h['call-id'],"z9hG4bK"+uuid.uuid4().hex,to=h['to']),self.address)
        self.hangup(accepted)

    # 连续多个183切换早期媒体目标后立即200接通，最终媒体不得被较旧的异步连接覆盖。
    def test_early_update_burst_keeps_final_media_target(self):
        _, outgoing, peer = self.invite()
        for _ in range(24):
            early_rtp, early_rtcp = self.sock(), self.sock()
            self.upstream.sendto(response(outgoing, 183, "Session Progress",
                sdp(early_rtp.getsockname()[1], early_rtcp.getsockname()[1]), self.upstream.getsockname()[1]), peer)
        self.upstream.sendto(response(outgoing, body=sdp(self.br.getsockname()[1], self.bc.getsockname()[1]),
                                      port=self.upstream.getsockname()[1]), peer)
        receive(self.upstream, lambda m: m[0].startswith("ACK "))
        accepted, _ = receive(self.a, lambda m: m[0].startswith("SIP/2.0 200"))
        h = parse(accepted)[1]
        self.a.sendto(message("ACK", "sip:rustswitch@local", self.a.getsockname()[1], h["call-id"],
                              "z9hG4bK"+uuid.uuid4().hex, to=h["to"]), self.address)
        target = media_ports(parse(accepted)[2])[0]
        # 多个真实媒体往返覆盖最终应答之后的处理窗口，核对实际字节和最终端点而非仅检查状态数。
        for sequence in range(10):
            packet = struct.pack("!BBHII", 0x80, 0, sequence, sequence*160, 887) + bytes(160)
            self.ar.sendto(packet, ("127.0.0.1", target))
            self.assertEqual(self.br.recvfrom(2048)[0], packet)
            time.sleep(0.02)
        self.hangup(accepted)

    # 同一个控制进程反复建立和拆线，核对资源与线程不会按累计通话数持续增长。
    def test_repeated_calls_reclaim_media_and_control_work(self):
        # 预热后台目录的大响应及缓存，促使首轮 GC 在基线前完成；短通话本身不保证触发 GC。
        for _ in range(3):
            self.get('/v1/fs-config')
        # 先完成同样的暖机负载：Go 首轮 GC 会初始化每个 P 的后台工作协程。
        # 冷启动直接比较会把这批固定工作者误报为通话泄漏，不能仅放大断言阈值。
        for _ in range(16):
            _, _, accepted = self.establish()
            self.hangup(accepted)
            time.sleep(0.12)
        time.sleep(1.1)  # 超过资源缓存周期，基线必须来自暖机后的新样本。
        baseline = self.get('/v1/status')['controller']
        for _ in range(16):
            _, _, accepted = self.establish()
            self.hangup(accepted)
            # 开发夹具每秒10次准入，保留真实建呼节奏，不通过修改业务策略绕过保护。
            time.sleep(0.12)
        deadline = time.monotonic() + 4
        while time.monotonic() < deadline:
            state = self.get('/v1/status')
            controller = state['controller']
            if all(w['stats'].get('active_calls') == 0 for w in state['workers']) and controller['media_results_queue_length'] == 0:
                break
            time.sleep(0.05)
        # 比较同样静置条件下的新样本，避免把处理中采样和空闲基线混用。
        time.sleep(1.1)
        state = self.get('/v1/status')
        controller = state['controller']
        self.assertEqual(state['active_calls'], 0)
        self.assertEqual(state['established_calls'], 0)
        self.assertTrue(all(w['stats'].get('active_calls') == 0 for w in state['workers']))
        self.assertEqual(controller['media_results_queue_length'], 0)
        # 管理HTTP连接和采样时机有少量波动；预算关注每通遗留协程，避免断言精确常量。
        self.assertLessEqual(controller['goroutines'], baseline['goroutines'] + 8)
        if baseline['runtime_threads'] is not None and controller['runtime_threads'] is not None:
            self.assertLessEqual(controller['runtime_threads'], baseline['runtime_threads'] + 8)

    # 只终止本测试服务的一个媒体 worker，另一分片上的呼叫必须继续转发。
    def test_other_shard_survives_media_process_failure(self):
        self.establish()
        _, _, second = self.establish()
        state=self.get('/v1/status')
        os.kill(state['workers'][0]['pid'],signal.SIGKILL)
        self.wait_active(1)
        packet=struct.pack("!BBHII",0x80,0,1,160,321)+bytes(160)
        self.ar.sendto(packet,("127.0.0.1",media_ports(parse(second)[2])[0]))
        self.br.settimeout(2)
        self.assertEqual(self.br.recvfrom(2048)[0],packet)

    # 两路额度占满后第三路返回 503，不能把已有呼叫挤出或产生额外预留。
    def test_concurrency_limit_rejects_third_call(self):
        self.establish()
        self.establish()
        wire=message("INVITE","sip:100@local",self.a.getsockname()[1],uuid.uuid4().hex,"z9hG4bK"+uuid.uuid4().hex,sdp(self.ar.getsockname()[1],self.ac.getsockname()[1]))
        self.a.sendto(wire,self.address)
        receive(self.a,lambda m:m[0].startswith("SIP/2.0 503"))
        self.assertEqual(self.get('/v1/status')['active_calls'],2)

    # 正常停机期间延迟 B 腿 BYE 的响应，确认事务重传和最终关闭完成前进程仍在。
    def test_shutdown_waits_for_bye_transaction(self):
        _,_,accepted=self.establish()
        self.process.send_signal(signal.SIGTERM)
        h=parse(accepted)[1]
        wire=message("BYE","sip:rustswitch@local",self.a.getsockname()[1],h['call-id'],"z9hG4bK"+uuid.uuid4().hex,to=h['to'],cseq=2)
        self.a.sendto(wire,self.address)
        receive(self.a,lambda m:m[0].startswith("SIP/2.0 200") and m[1]['cseq'].endswith('BYE'))
        first,_=receive(self.upstream,lambda m:m[0].startswith('BYE '))
        # 不回答首次 BYE，等待完全相同的重传消息后才完成事务。
        repeated,peer=receive(self.upstream,lambda m:m[0].startswith('BYE '),timeout=2)
        self.assertEqual(first,repeated)
        self.assertIsNone(self.process.poll())
        self.upstream.sendto(response(repeated),peer)
        self.assertEqual(self.process.wait(3),0)

    # 等统计确认所属 worker 后注入进程故障，验证呼叫清理和新代际 worker 恢复。
    def test_media_worker_failure_and_restart(self):
        _, _, accepted = self.establish()
        deadline = time.monotonic()+3
        worker = None
        while time.monotonic() < deadline:
            workers = self.get("/v1/status")["workers"]
            worker = next((w for w in workers if w["stats"].get("active_calls") == 1), None)
            if worker:
                break
            time.sleep(0.03)
        self.assertIsNotNone(worker)
        os.kill(worker["pid"], signal.SIGKILL)
        self.wait_active(0)
        deadline = time.monotonic()+6
        while time.monotonic() < deadline:
            now = self.get("/v1/status")["workers"][worker["id"]]
            if now["healthy"] and now["generation"] > worker["generation"]:
                return
            time.sleep(0.05)
        self.fail("media worker did not restart")

    # 管理排空只拒绝新呼叫，已成立媒体和正常 BYE 清理必须继续工作。
    def test_drain_rejects_new_calls_and_preserves_existing_media(self):
        _, outgoing, accepted = self.establish()
        # 与真实管理页面一致，先取得当前进程令牌，再带 JSON 类型和 CSRF 头执行写操作。
        csrf_token = self.get("/v1/config")["csrf_token"]
        req = urllib.request.Request(
            f"http://127.0.0.1:{self.port}/v1/drain", data=b"{}", method="POST",
            headers={"Content-Type": "application/json", "X-RustSwitch-CSRF": csrf_token},
        )
        with urllib.request.urlopen(req, timeout=1) as r:
            self.assertTrue(json.load(r)["draining"])
        new = message("INVITE", "sip:100@local", self.a.getsockname()[1], uuid.uuid4().hex, "z9hG4bK"+uuid.uuid4().hex, sdp(self.ar.getsockname()[1], self.ac.getsockname()[1]))
        self.a.sendto(new, self.address)
        receive(self.a, lambda m: m[0].startswith("SIP/2.0 503"))
        packet = struct.pack("!BBHII",0x80,0,1,160,123)+bytes(160)
        self.ar.sendto(packet,("127.0.0.1",media_ports(parse(accepted)[2])[0]))
        self.br.settimeout(2)
        self.assertEqual(self.br.recvfrom(2048)[0],packet)
        self.hangup(accepted)

# 脚本参数只控制待测二进制和 UDP 模式，其余参数原样交给 unittest 选择用例。
if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--connected", action="store_true")
    parser.add_argument("--control", type=Path, default=CONTROL)
    args, remaining = parser.parse_known_args()
    CONTROL = args.control.resolve()
    CONNECTED = args.connected
    unittest.main(argv=[__file__]+remaining, verbosity=2)
