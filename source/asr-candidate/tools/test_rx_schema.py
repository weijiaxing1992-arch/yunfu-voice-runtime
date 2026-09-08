#!/usr/bin/env python3
"""离线核验候选RX JSON Schema与原始控制收据；不执行收据引用脚本，不运行媒体进程。"""
import argparse
import collections
import datetime
import hashlib
import json
import pathlib
import sys
from jsonschema import Draft202012Validator

U64 = 2**64 - 1
ERRORS = ["invalid_observation", "sequence_exhausted", "sample_count_exhausted", "gap_metadata_discontinuity", "rx_lane_unavailable", "rx_observation_failed", "rx_clock_out_of_range"]
RX_OPS = ["rx_subscribe", "rx_unsubscribe", "rx_status"]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def runtime_accounting(value, operation="rx_status"):
    """独立对账预言机；只补Schema无跨字段算术/请求关联能力的明确约束。"""
    p, s, d, q = (value[k] for k in ["produced_events", "submitted_events", "dropped_events", "queued_events"])
    if p != s + d + q:
        return False
    ps, ss, ds = (value[k] for k in ["produced_samples", "submitted_samples", "dropped_samples"])
    qs = ps - ss - ds
    if qs < 0 or qs > q * 160 or ps // 160 > p or ss // 160 > s or ds // 160 > d:
        return False
    return not (operation == "rx_unsubscribe" and value["state"] == "active")


