"""Durable plaintext event store, user-scoped objects and recoverable upload outbox."""
import base64
import hashlib
import json
import os
import re
import shutil
import sqlite3
import stat
import threading
import time
import uuid
from contextlib import closing, contextmanager
from pathlib import Path


def encode(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def file_digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def safe_id(value):
    return hashlib.sha256(str(value).encode()).hexdigest()[:24] if value else "unclassified"


def atomic(path, data):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temp = path.with_name(path.name + "." + uuid.uuid4().hex + ".tmp")
    try:
        with temp.open("xb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temp, 0o600)
        os.replace(temp, path)
        if os.name != "nt":
            fd = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
    finally:
        temp.unlink(missing_ok=True)


class Store:
    def __init__(self, root, min_free=512 * 1024**2, *, max_local_bytes=0, evict_verified=False):
        self.root = Path(root)
        self.root.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.path = self.root / "archive-v3.sqlite3"
        self.min_free = min_free
        self.max_local_bytes = int(max_local_bytes)
        self.evict_verified = bool(evict_verified)
        self.download_object = None
        self.object_pins = {}
        self.lock = threading.RLock()
        self.index_lock = threading.Lock()
        # Incremental vacuum reclaims the pages formerly occupied by event bodies.
        # Convert existing stores before serving requests, while the service is offline.
        if self.evict_verified:
            with closing(sqlite3.connect(self.path, isolation_level=None)) as connection:
                if connection.execute("PRAGMA auto_vacuum").fetchone()[0] != 2:
                    connection.execute("PRAGMA auto_vacuum=INCREMENTAL")
                    connection.execute("VACUUM")
        with self.db() as db:
            db.executescript("""
            CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY,name TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS records(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL,project TEXT NOT NULL,conversation TEXT NOT NULL,created REAL NOT NULL,updated REAL NOT NULL,model TEXT NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT 'pending',title TEXT NOT NULL DEFAULT '',preview TEXT NOT NULL DEFAULT '',deleted INTEGER NOT NULL DEFAULT 0);
            CREATE INDEX IF NOT EXISTS record_scope ON records(deleted,user_id,project,conversation,updated,id);
            CREATE TABLE IF NOT EXISTS events(request_id TEXT NOT NULL,seq INTEGER NOT NULL,hash TEXT NOT NULL,raw BLOB NOT NULL,created REAL NOT NULL,segment TEXT,PRIMARY KEY(request_id,seq));
            CREATE INDEX IF NOT EXISTS events_unsealed ON events(segment,created);
            CREATE INDEX IF NOT EXISTS events_retained ON events(request_id,segment)
                WHERE segment IS NOT NULL AND length(raw)>0;
            CREATE TABLE IF NOT EXISTS objects(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL,path TEXT UNIQUE NOT NULL,size INTEGER NOT NULL,hash TEXT NOT NULL,kind TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'queued',remote_id TEXT,session TEXT,attempts INTEGER NOT NULL DEFAULT 0,next_try REAL NOT NULL DEFAULT 0,lease REAL NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',created REAL NOT NULL,verified REAL);
            CREATE INDEX IF NOT EXISTS upload_queue ON objects(state,next_try,lease,created);
            CREATE TABLE IF NOT EXISTS attachments(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL,request_id TEXT NOT NULL,name TEXT NOT NULL,mime TEXT NOT NULL,object_id TEXT,external_url TEXT,UNIQUE(request_id,id));
            CREATE TABLE IF NOT EXISTS segments(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL,project TEXT NOT NULL,conversation TEXT NOT NULL,object_id TEXT NOT NULL,manifest_id TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS segment_dependencies(segment TEXT NOT NULL,object_id TEXT NOT NULL,PRIMARY KEY(segment,object_id));
            CREATE TABLE IF NOT EXISTS grants(user_id INTEGER PRIMARY KEY);
            CREATE TABLE IF NOT EXISTS audits(id TEXT PRIMARY KEY,actor INTEGER NOT NULL,action TEXT NOT NULL,target TEXT NOT NULL,created REAL NOT NULL);
            CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS index_queue(request_id TEXT PRIMARY KEY);
            """)
            if "search_text" not in {row[1] for row in db.execute("PRAGMA table_info(records)")}:
                db.execute("ALTER TABLE records ADD COLUMN search_text TEXT")
            db.execute("""CREATE INDEX IF NOT EXISTS records_eviction_ready ON records(id)
                WHERE status!='pending' AND search_text IS NOT NULL""")
            db.execute("INSERT OR IGNORE INTO index_queue SELECT id FROM records WHERE status!='pending' AND deleted=0 AND search_text IS NULL")
        os.chmod(self.path, 0o600)

    @contextmanager
    def db(self):
        db = sqlite3.connect(self.path, timeout=15)
        db.row_factory = sqlite3.Row
        db.execute("PRAGMA journal_mode=WAL")
        db.execute("PRAGMA synchronous=FULL")
        db.execute("PRAGMA foreign_keys=ON")
        def decoded_event(raw):
            if not raw:
                return ""
            value = json.loads(raw)
            data = base64.b64decode(value["data"]).decode("utf-8", "replace")
            try:
                return json.dumps(json.loads(data), ensure_ascii=False)
            except (ValueError, RecursionError):
                return data
        db.create_function("event_text", 1, decoded_event)
        try:
            with db:
                yield db
        finally:
            db.close()

    def storage_usage(self):
        disk = shutil.disk_usage(self.root)
        if self.max_local_bytes and disk.total <= self.max_local_bytes:
            used = disk.total - disk.free
            hard_limited = True
        else:
            used = 0
            for path in self.root.rglob("*"):
                try:
                    info = path.lstat()
                except FileNotFoundError:
                    continue
                if stat.S_ISREG(info.st_mode):
                    used += info.st_size
            hard_limited = False
        return {"local_bytes": used, "max_local_bytes": self.max_local_bytes,
                "hard_limited": hard_limited, "free_bytes": disk.free,
                "evict_verified": self.evict_verified}

    def ensure_capacity(self, required):
        usage = self.storage_usage() if self.max_local_bytes else {"free_bytes": shutil.disk_usage(self.root).free}
        if usage["free_bytes"] < required + self.min_free:
            raise OSError("本地封存空间不足，已保留尚未上传的数据")
        if self.max_local_bytes and usage["local_bytes"] + required > self.max_local_bytes:
            raise OSError("本地封存已达到容量上限，等待同步清理后重试")

    @contextmanager
    def object_file(self, oid):
        """Pin a verified local object or fetch a temporary checked copy from Drive."""
        temporary = None
        with self.lock, self.db() as db:
            row = db.execute("SELECT * FROM objects WHERE id=?", (oid,)).fetchone()
            if not row:
                raise KeyError("归档文件不存在")
            item = dict(row)
            path = self.root / "files" / item["path"]
            self.object_pins[oid] = self.object_pins.get(oid, 0) + 1
        try:
            if not path.is_file():
                if item["state"] != "verified" or not item["remote_id"] or not self.download_object:
                    raise OSError("归档文件暂时不可读取")
                with self.lock:
                    self.ensure_capacity(item["size"] + 65536)
                    temporary = self.root / "cache" / (uuid.uuid4().hex + ".download")
                    temporary.parent.mkdir(mode=0o700, exist_ok=True)
                self.download_object(item, temporary)
                path = temporary
            if path.stat().st_size != item["size"] or file_digest(path) != item["hash"]:
                raise OSError("归档文件完整性校验失败")
            yield path
        finally:
            if temporary:
                temporary.unlink(missing_ok=True)
            with self.lock:
                self.object_pins[oid] -= 1
                if not self.object_pins[oid]:
                    self.object_pins.pop(oid)

    def cleanup_verified(self):
        if not self.evict_verified:
            return {"files": 0, "bytes": 0}
        removed = freed = 0
        with self.lock, self.db() as db:
            # Keep sequence and hash metadata for idempotent writes and exact recovery.
            # Indexing and all dependent uploads must complete before discarding a body.
            # Bound each write transaction. Select from finalized records via
            # the partial index so already-evicted bodies are never scanned.
            db.execute("""UPDATE events SET raw=X'' WHERE rowid IN (
                SELECT e.rowid FROM records r INDEXED BY records_eviction_ready
                CROSS JOIN events e INDEXED BY events_retained ON e.request_id=r.id
                WHERE r.status!='pending' AND r.search_text IS NOT NULL
                    AND NOT EXISTS(SELECT 1 FROM index_queue i WHERE i.request_id=r.id)
                    AND e.segment IS NOT NULL AND length(e.raw)>0
                    AND EXISTS(SELECT 1 FROM segments s JOIN objects m ON m.id=s.manifest_id
                        WHERE s.id=e.segment AND m.state='verified'
                        AND NOT EXISTS(SELECT 1 FROM segment_dependencies d JOIN objects o ON o.id=d.object_id
                            WHERE d.segment=s.id AND o.state!='verified'))
                LIMIT 512)""")
            items = db.execute("SELECT * FROM objects WHERE state='verified' AND remote_id IS NOT NULL").fetchall()
            for item in items:
                if item["id"] in self.object_pins:
                    continue
                path = self.root / "files" / item["path"]
                if not path.is_file():
                    continue
                try:
                    size = path.stat().st_size
                    path.unlink()
                    removed += 1
                    freed += size
                except OSError:
                    continue
        with self.db() as db:
            db.execute("PRAGMA incremental_vacuum(2048)")
            db.execute("PRAGMA wal_checkpoint(PASSIVE)")
        return {"files": removed, "bytes": freed}

    def setting(self, key, value=None):
        with self.db() as db:
            if value is not None:
                db.execute("INSERT OR REPLACE INTO settings VALUES(?,?)", (key, json.dumps(value)))
            row = db.execute("SELECT value FROM settings WHERE key=?", (key,)).fetchone()
        return json.loads(row[0]) if row else None

    def audit(self, actor, action, target=""):
        with self.db() as db:
            db.execute("INSERT INTO audits VALUES(?,?,?,?,?)", (uuid.uuid4().hex, actor, action, str(target)[:512], time.time()))

    def ingest(self, raw, *, defer_index=False):
        envelope = json.loads(raw)
        if envelope.get("version") != 3:
            raise ValueError("Only plaintext archive v3 accepted")
        meta = envelope["meta"]
        rid, seq, uid = meta["archive_id"], meta["seq"], meta["user_id"]
        if not re.fullmatch(r"[a-f0-9]{32}", rid) or type(seq) is not int or seq < 0 or type(uid) is not int or uid < 1:
            raise ValueError("Invalid event identity")
        if meta["kind"] not in {"request", "input", "output", "final"}:
            raise ValueError("Unknown event kind")
        data = base64.b64decode(envelope["data"], validate=True)
        if len(data) > 1024**2:
            raise ValueError("Event must be chunked")
        sha = digest(raw)
        now = time.time()
        with self.lock, self.db() as db:
            old = db.execute("SELECT hash FROM events WHERE request_id=? AND seq=?", (rid, seq)).fetchone()
            if old:
                if old[0] != sha:
                    raise ValueError("Conflicting event")
                return sha
            self.ensure_capacity(len(raw) * 4 + 65536)
            previous = db.execute("SELECT seq,hash FROM events WHERE request_id=? ORDER BY seq DESC LIMIT 1", (rid,)).fetchone()
            if (previous is None and (seq != 0 or meta["kind"] != "request")) or (previous and (seq != previous[0] + 1 or meta.get("prev_hash") != previous[1])):
                raise ValueError("Broken event sequence")
            project, conversation = str(meta.get("project_id", ""))[:128], str(meta.get("conversation_id", ""))[:128]
            if previous:
                owner = db.execute("SELECT user_id,status FROM records WHERE id=?", (rid,)).fetchone()
                if owner[0] != uid:
                    raise ValueError("Owner mismatch")
                if owner["status"] != "pending":
                    raise ValueError("Request already finalized")
            else:
                db.execute("INSERT OR IGNORE INTO users VALUES(?,?)", (uid, "用户 " + str(uid)))
                db.execute("INSERT INTO records(id,user_id,project,conversation,created,updated,model) VALUES(?,?,?,?,?,?,?)", (rid, uid, project, conversation, float(meta.get("created", now)), now, str(meta.get("model", ""))[:256]))
            db.execute("INSERT INTO events VALUES(?,?,?,?,?,NULL)", (rid, seq, sha, raw, now))
            db.execute("UPDATE records SET updated=? WHERE id=?", (now, rid))
            if meta["kind"] == "final":
                status = meta.get("status", "interrupted")
                if status not in {"completed", "cancelled", "failed", "interrupted"}:
                    status = "interrupted"
                db.execute("UPDATE records SET status=? WHERE id=?", (status, rid))
                db.execute("INSERT OR IGNORE INTO index_queue VALUES(?)", (rid,))
        if meta["kind"] == "final" and not defer_index:
            # A temporary indexing failure must not undo durable event acceptance.
            # The worker retries before the final event can be sealed.
            try:
                self.finish_index(rid)
            except (OSError, sqlite3.Error):
                pass
        return sha

    def finish_index(self, rid=None):
        with self.index_lock:
            with self.db() as db:
                if rid is None:
                    row = db.execute("SELECT request_id FROM index_queue LIMIT 1").fetchone()
                    if row is None:
                        return
                    rid = row[0]
            self.index_record(rid)
            with self.db() as db:
                db.execute("DELETE FROM index_queue WHERE request_id=?", (rid,))

    def events(self, rid):
        return [json.loads(raw) for raw in self.raw_events(rid)]

    def raw_events(self, rid):
        with self.db() as db:
            record = db.execute("SELECT user_id FROM records WHERE id=? AND deleted=0", (rid,)).fetchone()
            if not record:
                raise KeyError("记录不存在")
            rows = [dict(row) for row in db.execute("SELECT seq,hash,raw,segment FROM events WHERE request_id=? ORDER BY seq", (rid,))]
            segments = {row["segment"]: None for row in rows if not row["raw"]}
            for sid in segments:
                segment = db.execute("SELECT object_id FROM segments WHERE id=?", (sid,)).fetchone()
                if not segment:
                    raise OSError("缺少归档分段元数据")
                segments[sid] = segment[0]
        fetched = {}
        wanted = {row["seq"]: row["hash"] for row in rows if not row["raw"]}
        for oid in segments.values():
            with self.object_file(oid) as path, path.open("rb") as stream:
                for line in stream:
                    event = json.loads(line)
                    if event["meta"]["archive_id"] != rid:
                        continue
                    seq = event["meta"]["seq"]
                    if seq not in wanted:
                        continue
                    raw = event["transport_json"].encode("utf-8")
                    if event["meta"]["user_id"] != record["user_id"] or digest(raw) != wanted[seq]:
                        raise OSError("远端事件校验失败")
                    fetched[seq] = raw
        result = []
        for row in rows:
            raw = row["raw"] or fetched.get(row["seq"])
            if not raw:
                raise OSError("远端归档缺少所需事件")
            result.append(raw)
        return result

    def payloads(self, rid):
        return {k: v.decode("utf-8", "replace") for k, v in self.raw_payloads(rid).items()}

    def raw_payloads(self, rid):
        events = self.events(rid)
        chunks = {"request": bytearray(), "input": bytearray(), "output": bytearray(), "final": bytearray()}
        for event in events:
            chunks[event["meta"]["kind"]].extend(base64.b64decode(event["data"]))
        return {k: bytes(v) for k, v in chunks.items()}

    def index_record(self, rid):
        payloads = self.payloads(rid)
        try:
            inp = json.loads(payloads["input"])
        except (ValueError, RecursionError):
            inp = {}
        messages = inp.get("messages", inp.get("input", [])) if isinstance(inp, dict) else []
        if isinstance(messages, str):
            title = messages
        elif isinstance(messages, list):
            title = next((str(m.get("content", "")) for m in reversed(messages) if isinstance(m, dict) and m.get("role") == "user"), inp.get("prompt", "") if isinstance(inp, dict) else "")
        else:
            title = inp.get("prompt", "") if isinstance(inp, dict) else ""
        title = re.sub(r"\s+", " ", str(title or ""))[:64] or "未命名请求"
        output = payloads["output"]
        try:
            parsed = json.loads(output)
            preview = parsed.get("choices", [{}])[0].get("message", {}).get("content", "") or output
        except (ValueError, AttributeError, IndexError, KeyError, TypeError, RecursionError):
            preview = output
        with self.db() as db:
            search_text = self.searchable_payload(payloads["input"]) + "\n" + self.searchable_payload(output)
            db.execute("UPDATE records SET title=?,preview=?,search_text=? WHERE id=?", (title, re.sub(r"\s+", " ", str(preview))[:180], search_text, rid))
        self.extract_attachments(rid, inp)
        try:
            self.extract_attachments(rid, json.loads(output))
        except (ValueError, RecursionError):
            pass
        try:
            final = json.loads(payloads["final"])
            mime = final.get("content_type", "").split(";")[0].strip().lower()
            if mime.startswith(("image/", "audio/", "video/")) or mime in {"application/octet-stream", "application/pdf"}:
                data = self.raw_payloads(rid)["output"]
                if data:
                    self.add_attachment(rid, "模型输出", mime, data)
        except (ValueError, AttributeError, RecursionError):
            pass

    @staticmethod
    def searchable_payload(payload):
        def text_parts(value):
            if isinstance(value, str):
                if not value.startswith("data:"):
                    yield value
            elif isinstance(value, list):
                for item in value:
                    yield from text_parts(item)
            elif isinstance(value, dict):
                for key, item in value.items():
                    if key not in {"b64_json", "file_data", "binary_base64"} and not (key == "data" and isinstance(item, str)):
                        yield from text_parts(item)
        try:
            return "\n".join(text_parts(json.loads(payload)))
        except (ValueError, RecursionError):
            if not payload.startswith("data:"):
                return payload
            parts = []
            for line in payload.splitlines():
                if line.startswith("data:"):
                    try:
                        parts.extend(text_parts(json.loads(line[5:])))
                    except (ValueError, RecursionError):
                        continue
            return "\n".join(parts)

    def object(self, db, uid, relative, data, kind):
        if not relative.startswith(f"user-{uid}/") or ".." in Path(relative).parts:
            raise ValueError("Object outside user directory")
        oid = digest(relative.encode())
        sha = digest(data)
        existing = db.execute("SELECT hash,state,remote_id FROM objects WHERE id=?", (oid,)).fetchone()
        if existing and existing[0] != sha:
            raise ValueError("Immutable object conflict")
        path = self.root / "files" / relative
        if not existing or not path.is_file() and not (existing["state"] == "verified" and existing["remote_id"]):
            self.ensure_capacity(len(data) + 65536)
            atomic(path, data)
        db.execute("INSERT OR IGNORE INTO objects(id,user_id,path,size,hash,kind,created) VALUES(?,?,?,?,?,?,?)", (oid, uid, relative, len(data), sha, kind, time.time()))
        return oid

    def add_attachment(self, rid, name, mime, data=None, external=None):
        name = str(name or "附件")
        mime = str(mime or "application/octet-stream")
        with self.lock, self.db() as db:
            row = db.execute("SELECT user_id FROM records WHERE id=? AND deleted=0", (rid,)).fetchone()
            if not row:
                raise KeyError("Missing request")
            uid = row[0]
            aid = digest(encode([uid, rid, name, mime, digest(data) if data is not None else external]))
            oid = None
            if data is not None:
                # Deduplication is deliberately scoped to the owning user.
                oid = self.object(db, uid, f"user-{uid}/attachments/sha256/{digest(data)[:2]}/{digest(data)}", data, "attachment")
            db.execute("INSERT OR IGNORE INTO attachments VALUES(?,?,?,?,?,?,?)", (aid, uid, rid, name[:256], mime[:128], oid, external))
        return aid

    def extract_attachments(self, rid, value):
        def visit(obj):
            if isinstance(obj, list):
                for item in obj:
                    visit(item)
            elif isinstance(obj, dict):
                url = obj.get("image_url", obj.get("file_data"))
                if isinstance(url, dict):
                    url = url.get("url")
                if isinstance(url, str):
                    match = re.fullmatch(r"data:([^;,]+);base64,(.*)", url, re.S)
                    if match:
                        self.add_attachment(rid, obj.get("filename", "图片"), match[1], base64.b64decode(match[2], validate=True))
                    elif url.startswith(("http://", "https://")):
                        self.add_attachment(rid, obj.get("filename", "外部文件"), "external", external=url)
                source = obj.get("source", {})
                if isinstance(source, dict) and source.get("type") == "base64":
                    self.add_attachment(rid, obj.get("title", "附件"), source.get("media_type", "application/octet-stream"), base64.b64decode(source["data"], validate=True))
                if "inlineData" in obj:
                    item = obj["inlineData"]
                    self.add_attachment(rid, "附件", item.get("mimeType", "application/octet-stream"), base64.b64decode(item["data"], validate=True))
                if isinstance(obj.get("b64_json"), str):
                    self.add_attachment(rid, "生成图片", "image/png", base64.b64decode(obj["b64_json"], validate=True))
                if "parts" in obj:
                    for part in obj["parts"]:
                        if isinstance(part, dict) and part.get("filename") and "data" in part:
                            self.add_attachment(rid, part["filename"], part.get("content_type", "application/octet-stream"), base64.b64decode(part["data"], validate=True))
                for key, item in obj.items():
                    if key not in {"image_url", "file_data", "source", "inlineData"}:
                        visit(item)
        try:
            visit(value)
        except (ValueError, TypeError, KeyError, AttributeError, RecursionError):
            # Malformed attachments never invalidate an already persisted event.
            self.audit(0, "attachment_parse_failed", rid)

    def records(self, filters):
        where, args = ["r.deleted=0"], []
        for key, column in [("user", "user_id"), ("project", "project"), ("conversation", "conversation"), ("status", "status"), ("model", "model")]:
            if key in filters:
                where.append(f"r.{column}=?")
                args.append(filters[key])
        if filters.get("attachment") == "1":
            where.append("EXISTS(SELECT 1 FROM attachments a WHERE a.request_id=r.id)")
        if filters.get("abnormal") == "1":
            where.append("r.status IN ('failed','cancelled','interrupted')")
        for key, op in [("start", ">="), ("end", "<")]:
            if filters.get(key):
                where.append(f"r.created{op}?")
                args.append(float(filters[key]))
        # Exact UTF-8 substring search, including short Chinese words, over the
        # entire requested scope. No silent recent-only search window.
        if filters.get("q"):
            where.append("(instr(lower(r.title),?)>0 OR instr(lower(r.preview),?)>0 OR instr(lower(r.project),?)>0 OR instr(lower(r.conversation),?)>0 OR instr(lower(r.id),?)>0 OR EXISTS(SELECT 1 FROM users u WHERE u.id=r.user_id AND (instr(lower(u.name),?)>0 OR CAST(u.id AS TEXT)=?)) OR instr(lower(coalesce(r.search_text,'')),?)>0 OR EXISTS(SELECT 1 FROM events e WHERE e.request_id=r.id AND length(e.raw)>0 AND instr(lower(event_text(e.raw)),?)>0))")
            args.extend([filters["q"].lower()] * 9)
        if filters.get("local_q"):
            where.append("(instr(lower(r.title),?)>0 OR instr(lower(r.preview),?)>0 OR instr(lower(r.conversation),?)>0)")
            args.extend([filters["local_q"].lower()] * 3)
        cursor = filters.get("cursor")
        if cursor:
            stamp, rid = json.loads(base64.urlsafe_b64decode(cursor))
            where.append("(r.updated<? OR (r.updated=? AND r.id<?))")
            args.extend([stamp, stamp, rid])
        limit = max(1, min(int(filters.get("limit", 60)), 200))
        with self.db() as db:
            rows = [dict(r) for r in db.execute("SELECT r.*,(SELECT count(*) FROM attachments a WHERE a.request_id=r.id) attachment_count FROM records r WHERE " + " AND ".join(where) + " ORDER BY r.updated DESC,r.id DESC LIMIT ?", [*args, limit + 1])]
        more = len(rows) > limit
        rows = rows[:limit]
        next_cursor = base64.urlsafe_b64encode(encode([rows[-1]["updated"], rows[-1]["id"]])).decode() if rows and more else None
        return {"records": rows, "next_cursor": next_cursor}

    def directory(self):
        with self.db() as db:
            groups = [dict(r) for r in db.execute("SELECT user_id,project,conversation,count(*) count,min(created) first,max(updated) latest FROM records WHERE deleted=0 GROUP BY user_id,project,conversation ORDER BY latest DESC")]
            for group in groups:
                latest = db.execute("SELECT title FROM records WHERE user_id=? AND project=? AND conversation=? AND deleted=0 ORDER BY updated DESC LIMIT 1", (group["user_id"],group["project"],group["conversation"])).fetchone()
                group["title"] = latest[0] if group["conversation"] and latest else ""
            for item in self.setting("collections") or []:
                group = next((g for g in groups if all(g[key] == item[key] for key in ("user_id", "project", "conversation"))), None)
                if group:
                    group["title"] = item["title"] or group["title"]
                else:
                    groups.append({**item,"count":0,"latest":0})
            return {"users": [dict(r) for r in db.execute("SELECT * FROM users ORDER BY id")], "groups": groups}

    def detail(self, rid, include_payloads=True):
        with self.db() as db:
            row = db.execute("SELECT * FROM records WHERE id=? AND deleted=0", (rid,)).fetchone()
            if not row:
                raise KeyError("记录不存在")
            attachments = [dict(r) for r in db.execute("SELECT a.*,o.size,o.state FROM attachments a LEFT JOIN objects o ON o.id=a.object_id WHERE a.request_id=?", (rid,))]
            sealed = [dict(r) for r in db.execute("SELECT DISTINCT o.state,o.path,o.remote_id FROM events e JOIN segments s ON s.id=e.segment JOIN objects o ON o.id=s.manifest_id WHERE e.request_id=? ORDER BY o.created DESC", (rid,))]
            unsealed = db.execute("SELECT count(*) FROM events WHERE request_id=? AND segment IS NULL", (rid,)).fetchone()[0]
            blocked = db.execute("SELECT 1 FROM events e JOIN segment_dependencies d ON d.segment=e.segment JOIN objects o ON o.id=d.object_id WHERE e.request_id=? AND o.state='blocked' LIMIT 1", (rid,)).fetchone()
        states = [s["state"] for s in sealed] + [a["state"] for a in attachments if a["object_id"]]
        sync = "blocked" if blocked or "blocked" in states else "verified" if states and all(s == "verified" for s in states) and not unsealed else "pending"
        return {"record": dict(row), **({"payloads": self.payloads(rid)} if include_payloads else {}), "attachments": attachments, "sync": sync, "manifest": next((s for s in sealed if s["state"] == "verified"), None)}

    def seal(self, force=False):
        cutoff = time.time() - (0 if force else 300)
        count = 0
        with self.lock, self.db() as db:
            eligible = "e.segment IS NULL AND r.deleted=0 AND NOT EXISTS(SELECT 1 FROM index_queue i WHERE i.request_id=r.id)"
            groups = db.execute("SELECT r.user_id,r.project,r.conversation,min(e.created) oldest,sum(length(e.raw)) bytes FROM events e JOIN records r ON r.id=e.request_id WHERE " + eligible + " GROUP BY r.user_id,r.project,r.conversation").fetchall()
            for group in groups:
                if group["oldest"] > cutoff and group["bytes"] < 32 * 1024**2:
                    continue
                uid, project, conv = group["user_id"], group["project"], group["conversation"]
                events = []
                total = 0
                for event in db.execute("SELECT e.* FROM events e JOIN records r ON r.id=e.request_id WHERE " + eligible + " AND r.user_id=? AND r.project=? AND r.conversation=? ORDER BY e.created,e.request_id,e.seq", (uid, project, conv)):
                    events.append(event)
                    total += len(event["raw"]) + 1
                    if total >= 32 * 1024**2:
                        break
                sid = uuid.uuid4().hex
                date = time.strftime("%Y/%m/%d", time.gmtime(group["oldest"] + 8 * 3600))
                prefix = f"user-{uid}/projects/{safe_id(project)}/conversations/{safe_id(conv)}"
                # Disk JSONL is human-readable, unlike the Base64 transport.
                lines = []
                for event in events:
                    value = json.loads(event["raw"])
                    payload = base64.b64decode(value.pop("data"))
                    try:
                        value["text"] = payload.decode("utf-8")
                    except UnicodeDecodeError:
                        value["binary_base64"] = base64.b64encode(payload).decode()
                    value["event_hash"] = event["hash"]
                    # Preserve exact ingress bytes for hash-chain recovery;
                    # the parallel text field keeps Drive directly readable.
                    value["transport_json"] = bytes(event["raw"]).decode("utf-8")
                    lines.append(encode(value) + b"\n")
                data = b"".join(lines)
                oid = self.object(db, uid, f"{prefix}/records/{date}/part-{sid}.jsonl", data, "segment")
                rids = list({e["request_id"] for e in events})
                deps = [r[0] for rid in rids for r in db.execute("SELECT object_id FROM attachments WHERE request_id=? AND object_id IS NOT NULL", (rid,))]
                attachment_metadata = [dict(a) for rid in rids for a in db.execute("SELECT a.*,o.path,o.hash,o.size FROM attachments a LEFT JOIN objects o ON o.id=a.object_id WHERE a.request_id=?", (rid,))]
                manifest = {"schema_version": 3, "segment_id": sid, "user_id": uid, "project_id": project, "conversation_id": conv, "object_id": oid, "sha256": digest(data), "bytes": len(data), "events": len(events), "attachments": sorted(set(deps)), "attachment_metadata":attachment_metadata}
                mid = self.object(db, uid, f"{prefix}/manifests/{date}/part-{sid}.json", encode(manifest), "manifest")
                db.execute("INSERT INTO segments VALUES(?,?,?,?,?,?)", (sid, uid, project, conv, oid, mid))
                for dep in [oid, *deps]:
                    db.execute("INSERT OR IGNORE INTO segment_dependencies VALUES(?,?)", (sid, dep))
                db.executemany("UPDATE events SET segment=? WHERE request_id=? AND seq=?", [(sid, e["request_id"], e["seq"]) for e in events])
                count += 1
        return count

    def claim(self):
        now = time.time()
        with self.lock, self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            row = db.execute("SELECT o.* FROM objects o WHERE o.state IN ('queued','retry','uploading') AND o.next_try<=? AND o.lease<? AND (o.kind!='manifest' OR NOT EXISTS(SELECT 1 FROM segments s JOIN segment_dependencies d ON d.segment=s.id JOIN objects dep ON dep.id=d.object_id WHERE s.manifest_id=o.id AND dep.state!='verified')) ORDER BY CASE o.kind WHEN 'manifest' THEN 2 WHEN 'attachment' THEN 1 ELSE 0 END,o.created LIMIT 1", (now, now)).fetchone()
            if row:
                db.execute("UPDATE objects SET state='uploading',lease=? WHERE id=?", (now + 600, row["id"]))
        return dict(row) if row else None

    def mark(self, oid, **fields):
        allowed = {"remote_id", "session", "state", "verified", "error", "next_try", "attempts", "lease"}
        if not fields or not set(fields) <= allowed:
            raise ValueError("Invalid job update")
        with self.db() as db:
            db.execute("UPDATE objects SET " + ",".join(k + "=?" for k in fields) + " WHERE id=?", [*fields.values(), oid])

    def metrics(self):
        with self.db() as db:
            queue = db.execute("SELECT count(*) count,coalesce(sum(size),0) bytes,min(created) oldest FROM objects WHERE state!='verified'").fetchone()
            stats = dict(db.execute("SELECT count(*) requests,sum(status='pending') pending,sum(status IN ('failed','interrupted','cancelled')) abnormal FROM records WHERE deleted=0").fetchone())
            states = [dict(r) for r in db.execute("SELECT state,count(*) count FROM objects GROUP BY state")]
        return {**stats, "queued_items": queue["count"], "queued_bytes": queue["bytes"], "queue_age_seconds": time.time() - queue["oldest"] if queue["oldest"] else 0, **self.storage_usage(), "states": states}

    def control(self, db, uid, value):
        self.object(db, uid, f"user-{uid}/controls/{uuid.uuid4().hex}.json", encode({**value, "user_id": uid, "time": time.time()}), "control")

    def group(self, rids, project, conversation, *, journal=True):
        if not isinstance(rids, list) or not 1 <= len(rids) <= 500 or any(not isinstance(rid, str) for rid in rids):
            raise ValueError("请选择 1–500 条有效记录")
        rids = list(dict.fromkeys(rids))
        with self.lock, self.db() as db:
            owners = [r[0] for rid in rids for r in db.execute("SELECT user_id FROM records WHERE id=? AND deleted=0", (rid,))]
            if len(owners) != len(rids) or len(set(owners)) != 1:
                raise ValueError("只能整理同一个用户的请求")
            for rid in rids:
                db.execute("UPDATE records SET project=?,conversation=?,updated=? WHERE id=? AND deleted=0", (project[:128], conversation[:128], time.time(), rid))
            uid = owners[0]
            if journal:
                self.control(db, uid, {"action": "group", "request_ids": rids, "project": project[:128], "conversation": conversation[:128]})

    def rename_user(self, uid, name, *, journal=True):
        name = name.strip()[:128]
        if not name:
            raise ValueError("显示名不能为空")
        with self.lock, self.db() as db:
            if not db.execute("UPDATE users SET name=? WHERE id=?", (name, uid)).rowcount:
                raise KeyError("用户不存在")
            if journal:
                self.control(db, uid, {"action": "user_name", "name": name})

    def collection(self, uid, project, conversation, title, *, journal=True):
        project, conversation, title = project.strip()[:128], conversation.strip()[:128], title.strip()[:128]
        if not project:
            raise ValueError("项目名称不能为空")
        item = {"user_id":uid,"project":project,"conversation":conversation,"title":title}
        with self.lock, self.db() as db:
            if not db.execute("SELECT 1 FROM users WHERE id=?", (uid,)).fetchone():
                raise KeyError("用户不存在")
            entries = [entry for entry in (self.setting("collections") or []) if any(entry[key] != item[key] for key in ("user_id", "project", "conversation"))]
            db.execute("INSERT OR REPLACE INTO settings VALUES('collections',?)", (json.dumps([*entries, item]),))
            if journal:
                self.control(db, uid, {"action": "collection", **item})

    def retry(self):
        with self.db() as db:
            db.execute("UPDATE objects SET state='queued',next_try=0,lease=0,error='' WHERE state IN ('retry','blocked')")

    def checkpoint(self):
        """Portable per-user catalog; no live SQLite copy or Google secrets."""
        with self.lock, self.db() as db:
            for user in db.execute("SELECT * FROM users").fetchall():
                uid = user["id"]
                records = [{key: row[key] for key in row.keys() if key != "search_text"} for row in db.execute("SELECT * FROM records WHERE user_id=?", (uid,))]
                value = {"schema_version":3,"user":dict(user),"records":records,"segments":[dict(r) for r in db.execute("SELECT * FROM segments WHERE user_id=?",(uid,))],"created":time.time()}
                stamp = time.strftime("%Y%m%dT%H%M%SZ",time.gmtime())+"-"+uuid.uuid4().hex[:8]
                self.object(db,uid,f"user-{uid}/catalogs/{stamp}.json",encode(value),"catalog")
        self.setting("last_checkpoint",time.time())
