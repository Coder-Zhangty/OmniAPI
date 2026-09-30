#!/usr/bin/env python3
"""Recharge request service for OmniAPI.

The recharge flow is manual: the user scans the operator's QR code and the
operator confirms the money arrived before credit is issued. This service keeps
the piece that was missing - telling the operator that a user paid.

It is reached through Nginx and serves two audiences:

- Public:  POST /brand/api/topup/request
           The recharge page posts the account and the amount the user is about
           to pay. Strictly validated, rate limited, and recorded as a claim.
- Admin:   /brand/admin/api/*  (password protected, HttpOnly cookie)
           Lists pending claims and issues the matching redemption code in one
           click, through the same ledger the CLI uses.

It is deliberately not a payment callback: it never credits anything on its own.

Runs unprivileged under systemd. Bound to the host; the Nginx container reaches
it through host.docker.internal.
"""
import argparse
import base64
import datetime as dt
import hashlib
import json
import os
import re
import string
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
from io import BytesIO
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from decimal import Decimal
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
sys.path.insert(0, str(ROOT / 'ops'))
sys.path.insert(0, str(ROOT / 'ops' / 'support'))

import credit_topup  # noqa: E402
import fx_rates  # noqa: E402
import notifier  # noqa: E402
import operator_session  # noqa: E402
import user_identity  # noqa: E402
import user_notices  # noqa: E402
import topup_requests as queue  # noqa: E402
from gateway import API, Stack  # noqa: E402

# Re-exported so existing callers (the unit tests import `sign_session` and
# `session_scope` straight from this module) keep working now that the
# implementation lives in the shared `operator_session` module.
from operator_session import (  # noqa: E402,F401
    AuthError,
    COOKIE_NAME, COOKIE_PATH, COOKIE_TTL, PASSWORD_ALPHABET, SCOPE,
    SECRET_ITERATIONS, csrf_token, generate_password, hash_password,
    init_secrets, load_json as load_secrets, password_matches, session_scope,
    session_valid, sign_session,
)

MAX_BODY = 8192
MAX_UPLOAD_BODY = 1500000
SUBMIT_LIMIT = (10, 600)         # per source address; office/campus NAT shares one
SUBMIT_GLOBAL_LIMIT = (60, 600)  # whole service, the real backstop
LOGIN_LIMIT = (8, 900)           # per source address
IDENTIFY_LIMIT = (20, 600)       # per source address; bounds gateway lookups
DUPLICATE_WINDOW = 600           # seconds; an identical pending claim is reused
MAX_TOKEN_LENGTH = 4096          # bound on the dashboard token we forward
NAME_CACHE_LIMIT = 512           # bound the uid -> display name cache
# New API role levels: 1 =普通用户, 10 = 管理员, 100 = 站长.
ADMIN_ROLE = 10

SUBMIT_PATH = '/brand/api/topup/request'
QUOTE_PATH = '/brand/api/topup/quote'
FX_PATH = '/brand/api/fx/rates'
METHODS_PATH = '/brand/api/payment-methods'
ASSET_PREFIX = '/brand/api/payment-assets/'
NOTICES_PATH = '/brand/api/user-notices'
NOTICE_READ_PATH = '/brand/api/user-notices/read'
ADMIN_PREFIX = '/brand/admin/api/'
OVERDUE_SECONDS = 300

# Fields config/payment-methods.json is allowed to supply. Anything else in that
# file is ignored, so a typo cannot inject an unexpected key into a quote.
METHOD_FIELDS = ('id', 'kind', 'title', 'currency', 'minimum', 'enabled', 'sort',
                 'link', 'qr_path', 'instructions', 'asset', 'network', 'address')
# Human-readable causes for falling back to the declared rate, so the operator
# sees why on the admin page instead of a raw enum.
FALLBACK_REASONS = {
    'rate_missing': '行情源未返回人民币报价',
    'quote_time_unknown': '行情源未提供报价时间',
    'quote_too_old': '行情源报价已超出可用时限',
    'unavailable': '本地尚无行情快照',
    'disabled': '汇率服务未启用',
}
# Rails that must always exist on the admin page. Operator values override the
# skeleton; the skeleton itself only carries identity and safe defaults.
BUILTIN_METHODS = (
    {'id': 'payoneer', 'kind': 'payoneer', 'title': 'Payoneer', 'currency': 'USD',
     'minimum': '20', 'enabled': False, 'sort': 20, 'link': '', 'qr_path': '',
     'instructions': '仅收 USD，付款后填写 Payoneer 交易参考号。'},
    {'id': 'usdt-trc20', 'kind': 'crypto', 'title': 'USDT · TRC20', 'currency': 'USD',
     'asset': 'USDT', 'network': 'TRC20', 'minimum': '20', 'enabled': False, 'sort': 30,
     'address': '', 'qr_path': '', 'instructions': '仅收 USDT-TRC20，付款后填写链上交易哈希。'},
    {'id': 'usdt-erc20', 'kind': 'crypto', 'title': 'USDT · ERC20', 'currency': 'USD',
     'asset': 'USDT', 'network': 'ERC20', 'minimum': '20', 'enabled': False, 'sort': 40,
     'address': '', 'qr_path': '', 'instructions': '仅收 USDT-ERC20，付款后填写链上交易哈希。'},
    {'id': 'usdc-erc20', 'kind': 'crypto', 'title': 'USDC · ERC20', 'currency': 'USD',
     'asset': 'USDC', 'network': 'ERC20', 'minimum': '20', 'enabled': False, 'sort': 50,
     'address': '', 'qr_path': '', 'instructions': '仅收 USDC-ERC20，付款后填写链上交易哈希。'},
    {'id': 'usdc-solana', 'kind': 'crypto', 'title': 'USDC · Solana', 'currency': 'USD',
     'asset': 'USDC', 'network': 'Solana', 'minimum': '20', 'enabled': False, 'sort': 60,
     'address': '', 'qr_path': '', 'instructions': '仅收 USDC-Solana，付款后填写链上交易哈希。'},
)


def utcnow():
    return dt.datetime.now(dt.timezone.utc)


def iso(stamp=None):
    return (stamp or utcnow()).isoformat(timespec='seconds')


class SessionMissing(RuntimeError):
    """No gateway administrator session is seeded in this process yet.

    Deliberately distinct from a rejected token. A rejected token can be
    rebuilt by retrying; this one cannot be fixed by the service at all, because
    the only thing that can mint one is the operator's browser (the gateway's
    login is behind Cloudflare Turnstile). Retrying it is pure waste and calling
    `forget_api()` on it is actively harmful -- there is nothing to forget.

    Mapped to HTTP 409 with a machine-readable code so the admin page can
    re-seed the session and retry, instead of showing a dead end.
    """

    code = 'gateway_session_missing'


