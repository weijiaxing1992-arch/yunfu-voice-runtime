#!/usr/bin/env python3
"""生成全部原条目的兼容测试计划，或在显式 profile 上执行有限成对协议探针。"""
import argparse
import importlib.util
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("rustswitch_conformance", ROOT / "tests/conformance/suite.py")
suite = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(suite)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", type=Path, help="生成完整 ID 测试计划 JSON；不执行产品")
    parser.add_argument("--check-plan", type=Path, help="检查已生成计划和当前原始清单一致")
    parser.add_argument("--profile", type=Path, help="显式隔离测试端点配置")
    parser.add_argument("--output", type=Path, help="成对实测报告 JSON；同目录同时写 Markdown")
    parser.add_argument("--probe", action="append", help="只执行指定探针；其他探针保留未执行状态")
    args = parser.parse_args()
    if sum(bool(value) for value in [args.plan, args.check_plan, args.profile]) != 1:
        parser.error("必须且只能指定 --plan、--check-plan 或 --profile")
    if args.profile and not args.output:
        parser.error("实测必须指定 --output 保存原始证据")
    try:
        if args.plan or args.check_plan:
            plan = suite.build_plan(ROOT)
            if args.check_plan:
                if json.loads(args.check_plan.read_text()) != plan:
                    raise suite.ProbeError("计划已过期或缺失条目，请重新生成")
            else:
                args.plan.parent.mkdir(parents=True, exist_ok=True)
                args.plan.write_text(json.dumps(plan, ensure_ascii=False, indent=2) + "\n")
            print(json.dumps({"action": "plan_only_not_executed", "counts": plan["counts"]}, ensure_ascii=False))
            return 0
        if args.output.exists() or args.output.with_suffix(".md").exists():
            raise suite.ProbeError("证据输出已经存在；请使用新路径，禁止覆盖历史失败结果")
        report = suite.run_suite(ROOT, json.loads(args.profile.read_text()), selected=args.probe)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        args.output.with_suffix(".md").write_text(suite.render_report(report))
        print(json.dumps({"output": str(args.output.resolve()), "counts": report["counts"], "all_defined_probes_passed": report["all_defined_probes_passed"], "full_compatibility_passed": False}, ensure_ascii=False))
        # 0 只允许“本工具已定义全部有限探针通过”，不代表 3979 项或完整产品兼容。
        return 0 if report["all_defined_probes_passed"] else 2
    except (OSError, ValueError, suite.ProbeError) as error:
        print(json.dumps({"state": "failed_input_or_evidence", "error": str(error)}, ensure_ascii=False), file=sys.stderr)
        return 3


if __name__ == "__main__":
    raise SystemExit(main())
