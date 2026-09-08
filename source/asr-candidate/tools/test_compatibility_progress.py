"""验证计数变化不能通过丢ID、重复记录、篡改输入或凭空标绿产生。"""
import copy
import json
import unittest
import build_compatibility_progress as progress


class CompatibilityProgress(unittest.TestCase):
    def setUp(self):
        self.comparison = {'entries': [self.entry('a', 'partial'), self.entry('b', 'not_implemented')]}
        self.runtime = {'entries': [self.proof('a', 'partial', 'passed'), self.proof('b', 'not_implemented', 'not_run')],
                        'summary': {'by_local_status': {'passed': 1, 'not_run': 1}, 'green_entries': 1}, 'source_snapshot': {'unit.go': 'a' * 64}}
        self.baseline = {'version': '1.7.0', 'entries': [{'id': 'a', 'status': 'not_implemented', 'local_status': 'not_run', 'green_eligible': False},
                                                     {'id': 'b', 'status': 'partial', 'local_status': 'passed', 'green_eligible': True}]}

    @staticmethod
    def entry(identifier, status):
        return {'id': identifier, 'status': status, 'name': identifier, 'category': 'fs_event', 'module': 'core',
                'differences': ['缺少完整字段'], 'document_path': 'api/example.md', 'source_url': 'https://example.com/source'}

    @staticmethod
    def proof(identifier, status, local):
        return {'comparison_id': identifier, 'implementation_status': status, 'local_status': local,
                'green_eligible': local == 'passed', 'scope': '限定测试', 'reason': '未执行'}

    def build(self):
        raw = progress.wire(self.comparison)
        self.runtime['comparison_sha256'] = progress.sha(raw)
        return progress.build(raw, progress.wire(self.runtime), progress.wire(self.baseline))

    def test_new_and_lost_green_are_independent_even_when_total_unchanged(self):
        value, work = self.build()
        self.assertEqual(value['baseline']['green_entries'], value['current']['green_entries'])
        self.assertEqual([x['id'] for x in value['changes']['new_green']], ['a'])
        self.assertEqual([x['id'] for x in value['changes']['lost_green']], ['b'])
        self.assertEqual(len(value['changes']['implementation_changed']), 2)
        self.assertEqual(len(work['entries']), 2)
        self.assertTrue(all(not x['complete_contract_passed'] for x in work['entries']))

    def test_added_and_removed_ids_cannot_hide_denominator_changes(self):
        self.baseline['entries'][1]['id'] = 'removed'
        value, _ = self.build()
        self.assertEqual(value['changes']['added_ids'], ['b'])
        self.assertEqual(value['changes']['removed_ids'], ['removed'])
        self.assertEqual(value['changes']['lost_green'][0]['id'], 'removed')

    def test_missing_and_duplicate_proofs_rejected(self):
        original = copy.deepcopy(self.runtime)
        for mode in ['missing', 'duplicate']:
            with self.subTest(mode=mode):
                self.runtime = copy.deepcopy(original)
                if mode == 'missing': self.runtime['entries'].pop()
                else: self.runtime['entries'].append(self.runtime['entries'][0])
                with self.assertRaises(ValueError): self.build()

    def test_green_requires_pass_and_summary_must_recompute(self):
        for mutate in [lambda r: r['entries'][1].update(green_eligible=True),
                       lambda r: r['summary'].update(green_entries=2),
                       lambda r: r['summary'].update(by_local_status={'passed': 2}),
                       lambda r: r['entries'][0].update(implementation_status='implemented')]:
            original = copy.deepcopy(self.runtime)
            mutate(self.runtime)
            with self.assertRaises(ValueError): self.build()
            self.runtime = original

    def test_runtime_cannot_bind_another_comparison(self):
        self.runtime['comparison_sha256'] = '0' * 64
        with self.assertRaises(ValueError):
            progress.build(progress.wire(self.comparison), progress.wire(self.runtime), progress.wire(self.baseline))

    def test_baseline_requires_boolean_green_and_unique_id(self):
        self.baseline['entries'][0]['green_eligible'] = 'false'
        with self.assertRaises(ValueError): self.build()
        self.baseline['entries'][0]['green_eligible'] = False
        self.baseline['entries'][1]['id'] = 'a'
        with self.assertRaises(ValueError): self.build()

    def test_full_worklist_keeps_export_and_native_entries(self):
        for index, category, status in [('c', 'fs_native_function', 'not_implemented'), ('d', 'vanilla_parameter', 'export_only')]:
            entry = self.entry(index, status); entry['category'] = category
            self.comparison['entries'].append(entry)
            self.runtime['entries'].append(self.proof(index, status, 'not_run'))
        self.runtime['summary']['by_local_status']['not_run'] = 3
        _, work = self.build()
        self.assertEqual({x['id'] for x in work['entries']}, {'a', 'b', 'c', 'd'})
        self.assertTrue(all(row['priority'] == 'P2' for row in work['entries'] if row['id'] in {'c', 'd'}))
        self.assertEqual(work['summary']['complete_contract_passed'], 0)


    def test_deferred_product_scope_never_removes_or_greens_a_missing_feature(self):
        """首期不做会议仍必须保留未实现ID，不用缩小目标伪造兼容通过。"""
        self.comparison['entries'][1].update(name='conference', category='fs_application', module='mod_conference')
        value, work = self.build()
        row = next(row for row in work['entries'] if row['id'] == 'b')
        self.assertEqual(row['delivery_scope'], 'deferred')
        self.assertEqual(row['implementation_status'], 'not_implemented')
        self.assertEqual(row['local_status'], 'not_run')
        self.assertFalse(row['green_eligible'])
        self.assertEqual(value['current']['comparison_entries'], 2)

    def test_core_and_telephony_adapter_have_distinct_priorities(self):
        """媒体目标与FS兼容入口各有研发归属，不把视频UUID命令误列首期。"""
        row = self.entry('key-media-capacity', 'not_verified')
        self.assertEqual(progress.priority(row)[0], 'P0')
        row.update(id='registration:api:mod_commands:uuid_send_dtmf:1', category='fs_api', name='uuid_send_dtmf')
        self.assertEqual(progress.delivery_scope(row)[0], 'compatibility_adapter')
        self.assertEqual(progress.priority(row)[0], 'P1')
        row.update(name='uuid_video_refresh')
        self.assertEqual(progress.delivery_scope(row)[0], 'deferred')


if __name__ == '__main__':
    unittest.main()
