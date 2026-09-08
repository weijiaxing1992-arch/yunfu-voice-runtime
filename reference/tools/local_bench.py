#!/usr/bin/env python3
"""启动隔离配置的本机压测，保存发生器、服务状态与日志证据后清理本次服务。

同机调度和回环网络可能限制结果；它不能替代独立发生器与 Linux 实际网卡验收。
"""
import argparse
import json
from pathlib import Path
import signal
import socket
import subprocess
import time
import threading
import hashlib
import urllib.request

# 以脚本所在项目定位默认二进制和配置，不依赖调用者的当前工作目录。
ROOT = Path(__file__).resolve().parents[1]

# 临时绑定探测端口后立即关闭；返回的是候选端口，并不锁定到后续服务启动。
# 指定排除范围时，避免信令监听与媒体端口范围重叠。
def port(kind, excluded=None):
    with socket.socket(socket.AF_INET, kind) as s:
        if excluded is None:
            s.bind(("127.0.0.1", 0))
        else:
            for value in range(2000,10000):
                if excluded[0]<=value<=excluded[1]: continue
                try: s.bind(("127.0.0.1",value)); break
                except OSError: continue
            else: raise RuntimeError("no available local signaling port")
        return s.getsockname()[1]

# 修改前核对本次隔离配置和版本，提交后再独立回读；任何一步失败都不能继续执行压测。
# 只改 enabled，保留其他业务阈值和 Rust 媒体健康准入设置；令牌值仍受启动硬上限约束。
def set_guard_enabled(read, write, expected_config, enabled, expected_policy=None, attempt=None):
    snapshot = json.loads(read("/v1/config"))
    if snapshot.get("active") != expected_config:
        raise RuntimeError("管理端口的运行配置与本次隔离服务不一致，拒绝修改保护策略")
    revision = snapshot.get("revision")
    csrf = snapshot.get("csrf_token")
    guard = snapshot.get("guard")
    policy = guard.get("policy") if isinstance(guard, dict) else None
    if type(revision) is not int or revision < 1 or not isinstance(csrf, str) or not csrf or not isinstance(policy, dict) or type(policy.get("enabled")) is not bool:
        raise RuntimeError("管理接口未提供有效版本、令牌或保护策略，无法切换容量模式")
    if expected_policy is not None and policy != expected_policy:
        # 提交超时可能发生在服务实际写入之前；清理时若原策略仍在，无需再次写入。
        restored = dict(expected_policy)
        restored["enabled"] = enabled
        if policy == restored:
            return dict(policy), dict(policy)
        raise RuntimeError("隔离服务保护策略已被其他操作修改，拒绝覆盖")
    changed = dict(policy)
    changed["enabled"] = enabled
    if attempt is not None:
        # 在可能产生外部效果的请求之前记下恢复信息，覆盖“已提交但回复超时”的不确定结果。
        attempt.update(original=dict(policy), expected=changed)
    response = json.loads(write("/v1/guard", {"revision": revision, "policy": changed}, csrf))
    updated_revision = response.get("revision")
    if type(updated_revision) is not int or updated_revision <= revision or response.get("guard", {}).get("policy") != changed:
        raise RuntimeError("保护策略提交结果不匹配，停止容量验收")
    verified = json.loads(read("/v1/guard"))
    if verified.get("guard", {}).get("policy") != changed or verified.get("revision") != updated_revision:
        raise RuntimeError("保护策略未通过独立回读，停止容量验收")
    return dict(policy), changed

