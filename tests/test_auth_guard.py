import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'ops'))
from auth_guard import accepted


class EmailOnlyTests(unittest.TestCase):
    def test_accepts_email_identity(self):
        self.assertTrue(accepted(b'{"username":"person@example.com","password":"x"}'))

    def test_rejects_legacy_username_and_malformed_json(self):
        self.assertFalse(accepted(b'{"username":"legacy-user","password":"x"}'))
        self.assertFalse(accepted(b'not-json'))


if __name__ == '__main__':
    unittest.main()
