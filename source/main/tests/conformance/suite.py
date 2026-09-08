#!/usr/bin/env python3
"""原始条目可追溯的成对协议探针；有限探针通过不提升整条 FreeSWITCH 兼容状态。"""
from __future__ import annotations

import base64
import hashlib
import ipaddress
import json
import os
import re
import socket
import ssl
import time
import uuid
import xml.etree.ElementTree as ET
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import unquote

REFERENCE_VERSION = "1.11.3"
REFERENCE_COMMIT = "ef32e205295e29f034f1453ad245ba5efb07b94a"
ECHO_TEXT = "兼容测试\u2603"
UNKNOWN_COMMAND = "__rustswitch_conformance_unknown__"
JOB_UUID = "e58fd180-c621-41c4-bf1d-ad988c509301"
# 每个关联只是原条目的一个子场景，不能宣称原条目完整断言已覆盖。
PROBES = [
    {"id": "identity.esl", "adapter": "esl", "scope": "读取两端真实版本并核实 reference 发行版本；不比较版本字符串", "links": ["key-esl-api"], "identity_only": True},
    {"id": "esl.auth.fragmented", "adapter": "esl", "scope": "认证请求逐字节发送、成功身份和固定 echo 正文", "links": ["key-esl-framing", "acceptance:ESL-T01"]},
    {"id": "esl.auth.wrong", "adapter": "esl", "scope": "一个错误密码的回复及随后关闭；不包含 ACL、锁定、空密码、认证超时", "links": ["key-esl-framing", "acceptance:ESL-T02"], "isolated": True},
    {"id": "esl.auth.required", "adapter": "esl", "scope": "认证前 api echo 被拒绝且不获取身份，同连接随后有效认证和 echo 成功", "links": ["key-esl-api", "acceptance:ESL-T02"], "isolated": True},
    {"id": "esl.echo", "adapter": "esl", "scope": "api echo 固定 UTF-8 正文、Content-Length 字节数", "links": ["key-esl-api", "key-esl-framing", "acceptance:ESL-T09"]},
    {"id": "esl.console.single", "adapter": "esl", "scope": "fs_cli -x 的 console_execute 单命令包装：UTF-8、空 echo、首尾空格、单分号和 false 字面参数；不代表别名、批处理或交互客户端", "links": ["key-esl-api", "key-cli-fs-cli", "acceptance:ESL-T10"]},
    {"id": "esl.api.unknown", "adapter": "esl", "scope": "单个固定未知 API 命令的错误响应", "links": ["key-esl-api", "acceptance:ESL-T09"]},
    {"id": "esl.frames.pipeline", "adapter": "esl", "scope": "一次写入两条 echo；逐帧读取并核对两个正文；不代表事件洪峰", "links": ["key-esl-framing", "acceptance:ESL-T05"]},
    {"id": "esl.frames.crlf", "adapter": "esl", "scope": "CRLF 命令分隔的认证和 echo；不代表所有头部/长度边界", "links": ["key-esl-framing", "acceptance:ESL-T05"]},
    {"id": "esl.frames.split_positions", "adapter": "esl", "scope": "固定 echo 请求在每个字节边界拆成两次发送，每次独立连接", "links": ["key-esl-framing", "acceptance:ESL-T05"]},
    {"id": "esl.bgapi.echo", "adapter": "esl", "scope": "固定 Job-UUID、bgapi echo 回复及 BACKGROUND_JOB 结果；不包含并发作业/重连重放", "links": ["key-esl-bgapi", "acceptance:ESL-T11", "event:BACKGROUND_JOB"], "isolated": True},
    {"id": "identity.api_inventory", "adapter": "esl", "scope": "真实 show api as json 注册目录；逐个对照全部 fs_api 原始 ID，接口存在不表示语义通过", "links": ["key-esl-api"], "identity_only": True},
    {"id": "esl.api.echo_empty", "adapter": "esl", "scope": "echo 无参数、尾空格、首尾空格的完整原始响应；不预设空白归一化", "links": ["key-esl-api", "acceptance:ESL-T09"]},
    {"id": "esl.api.create_uuid", "adapter": "esl", "scope": "连续两次生成合法且不同 UUID，保留原字节；仅随机 UUID 值可规范化", "links": ["key-esl-api", "acceptance:ESL-T09"]},
    {"id": "esl.api.uuid_exists_missing", "adapter": "esl", "scope": "固定 nil UUID 实际不存在时返回 false；不代表存在通道完整契约", "links": ["key-esl-api", "acceptance:ESL-T09"]},
    {"id": "esl.api.uuid_getvar_missing", "adapter": "esl", "scope": "先核实固定 nil UUID 不存在，再读取变量并比较完整错误", "links": ["key-esl-api", "acceptance:ESL-T09"]},
    {"id": "esl.api.uuid_setvar_missing", "adapter": "esl", "scope": "仅隔离 fixture，确认 nil UUID 不存在后尝试设置并比较错误；不修改真实通道", "links": ["key-esl-api", "acceptance:ESL-T09"], "isolated": True},
    {"id": "esl.api.uuid_kill_missing", "adapter": "esl", "scope": "仅隔离 fixture，确认 nil UUID 不存在后调用并比较错误；不挂断真实通道", "links": ["key-esl-api", "acceptance:ESL-T09"], "isolated": True},
    {"id": "esl.api.uuid_syntax", "adapter": "esl", "scope": "uuid_exists/getvar/setvar/kill 缺参数的真实错误/usage；不执行有效通道动作", "links": ["key-esl-api", "acceptance:ESL-T09"], "isolated": True},
    {"id": "esl.command.case", "adapter": "esl", "scope": "混合大小写 AUTH/API/EVENT、格式沿用及 nixevent/noevents 开关", "links": ["key-esl-framing", "key-esl-subscriptions"], "isolated": True},
    {"id": "esl.filter.lifecycle", "adapter": "esl", "scope": "filter delete all、空值清除、noevents/nixevent；以真实后台事件交付证明过滤已清除", "links": ["key-esl-subscriptions"], "isolated": True},
    {"id": "esl.bgapi.echo.json", "adapter": "esl", "scope": "JSON BACKGROUND_JOB、固定 UUID、原始正文与内层字节长度", "links": ["key-esl-bgapi", "acceptance:ESL-T11"], "isolated": True},
    {"id": "esl.bgapi.echo.xml", "adapter": "esl", "scope": "XML BACKGROUND_JOB、固定 UUID、headers/root-body 结构、原始正文与内层字节长度", "links": ["key-esl-bgapi", "acceptance:ESL-T11"], "isolated": True},
    {"id": "sip.options.udp", "adapter": "sip_udp", "scope": "一个 OPTIONS 事务、状态码/能力头和回显事务标识；不代表完成呼叫", "links": ["key-sip-transports"]},
    {"id": "sip.options.tcp", "adapter": "sip_tcp", "scope": "TCP 连接上一个 OPTIONS 请求及完整响应；不代表 TCP 呼叫/复用/背压", "links": ["key-sip-transports"]},
    {"id": "sip.options.tls", "adapter": "sip_tls", "scope": "验证证书的 TLS 连接上一个 OPTIONS；不允许跳过证书验证", "links": ["key-sip-transports"]},
]


