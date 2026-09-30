#!/usr/bin/env python3
"""Portable, standard-library-only operations for the pinned New API deployment."""
import argparse
import datetime as dt
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
IMAGES = {"NEW_API_IMAGE": "calciumion/new-api:v1.0.0-rc.40",
          "POSTGRES_IMAGE": "postgres:15-alpine", "REDIS_IMAGE": "redis:7.4-alpine",
          "NGINX_IMAGE": "nginx:stable-alpine", "PYTHON_IMAGE": "python:3.12-alpine",
          "CERTBOT_IMAGE": "certbot/certbot:latest"}


def run(args, *, cwd=ROOT, stdin=None, output=None):
    environment = os.environ.copy()
    if Path(str(args[0])).name.lower() in ("docker", "docker.exe"):
        # New Desktop installs do not update the already-running shell's PATH;
        # credential helpers live next to docker.exe and must be discoverable.
        environment["PATH"] = str(Path(args[0]).resolve().parent) + os.pathsep + environment.get("PATH", "")
    input_options = {"stdin": stdin} if hasattr(stdin, "read") else {"input": stdin}
    result = subprocess.run([str(a) for a in args], cwd=cwd, **input_options,
                            stdout=output or subprocess.PIPE, stderr=subprocess.PIPE, env=environment)
    if result.returncode:
        # Never echo a command, expanded compose config, or upstream error body.
        raise RuntimeError(f"{Path(str(args[0])).name} failed (exit {result.returncode}); inspect locally with secrets redacted")
    return result.stdout or b""


def protect(path):
    path = Path(path)
    if os.name == "nt":
        identity = run(["whoami"]).decode().strip()
        # Desktop automation may execute as a restricted sandbox account while
        # USERNAME identifies the actual owner. Keep the owner able to read it.
        owner = os.environ.get("USERDOMAIN", ".") + "\\" + os.environ.get("USERNAME", identity)
        suffix = ":(OI)(CI)F" if path.is_dir() else ":F"
        run(["icacls", path, "/inheritance:r", "/grant:r", identity + suffix, owner + suffix])
    else:
        path.chmod(0o700 if path.is_dir() else 0o600)


def write_json(path, value, private=False):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    if private:
        protect(path.parent)
    # newline="\n" keeps generated files byte-identical across Windows and Linux,
    # so "publish locally then sync" does not produce spurious diffs.
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8", newline="\n")
    if private:
        protect(path)


def env_read(root=ROOT):
    path = root / ".env"
    if not path.exists():
        raise RuntimeError("Run init first")
    return dict(line.split("=", 1) for line in path.read_text(encoding="utf-8").splitlines()
                if line and not line.startswith("#"))


def env_write(values, root=ROOT):
    path = root / ".env"
    # Create restrictively before writing credentials.
    path.touch(exist_ok=True)
    protect(path)
    path.write_text("\n".join(f"{k}={v}" for k, v in values.items()) + "\n", encoding="utf-8")


def docker():
    executable = shutil.which("docker")
    if executable:
        return executable
    for base in (Path(os.environ.get("ProgramFiles", "C:/Program Files")) / "Docker/Docker",
                 Path(os.environ.get("LOCALAPPDATA", "")) / "Programs/DockerDesktop"):
        candidate = base / "resources/bin/docker.exe"
        if candidate.exists():
            return str(candidate)
    raise RuntimeError("Docker Desktop CLI not found; install/start Docker Desktop")


