#!/usr/bin/env python3
"""真实SIP/媒体/ASR原件的独立离线核验；不读取通过标记替代字节断言。"""
import ast
import datetime
import hashlib
import json
import math
from pathlib import Path
import re
import struct
import time

HERE = Path(__file__).resolve().parent
BASE = HERE.parent
PROJECT = BASE / "project"
RUN = BASE / "server-real-02"
CASES = RUN / "cases"

# 复用已独立实现的ASR1偏移解析函数；只装载函数定义，不执行旧批次或调用生产Go解析器。
HELPER = BASE / "client-independent-audit-02/audit.py"
tree = ast.parse(HELPER.read_text(encoding="utf-8"))
functions = [n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name in {"sha", "strict_pairs", "read_json", "frames"}]
exec(compile(ast.Module(body=functions, type_ignores=[]), str(HELPER), "exec"), globals())


def snapshot(root):
    return {str(p.relative_to(root)): sha(p) for p in sorted(root.rglob("*")) if p.is_file()}


def json_lines(path):
    return [json.loads(line, object_pairs_hook=strict_pairs) for line in path.read_text().splitlines()]


def one(events, kind):
    selected = [e for e in events if e["kind"] == kind]
    assert len(selected) == 1, (kind, len(selected))
    return selected[0]


def sip_message(event):
    raw = bytes.fromhex(event["value"]["hex"])
    head, body = raw.split(b"\r\n\r\n", 1)
    lines = head.decode("ascii").split("\r\n")
    headers = {}
    for line in lines[1:]:
        name, value = line.split(":", 1)
        name = name.lower()
        assert name not in headers
        headers[name] = value.strip()
    assert int(headers["content-length"]) == len(body)
    return {"line": lines[0], "headers": headers, "body": body.decode("ascii"), "event": event}


def g711(payload, code):
    """从标准量化码字直接计算有符号幅值，独立于Rust/Go生产解码器。"""
    if payload == 0:
        value = code ^ 255
        magnitude = (((value & 15) * 8 + 132) * (2 ** ((value >> 4) & 7))) - 132
        return -magnitude if value & 128 else magnitude
    value = code ^ 0x55
    exponent = (value >> 4) & 7
    magnitude = (value & 15) * 16 + 8
    if exponent:
        magnitude = (magnitude + 256) * (2 ** (exponent - 1))
    return magnitude if value & 128 else -magnitude


COEFFICIENT = PROJECT / "control/internal/asr/resample/testdata/coefficients.f64le"
assert sha(COEFFICIENT) == "e7d108779e82bc025af440ac3f2fbf4434972f93298b4bdfc79c56dc50795f01"
TAPS = struct.unpack("<127d", COEFFICIENT.read_bytes())


def fir(samples):
    upsampled = [v for sample in samples for v in (sample * 2, 0)]
    output = []
    for n in range(len(upsampled)):
        value = sum(TAPS[tap] * upsampled[n-tap] for tap in range(min(126, n)+1))
        rounded = math.floor(value+.5) if value >= 0 else math.ceil(value-.5)
        output.append(max(-32768, min(32767, rounded)))
    return struct.pack("<%dh" % len(output), *output)


assert (CASES / "suite-receipt.json").is_file()
before = snapshot(RUN)
outer, suite = read_json(RUN/"receipt.json"), read_json(CASES/"suite-receipt.json")
sources = read_json(CASES/"source-before.json")
assert sources == read_json(CASES/"source-after.json") == outer["source_before"] == outer["source_after"]
actual_source = {}
for root in ("control", "media/src", "config"):
    for path in (PROJECT/root).rglob("*"):
        assert not path.is_symlink()
        if path.is_file():
            actual_source[str(path.relative_to(PROJECT))] = sha(path)
for name in ("media/Cargo.lock", "media/Cargo.toml"):
    actual_source[name] = sha(PROJECT/name)
