#!/usr/bin/env python3
# coding: utf-8
"""逐项冻结FreeSWITCH对照的可验证性账本；静态关联与用例定义绝不计为互通通过。"""
import argparse
import ast
from collections import Counter
import csv
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
COMPARISON = 'docs/api/comparison.json'
STATUSES = {'implemented', 'partial', 'export_only', 'not_implemented', 'internal_only', 'not_verified'}
STATUS_LABELS = {'implemented': '自有接口已实现', 'partial': '部分相关能力', 'export_only': '配置产物',
                 'not_implemented': '兼容入口未实现', 'internal_only': '内部合同', 'not_verified': '验收未执行'}
# 类别关联说明审查所用边界，不能把一份通用测试扩散为同类别所有原接口已通过。
CATEGORY_EVIDENCE = {
    'fs_api': ['native_abi', 'config'], 'fs_application': ['native_abi', 'media_commands'],
    'fs_json_api': ['native_abi', 'http_admin'], 'fs_chat_application': ['native_abi', 'sip_dispatch'],
    'fs_module': ['native_abi', 'protocol_abi'], 'fs_native_function': ['native_abi', 'codec_loader'],
    'fs_event': ['journal', 'http_admin'], 'fs_channel_variable': ['config', 'xml_export'],
    'fs_conference_subcommand': ['media_commands', 'xml_export'],
    'fs_sofia_subcommand': ['sip_dispatch', 'xml_export'], 'fs_vanilla_parameter': ['xml_export'],
    'acceptance_definition': [], 'key_http': ['http_admin'], 'key_esl': ['http_admin', 'native_abi'],
    'key_config': ['config', 'xml_export'], 'key_guard': ['guard'], 'key_sip': ['sip_dispatch', 'sdp_limits'],
    'key_media': ['media_commands', 'rtp'], 'key_ipc': ['media_commands'],
    # 私有PCM服务及进程内RX SDK由SIP授权后进入媒体；不归为HTTP或原版原生API。
    'key_api': ['sip_dispatch', 'media_commands'],
    'key_c_abi': ['native_abi', 'protocol_abi'], 'key_cli': ['go_main'],
}
CATEGORY_DOMAINS = {'fs_native_function': 'native', 'fs_event': 'events', 'fs_channel_variable': 'xml',
                    'fs_vanilla_parameter': 'xml', 'acceptance_definition': 'commands', 'key_http': 'commands',
                    'key_esl': 'esl', 'key_config': 'xml', 'key_guard': 'admission', 'key_sip': 'sip',
                    'key_media': 'media', 'key_ipc': 'native', 'key_api': 'media', 'key_c_abi': 'native', 'key_cli': 'commands'}
RELATED_TEST_FILES = {
    'key_http': ['control/internal/server/admin_test.go', 'control/internal/server/admin_budget_test.go'],
    'key_config': ['control/internal/server/admin_test.go', 'control/internal/server/fs_config_validation_test.go'],
    'fs_vanilla_parameter': ['control/internal/server/admin_test.go', 'control/internal/server/fs_config_validation_test.go'],
    'key_guard': ['control/internal/server/guard_test.go'],
    'key_sip': ['control/internal/sip/message_test.go', 'control/internal/sip/sdp_test.go', 'tests/e2e.py'],
    'key_media': ['control/internal/sip/sdp_test.go', 'tests/e2e.py'],
    'key_ipc': ['control/internal/server/call_lifecycle_test.go'],
    'key_api': ['control/internal/server/pcm_endpoint_test.go', 'control/internal/server/pcm_stream_test.go',
                'tests/sip_pcm_stream_e2e.py'],
    'key_cli': ['control/internal/server/benchmark_test.go'],
}
# 只向这五个内部合同关联RX用例，不将上行测试扩散到全部IPC/原版接口。
RX_IDS = {'key-ipc-rx-subscribe', 'key-ipc-rx-status', 'key-ipc-rx-unsubscribe',
          'key-ipc-rx-stream', 'key-api-rx-sdk'}
RX_TEST_FILES = {'tests/media_rx_stream_e2e.py', 'control/internal/media/rx_stream_test.go',
                 'control/internal/media/rx_clock_test.go', 'control/internal/media/processing_rx_real_test.go',
                 'control/internal/server/rx_stream_test.go', 'control/internal/server/rx_stream_real_test.go'}
