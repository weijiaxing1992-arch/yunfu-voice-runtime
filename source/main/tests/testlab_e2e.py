#!/usr/bin/env python3
"""通过现有回环 HTTP 管理入口验收隔离测试作业，不自行启动或终止服务进程。

默认只执行单路 PCMU 三秒及媒体阶段停止测试；只有显式指定 --pressure-calls
才产生额外压力。CSRF 仅保存在内存，报告只包含白名单验收摘要，不复制服务配置或日志。
"""

import argparse
import http.client
import ipaddress
import json
import math
import os
from pathlib import Path
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


TERMINAL = {"completed", "failed", "stopped"}
CLEANUP_FIELDS = (
    "complete", "controller_exited", "generator_exited", "media_processes_exited",
    "ports_released", "work_dir_removed",
)


class AcceptanceFailure(Exception):
    """只携带脚本自身产生的安全诊断，不包含原始 HTTP 正文、令牌或配置。"""


class ConnectivityFailure(AcceptanceFailure):
    """只暴露白名单网络类别；不保留可能携带 URL、令牌或正文的原始异常。"""

    def __init__(self, category):
        """timeout 是等待超限；connect 是建连/取响应失败；read 是正文或 HTTP 流中断。"""
        self.category = category if category in {"timeout", "connect", "read"} else "read"
        labels = {"timeout": "读取等待超限", "connect": "连接未完成", "read": "响应读取中断"}
        super().__init__("回环管理网络采样失败：" + labels[self.category])


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """拒绝所有重定向，避免管理请求或 CSRF 被带到操作者未指定的地址。"""

    def redirect_request(self, request, response, code, message, headers, new_url):
        """由 urllib 将重定向当成 HTTP 错误交回，不发送第二次请求。"""
        return None


def require(condition, message):
    """使用不会被 python -O 关闭的断言；调用者只能传入白名单诊断文本。"""
    if not condition:
        raise AcceptanceFailure(message)


def loopback_url(value):
    """只接受 HTTP 回环地址和可选端口，不接受凭据、路径、查询或环境代理。"""
    try:
        parsed = urllib.parse.urlsplit(value)
        host = parsed.hostname
        local = host == "localhost" or (host is not None and ipaddress.ip_address(host).is_loopback)
        port = parsed.port or 80
    except (ValueError, TypeError):
        raise argparse.ArgumentTypeError("管理 URL 必须使用 localhost 或回环 IP 字面量") from None
    if not local or parsed.scheme != "http" or parsed.username is not None or parsed.password is not None:
        raise argparse.ArgumentTypeError("仅允许没有凭据的 HTTP 回环管理 URL")
    if parsed.path not in ("", "/") or parsed.query or parsed.fragment or not 1 <= port <= 65535:
        raise argparse.ArgumentTypeError("管理 URL 只能包含回环主机和有效端口")
    return urllib.parse.urlunsplit(("http", parsed.netloc, "", "", ""))


class Client:
    """有限超时的同源 JSON 客户端，明确禁用代理和重定向，不打印请求头或响应正文。"""

    def __init__(self, base_url):
        """令牌初始为空；只有读取配置后的写请求会带上内存中的 CSRF。"""
        self.base_url = base_url
        self.csrf = ""
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def request(self, method, path, body=None, *, raw=None, csrf=True, content_type="application/json", timeout=5):
        """返回状态及解码对象；每次最多读取 8 MiB，网络错误不带原始异常或服务正文。"""
        require(path.startswith("/v1/") and not path.startswith("//"), "脚本拒绝非管理接口路径")
        headers = {"Accept": "application/json"}
        data = None
        if method != "GET":
            data = raw if raw is not None else json.dumps(body, separators=(",", ":")).encode("utf-8")
            headers["Content-Type"] = content_type
            if csrf:
                headers["X-RustSwitch-CSRF"] = self.csrf
        request = urllib.request.Request(self.base_url + path, data=data, headers=headers, method=method)
        stage = "connect"
        try:
            try:
                response = self.opener.open(request, timeout=min(5, max(0.001, timeout)))
            except urllib.error.HTTPError as error:
                response = error
            stage = "read"
            with response:
                status = response.code
                raw_response = response.read(8 * 1024 * 1024 + 1)
        except (urllib.error.URLError, OSError, TimeoutError, http.client.HTTPException) as error:
            # urllib 可能包装超时；只检查类型，不把 reason 的原文放入报告。
            reason = error.reason if isinstance(error, urllib.error.URLError) else error
            category = "timeout" if isinstance(reason, TimeoutError) else "read" if isinstance(error, http.client.HTTPException) else stage
            raise ConnectivityFailure(category) from None
        require(len(raw_response) <= 8 * 1024 * 1024, "管理响应超过验收工具的读取上限")
        try:
            decoded = json.loads(raw_response)
        except (ValueError, UnicodeError):
            raise AcceptanceFailure("管理接口返回了无法解析的 JSON，未记录其正文") from None
        require(isinstance(decoded, dict), "管理接口响应必须为 JSON 对象")
        return status, decoded

    def get(self, path, *, timeout=5):
        """读取必须成功的观测入口；不因失败自动修改配置或重新启动服务。"""
        status, value = self.request("GET", path, timeout=timeout)
        require(status == 200, f"只读管理请求预期 HTTP 200，实际为 {status}")
        return value