class Service:
    """Holds the configuration, the rate gates and the reused admin session."""

    def __init__(self, secrets_path, events_path, ledger_path, config_path,
                 gateway_url=None, allow_insecure_cookie=False, quotes_path=None,
                 fx_cache_path=None, xe_secrets_path=None, payment_settings_path=None,
                 payment_assets_path=None, methods_config_path=None, session_state_path=None,
                 public_origin=None, notify_secrets_path=None, notify_config_path=None):
        self.secrets_path = Path(secrets_path)
        self.events_path = Path(events_path)
        self.ledger_path = Path(ledger_path)
        self.config_path = Path(config_path)
        runtime = self.events_path.parent
        self.quotes_path = Path(quotes_path or runtime / 'topup-quotes.jsonl')
        self.fx_cache_path = Path(fx_cache_path or runtime / 'fx-rates.json')
        self.xe_secrets_path = Path(xe_secrets_path or ROOT / '.secrets/xe.json')
        self.payment_settings_path = Path(payment_settings_path or runtime / 'payment-settings.json')
        self.payment_assets_path = Path(payment_assets_path or runtime / 'payment-assets')
        self.methods_config_path = Path(methods_config_path or
                                        ROOT / 'config/payment-methods.json')
        self.session_state_path = Path(session_state_path or runtime / 'admin-session-state.json')
        self.public_origin = (public_origin or os.environ.get('TOPUP_WATCH_PUBLIC_ORIGIN') or
                              'https://omniapi.biz').rstrip('/')
        self.notify_secrets_path = Path(notify_secrets_path or
            os.environ.get('TOPUP_NOTIFY_SECRETS') or ROOT / '.secrets/support-notify.json')
        self.notify_config_path = Path(notify_config_path or
            os.environ.get('TOPUP_NOTIFY_CONFIG') or ROOT / 'config/support.json')
        self.notices_path = Path(os.environ.get('TOPUP_USER_NOTICES') or
                                 runtime / 'user-notices.jsonl')
        self.gateway_url = gateway_url
        self.allow_insecure_cookie = allow_insecure_cookie
        self.secrets_doc = load_secrets(self.secrets_path)
        # 代际号**故意不缓存**：见 generation()。
        self.submit_gate = queue.SlidingWindow(*SUBMIT_LIMIT)
        self.submit_global_gate = queue.SlidingWindow(*SUBMIT_GLOBAL_LIMIT)
        self.login_gate = queue.SlidingWindow(*LOGIN_LIMIT)
        self.identify_gate = queue.SlidingWindow(*IDENTIFY_LIMIT)
        self.lock = threading.Lock()
        self._api = None
        self._names = {}
        self._fx_error = None

    def notices_for(self, user):
        return user_notices.unread(self.notices_path, int(user['id']),
                                   int(user.get('role') or 1))

    def acknowledge_notice(self, user, notice_id):
        return user_notices.acknowledge(self.notices_path, str(notice_id or ''),
                                        int(user['id']), int(user.get('role') or 1))

    def escalate_overdue(self, now=None):
        """Escalate unverified claims after five minutes without minting credit."""
        now = now or utcnow()
        escalated = []
        with self.lock:
            for record in queue.pending(self.events_path):
                if record.get('escalated_at'):
                    continue
                try:
                    created = dt.datetime.fromisoformat(str(record['created_at']).replace('Z', '+00:00'))
                except (KeyError, ValueError):
                    continue
                if (now - created).total_seconds() < OVERDUE_SECONDS:
                    continue
                queue.append_event({'type': 'escalated', 'id': record['id'], 'at': iso(now),
                                    'reason': 'operator_timeout_5m'}, self.events_path)
                title = '充值申请已等待超过 5 分钟'
                amount = record.get('quoted_amount') or record.get('cny') or ''
                currency = record.get('currency') or 'CNY'
                body = ('订单 %s（用户 %s，%s %s）尚未核验。系统已标记为“超时先行受理”，'
                        '但未发放可消费额度；请核实到账后确认，未到账可随时驳回。' %
                        (record['id'], record.get('uid'), amount, currency))
                user_notices.publish(self.notices_path, title, body, audience='admin',
                                     notice_id='topup-overdue:' + record['id'],
                                     href='/brand/admin.html', action='立即核验', kind='topup-overdue')
                escalated.append((record, title, body))
        for record, title, body in escalated:
            try:
                config = json.loads(self.notify_config_path.read_text(encoding='utf-8'))
            except (OSError, ValueError):
                config = {}
            channel = str((config.get('notify') or {}).get('channel') or 'none')
            result = notifier.publish(channel, notifier.load_secrets(self.notify_secrets_path),
                                      title, body, self.public_origin + '/brand/admin.html')
            try:
                queue.append_event({'type': 'escalation_notification', 'id': record['id'],
                                    'channel': channel,
                                    'status': 'ok' if result.get('ok') else 'failed',
                                    'error': str(result.get('error') or '')[:200],
                                    'at': iso()}, self.events_path)
            except OSError:
                pass
        return [row[0]['id'] for row in escalated]

    def notify_topup(self, record):
        """Push a pending-payment reminder without treating it as proof."""
        try:
            config = json.loads(self.notify_config_path.read_text(encoding='utf-8'))
        except (OSError, ValueError):
            config = {}
        channel = str((config.get('notify') or {}).get('channel') or 'none')
        if channel == 'none':
            return {'channel': 'none', 'ok': False, 'skipped': True}
        method = self.method(record.get('method')) or {}
        title = 'OmniAPI 充值待核验'
        account = record.get('username') or ('用户 %s' % record.get('uid'))
        amount = record.get('quoted_amount') or record.get('cny') or ''
        currency = record.get('currency') or ('CNY' if record.get('cny') else 'USD')
        body = ('订单：%s\n用户：%s\n方式：%s\n申报：%s %s\n'
                '请核实实际到账后再入账。' %
                (record.get('id'), account,
                 method.get('title') or record.get('method') or '人工充值',
                 amount, currency))
        result = notifier.publish(
            channel, notifier.load_secrets(self.notify_secrets_path), title, body,
            self.public_origin + '/brand/admin.html')
        try:
            queue.append_event({'type': 'notification', 'id': record.get('id'),
                                'channel': channel,
                                'status': 'ok' if result.get('ok') else 'failed',
                                'error': str(result.get('error') or '')[:200],
                                'at': iso()}, self.events_path)
        except OSError:
            pass
        return result

    def generation(self):
        """Read the revocation counter from disk, right now.

        It must NOT be cached in memory. The counter is shared with the support
        service, so whoever writes it -- logging out there, or rotating the
        password -- has to be seen here immediately. A cached value kept
        honouring a cookie that had just been revoked, which the logout probe
        caught: `authenticated` stayed true on the recharge side after logging
        out on the support side.
        """
        return operator_session.load_generation(self.session_state_path)

    def revoke_sessions(self):
        """Invalidate every operator cookie, in this service and in support.

        Delegated to the shared helper so there is exactly one writer of that
        counter. Only the counter is bumped; both services read it per request,
        so neither needs to be told.
        """
        return operator_session.bump_generation(self.session_state_path)

    def default_methods(self):
        """Built-in CNY entry plus the operator-maintained methods file.

        The file is re-read on every call on purpose: the operator edits
        config/payment-methods.json to change a collection link or a wallet
        address, and that change has to take effect without editing Python or
        restarting the service.
        """
        config = self.config()
        methods = [{
            'id': 'cny-qr', 'kind': 'cny_qr', 'title': '微信 / 支付宝扫码',
            'currency': 'CNY', 'minimum': str(config.get('min_amount_cny') or 1),
            'enabled': True, 'sort': 10, 'qr_path': config.get('qr_path') or '',
            'instructions': '请支付准确金额，到账后提交申请。',
        }]
        configured = {row['id']: row for row in self.methods_config()}
        for skeleton in BUILTIN_METHODS:
            row = dict(skeleton)
            row.update(configured.pop(skeleton['id'], {}))
            methods.append(row)
        if configured:
            # A typo must not silently drop a payment rail.
            raise ValueError('config/payment-methods.json 含未知支付方式：' +
                             ', '.join(sorted(configured)))
        return methods

    def methods_config(self):
        """Operator-owned method definitions; invalid entries are skipped."""
        path = self.methods_config_path
        if not path.exists():
            return []
        document = json.loads(path.read_text(encoding='utf-8'))
        rows = document.get('methods') if isinstance(document, dict) else document
        cleaned = []
        for row in rows or []:
            if not isinstance(row, dict) or not row.get('id'):
                continue
            cleaned.append({key: row[key] for key in METHOD_FIELDS if key in row})
        return cleaned

    def method_overrides(self):
        """Admin toggles from runtime/payment-settings.json.

        Only `enabled` and `sort` are honoured. Version 1 of that file stored
        whole method rows, so a legacy file is read for its toggles only — it
        must never be able to resurrect a stale collection link.
        """
        if not self.payment_settings_path.exists():
            return {}
        try:
            data = json.loads(self.payment_settings_path.read_text(encoding='utf-8'))
        except (ValueError, OSError):
            return {}
        result = {}
        for method_id, value in (data.get('overrides') or {}).items():
            if isinstance(value, dict):
                result[str(method_id).strip().lower()] = value
        for row in (data.get('methods') or []):
            if isinstance(row, dict) and row.get('id'):
                kept = {'enabled': bool(row.get('enabled'))}
                if row.get('sort') is not None:
                    kept['sort'] = row['sort']
                result.setdefault(str(row['id']).strip().lower(), kept)
        return result

    @staticmethod
    def complete_method(row):
        """Whether a method has everything it needs to actually receive money."""
        if not (row.get('id') and row.get('currency') and row.get('minimum')):
            return False
        if row.get('kind') == 'crypto':
            return bool(row.get('asset') and row.get('network') and row.get('address')
                        and row.get('qr_path'))
        if row.get('kind') == 'payoneer':
            return bool(row.get('link'))
        return True

    def payment_methods(self, public=False):
        overrides = self.method_overrides()
        result = []
        for method in self.default_methods():
            row = self.merged_method(method, overrides.get(str(method['id']).strip().lower()))
            if public and (not row.get('enabled') or not self.complete_method(row)):
                continue
            result.append(row)
        return sorted(result, key=lambda row: (int(row.get('sort') or 0), row.get('id', '')))

    @staticmethod
    def merged_method(method, override):
        row = dict(method)
        override = override or {}
        if 'enabled' in override:
            row['enabled'] = bool(override['enabled'])
        if override.get('sort') is not None:
            row['sort'] = override['sort']
        if override.get('qr_path'):
            row['qr_path'] = override['qr_path']
        return row

    def save_payment_methods(self, methods, operator='admin'):
        """Persist what the admin page may change: the switch, sort, and its own uploads.

        Collection links, wallet addresses, minimums and instructions belong to
        the operator and live in config/payment-methods.json. Accepting them here
        would let an admin-page session rewrite where money is sent, so they are
        ignored — which also means an accidental save can no longer wipe them.
        An uploaded QR is accepted because it is an asset this service created
        itself, and only when the path resolves inside the managed directory.
        """
        known = {row['id']: row for row in self.default_methods()}
        stored = self.method_overrides()
        overrides = {}
        for incoming in methods if isinstance(methods, list) else []:
            if not isinstance(incoming, dict):
                raise ValueError('支付方式格式无效')
            method_id = str(incoming.get('id') or '').strip().lower()
            if method_id not in known:
                raise ValueError('未知支付方式：' + method_id)
            override = {'enabled': bool(incoming.get('enabled')),
                        'sort': int(incoming.get('sort') or known[method_id].get('sort') or 0)}
            uploaded = self.managed_asset(str(incoming.get('qr_path') or '').strip())
            if not uploaded:
                # Carry a previously uploaded QR forward: a payload that omits it
                # must not silently revert the rail to its config-time image.
                uploaded = self.managed_asset(str((stored.get(method_id) or {})
                                                  .get('qr_path') or '').strip())
            if uploaded:
                override['qr_path'] = uploaded
            merged = self.merged_method(known[method_id], override)
            if merged['enabled'] and not self.complete_method(merged):
                raise ValueError(
                    '%s 尚未配置完整（缺收款链接、钱包地址或二维码）。'
                    '请先在 config/payment-methods.json 中填写后再启用。'
                    % (merged.get('title') or method_id))
            overrides[method_id] = override
        missing = sorted(set(known) - set(overrides))
        if missing:
            raise ValueError('缺少支付方式：' + ', '.join(missing))
        payload = {'version': 2, 'updated_at': iso(), 'updated_by': operator,
                   'overrides': overrides}
        fx_rates.atomic_json(self.payment_settings_path, payload)
        queue.append_event({'type': 'payment_settings', 'at': payload['updated_at'],
                            'by': operator,
                            'enabled': sorted(k for k, v in overrides.items() if v['enabled'])},
                           self.events_path.parent / 'payment-settings-audit.jsonl')
        return payload

    def managed_asset(self, path):
        """Accept a QR path only when it is one of this service's own uploads."""
        if not path.startswith(ASSET_PREFIX):
            return ''
        name = path[len(ASSET_PREFIX):]
        if not re.match(r'^[a-z0-9-]+\.(png|jpe?g|webp)$', name):
            return ''
        return path if (self.payment_assets_path / name).exists() else ''

    def method(self, method_id):
        return next((row for row in self.payment_methods(public=True)
                     if row['id'] == str(method_id or '').strip()), None)

    def fx_snapshot(self, refresh_if_needed=True):
        config = self.config().get('fx') or {}
        limit = config.get('max_age_seconds')
        snapshot = fx_rates.load(self.fx_cache_path, max_age=limit)
        if not config.get('enabled'):
            snapshot['enabled'] = False
            return snapshot
        if refresh_if_needed and fx_rates.needs_refresh(
                snapshot, config.get('refresh_seconds', 300)):
            try:
                snapshot = fx_rates.refresh(self.xe_secrets_path, self.fx_cache_path,
                                            provider=config.get('provider', 'xe'),
                                            max_age=limit)
                self._fx_error = None
            except Exception as error:
                # Keep serving the last good snapshot. quote() labels how old it
                # is and refuses it once past the window, so a broken feed
                # degrades into a labelled rate rather than a 500.
                self._fx_error = '%s: %s' % (type(error).__name__, error)
                snapshot = fx_rates.load(self.fx_cache_path, max_age=limit)
        snapshot['enabled'] = True
        if self._fx_error:
            snapshot['refresh_error'] = self._fx_error
        return snapshot

    def cny_rate(self):
        """Resolve the CNY rate, preferring market data and falling back loudly.

        Returns ``(rate, source, as_of, note)``. ``note`` is None for a normal
        market rate and carries the warning shown to the customer whenever
        anything less than a fresh quote had to be used.

        The fallback ladder is deliberate: the last real quote beats a hand-set
        number, because it is closer to the market and carries its own quote
        time; the operator's declared rate is the last resort before refusing.
        """
        config = self.config()
        fx = config.get('fx') or {}
        fallback_window = int(fx.get('fallback_max_age_seconds') or fx_rates.FALLBACK_MAX_AGE)
        snapshot = self.fx_snapshot()
        if snapshot.get('enabled'):
            verdict = fx_rates.evaluate(snapshot, 'CNY',
                                        max_age=fx.get('max_age_seconds'))
            if verdict['usable']:
                source = snapshot.get('source') or 'market rate'
                if verdict['age_seconds'] >= int(fx.get('refresh_seconds', 300)):
                    source += '（最新已公布报价）'
                return Decimal(verdict['rate']), source, verdict['as_of'], None
            reason = FALLBACK_REASONS.get(verdict['reason'], verdict['reason'])
            recent = fx_rates.evaluate(snapshot, 'CNY', max_age=fallback_window)
            if recent['usable']:
                return (Decimal(recent['rate']),
                        '%s（最近一次报价）' % (snapshot.get('source') or '行情报价'),
                        recent['as_of'],
                        '实时行情暂不可用（%s），本单沿用最近一次行情报价折算，请知悉。' % reason)
        else:
            reason = '汇率服务未启用'
        return self.fallback_cny_rate(reason)

    def fallback_cny_rate(self, reason):
        """The operator-declared rate, used only when market data is unusable.

        Bounded on purpose: a declared rate that has not been revisited for
        `fx.fallback_max_age_seconds` stops pricing orders, because silently
        charging an arbitrarily old rate is worse than a visible outage.
        """
        config = self.config()
        fx = config.get('fx') or {}
        rate = Decimal(str(config['cny_per_usd']))
        if not rate.is_finite() or rate <= 0:
            raise ValueError('config/topup.json 的 cny_per_usd 无效，无法报价')
        as_of = config.get('fx_as_of')
        limit = int(fx.get('fallback_max_age_seconds') or fx_rates.FALLBACK_MAX_AGE)
        age = fx_rates.age_seconds(as_of)
        if age is None or age > limit:
            raise ValueError(
                '汇率数据暂不可用（%s），备用汇率也已超过 %d 天有效期，人民币订单暂时无法报价。'
                '请稍后再试或联系客服。' % (reason, max(1, limit // 86400)))
        note = '实时行情暂不可用（%s），本单按经营者设定的备用汇率折算，请知悉。' % reason
        return (rate, '备用汇率（经营者设定，%s 起沿用）' % as_of, as_of, note)

    def quote(self, payload):
        method = self.method(payload.get('method'))
        if not method:
            raise ValueError('支付方式未启用或配置不完整')
        amount = queue.decimal_amount(payload.get('amount'))
        minimum = queue.decimal_amount(method['minimum'])
        if amount < minimum:
            raise ValueError('低于该支付方式最低金额 ' + queue.plain(minimum) + ' ' + method['currency'])
        currency = method['currency']
        note = None
        if currency == 'USD':
            usd, rate, source, as_of = amount, Decimal('1'), 'USD settlement', iso()
        elif currency == 'CNY':
            rate, source, as_of, note = self.cny_rate()
            # Reuse the shared converter: an ad-hoc quantize() here defaulted to
            # half-even, so for some amounts the quote promised one quota unit
            # more than the catalogue and the ledger actually granted.
            usd = queue.usd_from_cny(amount, rate)
        else:
            raise ValueError('该币种尚未开放收款')
        quote = queue.create_quote(method['id'], amount, currency, usd, rate, source, as_of,
                                   self.quotes_path, note=note)
        quote['method_details'] = method
        return quote

    # -- shared state ----------------------------------------------------
    def config(self):
        return json.loads(self.config_path.read_text(encoding='utf-8'))

    def url(self):
        if self.gateway_url:
            return self.gateway_url
        return Stack().url

    def api(self):
        """The admin session used for reading users and crediting balances.

        Never obtained by password login: the gateway's login is protected by
        Cloudflare Turnstile and an unattended service must not attempt to get
        around a human check. The session is instead seeded from the operator's
        own dashboard token the moment they open the admin page or the console
        badge (see `newapi_admin`) — the same browser session they use to click
        "核实到账".

        A missing session therefore means the operator has not identified
        themselves yet in this process lifetime; saying so is more useful than a
        login attempt the gateway would reject anyway. Note that the page is
        told the same thing through `has_api()`, so it can seed one itself
        instead of asking the operator to guess what went wrong.
        """
        if self._api is None:
            raise SessionMissing(
                '尚未取得网关管理员会话：服务重启后需要重新确认一次身份。'
                '登录接口受 Cloudflare Turnstile 保护，服务不会代替人机验证。')
        return self._api

    def has_api(self):
        """Whether a gateway administrator session is currently usable.

        Reported to the admin page because the operator's own cookie outlives
        this process: the cookie has a 30-day life while this session lives in
        memory only, so after a restart the page still opens the board while the
        session behind it is gone. Without this flag the page has no way to know
        and would keep showing a board whose buttons cannot work.
        """
        return self._api is not None

    def forget_api(self):
        self._api = None

    def newapi_admin(self, token):
        """Return the New API user behind an access token when it is an admin.

        The operator is already signed in to the console; asking for a second
        password just to read the queue is friction that made the notice look
        broken. A console session is an httpOnly refresh cookie plus a
        short-lived Bearer token, so the page can prove "I am the operator" and
        nothing else is needed. Verification goes through the gateway itself,
        the only component that can validate its own tokens.
        """
        admin = user_identity.admin_for(self.url(), token, ADMIN_ROLE)
        if admin is None:
            return None
        # Reuse the dashboard token the operator just proved. Password login is
        # protected by Turnstile, so unattended services must not try to bypass
        # it. The browser refresh flow will replace this token on the next admin
        # visit when it expires.
        session = API(self.url())
        session.access_token = str(token).strip()
        session.user_id = admin['id']
        self._api = session
        return admin

    def authenticated_user(self, token):
        """Return the enabled account behind a console access token.

        This is what turns the credited account into a server-derived fact. The
        recharge page already holds a short-lived dashboard token (it uses the
        refresh cookie to render the account label), so the service can ask the
        gateway who that token belongs to instead of trusting a number the
        browser sent. Verification runs through the gateway -- the only component
        that can validate its own tokens. It deliberately never assigns
        self._api: that session belongs to the operator, not to a customer.
        """
        return user_identity.user_for(self.url(), token)

    def identify(self, token, client):
        """Resolve the caller, on a budget of its own.

        Rate limited separately from the submit gate: without this an
        unauthenticated caller could turn a public route into a stream of
        gateway lookups, and would also spend the operator's own submit quota.
        """
        if not self.identify_gate.allow(client):
            raise PermissionError('身份校验过于频繁，请 %d 秒后再试。'
                                  % self.identify_gate.retry_after(client))
        return self.authenticated_user(token)

    def username(self, user_id):
        """Best-effort display name. A lookup failure must never block the list."""
        key = str(user_id)
        if key in self._names:
            return self._names[key]
        name = ''
        try:
            user = self.api().ok('/api/user/%s' % key) or {}
            name = user.get('display_name') or user.get('username') or ''
        except Exception:
            self.forget_api()
        # The submission endpoint is public, so the cache is capped: a flood of
        # invented user ids must not grow memory without bound.
        if len(self._names) >= NAME_CACHE_LIMIT:
            self._names.clear()
        self._names[key] = name
        return name

    # -- submissions -----------------------------------------------------
    def recent_duplicate(self, user_id, amount):
        """An identical claim inside the window is the same payment, not a new one."""
        cutoff = utcnow() - dt.timedelta(seconds=DUPLICATE_WINDOW)
        for row in reversed(queue.pending(self.events_path)):
            # Quoted claims record `quoted_amount`, not `cny`; comparing only the
            # latter silently disabled this check for every quoted submission.
            payable = row.get('quoted_amount') or row.get('cny')
            if int(row.get('uid', 0)) != int(user_id) or payable != amount:
                continue
            try:
                created = dt.datetime.fromisoformat(str(row.get('created_at')))
            except ValueError:
                continue
            if created >= cutoff:
                return row
        return None

    def submit(self, payload, client, user):
        """Record a claim against the account the caller proved they own.

        `user` comes from authenticated_user(), never from the request body: a
        client-supplied account number is exactly what allowed a payment to be
        aimed at somebody else's wallet, so payload['uid'] is ignored on purpose.
        A quote is mandatory -- it is the only thing that fixes the amount and
        the per-order rate, so an unquoted amount would let a caller price their
        own order. The payment reference is optional by contrast: it cannot fix
        an amount, so demanding it only cost us claims from users who had already
        paid and could not produce one.
        """
        if not self.submit_global_gate.allow('all'):
            raise PermissionError('提交过于频繁，请稍后再试。')
        if not self.submit_gate.allow(client):
            raise PermissionError('提交过于频繁，请 %d 秒后再试。' % self.submit_gate.retry_after(client))
        user_id = int(user['id'])
        quote_id = str(payload.get('quote_id') or '').strip()
        reference = str(payload.get('reference') or '').strip()
        if not quote_id:
            raise ValueError('请先获取报价再提交；如果页面已打开很久，请刷新后重试')
        quote = queue.find_quote(self.quotes_path, quote_id)
        if not queue.quote_valid(quote):
            raise ValueError('报价不存在或已过期，请重新获取')
        method = self.method(quote.get('method'))
        if not method:
            raise ValueError('该支付方式已停用')
        # The reference is optional on purpose. Requiring it only moved the cost
        # onto the user (they rarely have a Payoneer reference to hand before the
        # money lands, and would abandon the claim), while the operator confirms
        # every credit by hand against their own statement anyway. When it *is*
        # supplied it stays globally single-use, so the same transfer can never
        # back two claims.
        if reference and queue.reference_used(self.events_path, reference):
            raise ValueError('该交易参考号已提交过')
        amount_text = quote['quoted_amount']
        with self.lock:
            duplicate = (next((row for row in queue.pending(self.events_path)
                               if row.get('quote_id') == quote_id and
                               int(row.get('uid', 0)) == user_id), None)
                         or self.recent_duplicate(user_id, amount_text))
            if duplicate:
                return {'duplicate': True, 'request': duplicate,
                        'counts': queue.counts(self.events_path)}
            event = queue.build_quoted_request(user_id, quote, reference,
                                               source='web', client=client)
            # Audit trail: record where the credited account came from, so a
            # later reader can tell this apart from the old client-supplied path.
            event['uid_source'] = 'verified-session'
            queue.append_event(event, self.events_path)
        event['username'] = self.username(user_id)
        return {'duplicate': False, 'request': event, 'counts': queue.counts(self.events_path)}

    # -- admin actions ---------------------------------------------------
    def summary(self):
        rows = queue.fold(queue.load_events(self.events_path))
        for row in rows:
            row['username'] = self.username(row.get('uid'))
        order = {'pending': 0, 'fulfilled': 1, 'dismissed': 2}
        rows.sort(key=lambda row: (order.get(row.get('status'), 9),
                                  str(row.get('created_at') or '')), reverse=False)
        pending = [row for row in rows if row.get('status') == 'pending']
        return {'counts': queue.counts(self.events_path),
                'pending': list(reversed(pending)),
                'recent': list(reversed(rows))[:40],
                # Rides along with the poll the page already makes every 30
                # seconds, so a lost session is noticed without an extra call.
                'gateway_session': self.has_api()}

    def fulfill(self, request_id, transaction=None, note=None, operator='admin-page',
                net_received=None):
        with self.lock:
            record = queue.find(self.events_path, request_id)
            if record is None:
                raise ValueError('申请不存在：' + str(request_id))
            if record['status'] != 'pending':
                raise ValueError('该申请已处理（%s）' % record['status'])
            # The immutable request id owns wallet idempotency. Operator-entered
            # references remain audit metadata and can never select a new debit.
            transaction_id = 'topup:' + record['id']
            payment_reference = ((str(transaction).strip() if transaction else '') or
                                 record.get('reference') or '')
            result, net, credited = self._credit_with_session(record, transaction_id, net_received)
            event = queue.close_request(record['id'], 'fulfilled', self.events_path,
                                        code=result.get('code'), transaction=payment_reference,
                                        operator=operator, note=note, net_received=net,
                                        credited_usd=credited, reference=record.get('reference'))
        return {'request': record, 'issued': result, 'event': event}

    def _credit_with_session(self, record, transaction_id, net_received=None):
        """Credit the wallet, rebuilding the session once if it expired."""
        last_error = None
        for attempt in (1, 2):
            try:
                api = self.api()
                if record.get('quote_id'):
                    if net_received in (None, ''):
                        raise ValueError('请填写实际净到账金额')
                    net = queue.decimal_amount(net_received)
                    if record['currency'] == 'CNY':
                        credited = (net / Decimal(str(record['fx_rate']))).quantize(Decimal('0.000001'))
                    else:
                        credited = Decimal(str(net)).quantize(Decimal('0.000001'))
                    metadata = {key: record.get(key) for key in
                                ('id', 'quote_id', 'method', 'currency', 'quoted_amount',
                                 'fx_rate', 'fx_source', 'fx_as_of', 'fx_note',
                                 'reference', 'asset', 'network')
                                if record.get(key) is not None}
                    result = credit_topup.credit_usd(self.ledger_path, record['uid'], credited,
                                                     transaction_id, apply_changes=True,
                                                     api=api, metadata=metadata,
                                                     allow_replay=True)
                    return result, net, credited
                result = credit_topup.credit(self.config_path, self.ledger_path,
                                             record['uid'], record['cny'], transaction_id,
                                             apply_changes=True, api=api, via='balance',
                                             allow_replay=True)
                issued = result.get('issued') or {}
                return result, Decimal(str(record['cny'])), Decimal(str(issued.get('usd') or record['usd']))
            except SessionMissing:
                # Nothing to retry and nothing to discard: the session was never
                # seeded. Rethrown unchanged so the route can answer 409 with a
                # code the admin page understands, instead of a 400 that reads
                # like a validation error and strands the operator.
                raise
            except RuntimeError as error:
                # A stale Bearer token shows up as a rejected API call; the
                # ledger still guards against a double credit on the retry.
                last_error = error
                self.forget_api()
                if attempt == 2:
                    break
        raise ValueError('入账失败，未能确认额度已加到用户钱包：' + str(last_error))


class Handler(BaseHTTPRequestHandler):
    server_version = 'omniapi-topup-watch'
    sys_version = ''
    protocol_version = 'HTTP/1.1'
    service = None

    # -- plumbing --------------------------------------------------------
    def log_message(self, fmt, *args):
        safe_path = urllib.parse.urlsplit(self.path).path
        line = json.dumps({'time': iso(), 'client': self.client_ip(),
                           'request': '%s %s' % (self.command, safe_path),
                           'detail': fmt % args[:3]},
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

    def respond_bytes(self, status, body, content_type):
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(body)))
        self.send_header('Cache-Control', 'public, max-age=300')
        self.send_header('X-Content-Type-Options', 'nosniff')
        self.end_headers()
        self.wfile.write(body)

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

    def bearer_token(self):
        header = self.headers.get('Authorization') or ''
        if not header.lower().startswith('bearer '):
            return ''
        return header[7:].strip()

    def admin_cookie(self):
        return operator_session.parse_cookies(self.headers.get('Cookie')).get(COOKIE_NAME, '')

    def current_scope(self):
        return session_scope(self.service.secrets_doc, self.admin_cookie(),
                             generation=self.service.generation())

    def csrf_token(self):
        return operator_session.csrf_token(self.service.secrets_doc, self.admin_cookie())

    def require_admin_write(self):
        scope = self.require_admin()
        if scope is None:
            return None
        if not operator_session.secret_matches(self.headers.get('X-CSRF-Token'),
                                              self.csrf_token()):
            self.respond(403, {'success': False, 'message': 'CSRF 校验失败，请刷新管理页'})
            return None
        return scope

    def is_admin(self):
        return self.current_scope() is not None

    def require_admin(self):
        """Returns the session scope, or None after answering 401 itself."""
        scope = self.current_scope()
        if scope is None:
            self.respond(401, {'success': False, 'message': '需要管理员登录'})
            return None
        return scope

    def session_cookie(self, value, max_age):
        return operator_session.cookie_header(
            value, max_age, secure=not self.service.allow_insecure_cookie)

    def grant_session(self, scope):
        document = self.service.secrets_doc
        if not document:
            raise ValueError('管理员口令尚未初始化，请先在服务器上运行 --init-secrets')
        expires_at = int(time.time()) + COOKIE_TTL
        return self.session_cookie(sign_session(document, expires_at, scope,
                                                self.service.generation()), COOKIE_TTL)

    # -- routes ----------------------------------------------------------
    def do_POST(self):
        path = self.path.split('?')[0]
        try:
            if path.startswith(ADMIN_PREFIX):
                origin = (self.headers.get('Origin') or '').rstrip('/')
                if origin != self.service.public_origin:
                    # Do not leave the rejected POST body on an HTTP/1.1
                    # keep-alive connection; BaseHTTPRequestHandler would parse
                    # it as a second request and could echo fragments to logs.
                    self.close_connection = True
                    return self.respond(403, {'success': False, 'message': '请求来源校验失败'})
            if path == SUBMIT_PATH:
                return self.route_submit()
            if path == QUOTE_PATH:
                return self.route_quote()
            if path == NOTICE_READ_PATH:
                return self.route_notice_read()
            if path == ADMIN_PREFIX + 'session':
                return self.route_bootstrap()
            if path == ADMIN_PREFIX + 'login':
                return self.route_login()
            if path == ADMIN_PREFIX + 'logout':
                return self.route_logout()
            if path == ADMIN_PREFIX + 'fulfill':
                return self.route_fulfill()
            if path == ADMIN_PREFIX + 'dismiss':
                return self.route_dismiss()
            if path == ADMIN_PREFIX + 'settings':
                return self.route_settings_save()
            if path == ADMIN_PREFIX + 'payment-qr':
                return self.route_payment_qr()
            self.respond(404, {'success': False, 'message': 'not found'})
        except AuthError as error:
            self.respond(401, {'success': False, 'message': str(error)})
        except SessionMissing as error:
            self.respond(409, {'success': False, 'code': error.code, 'message': str(error)})
        except ValueError as error:
            self.respond(400, {'success': False, 'message': str(error)})
        except PermissionError as error:
            self.respond(429, {'success': False, 'message': str(error)})
        except Exception as error:  # never leak a stack trace to the client
            self.log_message('unhandled %s: %s', type(error).__name__, error)
            self.respond(500, {'success': False, 'message': '服务暂时不可用'})

    def do_GET(self):
        path = self.path.split('?')[0]
        try:
            if path == '/healthz':
                return self.respond(200, {'success': True, 'service': 'topup-watch'})
            if path == FX_PATH:
                snapshot = self.service.fx_snapshot()
                return self.respond(200, {'success': True, **snapshot})
            if path == METHODS_PATH:
                return self.respond(200, {'success': True,
                                          'methods': self.service.payment_methods(public=True)})
            if path == NOTICES_PATH:
                user = self.service.identify(self.bearer_token(), self.client_ip())
                if not user:
                    raise AuthError('需要登录')
                return self.respond(200, {'success': True,
                                          'notices': self.service.notices_for(user)})
            if path.startswith(ASSET_PREFIX):
                name = path[len(ASSET_PREFIX):]
                if not re.match(r'^[a-z0-9-]+\.(png|jpe?g|webp)$', name):
                    return self.respond(404, {'success': False, 'message': 'not found'})
                target = self.service.payment_assets_path / name
                if not target.exists():
                    return self.respond(404, {'success': False, 'message': 'not found'})
                mime = ('image/png' if name.endswith('.png') else
                        'image/webp' if name.endswith('.webp') else 'image/jpeg')
                return self.respond_bytes(200, target.read_bytes(), mime)
            if path == ADMIN_PREFIX + 'session':
                # `authenticated` answers "does your operator cookie check out",
                # `gateway_session` answers "can this service actually credit a
                # wallet right now". They are not the same question: the cookie
                # survives a restart, the gateway session does not.
                return self.respond(200, {'success': True,
                                          'authenticated': self.is_admin(),
                                          'gateway_session': self.service.has_api(),
                                          'csrf_token': self.csrf_token() if self.is_admin() else ''})
            if path == ADMIN_PREFIX + 'summary':
                if not self.require_admin():
                    return
                return self.respond(200, {'success': True, **self.service.summary()})
            if path == ADMIN_PREFIX + 'settings':
                if not self.require_admin():
                    return
                return self.respond(200, {'success': True,
                                          'methods': self.service.payment_methods(public=False)})
            self.respond(404, {'success': False, 'message': 'not found'})
        except AuthError as error:
            self.respond(401, {'success': False, 'message': str(error)})
        except PermissionError as error:
            self.respond(429, {'success': False, 'message': str(error)})
        except Exception as error:
            self.log_message('unhandled %s: %s', type(error).__name__, error)
            self.respond(500, {'success': False, 'message': '服务暂时不可用'})

    def route_submit(self):
        # Read the body first: with HTTP/1.1 keep-alive an undrained request body
        # would corrupt the next request served on this connection.
        payload = self.read_body()
        user = self.service.identify(self.bearer_token(), self.client_ip())
        if not user:
            raise AuthError('请先登录再提交充值申请，然后刷新本页重新提交。')
        result = self.service.submit(payload, self.client_ip(), user)
        record = result['request']
        if not result['duplicate']:
            self.service.notify_topup(record)
        if result['duplicate']:
            message = '这笔申请已经在等待核验，请勿重复付款。'
        else:
            message = ('已记录你的充值申请，核实到账后额度会直接计入你的钱包，'
                       '刷新钱包页即可看到。请把下方申请编号发给客服以便核对。')
        self.respond(200, {'success': True, 'duplicate': result['duplicate'],
                           'account': {'id': user['id'],
                                       'public_id': user.get('public_id') or '',
                                       'display_name': user.get('display_name') or '',
                                       'username': user.get('username') or ''},
                           'request_id': record['id'], 'cny': record.get('cny'),
                           'usd': record['usd'], 'quota': record['quota'],
                           'method': record.get('method'),
                           'currency': record.get('currency', 'CNY'),
                           'quoted_amount': record.get('quoted_amount', record.get('cny')),
                           'created_at': record['created_at'],
                           'pending_count': result['counts'].get('pending', 0),
                           'message': message})

    def route_quote(self):
        quote = self.service.quote(self.read_body())
        self.respond(200, {'success': True, 'quote': quote})

    def route_notice_read(self):
        origin = (self.headers.get('Origin') or '').rstrip('/')
        if origin != self.service.public_origin:
            self.close_connection = True
            return self.respond(403, {'success': False, 'message': '请求来源校验失败'})
        user = self.service.identify(self.bearer_token(), self.client_ip())
        if not user:
            raise AuthError('需要登录')
        payload = self.read_body()
        if not self.service.acknowledge_notice(user, str(payload.get('id') or '')):
            raise ValueError('通知不存在、已读或无权访问')
        self.respond(200, {'success': True})

    def route_login(self):
        client = self.client_ip()
        if not self.service.login_gate.allow(client):
            raise PermissionError('登录尝试过多，请 %d 秒后再试。'
                                  % self.service.login_gate.retry_after(client))
        payload = self.read_body()
        document = self.service.secrets_doc
        if not document:
            raise ValueError('管理员口令尚未初始化，请先在服务器上运行 --init-secrets')
        if not password_matches(document, payload.get('password')):
            time.sleep(0.6)  # blunt the value of guessing
            self.respond(401, {'success': False, 'message': '口令不正确'})
            return
        self.respond(200, {'success': True}, {'Set-Cookie': self.grant_session('pw')})

    def route_bootstrap(self):
        """Turn an existing console admin session into an operator session.

        Called by the badge and the management page, which already run inside the
        console origin: they fetch a Bearer token from /api/user/auth/refresh and
        present it here. Only a real New API administrator gets a cookie, and the
        gateway is the one that decides.
        """
        client = self.client_ip()
        if not self.service.login_gate.allow(client):
            raise PermissionError('尝试过多，请 %d 秒后再试。'
                                  % self.service.login_gate.retry_after(client))
        payload = self.read_body()
        admin = self.service.newapi_admin(payload.get('access_token'))
        if admin is None:
            self.respond(403, {'success': False, 'message': '需要本站管理员登录态'})
            return
        self.respond(200, {'success': True, 'user': admin, 'gateway_session': True},
                     {'Set-Cookie': self.grant_session('na:%s' % admin['id'])})

    def route_logout(self):
        if self.require_admin_write() is None:
            return
        self.service.revoke_sessions()
        self.respond(200, {'success': True}, {'Set-Cookie': self.session_cookie('', 0)})

    def route_fulfill(self):
        scope = self.require_admin_write()
        if scope is None:
            return
        payload = self.read_body()
        result = self.service.fulfill(payload.get('id'), payload.get('transaction'),
                                      payload.get('note'), operator=scope,
                                      net_received=payload.get('net_received'))
        issued = result['issued'].get('issued') or {}
        verified = bool(issued.get('balance_verified'))
        if issued.get('code'):
            message = '兑换码已生成，发给用户即可在其钱包页兑换。'
        elif verified:
            message = ('额度已直接计入用户钱包（%s → %s），用户刷新钱包页即可看到。'
                       % (issued.get('quota_before'), issued.get('quota_after')))
        else:
            message = result['issued'].get('note') or '接口返回成功，但余额未能立即核对，请到后台确认。'
        self.respond(200, {'success': True, 'request_id': result['request']['id'],
                           'code': issued.get('code'), 'quota': issued.get('quota'),
                           'quota_before': issued.get('quota_before'),
                           'quota_after': issued.get('quota_after'),
                           'balance_verified': verified,
                           'username': issued.get('username'),
                           'transaction': issued.get('transaction_id'),
                           'net_received': result['event'].get('net_received'),
                           'credited_usd': result['event'].get('credited_usd'),
                           'message': message})

    def route_settings_save(self):
        scope = self.require_admin_write()
        if scope is None:
            return
        payload = self.read_body()
        saved = self.service.save_payment_methods(payload.get('methods'), operator=scope)
        self.respond(200, {'success': True, 'updated_at': saved['updated_at'],
                           'methods': saved['methods']})

    def route_payment_qr(self):
        scope = self.require_admin_write()
        if scope is None:
            return
        payload = self.read_body(MAX_UPLOAD_BODY)
        method_id = str(payload.get('method_id') or '').strip().lower()
        if not re.match(r'^[a-z0-9-]{1,40}$', method_id):
            raise ValueError('支付方式 ID 无效')
        data_url = str(payload.get('data_url') or '')
        match = re.match(r'^data:image/(png|jpeg|webp);base64,([A-Za-z0-9+/=]+)$', data_url)
        if not match:
            raise ValueError('仅支持 PNG、JPEG 或 WebP 二维码')
        raw = base64.b64decode(match.group(2), validate=True)
        if not raw or len(raw) > 1048576:
            raise ValueError('二维码图片不能超过 1 MiB')
        from PIL import Image
        try:
            image = Image.open(BytesIO(raw))
            image.verify()
            image = Image.open(BytesIO(raw))
            if image.format not in ('PNG', 'JPEG', 'WEBP'):
                raise ValueError('不支持的真实图片格式')
            if image.width < 32 or image.height < 32 or image.width * image.height > 16000000:
                raise ValueError('二维码图片尺寸无效')
            clean = BytesIO()
            image.convert('RGB').save(clean, format='PNG', optimize=True)
            raw = clean.getvalue()
        except (OSError, ValueError) as error:
            raise ValueError('二维码图片无法完整解码：' + str(error))
        if len(raw) > 1048576:
            raise ValueError('安全重编码后的二维码图片超过 1 MiB')
        ext = 'png'
        self.service.payment_assets_path.mkdir(parents=True, exist_ok=True)
        name = method_id + '.' + ext
        target = self.service.payment_assets_path / name
        if target.is_symlink():
            raise ValueError('拒绝写入符号链接')
        fd, temporary = tempfile.mkstemp(prefix=name + '.', dir=str(self.service.payment_assets_path))
        try:
            with os.fdopen(fd, 'wb') as handle:
                handle.write(raw); handle.flush(); os.fsync(handle.fileno())
            os.chmod(temporary, 0o640)
            os.replace(temporary, target)
        finally:
            if os.path.exists(temporary): os.unlink(temporary)
        self.respond(200, {'success': True, 'qr_path': ASSET_PREFIX + name})

    def route_dismiss(self):
        scope = self.require_admin_write()
        if scope is None:
            return
        payload = self.read_body()
        queue.close_request(payload.get('id'), 'dismissed', self.service.events_path,
                            operator=scope, note=payload.get('note') or 'dismissed')
        self.respond(200, {'success': True, 'message': '已忽略该申请，未发放任何额度。'})


def handler_factory(service):
    return type('BoundHandler', (Handler,), {'service': service})


def main():
    credential_dir = os.environ.get('CREDENTIALS_DIRECTORY')
    default_xe_secrets = (Path(credential_dir) / 'xe.json' if credential_dir
                          else ROOT / '.secrets/xe.json')
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', default=os.environ.get('TOPUP_WATCH_ADDR', '0.0.0.0'))
    parser.add_argument('--port', type=int, default=int(os.environ.get('TOPUP_WATCH_PORT', '8787')))
    parser.add_argument('--secrets', type=Path,
                        default=Path(os.environ.get('TOPUP_WATCH_SECRETS',
                                                    str(ROOT / '.secrets/topup-admin.json'))))
    parser.add_argument('--events', type=Path,
                        default=Path(os.environ.get('TOPUP_WATCH_EVENTS',
                                                    str(queue.DEFAULT_EVENTS))))
    parser.add_argument('--ledger', type=Path,
                        default=Path(os.environ.get('TOPUP_WATCH_LEDGER',
                                                    str(credit_topup.DEFAULT_LEDGER))))
    parser.add_argument('--config', type=Path, default=ROOT / 'config/topup.json')
    parser.add_argument('--quotes', type=Path,
                        default=Path(os.environ.get('TOPUP_WATCH_QUOTES',
                                                    str(queue.DEFAULT_QUOTES))))
    parser.add_argument('--fx-cache', type=Path,
                        default=Path(os.environ.get('TOPUP_WATCH_FX_CACHE',
                                                    str(ROOT / 'runtime/fx-rates.json'))))
    parser.add_argument('--xe-secrets', type=Path,
                        default=Path(os.environ.get('TOPUP_WATCH_XE_SECRETS',
                                                    str(default_xe_secrets))))
    parser.add_argument('--methods-config', type=Path,
                        default=Path(os.environ.get('TOPUP_WATCH_METHODS_CONFIG',
                                                    str(ROOT / 'config/payment-methods.json'))))
    parser.add_argument('--gateway-url', default=os.environ.get('TOPUP_WATCH_GATEWAY_URL') or None)
    parser.add_argument('--init-secrets', action='store_true',
                        help='生成管理员口令文件（已存在则不改动）并打印口令一次')
    parser.add_argument('--check', action='store_true', help='自检配置与依赖，不监听端口')
    parser.add_argument('--refresh-fx', action='store_true',
                        help='拉取一次汇率快照后退出（供 systemd 定时器预热）')
    args = parser.parse_args()

    if args.init_secrets:
        if args.secrets.exists():
            print(json.dumps({'status': 'EXISTS', 'path': str(args.secrets),
                              'note': '已存在，未改动。如需重置请先备份并删除该文件。'},
                             ensure_ascii=False, indent=2))
            return
        _, password = init_secrets(args.secrets)
        print(json.dumps({'status': 'PASS', 'path': str(args.secrets), 'password': password,
                          'note': '这是唯一一次打印口令，请立即保存到密码管理器。'},
                         ensure_ascii=False, indent=2))
        return

    service = Service(args.secrets, args.events, args.ledger, args.config,
                      gateway_url=args.gateway_url,
                      allow_insecure_cookie=os.environ.get('TOPUP_WATCH_INSECURE_COOKIE') == '1',
                      quotes_path=args.quotes, fx_cache_path=args.fx_cache,
                      xe_secrets_path=args.xe_secrets,
                      methods_config_path=args.methods_config)
    if args.refresh_fx:
        config = service.config().get('fx') or {}
        if not config.get('enabled'):
            print(json.dumps({'status': 'SKIPPED', 'reason': 'fx.enabled 为 false，无需预热'},
                             ensure_ascii=False))
            return
        try:
            snapshot = fx_rates.refresh(args.xe_secrets, args.fx_cache,
                                        provider=config.get('provider', 'xe'),
                                        max_age=config.get('max_age_seconds'))
        except Exception as error:
            print(json.dumps({'status': 'FAIL', 'error': '%s: %s' % (type(error).__name__, error)},
                             ensure_ascii=False))
            raise SystemExit(1)
        verdict = fx_rates.evaluate(snapshot, 'CNY',
                                    max_age=config.get('max_age_seconds'))
        print(json.dumps({'status': 'PASS', 'provider': config.get('provider'),
                          'CNY': verdict['rate'], 'as_of': verdict['as_of'],
                          'age_seconds': verdict['age_seconds'], 'usable': verdict['usable']},
                         ensure_ascii=False))
        return
    if args.check:
        fx_config = service.config().get('fx') or {}
        snapshot = service.fx_snapshot(refresh_if_needed=False)
        verdict = fx_rates.evaluate(snapshot, 'CNY',
                                    max_age=fx_config.get('max_age_seconds'))
        report = {
            'status': 'PASS',
            'secrets': {'path': str(args.secrets), 'present': service.secrets_doc is not None},
            'events': {'path': str(args.events),
                       'counts': queue.counts(args.events)},
            'config': {'path': str(args.config),
                       'min_amount_cny': service.config().get('min_amount_cny'),
                       'cny_per_usd': service.config().get('cny_per_usd')},
            'gateway_url': service.url(),
            'fx': snapshot,
            'fx_verdict': verdict,
            'payment_methods_config': str(service.methods_config_path),
            'payment_methods': service.payment_methods(public=True),
        }
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return

    if service.secrets_doc is None:
        raise SystemExit('BLOCKED: 管理员口令未初始化；先运行 --init-secrets')
    server = ThreadingHTTPServer((args.host, args.port), handler_factory(service))
    server.daemon_threads = True
    print(json.dumps({'status': 'LISTENING', 'address': '%s:%d' % (args.host, args.port),
                      'events': str(args.events)}, ensure_ascii=False), flush=True)
    stop = threading.Event()

    def overdue_worker():
        while not stop.wait(15):
            try:
                service.escalate_overdue()
            except Exception as error:
                print(json.dumps({'status': 'OVERDUE_CHECK_FAILED',
                                  'error': '%s: %s' % (type(error).__name__, error)},
                                 ensure_ascii=False), flush=True)

    threading.Thread(target=overdue_worker, name='topup-overdue', daemon=True).start()
    try:
        server.serve_forever()
    finally:
        stop.set()


if __name__ == '__main__':
    main()