# 仅维护已经人工读过其目标的直接本地回归；“有定义”仍不表示本轮执行、逐断言覆盖或原版等价。
DIRECT_TESTS = {
    'key-http-codecs': ['TestAudioCapabilityContractAndBound', 'TestCodecCatalogMissingBackend'],
    'key-http-authentication': ['TestAdminProtection'],
    'key-http-config-write': ['TestAdminConfigurationWaitsForRestart', 'TestAdminSaveFailureDoesNotApply'],
    'key-http-concurrency': ['TestAdminGuardPersistenceAndConflict'],
    'key-http-status': ['TestAdminSlowFSHTTPKeepsStatusAndDrainResponsive'],
    'key-http-readiness': ['TestGuardReadinessTracksAdmission'],
    'key-config-parameter-edit': ['TestFSXMLPatchAndExport', 'TestFSParameterPatchPreservesNamespacedMetadata'],
    'key-config-raw-xml': ['TestFSConfigSaveAndReload', 'TestFSRejectsDuplicateAttributes'],
    'key-config-export': ['TestFSXMLPatchAndExport'],
    'key-config-persistence': ['TestAdminBaseChangePreservesProtection'],
    'key-guard-capacity': ['TestGuardPeakBoundaries', 'TestAdminGuardPersistenceAndConflict'],
    'key-sip-sdp': ['TestUnsupportedPacketDurationOnlySkipsThatCandidate', 'TestAudioCodecMappingsRoundTrip', 'TestCodecMappingAndFMTPRejections', 'TestAnswerNegotiationRejectsImplicitTransforms'],
    'key-sip-cancel': ['test_cancel_then_late_200_is_acked_and_torn_down', 'TestForkFinalRetransmissionKeepsOneCleanupTransaction'],
    'key-sip-bye': ['TestForkCleanupFinalResponseRequiresExactTransaction', 'TestForkCleanupStateBoundAndStatelessOverflow', 'TestForkCleanupGlobalTransactionPressureDoesNotAddRetryContext', 'TestForkCleanupExpiresAndCallGCRemovesItsTimers'],
    'key-media-rtp': ['test_bidirectional_rtp_rtcp_dtmf_and_cleanup'],
    'key-media-rtcp': ['test_bidirectional_rtp_rtcp_dtmf_and_cleanup'],
    'key-media-dtmf': ['TestAuxiliaryPayloadClockAndEvents', 'test_bidirectional_rtp_rtcp_dtmf_and_cleanup'],
    'key-media-failover': ['test_other_shard_survives_media_process_failure', 'test_media_worker_failure_and_restart'],
    # 下列关联来自逐断言审查；这里只定位定义，真实执行及候选指纹由独立验证器检查。
    'key-ipc-rx-subscribe': ['test_unsubscribe_idempotence_and_new_subscription_fence',
                           'test_relay_bridge_and_unknown_session_do_not_accept_rx',
                           'test_without_rx_fd_never_advertises_or_accepts_subscription'],
    'key-ipc-rx-status': ['test_exact_both_laws_wrap_metadata_and_status_never_replays',
                        'test_slow_reader_exports_gap_and_preserves_exact_accounting',
                        'TestRXControlCountersAndMissingFields'],
    'key-ipc-rx-unsubscribe': ['test_unsubscribe_idempotence_and_new_subscription_fence',
                             'test_release_reuse_rejects_old_session_and_preserves_new_identity',
                             'TestRXRevokeReadClearsSamplesAndKeepsControl'],
    'key-ipc-rx-stream': ['test_exact_both_laws_wrap_metadata_and_status_never_replays',
                        'test_rx_does_not_include_tone_or_active_dtmf_tx',
                        'test_cn_original_sid_and_six_plc_tail_suspend_without_silence',
                        'test_inbound_telephone_event_is_auxiliary_and_never_pcm',
                        'test_slow_reader_exports_gap_and_preserves_exact_accounting',
                        'TestRXUnixSocketResidenceRejectsTwoOldFramesWithoutBackpressure'],
    'key-api-rx-sdk': ['TestRealRXServerSIPAuthorizationSamplesAndCleanup',
                      'TestRXSDKWrongACKCannotAuthorizeButExactACKCan',
                      'TestRXSDKSameIDNeverRevivesAndHigherIDNeedsTerminal',
                      'TestRXSDKUnknownCancellationRetainsClosedCleanupHandle',
                      'TestRXSDKRevokeWakesReaderAndRetainsActualCleanup',
                      'TestRXSDKLateReadAndExpiredFrameRejectedAtServerBoundary'],
}
CODEC_NOTE = ('多编码的控制协商、测试向量、Rust worker实际接受、原生编解码自检、跨编码通话是五项独立证据。'
              '当前源树已扩展明确编码身份与RTP时钟，旧G.711-only措辞不再准确；不能从格式列表推导原模块兼容或转码通过。')
