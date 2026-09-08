#!/usr/bin/env python3
"""兼容执行器自身的负例与边界测试；这些测试通过不属于 RustSwitch 产品兼容证据。"""
import copy
import importlib.util
import json
import os
import socket
import threading
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("conformance_self_test", ROOT / "tests/conformance/suite.py")
suite = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(suite)
LIMITS = {"connect_seconds": 1, "probe_seconds": 1, "max_header_bytes": 256, "max_body_bytes": 1024, "max_evidence_bytes": 65536}


class FakeStream:
    """仅用于验证接收器能处理 TCP 任意切分；不模拟真实 FS 兼容性。"""
    def __init__(self, chunks):
        self.chunks = iter(chunks)

    def settimeout(self, timeout):
        if timeout <= 0:
            raise TimeoutError()

    def recv(self, _maximum):
        return next(self.chunks, b"")


def frame_stream(data, limits=None, chunks=None):
    return suite.FrameReader(FakeStream(chunks or [data]), time.monotonic() + 1, limits or LIMITS, suite.Wire(65536))


class FrameTests(unittest.TestCase):
    def test_utf8_byte_count_and_pipeline(self):
        body = "汉\n\n字".encode()
        first = b"Content-Type: api/response\nContent-Length: " + str(len(body)).encode() + b"\n\n" + body
        second = b"Content-Type: command/reply\nReply-Text: +OK\n\n"
        reader = frame_stream(first + second, chunks=[bytes([byte]) for byte in first + second])
        self.assertEqual(reader.frame()["body"], body)
        self.assertEqual(reader.frame()["headers"]["reply-text"], "+OK")
        self.assertFalse(reader.buffer)

    def test_crlf_header_and_zero_length(self):
        self.assertEqual(frame_stream(b"Content-Length: 0\r\n\r\n").frame()["body"], b"")

    def test_exact_body_boundary(self):
        reader = frame_stream(b"Content-Length: 1024\n\n" + b"x" * 1024)
        self.assertEqual(len(reader.frame()["body"]), 1024)

    def test_no_length_means_zero(self):
        self.assertEqual(frame_stream(b"Content-Type: auth/request\n\n").frame()["body"], b"")

    def test_duplicate_length_rejected(self):
        for raw in [b"Content-Length: 0\nContent-Length: 0\n\n", b"Content-Length: 0\ncontent-length: 1\n\nx"]:
            with self.subTest(raw=raw), self.assertRaises(suite.ProbeError):
                frame_stream(raw).frame()

    def test_invalid_and_overflow_lengths(self):
        for value in [b"-1", b"+1", b"1x", b"9999999999999999999999999999", b"1025"]:
            with self.subTest(value=value), self.assertRaises(suite.ProbeError):
                frame_stream(b"Content-Length: " + value + b"\n\n").frame()

    def test_unterminated_header_bounded(self):
        with self.assertRaises(suite.ProbeError):
            frame_stream(b"x" * 257).frame()

    def test_incomplete_body_and_eof(self):
        with self.assertRaises(EOFError):
            frame_stream(b"Content-Length: 4\n\nabc").frame()

    def test_timeout_is_not_eof_or_success(self):
        reader = frame_stream(b"Content-Length: 0\n\n")
        reader.deadline = time.monotonic() - 1
        with self.assertRaises(TimeoutError):
            reader.frame()

    def test_wire_limit_never_silently_truncates(self):
        wire = suite.Wire(2)
        with self.assertRaises(suite.ProbeError):
            wire.add("receive", b"abc")
        self.assertFalse(wire.records)

    def test_secrets_not_recorded_as_base64(self):
        wire = suite.Wire(100)
        wire.add("send", b"auth secret\n\n", secret=True)
        self.assertNotIn("base64", wire.records[0])
        self.assertNotIn("secret", json.dumps(wire.records))
        self.assertIn("sha256", wire.records[0])

    def test_event_inner_length_checked(self):
        event = b"Event-Name: BACKGROUND_JOB\nContent-Length: 3\n\nabc"
        self.assertEqual(suite.parse_plain_event(event)["body"], b"abc")
        with self.assertRaises(suite.ProbeError):
            suite.parse_plain_event(event + b"d")