assert sources.keys() == actual_source.keys()
current_differences = {path:{"historical":digest,"current":actual_source[path]} for path,digest in sources.items() if actual_source[path] != digest}
# 根任务已明确后续只修正该真实客户端测试的/tmp便携路径；历史收据不得被当前源覆盖。
assert set(current_differences).issubset({"control/internal/asr/stream_real_test.go"})
assert outer["binaries"] == suite["binaries"]
assert all(sha(Path(path)) == digest for path, digest in suite["binaries"].items())
assert len(suite["binaries"]) == 3 and all(step["returncode"] == 0 for step in outer["steps"])
go_events = json_lines(RUN/"actual.jsonl")
assert not [e for e in go_events if e.get("Action") in ("skip", "fail")]
passed_tests = [e["Test"] for e in go_events if e.get("Action") == "pass" and "Test" in e]
assert len(passed_tests) == 6
rust_build = read_json(BASE.parent/"voice-runtime-m2-rx/media-build-08/receipt.json")
mock_build = read_json(BASE/"mock-build-04/receipt.json")
assert rust_build["source_before"] == rust_build["source_after"]
assert mock_build["source_before"] == mock_build["source_after"]
assert all(sources[path] == digest for path,digest in rust_build["source_before"].items())
assert all(sources["control/"+path] == digest for path,digest in mock_build["source_before"].items())
assert rust_build["binary"]["sha256"] in suite["binaries"].values()
assert mock_build["executables"]["asr-mock"] in suite["binaries"].values()