def task_path(identifier, suffix=""):
    """任务 ID 只来自后端返回值，逐段编码后构造路由，不能将其作为磁盘路径使用。"""
    require(isinstance(identifier, str) and bool(identifier), "任务缺少有效 ID")
    return "/v1/tests/" + urllib.parse.quote(identifier, safe="") + suffix


def main_config(client):
    """只把需要比较的配置保留在内存；CSRF 不进入比较对象或最终报告。"""
    config = client.get("/v1/config")
    require(isinstance(config.get("csrf_token"), str) and bool(config["csrf_token"]), "配置响应缺少写请求令牌")
    client.csrf = config["csrf_token"]
    require(isinstance(config.get("guard"), dict) and isinstance(config["guard"].get("policy"), dict), "配置响应缺少峰值保护策略")
    require(isinstance(config.get("active"), dict) and "revision" in config, "配置响应缺少活动配置或版本")
    return {"revision": config["revision"], "active": config["active"], "guard_policy": config["guard"]["policy"]}


def has_active_test(overview):
    """已有非终态任务或尚未清理的当前任务时拒绝执行，不能停止其他操作者的工作。"""
    current = overview.get("current")
    if current is not None:
        require(isinstance(current, dict), "测试目录 current 类型无效")
        if current.get("status") not in TERMINAL or not current.get("cleanup", {}).get("complete", False):
            return True
    history = overview.get("history")
    require(isinstance(history, list), "测试目录缺少历史列表")
    return any(isinstance(task, dict) and task.get("status") not in TERMINAL for task in history)


def number(value):
    """读取有限非负数，拒绝把 true、缺失值或 NaN 当成真实媒体计数。"""
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value) and value >= 0


def check_cleanup(task):
    """不仅核对 complete，还逐项确认专属进程、端口和工作目录已经清理。"""
    cleanup = task.get("cleanup")
    require(isinstance(cleanup, dict), "任务缺少清理状态")
    for name in CLEANUP_FIELDS:
        require(cleanup.get(name) is True, f"任务清理未确认：{name}")