class PlanAndPolicyTests(unittest.TestCase):
    def setUp(self):
        self.profile = json.loads((ROOT / "tests/conformance/profile.example.json").read_text())

    def test_all_original_ids_definitions_domains_present(self):
        plan = suite.build_plan(ROOT)
        comparison = json.loads((ROOT / "docs/api/comparison.json").read_text())
        self.assertEqual({row["id"] for row in plan["entries"]}, {row["id"] for row in comparison["entries"]})
        self.assertEqual(plan["counts"]["acceptance_definitions"], 177)
        self.assertEqual(plan["counts"]["business_domains"], 35)
        self.assertTrue(all(not row["complete_contract_passed"] for row in plan["entries"]))
        boundaries = {row["id"] for row in plan["protocol_boundaries"]}
        self.assertTrue({"sip_udp", "sip_tcp", "sip_tls", "sip_ws", "sip_wss", "ip4_ip6", "srtp", "dtls_srtp", "ice_stun_turn", "fax_t38", "scripts_abi"} <= boundaries)

    def test_wrong_version_profile_rejected(self):
        self.profile["reference"]["version"] = "1.10.12"
        with self.assertRaises(suite.ProbeError):
            suite.validate_profile(self.profile)

    def test_wrong_commit_profile_rejected(self):
        self.profile["reference"]["commit"] = "main"
        with self.assertRaises(suite.ProbeError):
            suite.validate_profile(self.profile)

    def test_remote_requires_explicit_isolation(self):
        self.profile["endpoints"]["reference"]["esl"]["host"] = "192.0.2.1"
        with self.assertRaises(suite.ProbeError):
            suite.validate_profile(self.profile)
        self.profile["fixture"]["allow_remote"] = True
        suite.validate_profile(self.profile)
        self.profile["fixture"]["isolated"] = False
        with self.assertRaises(suite.ProbeError):
            suite.validate_profile(self.profile)

    def test_no_plain_password_or_tls_bypass(self):
        for key in ["password", "insecure"]:
            profile = copy.deepcopy(self.profile)
            profile["endpoints"]["reference"]["esl"][key] = "bad"
            with self.subTest(key=key), self.assertRaises(suite.ProbeError):
                suite.validate_profile(profile)

    def test_no_unbounded_limits(self):
        for key, value in [("probe_seconds", 61), ("max_body_bytes", 2**64), ("max_header_bytes", 1.5), ("connect_seconds", True)]:
            profile = copy.deepcopy(self.profile)
            profile["limits"][key] = value
            with self.subTest(key=key), self.assertRaises(suite.ProbeError):
                suite.validate_profile(profile)

    def test_unknown_or_empty_probe_rejected(self):
        for selection in [[], ["made-up-probe"]]:
            with self.subTest(selection=selection), self.assertRaises(suite.ProbeError):
                suite.run_suite(ROOT, self.profile, selection)

    def test_reference_version_match_is_bounded(self):
        self.assertTrue(suite.reference_version_matches("FreeSWITCH Version 1.11.3-release git ef32e20"))
        for text in ["FreeSWITCH Version 1.11.30", "FreeSWITCH Version 11.11.3", "RustSwitch 1.11.3", "FreeSWITCH Version 1.10.12"]:
            self.assertFalse(suite.reference_version_matches(text))

    def test_missing_transport_is_blocked(self):
        probe = next(p for p in suite.PROBES if p["id"] == "sip.options.tls")
        value = suite.execute_probe(probe, {}, self.profile, LIMITS)
        self.assertEqual(value["state"], "blocked")
        self.assertEqual(value["wire_bytes"], 0)

    def test_background_probe_requires_isolation(self):
        self.profile["fixture"]["isolated"] = False
        probe = next(p for p in suite.PROBES if p["id"] == "esl.bgapi.echo")
        value = suite.execute_probe(probe, self.profile["endpoints"]["reference"], self.profile, LIMITS)
        self.assertEqual(value["state"], "blocked")

    def test_missing_secret_cannot_pass(self):
        probe = next(p for p in suite.PROBES if p["id"] == "esl.echo")
        with mock.patch.dict(os.environ, {}, clear=True):
            value = suite.execute_probe(probe, self.profile["endpoints"]["reference"], self.profile, LIMITS)
        self.assertEqual(value["state"], "blocked")

    def test_refused_connection_is_failure(self):
        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
        listener.close()
        endpoint = {"esl": {"host": "127.0.0.1", "port": port, "password_env": "TEST_CONFORMANCE_PASSWORD"}}
        probe = next(p for p in suite.PROBES if p["id"] == "esl.echo")
        with mock.patch.dict(os.environ, {"TEST_CONFORMANCE_PASSWORD": "fixture-only"}):
            value = suite.execute_probe(probe, endpoint, self.profile, LIMITS)
        self.assertEqual(value["state"], "failed")
        self.assertIn(value["error_type"], ["ConnectionRefusedError", "TimeoutError"])

    def test_greeting_timeout_is_failure(self):
        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        listener.listen()
        port = listener.getsockname()[1]
        release = threading.Event()
        def hold():
            connection, _ = listener.accept()
            try:
                release.wait(1)
            finally:
                connection.close()
        worker = threading.Thread(target=hold)
        worker.start()
        try:
            limits = dict(LIMITS, probe_seconds=0.08, connect_seconds=0.05)
            endpoint = {"esl": {"host": "127.0.0.1", "port": port, "password_env": "TEST_CONFORMANCE_PASSWORD"}}
            probe = next(p for p in suite.PROBES if p["id"] == "esl.echo")
            with mock.patch.dict(os.environ, {"TEST_CONFORMANCE_PASSWORD": "fixture-only"}):
                value = suite.execute_probe(probe, endpoint, self.profile, limits)
            self.assertEqual(value["state"], "failed")
            self.assertIn(value["error_type"], ["TimeoutError", "timeout"])
        finally:
            release.set()
            worker.join(2)
            listener.close()