expected_cases = {"pcmu_8k_udp", "pcma_8k_connected", "pcmu_16k_udp", "pcma_16k_connected", "pcmu_8k_late_subscription"}
assert {p.name for p in CASES.iterdir() if p.is_dir()} == expected_cases
case_reports = []
for name in sorted(expected_cases):
    directory = CASES/name
    trace = json_lines(directory/"wire.jsonl")
    assert [e["elapsed_ns"] for e in trace] == sorted(e["elapsed_ns"] for e in trace)
    identity = one(trace, "identity")["value"]
    cfg = one(trace, "actual_config")["value"]
    assert identity["binaries"] == suite["binaries"]
    rate, payload, late = identity["sample_rate"], identity["payload"], identity["late_subscription"]
    assert rate == (16000 if "16k" in name else 8000)
    assert payload == (8 if "pcma" in name else 0)
    assert identity["connected"] == cfg["media"]["connect_sockets"] == ("connected" in name)
    base = cfg["media"]["port_start"]
    assert 4000 <= base and cfg["media"]["port_end"] == base+3 < 4800
    assert cfg["sip"]["listen"] == "127.0.0.1:%d" % (base+4)
    assert cfg["asr_stream"]["sample_rate"] == rate and cfg["asr_stream"]["max_streams"] == 1
    ready = one(trace,"actual_media_ready")["value"]
    assert ready["healthy"] and ready["worker_id"] == 0 and ready["generation"] > 0 and ready["pid"] > 0
    assert "rx_g711_local_v2" in ready["capabilities"]

    sip_events = [sip_message(e) for e in trace if e["kind"].startswith("sip_")]
    request = lambda method: [e for e in sip_events if e["line"].startswith(method+" ")]
    invite, wrong_ack, ack, bye = request("INVITE")[0], request("ACK")[0], request("ACK")[1], request("BYE")[0]
    options = request("OPTIONS")[0]
    response = lambda method: next(e for e in sip_events if e["line"] == "SIP/2.0 200 OK" and e["headers"]["cseq"].endswith(" "+method))
    answered, barrier, bye_ok = response("INVITE"), response("OPTIONS"), response("BYE")
    call_id = invite["headers"]["call-id"]
    for message in (invite, wrong_ack, ack, bye):
        assert message["headers"]["call-id"] == call_id
        assert message["headers"]["from"] == invite["headers"]["from"]
        assert message["event"]["value"]["target"] == cfg["sip"]["listen"]
        assert message["line"].split()[1] == "sip:1000@" + cfg["sip"]["listen"]
    assert wrong_ack["event"]["value"]["source"] != invite["event"]["value"]["source"]
    assert ack["event"]["value"]["source"] == bye["event"]["value"]["source"] == invite["event"]["value"]["source"]
    assert len(re.findall(r";tag=[^; ]+", answered["headers"]["to"])) == 1
    for message in (wrong_ack, ack, bye):
        assert message["headers"]["to"] == answered["headers"]["to"]
    for req, resp in ((invite,answered),(options,barrier),(bye,bye_ok)):
        for key in ("call-id", "cseq", "from"):
            assert req["headers"][key] == resp["headers"][key]
        assert re.search(r"branch=([^;]+)", req["headers"]["via"]).group(1) == re.search(r"branch=([^;]+)", resp["headers"]["via"]).group(1)
        assert resp["event"]["value"]["source"] == cfg["sip"]["listen"]
        assert resp["event"]["value"]["target"] == req["event"]["value"]["source"]
    assert ack["headers"]["cseq"] == wrong_ack["headers"]["cseq"] == "1 ACK" and bye["headers"]["cseq"] == "2 BYE"
    assert "m=audio %d RTP/AVP %d" % (base,payload) in answered["body"]
    assert "m=audio %d RTP/AVP %d" % (base+7,payload) in invite["body"]
    assert "c=IN IP4 127.0.0.1" in answered["body"] and "a=ptime:20" in answered["body"]
    rejected = one(trace, "wrong_ack_asr_start")
    assert not rejected["value"]["has_stream"] and rejected["value"]["asr"]["slots"] == 0
    assert "ACKed" in rejected["value"]["error"]
    acquisitions = [e for e in trace if e["kind"] == "actual_rx_acquire"]
    assert len(acquisitions) == 2 and not acquisitions[0]["value"]["has_source"] and acquisitions[1]["value"]["has_source"]
    assert answered["event"]["elapsed_ns"] < wrong_ack["event"]["elapsed_ns"] < barrier["event"]["elapsed_ns"] < rejected["elapsed_ns"] < ack["event"]["elapsed_ns"] < acquisitions[1]["elapsed_ns"]
    created = [e["value"] for e in trace if e["kind"] == "actual_esl_event" and e["value"]["name"] == "CHANNEL_CREATE"]
    assert len(created) == 1 and created[0]["headers"]["variable_sip_call_id"] == call_id
    uuid = created[0]["headers"]["Unique-ID"]
    assert all(e["value"]["uuid"] == uuid and e["value"]["subscription_id"] == 1 for e in acquisitions)

    provider_root = directory/"provider"
    provider_ready = read_json(provider_root/"ready.json")
    rejected_dir = provider_root/"connection-0001"
    rejected_in, rejected_out = frames(rejected_dir/"inbound.bin"), frames(rejected_dir/"outbound.bin")
    rejected_receipt = read_json(rejected_dir/"receipt.json")
    assert [x["kind"] for x in rejected_in] == [1] and [x["kind"] for x in rejected_out] == [2]
    assert rejected_in[0]["data"] == rejected_out[0]["data"] and rejected_in[0]["data"]["uuid"] == uuid
    assert not (rejected_dir/"consumed.pcm").read_bytes() and rejected_receipt["state"] == "failed"
    assert all(sha(rejected_dir/path) == digest for path,digest in rejected_receipt["files"].items())

    rx_start = one(trace,"actual_asr_start")
    rx_status = one(trace,"actual_rust_rx_status")["value"]
    assert rx_status["error"] == "<nil>" and rx_status["reply"]["type"] == "rx_state" and rx_status["reply"]["ok"] and rx_status["reply"]["state"] == "active"
    assert rx_status["reply"]["subscription_id"] == 1
    assert rx_status["reply"]["session"] == rx_start["value"]["rx"]["Session"]
    sent = [e for e in trace if e["kind"] == "rtp_send"]
    assert len(sent) == (12 if late else 8)
    inputs = {}
    for event in sent:
        raw = bytes.fromhex(event["value"]["hex"])
        assert len(raw) == 172 and raw[0] == 0x80 and raw[1]&127 == payload
        sequence, timestamp, ssrc = struct.unpack_from("!HII", raw, 2)
        index = (sequence-65532) & 65535
        assert index in (list(range(4))+list(range(20,28)) if late else range(8))
        assert timestamp == (0xfffffc00+index*160)&0xffffffff and ssrc == 0x7a123456
        assert raw[12:] == bytes((index*47+i*13+31)&255 for i in range(160))
        assert event["value"]["source"] == "127.0.0.1:%d" % (base+7)
        assert event["value"]["target"] == "127.0.0.1:%d" % base
        inputs[index] = (raw,event)
    assert all(0 <= e["value"]["late_ns"] < 20_000_000 for e in trace if e["kind"] == "generator_deadline")
    rx = [e for e in trace if e["kind"] == "actual_rx_sdk_frame"]
    decoded = [e for e in rx if e["value"]["frame"]["Kind"] == 1]
    assert len(decoded) == 8 and len({e["value"]["frame"]["Sequence"] for e in rx}) == len(rx)
    assert [e["value"]["frame"]["Sequence"] for e in rx] == list(range(1,len(rx)+1))
    if late:
        assert rx[0]["value"]["frame"]["Kind"] == 1 and all(e["value"]["frame"]["Kind"] != 7 for e in rx)
        pre = one(trace,"late_subscription_precondition")
        assert pre["elapsed_ns"] < rx_start["elapsed_ns"]
        assert all(inputs[i][1]["elapsed_ns"] < rx_start["elapsed_ns"] for i in range(4))
    expected, native_history, last = {}, [], None
    raw_rx = {}
    for event in rx:
        f = event["value"]["frame"]
        raw_rx[f["Sequence"]] = f
        assert f["Session"] == rx_status["reply"]["session"] and f["SubscriptionID"] == 1
        assert f["ExpiresAtNS"] == f["ObservationLowerBoundNS"]+100_000_000
        assert event["value"]["capture_clock_domain"] == f["ClockDomain"] == 2
        assert f["ObservationLowerBoundNS"] <= event["value"]["capture_clock_ns"] < f["ExpiresAtNS"]
        if f["Kind"] != 1:
            assert f["Kind"] == 7 and f["Boundary"] == 1 and not late
            continue
        index = (f["RTPSequence"]-65532)&65535
        assert index in (range(20,28) if late else range(8))
        raw, sending = inputs[index]
        assert sending["elapsed_ns"] < event["elapsed_ns"] and sending["elapsed_ns"] > rx_start["elapsed_ns"]
        assert f["RTPSequence"] == (1<<16)+65532+index
        assert f["RTPTimestamp"] == (1<<32)+0xfffffc00+index*160
        assert f["SSRC"] == 0x7a123456 and f["SourceGeneration"] == 1 and f["SourceSegment"] == (2 if late else 1)
        assert f["MediaTimeNS"] == index*20_000_000 and f["Flags"]&15 == 15
        assert f["SampleCount"] == 160 and f["BodyBytes"] == 320 and f["KnownDurationTicks"] == 160
        samples = [g711(payload,value) for value in raw[12:]]
        assert f["PCM"][:160] == samples
        if f["Boundary"] == 5:
            native_history = []
        if last:
            assert f["RTPSequence"] == last["RTPSequence"]+1 and f["RTPTimestamp"] == last["RTPTimestamp"]+160
        native_history += samples
        expected[f["Sequence"]] = struct.pack("<160h",*samples) if rate==8000 else fir(native_history)[-640:]
        last = f

    connection = provider_root/"connection-0002"
    incoming,outgoing = frames(connection/"inbound.bin"),frames(connection/"outbound.bin")
    provider = read_json(connection/"receipt.json")
    provider_events = json_lines(connection/"events.jsonl")
    hello = incoming[0]["data"]
    assert incoming[0]["kind"] == 1 and outgoing[0]["kind"] == 2 and hello == outgoing[0]["data"]
    assert hello["uuid"] == uuid and hello["subscription_id"] == "1" and hello["source_rate"] == 8000 and hello["target_rate"] == rate
    audio = [m["data"] for m in incoming if m["kind"] == 3]
    markers = [m["data"] for m in incoming if m["kind"] == 4]
    assert len(audio) == 8 and len(markers) == (0 if late else 1)
    for a in audio:
        f = raw_rx[a["seq"]]
        mapping={"generation":"SourceGeneration","segment":"SourceSegment","rtp_seq":"RTPSequence","rtp_ts":"RTPTimestamp","media":"MediaTimeNS","lower":"ObservationLowerBoundNS","expiry":"ExpiresAtNS","ssrc":"SSRC","rtp_rate":"RTPClockRate","flags":"Flags","domain":"ClockDomain"}
        assert all(a[k]==f[v] for k,v in mapping.items())
        assert a["pcm"] == expected[a["seq"]] and a["token"].hex() == hello["stream_token"]
        assert a["rate"] == rate and a["origin"] == 1 and a["samples"] == rate//50
        assert a["delay"] == (3937500 if rate==16000 else 0)
    for marker in markers:
        f=raw_rx[int(marker["event_seq"])]
        assert marker["rx_kind"] == f["Kind"] and marker["boundary"] == f["Boundary"]
        assert int(marker["source_generation"]) == f["SourceGeneration"] and int(marker["source_segment"]) == f["SourceSegment"]
    pcm = b"".join(a["pcm"] for a in audio)
    assert pcm == (connection/"consumed.pcm").read_bytes() and len(pcm) == 8*(rate//50)*2
    consumed = [e for e in provider_events if e["stage"] == "audio-consumed"]
    assert len(consumed) == 8
    for offset,(a,e) in enumerate(zip(audio,consumed)):
        assert int(e["event_seq"]) == a["seq"] and e["pcm_offset"] == offset*(rate//50)*2
        assert e["pcm_bytes"] == len(a["pcm"]) and e["samples"] == a["samples"]
        assert int(e["lower_ns"]) == a["lower"] and int(e["expires_ns"]) == a["expiry"]
        assert a["lower"] <= int(e["checked_clock_ns"]) < a["expiry"] and e["clock_domain"] == a["domain"]
    finishes=[m["data"] for m in incoming if m["kind"]==5]
    dones=[m["data"] for m in outgoing if m["kind"]==7]
    assert len(finishes)==1 and finishes==dones
    finish=finishes[0]
    assert finish==provider["counters"] and finish["audio_frames"]=="8" and int(finish["samples"])==len(pcm)//2
    assert int(finish["last_event_seq"])==audio[-1]["seq"] and int(finish["markers"])==len(markers)
    results=[m["data"] for m in outgoing if m["kind"]==6]
    assert len(results)==5 and [int(r["result_seq"]) for r in results]==list(range(1,6))
    assert [r["type"] for r in results]==["partial"]*4+["final"]
    for number,result in enumerate(results,1):
        selected=[a for a in audio if int(result["first_event_seq"])<=a["seq"]<=int(result["last_event_seq"])]
        assert result["stream_token"]==hello["stream_token"] and result["utterance_id"]=="1" and int(result["revision"])==number
        assert int(result["source_generation"])==selected[0]["generation"] and int(result["source_segment"])==selected[0]["segment"]
        assert result["text"]=="mock-sha256:"+hashlib.sha256(b"".join(a["pcm"] for a in selected)).hexdigest()
        assert int(result["start_media_ns"])==selected[0]["media"] and int(result["end_media_ns"])==selected[-1]["media"]+20_000_000
    delivered=[e for e in trace if e["kind"]=="actual_asr_result"]
    assert [e["value"]["type"] for e in delivered]==["partial","final"]
    for event in delivered:
        actual=event["value"]
        result=next(r for r in results if int(r["result_seq"])==actual["result_sequence"])
        assert actual["text"]==result["text"] and actual["type"]==result["type"]
        assert event["elapsed_ns"]<bye["event"]["elapsed_ns"]
    attempts=frames(connection/"final-attempted.bin")
    assert len(attempts)==1 and attempts[0]["data"]==results[-1]
    write_results=[e for e in provider_events if e["stage"]=="final-write-result"]
    assert len(write_results)==1 and write_results[0]["accepted_bytes"]==attempts[0]["bytes"] and write_results[0]["error"]==""
    assert provider["state"]=="completed" and provider["error"]=="" and provider["connection_closed"]
    assert all(sha(connection/path)==digest for path,digest in provider["files"].items())

    fresh=one(trace,"fresh_media_stats")
    assert fresh["elapsed_ns"]>bye_ok["event"]["elapsed_ns"] and fresh["value"]["error"]=="<nil>" and fresh["value"]["reply"]["ok"]
    stats=fresh["value"]["reply"]["stats"]
    zero_fields=["active_calls","processed_active_calls","processed_local_active_calls","rx_active_subscriptions","rx_queued_events","rx_observation_storage_bytes","rx_observation_failed"]
    assert all(stats[key]==0 for key in zero_fields)
    assert stats["processed_decoded_frames"]==(12 if late else 8)
    rebound=one(trace,"media_ports_rebound")
    assert rebound["value"]==list(range(base,base+4)) and rebound["elapsed_ns"]>fresh["elapsed_ns"]
    final=one(trace,"actual_final_zero")
    assert final["elapsed_ns"]>rebound["elapsed_ns"] and final["elapsed_ns"]-bye["event"]["elapsed_ns"]>=31_000_000_000
    assert final["value"]["active_calls"]==final["value"]["established"]==final["value"]["timers"]==0
    final_asr=final["value"]["completed_asr"]
    assert final_asr["state"]=="completed" and final_asr["provider_done"] and final_asr["resources_closed"] and final_asr["rx_stopped"]
    assert final_asr["adapter_written_samples"]==final_asr["provider_acknowledged_samples"]==len(pcm)//2
    assert final["value"]["asr"]["slots"]==0
    server_cleanup=one(trace,"server_cleanup")["value"]
    assert server_cleanup["normal_drain"] and not server_cleanup["test_failed"] and server_cleanup["asr"]["slots"]==0
    cleanup=one(trace,"provider_cleanup")["value"]
    provider_run=read_json(provider_root/"run-receipt.json")
    assert cleanup["pid"]==provider_run["pid"]==provider["pid"]==provider_ready["pid"]==rejected_receipt["pid"]
    assert not cleanup["forced"] and cleanup["wait_error"]=="<nil>" and cleanup["socket_removed"] and not Path(provider_ready["socket"]).exists()
    assert provider_run["accepted"]==2 and provider_run["completed_connections"]==provider_run["failed_connections"]==1
    assert provider_run["active"]==0 and provider_run["all_connection_goroutines_joined"] and provider_run["listener_closed"]
    journal=json_lines(directory/"journal.jsonl")
    assert [e["kind"] for e in journal]==["controller_started","call_admitted","call_answered","call_established","call_ended"]
    assert all(e.get("call_id",call_id)==call_id for e in journal) and journal[-1]["reason"]=="normal_hangup"
    case_reports.append({"case":name,"payload":payload,"output_rate":rate,"connected":identity["connected"],"late_subscription":late,"input_rtp_packets":len(sent),"asr_decoded_frames":8,"native_decoded_samples":1280,"provider_samples":len(pcm)//2,"provider_pcm_sha256":hashlib.sha256(pcm).hexdigest(),"first_rx_kind":rx[0]["value"]["frame"]["Kind"],"first_rx_boundary":rx[0]["value"]["frame"]["Boundary"],"first_expanded_rtp_sequence":decoded[0]["value"]["frame"]["RTPSequence"],"last_expanded_rtp_sequence":decoded[-1]["value"]["frame"]["RTPSequence"],"provider_pid":provider["pid"],"rust_pid":ready["pid"],"wrong_ack_delivered_audio":False,"delivered_result_types":[e["value"]["type"] for e in delivered],"bye_after_asr_completed":True,"seconds_bye_to_zero_timers":(final["elapsed_ns"]-bye["event"]["elapsed_ns"])/1e9,"media_ports_rebound":rebound["value"],"provider_normal_wait":True})

after=snapshot(RUN)
assert before==after
receipt={"schema":"asr-server-independent-audit-v1","created_at_unix":time.time(),"scope":"offline_independent_audit_of_real_sip_rust_sdk_asr1_mock_transport","network_started":False,"source_modified":False,"audit_script_sha256":sha(Path(__file__)),"parser_helper_sha256":sha(HELPER),"historical_source_count":len(sources),"historical_source_before_equals_after":True,"current_source_matches":not current_differences,"current_source_differences":current_differences,"rust_build_media_source_count":len(rust_build["source_before"]),"mock_build_source_count":len(mock_build["source_before"]),"actual_binaries":suite["binaries"],"test_pass_count":len(passed_tests),"artifacts_before":before,"artifacts_after":after,"artifacts_unchanged":True,"raw_assertions_passed":True,"cases":case_reports,"limits":["Exactly five short isolated local SIP/G711 cases; no production Linux capacity, real speech recognizer, external provider or full FreeSWITCH equivalence.","Late subscription first decoded carries real NewSegment Boundary5. This does not prove real SIP first decoded with no Boundary5.","BYE occurs after ASR completed; no real SIP hangup interrupts a pending final in this suite.","Raw RTP and raw ASR1 are preserved; intermediate RX is the actual Server SDK frame trace, not a separate raw RXS2 socket capture.","Rust supervisor uses existing Kill+Wait; do not claim normal Rust process exit zero.","Port rebind and timer zero rely on actual test trace plus frozen test/source and successful executable checks; audit does not bind ports again."]}
with (HERE/"receipt.json").open("x") as f:
    json.dump(receipt,f,ensure_ascii=False,indent=2)
print(json.dumps({"raw_assertions_passed":True,"cases":len(case_reports),"source_files":len(sources),"artifact_files":len(before),"receipt":str(HERE/"receipt.json")},ensure_ascii=True))
