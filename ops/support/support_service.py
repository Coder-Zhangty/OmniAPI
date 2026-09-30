#!/usr/bin/env python3
"""OmniAPI 客服服务：实时文字客服（SSE）+ append-only 事件日志。

设计与取舍见 `docs/SUPPORT_CHAT_PLAN.md`。要点：

- **独立进程**，不塞进充值入账服务：长连接的故障模式不该有机会碰到钱那条路。
- 传输用 **SSE**（`text/event-stream`）+ 普通 POST 发送；nginx 已具备条件，不需要
  WebSocket 的 upgrade map。所有 SSE 都终结在本进程内，所以不需要 Redis。
- 身份**只由服务端从 Bearer 令牌推导**；请求体里的 uid 一律忽略。
- 经营者侧复用充值服务的会话 cookie，并且写操作同样要 **Origin 匹配 + X-CSRF-Token**。
- 会话对外只用编号（`S-YYYYMMDD-xxxxxx`），不出现数字用户 ID。

它是消息通道，不是账本：**任何额度变更都不在这里发生**。经营者在客服台看不到
「加额度」按钮，那件事只有充值核验页能做。
"""
import argparse
import base64
import hashlib
import io
import json
import os
import re
import sys
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

try:
    from PIL import Image, UnidentifiedImageError
except ImportError:  # Attachment endpoint reports a controlled error; chat still works.
    Image = None
    UnidentifiedImageError = OSError

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
sys.path.insert(0, str(ROOT / 'ops'))
sys.path.insert(0, str(ROOT / 'ops' / 'support'))

import notifier  # noqa: E402
import operator_session  # noqa: E402
import support_store as store  # noqa: E402
import topup_requests as queue  # noqa: E402
import user_identity  # noqa: E402
from operator_session import AuthError  # noqa: E402

USER_PREFIX = '/brand/api/support/'
ADMIN_PREFIX = '/brand/admin/api/support/'

MAX_BODY = 32768
MAX_UPLOAD_BODY = 22 * 1024 * 1024
MAX_IMAGE_BYTES = 5 * 1024 * 1024
MAX_IMAGE_PIXELS = 25_000_000
MAX_IMAGES_PER_MESSAGE = 3
MAX_STREAMS = 64                 # 1 核 2G 上的硬上限；超出即降级为轮询
HEARTBEAT = 20                   # 秒；同时是 SSE 单次等待的上限
MAX_STREAM_SECONDS = 1500        # 25 分钟；之后客户端带 since 重连
STREAM_RETRY_AFTER = 5

SEND_UID_LIMIT = (20, 60)        # 每个账号每分钟
SEND_IP_LIMIT = (40, 60)
SEND_GLOBAL_LIMIT = (600, 60)
# 每次打开客服页都会调用 start（它是幂等的：已有进行中的会话就返回那一个），
# 所以这个桶必须宽松。线上曾因为 6 次/10 分钟，用户刷新几次就吃到 429。
START_IP_LIMIT = (60, 600)
# 真正「开一个新会话」才花这张严格的按账号配额。
START_UID_LIMIT = (6, 600)
IDENTIFY_IP_LIMIT = (40, 600)    # 身份校验要打网关，单独限量
CONVERSATIONS_PER_UID = 5        # 同时能开的会话数上限

DEFAULT_CONFIG = {
    'enabled': True,
    'service_hours': '',
    'greeting': '你好，这里是 OmniAPI 客服。',
    'offline_hint': '',
    'privacy_note': '客服消息会保存在我们的服务器上，用于答复与事后核对。',
    'max_text': store.MAX_TEXT,
    'retention_days': 180,
    'handoff_keywords': [],
    'quick_replies': ['收到，我看一下。', '请把报错时间和 Request ID 发我。',
                      '已经处理好了，请刷新页面再试。'],
    'notify': {'channel': 'none', 'min_interval_seconds': 60},
    'ai': {'enabled': False, 'max_rounds_per_conversation': 3,
           'max_tokens_per_reply': 400, 'per_minute_limit': 10},
}


def merge_config(raw):
    """Shallow-merge the operator's file over the defaults, one level deep.

    Nested blocks (notify, ai) are merged key by key so a file that only sets
    `ai.enabled` does not silently lose the limits.
    """
    config = dict(DEFAULT_CONFIG)
    for key, value in (raw or {}).items():
        if key.startswith('_comment'):
            continue
        if isinstance(value, dict) and isinstance(config.get(key), dict):
            merged = dict(config[key])
            merged.update(value)
            config[key] = merged
        else:
            config[key] = value
    return config


