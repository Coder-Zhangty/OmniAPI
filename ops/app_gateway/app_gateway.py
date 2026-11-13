#!/usr/bin/env python3
"""OmniAPI 客户端账号网关：桌面/安卓 App 的登录、会话与同步令牌签发。

设计与取舍：

- **独立进程**（systemd，127.0.0.1:8791），不塞进 new-api：守「默认不改核心源码」。
- 客户端 access_token 是本网关自签的 `omniapp1.` HS256 JWT（独立密钥，与 new-api
  的 dashboard token 完全分离）。客户端用它只查 `/api/user/self`，nginx 按
  Bearer 前缀分流到本网关，由网关回答**登录时从 new-api 抓取的资料快照**。
  快照的实时性见 docs/CLIENT_REMEDIATION_PLAN.md（头像/改名需重新登录）。
- 浏览器授权页（/brand/app-login.html）已登录控制台后，把 new-api 的 15 分钟
  access_token POST 给 /api/app/login/approve；本网关 HTTPS 调用 /api/user/self 验证
  并抓快照。httpOnly 的 refresh cookie 永远不到这里，refresh 由本网关自签自理。
- 同步令牌是 EdDSA JWT（iss=https://omniapi.biz, aud=omniapi-user-sync），与
  ops/sync/server.py 的 verify() 严格对齐（exp 落在 (now, now+65]）。
- 同步资格（end/quota）读 config/app-gateway.json；经营者未定价前用显式默认
  （登录即享 30 天 / 256 MiB），改配置即生效，UI 不做静默兜底。
- 任何额度变更都不在这里发生；这里只发「谁」和「能用多久」。
"""
import base64
import hashlib
import hmac
import json
import os
import re
import secrets
import sqlite3
import sys
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib import request as urlrequest
from urllib.error import HTTPError, URLError

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'ops'))

MAX_BODY = 64 * 1024
REQUEST_TTL = 600          # 登录请求 10 分钟过期（客户端轮询窗口 5 分钟）
ACCESS_TTL = 1800          # omniapp1 access token 30 分钟
REFRESH_TTL = 30 * 86400   # 设备会话 30 天
SYNC_TOKEN_TTL = 60        # 对齐 sync/server.py 的 65 秒上限
IDENTIFIER = re.compile(r'^[A-Za-z0-9_-]{16,80}$')


