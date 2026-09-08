#!/usr/bin/env python3
"""诊断解析回归：合成输入仅验证解析/拒绝策略，不能用于性能认证。"""
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import analyze_testlab_runtime as audit

GC = "gc 8 @23.100s 1%: 0.12+18.3+0.04 ms clock, 0.4+0/1/0+0.1 ms cpu, 19->20->13 MB, 24 MB goal, 1 MB stacks, 0 MB globals, 2 P"
SCHED = "SCHED 23100ms: gomaxprocs=2 idleprocs=1 threads=15 spinningthreads=0 needspinning=0 idlethreads=8 runqueue=3 [2 4]"


class RuntimeDiagnosticTest(unittest.TestCase):
    def test_gc_distinguishes_parallel_mark_from_stw(self):
        row = audit.parse_gc(GC)
        self.assertAlmostEqual(row["max_single_stw_ms"], 0.12)
        self.assertAlmostEqual(row["stw_total_ms"], 0.16)
        self.assertEqual(row["concurrent_mark_ms"], 18.3)
        self.assertEqual(row["trace_elapsed_seconds"], 23.1)
        self.assertFalse(row["forced"])
        self.assertTrue(audit.parse_gc(GC + " (forced)")["forced"])

    def test_scheduler_records_process_local_runnable(self):
        row = audit.parse_sched(SCHED)
        self.assertEqual(row["threads"], 15)
        self.assertEqual(row["local_runqueue_total"], 6)
        self.assertEqual(row["local_runqueue_max"], 4)

    def test_current_toolchain_schedticks_remains_separate_from_queue(self):
        row = audit.parse_sched(SCHED + " schedticks=[100 200]")
        self.assertEqual(row["scheduler_ticks"], [100, 200])
        self.assertEqual(row["local_runqueue_total"], 6)
        self.assertEqual(row["clock_origin"], "first_schedtrace")
        self.assertIsNone(audit.parse_sched(SCHED + " schedticks=[100]"))
        self.assertEqual(audit.parse_gc(GC)["clock_origin"], "runtime_start")

    def test_absent_trace_is_unknown_not_zero(self):
        row = audit.analyze_log("RustSwitch ready\n", tail_source=True)
        self.assertEqual(row["availability"], "not_observed")
        self.assertIsNone(row["max_observed_single_stw_ms"])
        self.assertIsNone(row["max_observed_threads"])
        self.assertFalse(row["complete_runtime_coverage_proven"])

    def test_broken_version_truncation_or_nonfinite_is_not_accepted(self):
        for value in [GC.replace("0.12+", "NaN+"), GC.replace("0.12+", "1e999+"),
                      "gc 8 @23s 0%: partial", SCHED.replace("threads=15", "threads=-1"),
                      SCHED.replace(" [2 4]", " [2]"),
                      SCHED.replace(" [2 4]", " unexpected=1 [2 4]")]:
            with self.subTest(line=value):
                row = audit.analyze_log(value, tail_source=True)
                self.assertEqual(row["availability"], "not_observed")
                self.assertEqual(row["unrecognized_trace_lines"], 1)

    def test_tail_capacity_does_not_claim_full_coverage(self):
        row = audit.analyze_log("x" * 65536 + "\n" + GC, tail_source=True)
        self.assertTrue(row["tail_at_capacity"])
        self.assertEqual(row["gc_lines"], 1)
        self.assertFalse(row["complete_runtime_coverage_proven"])

    def test_restart_or_mixed_process_clock_remains_visible(self):
        row = audit.analyze_log(GC + "\n" + GC.replace("@23.100s", "@1.100s"), tail_source=False)
        self.assertEqual(row["process_clock_rewinds"], 1)
        self.assertFalse(row["complete_runtime_coverage_proven"])

    def test_retained_events_are_bounded_without_losing_total(self):
        row = audit.analyze_log("\n".join([GC] * (audit.MAX_EVENTS + 3)), tail_source=False)
        self.assertEqual(row["gc_lines"], audit.MAX_EVENTS + 3)
        self.assertEqual(len(row["events_retained"]), audit.MAX_EVENTS)
        self.assertEqual(row["events_not_retained"], 3)

    def test_report_separates_processes_and_preserves_failure(self):
        report = {"result": {"passed": False}, "logs": {"generator_tail": GC, "controller_tail": SCHED}}
        original = copy.deepcopy(report)
        raw = json.dumps(report).encode()
        out = audit.analyze_report(report, raw)
        self.assertFalse(out["test_passed"])
        self.assertFalse(out["cross_process_clock_aligned"])
        self.assertEqual(out["cause"], "not_determined")
        self.assertEqual(out["report_sha256"], hashlib.sha256(raw).hexdigest())
        self.assertEqual(out["processes"]["generator"]["gc_lines"], 1)
        self.assertIsNone(out["processes"]["controller"]["max_observed_single_stw_ms"])
        self.assertEqual(report, original)

    def test_explicit_full_log_does_not_infer_missing_other_process(self):
        report = {"logs": {}}
        out = audit.analyze_report(report, b"{}", {"generator": GC.encode()})
        self.assertFalse(out["processes"]["generator"]["source_is_bounded_tail"])
        self.assertEqual(out["processes"]["controller"]["availability"], "not_observed")

    def test_cli_never_overwrites_old_observation_or_report(self):
        with tempfile.TemporaryDirectory() as directory:
            src = Path(directory) / "report.json"
            target = Path(directory) / "result.json"
            src.write_text(json.dumps({"logs": {}, "result": {"passed": False}}))
            command = [sys.executable, str(Path(audit.__file__)), "--report", str(src), "--output", str(target)]
            self.assertEqual(subprocess.run(command, capture_output=True).returncode, 0)
            before = target.read_bytes()
            self.assertEqual(subprocess.run(command, capture_output=True).returncode, 2)
            self.assertEqual(target.read_bytes(), before)


if __name__ == "__main__":
    unittest.main(verbosity=2)