class Service:
    """Config (hot-read), rate gates, the operator session and the chat store."""

    def __init__(self, secrets_path, events_folder, state_path, config_path,
                 gateway_url=None, allow_insecure_cookie=False, public_origin=None,
                 notify_secrets_path=None, max_streams=MAX_STREAMS):
        self.secrets_path = Path(secrets_path)
        self.events_folder = Path(events_folder)
        self.attachments_folder = self.events_folder.parent / 'support-attachments'
        self.runtime_config_path = self.events_folder.parent / 'support-settings.json'
        self.attachments_folder.mkdir(parents=True, exist_ok=True)
        self.state_path = Path(state_path)
        self.config_path = Path(config_path)
        self.notify_secrets_path = Path(notify_secrets_path or
                                        ROOT / '.secrets/support-notify.json')
        self.gateway_url = (gateway_url or os.environ.get('SUPPORT_GATEWAY_URL')
                            or 'https://omniapi.biz')
        self.allow_insecure_cookie = allow_insecure_cookie
        self.public_origin = (public_origin or os.environ.get('SUPPORT_PUBLIC_ORIGIN')
                              or 'https://omniapi.biz').rstrip('/')
        self.secrets_doc = operator_session.load_json(self.secrets_path)

        self.store = store.ConversationStore(self.events_folder)
        self.identify_gate = queue.SlidingWindow(*IDENTIFY_IP_LIMIT)
        self.send_uid_gate = queue.SlidingWindow(*SEND_UID_LIMIT)
        self.send_ip_gate = queue.SlidingWindow(*SEND_IP_LIMIT)
        self.send_global_gate = queue.SlidingWindow(*SEND_GLOBAL_LIMIT)
        self.start_gate = queue.SlidingWindow(*START_IP_LIMIT)
        self.start_uid_gate = queue.SlidingWindow(*START_UID_LIMIT)
        self.notify_throttle = notifier.Throttle()
        self._streams = 0
        self.stream_lock = threading.Lock()

    # -- configuration ---------------------------------------------------
    def config(self):
        """Re-read on every call: the operator edits config/support.json by hand."""
        try:
            raw = json.loads(self.config_path.read_text(encoding='utf-8'))
        except (OSError, ValueError):
            raw = {}
        config = merge_config(raw)
        try:
            runtime = json.loads(self.runtime_config_path.read_text(encoding='utf-8'))
        except (OSError, ValueError):
            runtime = {}
        for key in ('service_hours', 'greeting', 'offline_hint', 'quick_replies'):
            if key in runtime:
                config[key] = runtime[key]
        return config

    def public_config(self):
        config = self.config()
        return {
            'enabled': bool(config.get('enabled')),
            'service_hours': config.get('service_hours') or '',
            'greeting': config.get('greeting') or '',
            'offline_hint': config.get('offline_hint') or '',
            'privacy_note': config.get('privacy_note') or '',
            # Clamped: the store enforces its own ceiling, so advertising a
            # larger limit here would only produce a confusing rejection.
            'max_text': min(int(config.get('max_text') or store.MAX_TEXT),
                            store.MAX_TEXT),
            'ai_enabled': bool((config.get('ai') or {}).get('enabled')),
            'notify_channel': (config.get('notify') or {}).get('channel') or 'none',
            'categories': [
                {'id': 'recharge', 'label': '充值到账'},
                {'id': 'api_access', 'label': 'API 接入'},
                {'id': 'model_error', 'label': '模型调用错误'},
                {'id': 'billing', 'label': '计费与扣额'},
                {'id': 'account_security', 'label': '账号与安全'},
                {'id': 'other', 'label': '其他'}],
        }

    def update_public_settings(self, values):
        allowed = ('service_hours', 'greeting', 'offline_hint', 'quick_replies')
        try:
            config = json.loads(self.runtime_config_path.read_text(encoding='utf-8'))
        except (OSError, ValueError):
            config = {}
        clean = {}
        for key in allowed:
            if key not in values:
                continue
            value = values[key]
            if key == 'quick_replies':
                if not isinstance(value, list) or len(value) > 20:
                    raise ValueError('快捷回复格式无效')
                value = [str(item).strip()[:300] for item in value if str(item).strip()]
            else:
                value = str(value).strip()[:500]
            clean[key] = value
        if not clean:
            raise ValueError('没有可保存的设置')
        config.update(clean)
        temporary = self.runtime_config_path.with_suffix('.tmp')
        temporary.write_text(json.dumps(config, ensure_ascii=False, indent=2) + '\n',
                             encoding='utf-8')
        os.replace(temporary, self.runtime_config_path)
        os.chmod(self.runtime_config_path, 0o600)
        self.store.append_quiet({'type': 'settings', 'id': 'runtime-settings',
                                 'values': clean, 'created_at': store.iso()})
        return clean

    @staticmethod
    def ticket_priority(category, text):
        lowered = str(text or '').lower()
        if category == 'account_security' or any(word in lowered for word in
                ('盗号', '泄露', 'unauthorized', '无法登录', '重复扣费')):
            return 'high'
        return 'normal'

    def save_images(self, conv_id, files, uploader_role):
        if Image is None:
            raise ValueError('图片处理组件不可用')
        if not isinstance(files, list) or not 1 <= len(files) <= MAX_IMAGES_PER_MESSAGE:
            raise ValueError('每条消息最多上传 3 张图片')
        saved = []
        formats = {'PNG': ('image/png', '.png'), 'JPEG': ('image/jpeg', '.jpg'),
                   'WEBP': ('image/webp', '.webp')}
        for item in files:
            if not isinstance(item, dict):
                raise ValueError('附件格式无效')
            name = Path(str(item.get('name') or 'image')).name[:120]
            try:
                raw = base64.b64decode(str(item.get('data') or ''), validate=True)
            except (ValueError, TypeError):
                raise ValueError('图片数据无效')
            if not raw or len(raw) > MAX_IMAGE_BYTES:
                raise ValueError('每张图片不能超过 5 MiB')
            try:
                with Image.open(io.BytesIO(raw)) as source:
                    fmt = str(source.format or '').upper()
                    if fmt not in formats:
                        raise ValueError('只支持 PNG、JPEG 或 WebP 图片')
                    if source.width * source.height > MAX_IMAGE_PIXELS:
                        raise ValueError('图片尺寸过大')
                    source.load()
                    mime, suffix = formats[fmt]
                    image = source.convert('RGBA' if fmt in ('PNG', 'WEBP') else 'RGB')
                    attachment_id = 'A-' + uuid.uuid4().hex
                    stored = attachment_id + suffix
                    target = self.attachments_folder / stored
                    buffer = io.BytesIO()
                    options = {'optimize': True}
                    if fmt == 'JPEG': options.update({'quality': 88, 'progressive': True})
                    if fmt == 'WEBP': options.update({'quality': 88, 'method': 4})
                    image.save(buffer, format=fmt, **options)
                    data = buffer.getvalue()
                    with target.open('xb') as handle:
                        handle.write(data); handle.flush(); os.fsync(handle.fileno())
                    os.chmod(target, 0o600)
            except (UnidentifiedImageError, OSError):
                raise ValueError('图片无法解码')
            record = {'attachment_id': attachment_id, 'original_name': name,
                      'mime': mime, 'size': len(data),
                      'sha256': hashlib.sha256(data).hexdigest(),
                      'stored_name': stored, 'uploader_role': uploader_role}
            self.store.add_attachment(conv_id, record)
            saved.append({'id': attachment_id, 'name': name, 'mime': mime,
                          'size': len(data)})
        return saved

    # -- identity --------------------------------------------------------
    def identify(self, token, client):
        """Bearer token -> account, on its own budget so a public route cannot
        become a stream of gateway lookups."""
        if not self.identify_gate.allow(client):
            raise PermissionError('身份校验过于频繁，请 %d 秒后再试。'
                                  % self.identify_gate.retry_after(client))
        return user_identity.user_for(self.gateway_url, token)

    def generation(self):
        return operator_session.load_generation(self.state_path)

    def operator_scope(self, cookie_value):
        return operator_session.session_scope(self.secrets_doc, cookie_value,
                                              generation=self.generation())

    def notification_channel(self):
        return (self.config().get('notify') or {}).get('channel') or 'none'

    # -- stream slots ----------------------------------------------------
    def acquire_stream(self):
        with self.stream_lock:
            if self._streams >= MAX_STREAMS:
                return False
            self._streams += 1
            return True

    def release_stream(self):
        with self.stream_lock:
            self._streams = max(0, self._streams - 1)

    @property
    def streams(self):
        with self.stream_lock:
            return self._streams

    # -- side effects ----------------------------------------------------
    def notify_operator(self, conversation, text, urgent=False):
        """Push a message to the operator's phone and record the attempt.

        Recorded either way, including when no channel is configured: "为什么我
        没收到推送" must be answerable from the log instead of from memory.
        """
        config = self.config()
        channel = (config.get('notify') or {}).get('channel') or 'none'
        interval = int((config.get('notify') or {}).get('min_interval_seconds') or 60)
        conv_id = conversation['id']
        if urgent:
            self.notify_throttle.reset(conv_id)
        elif not self.notify_throttle.allow(conv_id, interval):
            self.store.append_quiet({
                'type': 'notify', 'conv': conv_id, 'channel': channel,
                'status': 'merged', 'error': '',
                'created_at': store.iso(),
            })
            return {'channel': channel, 'ok': False, 'merged': True, 'error': ''}

        who = conversation.get('username') or ('用户 %s' % conversation.get('uid'))
        title = 'OmniAPI 客服 · %s' % who
        link = '%s/brand/support-admin.html#%s' % (self.public_origin, conv_id)
        result = notifier.publish(channel, notifier.load_secrets(self.notify_secrets_path),
                                 title, text, link)
        self.store.append_quiet({
            'type': 'notify', 'conv': conv_id, 'channel': channel,
            'status': 'ok' if result.get('ok') else 'failed',
            'error': str(result.get('error') or '')[:200],
            'created_at': store.iso(),
        })
        return result

    def handoff_reason(self, text):
        """Deterministic keyword match. Deciding 'this needs a human' by asking a
        model is how a refund request ends up answered by a language model."""
        lowered = str(text or '').lower()
        for word in self.config().get('handoff_keywords') or []:
            if str(word).lower() in lowered:
                return str(word)
        return ''


