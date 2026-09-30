#!/usr/bin/env python3
"""End-to-end check of the two fixes made after the operator could not credit.

1. The payment reference is optional: a Payoneer claim with no reference must be
   accepted, while a reference that *is* supplied stays single-use.
2. A lost gateway administrator session is no longer reported as bad input. The
   operator's 30-day cookie outlives the in-memory gateway session, so after a
   restart the board opened while crediting failed with a 400 that read like a
   validation error. It is now a 409 carrying `gateway_session_missing`, which is
   what lets the admin page re-seed the session and retry.

Everything runs against a throwaway copy of the service on 127.0.0.1 with its own
events/quotes/ledger files, so the operator's live queue is never modified and
nothing here can credit a real wallet. That is the point: the missing-session path
has to fail closed.

The throwaway instance sets TOPUP_WATCH_INSECURE_COOKIE=1 because the production
cookie is `Secure` and would never be sent back over plain-HTTP loopback. This
verifies route behaviour, not cookie transport.
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
sys.path.insert(0, str(ROOT / 'ops' / 'topup_watch'))
import topup_requests as queue  # noqa: E402
import topup_watch  # noqa: E402

BASE = 'https://omniapi.biz'
LIVE_EVENTS = ROOT / 'runtime/topup-requests.jsonl'
LIVE_QUOTES = ROOT / 'runtime/topup-quotes.jsonl'
USER = {'id': 7, 'username': 'probe', 'display_name': 'Probe', 'public_id': ''}
verdict = {}


def live_fetch(url):
    request = urllib.request.Request(url, headers={'User-Agent': 'omniapi-probe'})
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return response.status, response.read().decode('utf-8', 'replace')
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode('utf-8', 'replace')


def free_port():
    for candidate in range(8791, 8811):
        with socket.socket() as probe:
            if probe.connect_ex(('127.0.0.1', candidate)) != 0:
                return candidate
    raise SystemExit('BLOCKED: 8791-8810 全部被占用，无法启动临时实例')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--password-file', type=Path,
                        default=Path(os.environ.get('TOPUP_PROBE_PASSWORD_FILE')
                                     or ROOT / '.secrets/topup-admin-password.txt'))
    args = parser.parse_args()

    live_before = queue.counts(LIVE_EVENTS)
    tmp = Path(tempfile.mkdtemp(prefix='topup-probe-'))
    events = tmp / 'requests.jsonl'
    quotes = tmp / 'quotes.jsonl'
    ledger = tmp / 'ledger.json'
    secrets = ROOT / '.secrets/topup-admin.json'
    config = ROOT / 'config/topup.json'

    # 1) the reference is optional, and single-use when given ---------------
    service = topup_watch.Service(secrets, events, ledger, config, gateway_url=BASE,
                                  quotes_path=quotes, fx_cache_path=tmp / 'fx.json')
    first_quote = service.quote({'method': 'payoneer', 'amount': '20'})
    without = service.submit({'quote_id': first_quote['id'], 'reference': ''},
                             '127.0.0.1', USER)
    pending_id = without['request']['id']
    print('无参考号提交:', pending_id, '| reference=%r' % without['request']['reference'])
    verdict['reference_is_optional'] = (not without['duplicate'] and
                                        without['request']['reference'] == '')

    second_quote = service.quote({'method': 'payoneer', 'amount': '50'})
    with_reference = service.submit({'quote_id': second_quote['id'],
                                     'reference': 'PAY-PROBE-1'}, '127.0.0.1', USER)
    verdict['reference_still_recorded_when_supplied'] = (
        with_reference['request']['reference'] == 'PAY-PROBE-1')

    third_quote = service.quote({'method': 'payoneer', 'amount': '100'})
    try:
        service.submit({'quote_id': third_quote['id'], 'reference': 'pay-probe-1'},
                       '127.0.0.1', USER)
        verdict['reference_stays_single_use'] = False
        reuse_error = '未被拒绝'
    except ValueError as error:
        reuse_error = str(error)
        verdict['reference_stays_single_use'] = '已提交过' in reuse_error
    print('重复参考号:', reuse_error)

    # 2) no operator session means no credit, and it says so on its own terms
    verdict['has_api_is_false_without_an_operator'] = service.has_api() is False
    verdict['summary_reports_the_missing_session'] = (
        service.summary()['gateway_session'] is False)
    try:
        service.fulfill(pending_id, net_received='20')
        verdict['fulfill_refuses_without_a_session'] = False
        fulfill_error = '未抛错'
    except topup_watch.SessionMissing as error:
        fulfill_error = '%s (%s)' % (error, error.code)
        verdict['fulfill_refuses_without_a_session'] = (
            error.code == 'gateway_session_missing')
    print('无会话入账:', fulfill_error)
    verdict['the_request_survives_a_failed_credit'] = (
        queue.find(events, pending_id)['status'] == 'pending')

    # 3) the same behaviour over HTTP, through the real routes -------------
    # Admin writes are guarded twice beyond authentication: an absolute Origin
    # match and a per-cookie CSRF token. The probe has to speak that protocol or
    # it would report a false failure, so it declares this instance's own origin
    # and carries the token the session route hands out. Sending neither is also
    # checked below -- those guards have to fail closed.
    port = free_port()
    origin = 'http://127.0.0.1:%d' % port
    environment = dict(os.environ, TOPUP_WATCH_INSECURE_COOKIE='1',
                       TOPUP_WATCH_PUBLIC_ORIGIN=origin)
    process = subprocess.Popen(
        [sys.executable, str(ROOT / 'ops/topup_watch/topup_watch.py'),
         '--host', '127.0.0.1', '--port', str(port),
         '--events', str(events), '--quotes', str(quotes), '--ledger', str(ledger),
         '--config', str(config), '--fx-cache', str(tmp / 'fx.json'),
         '--gateway-url', BASE],
        env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT)

    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    session_state = {'csrf': ''}

    def call(path, payload=None, csrf=False, send_origin=True):
        body = json.dumps(payload).encode('utf-8') if payload is not None else None
        headers = {}
        if body:
            headers['Content-Type'] = 'application/json'
        if send_origin:
            headers['Origin'] = origin
        if csrf:
            headers['X-CSRF-Token'] = session_state['csrf']
        request = urllib.request.Request(origin + path, data=body, headers=headers)
        try:
            with opener.open(request, timeout=20) as response:
                return response.status, json.loads(response.read().decode() or '{}')
        except urllib.error.HTTPError as error:
            raw = error.read().decode('utf-8', 'replace')
            try:
                return error.code, json.loads(raw or '{}')
            except ValueError:
                return error.code, {'raw': raw[:200]}
        except urllib.error.URLError as error:
            raise SystemExit('BLOCKED: 临时实例未就绪：%s' % error)

    steps = {'port': port}
    try:
        for _ in range(50):
            try:
                status, body = call('/healthz')
                if status == 200:
                    break
            except SystemExit:
                pass
            time.sleep(0.2)

        status, body = call('/brand/admin/api/session')
        steps['anonymous_session'] = {'status': status, **body}
        verdict['session_route_answers_without_a_cookie'] = status == 200
        verdict['session_route_is_not_authenticated_without_a_cookie'] = (
            body.get('authenticated') is False)
        verdict['session_route_reports_gateway_session'] = (
            body.get('gateway_session') is False)

        password = ''
        if args.password_file.exists():
            password = args.password_file.read_text(encoding='utf-8').strip()
        if password:
            status, body = call('/brand/admin/api/login', {'password': password})
            steps['password_login'] = {'status': status}
            verdict['password_login_still_works'] = status == 200
        else:
            steps['password_login'] = {'status': 'SKIPPED', 'reason': '口令副本不存在'}

        status, body = call('/brand/admin/api/session')
        steps['operator_session'] = {'status': status, **body}
        if password:
            verdict['the_cookie_authenticates_the_board'] = body.get('authenticated') is True
            verdict['the_cookie_does_not_fake_a_gateway_session'] = (
                body.get('gateway_session') is False)
        session_state['csrf'] = str(body.get('csrf_token') or '')
        verdict['the_session_route_hands_out_a_csrf_token'] = bool(session_state['csrf'])

        status, body = call('/brand/admin/api/summary')
        steps['summary'] = {'status': status, 'counts': body.get('counts'),
                            'gateway_session': body.get('gateway_session')}
        verdict['summary_lists_the_pending_claim'] = any(
            row.get('id') == pending_id for row in (body.get('pending') or []))
        verdict['summary_reports_the_missing_session_over_http'] = (
            body.get('gateway_session') is False)

        # Both write guards must fail closed before anything is credited.
        status, body = call('/brand/admin/api/fulfill',
                            {'id': pending_id, 'net_received': '20'}, csrf=True,
                            send_origin=False)
        steps['fulfill_from_a_foreign_origin'] = {'status': status, 'message': body.get('message')}
        verdict['admin_writes_from_a_foreign_origin_are_refused'] = status == 403

        status, body = call('/brand/admin/api/fulfill',
                            {'id': pending_id, 'net_received': '20'})
        steps['fulfill_without_csrf'] = {'status': status, 'message': body.get('message')}
        verdict['admin_writes_without_the_csrf_token_are_refused'] = status == 403

        status, body = call('/brand/admin/api/fulfill',
                            {'id': pending_id, 'net_received': '20'}, csrf=True)
        steps['fulfill_without_a_session'] = {'status': status, **body}
        verdict['fulfill_answers_409_not_400'] = status == 409
        verdict['fulfill_carries_the_machine_readable_code'] = (
            body.get('code') == 'gateway_session_missing')
        verdict['fulfill_message_tells_the_operator_what_to_do'] = (
            '网关管理员会话' in str(body.get('message')))

        status, body = call('/brand/admin/api/session', {'access_token': 'not-a-token'})
        steps['bogus_token'] = {'status': status, 'message': body.get('message')}
        verdict['a_bogus_token_does_not_seed_a_session'] = status == 403
        _, after = call('/brand/admin/api/session')
        verdict['the_failed_bootstrap_left_the_session_empty'] = (
            after.get('gateway_session') is False)

        verdict['nothing_was_credited'] = (
            queue.find(events, pending_id)['status'] == 'pending')
    finally:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()

    # 4) the shipped pages carry the change --------------------------------
    status, page = live_fetch(BASE + '/brand/topup.html')
    verdict['live_topup_page_labels_the_reference_optional'] = (
        status == 200 and '交易参考号（选填）' in page)
    status, script = live_fetch(BASE + '/brand/admin.js')
    verdict['live_admin_page_ships_the_session_heal'] = (
        status == 200 and 'gateway_session' in script and 'ensureGatewaySession' in script)
    status, methods = live_fetch(BASE + '/brand/api/payment-methods')
    verdict['live_payment_methods_still_served'] = status == 200

    # 5) the operator's live queue must be untouched -----------------------
    live_after = queue.counts(LIVE_EVENTS)
    verdict['live_queue_untouched'] = live_before == live_after
    print('线上真实队列:', live_before, '->', live_after)

    report = {'probe': 'ops/topup_watch/probe_admin_selfheal_e2e.py',
              'scope': '临时实例 + 公网只读；未触碰线上队列，未发放任何额度',
              'steps': steps,
              'live_counts_before': live_before,
              'live_counts_after': live_after,
              'verdict': verdict,
              'not_covered': ('浏览器里的自动补种与重试由 tests/admin-page-check.js 在 jsdom 中'
                              '验证；真实控制台令牌仍只能由经营者的浏览器产生。')}
    print()
    print('VERDICT ' + json.dumps(verdict, ensure_ascii=False))
    (ROOT / 'runtime/topup-admin-selfheal-e2e.json').write_text(
        json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    return 0 if all(verdict.values()) else 1


if __name__ == '__main__':
    sys.exit(main())
