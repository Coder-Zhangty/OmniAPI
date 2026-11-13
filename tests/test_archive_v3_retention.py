"""Verified-only local eviction, exact cold reads, and quota backpressure."""
import json
import tempfile
import unittest
import zipfile
from pathlib import Path

from ops.archive_v3.store import Store
from ops.archive_v3.synthetic import seed
from ops.archive_v3.service import Application


class RetentionTests(unittest.TestCase):
    def setUp(self):
        parent = Path(__file__).resolve().parents[1] / "runtime/archive-v3-tests"
        parent.mkdir(exist_ok=True, parents=True)
        self.temp = tempfile.TemporaryDirectory(dir=parent)
        self.root = Path(self.temp.name)
        self.store = Store(self.root, 0, evict_verified=True)
        seed(self.store)
        self.store.seal(force=True)
        with self.store.db() as db:
            self.items = [dict(row) for row in db.execute("SELECT * FROM objects")]
        self.remote = {
            item["id"]: (self.root / "files" / item["path"]).read_bytes()
            for item in self.items
        }
        self.downloaded = []

        def fetch(item, target):
            self.downloaded.append(item["id"])
            target.write_bytes(self.remote[item["id"]])

        self.store.download_object = fetch

    def tearDown(self):
        self.temp.cleanup()

    def verify_all(self):
        for item in self.items:
            self.store.mark(item["id"], state="verified", remote_id=item["id"], lease=0)

    def test_cleanup_preserves_pending_bytes_and_requires_manifest(self):
        self.assertEqual(self.store.cleanup_verified()["files"], 0)
        segment = next(item for item in self.items if item["kind"] == "segment")
        self.store.mark(segment["id"], state="verified", remote_id=segment["id"])
        self.store.cleanup_verified()
        with self.store.db() as db:
            self.assertTrue(db.execute("SELECT 1 FROM events WHERE length(raw)>0").fetchone())
        unverified = next(item for item in self.items if item["kind"] == "manifest")
        self.assertTrue((self.root / "files" / unverified["path"]).is_file())

    def test_cold_transcript_search_and_attachment_survive_eviction(self):
        row = self.store.records({"attachment": "1"})["records"][0]
        expected = self.store.raw_events(row["id"])
        before = self.store.detail(row["id"])
        self.verify_all()
        cleaned = self.store.cleanup_verified()
        self.assertGreater(cleaned["bytes"], 0)
        self.assertFalse(any((self.root / "files").rglob("*.jsonl")))
        with self.store.db() as db:
            self.assertEqual(db.execute("SELECT sum(length(raw)) FROM events").fetchone()[0], 0)
        self.assertEqual(self.store.raw_events(row["id"]), expected)
        self.assertEqual(self.store.detail(row["id"])["payloads"], before["payloads"])
        self.assertTrue(self.store.records({"q": "验证码"})["records"])
        attachment = before["attachments"][0]
        with self.store.object_file(attachment["object_id"]) as path:
            self.assertEqual(path.read_bytes(), self.remote[attachment["object_id"]])
        self.assertFalse(list((self.root / "cache").glob("*.download")))
        self.assertEqual(self.store.ingest(expected[0]), json.loads(expected[1])["meta"]["prev_hash"])

    def test_cleanup_releases_the_write_lock_between_batches(self):
        self.verify_all()
        with self.store.db() as db:
            row = db.execute("SELECT request_id,hash,raw,created,segment FROM events LIMIT 1").fetchone()
            db.executemany(
                "INSERT INTO events VALUES(?,?,?,?,?,?)",
                [(row["request_id"], seq, *tuple(row)[1:]) for seq in range(1000, 1520)],
            )
            before = db.execute("SELECT count(*) FROM events WHERE length(raw)>0").fetchone()[0]
        self.store.cleanup_verified()
        with self.store.db() as db:
            remaining = db.execute("SELECT count(*) FROM events WHERE length(raw)>0").fetchone()[0]
        self.assertEqual(remaining, before - 512)
        self.store.cleanup_verified()
        with self.store.db() as db:
            self.assertEqual(db.execute("SELECT count(*) FROM events WHERE length(raw)>0").fetchone()[0], 0)

    def test_corrupt_download_is_rejected_and_temporary_copy_is_removed(self):
        self.verify_all()
        self.store.cleanup_verified()
        attachment = next(item for item in self.items if item["kind"] == "attachment")
        self.remote[attachment["id"]] = b"tampered"
        with self.assertRaises(OSError):
            with self.store.object_file(attachment["id"]):
                self.fail("Corrupt remote content was accepted")
        self.assertFalse(self.store.object_pins)
        self.assertFalse(list((self.root / "cache").glob("*.download")))

    def test_reader_pin_prevents_cleanup_of_open_file(self):
        self.verify_all()
        item = next(item for item in self.items if item["kind"] == "attachment")
        with self.store.object_file(item["id"]) as path:
            self.store.cleanup_verified()
            self.assertTrue(path.is_file())
        self.store.cleanup_verified()
        self.assertFalse(path.exists())

    def test_full_local_quota_rejects_new_bytes_without_discarding_pending(self):
        pending = next(item for item in self.items if item["kind"] == "attachment")
        path = self.root / "files" / pending["path"]
        original = path.read_bytes()
        self.store.max_local_bytes = self.store.storage_usage()["local_bytes"] + 8
        with self.assertRaises(OSError):
            self.store.ensure_capacity(1024)
        self.store.cleanup_verified()
        self.assertEqual(path.read_bytes(), original)
        self.assertFalse(self.downloaded)

    def test_export_fetches_evicted_events_and_original_attachments(self):
        self.verify_all()
        self.store.cleanup_verified()
        app = Application(self.root, {"min_free_bytes": 0, "evict_verified": True})
        app.store.download_object = self.store.download_object
        job = app.create_export({"id": 1, "role": 100}, {"format": "html", "filters": {"user": "42"}})
        app.export_threads[job["id"]].join(10)
        self.assertEqual(app.store.setting("export:" + job["id"])["state"], "ready")
        with zipfile.ZipFile(self.root / "exports" / (job["id"] + ".zip")) as archive:
            self.assertTrue(any(name.endswith("/events.jsonl") for name in archive.namelist()))
            attachments = [name for name in archive.namelist() if "/attachments/" in name]
            self.assertEqual(len(attachments), 2)
            self.assertTrue(any(archive.read(name).startswith(b"%PDF") for name in attachments))
        self.assertFalse(list((self.root / "cache").glob("*.download")))


if __name__ == "__main__":
    unittest.main()
