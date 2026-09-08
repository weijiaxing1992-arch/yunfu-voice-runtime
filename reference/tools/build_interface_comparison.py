#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""生成固定 FreeSWITCH 参考与当前 RustSwitch 的逐项接口对照，不执行服务或兼容性测试。"""

import argparse
from collections import Counter
import hashlib
import html
import importlib.util
import json
from pathlib import Path
import re

REFERENCE_COMMIT = "ef32e205295e29f034f1453ad245ba5efb07b94a"
REFERENCE_VERSION = "1.11.3"
SOURCE_URL = "https://github.com/signalwire/freeswitch/blob/" + REFERENCE_COMMIT + "/"
STATUSES = {"implemented", "partial", "export_only", "not_implemented", "internal_only", "not_verified"}
RELATIONSHIPS = {"direct_comparison", "analogous_not_wire_compatible", "export_artifact_only", "no_compatible_entrypoint", "internal_contract_only", "acceptance_requirement_only"}
FIELDS = {"id", "category", "name", "module", "fs_interface", "fs_syntax", "fs_semantics", "rustswitch_interface", "rustswitch_semantics", "status", "relationship", "differences", "verification", "source_url", "source_path", "source_line", "document_path"}
BASELINE_COUNTS = {"registrations": 538, "modules": 144, "events": 94, "variables": 100, "native_functions": 1865, "vanilla_parameters": 875, "subcommands": 130, "acceptance_definitions": 177}
LABELS = {"fs_api": "API 注册", "fs_application": "拨号计划应用注册", "fs_json_api": "JSON API 注册", "fs_chat_application": "聊天应用注册", "fs_module": "源码模块目录", "fs_event": "事件枚举标识", "fs_channel_variable": "通道变量宏", "fs_native_function": "原生函数声明", "fs_vanilla_parameter": "vanilla 参数出现位置", "fs_conference_subcommand": "会议子命令", "fs_sofia_subcommand": "Sofia 子命令候选", "acceptance_definition": "兼容验收定义", "key_http": "关键对照：HTTP", "key_esl": "关键对照：ESL", "key_config": "关键对照：配置", "key_guard": "关键对照：峰值保护", "key_sip": "关键对照：SIP", "key_media": "关键对照：媒体", "key_ipc": "关键对照：内部 IPC", "key_c_abi": "关键对照：C ABI", "key_cli": "关键对照：CLI"}
STATUS_LABELS = {"implemented": "RustSwitch 自有接口已实现", "partial": "仅有部分相关能力", "export_only": "仅编辑/导出", "not_implemented": "未实现该兼容入口", "internal_only": "仅内部协议", "not_verified": "未执行对应兼容验收"}
LABELS["key_api"] = "关键对照：私有自有 API"
STANDARD = "docs/freeswitch-compatibility/"
DOCUMENT = "docs/api/freeswitch-comparison.md"