GAPS = {
    'implemented': '这是自有接口实现；原版协议、参数、输出及副作用不等价，缺少成对运行轨迹。',
    'partial': '仅有明确范围内相关能力；其余原接口分支、状态与业务副作用没有完成等价实现和成对验收。',
    'export_only': '只验证配置产物的解析、编辑或导出；当前业务引擎未执行原XML/模块语义。',
    'not_implemented': '缺少该原版兼容入口或模块宿主；不存在可直接运行该原接口的本地实现测试。',
    'internal_only': '这是Go/Rust或自定义C内部合同；不属于原FreeSWITCH线协议或宿主ABI兼容实现。',
    'not_verified': '只有验收定义或局部工具，未收集此项所需的原版运行基线、成对轨迹及全部强制断言。',
}


def require(condition, message):
    """所有证据门禁都使用显式异常，不能被Python优化模式移除。"""
    if not condition:
        raise ValueError(message)


def sha(data):
    """统一记录输入字节和原始条目的SHA256。"""
    return hashlib.sha256(data).hexdigest()


def load_module(name, path):
    """只加载项目生成器的定义；不调用main、不写其产物或执行运行测试。"""
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def codec_dynamic_definitions(text):
    """仅解析已知CodecCase声明/注册语法，不import、eval或运行测试文件。

    工厂或注册方式变化时明确拒绝，要求维护者复核；不能猜测动态测试名而静默漏项。
    常量、单层有限生成式及无格式转换的f-string足够描述当前21个线路格式。
    """
    require(len(text.encode('utf-8')) <= 1 << 20, '动态codec定义文件超过1MiB')
    tree = ast.parse(text)
    declarations = [node for node in tree.body if isinstance(node, ast.Assign)
                    and any(isinstance(target, ast.Name) and target.id == 'CASES' for target in node.targets)]
    factories = [node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == 'install_codec_case']
    registrations = [node for node in tree.body if isinstance(node, ast.For)
                     and isinstance(node.iter, ast.Name) and node.iter.id == 'CASES']
    require(len(declarations) == len(factories) == len(registrations) == 1, 'codec动态声明/工厂/注册必须各唯一')
    factory = factories[0]
    expected = ast.parse('''def install_codec_case(case):
    def test(self):
        self.relay_codec(case)
    test.__name__ = "test_relay_" + case.key
    setattr(CodecIntegration, test.__name__, test)
''').body[0]
    # 文档字符串不改变工厂结构；其余表达式、装饰器和函数体必须完全匹配已审阅形状。
    for node in ast.walk(factory):
        if isinstance(node, ast.FunctionDef) and node.body and isinstance(node.body[0], ast.Expr):
            first = node.body[0].value
            if isinstance(first, ast.Constant) and isinstance(first.value, str):
                node.body.pop(0)
    require(ast.dump(factory) == ast.dump(expected), 'codec动态测试工厂已变化，需重新审查')
    registration = registrations[0]
    require(isinstance(registration.target, ast.Name), 'codec注册变量必须是简单名称')
    name = registration.target.id
    expected_loop = ast.parse(f'for {name} in CASES:\n    install_codec_case({name})').body[0]
    require(ast.dump(registration) == ast.dump(expected_loop), 'codec注册循环已变化，需重新审查')

    def scalar(node, environment):
        """有限常量求值；函数调用、属性访问、下标、格式代码等一律不能执行。"""
        if isinstance(node, ast.Constant) and type(node.value) in {str, int}:
            require(len(str(node.value)) <= 4096, 'codec常量超过长度边界')
            return node.value
        if isinstance(node, ast.Name) and node.id in environment:
            return environment[node.id]
        if isinstance(node, ast.JoinedStr):
            parts = []
            for value in node.values:
                if isinstance(value, ast.Constant) and isinstance(value.value, str):
                    parts.append(value.value)
                else:
                    require(isinstance(value, ast.FormattedValue) and value.conversion == -1
                            and value.format_spec is None, 'codec f-string只允许简单常量插值')
                    parts.append(str(scalar(value.value, environment)))
            return ''.join(parts)
        raise ValueError('codec动态声明包含非许可常量表达式')

    result = []

    def visit(node, environment, generated=False):
        """展开至多256个格式，只允许一层显式有限常量循环，防止组合爆炸。"""
        if isinstance(node, (ast.Tuple, ast.List)):
            require(len(node.elts) <= 256, 'codec声明集合过大')
            for item in node.elts:
                visit(item, environment, generated)
        elif isinstance(node, ast.Starred):
            require(isinstance(node.value, ast.GeneratorExp) and not generated, 'codec仅允许一层生成式展开')
            visit(node.value, environment, generated)
        elif isinstance(node, ast.GeneratorExp):
            require(not generated and len(node.generators) == 1, 'codec不允许嵌套或多层生成式')
            loop = node.generators[0]
            require(isinstance(loop.target, ast.Name) and not loop.ifs and not loop.is_async
                    and isinstance(loop.iter, (ast.Tuple, ast.List)) and len(loop.iter.elts) <= 256,
                    'codec循环必须遍历有界常量列表且没有过滤器')
            for item in loop.iter.elts:
                visit(node.elt, {**environment, loop.target.id: scalar(item, environment)}, True)
        else:
            require(isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == 'CodecCase'
                    and 5 <= len(node.args) <= 7 and not node.keywords, 'codec格式必须是已审阅的CodecCase位置参数')
            values = [scalar(value, environment) for value in node.args]
            key = values[0]
            require(isinstance(key, str) and re.fullmatch('[a-z][a-z0-9_]{0,95}', key), 'codec用例key无效')
            require(len(result) < 256, '动态codec用例超过256项')
            result.append(('test_relay_' + key, node.lineno,
                           {'kind': 'limited_ast_dynamic_definition', 'case_line': node.lineno,
                            'factory_line': factory.lineno, 'registration_line': registration.lineno,
                            'executed': False}))
    visit(declarations[0].value, {})
    require(result and len({value[0] for value in result}) == len(result), 'codec动态用例为空或名称重复')
    return result


