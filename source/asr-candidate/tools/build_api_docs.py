#!/usr/bin/env python3
"""生成管理后台内嵌文档与可下载文档包；--check 只检查，不修改文件。

原文维护于 docs/api、固定 FreeSWITCH 标准目录和 native/include。
本工具检查 HTTP 路由覆盖、文档引用与对照 ID，防止页面副本静默落后。
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import zipfile
import build_schema_reference
import build_verification_ledger
import build_runtime_verification
import build_conformance_report
import build_compatibility_progress

ROOT = Path(__file__).resolve().parents[1]
TARGET = ROOT / "control/internal/server/doc_assets"


def require(condition, message):
    """发布门禁不能使用 assert，避免 Python 优化模式跳过必要校验。"""
    if not condition:
        raise ValueError(message)


def json_bytes(value):
    """固定序列化形式，保证连续生成得到相同哈希和可审查的中文文本。"""
    return (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode()


def resolve_ref(spec, reference):
    """只检查当前 OpenAPI 中的本地 JSON Pointer，不请求外部网络或执行引用。"""
    if not reference.startswith("#/"):
        raise ValueError(f"OpenAPI 不允许未打包的外部引用：{reference}")
    current = spec
    for part in reference[2:].split("/"):
        current = current[part.replace("~1", "/").replace("~0", "~")]
    return current


def validate_spec(spec):
    """对照实际注册路由校验 operation，检查全部本地引用、响应与独立对照说明。"""
    require(spec["openapi"].startswith("3.1."), "必须为 OpenAPI 3.1")
    methods = {"get", "put", "post", "delete", "patch", "options", "head"}
    operations = {(method.upper(), name): op for name, item in spec["paths"].items() for method, op in item.items() if method in methods}
    source_routes = set()
    for source_file in sorted((ROOT / "control/internal/server").glob("*.go")):
        if source_file.name.endswith("_test.go"):
            continue
        source = source_file.read_text()
        source_routes.update(re.findall(r'HandleFunc\(\s*"(GET|PUT|POST|DELETE|PATCH|OPTIONS|HEAD) ([^" ]+)"', source))
    source_routes = {(method, name) for method, name in source_routes if name.startswith("/v1/") or name in {"/healthz", "/readyz", "/metrics"}}
    require(set(operations) == source_routes, f"文档与注册路由不同：{set(operations) ^ source_routes}")
    ids = [op["operationId"] for op in operations.values()]
    require(len(ids) == len(set(ids)), "operationId 必须唯一")
    for op in operations.values():
        require(op.get("summary") and op.get("description") and op.get("responses"), "操作说明不完整")
        require(op.get("x-freeswitch"), "每个管理操作都必须明确 FreeSWITCH 对照边界")
    def walk(value):
        """遍历实际节点而不展开引用，避免递归 schema 造成无限循环。"""
        if isinstance(value, dict):
            if "$ref" in value:
                resolve_ref(spec, value["$ref"])
            for child in value.values():
                walk(child)
        elif isinstance(value, list):
            for child in value:
                walk(child)
    walk(spec)
    return len(operations)


def validate_feature_audit(audit):
    """离线复核审计引用的本地源码与输入，源码包构建也不能发布已经过期的结论。"""
    expected = {item['path'] for item in audit['runtime_source_snapshot']}
    current = set()
    for pattern in ['control/internal/**/*.go', 'control/cmd/**/*.go', 'control/go.mod', 'control/go.sum', 'control/vendor/**/*', 'media/src/**/*.rs', 'native/include/*.h', 'native/examples/*.c', 'native/audio/*.c', 'native/audio/*.py'] + ['control/**/*.' + suffix for suffix in build_runtime_verification.GO_NATIVE_SUFFIXES]:
        current.update(path.relative_to(ROOT).as_posix() for path in ROOT.glob(pattern)
                       if path.is_file() and not path.name.endswith('_test.go') and '/web/' not in str(path) and '/doc_assets/' not in str(path))
    require(current == expected, '运行源码集合变化，请重新生成完整功能审计')
    for item in audit['runtime_source_snapshot'] + audit['inputs'] + audit['evidence']:
        path = ROOT / item['path']
        require(path.resolve().is_relative_to(ROOT.resolve()), '审计输入路径越界')
        require(path.is_file() and hashlib.sha256(path.read_bytes()).hexdigest() == item['sha256'],
                '功能审计证据过期，请重新生成：' + item['path'])
    require(len(audit['modules']) == audit['denominators']['audit_union_entries'], '功能审计模块数量不一致')


def collect():
    """精确收集公开资料；运行日志、配置状态、CSRF 和用户草稿不属于文档集。"""
    documents = {}
    for folder in ["api", "freeswitch-compatibility"]:
        for source in sorted((ROOT / "docs" / folder).rglob("*")):
            if source.is_file() and source.suffix in {".md", ".json", ".csv"}:
                documents[str(source.relative_to(ROOT / "docs"))] = source.read_bytes()
    for source in sorted((ROOT / "native/include").glob("*.h")):
        documents[str(source.relative_to(ROOT))] = source.read_bytes()
    # 音频依赖的构建说明、固定来源和原始许可随文档一起发布，下载后仍可追溯。
    for name in ['native/audio/README.md', 'native/audio/sources.json',
                 'native/audio/OPUS-COPYING', 'native/vendor/spandsp/COPYING']:
        documents[name] = (ROOT / name).read_bytes()
    documents["reference/LICENSE.freeswitch.txt"] = (ROOT / "control/internal/server/fs_templates/LICENSE.freeswitch").read_bytes()
    # RXS2使用固定x/sys公共时钟接口；随二进制文档保留实际vendor许可和专利声明。
    for filename in ['LICENSE', 'PATENTS']:
        documents['reference/x-sys-' + filename + '.txt'] = (ROOT / 'control/vendor/golang.org/x/sys' / filename).read_bytes()
    required = {"api/README.md", "api/http-reference.md", "api/protocol-reference.md", "api/freeswitch-comparison.md", "api/openapi.json", "api/comparison.json", "api/media-ipc.schema.json", "api/feature-audit.md", "api/feature-audit.json", "api/stability-reference.md"}
    require(required <= documents.keys(), f"缺少必要文档：{required - documents.keys()}")
    require(documents.get("api/schema-reference.md", b"").decode() == build_schema_reference.assemble(), "字段字典落后于机器合同")
    spec = json.loads(documents["api/openapi.json"])
    comparison = json.loads(documents["api/comparison.json"])
    validate_feature_audit(json.loads(documents["api/feature-audit.json"]))
    # 逐项账本必须与当前对照和本地输入完全一致，离线源码构建也不能发布旧结论。
    ledger = json.loads(documents["api/verification-ledger.json"])
    build_verification_ledger.validate_ledger(ledger, comparison)
    # 绿色只来自当前源码绑定的真实运行；离线重新核验内嵌证据，不能发布过期成功。
    build_runtime_verification.validate_runtime(ROOT)
    build_compatibility_progress.validate(ROOT)
    # 独立原版差分报告必须与当前源码、实际构建、原始报文及摘要一致。
    build_conformance_report.validate(ROOT)
    for item in ledger['inputs']:
        source = ROOT / item['path']
        require(source.resolve().is_relative_to(ROOT.resolve()) and source.is_file()
                and hashlib.sha256(source.read_bytes()).hexdigest() == item['sha256'],
                '逐项账本证据过期，请重新生成：' + item['path'])
    operations = validate_spec(spec)
    entries = comparison["entries"]
    require(len(entries) == comparison["summary"]["total"], "对照条目计数不一致")
    require(len({item["id"] for item in entries}) == len(entries), "对照 ID 重复")
    for item in entries:
        link = item.get("document_path", "").split("#")[0]
        require(not link or link in documents, f"对照引用了未发布文档：{link}")
    return documents, operations, comparison


def title_and_category(name, data):
    """优先采用原文章标题，让管理页标题与下载后的原文一致。"""
    if name.endswith(".md"):
        title = next((line[2:].strip() for line in data.decode().splitlines() if line.startswith("# ")), Path(name).name)
    else:
        title = {"api/openapi.json": "OpenAPI 3.1 完整接口模型", "api/comparison.json": "FreeSWITCH 全量接口对照数据", "api/media-ipc.schema.json": "媒体 IPC JSON Schema", "reference/LICENSE.freeswitch.txt": "FreeSWITCH 原始许可"}.get(name, Path(name).name)
    if name.startswith("api/"):
        category = "接口与对照文档"
    elif "/catalog/" in name:
        category = "FreeSWITCH 源码目录"
    elif "/templates/" in name:
        category = "兼容验收模板"
    elif name.startswith("freeswitch-compatibility/"):
        category = "FreeSWITCH 兼容标准"
    else:
        category = "原生契约与来源许可"
    return title, category


def assemble():
    """清单记录每份文件的来源版本、长度和哈希；内容包含范围说明，不生成兼容百分比。"""
    documents, operations, comparison = collect()
    listing = []
    for name, data in sorted(documents.items()):
        title, category = title_and_category(name, data)
        listing.append({"path": name, "title": title, "category": category, "format": Path(name).suffix[1:], "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()})
    # 发布清单沿用已校验 OpenAPI 版本，避免接口已升级而页面仍显示旧版。
    version = json.loads(documents["api/openapi.json"])["info"]["version"]
    require(version == build_compatibility_progress.VERSION, "接口文档与兼容进展版本不同")
    manifest = {"version": version, "reference_version": comparison["reference_version"], "reference_commit": comparison["reference_commit"], "documents": listing, "counts": {"http_operations": operations, "comparison_entries": len(comparison["entries"])}, "scope": "当前 RustSwitch 接口完整说明、固定 FreeSWITCH 源码目录逐项对照与兼容标准；动态模块运行全集和差分认证未完成。", "notice": "文档中的运行接口、内部契约、仅导出配置和待实现功能分别标注；目录齐全不代表运行行为兼容。"}
    return {**documents, "manifest.json": json_bytes(manifest)}, manifest


def main():
    """生成或检查发布副本；可同时生成离线 ZIP，不自动部署或重启服务器。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="只检查内嵌副本是否与原文一致")
    parser.add_argument("--zip", type=Path, help="额外写出完整文档 ZIP；不能与 --check 同用")
    args = parser.parse_args()
    if args.check and args.zip:
        parser.error("--check 不允许写出 ZIP")
    assets, manifest = assemble()
    existing = {str(p.relative_to(TARGET)) for p in TARGET.rglob("*") if p.is_file()} if TARGET.exists() else set()
    if args.check:
        require(existing == set(assets), "内嵌文档文件列表过期，请运行 tools/build_api_docs.py")
        for name, data in assets.items():
            require((TARGET / name).read_bytes() == data, f"内嵌文档落后于原文：{name}")
    else:
        # 仅清理本工具专属生成目录中的旧文件；绝不删除 docs 源文档。
        for name in existing - set(assets):
            (TARGET / name).unlink()
        for name, data in assets.items():
            output = TARGET / name
            output.parent.mkdir(parents=True, exist_ok=True)
            if not output.exists() or output.read_bytes() != data:
                output.write_bytes(data)
        if args.zip:
            args.zip.parent.mkdir(parents=True, exist_ok=True)
            with zipfile.ZipFile(args.zip, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
                for name, data in sorted(assets.items()):
                    entry = zipfile.ZipInfo(name, date_time=(2026, 9, 5, 0, 0, 0))
                    entry.compress_type = zipfile.ZIP_DEFLATED
                    entry.external_attr = 0o100644 << 16
                    archive.writestr(entry, data)
    print(json.dumps({"mode": "checked" if args.check else "generated", "documents": len(manifest["documents"]), **manifest["counts"]}, ensure_ascii=False))


if __name__ == "__main__":
    main()