class Handler(BaseHTTPRequestHandler):
    server_version = 'omniapi-support'
    sys_version = ''
    protocol_version = 'HTTP/1.1'
    service = None

    # -- plumbing --------------------------------------------------------
    def log_message(self, fmt, *args):
        line = json.dumps({'time': store.iso(), 'client': self.client_ip(),
                           'request': self.requestline, 'detail': fmt % args[:3]},
                          ensure_ascii=False)
        sys.stdout.write(line + '\n')
        sys.stdout.flush()

    def client_ip(self):
        return (self.headers.get('X-Real-IP') or self.client_address[0] or '')[:64]

    def respond(self, status, payload, extra_headers=None):
        body = json.dumps(payload, ensure_ascii=False).encode('utf-8')
        self.send_response(status)
        self.send_header('Content-Type', 'application/json; charset=utf-8')
        self.send_header('Content-Length', str(len(body)))
        self.send_header('Cache-Control', 'no-store')
        self.send_header('X-Content-Type-Options', 'nosniff')
        self.send_header('Referrer-Policy', 'no-referrer')
        for name, value in (extra_headers or {}).items():
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)

    def respond_file(self, path, mime, name):
        data = Path(path).read_bytes()
        self.send_response(200)
        self.send_header('Content-Type', mime)
        self.send_header('Content-Length', str(len(data)))
        self.send_header('Content-Disposition', 'inline; filename="attachment"')
        self.send_header('Cache-Control', 'private, no-store')
        self.send_header('X-Content-Type-Options', 'nosniff')
        self.send_header('Content-Security-Policy', "default-src 'none'; img-src 'self'")
        self.end_headers(); self.wfile.write(data)

    def read_body(self, maximum=MAX_BODY):
        try:
            length = int(self.headers.get('Content-Length') or 0)
        except ValueError:
            raise ValueError('请求长度无效')
        if length <= 0:
            return {}
        if length > maximum:
            raise ValueError('请求过大')
        raw = self.rfile.read(length)
        try:
            payload = json.loads(raw.decode('utf-8'))
        except (ValueError, UnicodeDecodeError):
            raise ValueError('请求格式无效')
        if not isinstance(payload, dict):
            raise ValueError('请求格式无效')
        return payload

    def query(self):
        path, _, query = self.path.partition('?')
        params = {}
        for pair in query.split('&'):
            if not pair:
                continue
            name, _, value = pair.partition('=')
            from urllib.parse import unquote_plus
            params[unquote_plus(name)] = unquote_plus(value)
        return path, params

    def bearer_token(self):
        header = self.headers.get('Authorization') or ''
        if not header.lower().startswith('bearer '):
            return ''
        return header[7:].strip()

    def admin_cookie(self):
        return operator_session.parse_cookies(
            self.headers.get('Cookie')).get(operator_session.COOKIE_NAME, '')

    def require_operator(self):
        scope = self.service.operator_scope(self.admin_cookie())
        if scope is None:
            self.respond(401, {'success': False, 'message': '需要经营者登录'})
        return scope

    def require_operator_write(self):
        """GET is guarded by the cookie; a write additionally needs the right Origin
        and a CSRF token, so a page the operator merely visits cannot reply or
        close conversations on their behalf."""
        scope = self.require_operator()
        if scope is None:
            return None
        origin = (self.headers.get('Origin') or '').rstrip('/')
        if origin != self.service.public_origin:
            self.respond(403, {'success': False, 'message': '请求来源校验失败'})
            return None
        expected = operator_session.csrf_token(self.service.secrets_doc, self.admin_cookie())
        if not operator_session.secret_matches(self.headers.get('X-CSRF-Token'), expected):
            self.respond(403, {'success': False, 'message': 'CSRF 校验失败，请刷新客服台'})
            return None
        return scope

    def max_text(self):
        return min(int(self.service.config().get('max_text') or store.MAX_TEXT),
                   store.MAX_TEXT)

    def require_user(self):
        user = self.service.identify(self.bearer_token(), self.client_ip())
        if not user:
            raise AuthError('请先登录再联系客服，然后刷新本页。')
        return user

    # -- SSE -------------------------------------------------------------
    def sse_start(self):
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream; charset=utf-8')
        self.send_header('Cache-Control', 'no-store')
        self.send_header('X-Accel-Buffering', 'no')
        self.send_header('X-Content-Type-Options', 'nosniff')
        self.send_header('Referrer-Policy', 'no-referrer')
        self.send_header('Connection', 'close')
        self.end_headers()
        self.close_connection = True

    def sse(self, event, data):
        chunk = 'event: %s\ndata: %s\n\n' % (event, json.dumps(data, ensure_ascii=False))
        return self.sse_raw(chunk)

    def sse_raw(self, text):
        try:
            self.wfile.write(text.encode('utf-8'))
            self.wfile.flush()
            return True
        except (BrokenPipeError, ConnectionResetError, OSError):
            return False

    def sse_wait(self, revision, timeout=HEARTBEAT):
        """Wait for a change the caller has not seen yet, or for the heartbeat.

        `revision` is the one the caller's snapshot was taken at. If the log has
        already moved past it, this returns at once rather than sleeping -- a
        plain `notify_all()` would lose the wakeup whenever a message landed
        between the caller's fetch and this wait, leaving it silent until the
        next heartbeat. The heartbeat is otherwise what keeps the connection
        alive through nginx's 600s read timeout; when it closes, the client
        reconnects with `since` and nothing is lost.
        """
        with self.service.store.condition:
            if self.service.store.revision == revision:
                self.service.store.condition.wait(timeout)
            else:
                return True
        return self.sse_raw(': ping\n\n')

    def stream_slots(self):
        if self.service.acquire_stream():
            return True
        self.respond(503, {'success': False, 'retry_after': STREAM_RETRY_AFTER,
                           'message': '实时推送连接已满，请稍后自动降级为轮询。'},
                     {'Retry-After': str(STREAM_RETRY_AFTER)})
        return False

    # -- routes ----------------------------------------------------------
    def do_GET(self):
        path, params = self.query()
        try:
            if path == '/healthz':
                return self.respond(200, {
                    'success': True, 'service': 'omniapi-support',
                    'streams': self.service.streams,
                    'counts': self.service.store.counts()})
            if path == USER_PREFIX + 'config':
                return self.respond(200, {'success': True, **self.service.public_config()})
            if path == USER_PREFIX + 'v2/tickets':
                user = self.require_user()
                rows = [self.ticket_view(row) for row in self.service.store.search(limit=200)
                        if int(row.get('uid') or 0) == int(user['id'])]
                return self.respond(200, {'success': True, 'tickets': rows,
                                          'unread': sum(int(row.get('unread_user') or 0) for row in rows)})
            if path == USER_PREFIX + 'v2/ticket':
                user = self.require_user(); return self.user_v2_ticket(user, params)
            if path == USER_PREFIX + 'v2/attachment':
                user = self.require_user(); return self.attachment(user, params, False)
            if path == USER_PREFIX + 'v2/stream':
                user = self.require_user()
                params['conversation_id'] = params.get('ticket_id') or params.get('conversation_id')
                return self.user_stream(user, params)
            if path == USER_PREFIX + 'history':
                user = self.require_user()
                return self.user_history(user, params)
            if path == USER_PREFIX + 'stream':
                user = self.require_user()
                return self.user_stream(user, params)
            if path == ADMIN_PREFIX + 'session':
                # Lets the support workbench stand on its own: it can tell whether
                # the operator cookie is good and get the CSRF token it needs,
                # without depending on the recharge service being up.
                scoped = self.service.operator_scope(self.admin_cookie()) is not None
                return self.respond(200, {
                    'success': True,
                    'authenticated': scoped,
                    # 只有会话有效才给令牌。它是拿"你递过来的 cookie 值"算的，
                    # 所以对一张已作废的 cookie 也会算出结果 —— 那样调用方会把
                    # 「有令牌」误当成「已登录」。充值服务那侧本来就是这么写的。
                    'csrf_token': operator_session.csrf_token(
                        self.service.secrets_doc, self.admin_cookie()) if scoped else ''})
            if path == ADMIN_PREFIX + 'conversations':
                if self.require_operator() is None:
                    return
                return self.respond(200, {'success': True,
                                          'counts': self.service.store.counts(),
                                          'conversations': self.service.store.listing()})
            if path == ADMIN_PREFIX + 'v2/tickets':
                if self.require_operator() is None: return
                return self.admin_v2_tickets(params)
            if path == ADMIN_PREFIX + 'v2/ticket':
                if self.require_operator() is None: return
                return self.admin_v2_ticket(params)
            if path == ADMIN_PREFIX + 'v2/attachment':
                if self.require_operator() is None: return
                return self.attachment(None, params, True)
            if path == ADMIN_PREFIX + 'v2/stream':
                if self.require_operator() is None: return
                return self.operator_stream(params)
            if path == ADMIN_PREFIX + 'history':
                if self.require_operator() is None:
                    return
                conv_id = str(params.get('conversation_id') or '')
                rows = self.service.store.history(conv_id, int(params.get('since') or 0))
                return self.respond(200, {'success': True, 'conversation_id': conv_id,
                                          'messages': rows})
            if path == ADMIN_PREFIX + 'stream':
                if self.require_operator() is None:
                    return
                return self.operator_stream(params)
            if path == ADMIN_PREFIX + 'settings':
                if self.require_operator() is None:
                    return
                config = self.service.config()
                return self.respond(200, {
                    'success': True,
                    'service_hours': config.get('service_hours') or '',
                    'greeting': config.get('greeting') or '',
                    'offline_hint': config.get('offline_hint') or '',
                    'quick_replies': config.get('quick_replies') or [],
                    'privacy_note': config.get('privacy_note') or '',
                    'handoff_keywords': config.get('handoff_keywords') or [],
                    'retention_days': config.get('retention_days'),
                    'notify': config.get('notify') or {},
                    'ai': config.get('ai') or {},
                    'csrf_token': operator_session.csrf_token(
                        self.service.secrets_doc, self.admin_cookie()),
                    'notify_credentials_present': bool(
                        notifier.load_secrets(self.service.notify_secrets_path)),
                    'counts': self.service.store.counts()})
            self.respond(404, {'success': False, 'message': 'not found'})
        except AuthError as error:
            self.respond(401, {'success': False, 'message': str(error)})
        except ValueError as error:
            self.respond(400, {'success': False, 'message': str(error)})
        except PermissionError as error:
            self.respond(429, {'success': False, 'message': str(error)})
        except Exception as error:                     # never leak a stack trace
            self.log_message('unhandled %s: %s', type(error).__name__, error)
            self.respond(500, {'success': False, 'message': '服务暂时不可用'})

    def do_POST(self):
        path, _ = self.query()
        try:
            if path.startswith(ADMIN_PREFIX):
                origin = (self.headers.get('Origin') or '').rstrip('/')
                if origin != self.service.public_origin:
                    return self.respond(403, {'success': False,
                                              'message': '请求来源校验失败'})
            if path == USER_PREFIX + 'start':
                payload = self.read_body()
                return self.user_start(payload)
            if path == USER_PREFIX + 'send':
                payload = self.read_body()
                return self.user_send(payload)
            if path == USER_PREFIX + 'close':
                payload = self.read_body()
                return self.user_close(payload)
            if path == USER_PREFIX + 'v2/tickets':
                return self.user_v2_create(self.read_body())
            if path == USER_PREFIX + 'v2/message':
                return self.user_v2_message(self.read_body())
            if path == USER_PREFIX + 'v2/upload':
                return self.user_v2_upload(self.read_body(MAX_UPLOAD_BODY))
            if path == USER_PREFIX + 'v2/status':
                return self.user_v2_status(self.read_body())
            if path == USER_PREFIX + 'v2/read':
                return self.user_v2_read(self.read_body())
            if path == ADMIN_PREFIX + 'session':
                return self.operator_bootstrap()
            if path == ADMIN_PREFIX + 'logout':
                return self.operator_logout()
            if path == ADMIN_PREFIX + 'reply':
                return self.operator_reply()
            if path == ADMIN_PREFIX + 'read':
                return self.operator_read()
            if path == ADMIN_PREFIX + 'close':
                return self.operator_close()
            if path == ADMIN_PREFIX + 'v2/reply':
                return self.admin_v2_reply(self.read_body())
            if path == ADMIN_PREFIX + 'v2/note':
                return self.admin_v2_note(self.read_body())
            if path == ADMIN_PREFIX + 'v2/status':
                return self.admin_v2_status(self.read_body())
            if path == ADMIN_PREFIX + 'v2/update':
                return self.admin_v2_update(self.read_body())
            if path == ADMIN_PREFIX + 'v2/settings':
                return self.admin_v2_settings(self.read_body())
            if path == ADMIN_PREFIX + 'v2/upload':
                return self.admin_v2_upload(self.read_body(MAX_UPLOAD_BODY))
            if path == ADMIN_PREFIX + 'v2/draft':
                if self.require_operator_write() is None: return
                return self.respond(503, {'success': False, 'message': 'AI 草稿尚未配置'})
            self.respond(404, {'success': False, 'message': 'not found'})
        except AuthError as error:
            self.respond(401, {'success': False, 'message': str(error)})
        except ValueError as error:
            self.respond(400, {'success': False, 'message': str(error)})
        except PermissionError as error:
            self.respond(429, {'success': False, 'message': str(error)})
        except Exception as error:
            self.log_message('unhandled %s: %s', type(error).__name__, error)
            self.respond(500, {'success': False, 'message': '服务暂时不可用'})

    # -- ticket v2 -------------------------------------------------------
    @staticmethod
    def ticket_view(state):
        keys = ('id', 'subject', 'category', 'priority', 'status', 'assignee_id',
                'created_at', 'updated_at', 'last_message_at', 'last_text', 'last_role',
                'first_response_at', 'resolved_at', 'closed_at', 'unread_user')
        return {key: state.get(key) for key in keys}

    def owned_ticket(self, user, ticket_id):
        state = self.service.store.get(str(ticket_id or ''))
        if state is None:
            rows = self.service.store.search(text=str(ticket_id or ''), limit=2)
            state = next((row for row in rows if row.get('id') == ticket_id), None)
        if state is None or int(state.get('uid') or 0) != int(user['id']):
            raise ValueError('工单不存在')
        return state

    def user_v2_create(self, payload):
        user = self.require_user()
        category = str(payload.get('category') or '')
        subject = str(payload.get('subject') or '').strip()
        description = str(payload.get('description') or '').strip()
        if category not in store.CATEGORIES:
            raise ValueError('请选择问题分类')
        if not subject or len(subject) > 120:
            raise ValueError('工单标题为 1–120 字')
        reason = store.text_error(description, self.max_text())
        if reason: raise ValueError(reason)
        if not self.service.start_uid_gate.allow(user['id']):
            raise PermissionError('创建工单过于频繁，请稍后再试')
        active = [row for row in self.service.store.search(limit=200)
                  if int(row.get('uid') or 0) == int(user['id'])
                  and row.get('status') in store.ACTIVE_STATUSES]
        if len(active) >= CONVERSATIONS_PER_UID:
            raise PermissionError('进行中的工单已达上限，请先解决旧工单')
        priority = self.service.ticket_priority(category, description)
        state = self.service.store.start(user['id'], user.get('username') or
            user.get('display_name') or '', str(payload.get('source') or 'drawer')[:40],
            subject, category, priority, reuse=False)
        row = self.service.store.add_message(state['id'], 'user', description)
        self.service.notify_operator(self.service.store.get(state['id']),
                                     '%s：%s' % (subject, description), priority == 'high')
        return self.respond(201, {'success': True,
                                  'ticket': self.ticket_view(self.service.store.get(state['id'])),
                                  'message': row})

    def user_v2_ticket(self, user, params):
        state = self.owned_ticket(user, str(params.get('id') or ''))
        messages = self.service.store.history(state['id'], int(params.get('since') or 0))
        return self.respond(200, {'success': True, 'ticket': self.ticket_view(state),
                                  'messages': messages})

    def user_v2_message(self, payload):
        user = self.require_user(); state = self.owned_ticket(user, payload.get('ticket_id'))
        if state.get('status') not in store.ACTIVE_STATUSES:
            raise ValueError('请先重新打开工单')
        text = payload.get('text')
        reason = store.text_error(text, self.max_text())
        if reason: raise ValueError(reason)
        if not self.service.send_uid_gate.allow(str(user['id'])):
            raise PermissionError('发送过于频繁，请稍后再试')
        attachments = payload.get('attachments') or []
        if not isinstance(attachments, list) or len(attachments) > MAX_IMAGES_PER_MESSAGE:
            raise ValueError('附件数量无效')
        allowed = []
        for item in attachments:
            record = self.service.store.index.attachment(str(item.get('id') if isinstance(item, dict) else item))
            if not record or record['ticket_id'] != state['id'] or record['uploader_role'] != 'user':
                raise ValueError('附件无效')
            allowed.append({'id': record['id'], 'name': record['original_name'],
                            'mime': record['mime'], 'size': record['size']})
        row = self.service.store.add_message(state['id'], 'user', text, attachments=allowed)
        self.service.notify_operator(self.service.store.get(state['id']), '顾客：%s' % text)
        return self.respond(200, {'success': True, 'message': row,
                                  'ticket': self.ticket_view(self.service.store.get(state['id']))})

    def user_v2_upload(self, payload):
        user = self.require_user(); state = self.owned_ticket(user, payload.get('ticket_id'))
        saved = self.service.save_images(state['id'], payload.get('files'), 'user')
        return self.respond(201, {'success': True, 'attachments': saved})

    def user_v2_status(self, payload):
        user = self.require_user(); state = self.owned_ticket(user, payload.get('ticket_id'))
        action = str(payload.get('action') or '')
        if action == 'resolve': target = 'resolved'
        elif action == 'reopen': target = 'waiting_operator'
        else: raise ValueError('状态操作无效')
        updated = self.service.store.set_status(state['id'], target, by='user')
        return self.respond(200, {'success': True, 'ticket': self.ticket_view(updated)})

    def user_v2_read(self, payload):
        user = self.require_user(); state = self.owned_ticket(user, payload.get('ticket_id'))
        self.service.store.mark_read(state['id'], 'user')
        return self.respond(200, {'success': True})

    def admin_v2_tickets(self, params):
        self.service.store.archive_resolved()
        unread = str(params.get('unread') or '') in ('1', 'true')
        rows = self.service.store.search(text=str(params.get('q') or ''),
            category=str(params.get('category') or ''), status=str(params.get('status') or ''),
            priority=str(params.get('priority') or ''), unread=unread,
            limit=int(params.get('limit') or 100), offset=int(params.get('offset') or 0))
        now = store.utcnow(); overdue = 0; today = store.day_of()
        for row in rows:
            stamp = store._parse_iso(row.get('last_message_at'))
            row['overdue'] = bool(row.get('status') == 'waiting_operator' and stamp and
                                  now - stamp.astimezone(store.dt.timezone.utc) >= store.dt.timedelta(minutes=30))
            overdue += int(row['overdue'])
        all_rows = self.service.store.search(limit=200)
        stats = {'waiting': sum(r.get('status') == 'waiting_operator' for r in all_rows),
                 'overdue': overdue,
                 'unread': sum(int(r.get('unread_operator') or 0) for r in all_rows),
                 'resolved_today': sum(str(r.get('resolved_at') or '').startswith(today) for r in all_rows)}
        return self.respond(200, {'success': True, 'tickets': rows, 'stats': stats})

    def admin_v2_ticket(self, params):
        ticket_id = str(params.get('id') or '')
        state = self.service.store.get(ticket_id)
        if state is None:
            state = next((r for r in self.service.store.search(text=ticket_id, limit=5)
                          if r.get('id') == ticket_id), None)
        if state is None: raise ValueError('工单不存在')
        messages = self.service.store.admin_history(ticket_id)
        history = [r for r in self.service.store.search(limit=200)
                   if int(r.get('uid') or 0) == int(state.get('uid') or 0) and r.get('id') != ticket_id][:8]
        context = {'uid': state.get('uid'), 'username': state.get('username') or '',
                   'balance': None, 'group': None, 'usage': None,
                   'recent_topup': None, 'tickets': history}
        return self.respond(200, {'success': True, 'ticket': state,
                                  'messages': messages, 'context': context})

    def admin_v2_reply(self, payload):
        if self.require_operator_write() is None: return
        state = self.service.store.get(str(payload.get('ticket_id') or ''))
        if state is None: raise ValueError('工单不存在')
        reason = store.text_error(payload.get('text'), self.max_text())
        if reason: raise ValueError(reason)
        attachments = payload.get('attachments') or []
        if not isinstance(attachments, list) or len(attachments) > MAX_IMAGES_PER_MESSAGE:
            raise ValueError('附件数量无效')
        allowed = []
        for item in attachments:
            record = self.service.store.index.attachment(str(item.get('id') if isinstance(item, dict) else item))
            if not record or record['ticket_id'] != state['id'] or record['uploader_role'] != 'operator':
                raise ValueError('附件无效')
            allowed.append({'id': record['id'], 'name': record['original_name'],
                            'mime': record['mime'], 'size': record['size']})
        row = self.service.store.add_message(state['id'], 'operator', payload.get('text'),
                                             attachments=allowed)
        self.service.store.mark_read(state['id'], 'operator')
        return self.respond(200, {'success': True, 'message': row,
                                  'ticket': self.ticket_view(self.service.store.get(state['id']))})

    def admin_v2_note(self, payload):
        if self.require_operator_write() is None: return
        note = self.service.store.add_note(str(payload.get('ticket_id') or ''), payload.get('text'))
        return self.respond(201, {'success': True, 'note': note})

    def admin_v2_status(self, payload):
        if self.require_operator_write() is None: return
        updated = self.service.store.set_status(str(payload.get('ticket_id') or ''),
            str(payload.get('status') or ''), by='operator', reason=str(payload.get('reason') or ''))
        return self.respond(200, {'success': True, 'ticket': self.ticket_view(updated)})

    def admin_v2_update(self, payload):
        if self.require_operator_write() is None: return
        updated = self.service.store.update_ticket(str(payload.get('ticket_id') or ''),
            **{key: payload[key] for key in ('subject','category','priority','assignee_id') if key in payload})
        return self.respond(200, {'success': True, 'ticket': self.ticket_view(updated)})

    def admin_v2_settings(self, payload):
        if self.require_operator_write() is None: return
        values = self.service.update_public_settings(payload)
        return self.respond(200, {'success': True, 'settings': values})

    def admin_v2_upload(self, payload):
        if self.require_operator_write() is None: return
        ticket_id = str(payload.get('ticket_id') or '')
        if self.service.store.get(ticket_id) is None: raise ValueError('工单不存在')
        saved = self.service.save_images(ticket_id, payload.get('files'), 'operator')
        return self.respond(201, {'success': True, 'attachments': saved})

    def attachment(self, user, params, admin):
        record = self.service.store.index.attachment(str(params.get('id') or ''))
        if record is None: raise ValueError('附件不存在')
        state = self.service.store.get(record['ticket_id'])
        if not admin and (state is None or int(state.get('uid') or 0) != int(user['id'])):
            raise ValueError('附件不存在')
        path = (self.service.attachments_folder / record['stored_name']).resolve()
        if path.parent != self.service.attachments_folder.resolve() or not path.is_file():
            raise ValueError('附件不存在')
        self.service.store.append_quiet({'type': 'attachment_access', 'conv': record['ticket_id'],
            'attachment_id': record['id'], 'by': 'operator' if admin else 'user',
            'created_at': store.iso()})
        return self.respond_file(path, record['mime'], record['original_name'])

    # -- user side -------------------------------------------------------
    @staticmethod
    def conversation_view(state, messages=None):
        view = {key: state.get(key) for key in
                ('id', 'uid', 'username', 'source', 'created_at', 'status',
                 'closed_at', 'needs_human', 'handoff_reason', 'ai_rounds',
                 'last_seq', 'last_message_at', 'last_text', 'last_role')}
        if messages is not None:
            view['messages'] = messages
        return view

    def user_open_conversation(self, user):
        state = self.service.store.open_for(user['id'])
        return state

    def user_start(self, payload):
        user = self.require_user()
        config = self.service.config()
        if not config.get('enabled'):
            raise ValueError('客服暂时关闭，请发邮件联系')
        if not self.service.start_gate.allow(self.client_ip()):
            raise PermissionError('发起会话过于频繁，请 %d 秒后再试。'
                                  % self.service.start_gate.retry_after(self.client_ip()))
        state = self.user_open_conversation(user)
        if state is None:
            # Only creating a conversation spends the strict per-account quota.
            # A plain page load returns the open one, so reloading a few times
            # must never lock the visitor out.
            if not self.service.start_uid_gate.allow(user['id']):
                raise PermissionError(
                    '发起新会话过于频繁，请 %d 秒后再试。'
                    % self.service.start_uid_gate.retry_after(user['id']))
            if sum(1 for row in self.service.store.conversations.values()
                   if row['uid'] == user['id']) >= CONVERSATIONS_PER_UID:
                raise PermissionError('你的会话数量已达上限，请先结束旧会话。')
            state = self.service.store.start(user['id'], user.get('username') or
                                             user.get('display_name') or '',
                                             str(payload.get('source') or '')[:40])
        self.service.store.mark_read(state['id'], 'user')
        rows = self.service.store.since(state['id'], 0)
        return self.respond(200, {'success': True,
                                  'conversation': self.conversation_view(state, rows),
                                  'config': self.service.public_config()})

    def user_history(self, user, params):
        state = self.service.store.get(str(params.get('conversation_id') or ''))
        if state is None or state['uid'] != user['id']:
            raise ValueError('会话不存在')
        rows = self.service.store.since(state['id'], int(params.get('since') or 0))
        return self.respond(200, {'success': True, 'conversation_id': state['id'],
                                  'status': state['status'],
                                  'needs_human': state['needs_human'],
                                  'messages': rows})

    def user_send(self, payload):
        user = self.require_user()
        text = payload.get('text')
        state = self.service.store.get(str(payload.get('conversation_id') or ''))
        if state is None or state['uid'] != user['id']:
            raise ValueError('会话不存在')
        reason = store.text_error(text, self.max_text())
        if reason:
            raise ValueError(reason)
        if not self.service.send_global_gate.allow('all'):
            raise PermissionError('客服消息量已达上限，请稍后再试。')
        if not self.service.send_ip_gate.allow(self.client_ip()):
            raise PermissionError('发送过于频繁，请 %d 秒后再试。'
                                  % self.service.send_ip_gate.retry_after(self.client_ip()))
        uid_key = str(user['id'])
        if not self.service.send_uid_gate.allow(uid_key):
            raise PermissionError('发送过于频繁，请 %d 秒后再试。'
                                  % self.service.send_uid_gate.retry_after(uid_key))

        row = self.service.store.add_message(state['id'], 'user', text)
        self.service.store.mark_read(state['id'], 'user')

        keyword = self.service.handoff_reason(text)
        if keyword:
            before = self.service.store.get(state['id'])
            flagged = before.get('needs_human') if before else False
            self.service.store.handoff(state['id'], 'keyword:%s' % keyword)
            if not flagged:
                self.service.store.add_message(state['id'], 'system',
                                               '已记录为需要人工处理。')
        self.service.notify_operator(self.service.store.get(state['id']),
                                     '顾客：%s' % text, urgent=bool(keyword))
        return self.respond(200, {'success': True, 'message': row,
                                  'needs_human': self.service.store.get(state['id'])['needs_human']})

    def user_close(self, payload):
        user = self.require_user()
        state = self.service.store.get(str(payload.get('conversation_id') or ''))
        if state is None or state['uid'] != user['id']:
            raise ValueError('会话不存在')
        self.service.store.close(state['id'], by='user', reason='user closed')
        return self.respond(200, {'success': True, 'message': '会话已结束。'})

    # -- operator side ---------------------------------------------------
    def operator_bootstrap(self):
        """Turn the operator's console token into the shared session cookie.

        The gateway is the only thing that decides who is an administrator, and
        the cookie it earns here is the same one the recharge service accepts --
        one sign-in, both surfaces.
        """
        payload = self.read_body()
        admin = user_identity.admin_for(self.service.gateway_url,
                                       payload.get('access_token'),
                                       user_identity.ADMIN_ROLE)
        if admin is None:
            return self.respond(403, {'success': False, 'message': '需要本站管理员登录态'})
        cookie = operator_session.cookie_header(
            operator_session.sign_session(self.service.secrets_doc,
                                          int(time.time()) + operator_session.COOKIE_TTL,
                                          'na:%s' % admin['id'],
                                          self.service.generation()),
            operator_session.COOKIE_TTL,
            secure=not self.service.allow_insecure_cookie)
        return self.respond(200, {'success': True, 'user': admin}, {'Set-Cookie': cookie})

    def operator_logout(self):
        """Cut off every operator session, in this service and in recharge.

        Logging out of the console revokes the console's own session only, so
        without this the money surface kept accepting the old cookie for hours
        (8h TTL) after the operator believed they had signed out -- on a shared
        computer the next person could then credit quotas. The counter is shared
        between both services, so one bump closes both at once.
        """
        if self.require_operator_write() is None:
            return
        operator_session.bump_generation(self.service.state_path)
        return self.respond(200, {'success': True},
                            {'Set-Cookie': operator_session.cookie_header(
                                '', 0, secure=not self.service.allow_insecure_cookie)})

    def operator_reply(self):
        if self.require_operator_write() is None:
            return
        payload = self.read_body()
        state = self.service.store.get(str(payload.get('conversation_id') or ''))
        if state is None:
            raise ValueError('会话不存在')
        reason = store.text_error(payload.get('text'), self.max_text())
        if reason:
            raise ValueError(reason)
        row = self.service.store.add_message(state['id'], 'operator', payload.get('text'))
        self.service.store.mark_read(state['id'], 'operator')
        return self.respond(200, {'success': True, 'message': row})

    def operator_read(self):
        if self.require_operator_write() is None:
            return
        payload = self.read_body()
        state = self.service.store.get(str(payload.get('conversation_id') or ''))
        if state is None:
            raise ValueError('会话不存在')
        self.service.store.mark_read(state['id'], 'operator')
        return self.respond(200, {'success': True})

    def operator_close(self):
        if self.require_operator_write() is None:
            return
        payload = self.read_body()
        state = self.service.store.get(str(payload.get('conversation_id') or ''))
        if state is None:
            raise ValueError('会话不存在')
        self.service.store.close(state['id'], by='operator',
                                 reason=str(payload.get('reason') or '')[:200])
        return self.respond(200, {'success': True, 'message': '会话已结束。'})

    # -- streams ---------------------------------------------------------
    def user_stream(self, user, params):
        conv_id = str(params.get('conversation_id') or '')
        state = self.service.store.get(conv_id)
        if state is None or state['uid'] != user['id']:
            raise ValueError('会话不存在')
        if not self.stream_slots():
            return
        try:
            self.sse_start()
            last = int(params.get('since') or 0)
            last_state = None
            deadline = time.time() + MAX_STREAM_SECONDS
            if not self.sse('ready', {'conversation_id': conv_id, 'since': last}):
                return
            while time.time() < deadline:
                rows, revision = self.service.store.snapshot(conv_id, last)
                for row in rows:
                    if not self.sse('message', row):
                        return
                    last = row['seq']
                current = self.service.store.get(conv_id)
                if current is None:
                    return
                view = {'status': current['status'], 'needs_human': current['needs_human'],
                        'ai_rounds': current['ai_rounds']}
                # Only on change: a heartbeat every 20s that repeats the same state
                # would make the client re-render for nothing.
                if view != last_state:
                    last_state = dict(view)
                    if not self.sse('state', view):
                        return
                if current['status'] not in store.OPEN_STATUSES:
                    return
                if not self.sse_wait(revision):
                    return
        finally:
            self.service.release_stream()

    def operator_stream(self, params):
        if not self.stream_slots():
            return
        try:
            self.sse_start()
            seen = {conv: state['last_seq']
                    for conv, state in self.service.store.conversations.items()}
            last_listing = 0.0
            deadline = time.time() + MAX_STREAM_SECONDS
            if not self.sse('ready', {'counts': self.service.store.counts()}):
                return
            while time.time() < deadline:
                # Snapshot the revision and the conversation list under the same
                # lock the writers hold, so a message cannot slip in between them.
                with self.service.store.condition:
                    revision = self.service.store.revision
                    indexed = list(self.service.store.conversations.items())
                appeared = False
                for conv, state in indexed:
                    if seen.get(conv, 0) == state['last_seq']:
                        continue
                    appeared = True
                    for row in self.service.store.since(conv, seen.get(conv, 0)):
                        row = dict(row, conv=conv)
                        if not self.sse('message', row):
                            return
                    seen[conv] = state['last_seq']
                now = time.time()
                if appeared or now - last_listing >= 5:
                    if not self.sse('conversations',
                                    {'counts': self.service.store.counts(),
                                     'conversations': self.service.store.listing()}):
                        return
                    last_listing = now
                if not self.sse_wait(revision):
                    return
        finally:
            self.service.release_stream()


