"""Verified account identities share one atomic sliding-window quota."""
import concurrent.futures
from pathlib import Path
import sys
import threading
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
for folder in ('scripts', 'ops', 'ops/support'):
    sys.path.insert(0, str(ROOT / folder))
import support_service as support


class IdentityLimitTests(unittest.TestCase):
    def setUp(self):
        self.now = 0
        self.service = support.Service.__new__(support.Service)
        self.service.gateway_url = 'https://gateway.test'
        self.service.identify_gate = support.queue.SlidingWindow(
            *support.IDENTIFY_UID_LIMIT, clock=lambda: self.now)
        self.service.invalid_identify_gate = support.queue.SlidingWindow(
            *support.INVALID_IDENTIFY_IP_LIMIT, clock=lambda: self.now)
        self.service.identify_lock = threading.Lock()
        self.stub = patch.object(support.user_identity, 'user_for',
                                 side_effect=lambda _, token:
                                 None if token == 'invalid' else
                                 {'id': 7 if token in ('a', 'rotated-a') else 8})
        self.stub.start()
        self.addCleanup(self.stub.stop)

    def test_500_allowed_then_rejected_and_window_expires(self):
        for _ in range(500):
            self.assertEqual(self.service.identify('a', 'ip-one')['id'], 7)
        with self.assertRaisesRegex(PermissionError, '身份校验过于频繁'):
            self.service.identify('rotated-a', 'ip-two')
        self.assertEqual(self.service.identify('b', 'ip-one')['id'], 8)
        self.now = 601
        self.assertEqual(self.service.identify('a', 'ip-two')['id'], 7)

    def test_concurrent_devices_cannot_exceed_500(self):
        def attempt(i):
            try:
                self.service.identify('a', 'ip-' + str(i % 10))
                return True
            except PermissionError:
                return False
        with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
            self.assertEqual(sum(pool.map(attempt, range(550))), 500)

    def test_invalid_credentials_do_not_spend_verified_account_quota(self):
        for _ in range(40):
            self.assertIsNone(self.service.identify('invalid', 'shared-ip'))
        with self.assertRaises(PermissionError):
            self.service.identify('invalid', 'shared-ip')
        self.assertEqual(self.service.identify('a', 'shared-ip')['id'], 7)


if __name__ == '__main__':
    unittest.main()