class ProbeError(Exception):
    """输入、断帧、限额或协议断言不满足；必须作为失败而非通过记录。"""


class Unavailable(ProbeError):
    """本项缺端点、凭证或隔离前提；保留阻塞项而非悄悄跳过。"""


def stamp():
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def file_digest(path):
    """只读取明确指定的单个产物，分块计算，报告不泄露配置或凭证内容。"""
    h = hashlib.sha256()
    with Path(path).open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def source_fingerprint(root):
    """冻结执行器和产品源码；文档生成物不纳入，避免报告递归哈希。"""
    files = {}
    # 固定依赖的全部vendor文件以及本地编译输入共同绑定，不能漏掉共享时钟的汇编实现。
    go_inputs = {'.go', '.mod', '.sum', '.s', '.S', '.c', '.h', '.cc', '.cpp', '.cxx', '.m', '.mm', '.f', '.F', '.f90', '.for', '.syso', '.swig', '.swigcxx'}
    for folder, extensions in [("control", go_inputs), ("media", {".rs", ".toml", ".lock", ".h", ".c", ".cpp"}), ("tests/conformance", {".py", ".json"})]:
        for path in sorted((root / folder).rglob("*")):
            vendor = folder == 'control' and path.is_relative_to(root / 'control/vendor')
            if path.is_file() and (path.suffix in extensions or vendor) and not any(x in path.parts for x in ("target", "doc_assets", "web", "__pycache__")) and path.name != "plan.json":
                files[str(path.relative_to(root))] = file_digest(path)
    for relative in ["tools/run_conformance.py", "tools/test_conformance.py"]:
        path = root / relative
        if path.is_file():
            files[relative] = file_digest(path)
    return {"sha256": digest(canonical(files)), "files": files}


def probe_api_name(probe_id):
    """关联实际命令的子探针到所有同名源注册条目，不能按名字删除重复声明 ID。"""
    direct = {"esl.echo": "echo", "esl.console.single": "echo", "esl.api.echo_empty": "echo", "esl.api.create_uuid": "create_uuid", "identity.api_inventory": "show", "identity.esl": "version"}
    if probe_id in direct:
        return direct[probe_id]
    if probe_id.startswith("esl.api.uuid_") and probe_id.endswith("_missing"):
        return probe_id.removeprefix("esl.api.").removesuffix("_missing")
    return None


def parse_api_inventory(frame):
    """读取真正 show API JSON；行数/字段/重复项异常不能被降级为空目录。"""
    if frame["headers"].get("content-type") != "api/response":
        raise ProbeError("API 注册目录响应类别错误")
    value = json.loads(frame["body"])
    if not isinstance(value, dict) or not isinstance(value.get("rows"), list) or type(value.get("row_count")) is not int or value["row_count"] != len(value["rows"]) or len(value["rows"]) > 20000:
        raise ProbeError("API 目录行数或结构不合法")
    names = set()
    for row in value["rows"]:
        if not isinstance(row, dict) or any(not isinstance(row.get(key), str) for key in ("name", "description", "syntax", "ikey")) or not row["name"] or row["name"] in names:
            raise ProbeError("API 目录包含缺字段、空名称或重复注册")
        names.add(row["name"])
    return value


def compare_api_inventory(plan, record):
    """接口注册只证明当前实例提供入口，不能升级为语义、模块宿主或完整兼容通过。"""
    observations = record.get("results", {})
    available = {side: result.get("state") == "observed" and isinstance(result.get("value", {}).get("inventory"), dict) for side, result in observations.items()}
    rows = {side: result["value"]["inventory"]["rows"] if available.get(side) else [] for side, result in observations.items()}
    mappings = {side: {row["name"]: row for row in items} for side, items in rows.items()}
    original = [entry for entry in plan["entries"] if entry["category"] == "fs_api"]
    entries = []
    for entry in original:
        reference = mappings.get("reference", {}).get(entry["name"])
        candidate = mappings.get("candidate", {}).get(entry["name"])
        if not available.get("reference"):
            state = "blocked_reference_inventory"
        elif not reference:
            state = "blocked_reference_module"
        elif not available.get("candidate"):
            state = "blocked_candidate_inventory"
        elif not candidate:
            state = "missing_candidate_entrypoint"
        else:
            state = "requires_semantic_verification"
        entries.append({"id": entry["id"], "name": entry["name"], "source_module": entry["module"], "state": state, "reference_registration": reference, "candidate_registration": candidate, "complete_contract_passed": False, "provider_identity_policy": "源码模块与实际 ikey 同时保留；core 包装或同名覆盖须单独核对，名字存在不等于原模块可加载", "next_step": "补齐原版实际模块构建或候选入口，再为本原始 ID 实例化正常/错误/边界/权限/并发/副作用断言"})
    known = {entry["name"] for entry in original}
    return {"evidence_type": "live_registration_discovery_only", "reference_observed": available.get("reference", False), "candidate_observed": available.get("candidate", False), "source_registration_entries": len(entries), "reference_runtime_rows": len(rows.get("reference", [])), "candidate_runtime_rows": len(rows.get("candidate", [])), "counts_by_state": dict(sorted(Counter(entry["state"] for entry in entries).items())), "entries": entries, "extra_runtime_names": {side: sorted(set(mapping) - known) for side, mapping in mappings.items()}, "complete_contract_passed": False}