class ProtocolAdapterTests(unittest.TestCase):
    """使用隔离回环协议夹具验证收发和断言；夹具不是原版服务，不发布兼容通过。"""
    def console_probe_fixture(self, bad_response=None):
        # 内存流只验证探针不会接受拒绝响应或忽略控制台语义，不作为产品兼容证据。
        bodies = [suite.ECHO_TEXT.encode(), b"-ERR no reply\n", b"a;b", b"a;;echo b"]
        responses = [b"Content-Type: api/response\nContent-Length: " + str(len(body)).encode() + b"\n\n" + body for body in bodies]
        if bad_response is not None:
            responses[2] = bad_response
        stream = FakeStream([b"Content-Type: auth/request\n\n", b"Content-Type: command/reply\nReply-Text: +OK accepted\n\n"] + responses)
        stream.sendall = mock.Mock()
        stream.close = mock.Mock()
        target = {"host": "127.0.0.1", "port": 1, "password_env": "TEST_CONFORMANCE_PASSWORD"}
        with mock.patch.object(suite.socket, "create_connection", return_value=stream), mock.patch.dict(os.environ, {"TEST_CONFORMANCE_PASSWORD": "fixture-only"}):
            result = suite.run_esl({"id": "esl.console.single"}, target, LIMITS, suite.Wire(65536))
        return result, stream

    def test_console_probe_sends_real_wrapper_and_checks_all_responses(self):
        result, stream = self.console_probe_fixture()
        self.assertEqual(len(result["responses"]), 4)
        requests = [call.args[0] for call in stream.sendall.call_args_list]
        self.assertEqual(requests[1], ("api echo " + suite.ECHO_TEXT + "\nconsole_execute: true\n\n").encode())
        self.assertEqual(requests[3], b"api    echo   a;b  \nconsole_execute: true\n\n")
        self.assertEqual(requests[4], b"api echo a;;echo b\nconsole_execute: false\n\n")
        stream.close.assert_called_once()

    def test_console_probe_rejection_or_ignored_header_cannot_pass(self):
        for response in [b"Content-Type: command/reply\nReply-Text: -ERR console_execute not supported\n\n", b"Content-Type: api/response\nContent-Length: 4\n\n a;b"]:
            with self.subTest(response=response), self.assertRaises(suite.ProbeError):
                self.console_probe_fixture(response)

    def sip_fixture(self, transport, transform=None):
        listener = socket.socket(socket.AF_INET, socket.SOCK_DGRAM if transport == "udp" else socket.SOCK_STREAM)
        listener.bind(("127.0.0.1", 0))
        listener.settimeout(2)
        if transport == "tcp":
            listener.listen()
        target = {"host": "127.0.0.1", "port": listener.getsockname()[1]}
        errors = []
        def respond():
            peer = None
            try:
                if transport == "udp":
                    request, remote = listener.recvfrom(65536)
                else:
                    peer, remote = listener.accept()
                    peer.settimeout(2)
                    request = b""
                    while b"\r\n\r\n" not in request:
                        data = peer.recv(4096)
                        if not data:
                            return
                        request += data
                _first, raw = request.split(b"\r\n", 1)
                headers = suite.parse_headers(raw.split(b"\r\n\r\n")[0])
                response = ("SIP/2.0 200 OK\r\n" + "\r\n".join(name + ": " + headers[name] for name in ("via", "from", "to", "call-id", "cseq")) + "\r\nAllow: INVITE, ACK, BYE, OPTIONS\r\nContent-Length: 0\r\n\r\n").encode()
                if transform:
                    response = transform(response)
                if transport == "udp":
                    listener.sendto(response, remote)
                else:
                    # 实际 TCP 多次写入，接收器不能假设一次 recv 就是一帧。
                    for index in range(0, len(response), 7):
                        peer.sendall(response[index:index + 7])
            except OSError as error:
                errors.append(type(error).__name__)
            finally:
                if peer:
                    peer.close()
                listener.close()
        worker = threading.Thread(target=respond)
        worker.start()
        return target, worker, errors

    def test_udp_options_real_bytes(self):
        target, worker, errors = self.sip_fixture("udp")
        wire = suite.Wire(65536)
        try:
            value = suite.run_sip({"adapter": "sip_udp"}, target, dict(LIMITS, max_header_bytes=2048), wire)
            self.assertEqual(value["status_code"], 200)
            self.assertEqual(value["capabilities"]["allow"], ["ack", "bye", "invite", "options"])
            self.assertEqual({row["direction"] for row in wire.records}, {"send", "receive"})
        finally:
            worker.join(3)
        self.assertFalse(errors)

    def test_tcp_options_fragmented_response(self):
        target, worker, errors = self.sip_fixture("tcp")
        try:
            value = suite.run_sip({"adapter": "sip_tcp"}, target, dict(LIMITS, max_header_bytes=2048), suite.Wire(65536))
            self.assertEqual(value["status_code"], 200)
        finally:
            worker.join(3)
        self.assertFalse(errors)

    def test_wrong_call_id_fails(self):
        target, worker, _ = self.sip_fixture("udp", lambda data: data.replace(b"call-id: ", b"call-id: other-"))
        try:
            with self.assertRaises(suite.ProbeError):
                suite.run_sip({"adapter": "sip_udp"}, target, dict(LIMITS, max_header_bytes=2048), suite.Wire(65536))
        finally:
            worker.join(3)

    def test_wrong_from_tag_fails(self):
        target, worker, _ = self.sip_fixture("udp", lambda data: data.replace(b";tag=", b";tag=bad"))
        try:
            with self.assertRaises(suite.ProbeError):
                suite.run_sip({"adapter": "sip_udp"}, target, dict(LIMITS, max_header_bytes=2048), suite.Wire(65536))
        finally:
            worker.join(3)

    def test_partial_udp_body_cannot_splice_next_packet(self):
        target, worker, _ = self.sip_fixture("udp", lambda data: data.replace(b"Content-Length: 0", b"Content-Length: 1"))
        try:
            with self.assertRaises(EOFError):
                suite.run_sip({"adapter": "sip_udp"}, target, dict(LIMITS, max_header_bytes=2048), suite.Wire(65536))
        finally:
            worker.join(3)

    def test_udp_extra_body_cannot_pass(self):
        target, worker, _ = self.sip_fixture("udp", lambda data: data + b"unframed")
        try:
            with self.assertRaises(suite.ProbeError):
                suite.run_sip({"adapter": "sip_udp"}, target, dict(LIMITS, max_header_bytes=2048), suite.Wire(65536))
        finally:
            worker.join(3)

    def test_esl_echo_real_socket_and_redacted_auth(self):
        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        listener.listen()
        listener.settimeout(2)
        target = {"host": "127.0.0.1", "port": listener.getsockname()[1], "password_env": "TEST_CONFORMANCE_PASSWORD"}
        errors = []
        def respond():
            peer, _ = listener.accept()
            peer.settimeout(2)
            try:
                peer.sendall(b"Content-Type: auth/request\n\n")
                buffered = b""
                for expected in [b"auth fixture-only", ("api echo " + suite.ECHO_TEXT).encode()]:
                    while b"\n\n" not in buffered:
                        data = peer.recv(4096)
                        if not data:
                            return
                        buffered += data
                    message, buffered = buffered.split(b"\n\n", 1)
                    if message != expected:
                        errors.append("unexpected-request")
                        return
                    if expected.startswith(b"auth"):
                        peer.sendall(b"Content-Type: command/reply\nReply-Text: +OK accepted\n\n")
                    else:
                        body = suite.ECHO_TEXT.encode()
                        response = b"Content-Type: api/response\nContent-Length: " + str(len(body)).encode() + b"\n\n" + body
                        for byte in response:
                            peer.sendall(bytes([byte]))
            finally:
                peer.close()
                listener.close()
        worker = threading.Thread(target=respond)
        worker.start()
        wire = suite.Wire(65536)
        try:
            with mock.patch.dict(os.environ, {"TEST_CONFORMANCE_PASSWORD": "fixture-only"}):
                result = suite.run_esl({"id": "esl.echo"}, target, dict(LIMITS, max_header_bytes=2048), wire)
            self.assertEqual(result["response"]["headers"]["content-length"], str(len(suite.ECHO_TEXT.encode())))
            secret_rows = [row for row in wire.records if "redacted" in row]
            self.assertEqual(len(secret_rows), 1)
            self.assertNotIn("base64", secret_rows[0])
        finally:
            worker.join(3)
        self.assertFalse(errors)


