#!/usr/bin/env python3
"""批量变量的真实SIP/ESL/Rust媒体验证，逐项读回，不以固定成功字符串代替副作用。"""
import argparse
from pathlib import Path
import unittest

import e2e
import esl_applications_e2e as apps


class VariableIntegration(apps.ApplicationFixture):
    def test_batch_variables_preserve_audio_and_leg_isolation(self):
        a, b, outgoing, accepted = self.established_channel()
        self.assertEqual(self.esl.api(f"uuid_setvar_multi {a} customer=中文客户;token=x=y;repeat=old;repeat=new"), "+OK\n")
        for name, value in {"customer": "中文客户", "token": "x=y", "repeat": "new"}.items():
            self.assertEqual(self.esl.api(f"uuid_getvar {a} {name}"), value)
            self.assertEqual(self.esl.api(f"uuid_getvar {b} {name}"), "_undef_")
        self.check_media(outgoing, accepted)
        self.assertEqual(self.esl.api(f"uuid_setvar_multi {a} customer=;one=value;legacy;quoted=' x;y ';escaped=x\\;y"), "+OK\n")
        for name, value in {"customer": "_undef_", "legacy": "_undef_", "quoted": " x;y ", "escaped": "x;y"}.items():
            self.assertEqual(self.esl.api(f"uuid_getvar {a} {name}"), value)
        self.hangup(accepted)
        self.wait_active(0)
        self.assertEqual(self.esl.api(f"uuid_exists {a}"), "false")
        self.assertEqual(self.esl.api(f"uuid_exists {b}"), "false")

    def test_batch_partial_errors_limits_and_subsequent_call(self):
        a, _, outgoing, accepted = self.established_channel()
        self.assertEqual(self.esl.api(f"uuid_setvar_multi {a} =bad;accepted=yes"), "-ERR No variable specified\n+OK\n")
        self.assertEqual(self.esl.api(f"uuid_getvar {a} accepted"), "yes")
        self.assertTrue(self.esl.api(f"uuid_setvar_multi {a} " + "sentinel=bad;" * 65).startswith("-ERR"))
        self.assertEqual(self.esl.api(f"uuid_getvar {a} sentinel"), "_undef_")
        self.assertNotIn("+OK", self.esl.api(f"uuid_setvar_multi {a} uuid=forged"))
        self.assertEqual(self.esl.api(f"uuid_getvar {a} uuid"), a)
        self.check_media(outgoing, accepted)
        self.hangup(accepted)
        self.wait_active(0)
        self.esl.wait_events("CHANNEL_HANGUP")
        self.esl.events.clear()  # 第二通只能关联新CREATE/ANSWER，不能重用第一通缓存事件。
        a2, _, outgoing2, accepted2 = self.established_channel()
        self.assertNotEqual(a, a2)
        self.assertEqual(self.esl.api(f"uuid_getvar {a2} accepted"), "_undef_")
        self.check_media(outgoing2, accepted2)
        self.hangup(accepted2)
        self.wait_active(0)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--control", type=Path, default=e2e.CONTROL)
    parser.add_argument("--media", type=Path, default=e2e.MEDIA)
    parser.add_argument("--connected", action="store_true")
    args, remaining = parser.parse_known_args()
    e2e.CONTROL, e2e.MEDIA, e2e.CONNECTED = args.control.resolve(), args.media.resolve(), args.connected
    unittest.main(argv=[__file__] + remaining, verbosity=2)