def build_plan(root):
    """为每个原始 ID 建立必须继续实例化的计划；绝不凭清单自动产生可执行用例。"""
    comparison_path = root / "docs/api/comparison.json"
    ledger_path = root / "docs/api/verification-ledger.json"
    audit_path = root / "docs/api/feature-audit.json"
    cases_path = root / "docs/freeswitch-compatibility/catalog/conformance-cases.json"
    matrix_path = root / "tests/conformance/protocols.json"
    comparison = json.loads(comparison_path.read_text())
    ledger = json.loads(ledger_path.read_text())
    audit = json.loads(audit_path.read_text())
    definitions = json.loads(cases_path.read_text())["case_definitions"]
    ledger_map = {entry["id"]: entry for entry in ledger["entries"]}
    original_ids = [entry["id"] for entry in comparison["entries"]]
    if len(original_ids) != len(set(original_ids)) or set(original_ids) != set(ledger_map):
        raise ProbeError("comparison 与 ledger 的原始 ID 重复或不一致；不得缩小分母")
    if comparison["reference_version"] != REFERENCE_VERSION or comparison["reference_commit"] != REFERENCE_COMMIT:
        raise ProbeError("原始目录版本偏离固定基线")
    defined = {"acceptance:" + case["id"] for case in definitions}
    if defined != {entry["id"] for entry in comparison["entries"] if entry["category"] == "acceptance_definition"}:
        raise ProbeError("验收定义与 comparison 条目不一致")
    probes = {entry["id"]: entry for entry in PROBES}
    entries = []
    for original in comparison["entries"]:
        row = ledger_map[original["id"]]
        links = [probe["id"] for probe in PROBES if original["id"] in probe["links"]]
        if original["category"] == "fs_api":
            links += [probe["id"] for probe in PROBES if probe_api_name(probe["id"]) == original["name"]]
        missing = original["status"] in ("not_implemented", "export_only", "internal_only")
        entries.append({
            "id": original["id"], "name": original["name"], "category": original["category"], "module": original["module"],
            "domain": row["domain"], "original_sha256": digest(canonical(original)),
            "implementation_status": original["status"], "source_reference": row["source_reference"],
            "state": "blocked_implementation_or_entrypoint" if missing else "not_run_full_contract",
            "probe_ids": links, "probe_coverage": "partial_branch_only" if links else "no_executable_adapter",
            "required_assertion_groups": ["原入口与输入格式", "正常结果及字节/字段契约", "未知/畸形/边界/权限", "并发/背压/重传/乱序", "超时/断连/取消/恢复", "真实业务副作用和媒体", "资源回收与容量退化"],
            "fixture_todo": "按本条原入口实例化双方构建/模块/配置、合法与非法输入、客户端、可观察结果和清理断言；不允许通用模板代替实际输入",
            "complete_contract_covered": False, "complete_contract_passed": False,
            "next_step": row["required_next_step"],
        })
    protocols = json.loads(matrix_path.read_text())["boundaries"]
    domain_ids = {entry["id"] for entry in audit["business_domains"]}
    covered_domains = {domain for boundary in protocols for domain in boundary["domains"]}
    if domain_ids - covered_domains:
        raise ProbeError("协议/外部边界遗漏业务域：" + ",".join(sorted(domain_ids - covered_domains)))
    return {
        "schema_version": "1.0.0", "reference_version": REFERENCE_VERSION, "reference_commit": REFERENCE_COMMIT,
        "meaning": "完整原始 ID 追踪计划 + 有限真实探针；计划定义、执行器自测、原版单端执行均不等于产品成对兼容通过。",
        "inputs": {str(path.relative_to(root)): file_digest(path) for path in [comparison_path, ledger_path, audit_path, cases_path, matrix_path]},
        "counts": {"original_entries": len(entries), "acceptance_definitions": len(definitions), "business_domains": len(audit["business_domains"]), "communication_and_external_boundaries": len(protocols), "executable_probes": len(probes), "complete_contract_covered": 0},
        "entries": entries, "acceptance_definitions": definitions,
        "business_domains": [{"id": d["id"], "name": d["name"], "implementation_status": d["status"], "paired_acceptance": d["paired_acceptance"], "complete_contract_passed": False} for d in audit["business_domains"]],
        "protocol_boundaries": [dict(p, state="limited_probe_available" if p.get("adapter") else "blocked_missing_adapter_and_full_fixture", complete_contract_passed=False) for p in protocols],
        "probes": PROBES,
    }


def validate_profile(profile):
    """只接收受限协议连接参数；无任意命令、动态导入或 shell。"""
    if profile.get("schema_version") != "1.0.0" or profile.get("reference") != {"version": REFERENCE_VERSION, "commit": REFERENCE_COMMIT}:
        raise ProbeError("profile 的 reference 必须精确冻结 FreeSWITCH 版本与提交")
    if not isinstance(profile.get("fixture"), dict) or not isinstance(profile["fixture"].get("isolated"), bool):
        raise ProbeError("fixture.isolated 必须明确为布尔值")
    limits = {"connect_seconds": 2, "probe_seconds": 8, "max_header_bytes": 65536, "max_body_bytes": 1048576, "max_evidence_bytes": 2097152}
    supplied = profile.get("limits", {})
    if not isinstance(supplied, dict) or set(supplied) - set(limits):
        raise ProbeError("未知 limits 参数")
    limits.update(supplied)
    for name, value in limits.items():
        upper = 60 if name.endswith("seconds") else 16 * 1024 * 1024
        if isinstance(value, bool) or not isinstance(value, (int, float)) or value <= 0 or value > upper or (not name.endswith("seconds") and not isinstance(value, int)):
            raise ProbeError("限额非法：" + name)
    endpoints = profile.get("endpoints", {})
    if not isinstance(endpoints, dict) or set(endpoints) != {"reference", "candidate"}:
        raise ProbeError("必须分别指定 reference/candidate 端点，缺协议可省略该子端点")
    for endpoint in endpoints.values():
        if not isinstance(endpoint, dict):
            raise ProbeError("endpoint 必须为对象")
        for name in ("esl", "sip_udp", "sip_tcp", "sip_tls"):
            target = endpoint.get(name)
            if target is None:
                continue
            if not isinstance(target, dict) or not isinstance(target.get("host"), str) or not target["host"] or any(c in target["host"] for c in "\r\n\x00"):
                raise ProbeError("host 非法")
            port = target.get("port")
            if isinstance(port, bool) or not isinstance(port, int) or not 1 <= port <= 65535:
                raise ProbeError("port 非法")
            try:
                local = ipaddress.ip_address(target["host"]).is_loopback
            except ValueError:
                # 只允许数字地址或系统回环名，避免不可控 DNS 查询突破探针总时限。
                if target["host"] != "localhost":
                    raise ProbeError("测试 host 必须是数字 IP 或 localhost；TLS 证书名使用 server_name")
                local = True
            if not local and not (profile["fixture"]["isolated"] and profile["fixture"].get("allow_remote") is True):
                raise ProbeError("远端只允许显式隔离 profile 的 allow_remote=true")
            if "password" in target or "insecure" in target:
                raise ProbeError("禁止明文密码配置或跳过 TLS 校验")
            if name == "esl" and not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,127}", target.get("password_env", "")):
                raise ProbeError("ESL password_env 必须是环境变量名称")
    return limits


class Wire:
    """单个探针的有界原始报文记录；认证密码只保留长度和摘要。"""
    def __init__(self, maximum):
        self.maximum = maximum
        self.used = 0
        self.records = []

    def add(self, direction, data, secret=False):
        self.used += len(data)
        if self.used > self.maximum:
            raise ProbeError("原始报文证据超出限额，不能截断后判通过")
        item = {"direction": direction, "length": len(data), "sha256": digest(data)}
        if secret:
            item["redacted"] = "ESL 认证秘密；不保留可恢复密码"
        else:
            item["base64"] = base64.b64encode(data).decode()
        self.records.append(item)


