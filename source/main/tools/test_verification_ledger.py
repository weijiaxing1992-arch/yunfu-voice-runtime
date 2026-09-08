#!/usr/bin/env python3
# coding: utf-8
"""验证账本门禁的反例；只读取真实项目输入并修改内存副本，不运行原版或媒体服务。"""
import copy
import json
import unittest

import build_verification_ledger as ledger_tool


class VerificationLedgerTests(unittest.TestCase):
    """一次重建真实账本，再逐项故意损坏，以确认静态关联不会变成假认证。"""

    @classmethod
    def setUpClass(cls):
        """从固定参考树与当前对照创建共享只读夹具，不依赖已提交账本恰好是最新。"""
        work = ledger_tool.ROOT.parents[1] / 'work/freeswitch-reference'
        cls.ledger = ledger_tool.build(work / 'freeswitch-1.11.3', work / 'tree-v1.11.3-complete.json')
        cls.comparison = json.loads((ledger_tool.ROOT / ledger_tool.COMPARISON).read_text())

    def test_all_original_records_and_static_counterexamples(self):
        """原始每个ID都出现一次，并拒绝内置的漏项、重项与虚假运行证据。"""
        self.assertEqual(ledger_tool.self_test(self.ledger, self.comparison), 6)

    def test_missing_source_and_test_references_are_rejected(self):
        """孤立证据ID不能被账本当成存在的来源或回归。"""
        for field in ('source_reference', 'local_evidence_ids', 'local_test_coverage'):
            with self.subTest(field=field):
                value = copy.deepcopy(self.ledger)
                if field == 'source_reference':
                    value['entries'][0][field]['file_id'] = 'missing-source'
                elif field == 'local_evidence_ids':
                    value['entries'][0][field] = ['missing-evidence']
                else:
                    value['entries'][0][field]['direct_definition_ids'] = ['missing-test']
                with self.assertRaises(ValueError):
                    ledger_tool.validate_ledger(value, self.comparison)

    def test_domain_denominator_is_closed(self):
        """从业务域移除一个关联必须失败，不能只检查35个域标题还在。"""
        value = copy.deepcopy(self.ledger)
        next(group for group in value['domain_coverage'] if group['comparison_ids'])['comparison_ids'].pop()
        with self.assertRaises(ValueError):
            ledger_tool.validate_ledger(value, self.comparison)

    def test_no_generic_xml_tests_become_parameter_business_passes(self):
        """875个配置出现位置只关联通用编辑器，不产生875项原模块业务通过。"""
        entries = [row for row in self.ledger['entries'] if row['category'] == 'fs_vanilla_parameter']
        self.assertEqual(len(entries), self.comparison['summary']['baseline_counts']['vanilla_parameters'])
        for row in entries:
            self.assertEqual(row['local_test_coverage']['direct_definition_ids'], [])
            self.assertFalse(row['local_test_coverage']['generic_tests_prove_each_original_interface'])
            self.assertIsNone(row['paired_verification']['passed'])
            self.assertEqual(row['implementation_status'], 'export_only')

    def test_dynamic_codec_definitions_have_source_without_execution(self):
        """21个动态与7个显式用例全部入账；格式行、工厂行和注册行均有来源，不伪造运行。"""
        definitions = [value for value in self.ledger['test_definitions'].values()
                       if value['path'] == 'tests/codec_e2e.py']
        dynamic = [value for value in definitions if 'discovery' in value]
        self.assertEqual(len(definitions), 28)
        self.assertEqual(len(dynamic), 21)
        source = (ledger_tool.ROOT / 'tests/codec_e2e.py').read_text().splitlines()
        for value in dynamic:
            self.assertEqual(value['execution_status'], 'not_run_by_ledger')
            self.assertFalse(value['upstream_differential_test'])
            self.assertFalse(value['discovery']['executed'])
            self.assertIn('CodecCase(', source[value['discovery']['case_line'] - 1])
            self.assertIn('def install_codec_case', source[value['discovery']['factory_line'] - 1])
            self.assertIn('in CASES:', source[value['discovery']['registration_line'] - 1])
        identifiers = {value['id'] for value in definitions}
        for row in self.ledger['entries']:
            if row['id'] in {'key-sip-sdp', 'key-media-rtp', 'key-media-rtcp', 'key-media-dtmf', 'key-media-transcoding'}:
                self.assertTrue(identifiers.issubset(row['local_test_coverage']['related_definition_ids']))
                self.assertIsNone(row['paired_verification']['passed'])
            if row['category'] == 'fs_module':
                self.assertFalse(identifiers.intersection(row['local_test_coverage']['related_definition_ids']))

    def test_codec_inventory_never_loads_module_or_calls_expressions(self):
        """顶层异常不会执行；CASES中的未知调用被拒绝，不能通过动态清单执行任意代码。"""
        source = (ledger_tool.ROOT / 'tests/codec_e2e.py').read_text()
        self.assertEqual(len(ledger_tool.codec_dynamic_definitions('raise RuntimeError("不得执行模块")\n' + source)), 21)
        invalid = source.replace('CodecCase("pcmu",', 'CodecCase(__import__("os").system("false"),', 1)
        with self.assertRaises(ValueError):
            ledger_tool.codec_dynamic_definitions(invalid)

    def test_dynamic_codec_schema_changes_fail_closed(self):
        """重复名、未知工厂、无界range和过大常量集合必须明确失败，不能静默删掉定义。"""
        source = (ledger_tool.ROOT / 'tests/codec_e2e.py').read_text()
        variants = [
            source.replace('CodecCase("pcma",', 'CodecCase("pcmu",', 1),
            source.replace('self.relay_codec(case)', 'self.other_method(case)'),
            source.replace('for rate in (16, 24, 32, 40)', 'for rate in range(1000000)'),
            source.replace('for rate in (16, 24, 32, 40)', 'for rate in (' + ','.join(map(str, range(257))) + ')'),
        ]
        for index, invalid in enumerate(variants):
            with self.subTest(index=index), self.assertRaises(ValueError):
                ledger_tool.codec_dynamic_definitions(invalid)


if __name__ == '__main__':
    unittest.main()
