#!/usr/bin/env python3
"""只读分析隔离压测的 Go GC/调度日志；没有采集到的字段保持未知，不归因媒体失败。

GODEBUG 日志格式来自实际构建使用的 Go runtime/extern.go、proc.go，格式可随版本改变。
该工具不启动进程、修改配置或补发测试。两个 Go 进程的启动时钟彼此独立。
"""
import argparse
from collections import deque
import hashlib
import json
import math
from pathlib import Path
import re

MAX_INPUT = 32 * 1024 * 1024
MAX_LINE = 8192
MAX_EVENTS = 2048
NUMBER = r"(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?"
GC = re.compile(r"^gc (\d+) @(" + NUMBER + r")s (" + NUMBER
                + r")%: (" + NUMBER + r")\+(" + NUMBER + r")\+(" + NUMBER
                + r") ms clock, .+ ms cpu, (\d+)->(\d+)->(\d+) MB, .+$")
SCHED = re.compile(r"^SCHED (\d+)ms: ([^\[]+) \[([\d ]*)\](?: schedticks=\[([\d ]*)\])?$")
FIELDS = ("gomaxprocs", "idleprocs", "threads", "spinningthreads",
          "needspinning", "idlethreads", "runqueue")


def read_bound(path):
    """限制输入尺寸，避免把不受控日志载入内存；返回原字节以保留来源 SHA。"""
    with path.open("rb") as stream:
        raw = stream.read(MAX_INPUT + 1)
    if len(raw) > MAX_INPUT:
        raise ValueError("诊断输入超过 32 MiB 上限")
    return raw


def parse_gc(line):
    """区分两段 STW 与并行标记，禁止把整个 GC 历时当成停止媒体的时间。"""
    match = GC.fullmatch(line)
    if not match:
        return None
    cycle, uptime, share, sweep, mark, termination, start, end, live = match.groups()
    values = [float(v) for v in (uptime, share, sweep, mark, termination)]
    if not all(math.isfinite(v) and v >= 0 for v in values):
        return None
    uptime, share, sweep, mark, termination = values
    return {"kind": "gc", "cycle": int(cycle), "trace_elapsed_seconds": uptime, "clock_origin": "runtime_start",
            "gc_percent_since_start": share, "stw_sweep_ms": sweep,
            "concurrent_mark_ms": mark, "stw_mark_ms": termination,
            "stw_total_ms": sweep + termination, "max_single_stw_ms": max(sweep, termination),
            "heap_start_mb": int(start), "heap_end_mb": int(end), "heap_live_mb": int(live),
            "forced": line.endswith("(forced)")}


def parse_sched(line):
    """只接受单行调度格式；未知版本、详细多行和重复字段不猜测。"""
    match = SCHED.fullmatch(line)
    if not match:
        return None
    uptime, raw_fields, queues, ticks = match.groups()
    tokens = raw_fields.split()
    pairs = [token.split("=", 1) for token in tokens]
    if any(len(pair) != 2 or not pair[1].isdigit() for pair in pairs):
        return None
    if len(pairs) != len(set(pair[0] for pair in pairs)):
        return None
    values = {key: int(value) for key, value in pairs}
    if set(values) != set(FIELDS):
        return None
    local = [int(value) for value in queues.split()]
    scheduler_ticks = None if ticks is None else [int(value) for value in ticks.split()]
    if len(local) != values["gomaxprocs"] or (scheduler_ticks is not None and len(scheduler_ticks) != len(local)):
        return None
    return {"kind": "scheduler", "trace_elapsed_seconds": int(uptime) / 1000, "clock_origin": "first_schedtrace",
            "scheduler_ticks": scheduler_ticks,
            **values, "local_runqueue_total": sum(local), "local_runqueue_max": max(local, default=0)}