class Acceptance:
    """串行运行有界验收，仅凭本次生成且实际发送的幂等键识别自身任务。"""

    def __init__(self, client, summary, timeout):
        """每次运行只生成一次唯一前缀；同一场景的重试永远复用完整 request_id。"""
        self.client = client
        self.summary = summary
        self.timeout = timeout
        self.prefix = "testlab-e2e-" + uuid.uuid4().hex
        self.requests = {}
        self.owned = {}
        self.baseline = None

    def payload(self, name, *, mode="phone", calls=1, cps=1, seconds=3):
        """请求只选择模拟端点模式及 PCMU，不传外部分机、麦克风或任何服务端地址。"""
        return {"request_id": self.prefix + "-" + name, "mode": mode, "concurrency": calls,
                "cps": cps, "duration_seconds": seconds, "payload": 0}

    def remember(self, task):
        """只登记 request_id 精确匹配本次已发送请求的任务，拒绝认领目录里的其他任务。"""
        require(isinstance(task, dict) and task.get("request_id") in self.requests, "后端任务不属于本次验收请求")
        identifier = task.get("id")
        task_path(identifier)
        self.owned[identifier] = task["request_id"]
        return identifier

    def read_owned(self, identifier, *, timeout=5):
        """每次读取及停止前重新验证任务归属，不仅依赖最初返回的 ID。"""
        task = self.client.get(task_path(identifier), timeout=timeout)
        require(task.get("id") == identifier and task.get("request_id") == self.owned.get(identifier), "任务 ID 或幂等键归属发生变化")
        return task

    def check_main_unchanged(self):
        """配置比较只在内存进行，输出三个布尔值，绝不复制配置、策略参数或 CSRF。"""
        require(self.baseline is not None, "尚未记录主服务配置基线")
        current = main_config(self.client)
        checked = {key: current[key] == self.baseline[key] for key in self.baseline}
        self.summary["main_service_unchanged"] = checked
        require(all(checked.values()), "验收期间主服务 revision、保护策略或活动配置发生变化")

    def check_invalid_requests(self):
        """使用最多两路的安全负例，验证严格 JSON、参数范围、媒体类型及 CSRF 拒绝。"""
        cases = [
            ("unknown-field", 400, {"unexpected": True}, {}),
            ("extra-json", 400, {}, {"extra_json": True}),
            ("phone-two-calls", 422, {"concurrency": 2}, {}),
            ("zero-cps", 422, {"cps": 0}, {}),
            ("unsupported-payload", 422, {"payload": 97}, {}),
            ("wrong-content-type", 415, {}, {"content_type": "text/plain"}),
            ("missing-csrf", 403, {}, {"csrf": False}),
        ]
        passed = []
        for name, expected, change, options in cases:
            payload = self.payload("invalid-" + name)
            payload.update(change)
            self.requests[payload["request_id"]] = payload
            options = dict(options)
            if options.pop("extra_json", False):
                options["raw"] = json.dumps(payload).encode("utf-8") + b"\n{}"
            status, value = self.client.request("POST", "/v1/tests", payload, **options)
            if status in (200, 202) and value.get("request_id") in self.requests:
                self.remember(value)
            require(status == expected, f"非法请求 {name} 预期 HTTP {expected}，实际为 {status}")
            overview = self.client.get("/v1/tests")
            require(not has_active_test(overview), f"非法请求 {name} 之后出现运行任务；不会停止非本次任务")
            for task in [overview.get("current"), *overview.get("history", [])]:
                require(not isinstance(task, dict) or task.get("request_id") != payload["request_id"], f"非法请求 {name} 被登记为任务")
            passed.append({"case": name, "http_status": status})
        self.summary["invalid_requests"] = passed
        self.check_main_unchanged()

    def start(self, payload):
        """POST 的一次连接失败可用同键重试；不得换新键造成不可见的重复作业。"""
        require(not has_active_test(self.client.get("/v1/tests")), "已有活跃测试，拒绝新增任务")
        self.requests[payload["request_id"]] = dict(payload)
        for attempt in range(2):
            try:
                status, task = self.client.request("POST", "/v1/tests", payload)
                break
            except ConnectivityFailure:
                if attempt == 1:
                    raise
        require(status == 202 or (attempt > 0 and status == 200), f"新测试预期 HTTP 202，实际为 {status}")
        identifier = self.remember(task)
        for key in ("mode", "concurrency", "cps", "duration_seconds", "payload"):
            require(task.get(key) == payload[key], f"受理任务的 {key} 与本次请求不一致")
        return identifier

    def wait(self, identifier, *, want_media=False, timeout=None):
        """仅网络采样允许有界恢复；第三次连续中断失败，成功、重试均不重置总期限。"""
        deadline = time.monotonic() + (self.timeout if timeout is None else timeout)
        consecutive = 0
        for key in ("sampling_read_attempts", "sampling_read_errors", "sampling_max_consecutive_read_errors", "sampling_max_read_seconds"):
            self.summary.setdefault(key, 0)
        self.summary.setdefault("sampling_read_error_categories", {})
        self.summary.setdefault("sampling_read_error_details", [])
        while True:
            started = time.monotonic()
            require(started < deadline, "等待任务阶段或终态超时")
            self.summary["sampling_read_attempts"] += 1
            network_error = None
            try:
                # 单次仍不超过原五秒限制；总期限临近时只使用剩余预算。
                task = self.read_owned(identifier, timeout=min(5, deadline - started))
            except ConnectivityFailure as error:
                network_error = error
                consecutive += 1
                self.summary["sampling_read_errors"] += 1
                self.summary["sampling_max_consecutive_read_errors"] = max(self.summary["sampling_max_consecutive_read_errors"], consecutive)
                categories = self.summary["sampling_read_error_categories"]
                categories[error.category] = categories.get(error.category, 0) + 1
            finally:
                elapsed = max(0, time.monotonic() - started)
                self.summary["sampling_max_read_seconds"] = max(self.summary["sampling_max_read_seconds"], elapsed)
            if network_error is not None:
                # 保留最多20条安全错误耗时，避免长期采样使验收摘要无界增长。
                details = self.summary["sampling_read_error_details"]
                details.append({"category": network_error.category, "elapsed_seconds": elapsed, "consecutive": consecutive})
                del details[:-20]
                require(time.monotonic() < deadline, "等待任务阶段或终态超时")
                if consecutive >= 3:
                    raise ConnectivityFailure(network_error.category) from None
                time.sleep(min(0.2, max(0, deadline - time.monotonic())))
                continue
            consecutive = 0
            # 响应晚于总期限到达也不能作为通过，尤其不能因重试延长任务等待。
            require(time.monotonic() < deadline, "等待任务阶段或终态超时")
            if want_media:
                progress = task.get("progress") or {}
                samples = task.get("samples") or []
                media_seen = any(isinstance(sample, dict) and number(sample.get("rx_packets")) and sample["rx_packets"] > 0
                                 and number(sample.get("tx_packets")) and sample["tx_packets"] > 0 for sample in samples)
                if task.get("status") == "running" and (task.get("phase") == "media" or progress.get("phase") == "media") \
                        and number(progress.get("established_calls")) and progress["established_calls"] >= 1 and media_seen:
                    return task
                require(task.get("status") not in TERMINAL, "任务尚未观察到接通和真实媒体便已结束")
            elif task.get("status") in TERMINAL:
                return task
            require(time.monotonic() < deadline, "等待任务阶段或终态超时")
            time.sleep(min(0.2, max(0, deadline - time.monotonic())))

    def report(self, identifier):
        """报告仅在内存解析，不把包含隔离路径、配置证据或日志的原始对象写入输出。"""
        report = self.client.get(task_path(identifier, "/report"))
        run = report.get("run")
        require(isinstance(run, dict) and run.get("id") == identifier and run.get("request_id") == self.owned[identifier], "下载报告的任务归属不一致")
        return report

    def check_complete(self, identifier, calls, seconds):
        """核对真实负载和双向 RTP，而不是仅凭 HTTP 202、completed 或 passed 字样通过。"""
        task = self.wait(identifier)
        check_cleanup(task)
        require(task.get("report_available") is True, "终态任务没有可下载报告")
        report = self.report(identifier)
        result = report.get("result")
        # 先保留白名单结果，即使后续断言失败也能看见实际负载及原始 passed。
        outcome = {"id": identifier, "status": task.get("status"), "cleanup_complete": True,
                   "requested_calls": calls, "duration_seconds": seconds, "raw_passed": None}
        if isinstance(result, dict):
            for key in ("passed", "requested_calls", "established_calls", "nominal_packets", "sent_packets",
                        "received_unique_packets", "unreceived_packets", "offered_load_ratio", "generator_write_errors"):
                value = result.get(key)
                if value is None or isinstance(value, bool) or number(value):
                    outcome["raw_passed" if key == "passed" else key] = value
        self.summary["runs"].append(outcome)
        require(task.get("status") == "completed", "完整测试未进入 completed 终态")
        require(isinstance(result, dict) and result.get("passed") is True, "发生器完整验收未通过，实际结果已保留在摘要")
        require(result.get("requested_calls") == calls and result.get("established_calls") == calls, "目标呼叫未全部建立")
        require(result.get("payload_type") == 0 and result.get("media_seconds") == seconds, "实际媒体编码或时长与请求不一致")
        require(result.get("media_server_bypassed") is False, "测试绕过了 Rust 媒体服务，不能作为转发验收")
        nominal = calls * 2 * 50 * seconds
        require(result.get("nominal_packets") == nominal, "标称双向 RTP 包量与请求不一致")
        sent, received, ratio = (result.get(key) for key in ("sent_packets", "received_unique_packets", "offered_load_ratio"))
        require(number(sent) and number(received) and sent >= nominal * 0.98 and received == sent,
                "实际负载不足标称 98% 或发送与唯一接收包数不相等")
        require(number(ratio) and ratio >= 0.98 and abs(ratio - sent / nominal) < 1e-9, "发生器负载比例与真实包数不一致")
        for key in ("unreceived_packets", "duplicate_packets", "invalid_packets", "generator_write_errors", "unknown_rtp_packets", "unexpected_byes", "teardown_failures"):
            require(result.get(key) == 0, f"发生器存在异常或缺少必要计数：{key}")
        directions = result.get("directions")
        require(isinstance(directions, list) and len(directions) == 2, "报告缺少两个实际媒体方向")
        require({direction.get("source_side") for direction in directions if isinstance(direction, dict)} == {0, 1}, "报告媒体方向标识无效")
        for direction in directions:
            require(number(direction.get("sent")) and direction["sent"] > 0 and direction.get("received") == direction["sent"], "某一媒体方向没有真实流量或接收不完整")
        samples = report["run"].get("samples") or task.get("samples") or []
        require(any(isinstance(sample, dict) and number(sample.get("rx_packets")) and sample["rx_packets"] > 0
                    and number(sample.get("tx_packets")) and sample["tx_packets"] > 0 for sample in samples),
                "没有观察到 Rust 媒体进程的真实收发采样")
        check_cleanup(report["run"])
        outcome["verified"] = True
        self.check_main_unchanged()

    def run_phone(self):
        """验证单路三秒 PCMU、同请求幂等及相同键不同参数的冲突响应。"""
        payload = self.payload("phone-complete")
        identifier = self.start(payload)
        status, repeated = self.client.request("POST", "/v1/tests", payload)
        require(status == 200 and repeated.get("id") == identifier and repeated.get("request_id") == payload["request_id"],
                "相同幂等请求未返回 HTTP 200 和同一任务")
        changed = dict(payload, duration_seconds=4)
        status, _ = self.client.request("POST", "/v1/tests", changed)
        require(status == 409, "相同幂等键不同参数未返回 HTTP 409")
        self.summary["idempotency"] = {"same_request_http_status": 200, "changed_request_http_status": 409, "same_task_id": True}
        self.check_complete(identifier, 1, 3)
        # 完成后的同键重试也不得再次创建发生器。
        status, repeated = self.client.request("POST", "/v1/tests", payload)
        require(status == 200 and repeated.get("id") == identifier, "任务结束后同键重试重新创建了作业")

    def run_stop(self):
        """等单路十五秒场景真正进入媒体阶段后停止，只处理当前脚本创建的任务。"""
        identifier = self.start(self.payload("phone-stop", seconds=15))
        self.wait(identifier, want_media=True)
        self.read_owned(identifier)
        status, stopped = self.client.request("POST", task_path(identifier, "/stop"), {})
        require(status == 202 and stopped.get("id") == identifier, "媒体阶段停止请求未被正确受理")
        task = self.wait(identifier)
        check_cleanup(task)
        require(task.get("status") == "stopped", "停止请求后的终态不是 stopped")
        require(task.get("report_available") is True, "停止后没有可下载的诊断报告")
        report = self.report(identifier)
        require(report["run"].get("status") == "stopped", "停止报告与任务终态不一致")
        check_cleanup(report["run"])
        self.summary["stop_test"] = {"id": identifier, "media_observed": True, "stop_http_status": status,
                                     "status": "stopped", "cleanup_complete": True, "report_available": True}
        self.check_main_unchanged()

    def cleanup_owned(self):
        """错误或中断后的有界清理；只匹配本次已发送的唯一幂等键，绝不停止别人的任务。"""
        failures = []
        if not self.requests:
            return failures
        try:
            # POST 响应若丢失，恢复已受理的本次任务，避免不知道 ID 就遗留作业。
            overview = self.client.get("/v1/tests")
            for task in [overview.get("current"), *overview.get("history", [])]:
                if isinstance(task, dict) and task.get("request_id") in self.requests:
                    self.remember(task)
        except AcceptanceFailure:
            failures.append("无法从目录确认本次可能已受理的任务")
        for identifier in list(self.owned):
            try:
                task = self.read_owned(identifier)
                if task.get("status") not in TERMINAL:
                    status, response = self.client.request("POST", task_path(identifier, "/stop"), {})
                    require(status in (200, 202) and response.get("id") == identifier, "本次任务的补偿停止未被受理")
                    task = self.wait(identifier, timeout=30)
                check_cleanup(task)
            except AcceptanceFailure:
                failures.append("本次任务未能确认完整清理：" + identifier)
        return failures