class Stack:
    def __init__(self, root=ROOT, extra=None):
        self.root = Path(root)
        self.env = env_read(self.root)
        configured = self.env.get("COMPOSE_OVERLAY", "").strip()
        self.extra = extra or (self.root / configured if configured else None)

    def compose(self, *args, stdin=None, output=None):
        cmd = [docker(), "compose", "--project-directory", self.root,
               "--env-file", self.root / ".env", "-f", self.root / "docker-compose.yml"]
        if self.extra:
            cmd += ["-f", self.extra]
        return run(cmd + list(args), cwd=self.root, stdin=stdin, output=output)

    @property
    def url(self):
        public = self.env.get("GATEWAY_PUBLIC_URL", "").strip()
        return public.rstrip("/") if public else "http://127.0.0.1:" + self.env.get("GATEWAY_PORT", "80")

    def require_locked(self):
        for key in IMAGES:
            if not re.fullmatch(r"[\w./:-]+@sha256:[0-9a-f]{64}", self.env.get(key, "")):
                raise RuntimeError(f"{key} is not digest-pinned; run lock-images")
        for key in ("DB_PASSWORD", "REDIS_PASSWORD", "SESSION_SECRET", "CRYPTO_SECRET"):
            if not re.fullmatch(r"[a-f0-9]{64}", self.env.get(key, "")):
                raise RuntimeError(f"{key} must be a generated 256-bit hexadecimal secret")

    def start(self):
        self.require_locked()
        self.compose("config", "--quiet")
        self.compose("up", "-d", "--wait", "--wait-timeout", "240")
        print("Gateway ready: " + self.url, flush=True)


class API:
    def __init__(self, base):
        self.base = base.rstrip("/")
        self.user_id = None
        self.access_token = None
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
                          urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(self, path, body=None, method=None, token=None):
        headers = {"Content-Type": "application/json"}
        # v0.13.2 used a session cookie plus New-Api-User. rc.40 returns a
        # short-lived dashboard Bearer token. Keep the old header only as a
        # compatibility fallback for restoring old backups.
        if self.user_id is not None and self.access_token is None:
            headers["New-Api-User"] = str(self.user_id)
        authorization = token or self.access_token
        if authorization:
            headers["Authorization"] = "Bearer " + authorization
        req = urllib.request.Request(self.base + path, data=None if body is None else json.dumps(body).encode(),
                                     headers=headers, method=method or ("POST" if body is not None else "GET"))
        try:
            with self.opener.open(req, timeout=630) as response:
                data = response.read()
                return response.status, json.loads(data), dict(response.headers)
        except urllib.error.HTTPError as error:
            # Error contents can contain upstream secrets or user prompts.
            return error.code, {"success": False}, dict(error.headers)

    def ok(self, path, body=None, method=None):
        status, data, _ = self.request(path, body, method)
        if status != 200 or data.get("success") is False:
            # The gateway answers a rejected login with HTTP 200 and a message
            # ("Turnstile token 为空"), so passing that message through is what
            # makes a failure diagnosable instead of "check admin UI".
            detail = str(data.get("message") or "").strip()
            raise RuntimeError("Gateway API rejected %s (HTTP %s)%s; check admin UI"
                               % (path.split("?")[0], status,
                                  (": " + detail[:200]) if detail else ""))
        return data.get("data", data)

    def login(self, credentials):
        try:
            data = self.ok("/api/user/login", credentials)
        except RuntimeError as error:
            if "Turnstile" in str(error):
                raise RuntimeError(
                    "登录接口受 Cloudflare Turnstile 保护，脚本无法代替人机验证。"
                    "请在浏览器登录控制台后改用控制台身份（管理页会用 "
                    "/api/user/auth/refresh 取得令牌）。") from None
            raise
        self.access_token = data.get("access_token") if isinstance(data, dict) else None
        user = data.get("user", data) if isinstance(data, dict) else data
        self.user_id = user["id"]
        return data

    def options(self):
        return {x["key"]: x["value"] for x in self.ok("/api/option/")}

    def option(self, key, value):
        self.ok("/api/option/", {"key": key, "value": str(value)}, "PUT")


def init(root=ROOT, project="ai-gateway", port=80):
    root.mkdir(parents=True, exist_ok=True)
    if not (root / ".env").exists():
        env_write({"COMPOSE_PROJECT_NAME": project, "GATEWAY_PORT": str(port), "GATEWAY_HOST": "127.0.0.1",
                   **{key: secrets.token_hex(32) for key in
                      ("DB_PASSWORD", "REDIS_PASSWORD", "SESSION_SECRET", "CRYPTO_SECRET")}, **IMAGES}, root)
    private = root / ".secrets"
    private.mkdir(exist_ok=True)
    protect(private)
    for name, username in (("admin", "gw_operator"), ("tester", "gw_tester")):
        file = private / f"{name}.json"
        if not file.exists():
            # v0.13.2 model.User requires passwords between 8 and 20 characters.
            write_json(file, {"username": username, "password": secrets.token_urlsafe(15)}, True)
    print("Local secrets initialized; values omitted", flush=True)