def test_inventory():
    """静态枚举Go/Python/Rust用例定义，记录真实行号；不从函数名称推导断言通过。"""
    result = {}
    paths = set(ROOT.glob('control/**/*_test.go')) | set(ROOT.glob('tests/*.py')) | set(ROOT.glob('media/**/*.rs'))
    for path in sorted(paths):
        if '/target/' in path.as_posix():
            continue
        raw = path.read_bytes()
        text = raw.decode('utf-8')
        relative = path.relative_to(ROOT).as_posix()
        if path.suffix == '.py':
            definitions = [(node.name, node.lineno) for node in ast.walk(ast.parse(text))
                           if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name.startswith('test_')]
        elif path.suffix == '.go':
            definitions = [(m[1], text.count('\n', 0, m.start()) + 1)
                           for m in re.finditer(r'^func (Test\w+)\(t \*testing\.T\)', text, re.M)]
        else:
            definitions = [(m[1], text.count('\n', 0, m.start(1)) + 1)
                           for m in re.finditer(r'#\[test\]\s*(?:(?://[^\n]*\n)\s*)*fn\s+(\w+)\s*\(', text)]
        definitions = [(name, line, None) for name, line in definitions]
        if relative == 'tests/codec_e2e.py':
            definitions.extend(codec_dynamic_definitions(text))
        for name, line, discovery in definitions:
            identifier = relative + '::' + name
            require(identifier not in result, '重复用例定义ID：' + identifier)
            result[identifier] = {'id': identifier, 'path': relative, 'line': line, 'name': name,
                                  'file_sha256': sha(raw), 'execution_status': 'not_run_by_ledger',
                                  'upstream_differential_test': False}
            if discovery is not None:
                result[identifier]['discovery'] = discovery
    return result


def validate_ledger(ledger, comparison):
    """逐原ID闭合分母并禁止把静态关联、未知或未跑包装为互通通过。"""
    wanted = [row['id'] for row in comparison['entries']]
    actual = [row['id'] for row in ledger['entries']]
    require(len(wanted) == len(set(wanted)), '原对照ID不唯一')
    require(len(actual) == len(set(actual)), '账本ID重复')
    require(set(actual) == set(wanted), '账本遗漏或新增非原始对照记录')
    require(len(actual) == comparison['summary']['total'], '原对照summary分母与实际记录不一致')
    require(ledger['counts']['comparison_records'] == len(actual), '账本汇总分母错误')
    originals = {row['id']: row for row in comparison['entries']}
    for row in ledger['entries']:
        original = originals[row['id']]
        require(row['implementation_status'] == original['status'] in STATUSES, '静默改变原对照实现状态')
        require(row['original_record_sha256'] == sha(json.dumps(original, ensure_ascii=False, sort_keys=True).encode()), '原条目指纹不一致')
        require(row['upstream_runtime_verification']['executed'] is False and row['upstream_runtime_verification']['result'] == 'unknown', '无运行环境却宣告原版执行结果')
        require(row['paired_verification']['status'] == 'not_run' and row['paired_verification']['passed'] is None, '静态账本不能产生互通通过')
        require(bool(row['gap_reason']), '缺少缺口解释')
        require(row['source_reference']['file_id'] in ledger['source_files'], '原版来源文件引用不存在')
        for evidence in row['local_evidence_ids']:
            require(evidence in ledger['local_evidence'], '本地证据引用不存在')
        for test in row['local_test_coverage']['direct_definition_ids'] + row['local_test_coverage']['related_definition_ids']:
            require(test in ledger['test_definitions'], '回归定义引用不存在')
    require(len(ledger['module_coverage']) == 145 and len(ledger['domain_coverage']) == 35, '模块/核心业务域审计分母变化')
    domain_ids = [identifier for group in ledger['domain_coverage'] for identifier in group['comparison_ids']]
    require(len(domain_ids) == len(set(domain_ids)) and set(domain_ids) == set(actual), '业务域关联遗漏或重复原对照项')
    require(ledger['certification'] == 'not_certified', '账本不得生成兼容认证')


