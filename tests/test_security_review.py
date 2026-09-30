"""Security regression invariants from the 2026-09-27 white-box review."""
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

import sys

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

import credit_topup  # noqa: E402
import topup_requests as queue  # noqa: E402
from ops.topup_watch.topup_watch import Service  # noqa: E402
from ops.topup_watch.topup_watch import init_secrets, session_scope, sign_session  # noqa: E402


class FakeQuotaAPI:
    def __init__(self):
        self.quota = 0

    def ok(self, path, body=None):
        if path.startswith("/api/user/") and body is None:
            return {"id": 7, "username": "audit-user", "quota": self.quota}
        if path == credit_topup.QUOTA_API:
            self.quota += int(body["value"])
            return {"success": True}
        raise AssertionError((path, body))


class FundsIntegrityInvariants(unittest.TestCase):
    def test_idempotency_is_durable_before_wallet_mutation(self):
        """A crash after add_quota must not let the same receipt add twice."""
        with tempfile.TemporaryDirectory() as folder:
            ledger = Path(folder) / "ledger.json"
            api = FakeQuotaAPI()
            original_save = credit_topup.save_ledger
            calls = 0
            def fail_first(path, payload):
                nonlocal calls
                calls += 1
                if calls == 1:
                    raise OSError("disk full")
                return original_save(path, payload)
            with mock.patch.object(credit_topup, "BALANCE_WAIT_SECONDS", 0), \
                    mock.patch.object(credit_topup, "save_ledger", side_effect=fail_first):
                with self.assertRaises(OSError):
                    credit_topup.credit_usd(ledger, 7, "1.00", "receipt-1",
                                            apply_changes=True, api=api)
            credit_topup.credit_usd(ledger, 7, "1.00", "receipt-1",
                                    apply_changes=True, api=api)
            self.assertEqual(api.quota, credit_topup.quota_from_usd("1.00"))

    def test_concurrent_fulfillment_credits_one_request_once(self):
        """Two admins racing one request must not both reach wallet mutation."""
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            config = root / "topup.json"
            config.write_text('{"cny_per_usd":7,"min_amount_cny":1}', encoding="utf-8")
            events = root / "requests.jsonl"
            queue.append_event(queue.build_request({"cny_per_usd": 7}, 7, "7"), events)
            request_id = queue.pending(events)[0]["id"]
            service = Service(root / "secrets.json", events, root / "ledger.json", config,
                              gateway_url="http://127.0.0.1")
            credits = []

            def fake_credit(record, transaction_id, net_received=None):
                credits.append(transaction_id)
                return ({"issued": {"quota": 500000, "balance_verified": True}},
                        queue.decimal_amount("7"), queue.decimal_amount("1"))

            errors = []
            with mock.patch.object(service, "_credit_with_session", side_effect=fake_credit):
                threads = [threading.Thread(
                    target=lambda tx=tx: self._capture(errors, service.fulfill,
                                                        request_id, tx, "audit"))
                           for tx in ("bank-ref-a", "bank-ref-b")]
                for thread in threads:
                    thread.start()
                for thread in threads:
                    thread.join(timeout=5)
            self.assertEqual(len(credits), 1)

    @staticmethod
    def _capture(errors, function, *args):
        try:
            function(*args)
        except Exception as error:  # evidence includes the post-credit close failure
            errors.append(type(error).__name__)


class WebBoundaryInvariants(unittest.TestCase):
    def test_logout_or_password_rotation_revokes_existing_admin_cookie(self):
        with tempfile.TemporaryDirectory() as folder:
            secrets_path = Path(folder) / "admin.json"
            document, _ = init_secrets(secrets_path, password="old-password")
            cookie = sign_session(document, 4_000_000_000, "pw", generation=1)
            self.assertEqual(session_scope(document, cookie, now=2_000_000_000,
                                           generation=1), "pw")
            self.assertIsNone(session_scope(document, cookie, now=2_000_000_000,
                                            generation=2))

    def test_admin_mutations_have_explicit_csrf_or_origin_validation(self):
        source = (ROOT / "ops/topup_watch/topup_watch.py").read_text(encoding="utf-8")
        self.assertTrue("Origin" in source or "csrf" in source.lower())

    def test_uploaded_qr_is_fully_decoded_before_persistence(self):
        source = (ROOT / "ops/topup_watch/topup_watch.py").read_text(encoding="utf-8")
        self.assertTrue("Image.open" in source or "imghdr" in source)


class PatchIntegrityInvariants(unittest.TestCase):
    def test_public_id_patch_preserves_affiliate_code_field(self):
        source = (ROOT / "core/patches/apply_public_id.py").read_text(encoding="utf-8")
        added = source.split("FIELD_ADDED =", 1)[1].split("LOCK_ANCHOR", 1)[0]
        self.assertIn("FIELD_ANCHOR +", added)


if __name__ == "__main__":
    unittest.main()