# 从命令行生成一次隔离压测配置，完整保存结果；任何证据缺失都不能提升为通过。
def main():
    p = argparse.ArgumentParser()
    p.add_argument("--calls", type=int, default=100)
    p.add_argument("--cps", type=int, default=100)
    p.add_argument("--seconds", type=int, default=10)
    p.add_argument("--phase-slots", type=int, default=20)
    p.add_argument("--sequential-ports", action="store_true")
    p.add_argument("--endpoint-ip", default="127.0.0.1", help="IPv4 address already configured on this test host")
    p.add_argument("--endpoint-connected", action="store_true")
    p.add_argument("--socket-groups", type=int, default=0)
    p.add_argument("--endpoint-port-start", type=int, default=0)
    p.add_argument("--endpoint-port-end", type=int, default=0)
    p.add_argument("--port-start", type=int, default=20000)
    p.add_argument("--port-end", type=int, default=43999)
    p.add_argument("--connected", action="store_true")
    p.add_argument("--workers", type=int, default=4)
    p.add_argument("--media", type=Path, default=ROOT / "bin/rustswitch-media")
    p.add_argument("--direct", action="store_true", help="diagnostic only: bypass Rust media")
    p.add_argument("--capacity-mode", action="store_true", help="本次隔离服务按启动硬上限压测；暂时关闭额外峰值保护，保留媒体健康准入")
    p.add_argument("--burst", action="store_true", help="synchronize all RTP packet phases")
    p.add_argument("--payload", type=int, choices=(0,8), default=0)
    p.add_argument("--work-dir", type=Path, default=ROOT / "run/bench")
    p.add_argument("--report", type=Path, default=ROOT / "run/bench/report.json")
    p.add_argument("--control", type=Path, default=ROOT / "bin/rustswitch")
    p.add_argument("--generator", type=Path, default=ROOT / "bin/callbench")
    a = p.parse_args()
    a.work_dir = a.work_dir.resolve(); a.report = a.report.resolve()
    a.work_dir.mkdir(parents=True, exist_ok=True); a.report.parent.mkdir(parents=True, exist_ok=True)
    # 信令两端和管理端口分开探测；上游 UDP 不能与本次媒体范围或信令端口重叠。
    sip_port = port(socket.SOCK_DGRAM, (a.port_start,a.port_end))
    upstream_port = port(socket.SOCK_DGRAM)
    admin_port = port(socket.SOCK_STREAM)
    while upstream_port == sip_port or a.port_start<=upstream_port<=a.port_end:
        with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as probe:
            probe.bind(("127.0.0.1",sip_port))
            upstream_port = port(socket.SOCK_DGRAM,(a.port_start,a.port_end))
    c = json.loads((ROOT / "config/local.json").read_text())
    # 仅修改本次复制的配置；不写回项目默认配置，也不更改主机已有网络地址。
    c["sip"].update(listen=f"127.0.0.1:{sip_port}", advertise=f"127.0.0.1:{sip_port}", upstream=f"127.0.0.1:{upstream_port}")
    c["limits"].update(max_calls=a.calls, calls_per_second=a.cps, burst_calls=max(a.calls, a.cps), max_transactions=max(10000, a.calls*8))
    c["media"].update(connect_sockets=a.connected, binary=str(a.media.resolve()), workers=min(a.workers,a.calls), port_start=a.port_start, port_end=a.port_end)
    c["sip"]["trusted_networks"] = ["127.0.0.0/8", a.endpoint_ip+"/32"]
    c["media"]["allowed_remote_networks"] = ["127.0.0.0/8", a.endpoint_ip+"/32"]
    c["admin"]["listen"] = f"127.0.0.1:{admin_port}"
    c["journal"]["path"] = str(a.work_dir / "events.jsonl")
    conf = a.work_dir / "config.json"; conf.write_text(json.dumps(c, indent=2))
    # 所有状态读取都限定为本次启动服务的本机管理端口，并设置短超时。
    def read(path):
        with urllib.request.urlopen(f"http://127.0.0.1:{admin_port}{path}",timeout=1) as r:
            return r.read()
    # 写接口同样只能访问脚本分配的回环地址，使用读取到的 CSRF 令牌和完整版本化请求。
    def write(path, body, csrf):
        request = urllib.request.Request(f"http://127.0.0.1:{admin_port}{path}", data=json.dumps(body).encode(), method="PUT", headers={"Content-Type": "application/json", "X-RustSwitch-CSRF": csrf})
        with urllib.request.urlopen(request, timeout=2) as response:
            return response.read()
    with (a.work_dir / "server.log").open("w") as log:
        server = subprocess.Popen([str(a.control.resolve()),"-config",str(conf)],cwd=ROOT,stdout=log,stderr=log)
        capacity_original = None
        capacity_expected = None
        capacity_attempt = {}
        try:
            # 等待可接收新呼叫的 readiness；进程提前退出时保留完整服务日志定位原因。
            ready = False
            for _ in range(160):
                if server.poll() is not None:
                    raise RuntimeError((a.work_dir/"server.log").read_text())
                try:
                    if json.loads(read("/readyz"))["ready"]:
                        ready=True;break
                except Exception:
                    time.sleep(.05)
            if not ready:
                raise RuntimeError("server readiness timed out")
            if a.capacity_mode:
                # 仅在本次子进程就绪后修改它的策略；失败直接进入清理，发生器还未启动。
                if server.poll() is not None:
                    raise RuntimeError("隔离服务在切换容量模式前已经退出")
                capacity_original, capacity_expected = set_guard_enabled(read, write, c, False, attempt=capacity_attempt)
            command=[str(a.generator.resolve()),"--server",c["sip"]["listen"],"--uas-listen",c["sip"]["upstream"],"--calls",str(a.calls),"--cps",str(a.cps),"--seconds",str(a.seconds),"--payload",str(a.payload),"--output",str(a.report)]
            command += ["--phase-slots",str(a.phase_slots),"--bind-ip",a.endpoint_ip,"--advertise-ip",a.endpoint_ip]
            command += ["--socket-groups",str(a.socket_groups),"--media-port-start",str(a.endpoint_port_start),"--media-port-end",str(a.endpoint_port_end)]
            if a.sequential_ports: command.append("--sequential-media-ports")
            if a.endpoint_connected: command.append("--connect-media")
            if a.direct:
                # direct 只作为发生器/本机网络诊断，实际绕过 Rust 媒体，不能认证服务器转发容量。
                command.append("--direct-media")
            if a.burst:
                command.append("--spread=false")
            stopped=threading.Event()
            samples=[]
            sample_errors=[]
            # 后台周期采样，采样失败单独计数；HTTP 高压下遗漏的峰值不能当作没有并发。
            def sample():
                while not stopped.is_set():
                    try: samples.append({"monotonic_seconds":time.monotonic(),"status":json.loads(read("/v1/status"))})
                    except Exception as error: sample_errors.append(str(error))
                    stopped.wait(.25)
            monitor=threading.Thread(target=sample,daemon=True); monitor.start()
            try:
                result=subprocess.run(command,cwd=ROOT,timeout=a.seconds+a.calls/a.cps+30,capture_output=True,text=True)
            finally:
                # 无论发生器成功、失败还是超时，都先停止采样并保留已取得的时间序列。
                stopped.set(); monitor.join(2)
                (a.work_dir/"timeline.json").write_text(json.dumps(samples,indent=2))
            print(result.stderr,end="");print(result.stdout,end="")
            (a.work_dir/"generator.log").write_text(result.stderr+result.stdout)
            state={}
            # 发生器结束后给正常拆呼和统计刷新短暂收敛窗口，再检查控制与媒体两侧资源。
            for _ in range(100):
                state=json.loads(read("/v1/status"))
                if state["active_calls"]==0 and all(w["stats"].get("active_calls")==0 for w in state["workers"]):
                    break
                time.sleep(.05)
            (a.work_dir/"status-after.json").write_text(json.dumps(state,indent=2))
            (a.work_dir/"metrics-after.txt").write_bytes(read("/metrics"))
            # 日志顺序由单个控制事件循环产生；复用目录时只认本次 run_id 的事件。
            # 短暂等待异步写入完成；半行日志或未同步事件意味着证据不足，不能报告通过。
            events=[]; journal_error=None
            for _ in range(20):
                try:
                    journal_text=(a.work_dir/"events.jsonl").read_text()
                    if not journal_text.endswith("\n"):
                        raise ValueError("incomplete journal tail")
                    events=[json.loads(line) for line in journal_text.splitlines() if line]
                    journal_error=None
                    break
                except (OSError, ValueError) as error:
                    journal_error=str(error)
                    time.sleep(.05)
            current_run=next((e["run_id"] for e in reversed(events) if e["kind"]=="controller_started"),None)
            established=set();journal_peak=0;run_events=0
            # 用建立/结束事件重建同时成立的呼叫集合，避免把累计接通过的总数误当峰值并发。
            for event in events:
                if event.get("run_id")!=current_run: continue
                run_events+=1
                if event["kind"]=="call_established": established.add(event["call_id"])
                if event["kind"]=="call_ended": established.discard(event["call_id"])
                journal_peak=max(journal_peak,len(established))
            for _ in range(20):
                state=json.loads(read("/v1/status"))
                if state.get("journal_synced_events",0)>=run_events: break
                time.sleep(.05)
            evidence={"journal_read_error":journal_error,"journal_run_id":current_run,"journal_run_events":run_events,"journal_unended_calls":len(established),"journal_synced_events":state.get("journal_synced_events"),"journal_healthy":state.get("journal_healthy"),"journal_peak_established_calls":journal_peak,"status_sampling_errors":len(sample_errors),"peak_sampled_established_calls":max((v["status"]["established_calls"] for v in samples),default=0),"configuration":c,"control_sha256":hashlib.sha256(a.control.read_bytes()).hexdigest(),"media_sha256":hashlib.sha256(a.media.read_bytes()).hexdigest(),"generator_sha256":hashlib.sha256(a.generator.read_bytes()).hexdigest(),"active_calls_after":state.get("active_calls"),"workers_after":state.get("workers"),"journal_lost_events":state.get("journal_lost_events"),"timeline_samples":len(samples),"peak_active_calls":max((v["status"]["active_calls"] for v in samples),default=0)}
            # 保留所有原有证据字段，并明确标记容量模式与压测结束时的实际保护策略。
            evidence.update(capacity_mode=a.capacity_mode, guard_policy=state.get("guard", {}).get("policy"), guard_after=state.get("guard"), guard_policy_before_capacity_mode=capacity_original)
            # 先写证据再判断失败，失败结果也必须关联原始配置和三个实际二进制的哈希。
            a.report.with_suffix(".evidence.json").write_text(json.dumps(evidence,indent=2))
            if a.capacity_mode and evidence["guard_policy"] != capacity_expected:
                raise RuntimeError("压测结束时保护策略与容量模式不一致，不能认定为容量验收")
            if result.returncode:
                raise RuntimeError(f"benchmark failed with exit {result.returncode}")
            if journal_error or not current_run or established or journal_peak < a.calls or state.get("journal_lost_events")!=0 or not state.get("journal_healthy") or state.get("journal_synced_events",0)<run_events:
                # 发生器通过仍不足够：日志要完整、健康、已同步，目标并发达到且最终正常结束。
                raise RuntimeError("server journal cannot verify the requested concurrent call count")
            if state.get("active_calls")!=0:
                raise RuntimeError("call reservations leaked after benchmark")
            if any(w["stats"].get("active_calls")!=0 for w in state["workers"]):
                raise RuntimeError("media allocations leaked after benchmark")
        finally:
            try:
                # 管理策略会持久化；恢复原开关，避免复用 work-dir 的下一次默认测试继承关闭状态。
                # 不覆盖测试期间的其他策略改动；恢复失败也必须报错，但仍确保子进程得到清理。
                restore_policy = capacity_attempt.get("original")
                applied_policy = capacity_attempt.get("expected")
                if restore_policy is not None and restore_policy != applied_policy and server.poll() is None:
                    set_guard_enabled(read, write, c, restore_policy["enabled"], expected_policy=applied_policy)
            finally:
                # 只清理本脚本启动的进程；第二次终止信号采用服务已有的强制退出语义。
                if server.poll() is None:
                    server.send_signal(signal.SIGTERM)
                    try:server.wait(4)
                    except subprocess.TimeoutExpired:
                        server.send_signal(signal.SIGTERM);server.wait(4)

# 导入模块不会启动服务；命令行直接执行时才开始压测生命周期。
if __name__ == "__main__":
    main()
