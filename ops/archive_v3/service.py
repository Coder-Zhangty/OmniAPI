"""Archive v3 HTTP API. Operator authentication is verified against the gateway."""
import argparse
import base64
import hashlib
import hmac
import html
import json
import mimetypes
import os
import re
import secrets
import threading
import time
import urllib.parse
import urllib.request
import uuid
import zipfile
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from .drive import Drive, DriveError, NoRedirect
from .store import Store, encode

HERE = Path(__file__).parent


def messages(payloads):
    """Readable offline fallback; raw payloads are always exported alongside it."""
    result = []
    try:
        request = json.loads(payloads["input"])
        content = request.get("messages", request.get("input", request.get("contents", request.get("prompt", []))))
        if isinstance(content, str):
            result.append(("user", content))
        elif isinstance(content, list):
            for item in content:
                if isinstance(item, dict):
                    value = item.get("content", item.get("parts", item.get("text", "")))
                    result.append((item.get("role", "user"), value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)))
    except (ValueError, AttributeError):
        result.append(("input", payloads["input"]))
    output = payloads["output"]
    try:
        response = json.loads(output)
        value = response.get("choices", [{}])[0].get("message", {}).get("content")
        if value is None:
            value = response.get("content", response.get("output", response.get("candidates", response)))
        result.append(("assistant", value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)))
    except (ValueError, AttributeError, IndexError):
        chunks = []
        for line in output.splitlines():
            if not line.startswith("data:"):
                continue
            try:
                event = json.loads(line[5:].strip())
                if event.get("choices"):
                    chunks.append(event["choices"][0].get("delta", {}).get("content", ""))
                elif isinstance(event.get("delta"), str):
                    chunks.append(event["delta"])
                elif isinstance(event.get("delta"), dict):
                    chunks.append(event["delta"].get("text", ""))
            except (ValueError, AttributeError, IndexError):
                continue
        result.append(("assistant", "".join(chunks) or output))
    return result


