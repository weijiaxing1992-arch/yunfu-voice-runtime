#!/usr/bin/env python3
"""纯内存验证采样重试边界；不连接后台、不创建呼叫或终止进程。"""

import http.client
import unittest
import urllib.error
from unittest.mock import patch

import testlab_e2e as subject


class Clock:
    """用确定性单调时钟核对总期限，测试不真实等待。"""

    def __init__(self):
        self.now = 0.0

    def monotonic(self):
        return self.now

    def sleep(self, seconds):
        self.now += seconds


class FakeClient:
    """每次只返回预设对象或异常，记录预算与次数以检测无意重试。"""

    def __init__(self, clock, outcomes):
        self.clock = clock
        self.outcomes = list(outcomes)
        self.timeouts = []

    def get(self, path, *, timeout=5):
        self.timeouts.append(timeout)
        elapsed, result = self.outcomes.pop(0)
        self.clock.now += elapsed
        if isinstance(result, Exception):
            raise result
        return result


def task(status="completed", request_id="owned-request"):
    """最小真实归属对象；重试不降低终态或归属核验条件。"""
    return {"id": "owned-task", "request_id": request_id, "status": status}


class SamplingRetryTests(unittest.TestCase):
    """逐项验证可恢复网络失败和立即失败的合同错误。"""

    def run_wait(self, outcomes, timeout=30):
        clock = Clock()
        client = FakeClient(clock, outcomes)
        summary = {}
        acceptance = subject.Acceptance(client, summary, timeout)
        acceptance.owned["owned-task"] = "owned-request"
        return clock, client, summary, acceptance

    def waiting(self, clock, acceptance):
        with patch.object(subject.time, "monotonic", clock.monotonic), patch.object(subject.time, "sleep", clock.sleep):
            return acceptance.wait("owned-task")

    def test_one_interruption_recovers_without_stopping(self):
        clock, client, summary, acceptance = self.run_wait([(1.2, subject.ConnectivityFailure("timeout")), (.03, task())])
        self.assertEqual(self.waiting(clock, acceptance)["status"], "completed")
        self.assertEqual(len(client.timeouts), 2)
        self.assertEqual(summary["sampling_read_errors"], 1)
        self.assertEqual(summary["sampling_max_consecutive_read_errors"], 1)
        self.assertEqual(summary["sampling_read_error_categories"], {"timeout": 1})
        self.assertEqual(summary["sampling_max_read_seconds"], 1.2)
        self.assertEqual(summary["sampling_read_error_details"][0]["elapsed_seconds"], 1.2)

    def test_third_consecutive_interruption_fails_without_fourth_request(self):
        clock, client, summary, acceptance = self.run_wait([(1, subject.ConnectivityFailure("read"))] * 3 + [(0, task())])
        with self.assertRaises(subject.ConnectivityFailure):
            self.waiting(clock, acceptance)
        self.assertEqual(len(client.timeouts), 3)
        self.assertEqual(summary["sampling_read_errors"], 3)
        self.assertEqual(summary["sampling_max_consecutive_read_errors"], 3)

    def test_retry_does_not_reset_deadline_and_caps_remaining_budget(self):
        clock, client, summary, acceptance = self.run_wait([(1.4, subject.ConnectivityFailure("connect")), (.4, subject.ConnectivityFailure("timeout")), (0, task())], timeout=2)
        with self.assertRaisesRegex(subject.AcceptanceFailure, "终态超时"):
            self.waiting(clock, acceptance)
        self.assertEqual(len(client.timeouts), 2)
        self.assertAlmostEqual(client.timeouts[0], 2)
        self.assertAlmostEqual(client.timeouts[1], .4)
        self.assertAlmostEqual(clock.now, 2)
        self.assertEqual(summary["sampling_read_errors"], 2)

    def test_late_terminal_response_cannot_extend_deadline(self):
        clock, client, _, acceptance = self.run_wait([(2, task())], timeout=1)
        with self.assertRaisesRegex(subject.AcceptanceFailure, "终态超时"):
            self.waiting(clock, acceptance)
        self.assertEqual(client.timeouts, [1])

    def test_business_errors_and_ownership_mismatch_are_not_retried(self):
        for outcome in [subject.AcceptanceFailure("管理接口返回了无法解析的 JSON"), subject.AcceptanceFailure("只读管理请求预期 HTTP 200，实际为 503"), task(request_id="someone-else")]:
            with self.subTest(outcome=type(outcome).__name__):
                clock, client, summary, acceptance = self.run_wait([(0, outcome), (0, task())])
                with self.assertRaises(subject.AcceptanceFailure) as failure:
                    self.waiting(clock, acceptance)
                self.assertNotIsInstance(failure.exception, subject.ConnectivityFailure)
                self.assertEqual(len(client.timeouts), 1)
                self.assertEqual(summary["sampling_read_errors"], 0)

    def test_success_resets_consecutive_error_count(self):
        clock, client, summary, acceptance = self.run_wait([(0, subject.ConnectivityFailure("read")), (0, task("running")), (0, subject.ConnectivityFailure("connect")), (0, task())])
        self.assertEqual(self.waiting(clock, acceptance)["status"], "completed")
        self.assertEqual(len(client.timeouts), 4)
        self.assertEqual(summary["sampling_read_errors"], 2)
        self.assertEqual(summary["sampling_max_consecutive_read_errors"], 1)


class ClientClassificationTests(unittest.TestCase):
    """模拟 urllib 与 HTTP 流错误，确保只传播安全类别。"""

    def test_open_failures_are_safe_and_classified(self):
        cases = [(urllib.error.URLError(TimeoutError("private token")), "timeout"), (urllib.error.URLError(OSError("private url")), "connect"), (http.client.RemoteDisconnected("private body"), "read")]
        for error, category in cases:
            with self.subTest(category=category):
                client = subject.Client("http://127.0.0.1:1")
                with patch.object(client.opener, "open", side_effect=error) as opened:
                    with self.assertRaises(subject.ConnectivityFailure) as failed:
                        client.get("/v1/tests/owned-task", timeout=.25)
                self.assertEqual(failed.exception.category, category)
                self.assertNotIn("private", str(failed.exception))
                self.assertEqual(opened.call_args.kwargs["timeout"], .25)

    def test_body_interruption_and_invalid_json_are_distinct(self):
        class Response:
            code = 200

            def __init__(self, result):
                self.result = result

            def __enter__(self):
                return self

            def __exit__(self, *args):
                return False

            def read(self, size):
                if isinstance(self.result, Exception):
                    raise self.result
                return self.result

        for result, expected in [(OSError("private body"), subject.ConnectivityFailure), (b"{invalid", subject.AcceptanceFailure)]:
            client = subject.Client("http://127.0.0.1:1")
            with patch.object(client.opener, "open", return_value=Response(result)):
                with self.assertRaises(expected) as failed:
                    client.get("/v1/tests/owned-task")
            self.assertNotIn("private", str(failed.exception))
            if isinstance(result, Exception):
                self.assertEqual(failed.exception.category, "read")
            else:
                self.assertNotIsInstance(failed.exception, subject.ConnectivityFailure)


if __name__ == "__main__":
    unittest.main()
