import hashlib
import importlib.util
import json
import secrets
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
import zipfile
from pathlib import Path
from unittest.mock import patch
from http.server import ThreadingHTTPServer

from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives import serialization

from ops.archive.crypto import (b64, canonical, decrypt, encrypt, generate_key, redact,
                                unb64, unwrap_private)
from ops.archive.store import Store
from ops.archive.service import Application, handler_for
from ops.archive.cli import export_package, restore_package, snapshot, materialize_snapshot, materialize_image, readable_export, CollectorClient, EncryptedStream


class ArchiveTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.password = "synthetic-test-passphrase-only"
        cls.key, cls.recovery = generate_key(cls.password)
        cls.private = unwrap_private(cls.key["protected"], passphrase=cls.password)
        cls.public = cls.private.public_key()

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.store = Store(self.root / "local", min_free=0)
        self.store.control({"kind": "key", "id": secrets.token_hex(16), "key": self.key})
        self.aid = secrets.token_hex(16)
        self.created = time.time()
        self.seq, self.prev = 0, ""

    def tearDown(self):
        self.temp.cleanup()

    def event(self, kind="request", clear=b"synthetic private prompt", **extra):
        meta = {"archive_id": self.aid, "user_id": 42, "created": self.created, "model": "synthetic-model",
                "project_id": "", "conversation_id": "", "seq": self.seq, "prev_hash": self.prev,
                "kind": kind, **extra}
        raw = canonical(encrypt(self.public, meta, clear))
        self.prev = self.store.ingest(raw)
        self.seq += 1
        return raw

    def completed(self):
        self.event(clear=canonical({"input_format": "json", "ip": "192.0.2.1"}))
        self.event("input", canonical({"messages": [{"role": "user", "content": "中文合成消息"}]}))
        self.event("output", b'data: {"choices":[{"delta":{"content":"hello"}}]}\n\n')
        self.event("final", canonical({"http_status": 200}), status="completed")

    def test_changes_track_late_completion_and_group_without_duplicates(self):
        raw = self.event()
        baseline = self.store.changes(tail=True)["cursor"]
        self.store.ingest(raw)
        self.assertEqual(self.store.changes(tail=True)["cursor"], baseline)
        self.event("output", b"reply")
        self.event("final", status="completed")
        update = self.store.changes(baseline)
        self.assertEqual(len(update["records"]), 1)
        self.assertEqual(update["records"][0]["status"], "completed")
        self.store.control({"kind":"group","id":secrets.token_hex(16),"archive_id":self.aid,"project_id":"project","conversation_id":"thread"})
        grouped = self.store.changes(update["cursor"])
        self.assertEqual(grouped["records"][0]["conversation_id"], "thread")
        self.assertFalse(self.store.records({"group_project":""}))
        self.assertEqual(self.store.directory()["groups"][0]["count"], 1)

    def test_category_and_newest_cursor(self):
        self.completed()
        first = self.aid
        self.aid, self.seq, self.prev = secrets.token_hex(16), 0, ""
        self.created += 1
        self.event(user_id=0, model="__snapshot_bundle__")
        self.assertEqual(self.store.records({"category":"chat"})[0]["id"], first)
        self.assertEqual(len(self.store.records({"category":"snapshot"})), 1)
        newest = self.store.records({"sort":"newest"}, limit=1)[0]
        older = self.store.records({"sort":"newest"}, after=(newest["created"], newest["id"]))
        self.assertEqual([r["id"] for r in older], [first])

    def test_embedded_assets_and_exact_empty_group_http(self):
        import re
        self.completed()
        app = Application(self.store, "manager", origin="https://admin.synthetic.invalid", auth=lambda _: {"id":1,"role":100})
        origin = self.start_service(app)
        with urllib.request.urlopen(origin+"/") as response:
            self.assertIn("frame-ancestors 'none'", response.headers["Content-Security-Policy"])
            html = response.read().decode()
        for asset in re.findall(r'(?:src|href)="(/assets/[^"]+)"', html):
            with urllib.request.urlopen(origin+asset) as response:
                self.assertEqual(response.status, 200)
        with urllib.request.urlopen(origin+"/embed") as response:
            self.assertIn("frame-ancestors https://omniapi.biz", response.headers["Content-Security-Policy"])
            self.assertNotIn("https://evil.invalid", response.headers["Content-Security-Policy"])
        now = time.monotonic()
        app.sessions["synthetic"] = {"uid":1,"start":now,"last":now}
        _, rows = self.call(origin,"/api/records?group_project=&group_conversation=",unlock="synthetic")
        self.assertEqual(len(rows), 1)
        self.store.control({"kind":"group","id":secrets.token_hex(16),"archive_id":self.aid,"project_id":"project","conversation_id":"thread"})
        _, rows = self.call(origin,"/api/records?group_project=&group_conversation=",unlock="synthetic")
        self.assertFalse(rows)
        request = urllib.request.Request(origin+"/api/challenge", data=b"{}",headers={"Origin":"https://evil.invalid"})
        with self.assertRaises(urllib.error.HTTPError) as denied:
            urllib.request.urlopen(request)
        self.assertEqual(denied.exception.code, 403)

    def test_verification_records_are_hidden_from_chat_directory(self):
        self.event(project_id="deployment-live-verification")
        self.assertFalse(self.store.records({"category":"chat"}))
        self.assertFalse(self.store.directory()["groups"])
        self.assertEqual(len(self.store.records({"category":"system"})),1)

    def test_identity_outage_is_retryable_without_authorizing_access(self):
        app = Application(self.store,"manager",gateway="https://gateway.synthetic.invalid")
        with patch("ops.archive.service.secure_open",side_effect=OSError("synthetic outage")):
            with self.assertRaises(OSError):
                app.gateway_identity("synthetic")
        denied = urllib.error.HTTPError("https://gateway.synthetic.invalid",403,"Denied",{},None)
        with patch("ops.archive.service.secure_open",side_effect=denied):
            self.assertIsNone(app.gateway_identity("synthetic"))

    def test_read_poll_does_not_extend_unlock(self):
        app = Application(self.store, "manager")
        user = {"id":1,"role":100}
        now = time.monotonic()
        app.sessions["synthetic"] = {"uid":1,"start":now,"last":now-590}
        last = app.sessions["synthetic"]["last"]
        app.unlocked(user, "synthetic", activity=False)
        self.assertEqual(app.sessions["synthetic"]["last"], last)
        app.unlocked(user, "synthetic", activity=True)
        self.assertGreater(app.sessions["synthetic"]["last"], last)
        app.sessions["synthetic"]["start"] -= 1801
        with self.assertRaises(PermissionError):
            app.unlocked(user, "synthetic", activity=True)

    def test_recovery_and_wrong_password(self):
        self.assertEqual(unwrap_private(self.key["protected"], recovery_key=self.recovery).private_numbers(), self.private.private_numbers())
        with self.assertRaises(InvalidTag):
            unwrap_private(self.key["protected"], passphrase="wrong-password")

    def test_gcm_and_metadata_tampering(self):
        event = encrypt(self.public, {"owner": 42}, "中文内容".encode())
        self.assertEqual(decrypt(self.private, event).decode(), "中文内容")
        changed = json.loads(canonical(event)); changed["meta"]["owner"] = 1
        with self.assertRaises(ValueError):
            decrypt(self.private, changed)
        changed = json.loads(canonical(event)); data = bytearray(unb64(changed["ciphertext"])); data[0] ^= 1; changed["ciphertext"] = b64(data)
        with self.assertRaises(InvalidTag):
            decrypt(self.private, changed)

    def test_redaction(self):
        result = redact({"api_key": "secret", "messages": [{"password": "secret", "content": "actual conversation"}]})
        self.assertEqual(result["api_key"], "[REDACTED]")
        self.assertEqual(result["messages"][0]["content"], "actual conversation")

    def test_idempotency_conflict_and_chain(self):
        raw = self.event()
        self.assertEqual(self.store.ingest(raw), self.prev)
        event = json.loads(raw)
        with self.assertRaises(ValueError):
            self.store.ingest(canonical(encrypt(self.public, event["meta"], b"conflict")))
        event["meta"]["seq"] = 2
        with self.assertRaises(ValueError):
            self.store.ingest(canonical(encrypt(self.public, event["meta"], b"out of order")))

    def test_no_plaintext_on_disk_and_reopen(self):
        self.completed()
        for file in (self.root / "local").glob("archive.sqlite3*"):
            self.assertNotIn("中文合成消息".encode(), file.read_bytes())
            self.assertNotIn(b"192.0.2.1", file.read_bytes())
        reopened = Store(self.root / "local", min_free=0)
        self.assertEqual(len(reopened.events(self.aid)), 4)
        self.assertEqual(reopened.records({"user_id": 42})[0]["status"], "completed")

    def test_user_filter_and_group_history(self):
        self.completed()
        self.assertFalse(self.store.records({"user_id": 43}))
        for project in ("first", "second"):
            self.store.control({"kind": "group", "id": secrets.token_hex(16), "archive_id": self.aid, "project_id": project, "conversation_id": "conversation-a"})
        self.assertEqual(self.store.records({"project_id": "second"})[0]["id"], self.aid)
        self.assertEqual(sum(json.loads(unb64(c["raw"]))["kind"] == "group" for c in self.store.controls()), 2)

    def test_time_filters_and_pagination(self):
        self.completed()
        self.assertEqual(len(self.store.records({"from": self.created, "to": self.created + 1})), 1)
        self.assertFalse(self.store.records({"to": self.created}))
        self.assertFalse(self.store.records({}, after=(self.created, self.aid)))

    def test_upstream_model_filter(self):
        self.event()
        self.event("final", status="completed", upstream_model="resolved-model")
        self.assertEqual(self.store.records({"model": "resolved-model"})[0]["id"], self.aid)
        self.assertEqual(self.store.records({"model": "synthetic-model"})[0]["id"], self.aid)

    def test_plaintext_local_export_is_readable_and_escaped(self):
        self.event(clear=canonical({"input_format": "json"}))
        self.event("input", canonical({"messages": [{"role": "user", "content": "中文 <img src=x onerror=alert(1)>"}]}))
        self.event("output", b"synthetic response")
        self.event("final", canonical({"http_status": 200}), status="completed")
        output = self.root / "readable.zip"
        readable_export(self.store, output, {"user_id": 42}, recovery_key=self.recovery)
        with zipfile.ZipFile(output) as package:
            page = package.read("index.html").decode()
            self.assertIn("&lt;img", page)
            self.assertNotIn("<img src=x", page)
            self.assertIn("中文", page)
            self.assertEqual(package.read("users/42/projects/unclassified/conversations/unclassified/requests/" + self.aid + "/response.bin"), b"synthetic response")

    def test_rotated_keys_restore_all_versions_and_disable_grants(self):
        self.completed()
        key2, recovery2 = generate_key("synthetic-second-independent-password")
        self.store.control({"kind": "key", "id": secrets.token_hex(16), "key": key2})
        self.store.control({"kind": "grant", "id": secrets.token_hex(16), "user_id": 2, "allowed": True})
        self.aid, self.seq, self.prev = secrets.token_hex(16), 0, ""
        meta = {"archive_id": self.aid, "user_id": 43, "created": self.created, "model": "rotated",
                "project_id": "", "conversation_id": "", "seq": 0, "prev_hash": "", "kind": "request"}
        public2 = serialization.load_pem_public_key(key2["public_pem"].encode())
        self.store.ingest(canonical(encrypt(public2, meta, b"new key content")))
        package = self.root / "rotated.zip"; export_package(self.store, package)
        dest = self.root / "rotated-restored"
        restore_package([package], dest, recovery_key=self.recovery,
                        credentials={key2["key_id"]: {"recovery_key": recovery2}})
        restored = Store(dest, replica=True, min_free=0)
        self.assertEqual(restored.metrics()["records"], 2)
        self.assertFalse(restored.allowed(2))

    def test_retention_only_after_ack_and_replica_forever(self):
        self.created = time.time() - 91 * 86400
        self.completed()
        replica = Store(self.root / "replica", replica=True, min_free=0)
        self.assertEqual(self.store.prune(), 0)
        for oid, item in self.store.pending():
            self.assertEqual(replica.receive(item), item["hash"])
            replica.receive(item)
            self.store.acknowledge(oid, item)
        self.assertEqual(self.store.prune(), 1)
        self.assertEqual(replica.prune(time.time() + 1000 * 86400), 0)
        self.assertEqual(len(replica.events(self.aid)), 4)

    def test_capacity_fails_closed(self):
        self.store.min_free = 1 << 80
        with self.assertRaises(OSError):
            self.event()
        self.assertEqual(self.store.metrics()["records"], 0)

    def test_restore_roundtrip_and_no_live_overwrite(self):
        self.completed()
        package = self.root / "backup.zip"
        export_package(self.store, package)
        dest = self.root / "restored"
        restore_package([package], dest, passphrase=self.password)
        restored = Store(dest, replica=True, min_free=0)
        self.assertEqual(restored.events(self.aid), self.store.events(self.aid))
        with self.assertRaises(ValueError):
            restore_package([package], dest, passphrase=self.password)

    def test_corrupt_restore_not_published(self):
        self.completed()
        good = self.root / "good.zip"; export_package(self.store, good)
        corrupt = self.root / "bad.zip"
        with zipfile.ZipFile(good) as original, zipfile.ZipFile(corrupt, "w") as changed:
            for name in original.namelist():
                value = original.read(name)
                if name == "manifest.json":
                    manifest = json.loads(value); manifest["files"]["events.jsonl"] = "0" * 64; value = canonical(manifest)
                changed.writestr(name, value)
        dest = self.root / "failed-restore"
        with self.assertRaises(ValueError):
            restore_package([corrupt], dest, passphrase=self.password)
        self.assertFalse(dest.exists())

    def test_encrypted_snapshot_and_offline_materialization(self):
        source = self.root / "source"; source.mkdir()
        (source / ".env").write_bytes(b"SYNTHETIC_CONFIG=secret-for-recovery\n")
        (source / "docker-compose.yml").write_text("services: {}\n")
        snapshot(self.store, source)
        record = self.store.records({"model": "__snapshot_bundle__"})[0]
        output = self.root / "clear-snapshot"
        with patch("getpass.getpass", return_value=self.recovery):
            materialize_snapshot(self.store, record["project_id"], self.store.keys(), output, recovery=True)
        import tarfile
        with tarfile.open(output / "bundle.tar") as bundle:
            self.assertEqual(bundle.extractfile("bundle/.env").read(), b"SYNTHETIC_CONFIG=secret-for-recovery\n")

    def start_service(self, app):
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler_for(app))
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        self.addCleanup(server.server_close); self.addCleanup(server.shutdown)
        return "http://127.0.0.1:" + str(server.server_port)

    def call(self, origin, path, token="root", body=None, unlock=None):
        headers = {"Authorization": "Bearer " + token, "Origin": "https://admin.synthetic.invalid"}
        if unlock: headers["X-Archive-Unlock"] = unlock
        request = urllib.request.Request(origin + path, data=canonical(body) if body is not None else None, headers=headers)
        with urllib.request.urlopen(request, timeout=5) as response:
            return response.status, json.load(response)

    def test_permissions_unlock_proof_revocation_and_expiry(self):
        self.completed()
        users = {"root": {"id": 1, "role": 100}, "admin": {"id": 2, "role": 10}, "user": {"id": 42, "role": 1}}
        app = Application(self.store, "manager", origin="https://admin.synthetic.invalid", auth=users.get)
        origin = self.start_service(app)
        for token in ("user", "admin"):
            with self.assertRaises(urllib.error.HTTPError) as e:
                self.call(origin, "/api/keys", token)
            self.assertEqual(e.exception.code, 403)
        with self.assertRaises(urllib.error.HTTPError):
            self.call(origin, "/api/records")
        self.call(origin, "/api/grant", body={"user_id": 2, "allowed": True})
        _, challenge = self.call(origin, "/api/challenge", "admin", {"key_id": self.key["key_id"]})
        proof = decrypt(self.private, challenge["envelope"]).decode()
        _, unlocked = self.call(origin, "/api/unlock", "admin", {"challenge": challenge["challenge"], "proof": proof})
        session = unlocked["unlock"]
        _, records = self.call(origin, "/api/records?user_id=42", "admin", unlock=session)
        self.assertEqual(len(records), 1)
        app.sessions[session]["last"] -= 601
        with self.assertRaises(urllib.error.HTTPError):
            self.call(origin, "/api/records", "admin", unlock=session)
        self.call(origin, "/api/grant", body={"user_id": 2, "allowed": False})
        with self.assertRaises(urllib.error.HTTPError):
            self.call(origin, "/api/keys", "admin")

    def test_replicator_replays_after_outage_and_checksum(self):
        self.completed()
        replica = Store(self.root / "remote", replica=True, min_free=0)
        token = "synthetic-replication-token-32-chars"
        remote = Application(replica, "replica", ingest_token=token)
        origin = self.start_service(remote)
        app = Application(self.store, "collector", sync_url=origin, sync_token=token)
        self.assertGreater(app.sync_once(), 0)
        self.assertEqual(self.store.metrics()["queued_items"], 0)
        self.assertEqual(replica.events(self.aid), self.store.events(self.aid))
        item = {"type": "event", "raw": b64(b'{}'), "hash": "bad"}
        with self.assertRaises(ValueError): replica.receive(item)

    def test_replication_credential_cannot_grant_archive_access(self):
        replica = Store(self.root / "remote", replica=True, min_free=0)
        token = "synthetic-replication-token-32-chars"
        origin = self.start_service(Application(replica, "replica", ingest_token=token))
        raw = canonical({"kind": "grant", "id": secrets.token_hex(16), "user_id": 2, "allowed": True})
        with self.assertRaises(urllib.error.HTTPError) as error:
            self.call(origin, "/internal/replicate", token, {"type": "control", "raw": b64(raw), "hash": hashlib.sha256(raw).hexdigest()})
        self.assertEqual(error.exception.code, 403)
        self.assertFalse(replica.allowed(2))

    def test_snapshot_rpc_excludes_recursive_snapshot_payloads(self):
        self.completed()
        token = "synthetic-local-collector-token-32chars"
        origin = self.start_service(Application(self.store, "collector", ingest_token=token))
        client = CollectorClient(origin, token)
        source = self.root / "gateway"; source.mkdir(); (source / ".env").write_bytes(b"SYNTHETIC=1\n")
        snapshot(client, source)
        snapshot(client, source)
        index = self.root / "index.sqlite3"
        client.snapshot(index)
        import sqlite3
        from contextlib import closing
        with closing(sqlite3.connect(index)) as db:
            self.assertEqual(db.execute("SELECT COUNT(*) FROM events WHERE archive_id IN (SELECT id FROM records WHERE user_id=0)").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM events WHERE archive_id=?", (self.aid,)).fetchone()[0], 4)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM outbox").fetchone()[0], 0)

    def test_restore_wrong_key_not_published(self):
        self.completed()
        package = self.root / "archive.zip"; export_package(self.store, package)
        destination = self.root / "invalid-key"
        with self.assertRaises(InvalidTag):
            restore_package([package], destination, passphrase="wrong-passphrase")
        self.assertFalse(destination.exists())

    def test_custom_image_reference_recovers_and_rejects_bad_digest(self):
        image_id = "sha256:" + "a" * 64
        project = "image-" + "a" * 64
        stream = EncryptedStream(self.store, self.public, "__snapshot_image__", project,
                                 {"format": "docker-save", "image_id": image_id})
        payload = b"synthetic-image-tar" * 10000
        stream.write(payload); stream.finish()
        reference = {"image_id": image_id, "archive_id": stream.base["archive_id"], "project_id": project,
                     "sha256": hashlib.sha256(payload).hexdigest(), "bytes": len(payload)}
        output = self.root / "image.tar"
        materialize_image(self.store, reference, output, recovery_key=self.recovery)
        self.assertEqual(output.read_bytes(), payload)
        bad_output = self.root / "invalid-image.tar"
        with self.assertRaises(ValueError):
            materialize_image(self.store, dict(reference, sha256="b" * 64), bad_output, passphrase=self.password)
        self.assertFalse(bad_output.exists())

    def test_snapshot_preserves_active_overlay_and_rejects_external_path(self):
        source = self.root / "gateway"; source.mkdir()
        (source / ".env").write_text("COMPOSE_OVERLAY=compose.production-v2.yml\n")
        overlay = b"services:\n  new-api:\n    image: sha256:synthetic\n"
        (source / "compose.production-v2.yml").write_bytes(overlay)
        snapshot(self.store, source)
        record = self.store.records({"model": "__snapshot_bundle__"})[0]
        output = self.root / "materialized-overlay"
        with patch("getpass.getpass", return_value=self.password):
            materialize_snapshot(self.store, record["project_id"], self.store.keys(), output)
        import tarfile
        with tarfile.open(output / "bundle.tar") as bundle:
            self.assertEqual(bundle.extractfile("bundle/compose.production-v2.yml").read(), overlay)
        (source / ".env").write_text("COMPOSE_OVERLAY=../outside.yml\n")
        (self.root / "outside.yml").write_text("services: {}")
        with self.assertRaises(ValueError):
            snapshot(self.store, source)

    def test_archive_patch_is_repeatable_and_rejects_drift(self):
        path = Path(__file__).parents[1] / "core/patches/apply_archive.py"
        spec = importlib.util.spec_from_file_location("archive_patch", path); module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        src = self.root / "src"; (src / "router").mkdir(parents=True)
        for name in module.ROUTERS:
            (src / name).write_text("\n".join(["router.Use(middleware.TokenAuth())"] * 4))
        module.apply(src); first = (src / module.ROUTERS[0]).read_text(); module.apply(src)
        self.assertEqual(first, (src / module.ROUTERS[0]).read_text())
        (src / module.ROUTERS[0]).write_text("no anchors")
        with self.assertRaises(RuntimeError): module.apply(src, check=True)

    def test_archive_monitor_checks_remote_capacity_and_snapshot_freshness(self):
        path = Path(__file__).parents[1] / "scripts/archive_watch.py"
        spec = importlib.util.spec_from_file_location("archive_watch", path)
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        now = time.time()
        local = {"free_bytes": 4 * 1024**3, "queue_age_seconds": 0, "last_failure": 0}
        remote = {"free_bytes": 20 * 1024**3}
        self.assertEqual(module.alarms(local, remote, {"completed_at": now}, now), [])
        issues = module.alarms(dict(local, queue_age_seconds=61), {"free_bytes": 2 * 1024**3}, {}, now)
        self.assertEqual(len(issues), 3)
        self.assertEqual(len(module.alarms(None, None, {"completed_at": now}, now)), 2)

    def test_queue_metrics_use_metadata_index_for_large_encrypted_backlog(self):
        self.completed()
        with self.store.connect() as db:
            plan = db.execute("EXPLAIN QUERY PLAN SELECT MIN(created),COUNT(*) FROM outbox").fetchall()
            self.assertTrue(any("COVERING INDEX outbox_created" in row[3] for row in plan))
        metrics = self.store.metrics()
        self.assertEqual(metrics["queued_items"], 5)
        self.assertGreaterEqual(metrics["queue_age_seconds"], 0)


if __name__ == "__main__":
    unittest.main()
