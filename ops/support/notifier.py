#!/usr/bin/env python3
"""把「有新消息」推到经营者手机上。

出口都是普通 HTTP，不引 SDK：

    wecom     企业微信群机器人 webhook。**只能单向推**：在通知里回复不会回到本平台，
              所以消息里带一个深链，点开用手机浏览器回复。
    bark      Bark（iOS），GET 一个带路径的地址即可。
    telegram  Telegram Bot 的 sendMessage。配合 webhook 才能做到「在聊天窗里直接回」，
              本模块只负责推。
    none      不推送（默认）。配置里没写通道时就是它。

凭据只在 `.secrets/support-notify.json`，不进配置文件、不进仓库。

**推送失败绝不静默**：每次调用都返回结构化结果，由调用方写成 `notify` 事件留在日志里，
这样「消息没推到我手机上」是能查的，而不是靠猜。
"""
import json
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

CHANNELS = ('none', 'wecom', 'bark', 'telegram')
TIMEOUT = 8
MESSAGE_LIMIT = 300          # 通知里正文的截断长度：通知栏不是聊天窗口


def load_secrets(path):
    path = Path(path)
    if not path.exists():
        return {}
    try:
        document = json.loads(path.read_text(encoding='utf-8'))
    except ValueError:
        return {}
    return document if isinstance(document, dict) else {}


def _text(title, body, link):
    body = ' '.join(str(body or '').split())
    if len(body) > MESSAGE_LIMIT:
        body = body[:MESSAGE_LIMIT] + '…'
    lines = [title, body]
    if link:
        lines.append(link)
    return '\n'.join(line for line in lines if line)


def build(channel, secrets, title, body, link=''):
    """Return (url, payload_bytes_or_None, method) for a channel, or None when unusable."""
    section = (secrets or {}).get(channel) or {}
    text = _text(title, body, link)
    if channel == 'wecom':
        webhook = str(section.get('webhook') or '')
        if not webhook.startswith('https://'):
            return None
        payload = json.dumps({'msgtype': 'text', 'text': {'content': text}},
                            ensure_ascii=False).encode('utf-8')
        return webhook, payload, 'POST'
    if channel == 'bark':
        base = str(section.get('url') or '')
        if not base.startswith('http'):
            return None
        url = base.rstrip('/') + '/' + urllib.parse.quote(title or 'OmniAPI') + '/' \
            + urllib.parse.quote(_text('', body, link).strip())
        return url, None, 'GET'
    if channel == 'telegram':
        token = str(section.get('token') or '')
        chat_id = str(section.get('chat_id') or '')
        if not token or not chat_id:
            return None
        url = 'https://api.telegram.org/bot%s/sendMessage' % token
        payload = json.dumps({'chat_id': chat_id, 'text': text,
                              'disable_web_page_preview': True},
                             ensure_ascii=False).encode('utf-8')
        return url, payload, 'POST'
    return None


def default_transport(request, timeout):
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return response.status, response.read(2048).decode('utf-8', 'replace')


def publish(channel, secrets, title, body, link='', transport=None, timeout=TIMEOUT):
    """Send one notification. Never raises: the caller records the outcome."""
    channel = str(channel or 'none')
    if channel == 'none':
        return {'channel': 'none', 'ok': False, 'skipped': True,
                'error': '未配置推送通道'}
    if channel not in CHANNELS:
        return {'channel': channel, 'ok': False, 'error': '未知的推送通道'}
    built = build(channel, secrets, title, body, link)
    if built is None:
        return {'channel': channel, 'ok': False,
                'error': '推送凭据缺失或不完整（见 .secrets/support-notify.json）'}
    url, payload, method = built
    request = urllib.request.Request(url, data=payload, method=method)
    if payload is not None:
        request.add_header('Content-Type', 'application/json')
    started = time.time()
    try:
        status, raw = (transport or default_transport)(request, timeout)
    except urllib.error.HTTPError as error:
        return {'channel': channel, 'ok': False, 'status': error.code,
                'error': 'HTTP %s' % error.code, 'seconds': round(time.time() - started, 2)}
    except Exception as error:                       # noqa: BLE001 - report, never raise
        return {'channel': channel, 'ok': False,
                'error': '%s: %s' % (type(error).__name__, error),
                'seconds': round(time.time() - started, 2)}
    ok = 200 <= int(status) < 300
    error_text = '' if ok else ('HTTP %s' % status)
    # WeCom reports application errors in a HTTP 200 JSON body. Treating every
    # 2xx as delivery would make a revoked webhook look healthy in the audit log.
    if ok and channel == 'wecom':
        try:
            parsed = json.loads(raw or '{}')
            ok = int(parsed.get('errcode', -1)) == 0
            if not ok:
                error_text = '企业微信 errcode=%s' % parsed.get('errcode', 'unknown')
        except (TypeError, ValueError):
            ok = False
            error_text = '企业微信返回格式无效'
    return {'channel': channel, 'ok': ok, 'status': status,
            'seconds': round(time.time() - started, 2),
            'error': error_text,
            'response': (raw or '')[:200]}


class Throttle:
    """Merge a burst of messages into one notification.

    A customer typing five short lines must not buzz the operator's phone five
    times; the workbench is the place to read the detail.
    """

    def __init__(self, min_interval_seconds=60, clock=time.time):
        self.min_interval = max(0, int(min_interval_seconds))
        self.clock = clock
        self.last = {}

    def allow(self, key, interval=None):
        gap = self.min_interval if interval is None else max(0, int(interval))
        now = self.clock()
        previous = self.last.get(key)
        if previous is not None and now - previous < gap:
            return False
        self.last[key] = now
        return True

    def reset(self, key):
        self.last.pop(key, None)