class ComparisonBuilder:
    """持有已校验的输入快照；仅从文件取证，不查询生产服务，也不运行原版命令。"""

    def __init__(self, project, reference):
        self.project = project
        self.reference = reference
        catalog = project / STANDARD / "catalog"
        self.catalog = json.loads((catalog / "source-catalog.json").read_text())
        self.subcommands = json.loads((catalog / "conference-and-sofia-subcommands.json").read_text())
        self.cases = json.loads((catalog / "conformance-cases.json").read_text())["case_definitions"]
        self.manifest = json.loads((catalog / "reference-source-manifest.json").read_text())
        if self.manifest["commit"] != REFERENCE_COMMIT or self.manifest.get("errors"):
            raise ValueError("参考清单不是完整的固定提交选择集")
        if self.catalog["reference_commit"] != REFERENCE_COMMIT or self.subcommands["reference_commit"] != REFERENCE_COMMIT:
            raise ValueError("来源目录提交不一致")
        # 逐文件验证 SHA-256，防止使用移动分支或本地改动过的第三方源码产生错误定位。
        self.sources = {}
        for item in self.manifest["files"]:
            data = (reference / item["path"]).read_bytes()
            if hashlib.sha256(data).hexdigest() != item["sha256"]:
                raise ValueError("参考源码校验失败：" + item["path"])
            self.sources[item["path"]] = data.decode("utf-8", errors="replace")
        self.entries = []
        self.key_entries = []
        self.registration_index = {(row["kind"], row["name"]): row for row in self.catalog["registrations"] if row["name"] is not None}
        # 复用已有的受限宏解析器补充注册时的描述表达式，不伪造未提取的处理器语义。
        spec = importlib.util.spec_from_file_location("rustswitch_source_inventory", project / STANDARD / "tools/build_catalog.py")
        self.extractor = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.extractor)
        self.registration_descriptions = {}
        self._read_registration_descriptions()

    def _read_registration_descriptions(self):
        """保留真实宏参数中的短/长描述，无法解析时保留表达式并明确标记。"""
        for path in sorted({row["path"] for row in self.catalog["registrations"]}):
            clean = self.extractor.uncomment(self.sources[path])
            definitions = dict(re.findall(r"^\s*#define\s+(\w+)[ \t]+([^\n]+)", clean.replace("\\\n", ""), re.M))
            for match in re.finditer(r"\bSWITCH_ADD_(API|APP|JSON_API|CHAT_APP)\s*\(", clean):
                start = clean.rfind("\n", 0, match.start()) + 1
                if clean[start:match.start()].lstrip().startswith("#"):
                    continue
                values, _ = self.extractor.arguments(clean, match.end())
                line = clean.count("\n", 0, match.start()) + 1
                kind = match[1].lower()
                indices = (2, 3) if kind in ("app", "chat_app") else (2,)
                descriptions = []
                for index in indices:
                    resolved = self.extractor.literal(values[index], definitions)
                    descriptions.append(resolved if resolved is not None else "【未解析描述表达式】" + values[index])
                self.registration_descriptions[(path, line, kind)] = descriptions

    def source(self, path, needle=None):
        """定位固定源码行；指定锚文本时必须找到，不以猜测行号或搜索结果代替证据。"""
        content = self.sources[path]
        if needle is None:
            return path, 1
        for line, text in enumerate(content.splitlines(), 1):
            if needle in text:
                return path, line
        raise ValueError(f"参考源码锚点缺失：{path}：{needle}")

    def registration(self, name, kind="api"):
        """以注册类别和名称定位原版入口，API 与 JSON API 的同名项保持区分。"""
        row = self.registration_index[(kind, name)]
        return row["path"], row["line"]

    def local_line(self, path, needle):
        """定位本项目源码或标准定义；文档调整后重新寻找 ID，不沿用可能过期的旧行号。"""
        for number, line in enumerate((self.project / path).read_text().splitlines(), 1):
            if needle in line:
                return number
        raise ValueError(f"本地证据锚点缺失：{path}：{needle}")

    def add(self, identifier, category, name, module, fs_interface, fs_syntax, fs_semantics, rustswitch_interface, rustswitch_semantics, status, relationship, differences, verification, source, document=DOCUMENT, local_source=False):
        """形成统一的 17 字段记录；空 source_url 只用于本地编写的验收定义。"""
        source_path, source_line = source
        entry = dict(id=identifier, category=category, name=name, module=module, fs_interface=fs_interface, fs_syntax=fs_syntax, fs_semantics=fs_semantics, rustswitch_interface=rustswitch_interface, rustswitch_semantics=rustswitch_semantics, status=status, relationship=relationship, differences=differences, verification=verification, source_url="" if local_source else SOURCE_URL + source_path + "#L" + str(source_line), source_path=source_path, source_line=source_line, document_path=document)
        self.entries.append(entry)
        return entry

    def baseline_entries(self):
        """逐条保留八组原始来源；目录数量是声明覆盖分母，不是功能完成分母。"""
        categories = {"api": "fs_api", "app": "fs_application", "json_api": "fs_json_api", "chat_app": "fs_chat_application"}
        contracts = {"api": "同步 API 注册，处理器写返回正文；ESL api 的帧与业务成功状态另行核验", "app": "通道拨号计划应用，执行状态、变量、副作用及完成事件需按处理器验收", "json_api": "JSON API 注册，JSON 输入/输出合同独立于同名文本 API", "chat_app": "聊天消息应用注册，不是语音通道执行应用或通用 API"}
        for row in self.catalog["registrations"]:
            name = row["name"] if row["name"] is not None else "【名称未解析】" + row["name_expression"]
            syntax = row["syntax"] if row["syntax"] is not None else "【语法未解析】" + row["syntax_expression"]
            descriptions = self.registration_descriptions[(row["path"], row["line"], row["kind"])]
            semantic = contracts[row["kind"]] + "。处理器：" + row["handler"] + "。源码注册描述：" + "；".join(descriptions)
            raw_syntax = syntax + "\n原始语法表达式：" + row["syntax_expression"] + "\n原始名称表达式：" + row["name_expression"]
            if row["flags_expression"]:
                raw_syntax += "\n应用标志表达式：" + row["flags_expression"]
            entry = self.add("registration:" + row["id"], categories[row["kind"]], name, row["module"], row["kind"] + " " + name, raw_syntax, semantic, "未提供此 FreeSWITCH 注册入口", "当前 HTTP 路由和受限 SIP 状态机不注册此原版命令/应用；同名 JSON 字段或相近媒体能力不能调用该处理器。", "not_implemented", "no_compatible_entrypoint", ["注册描述和帮助字符串不包含完整错误、权限、事件与竞态合同。", "实际模块构建/加载、动态注册和每个子命令仍须原版运行发现。"], "固定源码声明已定位；RustSwitch 无 FreeSWITCH API/应用分发宿主；该条目完整原版行为差分仍未采齐；已执行子场景另见成对报告。", (row["path"], row["line"]), STANDARD + "05-api-contracts.md")
            if row["kind"] == "api" and row["module"] == "mod_commands" and name in {"echo", "create_uuid", "version", "status", "show", "uuid_exists", "uuid_getvar", "uuid_setvar", "uuid_setvar_multi", "uuid_break", "uuid_kill", "uuid_dump", "uuid_send_dtmf"}:
                entry.update(status="partial", relationship="direct_comparison", rustswitch_interface="入站 ESL api/bgapi " + name, rustswitch_semantics="已实现有限分派；版本与status如实返回RustSwitch；UUID操作仅覆盖当前双腿通话与基本变量/正常拆线。", differences=["完整原版命令参数、权限、错误、并发及事件分支尚未全部差分。", "变量数组/展开、完整挂机原因及原版完整status格式不支持；有界额度超限会显式失败。"], verification="真实执行结果与剩余分支见成对测试报告，不将单一echo或框架测试推广为全命令兼容。", document_path="api/esl-reference.md")
        for row in self.catalog["modules"]:
            paths = sorted(path for path in self.sources if path.startswith("src/mod/") and path.split("/")[3] == row["module"])
            self.add("module:" + row["module"], "fs_module", row["module"], row["module"], "src/mod/" + row["category"] + "/" + row["module"], "源码目录；无单一调用语法", f"选定源码含 {row['source_files']} 个文件，分类 {row['category']}；这是目录条目，不能据此断言模块已编译、可加载或存在同名运行模块。", "未提供原版模块加载宿主", "RustSwitch 使用固定 Go 服务与独立 Rust worker；仅有自定义编解码动态库检查器，不实现该目录对应的 FreeSWITCH 模块生命周期。", "not_implemented", "no_compatible_entrypoint", ["144 个目录包含 SDK 辅助目录，不等于 144 个已加载模块。", "应分别采集模块启动/关闭、接口注册、依赖与忙碌卸载行为。"], "目录和选定源码文件计数已核对；未执行 show modules、load/unload 或旧模块二进制装载。", self.source(paths[0]), STANDARD + "04-sip-media-native.md")
        event_notes = {"ALL": "订阅全集哨兵，不是独立应发事件", "CUSTOM": "自定义事件类型；子类名称和字段由模块/运行路径扩展", "CLONE": "内部克隆相关枚举标识，不能当作独立业务事件保证", "BACKGROUND_JOB": "后台 API 完成关联事件；Job-UUID、正文和失败结果须联合验证", "CHANNEL_CREATE": "通道创建生命周期标识", "CHANNEL_ANSWER": "通道应答生命周期标识", "CHANNEL_HANGUP_COMPLETE": "通道挂机完成阶段标识，字段和话单时序须运行对照", "CHANNEL_EXECUTE_COMPLETE": "通道应用执行完成阶段标识"}
        for row in self.catalog["events"]:
            note = event_notes.get(row["name"], "核心事件枚举标识；发出条件、字段、多值头、订阅格式和顺序尚未由枚举目录捕获")
            entry = self.add("event:" + row["name"], "fs_event", row["name"], "core", "SWITCH_EVENT_" + row["name"], "event plain|json|xml " + row["name"] + "；实际可订阅性需基线验证", note, "无 ESL 事件订阅入口", "当前 /v1/status 与本地 journal JSONL 是不同的数据模型；没有此事件名的 ESL 帧、过滤器或通道事件合同。", "not_implemented", "no_compatible_entrypoint", ["94 含 ALL 与 CLONE，不能解释为 94 种已验证可发业务事件。", "CUSTOM 子类、事件字段与慢消费者行为不由该列表穷尽。"], "已核对 switch_event_types_t 声明；无原版事件轨迹或 RustSwitch ESL 差分。", (row["path"], row["line"]), STANDARD + "02-esl-events.md")
            if row["name"] in {"CHANNEL_CREATE", "CHANNEL_ANSWER", "CHANNEL_HANGUP", "BACKGROUND_JOB"}:
                entry.update(status="partial", relationship="direct_comparison", rustswitch_interface="入站ESL event plain/json/xml " + row["name"], rustswitch_semantics="从实际SIP状态机或后台任务发布；支持连接级普通过滤和有界输出，完整字段/原版时序尚未齐全。", differences=["仅实现此事件的有限字段和触发路径。", "完整原版模块来源、通道应用、多值头及全部竞态仍需验证。"], verification="执行器与真实SIP/Rust媒体结果另见本轮报告；不能把单一事件子场景当作完整生命周期认证。", document_path="api/esl-reference.md")
        for row in self.catalog["variables"]:
            self.add("variable:" + row["macro"] + ":" + row["path"] + ":" + str(row["line"]), "fs_channel_variable", row["name"], "core", row["macro"], row["macro"] + " → " + json.dumps(row["name"], ensure_ascii=False), "头文件中的通道变量名称宏；具体读取者、默认值、作用域、继承及写入时机须沿引用处理器展开，不能从名称推断全部行为。", "无 FreeSWITCH 通道变量空间", "现有启动 JSON、GuardPolicy 和内部会话字段不是 uuid_getvar/uuid_setvar、set/export 或 ${...} 通道变量机制。", "not_implemented", "no_compatible_entrypoint", ["100 是可静态解析的命名宏声明位置，含 99 个唯一宏名称；未包含模块动态变量和用户自定义变量。", "不把相似 SIP/媒体配置字段自动映射为该通道变量。"], "宏名称和值保留并定位；变量作用域及原版客户端读写未执行差分。", (row["path"], row["line"]), STANDARD + "03-config-dialplan-integrations.md")
        for row in self.catalog["native_functions"]:
            signature = row["return_type"] + " " + row["symbol"] + "(" + row["parameters"] + ")"
            self.add("native:" + row["symbol"] + ":" + row["path"] + ":" + str(row["line"]), "fs_native_function", row["symbol"], "core/public_headers", row["symbol"], signature, "公开头文件函数声明；精确保留返回类型与参数文本。内存所有权、锁约束、回调线程、结构布局和错误值不能仅由声明确认。", "无该 FreeSWITCH 原生符号兼容实现", "rs_codec_get_v1/rs_codec_v1 是独立 C ABI；其描述符与回调不满足此函数符号及原版宿主布局。", "not_implemented", "no_compatible_entrypoint", ["1865 为声明位置，包含重复声明；不是唯一导出符号数。", "不含全部宏生成钩子、extern 或 C++ 绑定；未执行动态符号/布局/旧模块装载对照。"], "声明来源已定位，binary_abi 未验证；未建立兼容符号转发或原版模块宿主。", (row["path"], row["line"]), STANDARD + "04-sip-media-native.md")
        for index, row in enumerate(self.catalog["vanilla_parameters"]):
            relative = row["path"].removeprefix("conf/vanilla/")
            self.add("vanilla:" + row["path"] + ":" + str(row["line"]) + ":" + str(index), "fs_vanilla_parameter", row["name"], relative, row["path"] + " : param[name=" + row["name"] + "]", '<param name="' + row["name"] + '" value="' + row["value_expression"] + '"/>', "原版 vanilla 配置中一个非注释参数出现位置；value 保留表达式。所属 XML 结构、预处理结果和实际模块决定运行语义。", "GET/PUT /v1/fs-config；GET/PUT /v1/fs-config/file；GET /v1/fs-config/export", "可在相应文件中编辑和导出该参数；表单 ID 为当前文件字段次序，须从当前 revision 目录读取，不能使用本对照 ID 写入。当前引擎不执行此 XML 参数。", "export_only", "export_artifact_only", ["源参数行号不是管理表单字段序号；原版包含 875 个 param 出现位置，管理表单含其他标签共 4304 字段。", "修改、导出与模块运行生效是独立步骤；不存在通用 reloadxml 等价调用。"], "对应基线 XML 已内嵌；189 文件解析/导出回归已记录于 docs/management-v0.3.md；各参数业务语义未做原版差分。", (row["path"], row["line"]), STANDARD + "03-config-dialplan-integrations.md")
        groups = ("conference_table", "conference_global_branches", "sofia_parser_branches", "sofia_help_only")
        for group in groups:
            for index, row in enumerate(self.subcommands[group]):
                conference = group.startswith("conference")
                purpose = row.get("purpose_zh", "帮助文本候选，未找到已确认的分发路径")
                semantic = purpose + "。证据类型：" + row["evidence_kind"] + "。作用域提示：" + row.get("scope_hint", "尚未确认")
                if row.get("handler"):
                    semantic += "。处理器：" + row["handler"] + "；分发模式：" + row["dispatch_mode"]
                if row.get("notes"):
                    semantic += "。备注：" + row["notes"]
                syntax = row["command_path"] + " " + row["arguments_expression"]
                if row.get("help_display_name"):
                    syntax += "\n表内分发名：" + row["dispatch_name"] + "；帮助显示名：" + row["help_display_name"]
                self.add("subcommand:" + group + ":" + str(index) + ":" + row["command_path"], "fs_conference_subcommand" if conference else "fs_sofia_subcommand", row["command_path"], "mod_conference" if conference else "mod_sofia", row["command_path"], syntax, semantic, "无该原版子命令分发入口", "管理页面可保存会议/Sofia XML，但不创建会议、启动 profile、注册网关或执行成员控制。", "not_implemented", "no_compatible_entrypoint", ["会议静态表 84 条是表声明完整；6 个全局分支、38 个 Sofia 路径和 2 个仅帮助候选的证据等级不同。", "权限、完整参数分支、返回正文、错误与事件仍须逐处理器运行补齐。"], "固定源码表/分支定位已保留；permission_status=" + row["permission_status"] + "；runtime_status=" + row["runtime_status"], (row["path"], row["line"]), STANDARD + "catalog/conference-and-sofia-subcommands.md")
        for row in self.cases:
            document = STANDARD + row["source"]
            line = self.local_line(document, "| " + row["id"] + " |")
            self.add("acceptance:" + row["id"], "acceptance_definition", row["id"], row["kind"], row["input_or_scenario"], "验收定义，非运行命令；输入组合需进一步实例化", row["assertions"], "兼容验收待实现/运行", "已有 RustSwitch 单元和 e2e 结果不能自动填入这一原版差分用例；容量 profile 还需目标 Linux 服务器与真实媒体负载。", "not_verified", "acceptance_requirement_only", ["177=169 条场景定义+8 个容量 profile，均不是当前已执行测试数量。", "缺少原版轨迹、构建配置、客户端产物或任一强制断言时不能判通过。"], "源用例状态=" + row["status"] + "；comparison 仅重新定位定义，未执行用例。", (document, line), document, local_source=True)

    def key(self, group, slug, name, source, fs_interface, fs_syntax, fs_semantics, rs_interface, rs_semantics, status, differences, local_path, local_needle, evidence="静态实现已核对；本条目完整成对认证未完成，已执行子场景另见成对报告", relationship=None):
        """加入人工语义对照，支持状态仅说明当前这一入口，不能传播为整个原版模块已兼容。"""
        identifier = "key-" + group + "-" + slug
        line = self.local_line(local_path, local_needle)
        if relationship is None:
            relationship = "export_artifact_only" if status == "export_only" else "internal_contract_only" if status == "internal_only" else "no_compatible_entrypoint" if status == "not_implemented" else "analogous_not_wire_compatible"
        entry = self.add(identifier, "key_" + group, name, "cross_interface", fs_interface, fs_syntax, fs_semantics, rs_interface, rs_semantics, status, relationship, differences, f"{evidence}；RustSwitch 源码 {local_path}:{line}。", source, DOCUMENT + "#" + identifier)
        self.key_entries.append(entry)

    def critical_entries(self):
        """人工维护的关键合同：逐项列输入、输出、错误、副作用与不能直接替换的原因。"""
        S = self.source
        A = self.registration
        K = self.key
        admin = "control/internal/server/admin.go"
        store = "control/internal/server/admin_store.go"
        server = "control/internal/server/server.go"
        guard = "control/internal/server/guard.go"
        calls = "control/internal/server/calls.go"
        xml = "control/internal/server/fs_config.go"
        sip = "control/internal/sip/sdp.go"
        worker = "media/src/media/worker.rs"
        protocol = "media/src/media/protocol.rs"
        es = "src/mod/event_handlers/mod_event_socket/mod_event_socket.c"
        sf = "src/mod/endpoints/mod_sofia/mod_sofia.c"
        rtp = "src/switch_rtp.c"
        core = "src/include/switch_core.h"
        load = "src/include/switch_loadable_module.h"
        native = S(load, "struct switch_loadable_module_interface")
        sofia = A("sofia")
        lifecycle = S(es, "SWITCH_EVENT_BACKGROUND_JOB")
        regression = "实现静态核对；本项目相关回归记录见 docs/management-v0.3.md；不是 FreeSWITCH 差分通过"
        K("http", "codecs", "音频能力目录", A("show"), "show codecs / codec 模块", "api show codecs", "列出原版实际加载的编解码实现；编码、解码、格式协商与实时转码必须分别验证。", "GET /v1/codecs", "读取同格式RTP配置与当前原生PCM SDK后端探测；每个控制实例缓存一次，缺库明确标记。", "implemented", ["HTTP JSON 不兼容 ESL show codecs 的帧与正文。", "离线后端可用不表示通话转码已接入，不能原样加载 FreeSWITCH 模块。"], "control/internal/server/codecs.go", "func (s *Server) getCodecs", regression)
        K("http", "health", "进程存活与版本", A("version"), "API version/status", "api version [short]", "原版 version 返回其构建/版本正文，受调用层 ESL 帧封装；status 是另一个状态命令。", "GET /healthz", "HTTP 200 JSON 含 status=ok 与 RustSwitch 版本；只证明管理入口响应，不证明媒体分片可服务。", "implemented", ["HTTP 状态与 JSON 不是 api/response；不得伪装 FreeSWITCH 版本。", "原 fs_cli 无法将此端点当作 ESL 连接。"], server, "func (s *Server) health", regression)
        K("http", "readiness", "可准入状态", A("status"), "API status / fsctl", "api status", "原版状态正文包含其会话和运行信息；没有与本项目 /readyz 字段完全相同的内置 HTTP 合同。", "GET /readyz", "JSON ready；未排空、日志健康、至少一个媒体分片可准入且 Guard 当前允许时为 200，否则 503。", "implemented", ["200 不预留下一通呼叫资源。", "按当前保护策略预期拒接也会造成 503，不能只按进程存活判定故障。"], server, "func (s *Server) ready", regression)
        K("http", "status", "运行状态查询", A("status"), "API status / show channels", "api status；api show channels", "status 与 show 提供原版状态及查询格式；通道记录与桥接通话数量需按原版字段区分。", "GET /v1/status", "返回 active_calls、established_calls、workers、guard、journal_*、配置版本等 JSON 快照；不返回原版 UUID 通道列表。", "implemented", ["一条 RustSwitch 桥接通话有 A/B 两腿，不能直接按原版 session/channel 数字比较。", "轮询采样不能替代 CHANNEL_CREATE/ANSWER/HANGUP 事件。"], server, "func (s *Server) status", regression)
        K("http", "metrics", "指标采集", A("status"), "API status / 启用的指标模块", "按原版实际加载模块采集", "原版 status 和相关模块指标按各自字段与采集路径输出；不能假定 Prometheus 指标名一致。", "GET /metrics", "Prometheus 文本输出 sip_*、calls_*、media/worker 和 rustswitch_guard_* 计数；计数区分累计值和即时值。", "implemented", ["既有监控面板需要字段映射，当前未提供原版指标别名。", "socket_rx_drops 不支持时零值不等于内核无丢包。"], server, "func (s *Server) metrics", regression)
        K("http", "authentication", "管理鉴权与请求来源", S(es, "Content-Type: auth/request"), "ESL auth / ACL", "auth <password>\\n\\n", "inbound ESL 先发 auth/request，再处理连接身份、ACL 和可用命令；认证状态属于 TCP 连接。", "回环 HTTP；GET /v1/config 获取 CSRF", "检查 Host；写请求检查同源 Origin、application/json、X-RustSwitch-CSRF；没有多用户身份、ESL 密码或角色权限。", "implemented", ["CSRF token 不是登录凭据，不支持把远程服务直接暴露到公网。", "ESL 客户端认证帧不能发送到 HTTP 管理监听。"], admin, "func (s *Server) protectAdmin", regression)
        K("http", "config-read", "读取有效与待重启配置", A("global_getvar"), "global_getvar / 模块配置读取", "api global_getvar <var>", "原版全局变量查询返回变量正文，与 XML 配置或模块已加载状态并非同一结构。", "GET /v1/config", "返回 revision、csrf_token、active、desired、restart_required、guard、persisted、notice；所有页共享 revision。", "implemented", ["active/desired 是完整启动 JSON，并非 FreeSWITCH 变量空间。", "读取不会触发重载或自动合并旧草稿。"], admin, "func (s *Server) adminConfigLocked", regression)
        K("http", "config-write", "保存完整启动草稿", A("reloadxml"), "reloadxml / 模块重载", "api reloadxml", "原版 reloadxml 处理 XML 树重载；模块是否重新读取配置及在途通话影响由各模块决定。", "PUT /v1/config {revision,config}", "完整校验后保存 desired；监听、端口和 worker 仍按 active 运行，下一次正常启动采用；程序和日志路径由部署文件管理。", "implemented", ["保存成功不等于即时应用，不能用作 reloadxml 的返回替代。", "400/409/422/500 是 HTTP 管理错误，不是原版 +OK/-ERR 正文。"], admin, "func (s *Server) putAdminConfig", regression)
        K("http", "concurrency", "并发提交与错误", A("reloadxml"), "各原版 API 的独立并发语义", "原版命令没有通用 HTTP revision 字段", "原版 API 不能被全局假设为具有统一事务版本或幂等键；错误正文和副作用需要逐命令定义。", "配置 PUT 的 revision", "持管理锁比较版本；旧版本 409 并返回当前 revision；一次成功提交递增，保存失败不发布旧文件之外的新状态。", "implemented", ["ESL Job-UUID 是后台关联号，不能映射成配置 revision。", "客户端不能拿新 revision 自动重发过期完整表单，否则会覆盖别人的修改。"], admin, "func (s *Server) revisionConflictLocked", regression)
        K("esl", "framing", "ESL 字节分帧", S(es, "Content-Type: api/response"), "mod_event_socket TCP", "头行 + 空行 + Content-Length 字节正文", "命令响应与异步事件交错，按字节数分帧，认证和权限属于连接。", "可选 -esl-listen 回环入站 ESL", "Go 有界 TCP 解析器处理 LF/CRLF、分片、粘包、UTF-8 正文及认证；单写者保持帧完整。", "partial", ["头上限64KiB与原版不同，正文上限16MiB。", "仅回环密码认证，无userauth、远程ACL与outbound会话控制；实际差分见成对测试报告。"], "control/internal/esl/frame.go", "func ReadFrame")
        K("esl", "api", "同步 API 调用", S(es, "Content-Type: api/response"), "ESL api", "api <command> [args]\\n\\n", "返回api/response帧与实际命令正文，完成传输不等于业务成功。", "入站ESL api：echo/create_uuid/version/status/show api as json/uuid_exists/uuid_getvar/uuid_setvar/uuid_setvar_multi/uuid_break/uuid_kill/uuid_dump/uuid_send_dtmf及自有uuid_send_dtmf_status", "有限命令分派；UUID控制串行进入通话主循环并按UUID索引定位。未知命令返回-ERR，不伪造FreeSWITCH版本。", "partial", ["status为RustSwitch实际状态格式；不是原版status输出。", "普通变量受128个/64KiB预算限制；无数组、展开、全原因码、originate、conference或任意应用。"], "control/internal/server/esl_api.go", "func (s *Server) CompatibilityAPI")
        K("esl", "bgapi", "后台 API 与作业完成", lifecycle, "ESL bgapi", "bgapi <command> [args]；Job-UUID", "受理与BACKGROUND_JOB完成分别回应，关联号不具备天然幂等语义。", "入站ESL bgapi及BACKGROUND_JOB", "固定8执行器、有界128任务队列；Job-UUID两处确认，实际命令结果作为事件正文；重复关联号分别执行。", "partial", ["只运行当前有限API集合；后台受理不保证业务成功。", "任务不持久化；进程退出取消，未提供崩溃后任务恢复或原版所有作业字段。"], "control/internal/esl/server.go", "func (s *Server) worker")
        K("esl", "sendmsg", "通道应用执行", S(es, '"sendmsg"'), "ESL sendmsg", "sendmsg <uuid>；call-command: execute；execute-app-name / execute-app-arg", "请求在指定通道执行应用，event-lock、执行 UUID 和 CHANNEL_EXECUTE_COMPLETE 等行为需联合对照。", "无通道应用执行端点", "当前 Go 会话只实现固定两腿呼叫状态推进，不运行 playback、record_session、bridge 等拨号计划应用。", "not_implemented", ["媒体 worker allocate/connect 不具备应用栈和通道 UUID 语义。", "无应用完成、排队、打断或并发应用控制事件。"], calls, "func (s *Server) handle")
        K("esl", "subscriptions", "事件订阅与过滤", S(es, '"filter "'), "event / filter / myevents", "event plain|json|xml <events>；filter <header> <value>", "连接级订阅、头过滤、事件编码及慢消费者边界须联合验证。", "入站ESL事件订阅、普通值过滤、nixevent/noevents", "从真实通话、应用及按键发布CHANNEL_CREATE/ANSWER/HANGUP、CHANNEL_EXECUTE/COMPLETE、PLAYBACK_START/STOP、CHANNEL_PARK/UNPARK和DTMF；后台任务发布BACKGROUND_JOB；本地呼入仅A腿。", "partial", ["仅上述实际事件源；未完成CHANNEL_HANGUP_COMPLETE、CUSTOM、日志及myevents。", "正则过滤、完整原版字段和多值头仍未实现；输出队列溢出断开连接以显式报告流中断。"], "control/internal/esl/server.go", "func (s *Server) Publish")
        K("esl", "outbound", "Outbound ESL 会话与 linger", S(es, '"linger"'), "socket 应用 / outbound ESL", "socket <host:port> [async] [full]；connect；linger", "原版由通道连接业务服务；连接初始化、权限、断连与通话生命周期以及 linger 保留期均是可观察合同。", "无 outbound ESL", "固定 SIP 上游不是 ESL 业务连接；管理 HTTP 客户端断开不会创建或接管原版通道应用。", "not_implemented", ["原业务服务监听 socket 不能接入当前 RustSwitch。", "活动 ESL TCP 状态在线迁移尚未实现。"], calls, "func (s *Server) invite")
        K("config", "runtime-xml", "XML 运行与预处理", S("src/switch_xml.c", "switch_xml_open_cfg"), "XML 树 / X-PRE-PROCESS", "conf/freeswitch.xml 与 include/set 预处理", "FreeSWITCH 在相应配置加载路径解析 XML，预处理变量与模块配置查找参与实际运行。", "GET/PUT /v1/fs-config", "XML 仅作草稿解析、字段展示和导出；保留 X-PRE-PROCESS 文本，不执行 include、set 或模块加载。", "export_only", ["响应明确 mode=export_only、runtime_supported=false。", "表单覆盖不等于 XML 业务执行兼容。"], xml, "func parseFSFile", regression)
        K("config", "parameter-edit", "逐字段编辑与原文保留", S("conf/vanilla/vars.xml"), "原版 param/variable/action/condition 等 XML", "原始属性值和 $${...} 表达式", "不同标签、节点位置及执行顺序决定语义；相同 name 在不同 profile/绑定中可能重复出现。", "PUT /v1/fs-config {revision,overrides:{ID:value}}", "按当前字段 ID 从后往前替换属性值并做 XML 转义；保留未编辑文本；重复属性拒绝，命名空间元数据不混入字段。", "export_only", ["ID 是当前文件内字段序号，不能从原版行号或本对照 id 猜测。", "结构修改后必须重新取目录及 revision。"], xml, "func patchFSParameters", regression)
        K("config", "raw-xml", "高级 XML 保存", S("src/switch_xml.c", "switch_xml_open_cfg"), "FreeSWITCH XML 文件", "相对 XML 路径与原始内容", "原版配置最终是否可运行取决于模块 schema、预处理与环境；XML 语法正确不保证业务正确。", "PUT /v1/fs-config/file {revision,path,xml}", "支持修改/新增，多根片段；禁止目录穿越、DTD、非 UTF-8 声明、重复属性；256 KiB/文件、512 文件、5 MiB 总草稿。", "export_only", ["未知文件 GET 404，非法 XML/路径 422；高级保存不会写入真实 FreeSWITCH conf 目录。", "不会做全模块参数类型、凭证可用性或拨号计划结果验证。"], admin, "func (s *Server) putFSFile", regression)
        K("config", "export", "配置 ZIP 导出", S("conf/vanilla/freeswitch.xml"), "原版 vanilla 配置树", "conf/ 目录", "原版配置包含模块、profile、目录与拨号计划，实际运行仍依赖对应构建和资源。", "GET /v1/fs-config/export", "导出已保存基线合并草稿的 ZIP，带许可与来源；189 份参考 XML，五份仅含 ASCII 的旧编码声明统一 UTF-8。", "export_only", ["下载未部署、未 reloadxml，未保存的浏览器草稿不进入 ZIP。", "4,304 个管理字段不等于 4,304 个不同功能。"], xml, "func exportFSConfig", regression)
        K("config", "directory", "用户、网关与拨号计划", A("bridge", "app"), "directory / Sofia gateway / XML dialplan", "user id；gateway；condition/action bridge", "原版目录认证、网关注册和拨号计划执行分别由对应运行模块处理，并产生信令、通道及事件副作用。", "用户/网关/路由 XML 草稿和导出", "可生成查看XML文本但不执行；独立sip.registration可注册固定上游，local_extensions可精确本地接听，均不是XML配置的运行映射。", "export_only", ["生成一个用户 XML 不会让 REGISTER 成功。", "保存 bridge action 不会改变当前 SIP 上游或创建动态桥接。"], calls, "func (s *Server) invite")
        K("config", "persistence", "持久化与启动文件优先级", A("reloadxml"), "原版配置文件与模块重载", "模块各自的配置加载合同", "FreeSWITCH 不采用本项目的 desired/policy/fs_files 管理状态封装；不能假定相同重启覆盖顺序。", "journal.path + .admin.json", "临时文件同步后原子重命名；下次启动按启动文件摘要选 desired。部署文件改变时采用新 base，保留策略并只按新硬上限收紧，保留可读取的 XML 草稿。", "implemented", ["更改 journal.path 会改变状态文件位置，不自动查找迁移旧草稿。", "目录 fsync 失败会警告；不承诺任何断电条件下零配置丢失。"], store, "func openAdminStore", regression)
        K("guard", "capacity", "总并发硬边界与业务阈值", A("fsctl"), "fsctl max_sessions", "fsctl max_sessions [value]", "原版核心 session 上限作用于其会话资源；一通桥接常涉及多个通道，不能与桥接通话数直接换算。", "PUT /v1/guard policy.max_active_calls", "在线阈值不能超过 active/desired 的 limits.max_calls；计数为预留且尚未释放的桥接资源，包括建立阶段。降低阈值不会挂断已有通话。", "implemented", ["max_active_calls 与原版 max_sessions 口径不同。", "当前存量大于新阈值时只拒绝新 INVITE，不削减存量。"], guard, "func (g *AdmissionGuard) limitReason", regression)
        K("guard", "establishing", "建立中保护", A("fsctl"), "核心会话/准入限制", "须按原版具体 profile 与模块配置采集", "原版建立中会话统计及限制需要按已加载模块和通道状态确定；没有已证明等价的统一 establishing 字段。", "policy.max_establishing_calls", "采用 max(active_calls-established_calls,0)，包括结束后仍等待资源释放的呼叫；达到阈值拒绝新 INVITE。", "implemented", ["不能解释为每秒产生 200 OK 的最大数。", "瞬时原子快照采用饱和减法，统计不是原版通道状态全集。"], guard, "func establishingCount", regression)
        K("guard", "rate-burst", "CPS 与突发令牌", A("fsctl"), "fsctl sps", "fsctl sps [value]", "原版核心每秒会话限制与其计数/定时实现关联；不能从名称 sps 推断相同令牌补充算法。", "policy.calls_per_second / burst_calls", "新 INVITE 通过准入时扣一个令牌；按单调时间补充并限制容量；重传、ACK、BYE 不重复扣令牌，保存不自动补满。", "implemented", ["CPS 是新准入速率，不是并发数或严格自然秒接通数。", "瞬时突发可大于一秒平均量，但不越配置 burst。"], guard, "func (g *AdmissionGuard) TryAdmit", regression)
        K("guard", "soft-throttle", "负载接近阈值时缩速", A("fsctl"), "fsctl min_idle_cpu / 原版准入策略", "按原版核心及 profile 配置", "原版 CPU/会话准入需要按其实现与配置验证，不等同于本项目按两个资源比例进行线性缩速。", "policy.soft_limit_ratio / minimum_rate_ratio", "以 active/max_active 与 establishing/max_establishing 的较大值计算有效补充速率；enabled=false 仍保留启动硬容量/CPS/burst。", "implemented", ["默认 0.8 后开始缩速，0.9 时为基准的 55%；到完整阈值仍拒绝。", "不调度或延迟已经产生的 200 OK。"], guard, "func (g *AdmissionGuard) effectiveRate", regression)
        K("guard", "retry-after", "过载拒绝与重试", sofia, "SIP 过载响应与 profile 限制", "503 / Retry-After，精确原因按原版场景采集", "原版不同资源/认证/路由失败可能有不同状态、头和原因映射；须冻结各场景的外部响应。", "新 INVITE 的 503 + Retry-After", "保护、启动容量或无媒体可准入时使用当前建议间隔；同一事务缓存响应；对端自行决定新事务重试，无服务端等待队列。", "partial", ["Retry-After 不保证届时一定存在容量。", "其他 SIP 错误不能统一变成此过载响应。"], calls, "func (s *Server) rejectAdmission", regression)
        K("guard", "drain-resume", "排空与恢复新接入", A("fsctl"), "fsctl pause/resume/shutdown", "pause/resume [inbound|outbound]；shutdown elegant 等", "原版区分入向/出向暂停与不同退出模式，其任务、模块和通道退出条件需逐模式验证。", "POST /v1/drain；POST /v1/resume", "排空停止新准入，保留已有通话且不自动退出；恢复不要求 revision，已进入停止流程返回 409。", "implemented", ["没有原版全部方向选择、shutdown cancel/restart 模式。", "排空完成不等于活动呼叫、ESL TCP 状态在线迁移。"], admin, '"POST /v1/drain"', regression)
        K("sip", "invite", "可信中继两腿呼叫", sofia, "Sofia endpoint / bridge/originate", "INVITE + SDP；路由依 profile/dialplan", "原版终端、网关、拨号计划和 originate 可选择不同目的地、变量与多腿路径，并产生原版通道事件。", "SIP UDP/TCP/TLS INVITE → 固定 sip.upstream", "只接受配置可信 CIDR 的初始请求；创建 A/B 两腿和四媒体端口，处理受限响应/ACK/释放；不执行 XML 路由或 originate API。", "partial", ["可信中继白名单不是用户认证系统。", "无按用户/号码执行原版 dialplan、脚本与通道变量。"], calls, "func (s *Server) invite", regression)
        K("sip", "register", "用户注册与 Digest", S("src/mod/endpoints/mod_sofia/sofia_reg.c", "void sofia_reg_handle_sip_i_register"), "Sofia REGISTER / Digest", "REGISTER；401/407 challenge；Authorization", "原版注册路径处理注册位置、到期、认证、联系人及 profile/目录相关行为。", "REGISTER 当前不支持", "允许方法列表只有 INVITE、ACK、CANCEL、BYE、OPTIONS；REGISTER 落入 405。XML 用户草稿不产生可认证账户。", "not_implemented", ["不能直接接替需要注册认证的终端或现有网关账号。", "nonce、注册刷新、多 Contact、NAT 注册绑定均未实现。"], calls, 'case "OPTIONS":')
        K("sip", "transports", "传输与 WebSocket", sofia, "Sofia profile传输配置", "SIP UDP/TCP/TLS/WS/WSS按构建启用", "原版传输涉及连接复用、证书、重连与SIP事务，不能只检查监听端口。", "IPv4 SIP UDP及可选TCP/TLS双腿", "sip.stream启用可靠传输；真实连接身份进入事务与缓存，固定队列和连接预算；TLS校验CA/名称。", "partial", ["尚无WS/WSS、IPv6、DNS/SRV、客户端证书认证及完整SIPS路由。", "TLS仅保护信令，不表示SRTP/WebRTC已实现；INVITE 2xx端到端ACK重传仍保留。"], "control/internal/server/transport.go", "type sipFlow")
        K("sip", "renegotiation", "reINVITE/UPDATE 与媒体重协商", sofia, "Sofia 对话内重协商", "reINVITE / UPDATE + SDP", "原版对话内更新可影响保持、编解码、远端地址、会话计时与媒体状态；竞态结果须按场景比较。", "对话内 INVITE → 501", "已有 To-tag 的重 INVITE 明确拒绝；内部 Connect 存在并不代表 SIP 重协商接口已支持。UPDATE 不在允许方法列表。", "not_implemented", ["hold/resume、codec 切换与 NAT 重协商不能无感沿用。", "内部请求 id 不提供 SIP CSeq 对应的协商版本控制。"], calls, '"Re-INVITE Not Implemented"')
        K("sip", "extensions", "PRACK、REFER、订阅与扩展", sofia, "Sofia SIP 扩展", "100rel/PRACK；REFER；SUBSCRIBE/NOTIFY；Session-Expires", "原版可靠临时响应、转接、订阅通知与会话刷新分别具有对话、计时和事件合同。", "受限 SIP 方法与扩展检查", "PRACK/REFER/SUBSCRIBE/NOTIFY 等不在当前方法集；不支持的必需扩展/部分 SDP 情况明确拒绝。", "not_implemented", ["当前早期媒体不意味着实现 PRACK/100rel。", "没有盲转、咨询转接、会话刷新或订阅状态接管。"], calls, '"Unsupported SDP or SIP Extension"')
        K("sip", "cancel", "CANCEL 与迟到成功应答", sofia, "Sofia INVITE/CANCEL 事务", "CANCEL；487；迟到 2xx 后 ACK/BYE", "原版须区分事务取消、对话成功和分叉胜出腿，不能收到 CANCEL 就撤销所有成功会话。", "匹配初始 INVITE 的 CANCEL", "匹配分支取消；最终应答已发则只确认 CANCEL；上游迟到 200 使用 ACK/BYE 清理。", "partial", ["现有回归只覆盖固定上游受限两腿场景。", "多目标 fork/CANCEL race 与原版完整允许结果集合仍未差分。"], calls, "func (s *Server) cancelA", regression)
        K("sip", "bye", "正常拆线与原因码", A("uuid_kill"), "SIP BYE / uuid_kill", "BYE；api uuid_kill <uuid> [cause]", "原版可由 SIP 或 API 指定挂机原因，影响 SIP/Reason、通道原因和挂机事件。", "SIP BYE 与内部结束原因", "支持受限双腿 BYE/事务清理和内部 journal 原因；无 uuid_kill API，也没有完整 Q.850/SWITCH_CAUSE 映射。", "partial", ["当前 reason 文本不能替代 hangup_cause/originate_disposition。", "收到 200 不等于已完成所有原版 CDR/录音/事件副作用。"], calls, "func (s *Server) bye", regression)
        K("sip", "sdp", "SDP 与编解码协商", sofia, "Sofia/core media SDP", "m=audio；rtpmap/fmtp；方向；传输与安全属性", "原版 SDP 能力按编解码、媒体安全及 profile 配置组合选择；多媒体、方向和重协商均需验证。", "sip.ParseSDP / RenderSDP", "识别 G.711、G.722、Opus、G.729、G.726/AAL2、L16，分开音频采样率与RTP时钟，并校验ptime/fmtp及DTMF/CN；只桥接同编码同PT，不做隐式转码。", "partial", ["不是完整 SDP offer/answer 实现，不能复用全部 FreeSWITCH 终端配置。", "媒体目的 CIDR 校验不等于 ICE 或加密身份认证。"], sip, "func ParseSDP", regression)
        K("media", "rtp", "主流音频同格式 RTP 转发", S(rtp), "原版 RTP / proxy/bypass media", "按 SDP 与媒体模式建立 RTP", "原版媒体模式可能处理、代理或绕过 RTP；序列号、SSRC、时间戳、jitterbuffer 与 RTP 重写依场景而定。", "Rust worker 两腿 RTP 转发", "校验 RTP 结构、载荷与对端，转发原数据报；当前通话路径不解码/混音/转码。Linux 用同 fd recvmmsg 接收。", "partial", ["不能把透传与原版所有媒体模式视为同一行为。", "发送计数只表示本地系统调用接受，不能承诺对端已收到。"], worker, "fn receive(", regression)
        K("media", "rtcp", "RTCP 转发与统计", S(rtp), "原版 RTCP", "独立/复用 RTCP 按协商配置", "原版可处理 RTCP 报告及媒体质量相关行为，具体生成、转发、复用依模式与协商。", "独立 RTCP 端口透传", "检查复合 RTCP 基本结构并转发；每腿 RTCP 单独限额；当前 RTP/RTCP 必须使用不同端口。", "partial", ["不实现完整原版 RTCP 报告生成、质量事件或 rtcp-mux。", "端到端 RTCP 回归仅证明现有透传路径。"], "media/src/media/rtp.rs", "pub fn valid_rtcp", regression)
        K("media", "dtmf", "DTMF 的媒体与业务层", A("playback", "app"), "telephone-event / SIP INFO / 应用收号", "RTP telephone-event；INFO；应用 DTMF 队列", "原版支持的 DTMF 传输、生成、检测及应用收号会影响媒体、通道队列和业务事件。", "协商 telephone-event RTP 透传", "校验协商的动态载荷、事件集合、时钟与RTP telephone-event结构并透传；不生成原版 DTMF 业务事件，也不提供 INFO、音内检测或收号应用。", "partial", ["数据报到达不等于 play_and_get_digits 等业务收号兼容。", "持续时间、结束位重复、转接期间 DTMF 行为仍需完整基线测试。"], protocol, "dtmf_payload: Option<u8>", regression)
        K("media", "transcoding", "转码通话", S(core, "switch_core_codec_init_with_bitrate"), "FreeSWITCH codec interfaces", "协商两腿不同 codec/ptime", "原版通过编解码实现与媒体路径连接不同格式，需对帧时长、丢包处理、缓冲及质量进行测试。", "无通话转码路径", "提供 G.711 与可选 G.722/Opus 的离线可变采样率音频 SDK（8/16/24/48k；按分支转换）；媒体 worker 不调用解码图处理呼叫数据，不能桥接不同编解码。", "not_implemented", ["插件加载成功与转码服务可用是不同验收项。", "转码万路性能不能由 G.711 透传吞吐推算。"], worker, "Command::Allocate", regression)
        K("media", "security", "SRTP、DTLS 与 ICE", S(rtp), "原版安全媒体/WebRTC", "SRTP/DTLS-SRTP；ICE/STUN/TURN 按模块配置", "原版安全媒体需要协商密钥、认证报文与连接候选，并按安全配置管理生命周期。", "当前普通 UDP RTP", "未实现加解密、DTLS 握手、ICE 候选检查或 TURN；可信网段和固定来源过滤不提供等价安全保证。", "not_implemented", ["不能接替需要 WebRTC/SRTP 的现有终端。", "不得把媒体 IP 白名单称为媒体身份认证。"], worker, "fn check_peer")
        K("media", "nat", "NAT 对端学习与迁移", sofia, "Sofia NAT / symmetric RTP", "profile NAT 设置与实际源学习", "原版 NAT 行为依 profile、终端 Contact、SDP、连接及媒体来源策略共同确定。", "固定协商对端 + 可选 connected UDP", "只接受协商并经过 CIDR 校验的来源；不自动学习新 NAT 源地址。对端变化时清理旧待发队列。", "partial", ["NAT 端口改变会被拒绝，不能声称移动终端无感。", "connected socket 由内核提前过滤的包不会全部反映在应用 source_rejected 中。"], worker, "fn check_peer")
        K("media", "conference", "会议与成员控制", A("conference"), "conference API / application", "conference <name> <subcommand> [args]", "原版会议维护房间、成员、混音/视频、成员权限及会议事件；静态表包含 84 个分发项。", "会议 XML 编辑/导出", "没有运行会议对象、成员管理、混音或会议事件；不能使用原 conference 命令控制媒体 worker。", "export_only", ["130 子命令目录中的会议/Sofia 项均未成为当前运行接口。", "会议容量与点对点透传容量必须分别验收。"], xml, "func fsCapabilities")
        K("media", "recording", "录音 API、事件和文件", A("uuid_record"), "uuid_record / record_session", "uuid_record <uuid> start|stop|mask|unmask <path> ...", "原版录音合同包括文件内容、声道、格式、错误、停止时机、变量和相关事件，不能只返回命令成功。", "录音 XML 编辑/导出；无录音执行", "现有 journal 是文本事件日志，不是通话音频文件；没有录音句柄、掩码、录音事件或音频产物。", "export_only", ["不能用 journal_written_events 证明录音成功。", "磁盘满、转接跟随、文件完整性与原后端对接尚未实现。"], xml, "func fsCapabilities")
        K("media", "capacity", "一万路功能组合验收", A("status"), "原版负载基线 + 业务场景", "并发、CPS、ptime、codec、功能组合、时长与故障条件", "原版实际构建、配置和业务组合决定容量，必须采集真实媒体及控制负载后比较。", "callbench / local_bench.py / collect_linux.py", "1.9新增本地按键发送与可变采样率SDK后尚无5000路完整Agent组合验收；上轮1.5最新5000路复测未通过：最新仅提供48.14184%标称媒体量，另有8次发生器发送写错误。1.4两轮成功仅为历史记录，不沿用当前容量绿色；万路、物理服务器及转码/会议/录音组合仍未验证。", "not_verified", ["并发信令会话数不能替代双向媒体 100 万包/秒量级的真实提交与接收核对。", "透传、转码、会议、录音和事件订阅需分 profile 验收。"], "control/cmd/callbench/main.go", "passed :=")
        K("media", "failover", "故障隔离与活动状态迁移", A("fsctl"), "原版恢复/切换合同", "按实际部署定义故障与恢复行为", "是否支持恢复取决于原版模块、状态存储与切换方式，不能假定任何服务器可无损复制活动 SIP/媒体/ESL 状态。", "Rust worker 分进程监管与重启", "失败分片上的呼叫会异常结束并清理；其他分片可继续。控制进程退出会结束媒体子进程，尚无活动会话或 ESL TCP 在线迁移。", "partial", ["进程重启恢复新服务能力不等于恢复同一通活动呼叫。", "排空旧呼叫后切换与不停话在线迁移必须分别验收。"], calls, "func (s *Server) workerFailed", regression)
        K("config", "dialplan-runtime", "XML 本地拨号计划执行", A("answer", "app"), "mod_dialplan_xml / mod_dptools", "context → extension → condition → action", "原版XML支持条件、应用、预处理和模块绑定；实际匹配和执行产生SIP/媒体副作用。", "sip.dialplan {file,context}", "启动冻结受限XML；destination_number首个RE2条件命中后顺序执行answer/set/unset/read/playback/sleep/park/hangup；未匹配404，ACK后继续，关闭ESL仍可运行。", "partial", ["仅单条件且首动作为answer，无预处理、外部include、反向动作、变量/捕获展开、continue或reloadxml。", "park不可处理原版私有抢占事件；无bridge/originate/transfer及全部原版应用。"], "control/internal/server/dialplan.go", "package server", "限定真实场景见dialplan-reference与本地/原版成对记录。")
        ipc_source = S(load, "switch_loadable_module_get_endpoint_interface")
        ipc_items = [
            ("playback-file-start", "内部 WAV 文件放音", "{id,op:playback_file_start,session,playback_id,leg,path}", "根内8k单声道PCM16/PCMA/PCMU WAV转当前G.711媒体；异步loading后实际发包，1MiB/30秒与固定队列/缓存/任务预算。", "PlaybackFileStart {"),
            ("dtmf-events", "内部按键事件游标", "{id,op:dtmf_events,after_seq,limit}", "按worker读取固定1024事件环，最多64条；结束包去重，缺失结束/溢出明确报告，不能当作用户沉默。", "DtmfEvents {"),
            ("playback-start", "内部提示音启动", "{id,op:playback_start,session,playback_id,leg,frequency_hz,duration_ms}", "PCMU/PCMA真实20ms提示音，200..2000Hz、20..10000ms，每worker最多64路；A腿本地会话无需B对端。", "PlaybackStart {"),
            ("playback-status", "内部提示音状态", "{id,op:playback_status,session,playback_id}", "读取实际sent_packets/total_packets及loading/running/completed/stopped/failed，不把启动确认当播放完成。", "PlaybackStatus {"),
            ("playback-stop", "内部提示音停止", "{id,op:playback_stop,session,playback_id}", "按会话及任务ID停止，有界幂等；恢复桥接音频，不将主动停止标为完整播放。", "PlaybackStop {"),
            ("ready", "内部启动握手", "Ready {id:0,ok:true,type:ready,protocol_version:1,worker_id,pid,capabilities}", "worker启动回报固定id=0、版本、真实进程身份及精确能力；错误ID拒绝。双腿需要processed_g711_v1，本地还需processed_g711_local_v1；端口按需绑定，就绪不代表音质或容量。", "Ready {"),
            ("allocate", "内部分配媒体会话", "{id,op:allocate,session,a:{rtp,rtcp},payload,dtmf_payload}", "成功返回 allocated 的四端口；同 session 且参数一致的重复分配返回原分配，冲突/容量/绑定错误返回 error。", "Allocate {"),
            ("connect", "内部设置 B 腿", "{id,op:connect,session,b:{rtp,rtcp},dtmf_payload}", "配置协商远端，确认返回 ack；不是 SIP reINVITE。Go 有序提交并合并同通话更新；线上请求 id 只关联响应，不提供去重或协商版本排序。", "Connect {"),
            ("release", "内部资源释放", "{id,op:release,session}", "不存在会话也返回 ack，允许重试；端口进入隔离期，释放确认不等于端口立即可复用。", "Release {"),
            ("stats", "内部 worker 统计", "{id,op:stats} → {id,ok,type:stats,stats:{...}}", "返回当前进程累计计数/即时资源，重启计数归零；平台不支持丢包计数时显式返回支持标记。", "Stats,"),
            ("shutdown", "内部 worker 停止", "{id,op:shutdown} → ack", "确认后退出事件循环，不等待活动呼叫自然结束；正常业务排空由 Go 控制面协调。", "Shutdown,"),
        ]
        for slug, title, interface, semantic, needle in ipc_items:
            K("ipc", slug, title, ipc_source, "原版核心/endpoint 内部调用", "FreeSWITCH 内部 C 调用，不存在该 JSONL 指令", "原版会话与媒体接口使用其对象、锁、内存池和回调；该参考只说明内部接口层次相近，不存在消息一一映射。", interface, semantic, "internal_only", ["仅 Go 控制面与 Rust 子进程 stdin/stdout 使用，有界 JSON 行；不是公开 HTTP/ESL。", "请求/响应 id、session、ok、type 的语义不等于 UUID、Job-UUID 或 +OK。"], protocol, needle)
        K("c_abi", "codec", "自定义编解码 ABI", S(core, "switch_core_codec_init_with_bitrate"), "switch_codec_interface_t / codec init", "原版 codec 模块与核心编解码调用", "原版编解码依赖模块注册、内存池、codec 结构及核心调用约定；头文件名或算法相同不产生二进制兼容。", "rs_codec_get_v1 → rs_codec_v1", "版本/结构大小、int16 PCM、create/destroy/encode/decode、显式容量与输出长度；库需保持加载，上下文精确销毁，异常不可跨 ABI。", "partial", ["示例 G.711 往返已验证，当前通话路径不调用此插件。", "不导出 switch_* 宿主符号，不可原样装载 mod_*.so。"], "native/include/rustswitch_codec.h", "typedef struct rs_codec_v1", regression)
        K("c_abi", "protocol-provider", "C 协议提供器预留边界", native, "原版 endpoint 模块 ABI", "switch_endpoint_interface_t 与 Sofia 提供器生命周期", "原版 endpoint 依赖会话回调、核心结构、模块注册和运行上下文；仅保留 C 链接约定不足以兼容。", "rs_protocol_v1 {create,submit,poll_event,destroy}", "仅有自定义头文件；实际装载入口、JSON schema、版本协商与 Sofia/PJSIP 适配器尚未实现。poll_event 的 1/0/负值仅为预留契约。", "not_implemented", ["不存在已可运行的 C 协议提供器或原版 Sofia 二进制宿主。", "C/C++ 库复用必须独立说明 N-LIB/N-SRC/N-BIN/N-HOST 范围。"], "native/include/rustswitch_protocol.h", "typedef struct rs_protocol_v1")
        K("cli", "fs-cli", "原版 fs_cli 不改动连接", S("libs/esl/fs_cli.c"), "fs_cli / libesl", "fs_cli -x 'status'；交互命令/补全/日志/事件", "原版客户端依赖 ESL 认证、帧、命令、补全与事件处理，交互模式比单条 status 请求覆盖更多能力。", "可选 ESL 监听：fs_cli -x 的 console_execute 单 API 子集", "已接入现有14个API的单命令包装（包含1个自有DTMF状态扩展）；版本保持RustSwitch身份，status为自有摘要。批处理、别名与完整交互尚未实现。", "partial", ["仅限定单命令路径；双分号批处理和别名在执行任何API前拒绝。", "原客户端成功退出不能代替正文与副作用验证；完整交互、补全、日志和所有API仍待补齐。"], "control/internal/esl/console.go", "func singleConsoleCommand")
        K("cli", "config-check", "启动与无监听校验", S("src/switch.c"), "freeswitch 启动 CLI", "原版启动参数与退出选项按现场冻结", "原版启动选项控制目录、模块、控制台、前后台和进程退出，其安装/service 合同需单列验证。", "bin/rustswitch -config <json> [-check-config]", "读取启动 JSON 及对应管理状态；校验模式不绑定 socket/启动 worker；普通模式首次信号排空，第二次信号强制取消。", "implemented", ["不会接受全部 freeswitch 原参数、PID/目录/service 约定。", "校验通过不证明目标端口当前可绑定或达到容量 SLO。"], "control/cmd/rustswitch/main.go", 'flag.Bool("check-config"', regression)
        K("cli", "callbench", "压测生成器与通过条件", A("status"), "原版系统观测/外部负载发生器", "状态查询不是负载验收", "原版 status 不会独立证明端到端媒体正确；需同一负载下核对发生器、网络和服务资源证据。", "bin/callbench --calls ... --cps ... --seconds ...", "模拟主被叫，核对 SSRC、源地址、时间戳、载荷、唯一接收和拆线。passed 要求负载≥98%、全部建立、零缺包/重复/非法/写错/意外 BYE/拆线失败。", "implemented", ["乱序与 late_ticks 当前只报告，不单独判失败；direct-media 仅诊断发生器/网络。", "socket-groups、spread、connected 等改变负载形态，报告必须保留这些字段。"], "control/cmd/callbench/main.go", "passed :=", regression)

    def validate(self):
        """校验数量、唯一 ID、字段类型和所有源码/文档链接；不把静态校验计为兼容测试。"""
        actual = {key: len(self.catalog[key]) for key in ("registrations", "modules", "events", "variables", "native_functions", "vanilla_parameters")}
        actual["subcommands"] = sum(len(self.subcommands[key]) for key in ("conference_table", "conference_global_branches", "sofia_parser_branches", "sofia_help_only"))
        actual["acceptance_definitions"] = len(self.cases)
        if actual != BASELINE_COUNTS:
            raise ValueError(f"基线数量改变，必须人工审阅：{actual}")
        if len(self.key_entries) < 40:
            raise ValueError("人工关键语义对照不足 40 条")
        if len(self.entries) != sum(actual.values()) + len(self.key_entries):
            raise ValueError("对照总数与分组分母不一致")
        identifiers = set()
        for row in self.entries:
            if set(row) != FIELDS or row["id"] in identifiers:
                raise ValueError("重复 ID 或字段不完整：" + row["id"])
            identifiers.add(row["id"])
            if row["status"] not in STATUSES or row["relationship"] not in RELATIONSHIPS:
                raise ValueError("状态或关系枚举无效：" + row["id"])
            for key, value in row.items():
                if key == "differences":
                    if not isinstance(value, list) or not value or not all(isinstance(item, str) and item for item in value):
                        raise ValueError("差异字段无效")
                elif key == "source_line":
                    if type(value) is not int or value < 1:
                        raise ValueError("行号无效")
                elif not isinstance(value, str):
                    raise ValueError("字段应为字符串：" + key)
            if row["source_url"]:
                expected = SOURCE_URL + row["source_path"] + "#L" + str(row["source_line"])
                if row["source_url"] != expected or row["source_line"] > len(self.sources[row["source_path"]].splitlines()):
                    raise ValueError("固定源码链接无效：" + row["id"])
            else:
                if row["category"] != "acceptance_definition":
                    raise ValueError("只有本地验收定义可以没有原版源码链接")
                if row["source_line"] > len((self.project / row["source_path"]).read_text().splitlines()):
                    raise ValueError("本地定义行号无效")
            doc = row["document_path"].split("#", 1)[0]
            if doc != "api/freeswitch-comparison.md" and not (self.project / "docs" / doc).is_file():
                raise ValueError("文档链接无效：" + doc)
        # 原目录 ID/声明位置逐个进入生成记录，不能仅通过凑总数满足覆盖要求。
        for row in self.catalog["registrations"]:
            if "registration:" + row["id"] not in identifiers:
                raise ValueError("注册 ID 遗漏")
        for row in self.cases:
            if "acceptance:" + row["id"] not in identifiers:
                raise ValueError("验收定义 ID 遗漏")
        return actual

    def business_entries(self):
        """以实际入口补充AI呼叫子集，原版模块宿主、完整XML与全部APP仍保持未实现。"""
        # 自有查询和内部请求分别保留ID，不能冒充原版新增了三个兼容命令。
        for group, slug, name, interface, semantic, status, path, needle in [
            ('esl', 'dtmf-send-status', '本地按键发送进度查询', 'api uuid_send_dtmf_status <uuid>',
             '返回实际媒体受理/完成/失败/排队数字、发送包数和错误；这是RustSwitch自有扩展，完成仅表示UDP提交成功。',
             'implemented', 'control/internal/server/esl_send_dtmf.go', 'func (s *Server) handleSendDTMF'),
            ('ipc', 'dtmf-send', '内部按键发送', '{id,op:dtmf_send,session,leg:a,digits,duration_ms}',
             '对本地A腿整批受理1..32个协商按键；每会话32数字、每worker64活动发送器，调度迟到或发送失败明确结束。',
             'internal_only', 'media/src/media/dtmf_sender.rs', 'pub fn enqueue('),
            ('ipc', 'dtmf-send-status', '内部按键发送进度', '{id,op:dtmf_send_status,session}',
             'dtmf_send_state返回媒体队列真实计数；不可能的完成/包数和部分受理回执由Go拒绝，释放后不能继续查询已删除会话。',
             'internal_only', 'control/internal/media/interaction.go', 'func validateDTMFSendReply'),
        ]:
            self.key(group, slug, name, self.registration('uuid_send_dtmf'),
                'uuid_send_dtmf及核心媒体发送队列（仅概念对应）', 'api uuid_send_dtmf <uuid> <dtmf_data>',
                '原版API提交发送数据；不存在这里定义的同名状态扩展或JSONL内部合同。实际线协议与RTP副作用需分开验证。',
                interface, semantic, status,
                ['仅支持已ACK本地A腿；桥接、B腿和SIP INFO发送拒绝。', '受理不等于远端收到或播放，超时在途请求需查询状态或挂断清理；不是完整FreeSWITCH发送语法。'],
                path, needle)
            self.entries[-1]['document_path'] = 'api/dtmf-send-reference.md'
        updates = {
            "key-sip-register": ("sip.trunk_auth + sip.registration", "固定上游REGISTER客户端支持认证、Contact到期刷新、423调整、失败退避与注销；INVITE认证挑战先ACK再递增CSeq重试。", "api/trunk-registration.md", ["尚无终端注册服务器、位置数据库、多账户网关、DNS发现或原Sofia配置执行。", "只支持已文档化Digest算法与qop=auth/旧式无qop，认证错误不能推导运营商线路已互通。"]),
            "key-sip-invite": ("固定上游桥接；本地精确号码或sip.dialplan XML", "启用XML后按context匹配且未匹配404；未启用时保留固定上游/精确号码。本地只有A腿，Rust分配成功200、正确ACK后执行应用或授权私有PCM流；200未获ACK到期向A发送BYE并释放。", "api/ai-calling-reference.md", ["XML仅支持明确列出的条件和动作，无完整求值、Lua或FreeSWITCH originate。", "已接入本地G.711流式PCM下行；完整ASR/LLM/TTS服务、非G.711实时转码、转接与完整路由仍未实现，私有PCM不等于原版speak。"]),
            "key-esl-sendmsg": ("sendmsg <uuid> / call-command: execute", "已提供有界串行answer/set/unset/read/playback/sleep/park/hangup应用，真实执行及完成事件；read支持终止键、超时、提示音打断，挂断取消排队任务。", "api/ivr-reference.md", ["并非全部应用、sendmsg命令或全部并发/锁语义；仅支持受限WAV文件，仍无TTS和完整菜单XML。", "仅支持列出的参数子集，错误/完成事件必须与实际媒体和变量结果联合验证。"]),
            "key-media-dtmf": ("RTP telephone-event/显式INFO接收；本地A腿RTP按键发送", "接收按协商源/PT/clock/events去重完成按键；INFO要求有效对话和显式Duration。发送支持有界digits[@ms]子集并共用本地播放SSRC/序号；不修改透明桥接包。", "api/dtmf-send-reference.md", ["不提供音内检测、桥接/B腿或INFO发送、完整长事件分段和全部INFO格式。", "read缓冲时序、全部原版事件字段和运营商终端差异仍需逐项对照。"]),
        }
        by_id = {row['id']:row for row in self.entries}
        for identifier,(interface,semantic,document,differences) in updates.items():
            by_id[identifier].update(status='partial',relationship='direct_comparison',rustswitch_interface=interface,rustswitch_semantics=semantic,document_path=document,differences=differences,verification='当前源码的限定用例证据见运行验证；完整原版合同、运营商互通和容量组合尚未全部通过。')
        by_id['registration:api:mod_commands:uuid_send_dtmf:7774'].update(status='partial', relationship='direct_comparison',
            rustswitch_interface='api/bgapi uuid_send_dtmf <uuid> <digits>[@milliseconds]',
            rustswitch_semantics='已ACK本地A腿按协商telephone-event发送0-9*#A-D；50..1000ms、默认250ms、20ms调度与三个结束包。+OK表示媒体整批受理，真实结果另查自有状态。',
            document_path='api/dtmf-send-reference.md',
            differences=['不支持透明桥接、B腿、INFO发送、w/W停顿、+分段、~标志和原版变量覆盖。', '包序/时钟保持连续，但原版与候选按键期间的音频调度差异如实保留；不是完整原版发送合同。'],
            verification='实际RTP/失败/回收及原版限定报文成对结果见运行报告；不以+OK证明远端接收。')
        by_id['registration:api:mod_commands:uuid_dump:7744'].update(status='partial', relationship='direct_comparison',
            rustswitch_interface='api/bgapi uuid_dump <uuid> [format]',
            rustswitch_semantics='在通话主循环内生成单腿CHANNEL_DATA快照；包含真实通道身份、当前状态、SIP Call-ID和已存变量。',
            document_path='api/channel-snapshot-reference.md', differences=['仅输出当前实际持有的字段，不伪造原版core/session/codec信息。', '支持txt/plain/JSON/XML与未知格式回退；完整原版状态/变量空间及所有参数解析仍未齐全，输出受资源额度限制。'],
            verification='限定本地实测与原版源码语义逐项记录；不是完整uuid_dump契约认证。')
        for identifier in ['event:PLAYBACK_START', 'event:PLAYBACK_STOP', 'event:CHANNEL_PARK', 'event:CHANNEL_UNPARK']:
            by_id[identifier].update(status='partial', relationship='direct_comparison',
                rustswitch_interface='入站ESL event plain/json/xml '+by_id[identifier]['name'],
                rustswitch_semantics='由实际放音或停泊生命周期发出；关联当前通道与任务，错误、终止和挂机保持明确顺序。',
                document_path='api/event-lifecycle-reference.md', differences=['仅覆盖当前受限放音/停泊应用，完整事件头、多值字段及其他业务来源仍未完成。', '订阅普通事件名称本身不表示事件全部已实现；具体触发时机与错误场景见事件手册。'],
                verification='按具体正常/终止/失败场景验证事件与媒体，不以订阅受理或同名枚举代替业务证据。')
        for row in self.entries:
            if row['category']=='fs_application' and row['module']=='mod_dptools' and row['name'] in {'answer','set','unset','read','playback','sleep','park','hangup'}:
                row.update(status='partial',relationship='direct_comparison',rustswitch_interface='sendmsg execute '+row['name'],rustswitch_semantics='使用实际通道应用队列执行所列参数子集，结果与CHANNEL_EXECUTE_COMPLETE关联；详见IVR手册。',document_path='api/dialplan-reference.md',differences=['支持受限XML顺序调度和WAV；仍无全部参数、变量展开、完整文件后端及park抢占语义。','真实结果及明确拒绝范围需结合运行证据查看。'],verification='本地真实SIP/Rust媒体回归与原版限定对照分别记录；不是完整模块认证。')
            if row['id'] in {'event:DTMF','event:CHANNEL_EXECUTE','event:CHANNEL_EXECUTE_COMPLETE'}:
                row.update(status='partial',relationship='direct_comparison',rustswitch_interface='入站ESL event plain/json/xml '+row['name'],rustswitch_semantics='来自实际按键或应用生命周期，带通道/应用关联及限定字段；未执行或失败不伪造成功事件。',document_path='api/ivr-reference.md',differences=['只实现受限字段和业务路径，完整原版事件字段及竞态尚未全部验证。'],verification='限定断言见本地运行证据与原版对照记录。')

    def processed_media_entries(self):
        """M1只扩展既有媒体条目的限定实现范围；测试和原版认证状态继续由独立证据决定。"""
        changes = {
            'key-media-transcoding': {
                'status': 'partial', 'relationship': 'analogous_not_wire_compatible',
                'rustswitch_interface': 'media.processing=g711：PCMU/PCMA 8k/20ms，bridge/local独立握手',
                'rustswitch_semantics': '真实双腿图执行重排→8k借用PCM→目标G.711编码→独立RTP封装；本地local图消费A侧RX，将tone/WAV或有界流式PCM编码到A。流式PCM与传统播放互斥，共用本地按键TX身份，默认relay保留透传。',
                'differences': ['G.711 8k单声道20ms双腿、本地播放与流式PCM分别核验；旧双腿22项、本地26项及外部SIP授权PCM22项是独立集合。G.722/Opus实时处理、双腿播放混入、ASR上行、TTS服务适配、VAD和录音仍未接入。', 'PCM供音与单路真实媒体回归不等于原版speak/录音兼容、原版成对音质或5000/10000路转码认证。'],
            },
            'key-media-rtp': {
                'rustswitch_interface': '默认relay；可选g711终结型双向媒体图',
                'rustswitch_semantics': 'relay校验后转发原数据报；g711模式逐腿解码/编码，使用新的SSRC、包序和时间映射，缺失/迟到/重复分别处理。本地RX不反射，tone/WAV/流式PCM和主动DTMF共享本地TX身份，换turn不重置SSRC/包序/时钟。Linux批量接收继续保留。',
                'differences': ['两种模式使用不同媒体合同，不能概括为全部FreeSWITCH proxy/bypass/normal行为。', '本地UDP提交和功能回归不构成远端零丢包或万路性能承诺。'],
            },
            'key-media-rtcp': {
                'rustswitch_interface': 'relay原包转发；g711模式逐腿SR/RR/SDES/BYE',
                'rustswitch_semantics': '处理图用实际RX/TX计数生成复合报告，保持SR媒体时钟不被DTMF或迟到CN回拨；本地流式PCM使用同一TX计数和时钟，单个turn结束不结束RTP会话，Release尝试发送BYE。',
                'differences': ['只使用独立RTCP端口；rtcp-mux、SRTCP、完整反馈、质量事件与带宽自适应周期未完成。', '本地成对端点实测与FreeSWITCH原版成对认证是不同证据。'],
            },
            'key-sip-sdp': {
                'rustswitch_semantics': 'relay保留同编码同PT协商；g711显式建立独立两腿报价，保留A腿G.711动态映射，B腿选择已报价静态0/8，183收紧与200恢复辅助集合不重置语音。',
            },
            'key-ipc-allocate': {
                'rustswitch_interface': '{id,op:allocate,session,a,payload,codec,processing?,…}',
                'rustswitch_semantics': '处理计划在分配时固定；Go验证精确ready能力及processing_version=1；local还要求processing_topology=local，禁止连接B腿，旧bridge省略拓扑。错误回执拒绝；透传省略处理计划和版本。',
            },
            'key-ipc-connect': {
                'rustswitch_semantics': '处理模式B腿独立G.711协商；相同目标可幂等确认，辅助格式可收紧恢复，已连接主格式与对端不能随意改变。relay保持既有合同。',
            },
        }
        indexed = {row['id']: row for row in self.entries}
        for identifier, values in changes.items():
            if identifier not in indexed:
                raise ValueError('M1必须关联现存对照ID：' + identifier)
            indexed[identifier].update(values, document_path='api/processed-media-reference.md',
                verification='候选限定实现与实际运行结果见processed-media-verification.json；生成目录不会授予绿色，当前不声明FreeSWITCH完整成对认证或已发布主服务。')

    def pcm_stream_entries(self):
        """PCM是自有有界媒体合同；只增加固定入口ID，不向原版speak/录音或容量传播状态。"""
        endpoint = self.source("src/include/switch_loadable_module.h", "switch_loadable_module_get_endpoint_interface")
        evidence = ("静态合同已定位；内部PCM与外部SIP授权分别取证。外部候选sip-02的22方法、"
                    "Go race 899项和旧24阶段228方法是独立结果，不能相加为本条完整兼容认证；"
                    "当前源码绿色由独立runtime证据门禁判定，生成目录不授予通过状态")
        shared = [
            "仅本地G.711 8k mono 20ms图；不是FreeSWITCH同名命令、speak、录音或ASR上行接口。",
            "内部session不是SIP UUID；Rust媒体层不拥有SIP ACK，外部Go入口另行核验正确ACK、分配及worker代次。",
            "接受、关闭输入、Go句柄退休、Rust实际终态和远端收到是不同事实；结果未知需用原身份查询/停止，不盲重发音频。",
        ]
        protocol = "media/src/media/protocol.rs"
        items = [
            ("pcm-turn-begin", "内部PCM轮次启动", "{id,op:pcm_turn_begin,session,turn_id,buffer_ms,prebuffer_ms} → pcm_turn_state",
             "turn_id非零且新轮严格递增；同turn同配置幂等返回原状态、不复活。buffer为20..1000ms且步长20，prebuffer为20..buffer且步长20，物理队列至多50帧。新轮原子取消旧队列；与running/loading的tone/WAV互斥。依赖pcm_turn_v1与有效FD3，预缓冲2秒不足明确失败。",
             protocol, "PcmTurnBegin {"),
            ("pcm-turn-end", "内部PCM生产结束", "{id,op:pcm_turn_end,session,turn_id,final_samples} → pcm_turn_state",
             "final_samples必须等于本轮accepted_samples，且为160整倍数；错误偏移不结束输入。成功关闭输入，未达预缓冲也排空已有整帧，空轮可立即completed；非空仅最后一帧实际提交UDP后满20ms才completed。无部分尾帧或隐式padding。",
             protocol, "PcmTurnEnd {"),
            ("pcm-turn-interrupt", "内部PCM打断与有限淡出", "{id,op:pcm_turn_interrupt,session,turn_id,fade_ms?} → pcm_turn_state",
             "fade默认为0，仅0/20/40ms；立即关闭旧轮输入。0丢弃剩余队列并形成实际stopped屏障；淡出只保留已收到队首至多1/2帧、线性衰减到0，不重播已发帧、不重新排期超期音频。重复淡出不延后原计划，stopping可升级为0立即停。",
             protocol, "PcmTurnInterrupt {"),
            ("pcm-turn-status", "内部PCM真实轮次状态", "{id,op:pcm_turn_status,session,turn_id} → pcm_turn_state",
             "只查询当前匹配轮次；返回buffering/playing/draining/stopping/completed/stopped/failed和实际样本/容量/年龄/error。恒有accepted=sent+queued+discarded；sent仅完整UDP提交样本，状态查询不代发音频。playing且队列空时按last_accepted_at+20ms+100ms判断断供，已超期push/end不能复活。Release后无旧会话可查，失败错误不能伪装完成。",
             protocol, "PcmTurnStatus {"),
            ("pcm-push", "内部PCM二进制供音", "独立继承FD3 UnixDatagram：RSP1固定44字节头+S16LE → RSR1固定96字节回执",
             "非JSON操作；请求携request_id/session/turn_id/offset，8k mono每批160..800样本且整160，offset准确等于accepted_samples。整批验证后入队，满队列或偏移错误不部分接受；RSP1 code8是queue_full，可按真实偏移恢复。lane仅保留一个待发回复，WouldBlock/ENOBUFS有一秒总期限，超时隔离PCM lane而非阻塞RTP/控制。",
             "media/src/media/pcm_transport.rs", "pub fn parse(bytes:"),
        ]
        for slug, title, interface, semantics, path, needle in items:
            self.key("ipc", slug, title, endpoint,
                "原版endpoint/媒体内部接口（仅层次参考）",
                "FreeSWITCH没有pcm_turn_* JSONL或RSP1/RSR1指令",
                "固定源码用于定位原版内部媒体接口层次；对象、回调及锁模型与本项目二进制队列不同，不存在逐消息兼容关系。",
                interface, semantics, "internal_only", shared.copy(), path, needle,
                evidence=evidence, relationship="internal_contract_only")
            self.entries[-1]["document_path"] = "api/pcm-turn-reference.md"
        self.key("api", "pcm-stream", "私有RVA1流式PCM API", endpoint,
            "FreeSWITCH endpoint媒体层（无同名私有API）",
            "原版没有RVA1/RVR1、流token或对应HTTP路径",
            "仅比较应用向媒体提供音频这一层次；原版ESL、模块ABI与speak/录音应用并不实现本项目的帧或令牌合同。",
            "可选pcm_stream.socket_path：同UID Unix stream RVA1/RVR1；HELLO/BEGIN/PUSH/END/INTERRUPT/STATUS/FORGET/PING",
            "非HTTP/非ESL的持久二进制接口，默认关闭；私有0700父目录、0600 socket。控制/数据连接分别有界，UUID流槽由max_streams限制，未知替换最多保留当前+旧两个handle/token。BEGIN由Go核验正确ACK、本地G.711分配与健康同代三能力；未知结果保留token供对账。数据连接断开不自动取消媒体；PUSH/END串行防超车，确定偏移拒绝可恢复，错误5合并Go/Rust背压。park与静默read可并行，tone/WAV或带提示read与PCM互斥；挂机/代次失败撤销。",
            "implemented",
            ["这是自有部署可选API；64字节RVA1请求头、96字节RVR1回复头和各自错误码不兼容ESL、HTTP或内部RSP1/RSR1。", "新轮成功替换旧token；未知替换必须保留受限旧控制身份直到结果收敛。输入关闭/退休不等于真实停止，只有实际终态确认后FORGET才删除；原ASR/TTS服务、录音与桥接注入仍未实现。", "单路22项验收不证明完整并发连接/故障组合、原版兼容或5000/10000路Agent容量；旧失败证据保留。"],
            "control/internal/server/pcm_endpoint.go", "func newPCMEndpoint(",
            evidence=evidence, relationship="analogous_not_wire_compatible")
        self.entries[-1]["document_path"] = "api/rva1-reference.md"
        updates = {
            "key-ipc-ready": "worker固定id=0、protocol_version=1、真实worker_id/pid及精确能力；双腿需processed_g711_v1，本地另需processed_g711_local_v1，PCM仅在有效独立FD3初始化后声明pcm_turn_v1。Go发布健康进程代次后才能授权；就绪不代表SIP已ACK、ASR/TTS可用或容量通过。",
            "key-ipc-allocate": "处理计划分配时固定；Go核验ready及processing_version=1，local另需processing_topology=local且禁止B Connect。分配不创建PCM轮次、不授权SIP供音；begin和独立PCM能力另检。同session同参数返回原分配，冲突拒绝；relay省略处理计划。",
            "key-ipc-release": "不存在会话也ack，允许重试；释放取消本会话PCM队列和定时项、旧播放/按键、处理图及socket，处理模式尝试RTCP BYE。端口按隔离期复用；Go先撤销句柄，再以真实释放/原代次死亡确认无媒体所有权，旧token/音频不能进入新会话。",
            "key-ipc-stats": "返回当前worker真实累计计数和即时资源；local consumed是RX真实解码+有限PLC，不是下行供音。流式PCM编码/发送及deadline失败进入既有processed/TX桶；各turn的accepted/sent/queued/discarded/error由pcm_turn_status查询，不伪造全局PCM统计。重启分代归零，sent不等于远端接收。",
            "key-ipc-playback-start": "PCMU/PCMA真实20ms单音，200..2000Hz、20..10000ms，每worker最多64路；本地A无需B。处理图直接编码原PCM，共用本地TX身份；活动PCM轮次与tone双向互斥，受理或真正启动前都不能抢走既有TX所有权。",
            "key-ipc-playback-file-start": "根内8k单声道PCM16/PCMA/PCMU WAV有界加载，1MiB/30秒与固定队列/缓存/任务预算；loading不代表已发包。本地处理图直接编码原PCM；活动PCM轮次与WAV双向互斥，过期加载结果不复活旧任务。",
            "key-ipc-playback-stop": "仅停止匹配playback_id的传统tone/WAV，有界幂等；本地停止后无旧播放输出，桥接路径按既有合同恢复音频。不会停止另一PCM turn，流式供音必须使用pcm_turn_interrupt；主动停止不标为完整播放。",
        }
        indexed = {row["id"]: row for row in self.entries}
        for identifier, semantics in updates.items():
            if identifier not in indexed:
                raise ValueError("PCM必须关联现存内部ID：" + identifier)
            indexed[identifier].update(rustswitch_semantics=semantics,
                document_path="api/pcm-turn-reference.md", verification=evidence)

    def rx_stream_entries(self):
        """上行观察与下行供音分开记账；新增内部合同，不授予原版兼容或外部ASR完成状态。"""
        endpoint = self.source("src/include/switch_loadable_module.h", "switch_loadable_module_get_endpoint_interface")
        evidence = ("源码与独立验收材料已关联：Rust06的RXS2普通UDP/connected各12方法；"
                    "Server SDK03与Rust08的server-sip-03含PCMU/PCMA×两种UDP模式四个真实SIP子例，"
                    "检查正确ACK授权、逐样本RX、BYE撤读、真实资源归零与端口复绑。"
                    "两批候选身份分别保留；本地绿色由独立运行证据门禁判定，目录生成不等于执行或原版认证")
        shared = [
            "只接收本地G.711 PCMU/PCMA 8k、单声道、20ms图的A腿原生音频；不默认转16k，不含TX回音、双腿混音或外部ASR服务适配。",
            "这是自有进程间合同，session/subscription_id不是SIP UUID或ESL Job-UUID；Rust本身不判断SIP ACK，服务端Go SDK另行授权。",
            "真实解码、历史PLC、CN、按键辅助、缺包、源边界与导出缺口分开表达；非零能量和socket提交都不是VAD、用户发声或ASR识别结论。",
        ]
        protocol = "media/src/media/protocol.rs"
        items = [
            ("rx-subscribe", "内部原生音频上行订阅", "{id,op:rx_subscribe,session,subscription_id} → rx_state",
             "session/subscription_id须为非零u64；每会话最多一个活动订阅。同ID返回原状态，不复活、不重锚时钟；新ID严格递增且旧订阅须已停止/失败。需local图与可用独立FD4；新订阅先校准共享时钟，失败不改观察图。每订阅8槽，未订阅不分配导出观察队列。",
             "media/src/media/worker_rx.rs", "pub(super) fn rx_subscribe("),
            ("rx-status", "内部上行订阅状态与对账", "{id,op:rx_status,session,subscription_id} → rx_state",
             "只查询精确会话/订阅，返回active/stopped/failed、produced/submitted/dropped/queued事件数、样本数、oldest_age_ms与error。produced_events=submitted_events+dropped_events+queued_events；样本按产生、提交、丢弃及在队内容对账。查询会执行过期淘汰，不代发音频。submitted只证明Rust交给内核，缺口/结束摘要不计为新的生产观察；Release后旧会话不可查。",
             "media/src/media/worker_rx.rs", "pub(super) fn rx_control("),
            ("rx-unsubscribe", "内部上行停止与终态", "{id,op:rx_unsubscribe,session,subscription_id} → rx_state",
             "仅停止匹配订阅，重复停止返回原终态；关闭观察图、丢弃未提交音频并保留真实对账，可尽力发出缺口/结束摘要。控制回执不等待消费者读取，不停止下行PCM、提示音或按键；旧ID不会复活，结果未知须继续使用原身份查询或停止。会话释放与订阅停止是不同生命周期。",
             "media/src/media/worker_rx.rs", "pub(super) fn rx_control("),
            ("rx-stream", "内部RXS2原生音频数据流", "独立继承FD4 UnixDatagram：RXS2固定168字节头，正文最多480字节",
             "仅Rust写、Go读；Decoded/HistoryPlc各160个S16LE样本，CN保留原SID，其余标记无PCM。携带订阅、事件序号、源代次/分段、展开RTP包序/时间、真实年龄和原因。普通观察以共享单调时钟下界+100ms冻结到期值，重试/序列化不续期；Gap/End/ExportFailed两个时间字段为0。仅Linux MONOTONIC/macOS UPTIME_RAW同机同域可用；Go接收、出队及Server交付检查点复核，后续适配器使用前仍须CheckFresh。",
             "media/src/media/rx_export.rs", "pub const HEADER: usize = 168;"),
        ]
        for slug, title, interface, semantics, path, needle in items:
            self.key("ipc", slug, title, endpoint,
                "原版endpoint/媒体内部接口（仅职责层次参考）",
                "FreeSWITCH没有rx_* JSONL、FD4或RXS2数据报合同",
                "原版内部媒体读取由其会话、回调和模块对象管理；该源码锚点仅说明层次，不表示本项目实现了对应原生符号或逐消息映射。",
                interface, semantics, "internal_only", shared.copy(), path, needle,
                evidence=evidence, relationship="internal_contract_only")
            self.entries[-1]["document_path"] = "api/rx-stream-reference.md"
            if slug == "rx-stream":
                self.entries[-1]["differences"].append("Rust每订阅8槽，队列满或观察年龄达到100ms丢旧记录并报告连续缺口；无法保真对账则显式失败。写入连续一秒无进展隔离RX，控制与TX继续；Go每订阅8槽独立限流，慢读或过期明确失败，不把OS排队后的旧PCM当作新音频。")
        self.key("api", "rx-sdk", "内部Go服务端上行音频SDK", endpoint,
            "FreeSWITCH内部媒体访问（无同名Go SDK）",
            "原版没有Server.SubscribeRX/RXHandle或对应HTTP/Unix监听接口",
            "仅比较应用消费入站音频的职责；不提供FreeSWITCH模块ABI、ESL命令、媒体钩子符号或外部识别服务合同。",
            "Server.SubscribeRX(ctx,uuid,subscriptionID) → RXHandle；Read/Status/Unsubscribe/Snapshot/Retired",
            "进程内Go调用，未暴露HTTP或Unix服务。只授权真实正确ACK、已接通且已分配的本地G.711 A腿及同一健康worker代次，绑定UUID/worker/generation/session/subscriptionID。固定4个控制协程、64个请求/在途预算；音频直接经独立SDK读取。同ID返回原句柄、不重开读权；新ID须确认旧远端终态。未知受理保留不可读清理句柄；挂断先撤读并唤醒，实际Release/代次死亡后退休。Read在交付检查点复核期限与读权，Snapshot是Go接收视图，Status查询Rust生产状态。",
            "internal_only",
            ["这是服务内部SDK，没有供外部ASR直接连接的HTTP/Unix协议、供应商适配器、自动重采样、VAD或识别结果事件。", "Go撤读/退休、Rust终态、内核已提交和SDK交付分别取证；调用者暂停后再使用音频须复核原到期值，100ms从Graph观察开始，并非声学采集到识别的总延迟。", "真实四子例检查单路SIP授权和媒体生命周期，不证明全FreeSWITCH兼容、Linux运行、5000/10000路Agent容量或ASR识别效果。"],
            "control/internal/server/rx_stream.go", "func (s *Server) SubscribeRX(",
            evidence=evidence, relationship="internal_contract_only")
        self.entries[-1]["document_path"] = "api/rx-stream-reference.md"
        # 沿用既有ID和实现状态，只更新与RX有关的合同及当前可重新定位的源码。
        updates = {
            "key-ipc-ready": ("worker固定id=0、protocol_version=1、真实worker_id/pid及精确能力；双腿需processed_g711_v1，本地另需processed_g711_local_v1。独立FD3可用时声明pcm_turn_v1；独立FD4且共享时钟域受支持时才声明rx_g711_local_v2，不接受旧RXS1能力。Go另核验健康代次、真实分配和正确ACK；ready不代表ASR/TTS可用或容量通过。", "Ready {"),
            "key-ipc-release": ("不存在会话也ack，允许重试；清理本会话PCM/RX队列和定时项、旧播放/按键、观察图及socket，处理模式尝试RTCP BYE。RX末尾摘要不阻塞释放，端口按隔离期复用；Go先撤销读写权，再由实际Release或原代次死亡确认媒体所有权消失，旧token/订阅不能进入新会话。", "Release {"),
            "key-ipc-stats": ("返回真实进程累计量与即时资源；新增rx_active_subscriptions/rx_queued_events/rx_observation_storage_bytes/rx_observation_failed分别表示活动订阅、在队观察、已分配观察存储及Graph失败状态。local consumed含真实解码和有限PLC，不是下行供音。每订阅生产/提交/丢弃由rx_status对账，Go读取/拒绝由Snapshot另计；PCM轮次由pcm_turn_status查询。重启分代归零，提交不等于远端接收或ASR识别。", "Stats,"),
        }
        indexed = {row["id"]: row for row in self.entries}
        for identifier, (semantics, needle) in updates.items():
            if identifier not in indexed:
                raise ValueError("RX必须关联现存内部ID：" + identifier)
            line = self.local_line(protocol, needle)
            indexed[identifier].update(rustswitch_semantics=semantics,
                document_path="api/rx-stream-reference.md",
                verification=f"{evidence}；RustSwitch 源码 {protocol}:{line}。")
        transcoding = indexed["key-media-transcoding"]
        transcoding.update(
            rustswitch_semantics="真实双腿图重排→8k原生PCM→目标G.711编码→独立RTP封装；本地图将tone/WAV或有界流式PCM编码到A，活动供音互斥且共享按键TX身份。可独立订阅A侧原生8k RX，输出真实解码/PLC/CN/缺口元数据；按需分支重采样的外部ASR适配仍未接入，默认relay继续透传。",
            differences=["G.711双腿处理、本地播放、流式PCM与原生RX各有独立验收材料，不能互相传播通过状态。G.722/Opus实时处理、双腿混入、外部ASR/TTS适配、VAD与录音仍未接入。", "内部RX与单路SIP逐样本验证不等于原版媒体钩子/录音兼容、原版成对音质或5000/10000路Agent容量；详见api/rx-stream-reference.md。"])

    def result(self):
        """输出稳定排序、无当前时间戳的 JSON，保证同一输入生成逐字节一致。"""
        self.baseline_entries()
        self.critical_entries()
        self.business_entries()
        self.processed_media_entries()
        self.pcm_stream_entries()
        self.rx_stream_entries()
        for row in self.entries:
            row["document_path"] = row["document_path"].removeprefix("docs/")
        baseline = self.validate()
        self.entries.sort(key=lambda row: (row["category"], row["id"]))
        return {"version": "1.0.0", "reference_version": REFERENCE_VERSION, "reference_commit": REFERENCE_COMMIT, "scope": "固定 FreeSWITCH 1.11.3 所选 712 份源码/配置的声明目录、130 个会议/Sofia 子命令证据、177 个未执行验收定义，以及人工关键语义对照。源码选择集已校验；已运行隔离原版并采集所选模块，全部模块动态全集与完整行为认证仍未完成，实际成对子场景另见报告。不是 100% 兼容认证，也不能从 implemented 条目计算兼容率；该状态只表示相应 RustSwitch 自有入口已实现。", "summary": {"total": len(self.entries), "by_category": dict(sorted(Counter(row["category"] for row in self.entries).items())), "by_status": dict(sorted(Counter(row["status"] for row in self.entries).items())), "baseline_counts": baseline}, "entries": self.entries}