def build(reference, tree_path):
    """读取当前完整对照与重新计算的模块审计，逐条给出来源、实现边界、测试定义和未跑缺口。"""
    comparison = json.loads((ROOT / COMPARISON).read_text())
    audit_tool = load_module('verification_feature_audit', ROOT / 'tools/build_feature_audit.py')
    audit = audit_tool.build(reference, tree_path)
    require(comparison['reference_commit'] == audit['reference_commit'], '对照与模块审计基线不一致')
    comparison_tool = load_module('verification_comparison', ROOT / 'tools/build_interface_comparison.py')
    locator = comparison_tool.ComparisonBuilder(ROOT, reference)
    locator.critical_entries()
    # RX生成方法只添加内部合同并重定位既有语义；不调用result/main，也不执行测试或写产物。
    locator.rx_stream_entries()
    keys = {row['id']: row for row in locator.key_entries}
    tests = test_inventory()
    by_name = {}
    for identifier, value in tests.items():
        by_name.setdefault(value['name'], []).append(identifier)
    local = {value['id']: dict(value, kind='reviewed_scope_boundary', runtime_execution_verified=False) for value in audit['evidence']}
    inputs = {item['path']: item for item in audit['runtime_source_snapshot'] + audit['inputs']}
    source_files = {}
    modules = {row['module']: row for row in audit['modules']}
    entries = []
    for original in comparison['entries']:
        identifier, category, status = original['id'], original['category'], original['status']
        require(category in CATEGORY_EVIDENCE, '新类别需要明确审查：' + category)
        evidence_ids = list(CATEGORY_EVIDENCE[category])
        note = ''
        if identifier in keys:
            # 重新从维护中的原生成器找锚点，不能沿用comparison里可能已经变化的本地行号。
            match = re.search(r'RustSwitch 源码 ([^：\s]+):(\d+)。', keys[identifier]['verification'])
            require(match is not None, '关键对照缺少可定位的本地来源：' + identifier)
            path, line = match[1], int(match[2])
            raw = (ROOT / path).read_bytes()
            evidence_id = 'key-local:' + identifier
            local[evidence_id] = {'id': evidence_id, 'path': path, 'line': line, 'sha256': sha(raw),
                                  'kind': 'anchored_local_contract', 'description': '关键对照维护者明确关联的当前源码锚点，不表示原版等价。',
                                  'runtime_execution_verified': False}
            evidence_ids.append(evidence_id)
        if category == 'fs_vanilla_parameter':
            path = 'control/internal/server/fs_templates/' + original['source_path'].removeprefix('conf/vanilla/')
            raw = (ROOT / path).read_bytes()
            evidence_id = 'xml-artifact:' + path
            local[evidence_id] = {'id': evidence_id, 'path': path, 'line': 1, 'sha256': sha(raw),
                                  'kind': 'embedded_configuration_artifact', 'description': '对应官方配置文件确实内嵌；本参数的业务语义没有执行。',
                                  'runtime_execution_verified': False}
            evidence_ids.append(evidence_id)
            inputs[path] = {'path': path, 'sha256': sha(raw), 'bytes': len(raw)}
        if identifier in {'key-sip-sdp', 'key-media-rtp', 'key-media-transcoding', 'key-c_abi-codec', 'key-http-codecs'}:
            evidence_ids += ['sdp_codecs', 'codec_profiles', 'codec_answer']
            note = CODEC_NOTE
        if identifier in RX_IDS:
            note = ('原生8k A腿RX、RXS2及正确ACK后的进程内Go SDK已有明确合同；'
                    '不存在同名FreeSWITCH线协议或外部HTTP/Unix RX API。'
                    '外部ASR适配、按需重采样、VAD与识别结果仍未接入；相关用例定义不产生绿色或原版兼容认证。')
        source_kind = 'upstream_fixed_source' if original['source_url'] else 'local_acceptance_definition'
        source_path, line = original['source_path'], original['source_line']
        base = reference if source_kind == 'upstream_fixed_source' else ROOT
        raw = (base / source_path).read_bytes()
        require(type(line) is int and 1 <= line <= len(raw.splitlines()), '原声明行号超出真实文件：' + identifier)
        source_id = source_kind + ':' + source_path
        if source_kind == 'upstream_fixed_source':
            require(source_path in locator.sources and raw.decode('utf-8', errors='replace') == locator.sources[source_path], '声明不在已校验固定源码集合中')
            require(original['source_url'] == audit_tool.SOURCE_URL + source_path + '#L' + str(line), '官方链接未锁定相同提交、文件及行号')
        else:
            inputs[source_path] = {'path': source_path, 'sha256': sha(raw), 'bytes': len(raw)}
        source_files[source_id] = {'id': source_id, 'path': source_path, 'kind': source_kind,
                                   'sha256': sha(raw), 'integrity': 'verified_fixed_manifest' if original['source_url'] else 'current_local_definition'}
        direct = []
        for name in DIRECT_TESTS.get(identifier, []):
            require(name in by_name, '人工关联回归定义消失：' + name)
            direct.extend(by_name[name])
        related = [key for key, value in tests.items() if value['path'] in RELATED_TEST_FILES.get(category, []) and key not in direct]
        if identifier in RX_IDS:
            related.extend(key for key, value in tests.items() if value['path'] in RX_TEST_FILES
                           and key not in direct and key not in related)
        if identifier in {'key-sip-sdp', 'key-media-rtp', 'key-media-rtcp', 'key-media-dtmf', 'key-media-transcoding'}:
            # 仅标注相关格式/拒绝边界用例；原版codec模块、转码功能不会因此变成已实现或已通过。
            related.extend(key for key, value in tests.items() if value['path'] == 'tests/codec_e2e.py'
                           and key not in direct and key not in related)
        if category.startswith('fs_') and category != 'fs_vanilla_parameter':
            # 原版API缺失时即使存在同名HTTP/媒体概念，也不传播任何通用回归覆盖。
            direct, related = [], []
        domain = modules.get(original['module'], {}).get('domain', CATEGORY_DOMAINS.get(category, 'commands'))
        entries.append({'id': identifier, 'category': category, 'name': original['name'], 'module': original['module'], 'domain': domain,
                        'original_record_sha256': sha(json.dumps(original, ensure_ascii=False, sort_keys=True).encode()),
                        'implementation_status': status, 'implementation_label': STATUS_LABELS[status],
                        'relationship': original['relationship'], 'current_review_note': note,
                        'local_evidence_ids': list(dict.fromkeys(evidence_ids)),
                        'local_source_coverage': 'configuration_artifact_and_generic_editor' if status == 'export_only' else 'scope_boundary_only' if status in {'not_implemented', 'not_verified'} else 'related_implementation_anchor',
                        'local_test_coverage': {'direct_definition_ids': sorted(direct), 'related_definition_ids': sorted(related),
                                                'execution_status': 'not_run_by_ledger', 'assertion_completeness': 'not_verified',
                                                'generic_tests_prove_each_original_interface': False},
                        'source_reference': {'file_id': source_id, 'line': line, 'url': original['source_url'], 'document_path': original['document_path'], 'meaning': 'declaration_or_concept_anchor_not_complete_runtime_contract'},
                        'upstream_runtime_verification': {'environment': 'not_collected', 'executed': False, 'result': 'unknown', 'artifact_ids': []},
                        'paired_verification': {'status': 'not_run', 'passed': None, 'artifact_ids': [], 'normalization_whitelist': []},
                        'gap_reason': GAPS[status] + (' 普通编辑器测试不能证明这个参数运行有效。' if category == 'fs_vanilla_parameter' else ''),
                        'required_next_step': ('按候选源码、二进制及原始媒体轨迹核验内部RX用例；外部ASR适配另验使用点期限、缺口语义、识别结果和容量。若提供FreeSWITCH适配入口，必须另外冻结原版合同并做成对验收。'
                                               if identifier in RX_IDS else
                                               '冻结原版构建、启用模块与客户端；将本条展开正常/异常/并发/超时/副作用用例，双方执行后保留原始报文、事件、文件和断言结果。')})
    entries.sort(key=lambda row: row['id'])
    for item in local.values():
        path = item['path']
        raw = (ROOT / path).read_bytes()
        inputs[path] = {'path': path, 'sha256': sha(raw), 'bytes': len(raw)}
    for value in tests.values():
        path = value['path']
        raw = (ROOT / path).read_bytes()
        inputs[path] = {'path': path, 'sha256': sha(raw), 'bytes': len(raw)}
    for path in [COMPARISON, 'tools/build_verification_ledger.py', 'tools/test_verification_ledger.py', 'tools/build_feature_audit.py',
                 'tools/build_interface_comparison.py', 'docs/freeswitch-compatibility/tools/build_catalog.py']:
        raw = (ROOT / path).read_bytes()
        inputs[path] = {'path': path, 'sha256': sha(raw), 'bytes': len(raw)}
    ledger = {'version': '1.0.0', 'scope': '逐原始对照记录的静态可验证性账本；不执行原版动态差分，不计算互通成功率',
              'reference_version': audit['reference_version'], 'reference_commit': audit['reference_commit'], 'certification': 'not_certified',
              'module_audit_snapshot_sha256': sha(json.dumps(audit, ensure_ascii=False, sort_keys=True).encode()),
              'counts': {'comparison_records': len(entries), 'by_implementation_status': dict(sorted(Counter(row['implementation_status'] for row in entries).items())),
                         'by_category': dict(sorted(Counter(row['category'] for row in entries).items())),
                         'with_direct_local_test_definitions': sum(bool(row['local_test_coverage']['direct_definition_ids']) for row in entries),
                         'local_test_definitions': len(tests), 'upstream_runtime_executions': 0, 'paired_passes': 0,
                         'module_audit_entries': len(audit['modules']), 'business_domains': len(audit['business_domains'])},
              'denominator_policy': {'comparison_ids_preserved_exactly': True, 'raw_baseline_counts': comparison['summary']['baseline_counts'],
                                     'unknown_not_run_failed_skipped_never_count_as_pass': True, 'runtime_test_execution_by_this_tool': False,
                                     'test_link_meaning': '用例定义和关联定位，不表示执行过或穷尽原接口断言'},
              'entries': entries, 'local_evidence': local, 'source_files': source_files, 'test_definitions': tests,
              'module_coverage': [{'id': row['id'], 'module': row['module'], 'status': row['status'],
                                   'comparison_ids': [entry['id'] for entry in entries if entry['module'] == row['module']],
                                   'original_module_runtime_verified': False, 'scope_note': row['current_scope']} for row in audit['modules']],
              'domain_coverage': [{'id': row['id'], 'name': row['name'], 'status': row['status'],
                                   'comparison_ids': [entry['id'] for entry in entries if entry['domain'] == row['id']],
                                   'paired_acceptance': row['paired_acceptance'], 'scope_note': row['current_scope']} for row in audit['business_domains']],
              'inputs': [inputs[path] for path in sorted(inputs)]}
    validate_ledger(ledger, comparison)
    return ledger


