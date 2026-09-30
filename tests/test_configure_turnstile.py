import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
import configure_turnstile  # noqa: E402


class TurnstileKeyFileTests(unittest.TestCase):
    def test_reads_labelled_export(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'keys.txt'
            path.write_text('Site key：0xsite-value-1234567890\nSecret key：0xsecret-value-1234567890\n',
                            encoding='utf-8')
            site, secret = configure_turnstile.read_keys(path)
            self.assertEqual(site, '0xsite-value-1234567890')
            self.assertEqual(secret, '0xsecret-value-1234567890')

    def test_reads_json_export(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'keys.json'
            path.write_text('{"site_key":"site","secret_key":"secret"}', encoding='utf-8')
            self.assertEqual(configure_turnstile.read_keys(path), ('site', 'secret'))


if __name__ == '__main__':
    unittest.main()