def inline(value):
    """对表格单元格转义，原始语法以 JSON 内容为准，不让管道或换行破坏表格结构。"""
    return html.escape(str(value), quote=False).replace("|", "\\|").replace("\n", "<br>")


def render_markdown(data, keys):
    """把关键人工合同与覆盖口径渲染为可阅读文档；全量逐项机器记录保存在 comparison.json。"""
    counts = data["summary"]
    lines = ["# FreeSWITCH 1.11.3 与 RustSwitch 逐项接口对照", "", "固定参考：FreeSWITCH **1.11.3**，提交 `" + REFERENCE_COMMIT + "`。对照数据版本：`" + data["version"] + "`。", "", "首期目标与优先级见[云蝠 Voice Runtime研发方向](voice-runtime-direction.md)：优先电话语音智能体与必要接口适配，延后的PBX能力保留原ID及缺口状态。**当前不能直接替换 FreeSWITCH。RustSwitch 的 HTTP 管理接口不是 ESL 兼容入口，可选ESL入口已支持有限单命令客户端路径；完整ESL SDK、XML业务执行和mod_*.so宿主尚未兼容。**", "", f"本包包含 **{counts['total']:,} 条可追踪记录**：3,923 条基线目录/验收定义，加 {len(keys)} 条人工关键语义对照。每条都有双方入口、原语法、语义边界、差异、验证说明与来源定位。完整逐项列表见 [comparison.json](comparison.json)，HTTP 合同见 [http-reference.md](http-reference.md) 与 [openapi.json](openapi.json)。", "", "这里的完整是对下列固定输入集逐条覆盖，不是原版动态运行接口全集或 100% 行为兼容。712 份选定源码均校验 SHA-256；已在隔离Linux构建运行原版，采集所选模块、动态注册和成对协议子场景；第三方模块、CUSTOM全集与完整客户端轨迹仍未齐全。", "", "## 对照口径与数据结构", "", "`implemented` 仅表示该条所描述的 RustSwitch 自有入口已实现；它通常与原版是功能概念相近的不同协议。必须同时读取 `relationship`、双方语义、差异和验证字段，不得把这些条目计作原版兼容完成率。", "", "| 状态 | 严格含义 |", "| --- | --- |"]
    for status, label in STATUS_LABELS.items():
        lines.append(f"| `{status}` | {label} |")
    lines += ["", "`relationship` 区分直接对照、概念相近但不兼容线协议、仅导出、无兼容入口、仅内部合同与仅验收要求；当前没有任何一条被授予完整 FreeSWITCH 差分兼容认证。", "", "每条记录固定 17 字段：`id/category/name/module/fs_interface/fs_syntax/fs_semantics/rustswitch_interface/rustswitch_semantics/status/relationship/differences/verification/source_url/source_path/source_line/document_path`。除 `differences` 为字符串数组、`source_line` 为正整数，其余均为字符串；本地验收定义的 `source_url` 为空，其来源定位到本项目标准。`document_path` 相对 docs 根目录，可带锚点。", "", "## 基线逐项覆盖", "", "| 来源 | 条数 | 条目性质与必须保留的限制 |", "| --- | ---: | --- |", "| 注册声明 | 538 | 292 API、222 APP、10 JSON API、14 CHAT_APP；同名不同入口独立保留；原名称/语法/标志表达式保留，未解析处明确标记 |", "| 模块源码目录 | 144 | 含 SDK 辅助目录；不是已构建或已加载模块列表 |", "| 事件枚举 | 94 | 包含 ALL 订阅哨兵与 CLONE；不是 94 种已验证业务事件，不含全部 CUSTOM 子类 |", "| 变量名称宏 | 100 | 声明位置，含 99 个唯一宏/变量名称；不是动态或用户自定义变量全集，作用域/默认值需沿处理器追踪 |", "| 原生函数声明 | 1,865 | 声明位置，含 1,860 个唯一名称；不是唯一导出符号数，未覆盖全部宏生成钩子、extern 与 C++ 绑定 |", "| vanilla 参数 | 875 | 非注释 param 出现位置，重复保留；不是全部运行配置 schema，也不是管理表单的 4,304 个字段 |", "| Conference/Sofia 子命令 | 130 | 84 会议静态表 + 6 全局分支 + 38 Sofia 解析候选 + 2 仅帮助候选；证据等级不能混同 |", "| 兼容验收定义 | 177 | 169 场景 + 8 容量 profile；全部是未执行的兼容定义，需要实例化与原版差分 |", "", "原始目录：[注册/模块/事件/变量/原生/参数](../freeswitch-compatibility/catalog/source-catalog.json)、[会议与 Sofia 子命令](../freeswitch-compatibility/catalog/conference-and-sofia-subcommands.md)、[验收定义](../freeswitch-compatibility/catalog/conformance-cases.json)。生成器逐条保留记录并检查唯一 ID、数量和固定源码链接。原验收目录旧行号可因文档修订过期，因此对照生成时按用例 ID 重新定位。", "", "对于仅有声明元数据的条目，`fs_semantics` 明确只记录注册类别、源码描述、处理器或声明约束；不把未知行为写成已核实语义。原生参数文本、宏表达式与 NULL/动态语法不被静默清空。全量记录的同名字段可在 JSON 中检索；需要业务层面的成对比较时，使用下方关键合同。", "", "## 当前验证结论", "", "本地实际运行结果另见 [运行验证与绿色状态](runtime-verification.md)。绿色只表示当前源码下所列限定断言通过，原始实现状态和原版成对认证保持独立。源码或证据过期后不沿用旧的绿色。", "", "本项目已记录的 Go/Rust 单元、竞态与静态检查、管理/XML 回归以及 17 个受限通话场景在两种 UDP 模式的历史回归，说明当前原型这些实现范围可工作；详细证据边界见 [0.3 管理交付说明](../management-v0.3.md)。它们没有执行原版对照，不能填充 177 条兼容验收定义为通过。C G.711 独立往返检查不代表转码通话已实现。", "", "1.4历史代码的两轮 Linux 虚拟机 5000 路双向 PCMU 短测已通过；不能自动沿用到1.6新增注册/IVR的组合，完整数据、历次失败与硬件边界见 [5000路验证与本轮逐项复核](verification-5000-2026-09-06.md)。Linux 物理服务器和一万路验收仍待提供环境。整机 UDP 计数与实际 RTP 缺包分别记录，不能把回环透传容量迁移为转码、会议、录音、真实网卡或原版成对认证。", "", "## 成对业务示例", "", "### 查询运行状态：协议和返回字段不同", "", "原版已有客户端通过 ESL 执行：", "", "```text", "api status", "", "```", "", "服务端以 `Content-Type: api/response`、`Content-Length` 和原版状态正文返回；原版构建相关正文必须实际采集，不能由本文件伪造。RustSwitch 当前使用：", "", "```http", "GET /v1/status HTTP/1.1", "Host: 127.0.0.1:9080", "", "```", "", "RustSwitch 返回 HTTP JSON，包含 `active_calls/established_calls/workers/guard/journal_*/config_revision` 等。既有 `fs_cli -x status` 不能仅更改端口就解析此响应；原版 `show channels` 的 UUID 列表也没有在 `/v1/status` 中实现。", "", "### 调整接入保护：session、桥接通话与令牌口径不同", "", "原版 `fsctl sps`、`fsctl max_sessions` 控制其核心会话边界；原始参数和错误正文见固定源码。RustSwitch 应先 GET `/v1/config`，保留返回的完整 `guard.policy`，修改其中 `calls_per_second/max_active_calls/burst_calls` 后提交：", "", "```text", "PUT /v1/guard", "Content-Type: application/json", "X-RustSwitch-CSRF: <本进程 GET /v1/config 返回的 token>", "", '{"revision": <读取的版本>, "policy": <保留其他字段的完整策略对象>}', "```", "", "尖括号表示必须从实际响应填写的值，示例不是可原样提交的 JSON。旧版本返回 409；校验失败 422；保存失败 500 且运行策略不更新。降低阈值仅拒绝后续新 INVITE，已有通话继续；过载 SIP 响应为 503/Retry-After，不延迟已经产生的 200 OK，也不在服务端排队等待。", "", "### 保存用户或网关：XML草稿与固定上游注册配置", "", "原版配置由目录/Sofia 模块和相应 reload/profile 路径实际消费。RustSwitch `PUT /v1/fs-config/file` 可保存 `directory/...xml` 或 `sip_profiles/...xml`，随后 ZIP 导出包含这些已保存文本；返回 `runtime_supported:false`。终端向RustSwitch发送 REGISTER 仍不支持；通过sip.trunk_auth和sip.registration可启用固定上游REGISTER客户端，详见[线路注册](trunk-registration.md)。保存用户XML不会创建认证账户。", "", "### 执行录音：不能用日志或成功状态代替音频文件", "", "原版 `uuid_record <uuid> start <path>` 对指定通道执行录音，必须结合原版返回、录音事件、最终音频与磁盘失败结果验证。RustSwitch 尚无此 API 或运行录音功能；会议/录音 XML 可以编辑导出，但 journal JSONL 不是音频。这里不提供会返回虚假 +OK 的桥接示例。", "", "### 释放内部媒体：ACK 并非 ESL 命令结果", "", "```json", '{"id":7,"op":"release","session":42}', "```", "", "这是 Go→Rust 子进程 JSONL。`id` 关联操作、`session` 是 worker 内编号，成功返回内部 ack，端口随后仍可能处于隔离期。它不是 `uuid_kill`，不会提供原版通道原因、BACKGROUND_JOB 或 CHANNEL_HANGUP_COMPLETE；不可直接发送给 HTTP 或 ESL 客户端。", "", "## 关键语义逐项对照", ""]
    for row in keys:
        lines += [f'<a id="{row["id"]}"></a>', "", "### " + row["name"] + " · `" + row["id"] + "`", "", "| 项目 | 合同 |", "| --- | --- |", "| FreeSWITCH 入口 | " + inline(row["fs_interface"]) + " |", "| 原版参数/帧 | " + inline(row["fs_syntax"]) + " |", "| 原版语义 | " + inline(row["fs_semantics"]) + " |", "| RustSwitch 入口 | " + inline(row["rustswitch_interface"]) + " |", "| 当前语义 | " + inline(row["rustswitch_semantics"]) + " |", "| 状态/关系 | `" + row["status"] + "` / `" + row["relationship"] + "` |", "", *["- " + item for item in row["differences"]], "", "验证：" + row["verification"], "", "原版定位：[" + row["source_path"] + ":" + str(row["source_line"]) + "](" + row["source_url"] + ")。", ""]
    lines += ["## 差分闭合与无感切换要求", "", "最终无改动替换需先冻结现网构建、模块、配置、客户端、脚本及 C/C++ 依赖；补采动态接口和返回/错误/事件/文件轨迹；将每个强制定义展开成可执行的成对测试。任一未运行、未知、跳过或不支持项都不能计入通过。完整门槛见 [差分验收与切换标准](../freeswitch-compatibility/06-conformance-and-cutover.md)。", "", "已有呼叫自然排空后切换可避免主动挂断，但需要等待并正确路由旧对话；活动呼叫、ESL TCP 连接、作业、订阅及录音状态在线迁移是独立目标。当前尚未实现后者，不能用排空按钮或子进程重启作为无损接管证明。", "", "## 可复现生成", "", "从项目根目录运行：", "", "```sh", "python3 tools/build_interface_comparison.py --reference-root /绝对路径/freeswitch-1.11.3", "python3 tools/build_interface_comparison.py --reference-root /绝对路径/freeswitch-1.11.3 --check", "```", "", "生成器只读固定源码与已有标准，写本页和 comparison.json；`--check` 不写文件，比较现有产物与重新生成字节。没有网络、运行时探测或服务副作用。若输入数量、源码哈希、ID、链接或人工证据锚点不匹配则失败，不能静默减少覆盖分母。", ""]
    return "\n".join(lines)


def main():
    """解析离线生成参数；检查模式仅比较确定性产物，不修改已有接口文档。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project-root", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--reference-root", type=Path)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    project = args.project_root.resolve()
    reference = args.reference_root or project.parents[1] / "work/freeswitch-reference/freeswitch-1.11.3"
    builder = ComparisonBuilder(project, reference.resolve())
    result = builder.result()
    outputs = {project / "docs/api/comparison.json": json.dumps(result, ensure_ascii=False, indent=2) + "\n", project / DOCUMENT: render_markdown(result, builder.key_entries)}
    for path, content in outputs.items():
        if args.check:
            if not path.is_file() or path.read_bytes() != content.encode("utf-8"):
                raise SystemExit("生成产物与输入不一致：" + str(path))
        else:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8")
    print(json.dumps({"mode": "check" if args.check else "generate", "total": result["summary"]["total"], "baseline_counts": result["summary"]["baseline_counts"], "manual_semantic_entries": len(builder.key_entries), "verified_source_files": len(builder.sources), "unique_ids": True, "source_links_valid": True, "runtime_compatibility_tested": False}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