class FrameReader:
    """TCP 累积缓冲；按字节 Content-Length 分帧，保留管线中的下一帧。"""
    def __init__(self, stream, deadline, limits, wire):
        self.stream = stream
        self.deadline = deadline
        self.limits = limits
        self.wire = wire
        self.buffer = bytearray()

    def remaining(self):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("探针达到总时限")
        return remaining

    def receive(self):
        self.stream.settimeout(self.remaining())
        data = self.stream.recv(16384)
        if not data:
            raise EOFError("在完整帧或预期结果前连接关闭")
        self.wire.add("receive", data)
        self.buffer.extend(data)

    def frame(self):
        while True:
            endings = [(at, size) for marker, size in ((b"\n\n", 2), (b"\r\n\r\n", 4)) if (at := self.buffer.find(marker)) >= 0]
            if endings:
                at, size = min(endings)
                if at > self.limits["max_header_bytes"]:
                    raise ProbeError("帧头超过上限")
                break
            if len(self.buffer) > self.limits["max_header_bytes"]:
                raise ProbeError("未结束的帧头超过上限")
            self.receive()
        header = bytes(self.buffer[:at])
        headers = parse_headers(header)
        length_raw = headers.get("content-length", "0")
        if not re.fullmatch(r"[0-9]{1,10}", length_raw):
            raise ProbeError("Content-Length 不是非负十进制字节数")
        length = int(length_raw)
        if length > self.limits["max_body_bytes"]:
            raise ProbeError("帧正文超过上限")
        end = at + size + length
        while len(self.buffer) < end:
            self.receive()
        body = bytes(self.buffer[at + size:end])
        del self.buffer[:end]
        return {"headers": headers, "body": body}


def parse_headers(raw):
    headers = {}
    for line in raw.replace(b"\r\n", b"\n").split(b"\n"):
        if not line or b":" not in line:
            raise ProbeError("缺少有效 header 名称/冒号")
        key_raw, value_raw = line.split(b":", 1)
        try:
            key = key_raw.decode("ascii").lower()
            value = value_raw.strip(b" \t").decode("utf-8")
        except UnicodeDecodeError as error:
            raise ProbeError("帧头编码非法") from error
        if not re.fullmatch(r"[a-z0-9_-]+", key) or key in headers:
            raise ProbeError("重复或非法帧头：" + key)
        headers[key] = value
    return headers


def comparable_frame(frame):
    """帧头大小写/排列无语义差异；正文逐字节比较，不修剪空白或错误文本。"""
    return {"headers": frame["headers"], "body_base64": base64.b64encode(frame["body"]).decode()}


class ESL:
    """每个子场景独立连接，退出时无条件释放 socket。"""
    def __init__(self, target, limits, wire, deadline):
        self.target = target
        self.limits = limits
        self.wire = wire
        self.deadline = deadline
        self.socket = None
        self.reader = None

    def __enter__(self):
        remaining = min(self.limits["connect_seconds"], self.deadline - time.monotonic())
        if remaining <= 0:
            raise TimeoutError("连接前探针总时限已到")
        self.socket = socket.create_connection((self.target["host"], self.target["port"]), remaining)
        self.reader = FrameReader(self.socket, self.deadline, self.limits, self.wire)
        try:
            greeting = self.reader.frame()
            if greeting["headers"].get("content-type") != "auth/request" or greeting["body"]:
                raise ProbeError("ESL 未返回 auth/request")
            self.greeting = comparable_frame(greeting)
            return self
        except BaseException:
            self.socket.close()
            raise

    def __exit__(self, *_):
        self.socket.close()

    def send(self, data, fragment=False, secret=False):
        self.wire.add("send", data, secret=secret)
        for chunk in ([bytes([byte]) for byte in data] if fragment else [data]):
            self.socket.settimeout(self.reader.remaining())
            self.socket.sendall(chunk)

    def auth(self, password, separator=b"\n\n", fragment=False):
        self.send(b"auth " + password.encode() + separator, fragment=fragment, secret=True)
        frame = self.reader.frame()
        if frame["headers"].get("content-type") != "command/reply" or frame["headers"].get("reply-text") != "+OK accepted":
            raise ProbeError("ESL 身份认证未成功")
        return comparable_frame(frame)

    def request(self, data):
        self.send(data)
        return self.reader.frame()


def endpoint_password(target):
    password = os.environ.get(target["password_env"])
    if not password:
        raise Unavailable("缺少专用 ESL password_env 指向的环境变量")
    if len(password.encode()) > 1024 or any(char in password for char in "\r\n\x00"):
        raise ProbeError("认证凭证含控制字符或过长")
    return password


def require_echo(frame, text):
    if frame["headers"].get("content-type") != "api/response" or frame["body"] != text.encode():
        raise ProbeError("api echo 不是预期 Content-Type/完整 UTF-8 正文")
    if frame["headers"].get("content-length") != str(len(text.encode())):
        raise ProbeError("api echo Content-Length 不等于实际 UTF-8 字节数")
    return comparable_frame(frame)


