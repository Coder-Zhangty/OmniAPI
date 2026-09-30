#!/usr/bin/env python3
"""经营者会话与 CSRF 的共享实现，充值服务和客服服务都用这一份。

两个服务面对的是同一个人，所以必须共用同一张 cookie：经营者登录一次，就能在同一个
浏览器会话里核验充值、也能回客服消息。把这段逻辑写两遍还有第二个代价——令牌轮换
会变成两个地方要做对，而本项目已经因为「同一个规则抄第二份」吃过三次亏
（见 `topup_requests.usd_from_cny()` 的历史）。

这里不碰网络也不碰数据库：只对 `.secrets/topup-admin.json` 的内容和
`runtime/admin-session-state.json` 里的 generation 计数做签名与校验。

cookie 格式：`v2.<过期时间戳>.<generation>.<scope>.<mac>`，
scope 记录经营者是怎么证明身份的（`pw` = 管理口令，`na:<用户ID>` = 控制台管理员令牌）。
"""
import base64
import datetime as _dt
import hashlib
import hmac
import json
import os
import re
import secrets
import time
from pathlib import Path

class AuthError(Exception):
    """The caller must prove who they are before this action can run.

    Mapped to HTTP 401 by both services' request handlers, so a client cannot
    mistake a missing identity for a validation error (400) or a rate limit (429).
    Defined once here because "who are you" is answered the same way in both.
    """


COOKIE_NAME = 'omniapi_admin'
COOKIE_TTL = 28800               # 8 小时；管钱的会话不该长命
COOKIE_PATH = '/brand/'
SECRET_ITERATIONS = 200000
SCOPE = re.compile(r'^[A-Za-z0-9:_-]{1,32}$')
PASSWORD_ALPHABET = 'abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789'


def iso(stamp=None):
    return (stamp or _dt.datetime.now(_dt.timezone.utc)).isoformat(timespec='seconds')


def generate_password(length=16):
    return ''.join(secrets.choice(PASSWORD_ALPHABET) for _ in range(length))


def hash_password(password, salt, iterations=SECRET_ITERATIONS):
    return hashlib.pbkdf2_hmac('sha256', password.encode('utf-8'),
                               bytes.fromhex(salt), iterations).hex()


def load_json(path):
    """Read a JSON document, or None when it is absent. Used for secrets and state."""
    path = Path(path)
    if not path.exists():
        return None
    return json.loads(path.read_text(encoding='utf-8'))


def init_secrets(path, password=None):
    """Create the admin password file. Returns (document, plaintext password)."""
    path = Path(path)
    password = password or generate_password()
    document = {
        'version': 1,
        'iterations': SECRET_ITERATIONS,
        'salt': secrets.token_hex(16),
        'cookie_secret': secrets.token_hex(32),
        'created_at': iso(),
    }
    document['password_hash'] = hash_password(password, document['salt'], document['iterations'])
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(document, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    os.chmod(path, 0o600)
    return document, password


def password_matches(document, password):
    if not document or not isinstance(password, str) or not password:
        return False
    expected = hash_password(password, document['salt'], int(document['iterations']))
    return hmac.compare_digest(expected, str(document.get('password_hash', '')))


def sign_session(document, expires_at, scope='pw', generation=None):
    """Sign a session cookie. `scope` records how the operator proved identity."""
    if not SCOPE.match(str(scope)):
        raise ValueError('invalid scope')
    generation = int(generation if generation is not None else document.get('session_generation', 1))
    payload = 'v2.%d.%d.%s' % (int(expires_at), generation, scope)
    mac = hmac.new(bytes.fromhex(document['cookie_secret']), payload.encode('ascii'),
                   hashlib.sha256).digest()[:18]
    return payload + '.' + base64.urlsafe_b64encode(mac).decode('ascii').rstrip('=')


def session_scope(document, value, now=None, generation=None):
    """Verify the admin cookie and return its scope, or None. No session store."""
    if not document or not value:
        return None
    parts = str(value).split('.')
    if len(parts) != 5 or parts[0] != 'v2':
        return None
    try:
        expires_at = int(parts[1])
        cookie_generation = int(parts[2])
    except ValueError:
        return None
    if expires_at < int(now or time.time()):
        return None
    current_generation = int(generation if generation is not None else document.get('session_generation', 1))
    if cookie_generation != current_generation or not SCOPE.match(parts[3]):
        return None
    if not hmac.compare_digest(sign_session(document, expires_at, parts[3], current_generation), str(value)):
        return None
    return parts[3]


def session_valid(document, value, now=None):
    return session_scope(document, value, now) is not None


def load_generation(state_path):
    """Read the revocation counter.

    Bumping it invalidates every cookie already handed out, so both services must
    read it from the same file rather than keeping their own copy -- otherwise
    rotating the password would leave one service still honouring old cookies.
    """
    state = load_json(state_path) or {}
    try:
        return max(1, int(state.get('generation') or 1))
    except (TypeError, ValueError):
        return 1


def write_generation(state_path, value):
    path = Path(state_path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + '.tmp')
    tmp.write_text(json.dumps({'generation': int(value), 'updated_at': iso()},
                              ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    os.chmod(tmp, 0o640)
    os.replace(tmp, path)
    return int(value)


def bump_generation(state_path):
    """Revoke every cookie already handed out, in every service reading this file.

    This is the single implementation of "log out everywhere". Both services
    compare cookies against the same counter, so one bump cuts off all surfaces
    at the same instant -- which is what makes console logout actually mean
    logout for the money and support surfaces too. Deliberately not re-implemented
    per service: two writers would drift.
    """
    value = load_generation(state_path) + 1
    write_generation(state_path, value)
    return value



def parse_cookies(header_value):
    """Parse a Cookie header into a dict. Tolerant: a malformed pair is skipped."""
    jar = {}
    for part in str(header_value or '').split(';'):
        name, _, value = part.strip().partition('=')
        if name:
            jar[name] = value
    return jar


def cookie_header(value, max_age, secure=True):
    return '%s=%s; Path=%s; Max-Age=%d; HttpOnly; SameSite=Strict%s' % (
        COOKIE_NAME, value, COOKIE_PATH, int(max_age), '; Secure' if secure else '')


def csrf_token(document, cookie_value):
    """Derive the CSRF token from the session cookie itself.

    Binding it to the cookie (rather than storing a random token) means it cannot
    outlive the session, and any service holding the same `cookie_secret` derives
    the identical value -- which is what lets the support service accept a token
    the recharge service handed out.
    """
    if not document or not cookie_value:
        return ''
    return hmac.new(bytes.fromhex(document['cookie_secret']),
                    ('csrf:' + str(cookie_value)).encode('ascii'),
                    hashlib.sha256).hexdigest()


def secret_matches(supplied, expected):
    """Constant-time comparison that treats a missing expectation as a failure."""
    if not expected:
        return False
    return hmac.compare_digest(str(supplied or ''), str(expected))


def scope_of(cookie_value, document, generation=None, now=None):
    """Convenience wrapper: cookie value -> scope, reading the document itself."""
    return session_scope(document, cookie_value, now=now, generation=generation)