class Application:
    def __init__(self, root, config, identity=None):
        self.config = config
        self.store = Store(
            root, config.get("min_free_bytes", 512 * 1024**2),
            max_local_bytes=config.get("max_local_bytes", 0),
            evict_verified=config.get("evict_verified", False),
        )
        self.drive = Drive(self.store, config)
        self.store.download_object = self.drive.download
        self.identity_provider = identity
        self.sessions = {}
        self.lock = threading.RLock()
        self.stop = threading.Event()
        self.export_threads = {}
        self.next_drive_health = 0
        self.next_checkpoint = time.time() + 3600
        self.next_cleanup = 0
        cache = self.store.root / "cache"
        if cache.is_dir():
            for path in cache.glob("*.download"):
                path.unlink(missing_ok=True)
        with self.store.db() as db:
            for key, value in db.execute("SELECT key,value FROM settings WHERE key LIKE 'export:%'").fetchall():
                job = json.loads(value)
                if job["state"] == "running":
                    job.update(state="failed", error="服务已重启，请重新导出")
                    db.execute("UPDATE settings SET value=? WHERE key=?", (json.dumps(job), key))

    def identity(self, token):
        if not token or len(token) > 4096:
            raise PermissionError("请登录控制台管理员账号")
        if self.identity_provider:
            user = self.identity_provider(token)
        else:
            gateway = self.config.get("gateway_url", "")
            if not gateway.startswith("https://"):
                raise PermissionError("服务端尚未配置 HTTPS 网关身份验证地址")
            request = urllib.request.Request(gateway.rstrip("/") + "/api/user/self", headers={"Authorization": "Bearer " + token})
            try:
                with urllib.request.build_opener(NoRedirect()).open(request, timeout=8) as response:
                    value = json.loads(response.read(65536))
                user = value.get("data") if value.get("success") else None
            except Exception:
                raise PermissionError("控制台身份验证失败或暂时不可用") from None
        if not user or user.get("status", 1) != 1 or user.get("role", 0) < 10:
            raise PermissionError("需要有效管理员账号")
        if user["role"] < 100:
            with self.store.db() as db:
                if not db.execute("SELECT 1 FROM grants WHERE user_id=?", (user["id"],)).fetchone():
                    raise PermissionError("尚未获得封存访问权限")
        return {key: user[key] for key in ("id", "role", "username")}

    def session(self, handler):
        cookies = SimpleCookie()
        cookies.load(handler.headers.get("Cookie", ""))
        sid = cookies.get("omni_archive_v3")
        key = sid.value if sid else ""
        with self.lock:
            session = self.sessions.get(key)
        if not session or session["expires"] < time.time():
            raise PermissionError("登录已过期，请重新验证管理员身份")
        user = self.identity(session["token"])
        return key, user

    def worker(self):
        while not self.stop.wait(1):
            try:
                self.store.finish_index()
                self.store.seal()
                with self.lock:
                    checkpoint = self.next_checkpoint <= time.time()
                    if checkpoint:
                        self.next_checkpoint = time.time() + 86400
                    cleanup = self.next_cleanup <= time.time()
                    if cleanup:
                        self.next_cleanup = time.time() + 15
                if cleanup:
                    self.store.cleanup_verified()
                if checkpoint:
                    self.store.checkpoint()
                status = self.drive.status()
                if not status["connected"] or status["error"] in {"invalid_grant", "needs_reauthorization", "archive_root_missing"}:
                    continue
                with self.lock:
                    health = self.next_drive_health <= time.time()
                    if health:
                        self.next_drive_health = time.time() + 900
                if health:
                    try:
                        self.drive.health()
                    except DriveError as error:
                        self.store.setting("drive_error", error.reason)
                        continue
                item = self.store.claim()
                if not item:
                    continue
                try:
                    self.drive.upload(item)
                except Exception as error:
                    reason = error.reason if isinstance(error, DriveError) else "upload_network_or_disk_error"
                    blocked = reason in {"invalid_grant", "needs_reauthorization", "storageQuotaExceeded", "remote_integrity_error", "local_integrity_error", "archive_root_missing"} or isinstance(error, DriveError) and error.status in {401, 403, 404, 422} and reason not in {"rateLimitExceeded", "userRateLimitExceeded"}
                    attempts = item["attempts"] + 1
                    self.store.mark(item["id"], state="blocked" if blocked else "retry", lease=0, error=reason, attempts=attempts, next_try=time.time() + min(300, 2**min(attempts, 8)) + secrets.randbelow(1000) / 1000)
                    self.store.setting("drive_error", reason)
            except Exception:
                self.store.setting("worker_error", "归档整理暂时失败，请检查磁盘和服务状态")

    def create_export(self, user, spec):
        with self.store.lock:
            self.store.ensure_capacity(1024**2)
        jid = uuid.uuid4().hex
        state = {"id": jid, "actor": user["id"], "state": "running", "created": time.time(), "count": 0, "format": spec.get("format", "html"), "cancel": False}
        if state["format"] not in {"html", "json", "markdown"}:
            raise ValueError("导出格式无效")
        self.store.setting("export:" + jid, state)
        self.store.audit(user["id"], "export_start", jid)
        thread = threading.Thread(target=self.run_export, args=(jid, spec), daemon=True)
        thread.start()
        self.export_threads[jid] = thread
        return state

    def run_export(self, jid, spec):
        state = self.store.setting("export:" + jid)
        path = self.store.root / "exports" / (jid + ".zip")
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        temp = path.with_suffix(".partial")
        try:
            filters = {k: str(v) for k, v in spec.get("filters", {}).items() if k in {"user", "project", "conversation", "q", "local_q", "status", "model", "attachment", "abnormal", "start", "end"}}
            filters["limit"] = "200"
            selected = set(spec.get("request_ids", []))
            entries = []
            with zipfile.ZipFile(temp, "w", zipfile.ZIP_DEFLATED, allowZip64=True) as archive:
                while True:
                    batch = self.store.records(filters)
                    for row in batch["records"]:
                        if self.store.setting("export-cancel:" + jid):
                            raise InterruptedError("导出已取消")
                        if selected and row["id"] not in selected:
                            continue
                        detail = self.store.detail(row["id"])
                        prefix = f"user-{row['user_id']}/requests/{row['id']}"
                        archive.writestr(prefix + "/record.json", encode(detail))
                        with archive.open(prefix + "/events.jsonl", "w") as stream:
                            for raw in self.store.raw_events(row["id"]):
                                stream.write(raw + b"\n")
                        transcript = messages(detail["payloads"])
                        if state["format"] == "markdown":
                            archive.writestr(prefix + "/conversation.md", "\n\n".join("## " + role + "\n\n" + body for role, body in transcript))
                        if state["format"] == "html":
                            body = "".join("<article><h2>" + html.escape(role) + "</h2><pre>" + html.escape(text) + "</pre></article>" for role, text in transcript)
                            archive.writestr(prefix + "/conversation.html", '<!doctype html><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src \'none\'; style-src \'unsafe-inline\'"><title>对话封存</title><style>body{font:16px/1.7 system-ui;max-width:900px;margin:48px auto;padding:24px;color:#202124}pre{white-space:pre-wrap;overflow-wrap:anywhere}article{border-bottom:1px solid #ddd;padding:20px 0}</style>' + body)
                        for attachment in detail["attachments"]:
                            if attachment["object_id"]:
                                with self.store.object_file(attachment["object_id"]) as source:
                                    archive.write(source, prefix + "/attachments/" + attachment["id"])
                        entries.append({"id": row["id"], "user_id": row["user_id"], "title": row["title"], "path": prefix})
                        state["count"] += 1
                        self.store.setting("export:" + jid, state)
                    if not batch["next_cursor"]:
                        break
                    filters["cursor"] = batch["next_cursor"]
                archive.writestr("index.json", encode(entries))
                archive.writestr("README.txt", "明文对话封存导出。record.json 含可读协议，events.jsonl 含可无损还原的事件。外链附件仅保存引用；附件原始文件名见 record.json。")
            if self.store.setting("export-cancel:" + jid):
                raise InterruptedError("导出已取消")
            os.replace(temp, path)
            os.chmod(path, 0o600)
            state.update(state="ready", bytes=path.stat().st_size)
        except InterruptedError:
            state["state"] = "cancelled"
        except Exception:
            state.update(state="failed", error="导出失败，原始档案未被修改")
        finally:
            temp.unlink(missing_ok=True)
            self.store.setting("export:" + jid, state)