def run_esl(probe, target, limits, wire):
    password = endpoint_password(target)
    deadline = time.monotonic() + limits["probe_seconds"]
    probe_id = probe["id"]
    separator = b"\r\n\r\n" if probe_id == "esl.frames.crlf" else b"\n\n"
    with ESL(target, limits, wire, deadline) as connection:
        result = {"greeting": connection.greeting}
        if probe_id in ("esl.auth.wrong", "esl.auth.required"):
            command = b"auth conformance-deliberately-wrong-password\n\n" if probe_id.endswith("wrong") else b"api echo must-not-execute\n\n"
            connection.send(command, secret=probe_id.endswith("wrong"))
            frame = connection.reader.frame()
            if frame["headers"].get("content-type") != "command/reply" or not frame["headers"].get("reply-text", "").startswith("-ERR"):
                raise ProbeError("认证失败路径未拒绝请求")
            result["response"] = comparable_frame(frame)
            if probe_id == "esl.auth.required":
                if frame["headers"].get("reply-text") != "-ERR command not found":
                    raise ProbeError("原版未认证非 auth 命令必须返回 command not found")
                result["later_authentication"] = connection.auth(password)
                result["authenticated_echo"] = require_echo(connection.request(b"api echo now-authenticated\n\n"), "now-authenticated")
                return result
            # 异步 text/disconnect-notice 也是合法待比较响应；等待 EOF，不能只看一条错误就当资源释放。
            tail = []
            try:
                while len(tail) < 4:
                    tail.append(comparable_frame(connection.reader.frame()))
                raise ProbeError("认证拒绝后持续发送过多帧")
            except EOFError:
                if connection.reader.buffer:
                    raise ProbeError("认证拒绝后存在不完整尾帧")
                result["closed"] = True
                result["tail"] = tail
                return result
        if probe_id == "esl.command.case":
            connection.send(b"AuTh " + password.encode() + b"\n\n", secret=True)
            auth = connection.reader.frame()
            if auth["headers"].get("reply-text") != "+OK accepted":
                raise ProbeError("混合大小写 AUTH 失败")
            result["authentication"] = comparable_frame(auth)
        else:
            result["authentication"] = connection.auth(password, separator, probe_id == "esl.auth.fragmented")
        if probe_id == "identity.esl":
            frame = connection.request(b"api version\n\n")
            if frame["headers"].get("content-type") != "api/response":
                raise ProbeError("版本入口不是 api/response")
            return {"version": frame["body"].decode("utf-8", errors="strict"), "frame": comparable_frame(frame)}
        if probe_id == "identity.api_inventory":
            frame = connection.request(b"api show api as json\n\n")
            inventory = parse_api_inventory(frame)
            return {"inventory": inventory, "raw_frame": comparable_frame(frame)}
        if probe_id == "esl.console.single":
            # 原版 fs_cli -x 会携带此头；只检查裸 api 会漏掉真实客户端无法调用的问题。
            # 前导空格要求实际进入控制台单命令路径，false 则不能错误解释字面 ;;。
            requests = [
                (("api echo " + ECHO_TEXT + "\nconsole_execute: true\n\n").encode(), ECHO_TEXT),
                (b"api echo\nconsole_execute: true\n\n", "-ERR no reply\n"),
                (b"api    echo   a;b  \nconsole_execute: true\n\n", "a;b"),
                (b"api echo a;;echo b\nconsole_execute: false\n\n", "a;;echo b"),
            ]
            result["responses"] = [require_echo(connection.request(command), expected) for command, expected in requests]
            return result
        if probe_id == "esl.api.echo_empty":
            commands = [b"api echo\n\n", b"api echo \n\n", b"api echo   spaced value  \n\n"]
            responses = []
            for command in commands:
                frame = connection.request(command)
                if frame["headers"].get("content-type") != "api/response":
                    raise ProbeError("echo 边界返回错误帧类别")
                responses.append(comparable_frame(frame))
            result["responses"] = responses
            return result
        if probe_id == "esl.api.create_uuid":
            values = []
            for _ in range(2):
                frame = connection.request(b"api create_uuid\n\n")
                text = frame["body"].decode("ascii")
                try:
                    parsed = uuid.UUID(text)
                except ValueError as error:
                    raise ProbeError("create_uuid 未生成合法 UUID") from error
                if str(parsed) != text or parsed.int == 0 or frame["headers"].get("content-type") != "api/response":
                    raise ProbeError("create_uuid 的格式或返回帧错误")
                values.append(text)
            if values[0] == values[1]:
                raise ProbeError("连续 create_uuid 返回重复值")
            result["uuid_contract"] = {"canonical": True, "non_nil": True, "distinct": True, "normalization_whitelist": ["完整随机 UUID 字段：格式和去重已验证，原值保留 wire"]}
            return result
        if probe_id.startswith("esl.api.uuid_"):
            if probe_id == "esl.api.uuid_syntax":
                commands = [b"api uuid_exists\n\n", b"api uuid_getvar\n\n", b"api uuid_setvar\n\n", b"api uuid_kill\n\n"]
                responses = []
                for command in commands:
                    frame = connection.request(command)
                    if frame["headers"].get("content-type") != "api/response":
                        raise ProbeError("uuid 缺参数响应类别错误")
                    responses.append(comparable_frame(frame))
                result["responses"] = responses
                return result
            absent = "00000000-0000-0000-0000-000000000000"
            precondition = connection.request(("api uuid_exists " + absent + "\n\n").encode())
            if precondition["headers"].get("content-type") != "api/response" or precondition["body"] != b"false":
                raise ProbeError("专用 nil UUID 不存在前提未成立，拒绝继续通道请求")
            result["absent_precondition"] = comparable_frame(precondition)
            command_name = probe_id.removeprefix("esl.api.").removesuffix("_missing")
            if command_name == "uuid_exists":
                return result
            arguments = absent + (" conformance_missing_variable fixture-value" if command_name == "uuid_setvar" else " conformance_missing_variable" if command_name == "uuid_getvar" else "")
            frame = connection.request(("api " + command_name + " " + arguments + "\n\n").encode())
            if frame["headers"].get("content-type") != "api/response" or frame["body"] != b"-ERR No such channel!\n":
                raise ProbeError("不存在通道未返回原版 No such channel 错误")
            result["response"] = comparable_frame(frame)
            return result
        if probe_id == "esl.command.case":
            result["case_echo"] = require_echo(connection.request(("ApI echo " + ECHO_TEXT + "\n\n").encode()), ECHO_TEXT)
            replies = []
            for command in [b"EvEnT JsOn background_job\n\n", b"EVENT BACKGROUND_JOB\n\n", b"NIXEVENT ALL\n\n", b"NOEVENTS\n\n"]:
                frame = connection.request(command)
                if not frame["headers"].get("reply-text", "").startswith("+OK"):
                    raise ProbeError("大小写或事件订阅生命周期被拒绝")
                replies.append(comparable_frame(frame))
            result["replies"] = replies
            return result
        if probe_id == "esl.filter.lifecycle":
            result["filter_rounds"] = []
            for clearing in [b"filter delete all\n\n", b"filter add Job-UUID \n\n"]:
                round_result = {"replies": []}
                for command in [b"event plain BACKGROUND_JOB\n\n", b"filter Job-UUID must-never-match\n\n", clearing]:
                    frame = connection.request(command)
                    if not frame["headers"].get("reply-text", "").startswith("+OK"):
                        raise ProbeError("filter 生命周期命令未成功")
                    round_result["replies"].append(comparable_frame(frame))
                # 每次清除都独立要求实际交付作业，第二次清除不能掩盖第一次留下的错误过滤。
                connection.send(("bgapi echo " + ECHO_TEXT + "\nJob-UUID: " + JOB_UUID + "\n\n").encode())
                round_result["job"] = read_background_job(connection, "plain")
                result["filter_rounds"].append(round_result)
            return result
        if probe_id == "esl.api.unknown":
            frame = connection.request(("api " + UNKNOWN_COMMAND + "\n\n").encode())
            if frame["headers"].get("content-type") != "api/response" or not frame["body"].startswith(b"-ERR"):
                raise ProbeError("未知 API 未返回错误正文")
            result["response"] = comparable_frame(frame)
            return result
        if probe_id == "esl.frames.pipeline":
            connection.send(("api echo " + ECHO_TEXT + "\n\napi echo second-frame\n\n").encode())
            result["responses"] = [require_echo(connection.reader.frame(), text) for text in (ECHO_TEXT, "second-frame")]
            return result
        if probe_id.startswith("esl.bgapi.echo"):
            event_format = probe_id.rsplit(".", 1)[-1] if probe_id.endswith((".json", ".xml")) else "plain"
            subscription = connection.request(("event " + event_format + " BACKGROUND_JOB\n\n").encode())
            if subscription["headers"].get("reply-text", "").startswith("-ERR"):
                raise ProbeError("BACKGROUND_JOB 订阅失败")
            result["subscription"] = comparable_frame(subscription)
            connection.send(("bgapi echo " + ECHO_TEXT + "\nJob-UUID: " + JOB_UUID + "\n\n").encode())
            result["job"] = read_background_job(connection, event_format)
            return result
        if probe_id == "esl.frames.split_positions":
            # 首次连接用于基准，其他连接共享探针总时限；不是每条请求重新领取完整时限。
            command = ("api echo " + ECHO_TEXT + "\n\n").encode()
            result["response"] = require_echo(connection.request(command), ECHO_TEXT)
            observations = []
            for split in range(1, len(command)):
                with ESL(target, limits, wire, deadline) as peer:
                    peer.auth(password)
                    peer.send(command[:split])
                    peer.send(command[split:])
                    observations.append(require_echo(peer.reader.frame(), ECHO_TEXT))
            result["every_split_response"] = observations
            return result
        frame = connection.request(("api echo " + ECHO_TEXT).encode() + separator)
        result["response"] = require_echo(frame, ECHO_TEXT)
        return result