def markdown(ledger):
    """可读说明只汇总证据等级；全量逐条内容保留JSON和CSV，不用完成百分比混淆实现率。"""
    counts = ledger['counts']
    lines = ['# FreeSWITCH 逐项验证账本', '', '**目前不能无感替换FreeSWITCH，也没有全面超越或生产万路媒体认证。**', '',
             f"账本保留当前对照全部 {counts['comparison_records']} 个原始ID，另关联145项模块并集与35个业务域。原版运行环境未采集、动态差分执行0次。",
             '', '完整记录：[JSON](verification-ledger.json)；逐项便览：[CSV](verification-ledger.csv)。', '',
             '## 各层证据分别表示什么', '',
             '- 固定来源：文件哈希与声明行号可核验；头文件、注册宏或配置出现位置不是完整运行合同。',
             '- 本地源码：定位实际入口或缺失边界；配置文件存在不表示对应模块执行。',
             '- 本地测试：仅列真实用例定义及明确关联，未执行的用例保持未跑。通用XML回归不会变成875项参数业务通过。',
             '- 原版运行：环境和轨迹未知，executed=false、result=unknown。',
             '- 成对验收：status=not_run、passed=null；没有默许的归一化白名单，也没有兼容成功率。', '',
             '## 当前实现状态分布', '', '| 原对照状态 | 数量 | 含义 |', '| --- | ---: | --- |']
    for status, total in counts['by_implementation_status'].items():
        lines.append(f'| `{status}` | {total} | {STATUS_LABELS[status]} |')
    lines += ['', f"静态枚举{counts['local_test_definitions']}个本地用例定义，{counts['with_direct_local_test_definitions']}条对照有人工指定的直接本地回归定义；这些数字都不是通过数。", '',
              '## 多编码更新边界', '', CODEC_NOTE, '',
              '当前编码/格式来自控制协商与测试配置。G722音频采样率和RTP时钟分开、G726与AAL2打包分开、动态PT按rtpmap识别。'
              '音频自检、同编码字节往返、重新分包、跨编码转换及原FreeSWITCH codec模块宿主必须分别交付证据；不能把新增原生源码或构建脚本直接当作通话路径已完成。', '',
              '## 原始分母和新增范围', '',
              '账本的entries逐个保留comparison.json原ID、状态和原条目SHA256。新增关键对照会自动进入分母；原版注册、事件、变量、原生函数和参数清单不因自有扩展而减少。',
              '`mod_com_g729`仅完整树构建目录、`sdk/autotools`开发示例分别保留在145项模块审计中；未在原comparison出现的模块不会伪造一个原始记录。'
              '第三方、商业与现场自行构建模块仍不在已冻结现场清单中。', '',
              '## 复验方式', '', '在项目根目录先生成最终对照，再生成账本；源码或输入变化后旧账本的check会失败。', '',
              '```sh', 'python3 tools/build_feature_audit.py', 'python3 tools/build_interface_comparison.py',
              'python3 tools/build_verification_ledger.py --self-test', 'python3 tools/build_verification_ledger.py --check --self-test', '```', '',
              '另可执行 `python3 tools/test_verification_ledger.py`，覆盖来源/测试孤立引用、业务域漏项及配置通用回归不传播为业务通过。', '',
              '--check只读比对原ID一一覆盖、原条目指纹、固定源哈希、当前源码/用例行号、模块与业务域分母、反虚假通过门禁及产物字节。'
              '生成器不编译、不启动服务、不发媒体、不运行原版，不会把其他任务的口头测试结果自动录为本账本证据。', '',
              '下一步须冻结双方二进制、配置、依赖与客户端，为每个启用接口展开强制正常、边界、错误、并发、重试和副作用断言。'
              '保留原始报文/事件偏序/文件/外部请求，再分别出usage兼容、万路性能、drain和live迁移结论。', '']
    return '\n'.join(lines)