def lock_images():
    values = env_read()
    records = {}
    for key, source in IMAGES.items():
        # Existing immutable locks are reused; upgrades must deliberately clear locks.
        selected = values.get(key, source)
        print("Pulling " + key + " ...", flush=True)
        run([docker(), "pull", selected])
        info = json.loads(run([docker(), "image", "inspect", selected]))[0]
        digests = info.get("RepoDigests", [])
        if not digests:
            raise RuntimeError("Registry returned no digest for " + key)
        values[key] = digests[0]
        records[key] = {"source": source, "image": digests[0], "id": info["Id"]}
    env_write(values)
    version = IMAGES["NEW_API_IMAGE"].rsplit(":", 1)[-1]
    write_json(ROOT / "images.lock.json", {"new_api_version": version, "images": records})
    print("All image digests locked", flush=True)


def credentials(root, name):
    return json.loads((root / ".secrets" / f"{name}.json").read_text(encoding="utf-8"))


def bootstrap(stack):
    api = API(stack.url)
    setup = api.ok("/api/setup")
    if not setup["status"]:
        if setup.get("root_init"):
            raise RuntimeError("Existing root user detected; finish setup manually without replacing credentials")
        admin = credentials(stack.root, "admin")
        api.ok("/api/setup", {**admin, "confirmPassword": admin["password"],
                             "SelfUseModeEnabled": False, "DemoSiteEnabled": False})
    api.login(credentials(stack.root, "admin"))
    marker = stack.root / ".secrets/bootstrap.json"
    if marker.exists():
        print("Bootstrap already completed; existing balances/options preserved")
        return api
    # DataExportEnabled must stay true. It is not a prompt-logging switch (that
    # misconception turned every dashboard chart blank), it only controls the
    # hourly `quota_data` aggregate the charts read from: with it off the table
    # stays empty forever while `logs` fills up, so 用量统计 shows nothing on both
    # the admin and the user side. It stores no request content.
    for key, value in {"SystemName": "OmniAPI", "Logo": "/brand/omniapi.svg", "RetryTimes": "0", "RegisterEnabled": "true",
                       "PasswordRegisterEnabled": "true", "LogConsumeEnabled": "true",
                       "DataExportEnabled": "true", "QuotaForNewUser": "370430",
                       "QuotaForInviter": "1333551", "QuotaForInvitee": "0"}.items():
        api.option(key, value)
    tester = credentials(stack.root, "tester")
    users = api.ok("/api/user/search?keyword=" + tester["username"])
    users = users.get("items", []) if isinstance(users, dict) else users
    if not any(u["username"] == tester["username"] for u in users):
        api.ok("/api/user/", {**tester, "display_name": "Gateway Test User", "role": 1})
    user = API(stack.url)
    data = user.login(tester)
    # Set only on first bootstrap; reruns do not add free credit.
    api.ok("/api/user/manage", {"id": data["id"], "action": "add_quota", "mode": "override", "value": 500000})
    # v0.13.2 override writes SQL but does not invalidate Redis. Reapply the
    # intended enabled state through the native API, which clears user caches.
    api.ok("/api/user/manage", {"id": data["id"], "action": "enable"})
    tokens = user.ok("/api/token/")
    tokens = tokens.get("items", []) if isinstance(tokens, dict) else tokens
    token = next((t for t in tokens if t["name"] == "gateway-smoke-test"), None)
    if token is None:
        user.ok("/api/token/", {"name": "gateway-smoke-test", "remain_quota": 50000,
                               "expired_time": -1, "unlimited_quota": False, "group": "default"})
        result = user.ok("/api/token/")
        tokens = result.get("items", []) if isinstance(result, dict) else result
        token = next(t for t in tokens if t["name"] == "gateway-smoke-test")
    key = user.ok(f"/api/token/{token['id']}/key", {})["key"]
    if not key.startswith("sk-"):
        key = "sk-" + key
    write_json(stack.root / ".secrets/test-token.json", {"id": token["id"], "key": key}, True)
    write_json(marker, {"user_id": data["id"], "created_at": dt.datetime.now(dt.timezone.utc).isoformat()}, True)
    print("Admin, limited test user and Token initialized; enter Provider Key in the admin UI")
    return api


