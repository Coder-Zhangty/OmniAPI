#!/usr/bin/env python3
"""End-to-end check of the credit step through the live service.

Goes through the real HTTP surface instead of constructing a Service, so it
exercises the same code path the operator's "核实到账，加额度" button does:

    POST /brand/admin/api/login      (service password -> session cookie)
    GET  /brand/admin/api/summary    (the pending list)
    POST /brand/admin/api/fulfill    (credit the wallet)

The credit call needs the service process to hold a gateway session, which it
only obtains from the operator's own dashboard token. Login is Cloudflare
Turnstile protected, so no script can mint one: if the service has no session
yet, this probe says exactly that, changes nothing, and exits non-zero.

Every number is verified from what the service itself returned
(`quota_before` / `quota_after` / `credited_usd` / `balance_verified`), so no
second admin credential is needed. Only the gw_tester account is used.

Scope note: since the credited account is derived server-side from the caller's
token, creating the claim now needs a real signed-in browser too. A script cannot
hold one, so this probe stops with BLOCKED at that step and says so, instead of
pretending to cover the customer half. That half is covered by
`tests/topup-page-check.js` and `probe_admin_selfheal_e2e.py`.
"""
import http.cookiejar
import json
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path('/opt/ai-gateway')

# Through nginx over HTTPS, not http://127.0.0.1:8787: the admin session cookie
# is Secure, so a plain-HTTP client would never send it back.
BASE = 'https://omniapi.biz'
PUBLIC = 'https://omniapi.biz'
TARGET = 2
AMOUNT = '20'
REPORT = ROOT / 'runtime/topup-credit-e2e.json'


def opener():
    jar = http.cookiejar.CookieJar()
    return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar)), jar


# Admin writes are guarded by an absolute Origin match plus a per-session CSRF
# token, so this probe has to present both or it would see 403s and misreport
# them as a broken service.
CSRF = {'token': ''}


def call(op, base, path, body=None, method=None):
    data = None if body is None else json.dumps(body).encode('utf-8')
    request = urllib.request.Request(base + path, data=data, method=method)
    request.add_header('Content-Type', 'application/json')
    request.add_header('Origin', BASE)
    if CSRF['token']:
        request.add_header('X-CSRF-Token', CSRF['token'])
    try:
        with op.open(request, timeout=120) as response:
            raw = response.read()
            return response.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw)
        except ValueError:
            return error.code, {'raw': raw[:300].decode('utf-8', 'ignore')}


