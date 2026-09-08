#!/usr/bin/env python3
# 提取可复现的源码声明目录，不据此判定兼容性。
# 本工具不运行 C 预处理器，条件编译中的声明仍作为候选保留；
# 动态注册结果、模块构建组合和运行时配置必须另外盘点、实测。
# 下方原有模块 docstring 会被 argparse 读取为命令行帮助，保留其原文以维持外部输出。
"""Extract a reproducible declaration inventory; never a compatibility verdict.

No C preprocessor is executed. Conditional declarations remain candidates;
dynamic registration and runtime configuration require a separate inventory.
"""
import argparse
import ast
import csv
import hashlib
import json
import re
from collections import Counter
from pathlib import Path

# 固定参考提交，禁止静默跟随移动分支；每条定位链接与清单内容对应同一份源码。
COMMIT = "ef32e205295e29f034f1453ad245ba5efb07b94a"
URL = "https://github.com/signalwire/freeswitch/blob/" + COMMIT + "/"
# 优先识别字符串/字符字面量，再匹配注释，避免误删字面量中的 // 或 /*。
TOKEN = re.compile(r'"(?:\\.|[^"\\])*"|\'(?:\\.|[^\'\\])*\'|/\*[\s\S]*?\*/|//[^\n]*')
# STRING 只用于识别相邻的双引号字面量，不承担完整 C/C++ 表达式解析。
STRING = re.compile(r'"(?:\\.|[^"\\])*"')


def uncomment(text):
    """将 C/C++ 注释替换为等长空白并保留换行、字面量，使原始行号和字符偏移仍可定位。"""
    return TOKEN.sub(lambda m: re.sub(r"[^\n]", " ", m[0]) if m[0].startswith(("/*", "//")) else m[0], text)


def arguments(text, start):
    """从左括号后一位 start 拆分平衡的宏参数，返回参数列表和右括号后一位。

    调用前须先用 uncomment 去除注释；本函数跳过字符串/字符字面量，
    仅跟踪圆括号嵌套，不执行预处理，也不解析模板或任意 C++ 语法。
    """
    result, depth, mark, pos = [], 1, start, start
    while pos < len(text):
        if text[pos] in "\"'":
            quote = text[pos]
            pos += 1
            while pos < len(text):
                if text[pos] == "\\":
                    # 转义字符与其后一字符一同跳过，避免将转义引号误认成字面量结尾。
                    pos += 2
                    continue
                if text[pos] == quote:
                    break
                pos += 1
        elif text[pos] == "(":
            depth += 1
        elif text[pos] == ")":
            depth -= 1
            if depth == 0:
                result.append(text[mark:pos].strip())
                return result, pos + 1
        elif text[pos] == "," and depth == 1:
            # 内层函数调用的逗号仍属于当前宏参数，只在最外层括号拆分。
            result.append(text[mark:pos].strip())
            mark = pos + 1
        pos += 1
    # 对结构不完整的声明显式失败，避免把后续源码拼入错误条目却输出可信计数。
    raise ValueError("unterminated macro")


def literal(expression, definitions, visited=()):
    """解析纯字符串或本文件简单对象宏链；无法静态确认时返回 None。

    visited 阻止宏相互引用导致无限递归。仅用 literal_eval 解释字面量，
    不执行源代码；NULL、动态表达式和未解析的条件构建结果都须人工核验。
    """
    expression = expression.strip()
    if expression in definitions and expression not in visited:
        return literal(definitions[expression], definitions, visited + (expression,))
    pieces = STRING.findall(expression)
    if pieces and not STRING.sub("", expression).strip():
        # 只接受剔除所有字符串后没有剩余表达式的情况，支持相邻字面量拼接。
        try:
            return "".join(ast.literal_eval(piece) for piece in pieces)
        except (ValueError, SyntaxError):
            pass
    return None


def write_csv(path, rows, fields):
    """按明确字段顺序写 UTF-8 CSV；更完整的元数据保留在 JSON 中，额外字段不进入此表。"""
    with path.open("w", newline="", encoding="utf-8") as output:
        writer = csv.DictWriter(output, fieldnames=fields, extrasaction="ignore")
        writer.writeheader()
        writer.writerows(rows)