def csv_text(ledger):
    """CSV逐条保留ID及验证状态；复杂证据引用通过分号连接，可回查同名JSON。"""
    stream = io.StringIO(newline='')
    writer = csv.writer(stream, lineterminator='\n')
    writer.writerow(['id', 'category', 'name', 'module', 'implementation_status', 'local_source_coverage',
                     'direct_test_definitions', 'related_test_definitions', 'upstream_runtime_result', 'paired_status', 'gap_reason'])
    for row in ledger['entries']:
        coverage = row['local_test_coverage']
        writer.writerow([row['id'], row['category'], row['name'], row['module'], row['implementation_status'], row['local_source_coverage'],
                         ';'.join(coverage['direct_definition_ids']), ';'.join(coverage['related_definition_ids']), 'unknown', 'not_run', row['gap_reason']])
    return stream.getvalue()


def self_test(ledger, comparison):
    """用内存反例确认漏项、重项、假指纹和虚构运行成功都会被拒绝，不修改真实产物。"""
    validate_ledger(ledger, comparison)
    bad = []
    copy = dict(ledger, entries=ledger['entries'][1:])
    bad.append(copy)
    bad.append(dict(ledger, entries=ledger['entries'] + [ledger['entries'][0]]))
    for field, value in [('original_record_sha256', 'invalid'),
                         ('upstream_runtime_verification', {'executed': True, 'result': 'passed'}),
                         ('paired_verification', {'status': 'passed', 'passed': True})]:
        row = dict(ledger['entries'][0], **{field: value})
        bad.append(dict(ledger, entries=[row] + ledger['entries'][1:]))
    for fixture in bad:
        try:
            validate_ledger(fixture, comparison)
        except ValueError:
            continue
        raise ValueError('反例未被门禁拒绝')
    return 1 + len(bad)