def handler_for(app):
    class Handler(BaseHTTPRequestHandler):
        server_version = "OmniArchive/3"
        def log_message(self, *args):
            pass  # Never log bodies, cookies, OAuth codes, or query strings.

        def respond(self, status, value, mime="application/json", extra=None):
            data = encode(value) if mime == "application/json" else value
            self.send_response(status)
            self.send_header("Content-Type", mime + ("; charset=utf-8" if mime.startswith("text/") or mime == "application/json" else ""))
            self.send_header("Content-Length", str(len(data)))
            self.send_header("Cache-Control", "no-store")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.send_header("Referrer-Policy", "no-referrer")
            self.send_header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; connect-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'self'")
            for key, val in (extra or {}).items():
                self.send_header(key, val)
            self.end_headers()
            self.wfile.write(data)

        def body(self, limit=2 * 1024**2):
            size = int(self.headers.get("Content-Length", 0))
            if size < 0 or size > limit:
                raise ValueError("请求体过大")
            return self.rfile.read(size)

        def require_root(self, user):
            if user["role"] < 100:
                raise PermissionError("只有站长可以执行此操作")

        def file(self, path, name, inline=False, mime="application/octet-stream"):
            size = path.stat().st_size
            self.send_response(200)
            self.send_header("Content-Type", mime)
            self.send_header("Content-Length", str(size))
            self.send_header("Content-Disposition", ("inline" if inline else "attachment") + "; filename*=UTF-8''" + urllib.parse.quote(name, safe=""))
            self.send_header("Cache-Control", "no-store")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.send_header("Content-Security-Policy", "default-src 'none'; sandbox")
            self.end_headers()
            with path.open("rb") as stream:
                while chunk := stream.read(1024**2):
                    self.wfile.write(chunk)

        def dispatch(self):
            parsed = urllib.parse.urlsplit(self.path)
            path = parsed.path
            if path.startswith("/archive-v3/"):
                path = path[len("/archive-v3"):]
            query = {k: v[0] for k, v in urllib.parse.parse_qs(parsed.query, keep_blank_values=True).items()}
            post = self.command == "POST"
            if path == "/healthz":
                return self.respond(200, {"ok": True, "version": 3})
            if path.startswith("/internal/"):
                if not app.config.get("ingest_token") or not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + app.config["ingest_token"]):
                    raise PermissionError("采集凭据无效")
                if post and path == "/internal/events":
                    return self.respond(200, {"hash": app.store.ingest(self.body(), defer_index=True)})
                if path == "/internal/metrics":
                    return self.respond(200, app.store.metrics())
                raise KeyError("Unknown internal endpoint")
            if path == "/api/public-config" and not post:
                return self.respond(200, {"demo": bool(app.config.get("demo"))})
            if not path.startswith("/api/"):
                relative = path.lstrip("/") or "index.html"
                target = (HERE / "web" / "dist" / relative).resolve()
                root = (HERE / "web" / "dist").resolve()
                if not target.is_relative_to(root) or not target.is_file():
                    raise KeyError("页面尚未构建或不存在")
                return self.respond(200, target.read_bytes(), mimetypes.guess_type(target)[0] or "application/octet-stream")
            if post:
                origin = self.headers.get("Origin")
                if origin and origin != app.config["public_origin"]:
                    raise PermissionError("Origin mismatch")
                if self.headers.get("X-Archive-Request") != "1":
                    raise PermissionError("缺少请求校验标记")
            if path == "/api/session" and post:
                token = json.loads(self.body()).get("token", "")
                user = app.identity(token)
                sid = secrets.token_urlsafe(32)
                with app.lock:
                    app.sessions = {k: v for k, v in app.sessions.items() if v["expires"] > time.time()}
                    app.sessions[sid] = {"token": token, "expires": time.time() + 1800}
                cookie = f"omni_archive_v3={sid}; HttpOnly; SameSite=Lax; Path=/; Max-Age=1800" + ("; Secure" if not app.config.get("demo") else "")
                return self.respond(200, {"user": user}, extra={"Set-Cookie": cookie})
            sid, user = app.session(self)
            data = json.loads(self.body()) if post else {}
            if path == "/api/session":
                return self.respond(200, {"user": user, "demo": bool(app.config.get("demo"))})
            if path == "/api/logout" and post:
                with app.lock:
                    app.sessions.pop(sid, None)
                return self.respond(200, {"ok": True}, extra={"Set-Cookie": "omni_archive_v3=; HttpOnly; SameSite=Lax; Path=/; Max-Age=0"})
            if path == "/api/directory":
                return self.respond(200, app.store.directory())
            if path == "/api/records":
                return self.respond(200, app.store.records(query))
            if path.startswith("/api/records/"):
                parts = path.split("/")
                rid = parts[3]
                metadata = parts[4:] == ["status"]
                if not metadata:
                    app.store.audit(user["id"], "read", rid)
                detail = app.store.detail(rid, include_payloads=not metadata)
                if detail["manifest"]:
                    detail["drive_folder_id"] = app.drive.conversation_for(detail["manifest"]["path"])
                return self.respond(200, detail)
            if path == "/api/stats":
                return self.respond(200, app.store.metrics())
            if path == "/api/drive/status":
                return self.respond(200, app.drive.status())
            if path == "/api/drive/connect" and post:
                self.require_root(user)
                return self.respond(200, {"url": app.drive.start(sid)})
            if path == "/api/oauth/callback":
                self.require_root(user)
                if "error" in query:
                    raise ValueError("Google 授权已取消，请返回工作台重试")
                app.drive.finish(query["state"], query["code"], sid)
                app.store.audit(user["id"], "google_connected")
                return self.respond(200, "<!doctype html><meta charset=utf-8><title>授权完成</title><p>Google Drive 已连接，可以关闭此窗口并返回封存工作台。</p>".encode(), "text/html")
            if path == "/api/drive/refresh" and post:
                self.require_root(user)
                app.drive.health(force=True)
                return self.respond(200, app.drive.status())
            if path == "/api/sync" and post:
                self.require_root(user)
                sealed = app.store.seal(force=True)
                app.store.retry()
                return self.respond(200, {"sealed": sealed})
            if path == "/api/queue":
                with app.store.db() as db:
                    rows = [dict(r) for r in db.execute("SELECT id,user_id,path,size,kind,state,error,attempts,created,verified FROM objects ORDER BY created DESC LIMIT 200")]
                return self.respond(200, rows)
            if path == "/api/group" and post:
                app.store.group(data["request_ids"], str(data.get("project", "")), str(data.get("conversation", "")))
                app.store.audit(user["id"], "group", ",".join(data["request_ids"]))
                return self.respond(200, {"ok": True})
            if path == "/api/user-name" and post:
                app.store.rename_user(int(data["id"]), str(data["name"]))
                app.store.audit(user["id"], "user_name", data["id"])
                return self.respond(200, {"ok": True})
            if path == "/api/collections" and post:
                uid = int(data["user_id"])
                app.store.collection(uid, str(data.get("project","")), str(data.get("conversation","")), str(data.get("title","")))
                app.store.audit(user["id"], "collection_created", uid)
                return self.respond(200, {"ok":True})
            if path.startswith("/api/attachments/"):
                aid = path.rsplit("/", 1)[-1]
                with app.store.db() as db:
                    row = db.execute("SELECT a.*,o.path,o.hash FROM attachments a JOIN records r ON r.id=a.request_id LEFT JOIN objects o ON o.id=a.object_id WHERE a.id=? AND r.deleted=0", (aid,)).fetchone()
                if not row or not row["path"]:
                    raise KeyError("只保存了外部引用，未取得文件")
                app.store.audit(user["id"], "download_attachment", aid)
                inline = query.get("preview") == "1" and row["mime"] in {"image/png", "image/jpeg", "image/gif", "image/webp"}
                with app.store.object_file(row["object_id"]) as source:
                    return self.file(source, row["name"], inline, row["mime"] if inline else "application/octet-stream")
            if path == "/api/exports":
                if post:
                    return self.respond(202, app.create_export(user, data))
                with app.store.db() as db:
                    jobs = [json.loads(r[0]) for r in db.execute("SELECT value FROM settings WHERE key LIKE 'export:%' ORDER BY rowid DESC LIMIT 100")]
                return self.respond(200, [j for j in jobs if j["actor"] == user["id"] or user["role"] >= 100])
            if path.startswith("/api/exports/"):
                parts = path.split("/")
                jid = parts[3]
                job = app.store.setting("export:" + jid)
                if not re.fullmatch("[a-f0-9]{32}", jid) or not job or job["actor"] != user["id"] and user["role"] < 100:
                    raise KeyError("导出任务不存在")
                if post:
                    if job["state"] == "running":
                        app.store.setting("export-cancel:" + jid, True)
                    return self.respond(200, job)
                if len(parts) == 5 and parts[4] == "download" and job["state"] == "ready":
                    app.store.audit(user["id"], "export_download", jid)
                    return self.file(app.store.root / "exports" / (jid + ".zip"), "对话封存-" + jid[:8] + ".zip")
                return self.respond(200, job)
            if path == "/api/settings":
                if post:
                    self.require_root(user)
                    name = str(data.get("workspace_name", "对话封存"))[:40]
                    app.store.setting("workspace_name", name)
                retention = "Drive 校验通过后自动清理本地文件，读取时按需取回" if app.store.evict_verified else "本地和 Drive 均不自动删除"
                return self.respond(200, {"workspace_name": app.store.setting("workspace_name") or "对话封存", "capture_format": "plaintext-v3", "retention": retention, "max_local_bytes": app.store.max_local_bytes, "oauth_callback": app.config.get("google_redirect_uri", ""), "root_directory": "OmniAPI-Archive-v3 / user-用户ID / projects / conversations"})
            if path == "/api/grants":
                self.require_root(user)
                with app.store.db() as db:
                    if post:
                        uid = int(data["user_id"])
                        if data.get("allowed"):
                            db.execute("INSERT OR IGNORE INTO grants VALUES(?)", (uid,))
                        else:
                            db.execute("DELETE FROM grants WHERE user_id=?", (uid,))
                    rows = [r[0] for r in db.execute("SELECT user_id FROM grants")]
                if post:
                    app.store.audit(user["id"], "grant_changed", uid)
                return self.respond(200, rows)
            if path == "/api/audits":
                self.require_root(user)
                with app.store.db() as db:
                    rows = [dict(r) for r in db.execute("SELECT * FROM audits ORDER BY created DESC LIMIT 200")]
                return self.respond(200, rows)
            raise KeyError("接口不存在")

        def run(self):
            try:
                self.dispatch()
            except PermissionError as error:
                self.respond(401, {"error": str(error)})
            except KeyError:
                self.respond(404, {"error": "记录、文件或接口不存在"})
            except DriveError as error:
                self.respond(502, {"error": "Google Drive: " + error.reason})
            except (ValueError, TypeError) as error:
                self.respond(400, {"error": str(error)[:256]})
            except (BrokenPipeError, ConnectionResetError):
                pass
            except Exception:
                self.respond(503, {"error": "归档服务暂时不可用，请检查状态"})

        do_GET = run
        do_POST = run
    return Handler


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--data", required=True)
    parser.add_argument("--config")
    parser.add_argument("--bind", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8793)
    parser.add_argument("--demo", action="store_true")
    args = parser.parse_args()
    config = json.loads(Path(args.config).read_text()) if args.config else {}
    identity = None
    if args.demo:
        if args.bind != "127.0.0.1":
            raise SystemExit("Demo must listen on loopback only")
        config.update(demo=True, public_origin=f"http://127.0.0.1:{args.port}")
        identity = lambda token: {"id": 1, "role": 100, "status": 1, "username": "合成验收管理员"} if token == "archive-v3-local-demo" else None
    if not args.demo:
        origin = urllib.parse.urlsplit(config.get("public_origin", ""))
        if origin.scheme != "https" or not origin.hostname or origin.path or origin.query or origin.fragment or origin.username:
            raise SystemExit("public_origin must be a bare HTTPS origin")
        if not re.fullmatch(r"[0-9a-fA-F]{64}", config.get("ingest_token", "")):
            raise SystemExit("A dedicated 64-hex-character ingest token is required")
        if config.get("google_redirect_uri") and config["google_redirect_uri"] != config["public_origin"] + "/archive-v3/api/oauth/callback":
            raise SystemExit("Google redirect URI must match the HTTPS archive callback")
    app = Application(args.data, config, identity)
    if args.demo:
        from .synthetic import seed
        seed(app.store)
    for _ in range(2):
        threading.Thread(target=app.worker, daemon=True).start()
    server = ThreadingHTTPServer((args.bind, args.port), handler_for(app))
    server.daemon_threads = True
    print(f"Archive v3 listening on {args.bind}:{args.port}; demo={args.demo}", flush=True)
    try:
        server.serve_forever()
    finally:
        app.stop.set()


if __name__ == "__main__":
    main()
