import importlib.util
import json
import os
import stat
import tempfile
import unittest
from pathlib import Path

BASE = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location(
    'configure_support_notify', BASE / 'scripts/configure_support_notify.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ConfigureSupportNotifyTests(unittest.TestCase):
    def test_accepts_only_the_official_wecom_webhook_shape(self):
        good = 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc_def-1234567890'
        self.assertEqual(module.validate_wecom(good), good)
        bad = [
            'http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc_def-1234567890',
            'https://evil.example/cgi-bin/webhook/send?key=abc_def-1234567890',
            'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x',
            'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc_def-1234567890&next=x',
        ]
        for value in bad:
            with self.subTest(value=value), self.assertRaises(ValueError):
                module.validate_wecom(value)

    def test_secret_writer_uses_restricted_permissions(self):
        with tempfile.TemporaryDirectory() as folder:
            target = Path(folder) / 'secret.json'
            module.atomic_json(target, {'wecom': {'webhook': 'redacted'}}, 0o600)
            self.assertEqual(json.loads(target.read_text()),
                             {'wecom': {'webhook': 'redacted'}})
            if os.name != 'nt':
                self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o600)

    @unittest.skipIf(os.name == 'nt', 'POSIX ownership is verified on deployment')
    def test_config_remains_group_readable(self):
        with tempfile.TemporaryDirectory() as folder:
            target = Path(folder) / 'support.json'
            module.atomic_json(target, {'notify': {'channel': 'wecom'}}, 0o640)
            module.services_can_read_config(target)
            self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o640)


if __name__ == '__main__':
    unittest.main()