def main():
    """验证清单和文件哈希，扫描选定源码，输出带定位信息与未验证标记的声明候选目录。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    manifest = json.loads(args.manifest.read_text())
    # 清单必须属于固定提交且没有已知下载错误；逐文件校验是可复现性的前提。
    # 这证明本次选中文件一致，不等于清单囊括了所有依赖源码或运行时模块。
    if manifest["commit"] != COMMIT or manifest.get("errors"):
        parser.error("expected complete selected-file manifest for the pinned commit")
    files = []
    for item in manifest["files"]:
        path = args.source / item["path"]
        data = path.read_bytes()
        if hashlib.sha256(data).hexdigest() != item["sha256"]:
            parser.error("source hash mismatch: " + item["path"])
        files.append((item["path"], data.decode("utf-8", errors="replace")))
    # 列表保留声明出现位置及重复项；不能将条目数直接当作独立命令或导出符号总数。
    registrations, variables, symbols, configuration = [], [], [], []
    modules = {}
    for path, original in files:
        parts = path.split("/")
        module = parts[3] if path.startswith("src/mod/") else "core"
        if module != "core":
            # 这里统计选定源码中的模块目录和文件数，不代表当前实例实际构建或加载的模块数。
            modules.setdefault(module, {"module": module, "category": parts[2], "source_files": 0})["source_files"] += 1
        if path.endswith(".xml"):
            # 保留重复参数及精确行号；其所在 profile/binding 影响含义，不能视为扁平配置模式。
            # 仅扫描非注释 <param> 中双引号属性，不展开 XML 预处理、包含文件或动态配置。
            clean = re.sub(r"<!--[\s\S]*?-->", lambda m: re.sub(r"[^\n]", " ", m[0]), original)
            for match in re.finditer(r"<param\b([^>]*?)/?>", clean):
                attrs = dict(re.findall(r'([\w:-]+)\s*=\s*"([^"]*)"', match[1]))
                if "name" in attrs:
                    line = original.count("\n", 0, match.start()) + 1
                    configuration.append({"path": path, "line": line, "name": attrs["name"], "value_expression": attrs.get("value", ""), "source_url": URL + path + "#L" + str(line)})
            continue
        text = uncomment(original)
        # 拼接行续接仅用于简单对象宏值解析；定位扫描继续使用保留原始行结构的 text。
        # 定义表不执行 #if，遇到条件分支重名宏时仍必须结合实际构建检查结果。
        joined = text.replace("\\\n", "")
        definitions = dict(re.findall(r"^\s*#define\s+(\w+)[ \t]+([^\n]+)", joined, re.M))
        for match in re.finditer(r"\bSWITCH_ADD_(API|APP|JSON_API|CHAT_APP)\s*\(", text):
            # 跳过宏定义本身，保留条件编译调用点；此模式不覆盖直接接口赋值等非宏动态注册。
            line_start = text.rfind("\n", 0, match.start()) + 1
            if text[line_start:match.start()].lstrip().startswith("#"):
                continue
            values, _ = arguments(text, match.end())
            kind = match[1].lower()
            # APP/CHAT_APP 与 API/JSON_API 的参数位置不同，按各自声明签名读取，不能混成 API。
            expected = 7 if kind in ("app", "chat_app") else 5
            line = original.count("\n", 0, match.start()) + 1
            if len(values) != expected:
                raise ValueError(f"unexpected {kind} signature in {path}:{line}: {len(values)}")
            syntax_index = 5 if kind in ("app", "chat_app") else 4
            function_index = 4 if kind in ("app", "chat_app") else 3
            name = literal(values[1], definitions)
            syntax = literal(values[syntax_index], definitions)
            # name/syntax 是静态可解析值，*_expression 保留原表达式以供复核。
            # manual_resolution_required 也可能仅因 syntax 为 NULL，不能等同于命令名未知。
            # 注册声明不包含完整子命令、错误行为或运行状态，因此明确标记未捕获/未验证。
            registrations.append({"id": f"{kind}:{module}:{name or values[1]}:{line}", "kind": kind, "module": module, "name": name, "name_expression": values[1], "syntax": syntax, "syntax_expression": values[syntax_index], "handler": values[function_index], "flags_expression": values[-1] if kind in ("app", "chat_app") else "", "path": path, "line": line, "source_url": URL + path + "#L" + str(line), "resolution": "literal" if name is not None and syntax is not None else "manual_resolution_required", "behavior_contract": "not_captured", "runtime_status": "not_verified"})
        if path.startswith("src/include/"):
            # 仅统计符合命名模式且能解析为字符串的头文件宏，不是所有通道变量的全集。
            for match in re.finditer(r"^\s*#define\s+(SWITCH_\w*VARIABLE)\s+([^\n]+)", text, re.M):
                value = literal(match[2], definitions)
                if value is not None:
                    line = original.count("\n", 0, match.start()) + 1
                    variables.append({"macro": match[1], "name": value, "path": path, "line": line, "source_url": URL + path + "#L" + str(line)})
            for match in re.finditer(r"\bSWITCH_DECLARE(?:_NONSTD)?\s*\(", text):
                # switch_cpp.h 同样使用此宏声明未限定类名的方法，不能当成 C ABI 符号。
                # C++ 绑定要另审；本扫描也不展开 token-pasting 钩子宏或枚举所有 extern 符号。
                if path.endswith("/switch_cpp.h"):
                    continue
                line_start = text.rfind("\n", 0, match.start()) + 1
                if text[line_start:match.start()].lstrip().startswith("#"):
                    continue
                return_type, end = arguments(text, match.end())
                rest = re.match(r"\s*(\w+)\s*\(", text[end:])
                if rest:
                    params, _ = arguments(text, end + rest.end())
                    line = original.count("\n", 0, match.start()) + 1
                    symbols.append({"symbol": rest[1], "return_type": ",".join(return_type), "parameters": ", ".join(params), "path": path, "line": line, "source_url": URL + path + "#L" + str(line), "binary_abi": "not_verified"})
    # 仅定位指定事件枚举，保留 ALL 等标识，但不推断事件一定发出，也不枚举 CUSTOM 子类。
    event_source = dict(files)["src/include/switch_types.h"]
    event_clean = uncomment(event_source)
    enum = re.search(r"typedef enum\s*\{([^{}]*SWITCH_EVENT_ALL[^{}]*)\}\s*switch_event_types_t", event_clean, re.S)
    if not enum:
        raise ValueError("event enum not found")
    events = []
    for match in re.finditer(r"\bSWITCH_EVENT_(\w+)\b", enum[1]):
        line = event_source.count("\n", 0, enum.start(1) + match.start()) + 1
        events.append({"name": match[1], "classification": "subscription_sentinel" if match[1] == "ALL" else "enum_identifier_not_emission_guarantee", "path": "src/include/switch_types.h", "line": line, "source_url": URL + "src/include/switch_types.h#L" + str(line)})
    # ALL 是订阅哨兵；其余枚举标识也不能替代事件字段、触发顺序与过滤行为的差分验收。
    output = args.output
    output.mkdir(parents=True, exist_ok=True)
    # 所有计数都是本次选定文件中的声明/出现次数；尤其函数声明未去重，XML “active”仅指非注释。
    stats = {"registrations_by_kind": dict(Counter(r["kind"] for r in registrations)), "registration_sites": len(registrations), "manual_registration_resolution": sum(r["resolution"] != "literal" for r in registrations), "module_directories": len(modules), "event_enum_identifiers": len(events), "variable_macros": len(variables), "public_function_declarations": len(symbols), "active_vanilla_parameter_occurrences": len(configuration), "verified_reference_files": len(files)}
    data = {"reference_version": "1.11.3", "reference_commit": COMMIT, "scope": "source declaration candidates, NOT exhaustive runtime inventory or verified behavior", "counts": stats, "registrations": registrations, "modules": sorted(modules.values(), key=lambda r: r["module"]), "events": events, "variables": variables, "native_functions": symbols, "vanilla_parameters": configuration}
    # JSON 保存完整元数据；CSV 提供各界面的人工核对视图，二者都不写入“已兼容”的判断。
    (output / "source-catalog.json").write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n")
    write_csv(output / "api-and-applications.csv", registrations, ["id", "kind", "module", "name", "syntax", "syntax_expression", "handler", "flags_expression", "resolution", "behavior_contract", "runtime_status", "source_url"])
    write_csv(output / "modules.csv", data["modules"], ["module", "category", "source_files"])
    write_csv(output / "events.csv", events, ["name", "classification", "source_url"])
    write_csv(output / "channel-variable-macros.csv", variables, ["macro", "name", "source_url"])
    write_csv(output / "native-functions.csv", symbols, ["symbol", "return_type", "parameters", "binary_abi", "source_url"])
    write_csv(output / "vanilla-parameters.csv", configuration, ["path", "line", "name", "value_expression", "source_url"])
    print(json.dumps(stats, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    # 导入时只提供解析函数与常量，作为命令执行时才读取清单并生成目录文件。
    main()
