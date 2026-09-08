"""执行本目录离线验收并写不可覆盖的新收据；禁止测试创建网络或子进程。"""
from datetime import datetime, timezone
import hashlib
import io
import json
from pathlib import Path
import os
import sys
import time
import unittest

BASE = Path(__file__).resolve().parent
DENIED = []


def audit(event, arguments):
    # 用 CPython 审计点覆盖 socket 构造、连接、DNS 与进程启动；本轮不使用任何监听器。
    if event.startswith('socket.') or event in ('subprocess.Popen', 'os.system', 'os.posix_spawn', 'os.fork', 'os.exec'):
        DENIED.append(event)
        raise RuntimeError('离线夹具禁止网络或进程操作: ' + event)


def hashes():
    return {path.name: hashlib.sha256(path.read_bytes()).hexdigest()
            for path in sorted(BASE.iterdir()) if path.is_file() and path.suffix in ('.py', '.md', '.json')}


class RecordedResult(unittest.TextTestResult):
    def __init__(self, *arguments, **kwargs):
        super().__init__(*arguments, **kwargs)
        self.records = []

    def addSuccess(self, test):
        super().addSuccess(test)
        self.records.append({'id': test.id(), 'status': 'passed'})

    def addFailure(self, test, error):
        super().addFailure(test, error)
        self.records.append({'id': test.id(), 'status': 'failed'})

    def addError(self, test, error):
        super().addError(test, error)
        self.records.append({'id': test.id(), 'status': 'error'})

    def addSkip(self, test, reason):
        super().addSkip(test, reason)
        self.records.append({'id': test.id(), 'status': 'skipped', 'reason': reason})


def main():
    if len(sys.argv) != 2 or not sys.argv[1].startswith('selftest-') or '/' in sys.argv[1] or '\\' in sys.argv[1]:
        raise SystemExit('用法: python3 -B run_selftest.py selftest-新的编号')
    destination = BASE / sys.argv[1]
    destination.mkdir()  # 不覆盖旧的失败或通过原件。
    before = hashes()
    started = datetime.now(timezone.utc).isoformat()
    begin = time.monotonic()
    sys.dont_write_bytecode = True
    sys.addaudithook(audit)
    suite = unittest.defaultTestLoader.discover(str(BASE), pattern='test_asr1_fixture.py')
    log = io.StringIO()
    result = unittest.TextTestRunner(stream=log, verbosity=2, resultclass=RecordedResult).run(suite)
    after = hashes()
    (destination / 'unittest.log').write_text(log.getvalue(), encoding='utf-8')
    receipt = {'schema': 'asr1-offline-design-selftest-v1', 'scope': 'draft_protocol_model_only',
               'started_at': started, 'finished_at': datetime.now(timezone.utc).isoformat(),
               'duration_seconds': time.monotonic() - begin, 'python': sys.version, 'platform': list(os.uname()),
               'command': ['python3', '-B', 'run_selftest.py', sys.argv[1]],
               'tests_run': result.testsRun, 'failures': len(result.failures), 'errors': len(result.errors),
               'skips': len(result.skipped), 'cases': result.records,
               'network_or_subprocess_executed': False, 'denied_audit_events': DENIED,
               'source_before': before, 'source_after': after, 'source_stable': before == after,
               'passed': result.wasSuccessful() and not result.skipped and not DENIED and before == after,
               'not_verified': ['actual_unix_connection', 'real_sip_rust_rx_adapter', 'vendor_recognition',
                                '16k_fir', 'real_clock_or_scheduler', 'real_resource_limits', 'capacity_5000_10000'],
               'log_sha256': hashlib.sha256(log.getvalue().encode()).hexdigest()}
    (destination / 'receipt.json').write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(log.getvalue(), end='')
    print(json.dumps({'receipt': str(destination / 'receipt.json'), 'passed': receipt['passed'], 'tests_run': result.testsRun}))
    return 0 if receipt['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