def read_background_job(connection, event_format):
    """两个真实帧的接受/完成关联与完整正文；供不同订阅/过滤子场景复用相同严格断言。"""
    frames = [connection.reader.frame(), connection.reader.frame()]
    replies = [frame for frame in frames if frame["headers"].get("content-type") == "command/reply"]
    events = [frame for frame in frames if frame["headers"].get("content-type") == "text/event-" + event_format]
    if len(replies) != 1 or len(events) != 1:
        raise ProbeError("bgapi 缺唯一受理回复或唯一完成事件")
    reply = replies[0]
    if JOB_UUID not in reply["headers"].get("reply-text", "") or reply["headers"].get("job-uuid") != JOB_UUID:
        raise ProbeError("bgapi 受理回复的 Job-UUID 不一致")
    result = {}
    result["reply"] = comparable_frame(reply)
    event = parse_event(events[0]["body"], event_format)
    if event["headers"].get("event-name") != "BACKGROUND_JOB" or event["headers"].get("job-uuid") != JOB_UUID or event["headers"].get("job-command") != "echo" or event["headers"].get("job-command-arg", "") != ECHO_TEXT or event["body"] != ECHO_TEXT.encode():
        raise ProbeError("BACKGROUND_JOB 命令、参数、UUID 或正文不符合原作业")
    # 只白名单实例元数据；未知额外事件头保留并参与比较，不能任意删字段制造相同。
    ignored = {"core-uuid", "freeSWITCH-hostname".lower(), "freeswitch-switchname", "freeswitch-ipv4", "freeswitch-ipv6", "event-date-local", "event-date-gmt", "event-date-timestamp", "event-calling-file", "event-calling-function", "event-calling-line-number", "event-sequence"}
    normalized = {key: value for key, value in event["headers"].items() if key not in ignored}
    # 原始外层 Content-Length 已通过帧读取校验；它随已白名单的事件实例元数据变化。
    result["event"] = {"format": event_format, "headers": normalized, "body_base64": base64.b64encode(event["body"]).decode()}
    result["normalization_whitelist"] = sorted(ignored) + ["outer-event-frame.content-length: 已忽略元数据导致的长度变化"]
    return result


def parse_event(data, event_format):
    """将三种真实事件表示投影为头/正文；只消除已说明的序列化表示差异。"""
    if event_format == "plain":
        event = parse_plain_event(data)
        event["headers"] = {key: unquote(value) for key, value in event["headers"].items()}
        return event
    if event_format == "json":
        value = json.loads(data)
        if not isinstance(value, dict) or any(not isinstance(key, str) or not isinstance(item, str) for key, item in value.items()):
            raise ProbeError("JSON 事件不是当前单值字段契约")
        body = value.pop("_body", "").encode()
        headers = {key.lower(): item for key, item in value.items()}
        if len(headers) != len(value):
            raise ProbeError("JSON 事件存在大小写重复头")
    elif event_format == "xml":
        if b"<!DOCTYPE" in data.upper() or b"<!ENTITY" in data.upper():
            raise ProbeError("事件 XML 禁止 DTD/实体展开")
        root = ET.fromstring(data)
        if root.tag != "event" or root.attrib or len(root.findall("headers")) != 1 or root.find("headers").attrib:
            raise ProbeError("XML 事件缺唯一 event/headers 结构")
        headers = {}
        for child in root.find("headers"):
            key = child.tag.lower()
            if key in headers or len(child) or child.attrib:
                raise ProbeError("XML 事件含重复/嵌套/属性头，超出本单值断言")
            headers[key] = unquote(child.text or "")
        if any(child.tag not in ("headers", "Content-Length", "body") for child in root) or len(root.findall("body")) > 1 or len(root.findall("Content-Length")) > 1:
            raise ProbeError("XML 事件根元素契约不匹配")
        if any(len(child) or child.attrib for child in root if child.tag in ("body", "Content-Length")):
            raise ProbeError("XML 事件正文或长度出现嵌套/属性")
        body = (root.findtext("body") or "").encode()
        if root.find("Content-Length") is not None:
            headers["content-length"] = root.findtext("Content-Length") or ""
    else:
        raise ProbeError("未知事件序列化格式")
    length = headers.get("content-length", "0")
    if not re.fullmatch(r"[0-9]{1,10}", length) or int(length) != len(body):
        raise ProbeError("JSON/XML 内层 Content-Length 与原始正文不符")
    return {"headers": headers, "body": body}


def parse_plain_event(data):
    """事件内层也按 Content-Length 校验，不能把外层 frame 通过当作事件正文完整。"""
    separator = b"\n\n"
    index = data.find(separator)
    if index < 0:
        separator = b"\r\n\r\n"
        index = data.find(separator)
    if index < 0:
        raise ProbeError("BACKGROUND_JOB 缺事件头正文分隔")
    headers = parse_headers(data[:index])
    body = data[index + len(separator):]
    length = headers.get("content-length", "0")
    if not re.fullmatch(r"[0-9]{1,10}", length) or int(length) != len(body):
        raise ProbeError("事件内层 Content-Length 与正文不符")
    return {"headers": headers, "body": body}


