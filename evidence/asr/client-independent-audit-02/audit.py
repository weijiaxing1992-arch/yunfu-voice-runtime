#!/usr/bin/env python3
"""只读核验第二轮ASR真实连接证据；不调用生产解析器，不启动任何网络。"""
import hashlib
import json
import math
from pathlib import Path
import struct
import time

HERE = Path(__file__).resolve().parent
BASE = HERE.parent
RUN = BASE / "client-real-02"
CONTROL = BASE / "project/control"


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def strict_pairs(pairs):
    result = {}
    for k, v in pairs:
        assert k not in result, ("duplicate JSON key", k)
        result[k] = v
    return result


def read_json(path):
    return json.loads(path.read_text(), object_pairs_hook=strict_pairs)


def frames(path):
    """长度、种类、版本、保留位和完整EOF均从原始字节重算。"""
    data = path.read_bytes()
    out, off = [], 0
    while off < len(data):
        assert len(data) - off >= 8
        size, kind, version, reserved = struct.unpack_from("<IBBH", data, off)
        assert 4 <= size <= 8192 and 1 <= kind <= 11 and version == 1 and reserved == 0
        assert off + size + 4 <= len(data)
        body = data[off + 8:off + size + 4]
        if kind == 3:
            assert len(body) >= 112
            values = struct.unpack_from("<16s8Q4I5H6s", body)
            names = ("token", "seq", "generation", "segment", "rtp_seq", "rtp_ts", "media", "lower", "expiry", "ssrc", "rate", "rtp_rate", "delay", "flags", "domain", "samples", "origin", "bytes", "reserved")
            parsed = dict(zip(names, values))
            parsed["pcm"] = body[112:]
            assert parsed["reserved"] == bytes(6) and parsed["bytes"] == len(parsed["pcm"]) == parsed["samples"] * 2
        else:
            parsed = json.loads(body.decode("utf-8", "strict"), object_pairs_hook=strict_pairs)
        out.append({"kind": kind, "data": parsed, "offset": off, "bytes": size + 4})
        off += size + 4
    assert off == len(data)
    return out


def pcm_oracle(sequences, rate):
    """独立生成夹具非静音输入；16k采用直接插零卷积，不调用Go多相实现。"""
    src = [j * 23 - 1200 + seq * 31 for seq in sequences for j in range(160)]
    if rate == 16000:
        coefficients = struct.unpack("<127d", (CONTROL / "internal/asr/resample/testdata/coefficients.f64le").read_bytes())
        upsampled = [v for x in src for v in (x * 2, 0)]
        output = []
        for n in range(len(upsampled)):
            value = sum(coefficients[k] * upsampled[n-k] for k in range(min(n, 126) + 1))
            rounded = math.floor(value + .5) if value >= 0 else math.ceil(value - .5)
            output.append(max(-32768, min(32767, rounded)))
        src = output
    return struct.pack("<%dh" % len(src), *src)


def snapshot():
    return {str(p.relative_to(RUN)): sha(p) for p in sorted(RUN.rglob("*")) if p.is_file()}


