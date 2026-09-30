#!/usr/bin/env python3
"""Prove that logging out of the console cuts off BOTH operator surfaces.

The gap this closes: `POST /api/user/auth/logout` (the console's own logout)
revokes the console's session and nothing else. The operator cookie that guards
"充值核验" and "客服工作台" lives for 8 hours and knows nothing about it, so after
signing out of the console the money surface was still open -- on a shared
computer the next person could credit a wallet. The operator reported exactly
this ("我连管理员都没登上去我就可以看充值核验平台").

There is one counter file that both services compare cookies against, so bumping
it once has to cut off both at the same instant. This probe starts a throwaway
recharge service and a throwaway support service that share a *temporary* copy of
that counter, and checks the cross-service property directly:

  1. password login on the recharge service yields a cookie;
  2. that same cookie is accepted by the support service (one sign-in, both
     surfaces);
  3. logging out on the SUPPORT service invalidates it on BOTH;
  4. the live counter file is untouched, and nothing was credited.

Everything runs on 127.0.0.1 against temporary files, so the operator's live
session and queue are never touched. Both instances need
`*_INSECURE_COOKIE=1` because the production cookies are `Secure` and would never
come back over plain-HTTP loopback; this verifies route behaviour, not transport.
"""
import argparse
import http.cookiejar
import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path('/opt/ai-gateway')
sys.path.insert(0, str(ROOT / 'scripts'))
sys.path.insert(0, str(ROOT / 'ops'))
sys.path.insert(0, str(ROOT / 'ops' / 'support'))
sys.path.insert(0, str(ROOT / 'ops' / 'topup_watch'))

import operator_session  # noqa: E402
import topup_requests  # noqa: E402

BASE = 'https://omniapi.biz'
LIVE_STATE = ROOT / 'runtime/admin-session-state.json'
LIVE_EVENTS = ROOT / 'runtime/topup-requests.jsonl'
REPORT = ROOT / 'runtime/support-logout-e2e.json'
verdict = {}
steps = {}


def free_port(start, end):
    for candidate in range(start, end):
        with socket.socket() as probe:
            if probe.connect_ex(('127.0.0.1', candidate)) != 0:
                return candidate
    raise SystemExit('BLOCKED: %d-%d 全部被占用，无法启动临时实例' % (start, end))


def wait_ready(origin, path='/healthz', attempts=40, timeout=1.0):
    """Poll until the instance answers.

    The per-attempt timeout is deliberately short: a service that failed to start
    makes the connection *hang* rather than refuse, so a long timeout turns a
    clear failure into a ten-minute wait with no explanation.
    """
    for _ in range(attempts):
        try:
            request = urllib.request.Request(origin + path)
            with urllib.request.urlopen(request, timeout=timeout) as response:
                if response.status == 200:
                    return True
        except Exception:                                    # noqa: BLE001
            pass
        time.sleep(0.25)
    return False


def tail(path, lines=12):
    try:
        return '\n'.join(path.read_text(encoding='utf-8', errors='replace')
                         .splitlines()[-lines:])
    except OSError:
        return '(没有日志)' 