class InventoryAndEventTests(unittest.TestCase):
    def test_inventory_requires_truthful_count_and_unique_names(self):
        good = {"row_count": 1, "rows": [{"name": "echo", "description": "Echo", "syntax": "<data>", "ikey": "fixture"}]}
        frame = {"headers": {"content-type": "api/response"}, "body": json.dumps(good).encode()}
        self.assertEqual(suite.parse_api_inventory(frame), good)
        for value in [dict(good, row_count=2), dict(good, row_count=True), {"row_count": 2, "rows": good["rows"] * 2}, {"row_count": 1, "rows": [{"name": "echo"}]}]:
            with self.subTest(value=value), self.assertRaises(suite.ProbeError):
                suite.parse_api_inventory(dict(frame, body=json.dumps(value).encode()))

    def test_dynamic_inventory_preserves_all_source_api_ids_without_green(self):
        plan = suite.build_plan(ROOT)
        reference = {"echo", "uuid_exists"}
        candidate = {"echo"}
        def observation(names):
            rows = [{"name": name, "description": "fixture", "syntax": "", "ikey": "fixture"} for name in sorted(names)]
            return {"state": "observed", "value": {"inventory": {"row_count": len(rows), "rows": rows}}}
        result = suite.compare_api_inventory(plan, {"results": {"reference": observation(reference), "candidate": observation(candidate)}})
        self.assertEqual(result["source_registration_entries"], 292)
        self.assertEqual({entry["id"] for entry in result["entries"]}, {entry["id"] for entry in plan["entries"] if entry["category"] == "fs_api"})
        for row in result["entries"]:
            expected = "requires_semantic_verification" if row["name"] == "echo" else "missing_candidate_entrypoint" if row["name"] == "uuid_exists" else "blocked_reference_module"
            self.assertEqual(row["state"], expected)
            self.assertFalse(row["complete_contract_passed"])

    def test_missing_runtime_inventory_never_means_empty_success(self):
        result = suite.compare_api_inventory(suite.build_plan(ROOT), {})
        self.assertEqual(result["counts_by_state"], {"blocked_reference_inventory": 292})
        self.assertFalse(result["reference_observed"])

    def test_json_raw_percent_is_not_decoded_twice(self):
        event = suite.parse_event(json.dumps({"Event-Name": "BACKGROUND_JOB", "Job-Command-Arg": "%20", "Content-Length": "0", "_body": ""}).encode(), "json")
        self.assertEqual(event["headers"]["job-command-arg"], "%20")
        self.assertEqual(event["body"], b"")

    def test_xml_header_and_body_have_different_decoding(self):
        raw = '<event><headers><Event-Name>BACKGROUND_JOB</Event-Name><Job-Command-Arg>a%2Bb</Job-Command-Arg></headers><Content-Length>4</Content-Length><body>&lt;%20</body></event>'
        event = suite.parse_event(raw.encode(), "xml")
        self.assertEqual(event["headers"]["job-command-arg"], "a+b")
        self.assertEqual(event["body"], b"<%20")

    def test_nested_xml_body_dtd_and_duplicate_json_headers_rejected(self):
        for raw in [b'<event><headers/><body><hidden/></body></event>', b'<!DOCTYPE event [<!ENTITY a "x">]><event><headers/></event>', b'<event><headers/><Content-Length>2</Content-Length><body>x</body></event>']:
            with self.subTest(raw=raw), self.assertRaises(suite.ProbeError):
                suite.parse_event(raw, "xml")
        with self.assertRaises(suite.ProbeError):
            suite.parse_event(b'{"Event-Name":"x","event-name":"y"}', "json")


