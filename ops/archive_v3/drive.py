"""Personal My Drive OAuth and resumable uploads. No service-account fallback."""
import base64
import hashlib
import json
import os
import secrets
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from .store import atomic, digest, encode, file_digest


class DriveError(Exception):
    def __init__(self, reason, status=503):
        super().__init__(reason)
        self.reason, self.status = reason, status


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


def http_error(error):
    fallback = "google_http_" + str(error.code)
    try:
        value = json.loads(error.read(16384)).get("error", {})
        reason = value if isinstance(value, str) else (value.get("errors") or [{}])[0].get("reason", fallback)
    except (ValueError, AttributeError, IndexError, TypeError):
        reason = fallback
    return DriveError(reason, error.code)


class Drive:
    def __init__(self, store, config):
        self.store, self.config = store, config
        self.secret_path = store.root / "google-token.json"
        self.pending = {}
        import threading
        self.lock = threading.RLock()
        self.token = json.loads(self.secret_path.read_text()) if self.secret_path.exists() else {}
        self.access = ""
        self.expiry = 0

    @property
    def configured(self):
        return bool(self.config.get("google_client_id") and self.config.get("google_client_secret") and self.config.get("google_redirect_uri"))

    def start(self, owner):
        if not self.configured:
            raise ValueError("请先在服务端配置 Google OAuth 客户端和 HTTPS 回调地址")
        state, verifier = secrets.token_urlsafe(32), secrets.token_urlsafe(64)
        with self.lock:
            now = time.time()
            self.pending = {k: v for k, v in self.pending.items() if v["expires"] > now}
            self.pending[state] = {"owner": owner, "expires": now + 600, "verifier": verifier}
        query = {"client_id": self.config["google_client_id"], "redirect_uri": self.config["google_redirect_uri"], "response_type": "code", "scope": "https://www.googleapis.com/auth/drive.file", "access_type": "offline", "prompt": "consent", "state": state, "code_challenge": base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip("="), "code_challenge_method": "S256"}
        return "https://accounts.google.com/o/oauth2/v2/auth?" + urllib.parse.urlencode(query)

    def token_request(self, values):
        body = urllib.parse.urlencode({**values, "client_id": self.config["google_client_id"], "client_secret": self.config["google_client_secret"]}).encode()
        return self.request("https://oauth2.googleapis.com/token", body, {"Content-Type": "application/x-www-form-urlencoded"}, authorized=False)

    def finish(self, state, code, owner):
        with self.lock:
            pending = self.pending.get(state)
            if not pending or pending["owner"] != owner or pending["expires"] < time.time():
                raise PermissionError("Google 授权状态无效或已过期")
            self.pending.pop(state)
            result = self.token_request({"code": code, "code_verifier": pending["verifier"], "redirect_uri": self.config["google_redirect_uri"], "grant_type": "authorization_code"})
            if not result.get("refresh_token"):
                raise ValueError("Google 未返回离线授权，请重新同意授权")
            previous = self.token, self.access, self.expiry
            self.token = {"refresh_token": result["refresh_token"]}
            self.access, self.expiry = result["access_token"], time.time() + result.get("expires_in", 3600) - 60
            try:
                # Reauthorization must still see the existing archive root.
                self.ensure_root()
                self.health(force=True)
                atomic(self.secret_path, encode(self.token))
            except Exception:
                self.token, self.access, self.expiry = previous
                raise
            self.store.setting("drive_error", "")
            self.store.retry()

    def bearer(self):
        with self.lock:
            if not self.token.get("refresh_token"):
                raise DriveError("needs_reauthorization", 401)
            if time.time() >= self.expiry:
                try:
                    result = self.token_request({"grant_type": "refresh_token", "refresh_token": self.token["refresh_token"]})
                except DriveError as error:
                    if error.reason == "invalid_grant":
                        self.store.setting("drive_error", "needs_reauthorization")
                    raise
                self.access, self.expiry = result["access_token"], time.time() + result.get("expires_in", 3600) - 60
            return self.access

    def request(self, url, body=None, headers=None, method=None, authorized=True, raw=False):
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme != "https" or parsed.hostname not in {"www.googleapis.com", "oauth2.googleapis.com"}:
            raise ValueError("Untrusted Google endpoint")
        headers = dict(headers or {})
        if authorized:
            headers["Authorization"] = "Bearer " + self.bearer()
        request = urllib.request.Request(url, data=body, headers=headers, method=method)
        try:
            with urllib.request.build_opener(NoRedirect()).open(request, timeout=60) as response:
                if raw:
                    return response.read()
                data = response.read(2 * 1024**2)
                return json.loads(data) if data else {}
        except urllib.error.HTTPError as error:
            if error.code == 401:
                self.expiry = 0
            raise http_error(error) from None
        except (OSError, TimeoutError):
            raise DriveError("google_network_error") from None

    def api(self, path, data=None, method=None):
        return self.request("https://www.googleapis.com/drive/v3/" + path, encode(data) if data is not None else None, {"Content-Type": "application/json"}, method)

    def folder(self, parent, name):
        # Names are generated by our application, never used as access checks.
        query = f"'{parent}' in parents and trashed=false and mimeType='application/vnd.google-apps.folder' and name='{name}'"
        found = self.api("files?" + urllib.parse.urlencode({"q": query, "fields": "files(id)", "pageSize": 2}))["files"]
        if found:
            return found[0]["id"]
        return self.api("files?fields=id", {"name": name, "mimeType": "application/vnd.google-apps.folder", "parents": [parent]})["id"]

    def ensure_root(self):
        with self.lock:
            root = self.store.setting("drive_root")
            if root:
                try:
                    meta = self.api(f"files/{root}?fields=id,trashed,mimeType")
                except DriveError as error:
                    if error.status == 404:
                        raise DriveError("archive_root_missing", 404) from None
                    raise
                if meta.get("trashed") or meta.get("mimeType") != "application/vnd.google-apps.folder":
                    raise DriveError("archive_root_missing", 404)
                return root
            root = self.folder("root", "OmniAPI-Archive-v3")
            self.store.setting("drive_root", root)
            return root

    def parent_for(self, relative):
        with self.lock:
            parent = self.ensure_root()
            for component in Path(relative).parts[:-1]:
                cache_key = "folder:" + digest((parent + "/" + component).encode())
                cached = self.store.setting(cache_key)
                if cached:
                    parent = cached
                else:
                    parent = self.folder(parent, component)
                    self.store.setting(cache_key, parent)
            return parent

    def conversation_for(self, relative):
        components = Path(relative).parts[:5]
        parent = self.store.setting("drive_root")
        for component in components:
            if not parent:
                return None
            parent = self.store.setting("folder:" + digest((parent + "/" + component).encode()))
        return parent

    def health(self, force=False):
        cached = self.store.setting("drive_health")
        if not force and cached and time.time() - cached.get("checked", 0) < 900:
            return cached
        result = self.api("about?fields=user(displayName,emailAddress),storageQuota")
        result["checked"] = time.time()
        self.store.setting("drive_health", result)
        return result

    def status(self):
        health = self.store.setting("drive_health") or {}
        return {"configured": self.configured, "connected": bool(self.token.get("refresh_token")), "error": self.store.setting("drive_error") or "", "root_id": self.store.setting("drive_root"), "health": health, "last_sync": self.store.setting("last_sync")}

    def download(self, item, target):
        if item["state"] != "verified" or not item.get("remote_id"):
            raise DriveError("object_not_verified", 422)
        request = urllib.request.Request(
            "https://www.googleapis.com/drive/v3/files/" + urllib.parse.quote(item["remote_id"], safe="") + "?alt=media",
            headers={"Authorization": "Bearer " + self.bearer()},
        )
        checksum = hashlib.sha256()
        size = 0
        try:
            with urllib.request.build_opener(NoRedirect()).open(request, timeout=60) as response, target.open("xb") as stream:
                os.chmod(target, 0o600)
                while chunk := response.read(1024**2):
                    size += len(chunk)
                    if size > item["size"]:
                        raise DriveError("remote_integrity_error", 422)
                    checksum.update(chunk)
                    stream.write(chunk)
            if size != item["size"] or checksum.hexdigest() != item["hash"]:
                raise DriveError("remote_integrity_error", 422)
        except urllib.error.HTTPError as error:
            if error.code == 401:
                self.expiry = 0
            raise http_error(error) from None

    def upload(self, item):
        path = self.store.root / "files" / item["path"]
        if file_digest(path) != item["hash"]:
            raise DriveError("local_integrity_error", 422)
        parent = self.parent_for(item["path"])
        fid = item.get("remote_id")
        if not fid:
            fid = self.api("files/generateIds?count=1&space=drive&type=files")["ids"][0]
            self.store.mark(item["id"], remote_id=fid)
        try:
            remote = self.api(f"files/{fid}?fields=id,size,sha256Checksum,parents,trashed")
        except DriveError as error:
            if error.status != 404:
                raise
            remote = None
        if not remote:
            session = item.get("session")
            if not session:
                content_type = {"segment":"text/plain","manifest":"application/json","catalog":"application/json","control":"application/json"}.get(item["kind"],"application/octet-stream")
                metadata = {"id": fid, "name": path.name, "mimeType": content_type, "parents": [parent], "appProperties": {"archive_version": "3", "object_id": item["id"], "sha256": item["hash"]}}
                request = urllib.request.Request("https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&fields=id,size,sha256Checksum,parents,trashed", data=encode(metadata), headers={"Authorization": "Bearer " + self.bearer(), "Content-Type": "application/json", "X-Upload-Content-Length": str(item["size"]), "X-Upload-Content-Type": content_type}, method="POST")
                try:
                    with urllib.request.build_opener(NoRedirect()).open(request, timeout=60) as response:
                        session = response.headers.get("Location")
                except urllib.error.HTTPError as error:
                    if error.code == 409:
                        raise DriveError("upload_id_conflict_retry", 409) from None
                    if error.code == 401:
                        self.expiry = 0
                    raise http_error(error) from None
                if not session:
                    raise DriveError("upload_session_missing")
                self.store.mark(item["id"], session=session)
            parsed = urllib.parse.urlsplit(session)
            if parsed.scheme != "https" or parsed.hostname != "www.googleapis.com":
                raise DriveError("untrusted_upload_session", 422)
            # Query committed server offset before sending any resumed bytes.
            offset = 0
            probe = urllib.request.Request(session, data=b"", headers={"Authorization": "Bearer " + self.bearer(), "Content-Range": f"bytes */{item['size']}"}, method="PUT")
            try:
                with urllib.request.build_opener(NoRedirect()).open(probe, timeout=60) as response:
                    remote = json.loads(response.read())
            except urllib.error.HTTPError as error:
                if error.code == 308:
                    acknowledged = error.headers.get("Range", "")
                    offset = int(acknowledged.split("-")[-1]) + 1 if acknowledged else 0
                elif error.code in {404, 410}:
                    self.store.mark(item["id"], session=None)
                    raise DriveError("upload_session_expired", 503) from None
                else:
                    if error.code == 401:
                        self.expiry = 0
                    raise http_error(error) from None
            if offset > item["size"]:
                raise DriveError("upload_invalid_offset", 422)
            with path.open("rb") as stream:
                stream.seek(offset)
                while offset < item["size"] and remote is None:
                    chunk = stream.read(8 * 1024**2)
                    end = offset + len(chunk) - 1
                    request = urllib.request.Request(session, data=chunk, headers={"Authorization": "Bearer " + self.bearer(), "Content-Type": "application/octet-stream", "Content-Range": f"bytes {offset}-{end}/{item['size']}"}, method="PUT")
                    try:
                        with urllib.request.build_opener(NoRedirect()).open(request, timeout=60) as response:
                            remote = json.loads(response.read())
                    except urllib.error.HTTPError as error:
                        if error.code in {404, 410}:
                            self.store.mark(item["id"], session=None)
                            raise DriveError("upload_session_expired") from None
                        if error.code != 308:
                            if error.code == 401:
                                self.expiry = 0
                            raise http_error(error) from None
                        server_range = error.headers.get("Range", "")
                        acknowledged = int(server_range.split("-")[-1]) + 1 if server_range else 0
                        if not offset < acknowledged <= end + 1:
                            raise DriveError("upload_not_advancing")
                        end = acknowledged - 1
                        stream.seek(acknowledged)
                    offset = end + 1
                    self.store.mark(item["id"], lease=time.time() + 600)
            remote = self.api(f"files/{fid}?fields=id,size,sha256Checksum,parents,trashed")
        if remote.get("trashed") or parent not in remote.get("parents", []) or int(remote.get("size", -1)) != item["size"]:
            raise DriveError("remote_integrity_error", 422)
        actual = remote.get("sha256Checksum")
        if not actual:
            # Stream fallback checksum; never load an entire archive object.
            request = urllib.request.Request(f"https://www.googleapis.com/drive/v3/files/{fid}?alt=media", headers={"Authorization": "Bearer " + self.bearer()})
            checksum = hashlib.sha256()
            with urllib.request.build_opener(NoRedirect()).open(request, timeout=60) as response:
                while chunk := response.read(1024**2):
                    checksum.update(chunk)
            actual = checksum.hexdigest()
        if actual != item["hash"]:
            raise DriveError("remote_integrity_error", 422)
        self.store.mark(item["id"], state="verified", lease=0, verified=time.time(), error="", session=None)
        self.store.setting("last_sync", time.time())
        self.store.setting("drive_error", "")