class SingleDatagram:
    """为通用帧读取器提供一次 UDP 数据报；耗尽后立即 EOF，禁止跨报文补正文。"""
    def __init__(self, packet):
        self.packet = packet

    def settimeout(self, _timeout):
        pass

    def recv(self, _maximum):
        packet, self.packet = self.packet, b""
        return packet


def run_sip(probe, target, limits, wire):
    """只执行无呼叫副作用的 OPTIONS；TLS 使用可信 CA 与主机名校验。"""
    deadline = time.monotonic() + limits["probe_seconds"]
    transport = probe["adapter"].split("_")[1]
    datagram = transport == "udp"
    host, port = target["host"], target["port"]
    # 为每端生成并核对独立事务标识；比较时只允许这些客户端已知随机值不同。
    token = os.urandom(12).hex()
    call_id = token + "@conformance.invalid"
    branch = "z9hG4bK" + token
    infos = socket.getaddrinfo(host, port, type=socket.SOCK_DGRAM if datagram else socket.SOCK_STREAM)
    family, socktype, protocol, _, address = infos[0]
    stream = socket.socket(family, socktype, protocol)
    try:
        stream.settimeout(min(limits["connect_seconds"], deadline - time.monotonic()))
        stream.connect(address)
        if transport == "tls":
            context = ssl.create_default_context(cafile=target.get("ca_file"))
            context.minimum_version = ssl.TLSVersion.TLSv1_2
            stream = context.wrap_socket(stream, server_hostname=target.get("server_name", host))
        local_host, local_port = stream.getsockname()[:2]
        local_uri = "[" + local_host + "]" if ":" in local_host else local_host
        remote_uri = "[" + host + "]" if ":" in host else host
        sent_by = local_uri + ":" + str(local_port)
        uri = "sip:" + remote_uri + ":" + str(port)
        request = (f"OPTIONS {uri} SIP/2.0\r\nVia: SIP/2.0/{transport.upper()} {sent_by};branch={branch};rport\r\nMax-Forwards: 70\r\nFrom: <sip:conformance@conformance.invalid>;tag={token}\r\nTo: <{uri}>\r\nCall-ID: {call_id}\r\nCSeq: 1 OPTIONS\r\nContact: <sip:conformance@{sent_by}>\r\nContent-Length: 0\r\n\r\n").encode()
        wire.add("send", request)
        stream.sendall(request)
        reader = FrameReader(stream, deadline, limits, wire)
        while True:
            if datagram:
                # 一个 UDP 数据报必须独立构成完整消息，不能把两个损坏报文拼成合法响应。
                stream.settimeout(reader.remaining())
                packet = stream.recv(65536)
                if not packet:
                    raise ProbeError("收到空 SIP UDP 数据报")
                wire.add("receive", packet)
                reader = FrameReader(SingleDatagram(packet), deadline, limits, Wire(limits["max_evidence_bytes"]))
            # SIP 第一行不是 ESL header，先单独读取，再复用有界 Content-Length 解析。
            while b"\r\n" not in reader.buffer:
                reader.receive()
                if b"\r\n" not in reader.buffer and len(reader.buffer) > limits["max_header_bytes"]:
                    raise ProbeError("SIP 状态行超过上限")
            line, rest = bytes(reader.buffer).split(b"\r\n", 1)
            if len(line) > limits["max_header_bytes"]:
                raise ProbeError("SIP 状态行超过上限")
            reader.buffer = bytearray(rest)
            match = re.fullmatch(rb"SIP/2.0 ([1-6][0-9][0-9]) (.*)", line)
            if not match:
                raise ProbeError("SIP 状态行不合法")
            frame = reader.frame()
            if datagram and reader.buffer:
                raise ProbeError("单个 SIP UDP 数据报存在 Content-Length 外额外字节")
            headers = frame["headers"]
            via = headers.get("via", "")
            from_header = headers.get("from", "")
            if headers.get("call-id") != call_id or headers.get("cseq") != "1 OPTIONS" or not re.search(r"(?:^|;)branch=" + re.escape(branch) + r"(?:;|$)", via):
                raise ProbeError("SIP 返回不属于本次实际 OPTIONS 事务")
            if not via.startswith("SIP/2.0/" + transport.upper() + " " + sent_by + ";") or not re.search(r"(?:^|;)tag=" + re.escape(token) + r"(?:;|$)", from_header) or "<sip:conformance@conformance.invalid>" not in from_header or "<" + uri + ">" not in headers.get("to", ""):
                raise ProbeError("SIP Via/From/To 未正确回显本次客户端事务")
            code = int(match.group(1))
            if code < 200:
                continue
            if code != 200:
                raise ProbeError("OPTIONS 未成功，状态码 " + str(code))
            capabilities = {name: sorted(part.strip().lower() for part in headers[name].split(",") if part.strip()) if name in headers else None for name in ("allow", "supported", "accept", "allow-events")}
            # Server/User-Agent 品牌可不同；能力缺失不白名单，作为真实兼容差异保留。
            return {"status_code": code, "reason_base64": base64.b64encode(match.group(2)).decode(), "capabilities": capabilities, "body_base64": base64.b64encode(frame["body"]).decode(), "normalization_whitelist": ["客户端生成的 Via/From/Call-ID 与端点 URI：已各自核对事务", "To tag、Date：实例元数据", "Server/User-Agent：产品身份"], "scope": "只比较状态、能力头和正文；其他响应头保留原始报文但未全契约断言"}
    finally:
        stream.close()


def artifact_manifest(endpoint):
    declared = endpoint.get("artifact", {})
    result = {"source_commit_declared": declared.get("source_commit"), "binding_level": "declared_local_files_not_remote_process_attestation", "files": {}, "missing": []}
    for key in ("binary_path", "config_path", "source_manifest_path"):
        path = declared.get(key)
        if not path:
            result["missing"].append(key)
            continue
        try:
            result["files"][key] = {"path": str(Path(path).resolve()), "sha256": file_digest(path)}
        except OSError as error:
            result["missing"].append(key + ":" + type(error).__name__)
    return result


def execute_probe(probe, endpoint, profile, limits):
    target = endpoint.get(probe["adapter"])
    wire = Wire(limits["max_evidence_bytes"])
    started = stamp()
    try:
        if not target:
            raise Unavailable("未配置 " + probe["adapter"] + " 测试端点；不能替代为其他协议")
        if probe.get("isolated") and not profile["fixture"]["isolated"]:
            raise Unavailable("该探针需显式隔离 fixture，避免在现网订阅后台任务或触发鉴权失败")
        value = run_esl(probe, target, limits, wire) if probe["adapter"] == "esl" else run_sip(probe, target, limits, wire)
        result = {"state": "observed", "value": value}
    except Unavailable as error:
        result = {"state": "blocked", "error": str(error), "error_type": type(error).__name__}
    except (OSError, ProbeError, EOFError, UnicodeError, ValueError, ET.ParseError) as error:
        result = {"state": "failed", "error": str(error), "error_type": type(error).__name__}
    result.update(started_at=started, finished_at=stamp(), wire=wire.records, wire_bytes=wire.used)
    return result