class VerdictTests(unittest.TestCase):
    """用受控执行结果测试汇总逻辑，绝不把 mock 结果发布为产品运行报告。"""
    def setUp(self):
        self.profile = json.loads((ROOT / "tests/conformance/profile.example.json").read_text())

    def observe(self, probe, endpoint, profile, limits):
        if probe["id"] == "identity.esl":
            return {"state": "observed", "value": {"version": "FreeSWITCH Version 1.11.3"}, "wire_bytes": 10, "wire": []}
        if probe["id"] == "identity.api_inventory":
            return {"state": "observed", "value": {"inventory": {"row_count": 1, "rows": [{"name": "echo", "description": "Echo", "syntax": "<data>", "ikey": "fixture"}]}}, "wire_bytes": 10, "wire": []}
        return {"state": "observed", "value": {"body": "same"}, "wire_bytes": 10, "wire": []}

    def test_all_finite_probes_pass_still_never_full_product(self):
        with mock.patch.object(suite, "execute_probe", side_effect=self.observe):
            report = suite.run_suite(ROOT, self.profile)
        self.assertTrue(report["all_defined_probes_passed"])
        self.assertFalse(report["full_compatibility_passed"])
        self.assertFalse(report["all_protocols_passed"])
        self.assertFalse(report["full_superiority_proven"])
        self.assertEqual(report["counts"]["full_contract_passed"], 0)
        self.assertTrue(all(not row["complete_contract_passed"] for row in report["entries"]))

    def test_unselected_probes_prevent_full_probe_pass(self):
        with mock.patch.object(suite, "execute_probe", side_effect=self.observe):
            report = suite.run_suite(ROOT, self.profile, ["esl.echo"])
        self.assertFalse(report["all_defined_probes_passed"])
        self.assertEqual(report["counts"]["paired_scoped_passes"], 1)
        self.assertGreater(report["counts"]["probes_by_state"]["not_run_not_selected"], 0)

    def test_actual_wrong_reference_blocks_product_probes(self):
        def wrong(*args):
            return {"state": "observed", "value": {"version": "FreeSWITCH Version 1.10.12"}, "wire_bytes": 10, "wire": []}
        with mock.patch.object(suite, "execute_probe", side_effect=wrong) as executor:
            report = suite.run_suite(ROOT, self.profile)
        self.assertEqual(executor.call_count, 2)
        self.assertEqual(report["probes"][0]["state"], "failed_baseline")
        self.assertEqual(report["counts"]["paired_scoped_passes"], 0)

    def test_empty_traffic_cannot_pass(self):
        def missing(probe, *args):
            value = self.observe(probe, *args)
            if probe["id"] != "identity.esl":
                value["wire_bytes"] = 0
            return value
        with mock.patch.object(suite, "execute_probe", side_effect=missing):
            report = suite.run_suite(ROOT, self.profile)
        self.assertEqual(report["counts"]["probes_by_state"]["failed_missing_traffic"], len(suite.PROBES) - 1)
        self.assertFalse(report["all_defined_probes_passed"])

    def test_byte_difference_is_not_normalized_away(self):
        def different(probe, endpoint, profile, limits):
            value = self.observe(probe, endpoint, profile, limits)
            if probe["id"] != "identity.esl" and endpoint["esl"]["port"] == 8022:
                value["value"]["body"] = "same\n"
            return value
        with mock.patch.object(suite, "execute_probe", side_effect=different):
            report = suite.run_suite(ROOT, self.profile)
        self.assertEqual(report["counts"]["probes_by_state"]["failed_difference"], len([p for p in suite.PROBES if not p.get("identity_only")]))
        self.assertFalse(report["all_defined_probes_passed"])

    def test_source_changes_invalidate_scoped_green(self):
        with mock.patch.object(suite, "execute_probe", side_effect=self.observe), mock.patch.object(suite, "source_fingerprint", side_effect=[{"sha256": "before"}, {"sha256": "after"}]):
            report = suite.run_suite(ROOT, self.profile)
        self.assertEqual(report["counts"]["probes_by_state"]["stale_source_changed"], len([p for p in suite.PROBES if not p.get("identity_only")]))
        self.assertFalse(report["all_defined_probes_passed"])
        self.assertTrue(all(item["state"] != "passed_scoped" for row in report["entries"] for item in row["probe_results"]))


class SourceBindingTests(unittest.TestCase):
    """原版成对报告同样不能遗漏本地汇编和固定第三方模块选择。"""
    def test_local_native_and_vendor_changes_invalidate_source_fingerprint(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            baseline = suite.source_fingerprint(root)
            for relative in ['control/internal/clock/read.s', 'control/internal/clock/read.c',
                             'control/vendor/modules.txt', 'control/vendor/example.org/clock/read.s']:
                with self.subTest(path=relative):
                    path = root / relative
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text('actual compilation input\n')
                    self.assertNotEqual(suite.source_fingerprint(root), baseline)
                    self.assertIn(relative, suite.source_fingerprint(root)['files'])
                    path.unlink()
            header = root / 'control/internal/server/doc_assets/native/include/example.h'
            header.parent.mkdir(parents=True)
            header.write_text('download-only documented declaration\n')
            self.assertEqual(suite.source_fingerprint(root), baseline)


if __name__ == "__main__":
    unittest.main(verbosity=2)