def configure_model(stack, model):
    api = API(stack.url)
    api.login(credentials(stack.root, "admin"))
    options = api.options()
    # 0.5 quota/input token, output multiplier 2 => $1 / $2 per million at 500k quota/USD.
    if float(options["QuotaPerUnit"]) != 500000:
        raise RuntimeError("QuotaPerUnit changed; refuse to apply an incorrect test price")
    for option, value in (("ModelRatio", 0.5), ("CompletionRatio", 2), ("GroupRatio", 1)):
        mapping = json.loads(options.get(option) or "{}")
        mapping["default" if option == "GroupRatio" else model] = value
        api.option(option, json.dumps(mapping))
    # A fixed per-request price would take precedence over token ratios.
    mapping = json.loads(options.get("ModelPrice") or "{}")
    mapping.pop(model, None)
    api.option("ModelPrice", json.dumps(mapping))
    # Some v0.13.2 model prefixes ignore CompletionRatio overrides. Native
    # expression billing makes the advertised test price authoritative.
    for option, value in (("billing_setting.billing_expr", "v1:p * 1 + c * 2"),
                          ("billing_setting.billing_mode", "tiered_expr")):
        mapping = json.loads(options.get(option) or "{}")
        mapping[model] = value
        api.option(option, json.dumps(mapping))
    write_json(stack.root / "runtime/test-model.json", {"model": model, "input_usd_per_million": 1,
                                                       "output_usd_per_million": 2, "test_only": True})
    print("Applied TEST prices to selected model only")