def blocked(steps, reason, status=2, action=None):
    action = action or ('在浏览器打开一次 https://omniapi.biz/brand/admin.html，'
                        '让服务拿到控制台令牌后重跑本探针。')
    print()
    print('BLOCKED:', reason)
    print('         ' + action)
    REPORT.write_text(json.dumps({
        'probe': 'ops/topup_watch/probe_credit_e2e.py', 'status': 'BLOCKED',
        'reason': reason, 'steps': steps, 'action_required': action,
    }, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    raise SystemExit(status)


steps = {}
password = (ROOT / '.secrets/topup-admin-password.txt').read_text(encoding='utf-8').strip()
admin_op, admin_jar = opener()
status, payload = call(admin_op, BASE, '/brand/admin/api/login', {'password': password})
if payload.get('success') is not True:
    blocked(steps, '管理页口令登录失败（HTTP %s: %s）' % (status, payload.get('message')), 2)
steps['password_login'] = {'http': status, 'success': True}
print('管理页登录:', status, '| cookie:', [c.name for c in admin_jar])

# The session route hands out the CSRF token the write guards expect.
status, session = call(admin_op, BASE, '/brand/admin/api/session')
CSRF['token'] = str(session.get('csrf_token') or '')
print('CSRF 令牌:', '已取得' if CSRF['token'] else '未取得')
steps['csrf_token'] = {'http': status, 'present': bool(CSRF['token'])}

status, summary = call(admin_op, BASE, '/brand/admin/api/summary')
if status != 200:
    blocked(steps, '读待核验失败（HTTP %s: %s）' % (status, summary.get('message')), 2)
steps['summary_before'] = {'counts': summary.get('counts'),
                           'pending': len(summary.get('pending') or [])}
# A quoted claim records `quoted_amount`, not `cny`: reading row['cny'] raises
# KeyError on exactly the rows this probe exists to inspect.
print('待核验:', summary.get('counts'), '| 行:', [
    (row['id'], row['uid'], row.get('quoted_amount') or row.get('cny'),
     row.get('username'))
    for row in (summary.get('pending') or [])])

# ---- create one claim through the public customer surface --------------
customer_op, _ = opener()
status, quote_payload = call(customer_op, PUBLIC, '/brand/api/topup/quote',
                             {'method': 'cny-qr', 'amount': AMOUNT})
quote = quote_payload.get('quote') or {}
print('报价:', status, quote.get('id'), quote.get('fx_rate'), quote.get('fx_source'))
if status != 200:
    blocked(steps, '取报价失败（HTTP %s: %s）' % (status, quote_payload.get('message')), 2)
steps['quote'] = {'id': quote['id'], 'fx_rate': quote['fx_rate'],
                  'fx_source': quote['fx_source'], 'fx_as_of': quote['fx_as_of'],
                  'usd': quote['usd']}

status, submitted = call(customer_op, PUBLIC, '/brand/api/topup/request',
                         {'uid': TARGET, 'quote_id': quote['id'], 'reference': ''})
record_id = submitted.get('request_id')
print('提交:', status, record_id)
if status != 200 or not record_id:
    # Since the credited account is derived server-side from the caller's token,
    # submitting a claim needs a real signed-in browser session, and a script
    # cannot hold one (login is Cloudflare Turnstile protected). That half is now
    # covered by tests/topup-page-check.js (the page) and by
    # probe_admin_selfheal_e2e.py (the service, on a throwaway instance); this
    # probe covers the operator half only, so it stops here rather than faking it.
    blocked(steps, '提交充值申请失败（HTTP %s: %s）' % (status, submitted.get('message')),
            2, action=('提交需要一个真实登录的浏览器会话，脚本拿不到（登录受 Cloudflare '
                       'Turnstile 保护）。用户侧提交由 tests/topup-page-check.js 核对，'
                       '服务端受理与归属由 ops/topup_watch/probe_admin_selfheal_e2e.py 核对；'
                       '本探针只覆盖经营者侧的入账。'))
steps['submitted'] = {'id': record_id, 'http': status}

# The operator's list must show exactly what the customer was quoted.
status, listing = call(admin_op, BASE, '/brand/admin/api/summary')
row = next((item for item in (listing.get('pending') or [])
            if item.get('id') == record_id), None)
if row is None:
    blocked(steps, '提交后未在待核验列表中看到该申请', 2)
print('待核验行:', json.dumps({k: row.get(k) for k in
                            ('id', 'uid', 'quoted_amount', 'usd', 'quota', 'fx_rate',
                             'fx_source', 'reference')},
                           ensure_ascii=False))
steps['pending_row'] = {k: row.get(k) for k in
                        ('id', 'uid', 'quoted_amount', 'usd', 'quota', 'fx_rate',
                         'fx_source', 'reference')}

# ---- credit ------------------------------------------------------------
status, result = call(admin_op, BASE, '/brand/admin/api/fulfill',
                      {'id': record_id, 'net_received': AMOUNT})
print('入账:', status, json.dumps(result, ensure_ascii=False)[:400])
steps['fulfill'] = {'http': status, 'message': result.get('message'),
                    'quota': result.get('quota'), 'balance_verified': result.get('balance_verified'),
                    'quota_before': result.get('quota_before'),
                    'quota_after': result.get('quota_after'),
                    'credited_usd': result.get('credited_usd')}
if status != 200:
    blocked(steps, '入账失败（HTTP %s: %s）' % (status, result.get('message')), 2)

status, twice = call(admin_op, BASE, '/brand/admin/api/fulfill',
                     {'id': record_id, 'net_received': AMOUNT})
print('重复入账:', status, twice.get('message'))
steps['fulfill_again'] = {'http': status, 'message': twice.get('message')}

status, summary_after = call(admin_op, BASE, '/brand/admin/api/summary')
still_pending = [item.get('id') for item in (summary_after.get('pending') or [])]
steps['summary_after'] = {'counts': summary_after.get('counts'),
                          'this_claim_still_pending': record_id in still_pending}

before = result.get('quota_before')
after = result.get('quota_after')
delta = None if before is None or after is None else after - before
expected_quota = result.get('quota')
verdict = {
    'quote_used_market_data': '东方财富' in str(quote.get('fx_source')),
    'claim_carried_the_quoted_rate': steps['pending_row']['fx_rate'] == quote['fx_rate'],
    'claim_carried_the_quoted_source': steps['pending_row']['fx_source'] == quote['fx_source'],
    'claim_quota_matches_credit': steps['pending_row']['quota'] == expected_quota,
    'credit_succeeded': delta == expected_quota,
    'balance_verified_by_service': bool(result.get('balance_verified')),
    'duplicate_blocked': status == 400 and '已处理' in str(twice.get('message')),
    'left_the_pending_list': record_id not in still_pending,
}
print()
print('额度: %s -> %s (delta %s，期望 %s)' % (before, after, delta, expected_quota))
print('VERDICT ' + json.dumps(verdict, ensure_ascii=False))
REPORT.write_text(json.dumps({
    'probe': 'ops/topup_watch/probe_credit_e2e.py',
    'status': 'PASS' if all(verdict.values()) else 'FAIL',
    'account': {'id': TARGET, 'quota_before': before, 'quota_after': after,
                'delta': delta},
    'steps': steps, 'verdict': verdict,
    'note': ('通过线上服务的 HTTP 接口完成，与经营者在管理页点「核实到账，加额度」是同一路径。'
             '只使用 gw_tester 账号；额度无法用脚本还原（登录受 Turnstile 保护），'
             '余额变化已在 account 字段中列出。'),
}, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
raise SystemExit(0 if all(verdict.values()) else 2)