def main():
    """只写本账本三个产物；可显式指定参考路径，离线check不改变版本观察日期。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, default=ROOT.parents[1] / 'work/freeswitch-reference/freeswitch-1.11.3')
    parser.add_argument('--tree', type=Path, default=ROOT.parents[1] / 'work/freeswitch-reference/tree-v1.11.3-complete.json')
    parser.add_argument('--check', action='store_true')
    parser.add_argument('--self-test', action='store_true')
    args = parser.parse_args()
    ledger = build(args.source, args.tree)
    checks = self_test(ledger, json.loads((ROOT / COMPARISON).read_text())) if args.self_test else 0
    outputs = {'verification-ledger.json': json.dumps(ledger, ensure_ascii=False, indent=2) + '\n',
               'verification-ledger.md': markdown(ledger), 'verification-ledger.csv': csv_text(ledger)}
    for name, value in outputs.items():
        path = ROOT / 'docs/api' / name
        if args.check:
            require(path.exists() and path.read_text() == value, '账本已过期，请按当前冻结输入重新生成：' + name)
        else:
            path.write_text(value, encoding='utf-8')
    print(json.dumps({'passed': True, 'mode': 'check' if args.check else 'generate', 'records': ledger['counts']['comparison_records'],
                      'modules': 145, 'domains': 35, 'self_tests': checks, 'upstream_runtime_executions': 0,
                      'certification': 'not_certified'}, ensure_ascii=False))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError) as error:
        print('验证账本失败：' + str(error), file=sys.stderr)
        raise SystemExit(1)