class IdentityNoRedirect(urlrequest.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Never forward a dashboard credential to a redirect destination.
        return None

# 内存限流桶（1 核 2G，固定窗口够用）：IP 与设备维度各一道。
RATE = {
    'start':   (30, 600),   # 每次打开 App 的登录页都会 start
    # Native clients poll every two seconds for five minutes (up to 150 polls).
    'exchange': (180, 300),
    'refresh':  (60, 300),
    'approve_web': (30, 600),
}


def b64u(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b'=').decode()


def unb64u(value: str) -> bytes:
    return base64.urlsafe_b64decode(value + '=' * (-len(value) % 4))


class GatewayError(Exception):
    def __init__(self, code, status=400):
        self.code, self.status = code, status
        super().__init__(code)


class Store:
    def __init__(self, path):
        self.path = str(path)
        Path(path).parent.mkdir(parents=True, exist_ok=True)
        with self._connect() as db:
            db.executescript('''
            PRAGMA journal_mode=WAL;
            CREATE TABLE IF NOT EXISTS login_request(
                request TEXT PRIMARY KEY,
                challenge TEXT NOT NULL,
                device TEXT NOT NULL,
                platform TEXT NOT NULL,
                status TEXT NOT NULL DEFAULT 'pending',
                user_id INTEGER,
                snapshot TEXT,
                created INTEGER NOT NULL,
                expires_at INTEGER NOT NULL
            );
            CREATE TABLE IF NOT EXISTS device_session(
                sid TEXT PRIMARY KEY,
                user_id INTEGER NOT NULL,
                device TEXT NOT NULL,
                refresh_hash TEXT NOT NULL,
                snapshot TEXT NOT NULL,
                expires_at INTEGER NOT NULL,
                revoked INTEGER NOT NULL DEFAULT 0,
                created INTEGER NOT NULL
            );
            CREATE INDEX IF NOT EXISTS app_session_user ON device_session(user_id);
            CREATE TABLE IF NOT EXISTS web_transfer(
                request TEXT PRIMARY KEY, challenge TEXT NOT NULL,
                native_sid TEXT NOT NULL, user_id INTEGER NOT NULL,
                expires_at INTEGER NOT NULL, consumed INTEGER NOT NULL DEFAULT 0
            );
            ''')

    def _connect(self):
        db = sqlite3.connect(self.path, timeout=15, isolation_level=None)
        db.row_factory = sqlite3.Row
        db.execute('PRAGMA synchronous=FULL')
        return db

    def create_request(self, challenge, device, platform):
        rid = b64u(secrets.token_bytes(18))
        now = int(time.time())
        with self._connect() as db:
            db.execute(
                'INSERT INTO login_request VALUES(?,?,?,?,?,?,?,?,?)',
                (rid, challenge, device, platform, 'pending', None, None, now, now + REQUEST_TTL))
        return rid

    def approve(self, rid, user_id, snapshot):
        with self._connect() as db:
            row = db.execute('SELECT * FROM login_request WHERE request=?', (rid,)).fetchone()
            if row is None or row['status'] != 'pending' or row['expires_at'] < time.time():
                raise GatewayError('REQUEST_NOT_PENDING', 409)
            db.execute(
                'UPDATE login_request SET status=?, user_id=?, snapshot=? WHERE request=?',
                ('approved', user_id, json.dumps(snapshot, separators=(',', ':')), rid))

    def exchange(self, rid, verifier):
        with self._connect() as db:
            db.execute('DELETE FROM login_request WHERE expires_at < ?', (int(time.time()),))
            row = db.execute('SELECT * FROM login_request WHERE request=?', (rid,)).fetchone()
            if row is None:
                raise GatewayError('REQUEST_NOT_PENDING', 409)
            if row['expires_at'] < time.time():
                raise GatewayError('REQUEST_EXPIRED', 409)
            candidates = {verifier, b64u(verifier.encode())}
            matched = any(b64u(hashlib.sha256(c.encode()).digest()) == row['challenge']
                          for c in candidates)
            if not matched:
                raise GatewayError('PKCE_FAILED', 403)
            if row['status'] == 'pending':
                return None
            if row['status'] == 'consumed':
                raise GatewayError('REQUEST_CONSUMED', 409)
            if not row['user_id']:
                raise GatewayError('REQUEST_NOT_PENDING', 409)
            db.execute('UPDATE login_request SET status=? WHERE request=?', ('consumed', rid))
            sid = b64u(secrets.token_bytes(18))
            refresh = b64u(secrets.token_bytes(36))
            now = int(time.time())
            db.execute(
                'INSERT INTO device_session VALUES(?,?,?,?,?,?,0,?)',
                (sid, row['user_id'], row['device'],
                 hashlib.sha256(refresh.encode()).hexdigest(),
                 row['snapshot'] or '{}', now + REFRESH_TTL, now))
            return row, sid, refresh

    def session_by_refresh(self, refresh, sid):
        with self._connect() as db:
            row = db.execute('SELECT * FROM device_session WHERE sid=?', (sid,)).fetchone()
            if (row is None or row['revoked']
                    or row['refresh_hash'] != hashlib.sha256(refresh.encode()).hexdigest()):
                raise GatewayError('SESSION_REVOKED', 401)
            if row['expires_at'] < time.time():
                raise GatewayError('SESSION_EXPIRED', 401)
            return row

    def rotate_refresh(self, sid):
        """refresh 轮换：旧 refresh_token 立即作废，防重放。"""
        refresh = b64u(secrets.token_bytes(36))
        with self._connect() as db:
            db.execute('UPDATE device_session SET refresh_hash=? WHERE sid=?',
                       (hashlib.sha256(refresh.encode()).hexdigest(), sid))
        return refresh

    def revoke(self, sid):
        with self._connect() as db:
            db.execute('UPDATE device_session SET revoked=1 WHERE sid=?', (sid,))

    def session_live(self, user_id, device):
        with self._connect() as db:
            row = db.execute(
                'SELECT 1 FROM device_session WHERE user_id=? AND device=? AND revoked=0 AND expires_at>?',
                (user_id, device, int(time.time()))).fetchone()
            return row is not None


class Gateway:
    def __init__(self, store, config):
        self.store = store
        self.config = config
        self.token_secret = unb64u(config['token_secret_b64u'])
        self.sync_signing = self._load_sync_signing(config)
        self.introspection_secret = config['sync_introspection_secret']
        self.sync_default = config.get('sync_entitlement', {})
        self.buckets = {}
        self.lock = threading.Lock()
        self.cache = {}          # new-api 回环验证缓存 {token_sha: (exp, uid, snapshot)}

    @staticmethod
    def _load_sync_signing(config):
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
        raw = unb64u(config['sync_signing_key_b64u'])
        return Ed25519PrivateKey.from_private_bytes(raw)

    # ---- 限流 -------------------------------------------------------------
    def rate(self, bucket, key):
        limit, window = RATE[bucket]
        now = int(time.time())
        with self.lock:
            entries = self.buckets.setdefault(bucket, {})
            count, start = entries.get(key, (0, now))
            if now - start >= window:
                count, start = 0, now
            count += 1
            entries[key] = (count, start)
            if len(entries) > 5000:
                for k in [k for k, v in entries.items() if now - v[1] >= window]:
                    del entries[k]
            if count > limit:
                raise GatewayError('RATE_LIMITED', 429)

    # ---- omniapp1 token ----------------------------------------------------
    def sign_access(self, user_id, sid):
        now = int(time.time())
        payload = {'uid': user_id, 'sid': sid, 'iat': now, 'exp': now + ACCESS_TTL, 'jti': uuid.uuid4().hex}
        body = b64u(json.dumps(payload, separators=(',', ':')).encode())
        sig = b64u(hmac.new(self.token_secret, body.encode(), hashlib.sha256).digest())
        return f'omniapp1.{body}.{sig}'

    def verify_access(self, raw):
        return self.access_claims(raw)['uid']

    def access_claims(self, raw):
        try:
            head, body, sig = raw.split('.')
            if head != 'omniapp1':
                raise ValueError()
            expected = hmac.new(self.token_secret, body.encode(), hashlib.sha256).digest()
            if not hmac.compare_digest(unb64u(sig), expected):
                raise ValueError()
            payload = json.loads(unb64u(body))
            if payload['exp'] < time.time():
                raise GatewayError('SESSION_EXPIRED', 401)
            with self.store._connect() as db:
                row = db.execute('SELECT user_id, revoked, expires_at FROM device_session WHERE sid=?',
                                 (payload.get('sid'),)).fetchone()
            if row is None or row['user_id'] != payload['uid'] or row['revoked'] or row['expires_at'] <= time.time():
                raise GatewayError('SESSION_REVOKED', 401)
            return payload
        except GatewayError:
            raise
        except Exception:
            raise GatewayError('UNAUTHORIZED', 401) from None

    # ---- new-api 回环（只用于 approve-web 验证与抓快照） -------------------
    def new_api_self(self, bearer):
        key = hashlib.sha256(bearer.encode()).hexdigest()
        hit = self.cache.get(key)
        if hit and hit[0] > time.time():
            return hit[1]
        req = urlrequest.Request('https://omniapi.biz/api/user/self',
                                 headers={'Authorization': f'Bearer {bearer}'})
        try:
            with urlrequest.build_opener(IdentityNoRedirect()).open(req, timeout=8) as resp:
                payload = json.load(resp)
        except HTTPError as error:
            if error.code in (401, 403):
                raise GatewayError('UPSTREAM_TOKEN_INVALID', 401) from None
            raise GatewayError('UPSTREAM_UNAVAILABLE', 503) from None
        except (URLError, ValueError, TimeoutError):
            raise GatewayError('UPSTREAM_UNAVAILABLE', 503) from None
        if not payload.get('success') or not payload.get('data'):
            raise GatewayError('UPSTREAM_TOKEN_INVALID', 401)
        user = payload['data']
        self.cache[key] = (int(time.time()) + 55, user)
        return user

    # ---- 同步令牌（EdDSA，对齐 ops/sync/server.py verify()） ---------------
    def sync_entitlement(self, user_id):
        rule = self.sync_default or {}
        if not rule.get('enabled', True):
            raise GatewayError('SYNC_NOT_ENTITLED', 403)
        now = int(time.time())
        end = int(rule.get('end_epoch') or 0) or now + int(rule.get('end_days', 30)) * 86400
        quota = int(rule.get('quota_bytes', 268435456))
        return end, quota

    def sign_sync_token(self, user_id, device):
        end, quota = self.sync_entitlement(user_id)
        now = int(time.time())
        header = b64u(json.dumps({'alg': 'EdDSA', 'typ': 'JWT'}, separators=(',', ':')).encode())
        payload = b64u(json.dumps({
            'iss': 'https://omniapi.biz', 'aud': ['omniapi-user-sync'],
            'uid': user_id, 'device': device, 'end': end, 'quota': quota,
            'iat': now, 'exp': now + SYNC_TOKEN_TTL,
        }, separators=(',', ':')).encode())
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey  # noqa: F401
        signature = b64u(self.sync_signing.sign(f'{header}.{payload}'.encode()))
        return f'{header}.{payload}.{signature}'

    def introspect_sync(self, token):
        """sync 服务实时吊销检查：签名有效 + 设备会话未吊销才放行。"""
        try:
            header, payload, signature = token.split('.')
            claims = json.loads(unb64u(payload))
        except Exception:
            raise GatewayError('UNAUTHORIZED', 401) from None
        try:
            self.sync_signing.public_key().verify(
                unb64u(signature), f'{header}.{payload}'.encode())
        except Exception:
            raise GatewayError('UNAUTHORIZED', 401) from None
        now = int(time.time())
        if (claims.get('iss') != 'https://omniapi.biz'
                or claims.get('aud') not in ('omniapi-user-sync', ['omniapi-user-sync'])
                or type(claims.get('exp')) is not int or not now < claims['exp'] <= now + 65
                or not self.store.session_live(int(claims['uid']), claims.get('device', ''))):
            raise GatewayError('SESSION_REVOKED', 401)
        return {'uid': claims['uid'], 'device': claims['device'],
                'end': claims['end'], 'quota': claims['quota']}

    # ---- 业务路由 ----------------------------------------------------------
    def route(self, method, path, headers, body, client_ip):
        if method == 'GET' and path == '/healthz':
            return {'status': 'ok'}
        if method == 'GET' and path == '/api/user/self':
            uid = self.verify_access(bearer(headers))
            return self.snapshot_for(uid)
        if method == 'POST' and path.startswith('/api/app/web-session/'):
            if not path.endswith('/parent'):
                self.rate('approve_web', client_ip)
            if path.endswith('/start'):
                claims = self.access_claims(bearer(headers))
                challenge = str(body.get('challenge', ''))
                if not re.fullmatch(r'[A-Za-z0-9_-]{43}', challenge):
                    raise GatewayError('INVALID_PARAMS')
                with self.store._connect() as db:
                    row = db.execute('SELECT snapshot FROM device_session WHERE sid=?', (claims['sid'],)).fetchone()
                    if not json.loads(row['snapshot']).get('_web_parent'):
                        raise GatewayError('WEB_RELOGIN_REQUIRED', 401)
                    rid = b64u(secrets.token_bytes(24))
                    db.execute('DELETE FROM web_transfer WHERE expires_at<?', (int(time.time()),))
                    db.execute('INSERT INTO web_transfer VALUES(?,?,?,?,?,0)', (rid, challenge, claims['sid'], claims['uid'], int(time.time()) + 60))
                return {'request': rid}
            secret = self.config.get('web_bridge_secret', '')
            if len(secret) < 32 or not hmac.compare_digest(secret, headers.get('X-Omni-Web-Service', '')):
                raise GatewayError('FORBIDDEN', 403)
            with self.store._connect() as db:
                db.execute('BEGIN IMMEDIATE')
                if path.endswith('/consume'):
                    row = db.execute('SELECT * FROM web_transfer WHERE request=?', (str(body.get('request', '')),)).fetchone()
                    verifier = str(body.get('verifier', ''))
                    if (not IDENTIFIER.fullmatch(verifier) or row is None or row['consumed'] or row['expires_at'] <= time.time()
                            or not hmac.compare_digest(row['challenge'], b64u(hashlib.sha256(verifier.encode()).digest()))):
                        raise GatewayError('WEB_TRANSFER_INVALID', 401)
                    uid, sid = row['user_id'], row['native_sid']
                elif path.endswith('/parent'):
                    uid, sid = int(body.get('uid', 0)), str(body.get('sid', ''))
                else:
                    raise GatewayError('NOT_FOUND', 404)
                parent = db.execute('SELECT * FROM device_session WHERE sid=?', (sid,)).fetchone()
                if parent is None or parent['user_id'] != uid or parent['revoked'] or parent['expires_at'] <= time.time():
                    raise GatewayError('SESSION_REVOKED', 401)
                identity = json.loads(parent['snapshot']).get('_web_parent')
                if not identity:
                    raise GatewayError('WEB_RELOGIN_REQUIRED', 401)
                if path.endswith('/consume'):
                    db.execute('UPDATE web_transfer SET consumed=1 WHERE request=?', (row['request'],))
                db.commit()
            return {'uid': uid, 'native_sid': sid, 'parent_sid': identity['sid'], 'uv': identity['uv'], 'sv': identity['sv']}
        if path == '/api/app/login/start' and method == 'POST':
            self.rate('start', client_ip)
            challenge = str(body.get('challenge', ''))
            device = str(body.get('device', ''))
            platform = str(body.get('platform', ''))
            if not challenge or len(challenge) > 200:
                raise GatewayError('INVALID_PARAMS')
            if not IDENTIFIER.fullmatch(device):
                raise GatewayError('INVALID_DEVICE')
            if platform not in ('windows', 'android'):
                raise GatewayError('INVALID_PLATFORM')
            rid = self.store.create_request(challenge, device, platform)
            authorize = f'https://omniapi.biz/brand/app-login.html?request={rid}'
            return {'request': rid, 'authorize_url': authorize}
        if path == '/api/app/login/approve' and method == 'POST':
            # 双身份入口：客户端（omniapp1 Bearer，webLogin 给对端设备授权）与
            # 浏览器授权页（new-api dashboard JWT，经 OmniCommerce 转发）。
            # request 本身是 18 字节随机数，不可枚举，故任何有效身份均可批准。
            raw = bearer(headers)
            rid = str(body.get('request', ''))
            if raw.startswith('omniapp1.'):
                uid = self.verify_access(raw)
                self.store.approve(rid, uid, self.snapshot_for(uid))
                return {'approved': True}
            user = self.new_api_self(raw)
            uid = int(user.get('id') or 0)
            if uid <= 0:
                raise GatewayError('UPSTREAM_TOKEN_INVALID', 401)
            # Decode only AFTER upstream verified the exact token. Preserve the
            # original dashboard session reference, never the token itself.
            try:
                identity = json.loads(unb64u(raw.split('.')[1]))
                if (identity.get('iss') == 'new-api'
                        and identity.get('aud') in ('new-api-dashboard', ['new-api-dashboard'])
                        and identity.get('token_use') == 'access'
                        and int(identity['sub']) == uid
                        and int(identity['uv']) > 0 and int(identity['sv']) > 0):
                    user = {**user, '_web_parent': {'sid': identity['sid'], 'uv': identity['uv'], 'sv': identity['sv']}}
            except (ValueError, KeyError, IndexError, TypeError):
                pass  # Legacy/PAT identities can use native login, not web transfer.
            self.store.approve(rid, uid, user)
            return {'approved': True}
        if path == '/api/app/login/exchange' and method == 'POST':
            self.rate('exchange', client_ip)
            rid, verifier = str(body.get('request', '')), str(body.get('verifier', ''))
            if not IDENTIFIER.fullmatch(verifier):
                raise GatewayError('INVALID_VERIFIER')
            row = self.store.exchange(rid, verifier)
            if row is None:
                raise GatewayError('AUTHORIZATION_PENDING')
            request, sid, refresh = row
            access = self.sign_access(request['user_id'], sid)
            now = int(time.time())
            return {
                'user_id': request['user_id'], 'device': request['device'],
                'refresh_token': refresh,
                'auth': {'access_token': access, 'access_expires_at': now + ACCESS_TTL,
                         'session': {'sid': sid}},
            }
        if path == '/api/app/login/refresh' and method == 'POST':
            self.rate('refresh', client_ip)
            refresh, sid = str(body.get('refresh_token', '')), str(body.get('sid', ''))
            row = self.store.session_by_refresh(refresh, sid)
            new_refresh = self.store.rotate_refresh(sid)
            access = self.sign_access(row['user_id'], sid)
            now = int(time.time())
            return {
                'user_id': row['user_id'],
                'refresh_token': new_refresh,
                'auth': {'access_token': access, 'access_expires_at': now + ACCESS_TTL,
                         'session': {'sid': sid}},
            }
        if path == '/api/app/login/logout' and method == 'POST':
            claims = self.access_claims(bearer(headers))
            if body.get('sid') and body['sid'] != claims['sid']:
                raise GatewayError('FORBIDDEN', 403)
            self.store.revoke(claims['sid'])
            return {'ok': True}
        if path == '/api/app/sync/token' and method == 'POST':
            uid = self.verify_access(bearer(headers))
            device = str(body.get('device', ''))
            if not IDENTIFIER.fullmatch(device):
                raise GatewayError('INVALID_DEVICE')
            self.sync_entitlement(uid)      # 无资格在此即报错
            token = self.sign_sync_token(uid, device)
            url = self.config.get('sync_url', 'https://omniapi.biz/api/sync')
            return {'url': url, 'token': token}
        if path == '/api/app/sync/introspect' and method == 'POST':
            if headers.get('X-Omni-Sync-Service') != self.introspection_secret:
                raise GatewayError('FORBIDDEN', 403)
            return self.introspect_sync(str(body.get('token', '')))
        raise GatewayError('NOT_FOUND', 404)

    def snapshot_for(self, uid):
        with self.store._connect() as db:
            row = db.execute(
                'SELECT snapshot FROM device_session WHERE user_id=? AND revoked=0 ORDER BY created DESC LIMIT 1',
                (uid,)).fetchone()
        if row is None:
            raise GatewayError('UNAUTHORIZED', 401)
        return {k: v for k, v in json.loads(row['snapshot']).items() if k != '_web_parent'}


def bearer(headers):
    raw = headers.get('Authorization', '')
    if raw.startswith('Bearer '):
        return raw[7:]
    raise GatewayError('UNAUTHORIZED', 401)


def load_config():
    path = Path(os.environ.get('OMNI_APPGW_CONFIG', ROOT / 'config' / 'app-gateway.json'))
    raw = json.loads(path.read_text(encoding='utf-8')) if path.exists() else {}
    secrets_path = Path(os.environ.get('OMNI_APPGW_SECRETS', ROOT / '.secrets' / 'app-gateway.json'))
    merged = dict(raw)
    if secrets_path.exists():
        merged.update(json.loads(secrets_path.read_text(encoding='utf-8')))
    for required in ('token_secret_b64u', 'sync_signing_key_b64u', 'sync_introspection_secret'):
        if required not in merged:
            raise SystemExit(f'BLOCKED: {required} 未配置（见 .secrets/app-gateway.json 样例）')
    return merged


def main():
    port = int(os.environ.get('OMNI_APPGW_PORT', '8791'))
    config = load_config()
    gateway = Gateway(Store(ROOT / 'runtime' / 'app-gateway' / 'app-gateway.sqlite3'), config)

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass  # 不记 URL/token/正文

        def dispatch(self):
            status = 200
            try:
                size = int(self.headers.get('Content-Length', '0'))
                if not 0 <= size <= MAX_BODY:
                    raise GatewayError('BODY_TOO_LARGE', 413)
                body = json.loads(self.rfile.read(size)) if size else {}
                if size and not self.headers.get('Content-Type', '').startswith('application/json'):
                    raise GatewayError('JSON_REQUIRED', 415)
                result = gateway.route(self.command, self.path.split('?')[0],
                                       dict(self.headers), body, self.client_address[0])
                payload = {'success': True, 'data': result}
            except GatewayError as error:
                status, payload = error.status, {'success': False, 'code': error.code}
            except (ValueError, TypeError, KeyError):
                status, payload = 400, {'success': False, 'code': 'INVALID_REQUEST'}
            except Exception:
                status, payload = 503, {'success': False, 'code': 'GATEWAY_UNAVAILABLE'}
            raw = json.dumps(payload, separators=(',', ':')).encode()
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(raw)))
            self.send_header('Cache-Control', 'no-store')
            self.send_header('X-Content-Type-Options', 'nosniff')
            self.end_headers()
            self.wfile.write(raw)

        do_GET = do_POST = dispatch

    server = ThreadingHTTPServer((os.environ.get('OMNI_APPGW_ADDR', '0.0.0.0'), port), Handler)
    server.daemon_threads = True
    print(f'omniapi-app-gateway listening on 127.0.0.1:{port}', flush=True)
    server.serve_forever()


if __name__ == '__main__':
    main()