def health(stack):
    results = {}
    def containers():
        raw = stack.compose("ps", "--format", "json").decode()
        rows = json.loads(raw) if raw.lstrip().startswith("[") else [json.loads(line) for line in raw.splitlines() if line]
        by_service = {r["Service"]: r for r in rows}
        if any(name not in by_service or by_service[name]["State"] != "running" or
               by_service[name].get("Health") != "healthy" for name in ("database", "redis", "new-api", "nginx")):
            raise RuntimeError("A required container is missing, stopped or unhealthy")
        return [{k: r.get(k) for k in ("Service", "State", "Health", "Ports")} for r in rows]
    for name, action in {
        "containers": containers,
        "application": lambda: API(stack.url).ok("/api/status").get("version", "reachable"),
        "database": lambda: stack.compose("exec", "-T", "database", "pg_isready", "-U", "gateway", "-d", "gateway").decode().strip(),
        "redis": lambda: stack.compose("exec", "-T", "redis", "sh", "-c", 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli ping').decode().strip(),
        "docker_linux_resources": lambda: stack.compose("exec", "-T", "redis", "sh", "-c", "df -h /data; head -n 3 /proc/meminfo").decode(),
        "container_memory": lambda: run([docker(), "stats", "--no-stream", "--format", "{{.Name}} {{.MemUsage}} {{.MemPerc}}"]).decode(),
    }.items():
        try:
            results[name] = {"status": "PASS", "detail": action()}
        except Exception as exc:
            results[name] = {"status": "FAIL", "detail": str(exc)}
    disk = shutil.disk_usage(stack.root)
    results["host_disk"] = {"free_bytes": disk.free, "total_bytes": disk.total}
    if os.name == "nt":
        try:
            results["windows_memory"] = run(["powershell", "-NoProfile", "-Command",
                "Get-CimInstance Win32_OperatingSystem | Select-Object FreePhysicalMemory,TotalVisibleMemorySize | ConvertTo-Json -Compress"]).decode().strip()
        except Exception:
            results["windows_memory"] = "BLOCKED: host permissions"
    write_json(stack.root / "runtime/health.json", results)
    for name, result in results.items():
        print(name + ": " + (result.get("status", "RECORDED") if isinstance(result, dict) else "RECORDED"))
    if any(isinstance(v, dict) and v.get("status") == "FAIL" for v in results.values()):
        raise RuntimeError("One or more health checks failed")


def checksum(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def backup(stack):
    stack.require_locked()
    base = stack.root / "backups"
    base.mkdir(exist_ok=True)
    protect(base)
    dest = base / dt.datetime.now().strftime("%Y-%m-%d_%H-%M-%S")
    dest.mkdir()  # Fail on collision, never merge backups.
    protect(dest)
    stopped = False
    try:
        print("Draining Nginx (up to 620 seconds); no new requests accepted", flush=True)
        stack.compose("stop", "-t", "620", "nginx")
        stopped = True
        nginx_id = stack.compose("ps", "-a", "-q", "nginx").decode().strip()
        state = json.loads(run([docker(), "inspect", nginx_id]))[0]["State"]
        if state["ExitCode"] != 0:
            raise RuntimeError("Proxy did not drain cleanly; refusing a backup with uncertain in-flight requests")
        stack.compose("stop", "-t", "620", "new-api")
        with (dest / "database.dump").open("wb") as output:
            stack.compose("exec", "-T", "database", "pg_dump", "-U", "gateway", "-d", "gateway", "-Fc", output=output)
        container = stack.compose("ps", "-a", "-q", "new-api").decode().strip()
        # docker cp works on stopped containers and streams a tar archive without host tar dependencies.
        with (dest / "data.tar").open("wb") as output:
            run([docker(), "cp", container + ":/data/.", "-"], output=output)
        bundle = dest / "bundle"
        bundle.mkdir()
        for file in ("docker-compose.yml", ".env", "images.lock.json", "compose.https.yml", "compose.acme.yml"):
            source = stack.root / file
            if source.exists():
                shutil.copy2(source, bundle / file)
        shutil.copytree(stack.root / "nginx", bundle / "nginx")
        shutil.copytree(stack.root / "scripts", bundle / "scripts")
        if (stack.root / "config").exists():
            shutil.copytree(stack.root / "config", bundle / "config")
        if (stack.root / "ops").exists():
            shutil.copytree(stack.root / "ops", bundle / "ops")
        # Financial audit/idempotency state is part of a recoverable backup.
        # Copy only the known private files; transient scans and test artifacts
        # under runtime/ must never enter a production backup by accident.
        runtime_bundle = bundle / "runtime"
        runtime_bundle.mkdir()
        for name in ("topup-ledger.json", "topup-requests.jsonl",
                     "topup-quotes.jsonl", "payment-settings.json",
                     "payment-settings-audit.json", "fx-rates.json",
                     "admin-session-state.json"):
            source = stack.root / "runtime" / name
            if source.is_file() and not source.is_symlink():
                shutil.copy2(source, runtime_bundle / name)
        assets = stack.root / "runtime" / "payment-assets"
        if assets.is_dir() and not assets.is_symlink():
            shutil.copytree(assets, runtime_bundle / "payment-assets")
        shutil.copytree(stack.root / ".secrets", bundle / ".secrets")
        if (stack.root / "certbot").exists():
            shutil.copytree(stack.root / "certbot", bundle / "certbot", symlinks=True)
        lock = json.loads((stack.root / "images.lock.json").read_text(encoding="utf-8"))
        manifest = {"new_api_version": lock.get("new_api_version", "unknown"),
                    "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                    "files": {str(p.relative_to(dest)).replace("\\", "/"): checksum(p)
                              for p in dest.rglob("*") if p.is_file() and not p.is_symlink()},
                    "symlinks": {str(p.relative_to(dest)).replace("\\", "/"): os.readlink(p)
                                 for p in dest.rglob("*") if p.is_symlink()}}
        write_json(dest / "manifest.json", manifest)
        print("Backup complete: " + str(dest), flush=True)
        return dest
    finally:
        if stopped:
            stack.compose("up", "-d", "--wait", "--wait-timeout", "240")


def validate_backup(dest):
    dest = Path(dest).resolve()
    manifest = json.loads((dest / "manifest.json").read_text(encoding="utf-8"))
    for name, expected in manifest["files"].items():
        path = (dest / name).resolve()
        if not path.is_relative_to(dest) or path.is_symlink() or checksum(path) != expected:
            raise RuntimeError("Backup integrity check failed")
    for name, expected in manifest.get("symlinks", {}).items():
        path = (dest / name)
        if not path.is_symlink() or os.readlink(path) != expected or not path.resolve().is_relative_to(dest):
            raise RuntimeError("Backup symlink integrity check failed")
    for name in ("database.dump", "data.tar", "bundle/.env", "bundle/docker-compose.yml"):
        if name not in manifest["files"]:
            raise RuntimeError("Incomplete backup")
    return dest


def restore(source, port):
    source = validate_backup(source)
    project = "gw-restore-" + dt.datetime.now().strftime("%Y%m%d%H%M%S")
    dest = ROOT / "runtime" / project
    dest.mkdir(parents=True)
    protect(dest)
    shutil.copytree(source / "bundle", dest, dirs_exist_ok=True)
    values = env_read(dest)
    values.update(COMPOSE_PROJECT_NAME=project, GATEWAY_PORT=str(port),
                  GATEWAY_HOST="127.0.0.1", GATEWAY_PUBLIC_URL="", COMPOSE_OVERLAY="")
    env_write(values, dest)
    stack = Stack(dest)
    stack.require_locked()
    stack.compose("up", "-d", "--wait", "database", "redis")
    # Binary input is read by Python, not PowerShell text redirection.
    with (source / "database.dump").open("rb") as file:
        stack.compose("exec", "-T", "database", "pg_restore", "-U", "gateway", "-d", "gateway", "--exit-on-error", stdin=file)
    stack.compose("create", "new-api")
    container = stack.compose("ps", "-a", "-q", "new-api").decode().strip()
    with (source / "data.tar").open("rb") as file:
        run([docker(), "cp", "-", container + ":/data"], stdin=file)
    stack.start()
    api = API(stack.url)
    api.login(credentials(dest, "admin"))
    snapshot = {"users": api.ok("/api/user/?p=1&page_size=100"),
                "channels": api.ok("/api/channel/?p=1&page_size=100")}
    # Avoid storing channel responses, which can change their masking behavior across versions.
    write_json(dest / "restore-result.json", {"status": "PASS", "admin_login": True,
                  "user_records_readable": bool(snapshot["users"]), "channel_records_readable": bool(snapshot["channels"]),
                  "url": stack.url, "project": project})
    print("Restore started in isolated project: " + project + " at " + stack.url)
    return stack


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["init", "lock-images", "start", "stop", "restart", "bootstrap", "healthcheck", "backup", "restore", "test-price"])
    parser.add_argument("--backup")
    parser.add_argument("--port", type=int, default=18080)
    parser.add_argument("--model")
    args = parser.parse_args()
    if args.action == "init":
        init()
    elif args.action == "lock-images":
        lock_images()
    elif args.action == "restore":
        if not args.backup:
            parser.error("restore requires --backup PATH")
        restore(args.backup, args.port)
    else:
        stack = Stack()
        if args.action == "start":
            stack.start()
        elif args.action == "stop":
            stack.compose("stop", "-t", "620")
        elif args.action == "restart":
            stack.compose("stop", "-t", "620", "nginx")
            stack.compose("restart", "new-api")
            stack.start()
        elif args.action == "bootstrap":
            bootstrap(stack)
        elif args.action == "healthcheck":
            health(stack)
        elif args.action == "backup":
            backup(stack)
        elif args.action == "test-price":
            if not args.model:
                parser.error("test-price requires --model MODEL")
            configure_model(stack, args.model)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("ERROR: " + str(error), file=sys.stderr)
        sys.exit(1)