def vectors(stats=None):
    rows = []
    def add(name, branch, value, valid, accounting=None, operation="rx_status"):
        rows.append({"name": name, "branch": branch, "value": value, "schema_valid": valid, "accounting_valid": accounting, "operation": operation})
    for op in RX_OPS:
        req = {"id": 1, "op": op, "session": 2, "subscription_id": 3}
        add(op, op, req, True)
        add(op + "_unknown_fields_ignored", op, dict(req, generation=None, future={"ignored": True}), True)
        add(op + "_u64_max", op, dict(req, id=U64, session=U64, subscription_id=U64), True)
        for key in req:
            v = dict(req); del v[key]; add(op + "_missing_" + key, op, v, False)
            v = dict(req); v[key] = None; add(op + "_null_" + key, op, v, False)
        for key in ["id", "session", "subscription_id"]:
            for bad, label in [(-1, "negative"), (U64 + 1, "overflow"), (True, "bool"), (1.5, "fraction")]:
                add(f"{op}_{key}_{label}", op, dict(req, **{key: bad}), False)
        for key in ["session", "subscription_id"]:
            add(f"{op}_{key}_zero", op, dict(req, **{key: 0}), False)
    state = {"id": 1, "ok": True, "type": "rx_state", "session": 2, "subscription_id": 3, "state": "active", "produced_events": 0, "submitted_events": 0, "dropped_events": 0, "queued_events": 0, "produced_samples": 0, "submitted_samples": 0, "dropped_samples": 0, "oldest_age_ms": 0, "error": ""}
    add("state_zero", "rx_state", state, True, True)
    add("state_unknown_fields_ignored", "rx_state", dict(state, future=None), True, True)
    add("state_stopped", "rx_state", dict(state, state="stopped"), True, True, "rx_unsubscribe")
    add("state_mixed_audio_and_auxiliary", "rx_state", dict(state, produced_events=5, submitted_events=2, dropped_events=1, queued_events=2, produced_samples=480, submitted_samples=160, dropped_samples=160, oldest_age_ms=37), True, True)
    add("state_large_age_still_queryable", "rx_state", dict(state, produced_events=1, queued_events=1, oldest_age_ms=200), True, True)
    for error in ERRORS:
        add("state_failure_" + error, "rx_state", dict(state, state="failed", error=error), True, True)
    for key in state:
        v = dict(state); del v[key]; add("state_missing_" + key, "rx_state", v, False)
        add("state_null_" + key, "rx_state", dict(state, **{key: None}), False)
    for key in ["produced_events", "submitted_events", "dropped_events", "queued_events", "produced_samples", "submitted_samples", "dropped_samples", "oldest_age_ms"]:
        for bad, label in [(-1, "negative"), (U64 + 1, "overflow"), (True, "bool")]:
            add(f"state_{key}_{label}", "rx_state", dict(state, **{key: bad}), False)
    for key in ["produced_samples", "submitted_samples", "dropped_samples"]:
        add("state_partial_frame_" + key, "rx_state", dict(state, **{key: 159}), False)
    for name, patch in [("queue_9", {"queued_events": 9}), ("active_error", {"error": ERRORS[0]}), ("failed_empty", {"state": "failed"}), ("failed_unknown_error", {"state": "failed", "error": "asr_offline"}), ("stopped_queue", {"state": "stopped", "queued_events": 1}), ("empty_queue_age", {"oldest_age_ms": 1}), ("wrong_state", {"state": "playing"}), ("false_success", {"ok": False})]:
        add("state_" + name, "rx_state", dict(state, **patch), False)
    # 这些是有意构造的运行时负例：Schema必须允许结构，不能谎称自己已经算了跨字段守恒。
    for name, patch in [("event_conservation", {"produced_events": 1}), ("sample_conservation", {"produced_events": 1, "submitted_events": 1, "produced_samples": 160}), ("samples_exceed_events", {"produced_samples": 160, "submitted_samples": 160})]:
        add("runtime_only_" + name, "rx_state", dict(state, **patch), True, False)
    add("runtime_only_unsubscribe_active", "rx_state", state, True, False, "rx_unsubscribe")
    ready = {"id": 0, "ok": True, "type": "ready", "protocol_version": 1, "worker_id": 0, "pid": 1, "capabilities": ["processed_g711_v1", "processed_g711_local_v1", "rx_g711_local_v2"]}
    add("ready_v2", "ready", ready, True)
    add("ready_rx_without_local", "ready", dict(ready, capabilities=["processed_g711_v1", "rx_g711_local_v2"]), False)
    add("ready_old_worker_no_rx", "ready", dict(ready, capabilities=[]), True)
    if stats is not None:
        keys = ["rx_active_subscriptions", "rx_queued_events", "rx_observation_storage_bytes", "rx_observation_failed"]
        add("stats_current_rx_fields", "Stats", stats, True)
        add("stats_legacy_missing_rx_is_unknown", "Stats", {k: v for k, v in stats.items() if k not in keys}, True)
        for key in keys:
            for bad, label in [(-1, "negative"), (U64 + 1, "overflow"), (True, "bool"), (None, "null"), (1.5, "fraction"), ("0", "string")]:
                add(f"stats_{key}_{label}", "Stats", dict(stats, **{key: bad}), False)
    return rows


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--schema", type=pathlib.Path, default=pathlib.Path(__file__).resolve().parents[1] / "docs/api/media-ipc.schema.json")
    parser.add_argument("--wire-dir", action="append", type=pathlib.Path, default=[])
    parser.add_argument("--artifacts", type=pathlib.Path, required=True)
    args = parser.parse_args()
    args.artifacts.mkdir(parents=True, exist_ok=False)
    schema_before = digest(args.schema)
    schema = json.loads(args.schema.read_text())
    Draft202012Validator.check_schema(schema)
    branches = {}
    for container, tag in [("Request", "op"), ("Reply", "type")]:
        for value in schema["$defs"][container]["oneOf"]:
            name = value.get("properties", {}).get(tag, {}).get("const")
            if name:
                branches[name] = Draft202012Validator(dict(value, **{"$defs": schema["$defs"]}))
    branches["Stats"] = Draft202012Validator(dict(schema["$defs"]["Stats"], **{"$defs": schema["$defs"]}))
    # 正例以已经采集的真实完整Stats为底稿，只修改明确的RX负例字段。
    stats_fixture = None
    for directory in args.wire_dir:
        for path in sorted(directory.glob("wire/*/wire.json")):
            for item in json.loads(path.read_text()).get("wire", []):
                if item.get("direction") == "json_receive":
                    value = json.loads(bytes.fromhex(item["hex"]))
                    if value.get("type") == "stats":
                        stats_fixture = value["stats"]
                        break
            if stats_fixture is not None:
                break
        if stats_fixture is not None:
            break
    rows = vectors(stats_fixture)
    failures = []
    for item in rows:
        found = list(branches[item["branch"]].iter_errors(item["value"]))
        item["actual_schema_valid"] = not found
        item["errors"] = [e.message[:220] for e in found[:3]]
        if item["actual_schema_valid"] != item["schema_valid"]:
            failures.append("vector schema mismatch: " + item["name"])
        if item["accounting_valid"] is not None:
            item["actual_accounting_valid"] = runtime_accounting(item["value"], item["operation"])
            if item["actual_accounting_valid"] != item["accounting_valid"]:
                failures.append("vector accounting mismatch: " + item["name"])
    (args.artifacts / "vectors.json").write_text(json.dumps(rows, ensure_ascii=False, indent=2) + "\n")
    wire_results = []
    counts = collections.Counter()
    for directory in args.wire_dir:
        paths = sorted(directory.glob("wire/*/wire.json"))
        if not paths:
            failures.append("no raw wire receipts: " + str(directory))
        for path in paths:
            before = digest(path)
            record = json.loads(path.read_text())
            pending = {}
            checked = []
            for index, item in enumerate(record.get("wire", [])):
                if item.get("direction") not in ["json_send", "json_receive"]:
                    continue
                raw = bytes.fromhex(item["hex"])
                if len(raw) != item["bytes"]:
                    failures.append(f"raw byte length: {path}:{index}")
                value = json.loads(raw)
                if item["direction"] == "json_send":
                    pending[value["id"]] = value
                    continue
                kind = value.get("type")
                request = pending.get(value.get("id"))
                if kind not in ["rx_state", "ready", "stats", "error"]:
                    continue
                errors = list(branches[kind].iter_errors(value))
                valid = not errors
                counts[kind] += 1
                result = {"index": index, "type": kind, "id": value.get("id"), "schema_valid": valid, "errors": [e.message[:220] for e in errors[:3]]}
                if not valid:
                    failures.append(f"raw reply schema: {path}:{index}")
                if kind == "rx_state":
                    associated = request is not None and request.get("op") in RX_OPS and all(request.get(k) == value.get(k) for k in ["session", "subscription_id"])
                    result["request_identity_matches"] = associated
                    result["runtime_accounting_valid"] = valid and associated and runtime_accounting(value, request["op"])
                    if not result["runtime_accounting_valid"]:
                        failures.append(f"raw RX identity/accounting: {path}:{index}")
                if request is not None and request.get("op") in RX_OPS:
                    req_valid = branches[request["op"]].is_valid(request)
                    result["request_schema_valid"] = req_valid
                    counts["rx_requests"] += 1
                    # 非法结构只有真实error回复才可作为负例，不能把拒绝混记为执行通过。
                    if not req_valid and not (kind == "error" and value.get("ok") is False):
                        failures.append(f"invalid RX request accepted: {path}:{index}")
                    if kind == "error":
                        counts["rx_request_rejections"] += 1
                checked.append(result)
            after = digest(path)
            if before != after:
                failures.append("wire changed during read: " + str(path))
            wire_results.append({"path": str(path.resolve()), "sha256": before, "unchanged": before == after, "checks": checked})
    (args.artifacts / "raw-checks.json").write_text(json.dumps(wire_results, ensure_ascii=False, indent=2) + "\n")
    if digest(args.schema) != schema_before:
        failures.append("schema changed during validation")
    receipt = {"finished_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "scope": "候选RX Schema结构、独立运行算术与既有真实控制JSON回执；不执行媒体、不验证全部RXS2二进制、不授予SIP/ASR/容量", "schema_sha256": schema_before, "schema_unchanged": digest(args.schema) == schema_before, "vectors": len(rows), "positive_schema_vectors": sum(r["schema_valid"] for r in rows), "negative_schema_vectors": sum(not r["schema_valid"] for r in rows), "runtime_only_negative_vectors": sum(r["schema_valid"] and r["accounting_valid"] is False for r in rows), "wire_files": len(wire_results), "raw_counts": dict(counts), "failures": failures, "passed": not failures, "script_sha256": digest(pathlib.Path(__file__)), "artifacts": {p.name: digest(p) for p in args.artifacts.iterdir() if p.is_file()}}
    (args.artifacts / "receipt.json").write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(receipt, ensure_ascii=False, indent=2))
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