def reference_version_matches(value):
    return bool(re.search(r"(?<![0-9.])" + re.escape(REFERENCE_VERSION) + r"(?![0-9.])", value)) and "FreeSWITCH" in value


def run_suite(root, profile, selected=None):
    """报告全部原始 ID，漏探针/跳过/缺端点从不汇总为通过。"""
    limits = validate_profile(profile)
    plan = build_plan(root)
    before = source_fingerprint(root)
    ids = {probe["id"] for probe in PROBES}
    if selected is not None and (not selected or set(selected) - ids):
        raise ProbeError("选择为空或含未知探针，不能静默忽略")
    selected = set(selected) if selected else ids
    selected.add("identity.esl")
    report = {
        "schema_version": "1.0.0", "started_at": stamp(), "reference": profile["reference"],
        "profile_sha256": digest(canonical(profile)), "fixture": profile["fixture"],
        "plan_sha256": digest(canonical(plan)), "plan_counts": plan["counts"], "source_before": before,
        "endpoint_artifacts": {side: artifact_manifest(endpoint) for side, endpoint in profile["endpoints"].items()},
        "probes": [], "entries": [], "full_compatibility_passed": False, "all_protocols_passed": False,
        "full_superiority_proven": False, "production_capacity_certified": False,
        "evidence_type": "live_paired_protocol_subscenario_only",
        "normalization_policy": "只允许每个探针声明的字段；帧头名称大小写和排列规范化，ESL 正文原始字节不修剪。身份差异单独记录，不伪造 FreeSWITCH 版本。",
    }
    identity = next(probe for probe in PROBES if probe["id"] == "identity.esl")
    identities = {side: execute_probe(identity, endpoint, profile, limits) for side, endpoint in profile["endpoints"].items()}
    reference = identities["reference"]
    baseline_ok = reference["state"] == "observed" and reference_version_matches(reference["value"]["version"])
    identity_state = "identity_observed" if baseline_ok and identities["candidate"]["state"] == "observed" else "failed_baseline" if reference["state"] == "observed" and not baseline_ok else "blocked_baseline"
    report["probes"].append(dict(identity, state=identity_state, results=identities, counts_as_compatibility_pass=False, complete_contract_passed=False))
    for probe in PROBES:
        if probe["id"] == "identity.esl":
            continue
        record = dict(probe, complete_contract_passed=False)
        if probe["id"] not in selected:
            record.update(state="not_run_not_selected", next_step="在明确 fixture 下执行该探针；本次不能算完整探针集合通过")
        elif not baseline_ok:
            record.update(state="blocked_baseline", next_step="先取得固定版本原版 ESL 身份证据；失联/错版本不能当兼容通过")
        else:
            results = {side: execute_probe(probe, endpoint, profile, limits) for side, endpoint in profile["endpoints"].items()}
            record["results"] = results
            states = [value["state"] for value in results.values()]
            if "failed" in states:
                record["state"] = "failed"
            elif "blocked" in states:
                record["state"] = "blocked_environment"
            elif not all(value["wire_bytes"] > 0 for value in results.values()):
                record["state"] = "failed_missing_traffic"
            elif probe.get("identity_only"):
                record["state"] = "inventory_observed"
            elif results["reference"]["value"] != results["candidate"]["value"]:
                record["state"] = "failed_difference"
                record["difference"] = {side: result["value"] for side, result in results.items()}
            else:
                record["state"] = "passed_scoped"
            record["next_step"] = "继续实例化并运行关联原条目的其他强制分支" if record["state"] == "passed_scoped" else "按双方原始报文定位差异，修复后重新执行；禁止删字段或放宽断言以伪造通过"
        report["probes"].append(record)
    probe_map = {probe["id"]: probe for probe in report["probes"]}
    for entry in plan["entries"]:
        report["entries"].append({"id": entry["id"], "implementation_status": entry["implementation_status"], "state": entry["state"], "complete_contract_passed": False, "probe_results": [{"id": probe_id, "state": probe_map[probe_id]["state"]} for probe_id in entry["probe_ids"]], "remaining_assertion_groups": entry["required_assertion_groups"], "next_step": entry["next_step"]})
    report["api_inventory"] = compare_api_inventory(plan, probe_map.get("identity.api_inventory", {}))
    report["source_after"] = source_fingerprint(root)
    source_stable = before == report["source_after"]
    report["source_stable"] = source_stable
    if not source_stable:
        for probe in report["probes"]:
            if probe["state"] == "passed_scoped":
                probe["state"] = "stale_source_changed"
        for entry in report["entries"]:
            for item in entry["probe_results"]:
                item["state"] = probe_map[item["id"]]["state"]
    required = [probe for probe in report["probes"] if not probe.get("identity_only")]
    report["all_defined_probes_passed"] = source_stable and baseline_ok and all(probe["state"] == "passed_scoped" for probe in required)
    report["counts"] = {"original_entries": len(report["entries"]), "full_contract_passed": 0, "probes_by_state": dict(sorted(Counter(probe["state"] for probe in report["probes"]).items())), "paired_scoped_passes": sum(probe["state"] == "passed_scoped" for probe in report["probes"])}
    report["reference_runtime_executed"] = baseline_ok
    report["finished_at"] = stamp()
    return report


def render_report(report):
    lines = ["# FreeSWITCH 成对协议验证报告", "", "本报告只记录已经实际执行的子场景。原始条目完整兼容、全部协议和全面超越均未通过认证。", "", f"原始条目：{report['counts']['original_entries']}；完整条目通过：0；真实成对子场景通过：{report['counts']['paired_scoped_passes']}。", "", "| 探针 | 本次结果 | 严格范围 |", "| --- | --- | --- |"]
    for probe in report["probes"]:
        lines.append(f"| {probe['id']} | {probe['state']} | {probe['scope']} |")
    inventory = report.get("api_inventory", {})
    lines += ["", f"实际 API 目录：原版 {inventory.get('reference_runtime_rows', 0)} 个入口，候选 {inventory.get('candidate_runtime_rows', 0)} 个入口；逐个对照 {inventory.get('source_registration_entries', 0)} 个原始 API 声明。存在入口仍需语义验证。", "", "完整原始请求/响应（密码帧脱敏）、差异、逐条后续步骤和哈希见同名 JSON。", "", "源码和指定二进制/配置文件哈希是可追溯材料；远端正在运行进程与这些文件的对应关系仍需原构建/启动记录证明。", ""]
    return "\n".join(lines)
