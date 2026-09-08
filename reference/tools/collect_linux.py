#!/usr/bin/env python3
"""只读采集 Linux 主机和 RustSwitch 状态，逐条写入结果以限制内存占用。

该工具不启动压测、不修改系统参数，也不单独给出容量或零丢包验收结论。
"""
import argparse
import datetime
import json
import os
from pathlib import Path
import platform
import re
import time
import urllib.parse
import urllib.request


# 读取一个内核文本节点；读取或解析异常由采样层记录，不把失败伪装成零值。
def read_text(path):
    return Path(path).read_text().strip()


# /proc/net/snmp 按“字段名行/数值行”成对排列；仅组合协议前缀一致的一对。
# 字段数量和内核支持项可能不同，返回实际可配对的字段，不假定固定协议清单。
def protocol_counters(text):
    lines = text.splitlines()
    result = {}
    for first, second in zip(lines[::2], lines[1::2]):
        keys, values = first.split(), second.split()
        if keys and values and keys[0] == values[0]:
            result[keys[0].rstrip(":")] = dict(zip(keys[1:], map(int, values[1:])))
    return result


# 校验采样范围和本机读取目标，流式记录样本，最后计算网卡首尾差值。
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--interface", action="append", required=True)
    parser.add_argument("--admin", default="http://127.0.0.1:9080")
    parser.add_argument("--seconds", type=int, default=60)
    parser.add_argument("--interval", type=float, default=1)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if platform.system() != "Linux":
        parser.error("this collector requires Linux /proc and /sys; macOS results are not substituted")
    if not 1 <= args.seconds <= 259200 or not .1 <= args.interval <= 60:
        parser.error("invalid sampling limits")
    address = urllib.parse.urlparse(args.admin)
    # 管理读取限定本机 HTTP，不把此脚本变成携带凭据访问任意远端的工具。
    if address.scheme != "http" or address.hostname not in ("127.0.0.1", "localhost") or address.username or address.password:
        parser.error("admin endpoint must be local HTTP without embedded credentials")
    for name in args.interface:
        # 名称先限定字符再验证 sysfs 目录，避免用户输入逃出网卡统计目录。
        if not re.fullmatch(r"[a-zA-Z0-9_.:-]{1,64}", name) or not (Path('/sys/class/net') / name).is_dir():
            parser.error("invalid interface: " + name)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    first = last = None
    errors = 0
    start = time.monotonic()
    deadline = start + args.seconds
    # 排他创建结果文件，防止覆盖已有压测证据；每个样本立即刷入文件流。
    with args.output.open("x") as output:
        # 写出独立 JSON 行；flush 保证 Python 缓冲及时提交，不等于执行磁盘 fsync。
        def emit(value):
            output.write(json.dumps(value, separators=(",", ":")) + "\n")
            output.flush()
        emit({"type": "metadata", "kernel": platform.release(), "cpu_count": os.cpu_count(),
              "allowed_cpus": sorted(os.sched_getaffinity(0)), "interfaces": args.interface,
              "note": "host counters include other processes; NIC counters do not prove application packet delivery"})
        while True:
            # UTC 用于跨日志关联，monotonic 用于本次采样间隔，避免系统校时改变持续时间。
            sample = {"type": "sample", "utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                      "elapsed_seconds": time.monotonic()-start, "errors": []}
            try:
                sample["protocols"] = protocol_counters(read_text('/proc/net/snmp'))
                sample["cpu_jiffies"] = [list(map(int, line.split()[1:])) for line in read_text('/proc/stat').splitlines() if re.match(r'^cpu\d+ ', line)]
                sample["interfaces"] = {}
                for name in args.interface:
                    base = Path('/sys/class/net') / name / 'statistics'
                    sample["interfaces"][name] = {key: int(read_text(base / key)) for key in (
                        'rx_packets', 'tx_packets', 'rx_bytes', 'tx_bytes', 'rx_dropped', 'tx_dropped', 'rx_errors', 'tx_errors', 'rx_missed_errors')}
            except (OSError, ValueError) as error:
                # 内核统计读取异常与管理 API 异常分别记录，保留其余仍可采集的证据。
                sample["errors"].append(str(error))
            try:
                with urllib.request.urlopen(args.admin.rstrip('/')+'/v1/status', timeout=1) as response:
                    sample["server"] = json.load(response)
            except Exception as error:
                sample["errors"].append("admin: " + str(error))
            errors += len(sample["errors"])
            emit(sample)
            # 内存只保留首尾样本；全量时间序列已逐条落在输出文件中。
            if first is None: first = sample
            last = sample
            if time.monotonic() >= deadline: break
            time.sleep(min(args.interval, max(0, deadline-time.monotonic())))
        deltas = {}
        # 只计算首尾均存在的网卡字段；计数重置或网卡重建可能产生负差值，需要人工关联。
        for name, counters in last.get("interfaces", {}).items():
            if name in first.get("interfaces", {}):
                deltas[name] = {key: value-first["interfaces"][name][key] for key, value in counters.items()}
        emit({"type": "summary", "interface_deltas": deltas, "collection_errors": errors,
              "capacity_verdict": "not evaluated; correlate with generator report and actual concurrent calls"})
    print(str(args.output.resolve()))

# 只有直接执行脚本时采集；导入模块可单独测试解析器而不访问系统状态。
if __name__ == '__main__':
    main()