before = snapshot()
root = read_json(RUN / "receipt.json")
assert root["source_before"] == root["source_after"]
assert sha(RUN / "asr.test") == root["test_binary_sha256"]
assert sha(Path(root["mock_binary"])) == root["mock_sha256"]
assert all(s["returncode"] == 0 for s in root["steps"])
test_events = [json.loads(line) for line in (RUN / "actual.jsonl").read_text().splitlines()]
assert not [x for x in test_events if x.get("Action") in ("fail", "skip")]
test_passes = [x["Test"] for x in test_events if x.get("Action") == "pass" and "Test" in x]
assert len(test_passes) == 10
case_results = []
for directory in sorted((RUN / "cases").iterdir()):
    name = directory.name
    receipt = read_json(directory / "receipt.json")
    connection = directory / "provider/connection-0001"
    provider = read_json(connection / "receipt.json")
    ready = read_json(directory / "provider/ready.json")
    run = read_json(directory / "provider/run-receipt.json")
    events = [json.loads(x, object_pairs_hook=strict_pairs) for x in (connection / "events.jsonl").read_text().splitlines()]
    assert receipt["provider_pid"] == provider["pid"] == ready["pid"] == run["pid"]
    assert receipt["provider_wait_error"] == "<nil>" and not receipt["forced"] and receipt["socket_removed"]
    assert not Path(ready["socket"]).exists()
    assert run["active"] == 0 and run["accepted"] == 1 and run["all_connection_goroutines_joined"] and run["listener_closed"]
    assert receipt["rx_source"] == "unit_source_not_rust" and receipt["recognition_model"] is False
    assert all(sha(connection / path) == digest for path, digest in provider["files"].items())
    incoming, outgoing = frames(connection / "inbound.bin"), frames(connection / "outbound.bin")
    assert sum(x["bytes"] for x in incoming) == provider["inbound_bytes"]
    assert sum(x["bytes"] for x in outgoing) == provider["outbound_bytes"]
    assert incoming[0]["kind"] == 1 and outgoing[0]["kind"] == 2 and incoming[0]["data"] == outgoing[0]["data"]
    hello = incoming[0]["data"]
    assert hello["uuid"] == name and hello["target_rate"] == receipt["sample_rate"]
    audio = [x["data"] for x in incoming if x["kind"] == 3]
    marks = [x["data"] for x in incoming if x["kind"] == 4]
    inputs = {x["Sequence"]: x for x in receipt["input_frames"]}
    sequences = [x["seq"] for x in audio]
    expected_start = 1 if name == "midstream_first_decoded_8k" else 2
    assert sequences == list(range(expected_start, expected_start + len(audio)))
    expected = pcm_oracle(sequences, hello["target_rate"])
    assert b"".join(x["pcm"] for x in audio) == expected
    for a in audio:
        original = inputs[a["seq"]]
        for field, source in {"seq":"Sequence", "generation":"SourceGeneration", "segment":"SourceSegment", "rtp_seq":"RTPSequence", "rtp_ts":"RTPTimestamp", "media":"MediaTimeNS", "lower":"ObservationLowerBoundNS", "expiry":"ExpiresAtNS", "ssrc":"SSRC", "rtp_rate":"RTPClockRate", "flags":"Flags", "domain":"ClockDomain"}.items():
            assert a[field] == original[source], (name, field)
        assert a["token"].hex() == hello["stream_token"] and a["origin"] == 1 and a["flags"] == 15
        assert a["expiry"] == a["lower"] + 100_000_000 and a["rate"] == hello["target_rate"]
        assert a["samples"] == a["rate"] // 50 and a["delay"] == (3937500 if a["rate"] == 16000 else 0)
        assert original["PCM"][:160] == [j*23-1200+a["seq"]*31 for j in range(160)]
    assert len(marks) == (0 if name == "midstream_first_decoded_8k" else 1)
    for mark in marks:
        assert mark["rx_kind"] == 7 and mark["boundary"] == 1 and mark["event_seq"] == "1"
    consumed_events = [x for x in events if x["stage"] == "audio-consumed"]
    consumed = b""
    for event in consumed_events:
        matches = [a for a in audio if a["seq"] == int(event["event_seq"])]
        assert len(matches) == 1
        a = matches[0]
        assert int(event["lower_ns"]) == a["lower"] and int(event["expires_ns"]) == a["expiry"]
        assert a["lower"] <= int(event["checked_clock_ns"]) < a["expiry"]
        assert event["clock_domain"] == a["domain"]
        assert event["pcm_offset"] == len(consumed) and event["pcm_bytes"] == len(a["pcm"])
        consumed += a["pcm"]
    assert consumed == (connection / "consumed.pcm").read_bytes()
    counters = provider["counters"]
    assert int(counters["audio_frames"]) == len(consumed_events) and int(counters["samples"]) == len(consumed)//2
    assert len(consumed) == provider["consumed_pcm_bytes"]
    finishes = [x["data"] for x in incoming if x["kind"] == 5]
    dones = [x["data"] for x in outgoing if x["kind"] == 7]
    results = [x["data"] for x in outgoing if x["kind"] == 6]
    assert all(f == counters for f in finishes)
    for index, result in enumerate(results):
        assert int(result["result_seq"]) == index + 1
        selected = [a for a in audio if int(result["first_event_seq"]) <= a["seq"] <= int(result["last_event_seq"])]
        assert selected and result["text"] == "mock-sha256:" + hashlib.sha256(b"".join(a["pcm"] for a in selected)).hexdigest()
        assert int(result["start_media_ns"]) == selected[0]["media"] and int(result["end_media_ns"]) == selected[-1]["media"]+20_000_000
        assert result["stream_token"] == hello["stream_token"] or name == "wrong_token"
    snap = receipt["snapshot_before_cleanup"]
    for event in receipt["events"] or []:
        match = [x for x in results if int(x["result_seq"]) == event["result_sequence"]]
        assert len(match) == 1 and match[0]["text"] == event["text"] and match[0]["type"] == event["type"]
    evidence_limit = None
    attempts = frames(connection / "final-attempted.bin")
    attempt_events = [x for x in events if x["stage"] == "final-write-attempt"]
    write_results = [x for x in events if x["stage"] == "final-write-result"]
    assert len(attempts) == len(attempt_events) == len(write_results)
    attempted_raw = (connection / "final-attempted.bin").read_bytes()
    outgoing_raw = (connection / "outbound.bin").read_bytes()
    assert len(attempted_raw) == provider["final_attempted_bytes"]
    for attempt, ae, we in zip(attempts, attempt_events, write_results):
        assert attempt["kind"] == 6 and attempt["data"]["type"] == "final"
        result = attempt["data"]
        assert result["stream_token"] == hello["stream_token"] and result["source_generation"] == "1" and result["source_segment"] == "1"
        assert result["text"] == "mock-sha256:" + hashlib.sha256(consumed).hexdigest()
        assert int(result["first_event_seq"]) == audio[0]["seq"] and int(result["last_event_seq"]) == audio[-1]["seq"]
        assert int(result["start_media_ns"]) == audio[0]["media"] and int(result["end_media_ns"]) == audio[-1]["media"]+20_000_000
        assert ae["attempt_offset"] == we["attempt_offset"] == attempt["offset"]
        assert ae["bytes"] == we["attempt_bytes"] == attempt["bytes"]
        raw = attempted_raw[attempt["offset"]:attempt["offset"]+attempt["bytes"]]
        assert hashlib.sha256(raw).hexdigest() == ae["sha256"]
        assert 0 <= we["accepted_bytes"] <= attempt["bytes"]
        assert ae["outbound_offset"] == we["outbound_offset"]
        assert outgoing_raw[we["outbound_offset"]:we["outbound_offset"]+we["accepted_bytes"]] == raw[:we["accepted_bytes"]]
        assert int(ae["clock_ns"]) <= int(we["clock_ns"]) and ae["clock_domain"] == we["clock_domain"] == hello["clock_domain"]
        if name == "hangup_late_final":
            assert we["accepted_bytes"] == 0 and "broken pipe" in we["error"]
        else:
            assert we["accepted_bytes"] == attempt["bytes"] and we["error"] == ""
    rejected_checks = []
    for rejection in [x for x in events if x["stage"] == "consume-rejected"]:
        assert name == "expired_consumption" and rejection["kind"] == 3
        actual = [a for a in audio if a["seq"] == int(rejection["event_seq"])]
        assert len(actual) == 1
        a = actual[0]
        assert int(rejection["lower_ns"]) == a["lower"] and int(rejection["expires_ns"]) == a["expiry"]
        assert rejection["input_clock_domain"] == rejection["clock_domain"] == a["domain"]
        checked = int(rejection["checked_clock_ns"])
        assert a["expiry"] <= checked <= int(rejection["clock_ns"])
        rejected_checks.append({"event_seq":a["seq"],"actual_checked_ns":checked,"original_expiry_ns":a["expiry"],"late_ns":checked-a["expiry"]})
    if name in ("native_8k", "resampled_16k", "midstream_first_decoded_8k"):
        assert len(finishes) == len(dones) == 1 and finishes == dones
        assert snap["state"] == "completed" and snap["provider_done"] and snap["resources_closed"]
        assert snap["adapter_written_samples"] == snap["provider_acknowledged_samples"] == len(consumed)//2
        assert len([x for x in results if x["type"] == "final"]) == 1
    else:
        assert snap["state"] in ("failed", "cancelled") and not snap["provider_done"]
    if name == "wrong_done":
        assert len(dones) == 1 and int(dones[0]["samples"]) == int(finishes[0]["samples"])+1
        assert {k:v for k,v in dones[0].items() if k != "samples"} == {k:v for k,v in finishes[0].items() if k != "samples"}
    if name == "missing_done":
        assert not dones and results[-1]["type"] == "final" and provider["error"] == "injected close_before_done"
    if name == "wrong_token":
        assert len(results) == 1 and results[0]["stream_token"] != hello["stream_token"] and not receipt["events"]
    if name == "duplicate_final":
        finals = [r for r in results if r["type"] == "final"]
        assert len(finals) == 2 and finals[0]["utterance_id"] == finals[1]["utterance_id"]
    if name == "expired_consumption":
        assert len(audio) == 1 and not consumed_events and not consumed and not results and not dones
        assert "已经过期" in provider["error"] and any(x["stage"] == "fault-consume-pause" for x in events)
        assert len(rejected_checks) == 1
        evidence_limit = "已独立复算拒绝检查点超过原期限；写入端已提交4帧仍不等于供应商接收4帧。"
    if name == "hangup_late_final":
        assert len(audio) == 4 and any(x["stage"] == "waiting-final" for x in events)
        assert not [r for r in results if r["type"] == "final"] and not dones and not receipt["events"]
        assert len(attempts) == 1 and provider["state"] == "failed"
        waiting = next(x for x in events if x["stage"] == "waiting-final")
        assert int(waiting["clock_ns"]) < int(attempt_events[0]["clock_ns"])
        evidence_limit = "已证明等待结束后独立供应商实际尝试376B final且Write接受0B/broken pipe；客户端撤权仅来自模拟LifetimeDone，未采集独立撤权时钟，不能冒称真实SIP BYE或ASR模型识别。"
    case_results.append({"case":name,"pid":provider["pid"],"received_audio_frames":len(audio),"consumed_audio_frames":len(consumed_events),"provider_samples":len(consumed)//2,"client_written_audio_frames":snap["adapter_written_audio_frames"],"client_written_samples":snap["adapter_written_samples"],"pcm_sha256":hashlib.sha256(consumed).hexdigest(),"wire_kinds_in":[x["kind"] for x in incoming],"wire_kinds_out":[x["kind"] for x in outgoing],"client_state":snap["state"],"provider_state":provider["state"],"process_waited_and_socket_removed":True,"scope_limit":evidence_limit,"rejected_checks":rejected_checks,"final_attempt_count":len(attempts),"final_attempt_bytes":len(attempted_raw),"final_accepted_bytes":sum(w["accepted_bytes"] for w in write_results)})

after = snapshot()
assert before == after
changed = [p for p,h in root["source_before"].items() if not (CONTROL/p).is_file() or sha(CONTROL/p) != h]
report = {"schema":"asr1-independent-raw-audit-v1","created_utc_unix":time.time(),"audit_script_sha256":sha(Path(__file__)),"scope":"offline_only_real_unix_independent_provider_synthetic_RX_no_recognition","historical_source_before_equals_after":True,"historical_bound_source_count":len(root["source_before"]),"current_source_different_from_historical":changed,"artifact_hashes_before":before,"artifact_hashes_after":after,"artifacts_unchanged":True,"raw_checks_passed":True,"case_count":len(case_results),"test_pass_count":len(test_passes),"test_binary_sha256":root["test_binary_sha256"],"mock_binary_sha256":root["mock_sha256"],"coefficient_sha256":sha(CONTROL/"internal/asr/resample/testdata/coefficients.f64le"),"independent_16k_oracle":"127-tap direct causal zero-insertion convolution, integer rounding; exact chosen PCM samples, not general frequency-response certification","cases":case_results,"limits":["冻结391源码由原批次前后收据绑定；当前后续开发已改变清单所列文件，旧批次不能认证当前源码。","16k原测试调用同一Go转换器作为expected；本审计用独立Python直接卷积补足这批指定输入的逐样本验证。","unit_source未验证Rust、真实SIP、通话timer/端口、生产供应商识别；单连接不代表并发容量。","失败案例RX最终清理由测试模拟retired后Reap，不能冒称真实Rust撤销。","第二轮已记录并独立复算过期拒绝检查点、final实际写尝试与Write接受前缀；仍不认证SIP生命周期或生产识别效果。"]}
with (HERE/"receipt.json").open("x") as file:
    json.dump(report,file,ensure_ascii=False,indent=2)
print(json.dumps({"raw_checks_passed":True,"cases":len(case_results),"current_changed_source":changed,"receipt":str(HERE/"receipt.json")},ensure_ascii=False))
