import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer as AuthHTTPServer

# Both paths are needed: `scripts/` for the importable tooling, and this folder
# for mock_provider, which discovery adds implicitly but `unittest tests.x` does
# not -- so a single-module run used to fail with ModuleNotFoundError.
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
sys.path.insert(0, str(Path(__file__).resolve().parent))
from gateway import API, validate_backup
from acceptance import billing_rates, expected_quota, stream
from mock_provider import Handler, ThreadingHTTPServer


class ExpectedQuotaTests(unittest.TestCase):
    """The acceptance billing checks must follow the price that is actually live."""

    def options(self, expression):
        return {"QuotaPerUnit": 500000,
                "billing_setting.billing_expr": json.dumps({"sample-model": expression})}

    def test_internal_test_price_is_unchanged(self):
        rates = billing_rates(self.options("v1:p * 1 + c * 2"), "sample-model")
        self.assertEqual(rates, (1, 2))
        self.assertEqual(expected_quota({"prompt_tokens": 20, "completion_tokens": 10}, rates, 500000), 20)

    def test_retail_price_is_derived_from_the_live_expression(self):
        rates = billing_rates(self.options("v1:p * 0.0376 + c * 0.225"), "sample-model")
        self.assertEqual(expected_quota({"prompt_tokens": 20, "completion_tokens": 10}, rates, 500000), 2)

    def test_half_quota_rounds_up(self):
        rates = billing_rates(self.options("v1:p * 1 + c * 2"), "sample-model")
        self.assertEqual(expected_quota({"prompt_tokens": 1, "completion_tokens": 0}, rates, 500000), 1)

    def test_missing_expression_is_refused(self):
        with self.assertRaises(RuntimeError):
            billing_rates({"QuotaPerUnit": 500000, "billing_setting.billing_expr": "{}"}, "sample-model")

    def test_expression_with_extra_terms_is_refused(self):
        with self.assertRaises(RuntimeError):
            billing_rates(self.options("v1:p * 1 + c * 2 + cr * 0.1"), "sample-model")

    def test_per_request_price_shape_is_refused(self):
        with self.assertRaises(RuntimeError):
            billing_rates(self.options("v1:0.01"), "sample-model")

    def test_zero_or_absent_usage_yields_zero(self):
        rates = billing_rates(self.options("v1:p * 1 + c * 2"), "sample-model")
        self.assertEqual(expected_quota(None, rates, 500000), 0)
        self.assertEqual(expected_quota({}, rates, 500000), 0)


class BackupValidationTests(unittest.TestCase):
    def fixture(self, root):
        files = {}
        for name in ("database.dump", "data.tar", "bundle/.env", "bundle/docker-compose.yml"):
            p = root / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_bytes(b"fixture")
            files[name] = hashlib.sha256(b"fixture").hexdigest()
        (root / "manifest.json").write_text(json.dumps({"files": files}), encoding="utf-8")
        return files

    def test_valid_backup(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.fixture(root)
            self.assertEqual(validate_backup(root), root.resolve())

    def test_corrupt_database_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.fixture(root)
            (root / "database.dump").write_bytes(b"corruption")
            with self.assertRaises(RuntimeError):
                validate_backup(root)

    def test_incomplete_manifest_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            files = self.fixture(root)
            del files["database.dump"]
            (root / "manifest.json").write_text(json.dumps({"files": files}))
            with self.assertRaises(RuntimeError):
                validate_backup(root)

    def test_parent_traversal_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "backup"
            root.mkdir()
            files = self.fixture(root)
            (root.parent / "outside").write_bytes(b"fixture")
            files["../outside"] = hashlib.sha256(b"fixture").hexdigest()
            (root / "manifest.json").write_text(json.dumps({"files": files}))
            with self.assertRaises(RuntimeError):
                validate_backup(root)


class DashboardBearerTests(unittest.TestCase):
    def test_rc40_login_uses_returned_bearer_for_followup_requests(self):
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                return

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                self.rfile.read(length)
                payload = {"success": True, "data": {"access_token": "dashboard-jwt",
                           "user": {"id": 7}}}
                body = json.dumps(payload).encode()
                self.send_response(200); self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)

            def do_GET(self):
                Handler.authorization = self.headers.get("Authorization")
                Handler.legacy_user = self.headers.get("New-Api-User")
                body = b'{"success":true,"data":[]}'
                self.send_response(200); self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)

        server = AuthHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        try:
            api = API(f"http://127.0.0.1:{server.server_port}")
            api.login({"username": "operator", "password": "fixture-password"})
            api.ok("/api/option/")
            self.assertEqual(Handler.authorization, "Bearer dashboard-jwt")
            self.assertIsNone(Handler.legacy_user)
            self.assertEqual(api.user_id, 7)
        finally:
            server.shutdown(); server.server_close(); thread.join()

class StreamProbeTests(unittest.TestCase):
    def test_delayed_content_events_and_usage(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            api = API(f"http://127.0.0.1:{server.server_port}")
            result = stream(api, "synthetic-key", "upstream-a", "fixture")
            self.assertEqual(result["content"], "AI_GATEWAY_OK")
            self.assertTrue(result["done"])
            self.assertEqual(result["usage"]["prompt_tokens"], 20)
            self.assertEqual(len(result["content_event_seconds"]), 3)
            self.assertGreater(result["content_event_seconds"][-1] - result["content_event_seconds"][0], 0.5)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    unittest.main()