def analyze_log(text, *, tail_source):
    """有界保留最近事件，同时统计全部已识别行；日志缺失不写成零暂停。"""
    events = deque(maxlen=MAX_EVENTS)
    totals = {"gc": 0, "scheduler": 0}
    rejected = 0
    last_times = {}
    clock_rewinds = 0
    max_stw = max_threads = max_queue = None
    for line in text.splitlines():
        is_trace = line.startswith("gc ") or line.startswith("SCHED ")
        if not is_trace:
            continue
        event = None if len(line) > MAX_LINE else (parse_gc(line) if line.startswith("gc ") else parse_sched(line))
        if event is None:
            rejected += 1
            continue
        kind, at = event["kind"], event["trace_elapsed_seconds"]
        if kind in last_times and at < last_times[kind]:
            clock_rewinds += 1
        last_times[kind] = at
        totals[kind] += 1
        if kind == "gc":
            max_stw = max(max_stw or 0, event["max_single_stw_ms"])
        else:
            max_threads = max(max_threads or 0, event["threads"])
            max_queue = max(max_queue or 0, event["runqueue"] + event["local_runqueue_total"])
        events.append(event)
    count = sum(totals.values())
    return {"availability": "observed" if count else "not_observed",
            "gc_lines": totals["gc"], "scheduler_lines": totals["scheduler"],
            "unrecognized_trace_lines": rejected, "process_clock_rewinds": clock_rewinds,
            "max_observed_single_stw_ms": max_stw, "max_observed_threads": max_threads,
            "max_observed_runnable": max_queue, "events_retained": list(events),
            "events_not_retained": max(0, count - MAX_EVENTS),
            "source_is_bounded_tail": tail_source,
            "tail_at_capacity": tail_source and len(text.encode("utf-8")) >= 65536,
            "complete_runtime_coverage_proven": False,
            "interpretation": "GC 从运行时启动计时，SCHED 从首次调度日志计时；同进程的两种原点也未自动校准。缺行、截尾、未开启日志或未知格式均不能解释为零 GC/调度停顿。"}


def analyze_report(report, raw, overrides=None):
    """原测试结论只读透出，诊断不能修改通过数量、原失败或端到端时限。"""
    logs = report.get("logs")
    if not isinstance(logs, dict):
        raise ValueError("压测报告缺少 logs 对象")
    overrides = overrides or {}
    process = {}
    for role in ("controller", "generator"):
        override = overrides.get(role)
        if override is None:
            value = logs.get(role + "_tail", "")
            if not isinstance(value, str):
                raise ValueError("日志不是文本")
            log_raw = value.encode("utf-8")
        else:
            log_raw = override
            value = log_raw.decode("utf-8", errors="strict")
        process[role] = {"log_sha256": hashlib.sha256(log_raw).hexdigest(),
                         **analyze_log(value, tail_source=override is None)}
    result = report.get("result")
    passed = result.get("passed") if isinstance(result, dict) else None
    return {"schema_version": "1.0.0", "report_sha256": hashlib.sha256(raw).hexdigest(),
            "test_passed": passed if type(passed) is bool else None,
            "scope": "只读 Go 运行时观测，不是测试通过证据、逐包关联或根因认证",
            "processes": process,
            "cross_process_clock_aligned": False,
            "cause": "not_determined"}


def main():
    """输入日志可以显式补充，输出使用独占创建，禁止覆盖旧诊断或原测试报告。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", required=True, type=Path)
    parser.add_argument("--controller-log", type=Path)
    parser.add_argument("--generator-log", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        raw = read_bound(args.report)
        report = json.loads(raw)
        if not isinstance(report, dict):
            raise ValueError("压测报告不是 JSON 对象")
        overrides = {role: read_bound(path) for role, path in
                     (("controller", args.controller_log), ("generator", args.generator_log)) if path}
        result = analyze_report(report, raw, overrides)
        with args.output.open("x", encoding="utf-8") as out:
            json.dump(result, out, ensure_ascii=False, indent=2, allow_nan=False)
            out.write("\n")
    except (OSError, ValueError, TypeError) as error:
        parser.exit(2, "诊断未完成：" + str(error) + "\n")
    print(json.dumps({"output": str(args.output), "scope": result["scope"],
                      "processes": {key: value["availability"] for key, value in result["processes"].items()}},
                     ensure_ascii=False))


if __name__ == "__main__":
    main()