def write_summary(path, summary):
    """只写显式选择的摘要文件，以 0600 临时文件原子替换；父目录必须已经存在。"""
    data = (json.dumps(summary, ensure_ascii=False, indent=2, allow_nan=False) + "\n").encode("utf-8")
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(prefix=".testlab-e2e-", dir=path.parent, delete=False) as output:
            temporary = Path(output.name)
            os.chmod(output.name, 0o600)
            output.write(data)
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def main():
    """先只读预检，再串行创建自身任务；所有出口都尝试核对清理与主配置不变。"""
    parser = argparse.ArgumentParser(description="验收回环管理 API 的隔离电话/媒体测试；默认不运行压力场景")
    parser.add_argument("--url", type=loopback_url, default="http://127.0.0.1:9080", help="仅限回环 HTTP 管理地址，默认 http://127.0.0.1:9080")
    parser.add_argument("--output", type=Path, required=True, help="验收摘要 JSON 路径；父目录须已存在，不包含令牌或原始配置")
    parser.add_argument("--pressure-calls", type=int, default=0, help="可选压力呼叫数，0 表示跳过，显式开启范围 1..10000")
    parser.add_argument("--seconds", type=int, default=3, help="仅压力场景的媒体秒数，范围 1..300，默认 3；单路固定 3 秒")
    parser.add_argument("--pressure-cps", type=int, default=100, help="仅压力场景每秒启动数，范围 1..1000，默认 100")
    parser.add_argument("--timeout", type=int, default=180, help="每个任务阶段的最多等待秒数，范围 30..900，默认 180")
    args = parser.parse_args()
    if not 0 <= args.pressure_calls <= 10000 or not 1 <= args.seconds <= 300 or not 1 <= args.pressure_cps <= 1000 or not 30 <= args.timeout <= 900:
        parser.error("并发、时长、CPS 或等待上限超出帮助文本范围")
    if not args.output.parent.is_dir():
        parser.error("摘要文件父目录不存在，请先创建操作者选择的目录")
    summary = {"schema_version": "1.0.0", "passed": False, "pressure_requested": args.pressure_calls > 0,
               "runs": [], "errors": [], "main_service_unchanged": None,
               "scope": "仅本次回环独立实例的 HTTP、SIP 与双向 RTP 验收，不构成万路或生产容量认证"}
    started = time.monotonic()
    acceptance = Acceptance(Client(args.url), summary, args.timeout)
    try:
        overview = acceptance.client.get("/v1/tests")
        require(not has_active_test(overview), "已有活跃测试，立即拒绝执行；不会停止其他任务")
        require(overview.get("enabled") is True, "测试入口当前不允许创建任务")
        acceptance.baseline = main_config(acceptance.client)
        acceptance.check_invalid_requests()
        acceptance.run_phone()
        acceptance.run_stop()
        if args.pressure_calls:
            payload = acceptance.payload("pressure", mode="pressure", calls=args.pressure_calls,
                                         cps=min(args.pressure_cps, args.pressure_calls), seconds=args.seconds)
            identifier = acceptance.start(payload)
            acceptance.check_complete(identifier, args.pressure_calls, args.seconds)
        acceptance.check_main_unchanged()
        summary["passed"] = True
    except AcceptanceFailure as error:
        summary["errors"].append(str(error))
    except KeyboardInterrupt:
        summary["errors"].append("操作者中断验收，将仅尝试清理本次创建的任务")
    except Exception:
        # 不输出异常对象或回溯，避免未来调用栈把内存配置或 HTTP 信息带入摘要。
        summary["errors"].append("验收工具遇到未预期异常，未记录可能包含敏感信息的原始异常")
    finally:
        summary["cleanup_errors"] = acceptance.cleanup_owned()
        if acceptance.baseline is not None:
            try:
                acceptance.check_main_unchanged()
            except AcceptanceFailure as error:
                summary["errors"].append(str(error))
        summary["passed"] = summary["passed"] and not summary["errors"] and not summary["cleanup_errors"]
        summary["elapsed_seconds"] = round(time.monotonic() - started, 3)
        acceptance.client.csrf = ""
        try:
            write_summary(args.output, summary)
        except (OSError, ValueError):
            print("验收摘要写入失败；未打印任何配置或令牌。")
            return 1
    print("验收通过，摘要已写入指定文件。" if summary["passed"] else "验收未通过，摘要已写入指定文件；未将不足负载当成成功。")
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
