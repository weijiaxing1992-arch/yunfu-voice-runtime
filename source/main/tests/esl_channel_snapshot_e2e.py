#!/usr/bin/env python3
"""实际SIP/Rust媒体通话内逐项检验uuid_dump，不能只用伪造Call结构证明接口可用。"""
import argparse
import json
from pathlib import Path
import unittest
import urllib.parse
import xml.etree.ElementTree as ET

import e2e
import esl_e2e


class ChannelSnapshot(unittest.TestCase):
    # 复用隔离启动与媒体断言，不继承测试方法，避免同一旧用例被重复计数。
    setUp = esl_e2e.ESLIntegration.setUp
    stop_process = esl_e2e.ESLIntegration.stop_process
    sock = esl_e2e.ESLIntegration.sock
    free_tcp_port = esl_e2e.ESLIntegration.free_tcp_port
    get = esl_e2e.ESLIntegration.get
    invite = esl_e2e.ESLIntegration.invite
    establish = esl_e2e.ESLIntegration.establish
    hangup = esl_e2e.ESLIntegration.hangup
    wait_active = esl_e2e.ESLIntegration.wait_active
    check_media = esl_e2e.ESLIntegration.check_media

    def test_snapshot_formats_reflect_live_leg_variables_and_media(self):
        """快照读取、UTF8与百分号格式及错误参数都不能改变通话或另一腿。"""
        original, outgoing, accepted = self.establish()
        channels = self.esl.wait_events('CHANNEL_CREATE')
        self.esl.wait_events('CHANNEL_ANSWER')
        a, b = [next(event['Unique-ID'] for event in channels if event['Call-Direction'] == direction) for direction in ('inbound', 'outbound')]
        value = '中文 标签+&<>'
        self.assertEqual(self.esl.api('uuid_setvar ' + a + ' customer ' + value), '+OK\n')
        self.assertEqual(self.esl.api('uuid_setvar ' + a + ' 客户 张三'), '+OK\n')
        a_data = json.loads(self.esl.api('uuid_dump ' + a + ' json'))
        b_data = json.loads(self.esl.api('uuid_dump ' + b + ' JSON'))
        for identifier, snapshot in [(a, a_data), (b, b_data)]:
            self.assertEqual(snapshot['Event-Name'], 'CHANNEL_DATA')
            self.assertEqual(snapshot['Unique-ID'], identifier)
            self.assertEqual(snapshot['variable_uuid'], identifier)
            self.assertEqual(snapshot['Answer-State'], 'answered')
            self.assertNotIn('_body', snapshot)
        self.assertEqual(a_data['Other-Leg-Unique-ID'], b)
        self.assertEqual(b_data['Other-Leg-Unique-ID'], a)
        self.assertEqual(a_data['variable_sip_call_id'], e2e.parse(original)[1]['call-id'])
        self.assertEqual(b_data['variable_sip_call_id'], e2e.parse(outgoing)[1]['call-id'])
        self.assertEqual(a_data['variable_customer'], value)
        self.assertEqual(a_data['variable_客户'], '张三')
        self.assertNotIn('variable_customer', b_data)
        for format in ('', ' txt', ' plain', ' unknown'):
            raw = self.esl.api('uuid_dump ' + a + format)
            self.assertTrue(raw.endswith('\n\n'))
            fields = dict(line.split(': ', 1) for line in raw.splitlines() if line)
            if format == ' plain':
                self.assertTrue(fields['variable_customer'].startswith('[') and fields['variable_customer'].endswith(']'))
            observed = fields['variable_customer'][1:-1] if format == ' plain' else urllib.parse.unquote(fields['variable_customer'])
            self.assertEqual(observed, value)
            chinese = fields['variable_客户'][1:-1] if format == ' plain' else urllib.parse.unquote(fields['variable_客户'])
            self.assertEqual(chinese, '张三')
        for format in ('xml', 'XML'):
            document = ET.fromstring(self.esl.api('uuid_dump ' + a + ' ' + format))
            self.assertEqual(document.findtext('headers/Unique-ID'), a)
            self.assertEqual(urllib.parse.unquote(document.findtext('headers/variable_customer')), value)
            self.assertEqual(urllib.parse.unquote(document.findtext('headers/variable_客户')), '张三')
        self.assertTrue(self.esl.api('uuid_dump ' + a + ' json extra').startswith('-ERR'))
        self.assertEqual(self.esl.api('uuid_exists ' + a), 'true')
        self.check_media(outgoing, accepted)
        self.hangup(accepted)

    def test_snapshot_deleted_channel_errors_keep_next_call_usable(self):
        """挂机后所有格式都不能读到旧变量；再次呼叫有独立身份和可用双向音频。"""
        self.assertEqual(self.esl.api('uuid_dump'), '-USAGE: <uuid> [format]\n')
        self.assertEqual(self.esl.api('uuid_dump missing json'), '-ERR No such channel!\n')
        _, outgoing, accepted = self.establish()
        channels = self.esl.wait_events('CHANNEL_CREATE')
        identifiers = [event['Unique-ID'] for event in channels]
        self.check_media(outgoing, accepted)
        self.hangup(accepted)
        self.esl.wait_events('CHANNEL_HANGUP')
        for identifier in identifiers:
            for format in ('txt', 'plain', 'json', 'xml'):
                self.assertEqual(self.esl.api('uuid_dump ' + identifier + ' ' + format), '-ERR No such channel!\n')
        _, outgoing2, accepted2 = self.establish()
        channels = self.esl.wait_events('CHANNEL_CREATE', count=4)
        new_ids = [event['Unique-ID'] for event in channels if event['Unique-ID'] not in identifiers]
        self.assertEqual(len(new_ids), 2)
        for identifier in new_ids:
            self.assertEqual(json.loads(self.esl.api('uuid_dump ' + identifier + ' json'))['Unique-ID'], identifier)
        self.check_media(outgoing2, accepted2)
        self.hangup(accepted2)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--control', type=Path, default=e2e.CONTROL)
    parser.add_argument('--media', type=Path, default=e2e.MEDIA)
    parser.add_argument('--connected', action='store_true')
    args, remaining = parser.parse_known_args()
    e2e.CONTROL, e2e.MEDIA, e2e.CONNECTED = args.control.resolve(), args.media.resolve(), args.connected
    unittest.main(argv=[__file__] + remaining, verbosity=2)