class Client:
    """A cookie-jar-backed caller for one origin."""

    def __init__(self, origin):
        self.origin = origin
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar))

    def call(self, path, payload=None, csrf=None, cookie=None):
        body = json.dumps(payload).encode('utf-8') if payload is not None else None
        headers = {'Origin': self.origin}
        if body:
            headers['Content-Type'] = 'application/json'
        if csrf:
            headers['X-CSRF-Token'] = csrf
        if cookie is not None:
            # Overrides the jar, so a specific cookie can be tested after logout.
            headers['Cookie'] = '%s=%s' % (operator_session.COOKIE_NAME, cookie)
        request = urllib.request.Request(self.origin + path, data=body, headers=headers)
        try:
            with self.opener.open(request, timeout=20) as response:
                return response.status, json.loads(response.read().decode() or '{}')
        except urllib.error.HTTPError as error:
            raw = error.read().decode('utf-8', 'replace')
            try:
                return error.code, json.loads(raw or '{}')
            except ValueError:
                return error.code, {'raw': raw[:200]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--password-file', type=Path,
                        default=Path(os.environ.get('TOPUP_PROBE_PASSWORD_FILE')
                                     or ROOT / '.secrets/topup-admin-password.txt'))
    args = parser.parse_args()

    password = ''
    if args.password_file.exists():
        password = args.password_file.read_text(encoding='utf-8').strip()
    if not password:
        raise SystemExit('BLOCKED: 找不到管理员口令副本 %s' % args.password_file)

    live_generation_before = operator_session.load_generation(LIVE_STATE)
    live_events_before = topup_requests.counts(LIVE_EVENTS)

    tmp = Path(tempfile.mkdtemp(prefix='logout-probe-'))
    events = tmp / 'requests.jsonl'
    quotes = tmp / 'quotes.jsonl'
    ledger = tmp / 'ledger.json'
    # The recharge service derives its counter from the events directory, so both
    # instances end up on this one file -- a temporary copy, never the live one.
    shared_state = tmp / 'admin-session-state.json'
    secrets = ROOT / '.secrets/topup-admin.json'

    topup_port = free_port(8811, 8830)
    support_port = free_port(8831, 8850)
    topup_origin = 'http://127.0.0.1:%d' % topup_port
    support_origin = 'http://127.0.0.1:%d' % support_port

    # Both instances must load the *same* secrets file: sharing the cookie signing
    # key is what lets one sign-in open both surfaces, and sharing the counter
    # file is what lets one logout close both.
    recharge_log = tmp / 'recharge.log'
    support_log = tmp / 'support.log'
    recharge = subprocess.Popen(
        [sys.executable, str(ROOT / 'ops/topup_watch/topup_watch.py'),
         '--host', '127.0.0.1', '--port', str(topup_port),
         '--secrets', str(secrets),
         '--events', str(events), '--quotes', str(quotes), '--ledger', str(ledger),
         '--config', str(ROOT / 'config/topup.json'), '--fx-cache', str(tmp / 'fx.json'),
         '--gateway-url', BASE],
        env=dict(os.environ, TOPUP_WATCH_INSECURE_COOKIE='1',
                 TOPUP_WATCH_PUBLIC_ORIGIN=topup_origin),
        stdout=recharge_log.open('w', encoding='utf-8'), stderr=subprocess.STDOUT)
    support = subprocess.Popen(
        [sys.executable, str(ROOT / 'ops/support/support_service.py'),
         '--host', '127.0.0.1', '--port', str(support_port),
         '--secrets', str(secrets), '--events', str(tmp / 'support'),
         '--state', str(shared_state), '--config', str(ROOT / 'config/support.json'),
         '--gateway-url', BASE, '--public-origin', support_origin],
        env=dict(os.environ, SUPPORT_INSECURE_COOKIE='1'),
        stdout=support_log.open('w', encoding='utf-8'), stderr=subprocess.STDOUT)

    try:
        if not wait_ready(topup_origin):
            raise SystemExit('BLOCKED: 充值临时实例未就绪\n--- 它的输出 ---\n%s'
                             % tail(recharge_log))
        if not wait_ready(support_origin):
            raise SystemExit('BLOCKED: 客服临时实例未就绪\n--- 它的输出 ---\n%s'
                             % tail(support_log))
        steps['ports'] = {'recharge': topup_port, 'support': support_port}
        steps['shared_state'] = str(shared_state)

        recharge_client = Client(topup_origin)
        support_client = Client(support_origin)

        # 1) the two instances really do share one counter -------------------
        verdict['both_instances_start_on_the_same_counter'] = (
            operator_session.load_generation(shared_state) == 1)

        # 2) password login on the recharge service --------------------------
        status, body = recharge_client.call('/brand/admin/api/login',
                                            {'password': password})
        steps['password_login'] = {'status': status}
        verdict['password_login_works'] = status == 200
        status, body = recharge_client.call('/brand/admin/api/session')
        steps['recharge_session'] = {'status': status, **body}
        csrf = body.get('csrf_token') or ''
        cookie = ''
        for item in recharge_client.jar:
            if item.name == operator_session.COOKIE_NAME:
                cookie = item.value
        steps['cookie_present'] = bool(cookie)
        verdict['the_cookie_authenticates_the_recharge_service'] = (
            body.get('authenticated') is True)
        verdict['the_session_route_hands_out_a_csrf_token'] = bool(csrf)

        # 3) the same cookie opens the support workbench (one sign-in, both) --
        status, body = support_client.call('/brand/admin/api/support/session',
                                           cookie=cookie)
        steps['support_session_before_logout'] = {'status': status, **body}
        verdict['one_cookie_opens_both_surfaces'] = body.get('authenticated') is True
        verdict['the_support_service_uses_the_same_csrf_token'] = (
            body.get('csrf_token') == csrf)

        # 4) log out on the SUPPORT service (the cross-service direction) -----
        status, body = support_client.call('/brand/admin/api/support/logout', {},
                                           csrf=csrf, cookie=cookie)
        steps['logout_on_support'] = {'status': status, **body}
        verdict['logout_on_the_support_service_succeeds'] = status == 200

        after = operator_session.load_generation(shared_state)
        steps['generation_after_logout'] = after
        verdict['the_counter_was_bumped'] = after > 1

        # 5) the cookie must now be dead on BOTH surfaces --------------------
        status, body = support_client.call('/brand/admin/api/support/session',
                                           cookie=cookie)
        steps['support_session_after_logout'] = {'status': status, **body}
        verdict['the_cookie_dies_on_the_support_service'] = (
            body.get('authenticated') is False and not body.get('csrf_token'))

        status, body = recharge_client.call('/brand/admin/api/session', cookie=cookie)
        steps['recharge_session_after_logout'] = {'status': status, **body}
        verdict['the_cookie_dies_on_the_recharge_service'] = (
            body.get('authenticated') is False and not body.get('csrf_token'))

        # 6) and the write route really is closed, not just the read one -----
        status, body = recharge_client.call('/brand/admin/api/summary', cookie=cookie)
        steps['recharge_summary_after_logout'] = {'status': status, **body}
        verdict['the_money_route_is_closed_after_logout'] = status == 401

        # 7) logging out without the CSRF token must not work ----------------
        # A fresh client, so the jar cannot hand back the cookie that step 5 just
        # revoked -- that mistake made this step report 401 instead of 403.
        recharged = Client(topup_origin)
        status, body = recharged.call('/brand/admin/api/login', {'password': password})
        steps['second_login'] = {'status': status}
        verdict['a_second_login_issues_a_working_cookie'] = status == 200
        fresh_cookie = ''
        for item in recharged.jar:
            if item.name == operator_session.COOKIE_NAME:
                fresh_cookie = item.value
        status, _ = support_client.call('/brand/admin/api/support/logout', {},
                                        cookie=fresh_cookie)
        steps['logout_without_csrf'] = {'status': status}
        verdict['logout_without_csrf_is_refused'] = status == 403
        status, body = support_client.call('/brand/admin/api/support/session',
                                           cookie=fresh_cookie)
        verdict['the_refused_logout_left_the_new_session_alive'] = (
            body.get('authenticated') is True)

        # 8) nothing was credited, and the live instance was never touched ---
        verdict['nothing_was_credited'] = not ledger.exists() or not json.loads(
            ledger.read_text(encoding='utf-8') or '{}').get('entries')
    finally:
        for process in (recharge, support):
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()

    verdict['the_live_counter_was_untouched'] = (
        operator_session.load_generation(LIVE_STATE) == live_generation_before)
    verdict['the_live_queue_was_untouched'] = (
        topup_requests.counts(LIVE_EVENTS) == live_events_before)

    report = {
        'probe': 'ops/support/probe_logout_e2e.py',
        'scope': ('两个临时实例（充值 + 客服）共用一份**临时**代际文件；'
                  '未触碰线上会话与队列，未发放任何额度'),
        'steps': steps,
        'verdict': verdict,
        'not_covered': ('浏览器里「按控制台登出按钮 → 自动调用注销」由 '
                        'tests/auth-page-check.js 在 jsdom 中验证；真实控制台登录态'
                        '仍只能由经营者的浏览器产生。'),
    }
    REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n',
                      encoding='utf-8')
    print()
    print('VERDICT ' + json.dumps(verdict, ensure_ascii=False))
    passed = sum(1 for value in verdict.values() if value)
    print('判定 %d/%d 通过' % (passed, len(verdict)))
    if passed != len(verdict):
        print('未通过:', [k for k, v in verdict.items() if not v])
        raise SystemExit(1)


if __name__ == '__main__':
    main()