def handler_factory(service):
    return type('BoundHandler', (Handler,), {'service': service})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', default=os.environ.get('SUPPORT_ADDR', '0.0.0.0'))
    parser.add_argument('--port', type=int, default=int(os.environ.get('SUPPORT_PORT', '8790')))
    parser.add_argument('--secrets', type=Path,
                        default=Path(os.environ.get('SUPPORT_SECRETS',
                                                    str(ROOT / '.secrets/topup-admin.json'))))
    parser.add_argument('--events', type=Path,
                        default=Path(os.environ.get('SUPPORT_EVENTS',
                                                    str(ROOT / 'runtime/support'))))
    parser.add_argument('--state', type=Path,
                        default=Path(os.environ.get('SUPPORT_STATE',
                                                    str(ROOT / 'runtime/admin-session-state.json'))))
    parser.add_argument('--config', type=Path, default=ROOT / 'config/support.json')
    parser.add_argument('--notify-secrets', type=Path,
                        default=Path(os.environ.get('SUPPORT_NOTIFY_SECRETS',
                                                    str(ROOT / '.secrets/support-notify.json'))))
    parser.add_argument('--gateway-url', default=os.environ.get('SUPPORT_GATEWAY_URL') or None)
    parser.add_argument('--public-origin', default=os.environ.get('SUPPORT_PUBLIC_ORIGIN') or None)
    parser.add_argument('--check', action='store_true', help='自检配置与依赖，不监听端口')
    parser.add_argument('--prune', type=int, metavar='DAYS',
                        help='删除超过 N 天的会话日志（默认只预演，加 --apply 才删）')
    parser.add_argument('--apply', action='store_true', help='配合 --prune 才真的删除')
    args = parser.parse_args()

    if args.prune is not None:
        report = store.prune(args.events, args.prune, apply=args.apply)
        if args.apply:
            index = store.SupportIndex(Path(args.events) / 'support-index.sqlite3')
            index.reset(); report['index'] = index.replay(args.events); index.close()
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return

    service = Service(args.secrets, args.events, args.state, args.config,
                      gateway_url=args.gateway_url,
                      allow_insecure_cookie=os.environ.get('SUPPORT_INSECURE_COOKIE') == '1',
                      public_origin=args.public_origin,
                      notify_secrets_path=args.notify_secrets)

    if args.check:
        config = service.config()
        snapshot = {
            'status': 'PASS' if service.secrets_doc else 'BLOCKED',
            'secrets': {'path': str(args.secrets), 'present': service.secrets_doc is not None},
            'config': {'path': str(args.config),
                       'enabled': config.get('enabled'),
                       'notify_channel': (config.get('notify') or {}).get('channel'),
                       'ai_enabled': (config.get('ai') or {}).get('enabled'),
                       'handoff_keywords': len(config.get('handoff_keywords') or [])},
            'events': {'folder': str(args.events),
                       'counts': service.store.counts(),
                       'files': [path.name for path in store.day_files(args.events)]},
            'notify_credentials': {
                'path': str(args.notify_secrets),
                'present': bool(notifier.load_secrets(args.notify_secrets))},
            'gateway_url': service.gateway_url,
            'public_origin': service.public_origin,
            'max_streams': MAX_STREAMS,
        }
        print(json.dumps(snapshot, ensure_ascii=False, indent=2))
        return

    if service.secrets_doc is None:
        raise SystemExit('BLOCKED: 经营者会话密钥未初始化；先运行充值服务的 --init-secrets')
    server = ThreadingHTTPServer((args.host, args.port), handler_factory(service))
    server.daemon_threads = True
    print(json.dumps({'status': 'LISTENING', 'address': '%s:%d' % (args.host, args.port),
                      'events': str(args.events)}, ensure_ascii=False), flush=True)
    server.serve_forever()


if __name__ == '__main__':
    main()
