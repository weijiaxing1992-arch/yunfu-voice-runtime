#!/usr/bin/env python3
"""成对报告发布门禁负例：测试夹具不是产品运行证据，不改真实报告或正式源码指纹。"""
import base64
import copy
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("conformance_publisher_self_test", ROOT / "tools/build_conformance_report.py")
publisher = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(publisher)
suite = publisher.suite


def sha(data):
    return hashlib.sha256(data).hexdigest()


def encoded(value):
    return (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode()


def wire(direction, data, secret=False):
    value = {"direction": direction, "length": len(data), "sha256": sha(data)}
    if secret:
        value["redacted"] = "ESL 认证秘密；不保留可恢复密码"
    else:
        value["base64"] = base64.b64encode(data).decode()
    return value


def response(body):
    headers = {"content-type": "api/response", "content-length": str(len(body))}
    raw = b"Content-Type: api/response\nContent-Length: " + str(len(body)).encode() + b"\n\n" + body
    return {"headers": headers, "body_base64": base64.b64encode(body).decode()}, raw


def observation(command, body, identity=False):
    """夹具保留可解析的真实协议格式，但绝不作为原版服务执行记录输出。"""
    frame, raw_response = response(body)
    records = [
        wire("receive", b"Content-Type: auth/request\n\n"),
        wire("send", b"auth unit-fixture-only\n\n", secret=True),
        wire("receive", b"Content-Type: command/reply\nReply-Text: +OK accepted\n\n"),
        wire("send", command),
        wire("receive", raw_response),
    ]
    value = {"version": body.decode(), "frame": frame} if identity else {
        "greeting": {"headers": {"content-type": "auth/request"}, "body_base64": ""},
        "authentication": {"headers": {"content-type": "command/reply", "reply-text": "+OK accepted"}, "body_base64": ""},
        "response": frame,
    }
    return {"state": "observed", "value": value, "wire_bytes": sum(item["length"] for item in records), "wire": records}


class ConformancePublishTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.comparison = json.loads((ROOT / "docs/api/comparison.json").read_text())
        cls.plan = suite.build_plan(ROOT)

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.target = Path(self.temporary.name)
        self.current = {"sha256": "a" * 64, "files": {"control/fixture.go": "b" * 64}}
        self.addCleanup(mock.patch.stopall)
        mock.patch.object(publisher, "TARGET", self.target).start()
        mock.patch.object(suite, "source_fingerprint", return_value=self.current).start()
        now = datetime.now(timezone.utc)
        self.build = {
            "source_before": self.current, "source_after": self.current, "returncode": 0,
            "started_at": (now - timedelta(minutes=2)).isoformat(), "finished_at": (now - timedelta(minutes=1)).isoformat(),
            "binaries": {"rustswitch": "c" * 64},
            "runtime_config": {"sha256": "d" * 64},
        }
        self.reference = {
            "version": suite.REFERENCE_VERSION, "reference_version": suite.REFERENCE_VERSION,
            "commit": suite.REFERENCE_COMMIT, "reference_commit": suite.REFERENCE_COMMIT,
            "binary": {"sha256": "e" * 64}, "active_config": {"sha256": "f" * 64},
        }
        (self.target / "conformance-build.json").write_bytes(encoded(self.build))
        (self.target / "conformance-reference.json").write_bytes(encoded(self.reference))
        entries = [{"id": row["id"], "complete_contract_passed": False} for row in self.comparison["entries"]]
        probes = []
        for definition in suite.PROBES:
            row = dict(definition, state="not_run_not_selected", complete_contract_passed=False)
            if definition["id"] == "identity.esl":
                row.update(state="identity_observed", counts_as_compatibility_pass=False, results={
                    "reference": observation(b"api version\n\n", b"FreeSWITCH Version 1.11.3-release~64bit\n", identity=True),
                    "candidate": observation(b"api version\n\n", b"RustSwitch Version unit-fixture\n", identity=True),
                })
            if definition["id"] == "esl.echo":
                value = observation(("api echo " + suite.ECHO_TEXT + "\n\n").encode(), suite.ECHO_TEXT.encode())
                row.update(state="passed_scoped", results={"reference": copy.deepcopy(value), "candidate": copy.deepcopy(value)})
            probes.append(row)
        plan = self.plan
        self.report = {
            "schema_version": "1.0.0", "source_stable": True, "source_before": self.current, "source_after": self.current,
            "reference": {"version": suite.REFERENCE_VERSION, "commit": suite.REFERENCE_COMMIT},
            "reference_runtime_executed": True, "full_compatibility_passed": False, "all_protocols_passed": False,
            "full_superiority_proven": False, "production_capacity_certified": False,
            "started_at": (now - timedelta(seconds=10)).isoformat(), "finished_at": now.isoformat(),
            "fixture": {"isolated": True, "allow_remote": False}, "profile_sha256": "1" * 64,
            "plan_sha256": sha(suite.canonical(plan)), "plan_counts": plan["counts"],
            "entries": entries, "probes": probes,
            "api_inventory": suite.compare_api_inventory(plan, {}),
            "endpoint_artifacts": {
                "reference": {"missing": [], "files": {
                    "binary_path": {"sha256": "e" * 64}, "config_path": {"sha256": "f" * 64},
                    "source_manifest_path": {"sha256": sha(encoded(self.reference))},
                }},
                "candidate": {"missing": [], "files": {
                    "binary_path": {"sha256": "c" * 64}, "config_path": {"sha256": "d" * 64},
                    "source_manifest_path": {"sha256": sha(encoded(self.build))},
                }},
            },
        }

    def row(self, probe_id="esl.echo"):
        return next(row for row in self.report["probes"] if row["id"] == probe_id)

    def reject(self):
        with self.assertRaises((ValueError, KeyError, TypeError)):
            publisher.summarize(encoded(self.report))

    def test_valid_partial_fixture_never_full_product_green(self):
        value = publisher.summarize(encoded(self.report))
        self.assertEqual(value["counts"]["passed_scoped"], 1)
        self.assertFalse(value["full_compatibility_passed"])
        self.assertFalse(value["all_protocols_passed"])
        self.assertFalse(value["full_superiority_proven"])

    def test_missing_original_wire_rejected(self):
        self.row()["results"]["candidate"]["wire"] = []
        self.row()["results"]["candidate"]["wire_bytes"] = 0
        self.reject()

    def test_changed_packet_sha_rejected(self):
        self.row()["results"]["candidate"]["wire"][-1]["sha256"] = "0" * 64
        self.reject()

    def test_wire_length_sum_rejected(self):
        self.row()["results"]["candidate"]["wire_bytes"] += 1
        self.reject()

    def test_invalid_base64_rejected(self):
        self.row()["results"]["reference"]["wire"][-1]["base64"] = "not-base64$"
        self.reject()

    def test_receive_frames_cannot_all_be_redacted(self):
        for result in self.row()["results"].values():
            for item in result["wire"]:
                item.pop("base64", None)
                item["redacted"] = "hide all traffic"
        self.reject()

    def test_identity_cannot_be_green(self):
        self.row("identity.esl")["state"] = "passed_scoped"
        self.reject()

    def test_identity_requires_actual_version_traffic(self):
        self.row("identity.esl")["results"] = {}
        self.reject()

    def test_wrong_reference_identity_rejected(self):
        self.row("identity.esl")["results"]["reference"] = observation(b"api version\n\n", b"FreeSWITCH Version 1.10.12\n", identity=True)
        self.reject()

    def test_missing_probe_rejected(self):
        self.report["probes"].pop()
        self.reject()

    def test_probe_scope_cannot_be_broadened(self):
        self.row()["scope"] = "全部 FreeSWITCH API 和所有协议已完全兼容"
        self.reject()

    def test_missing_original_id_rejected(self):
        self.report["entries"].pop()
        self.reject()

    def test_fake_original_id_cannot_preserve_denominator_count(self):
        self.report["entries"][0]["id"] = "invented:replacement"
        self.reject()

    def test_report_expired_after_thirty_days_rejected(self):
        self.report["finished_at"] = (datetime.now(timezone.utc) - timedelta(days=31)).isoformat()
        self.reject()

    def test_future_report_rejected(self):
        self.report["finished_at"] = (datetime.now(timezone.utc) + timedelta(days=1)).isoformat()
        self.reject()

    def test_candidate_binary_different_from_build_rejected(self):
        self.report["endpoint_artifacts"]["candidate"]["files"]["binary_path"]["sha256"] = "0" * 64
        self.reject()

    def test_reference_binary_different_from_install_rejected(self):
        self.report["endpoint_artifacts"]["reference"]["files"]["binary_path"]["sha256"] = "0" * 64
        self.reject()

    def test_build_manifest_hash_tampering_rejected(self):
        self.report["endpoint_artifacts"]["candidate"]["files"]["source_manifest_path"]["sha256"] = "0" * 64
        self.reject()

    def test_candidate_config_must_match_build_receipt(self):
        self.report["endpoint_artifacts"]["candidate"]["files"]["config_path"]["sha256"] = "0" * 64
        self.reject()

    def test_changed_source_invalidates_green(self):
        self.report["source_after"] = {"sha256": "0" * 64, "files": {}}
        self.reject()

    def test_full_contract_claim_rejected(self):
        self.row()["complete_contract_passed"] = True
        self.reject()

    def test_unequal_results_rejected(self):
        self.row()["results"]["candidate"]["value"]["response"]["body_base64"] = base64.b64encode(b"changed").decode()
        self.reject()

    def test_identity_packet_sha_must_be_validated(self):
        self.row("identity.esl")["results"]["reference"]["wire"][-1]["sha256"] = "0" * 64
        self.reject()

    def test_original_api_request_cannot_disappear(self):
        result = self.row()["results"]["candidate"]
        result["wire"] = [item for item in result["wire"] if item["direction"] != "send" or "redacted" in item]
        result["wire_bytes"] = sum(item["length"] for item in result["wire"])
        self.reject()

    def test_recomputed_packet_hash_cannot_hide_claimed_response_difference(self):
        item = self.row()["results"]["candidate"]["wire"][-1]
        original = base64.b64decode(item["base64"])
        changed = original[:-1] + bytes([original[-1] ^ 1])
        item["base64"] = base64.b64encode(changed).decode()
        item["sha256"] = sha(changed)
        # 包长度和SHA内部自洽，但已不再支持 value 中宣称的 echo；必须从原流量重新验证。
        self.reject()

    def test_original_entry_cannot_claim_complete_contract(self):
        self.report["entries"][0]["complete_contract_passed"] = True
        self.reject()

    def test_api_inventory_cannot_drop_original_registration_ids(self):
        self.report["api_inventory"]["entries"] = []
        self.report["api_inventory"]["source_registration_entries"] = 0
        self.reject()


if __name__ == "__main__":
    unittest.main(verbosity=2)
