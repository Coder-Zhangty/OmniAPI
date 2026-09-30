#!/usr/bin/env python3
"""End-to-end check of the market-priced quote path (no admin session needed).

Exercises what a real CNY customer does — get a quote, submit a claim — plus the
operator's pending list and the FX fallback ladder, against the live gateway.
Temporary events/ledger/quote files are used, so the operator's pending list and
the live quote log are never modified. Nothing is credited here: the credit step
needs an operator dashboard token and lives in probe_credit_e2e.py.

Known limitation: the operator's account is signed in to the console via
Cloudflare Turnstile, so no script can obtain a dashboard token; that is why the
credit step is split out rather than faked.
"""
import json
import sys
import tempfile
from decimal import Decimal
from pathlib import Path

ROOT = Path('/opt/ai-gateway')
sys.path.insert(0, str(ROOT / 'scripts'))
sys.path.insert(0, str(ROOT / 'ops' / 'topup_watch'))
import fx_rates  # noqa: E402
import topup_requests as queue  # noqa: E402
import topup_watch  # noqa: E402

TARGET = 2
AMOUNT = '20'
BASE = 'https://omniapi.biz'
LIVE_EVENTS = ROOT / 'runtime/topup-requests.jsonl'
LIVE_QUOTES = ROOT / 'runtime/topup-quotes.jsonl'


def service_at(folder):
    return topup_watch.Service(ROOT / '.secrets/topup-admin.json',
                               Path(folder) / 'requests.jsonl',
                               Path(folder) / 'ledger.json',
                               ROOT / 'config/topup.json', gateway_url=BASE)


def count_lines(path):
    try:
        return sum(1 for line in path.read_text(encoding='utf-8').splitlines() if line.strip())
    except OSError:
        return 0


report = {'probe': 'ops/topup_watch/probe_fx_quote_e2e.py', 'amount_cny': AMOUNT}
live_before = queue.counts(LIVE_EVENTS)
quotes_before = count_lines(LIVE_QUOTES)
print('线上真实队列:', live_before, '| 线上报价日志行数:', quotes_before)

tmp = tempfile.mkdtemp()
service = service_at(tmp)

# 1) quote with live market data -----------------------------------------
quote = service.quote({'method': 'cny-qr', 'amount': AMOUNT})
print('报价:', json.dumps({k: quote.get(k) for k in
                          ('id', 'currency', 'quoted_amount', 'usd', 'fx_rate',
                           'fx_source', 'fx_as_of', 'fx_note')}, ensure_ascii=False))
# Compare against the shared converter rather than a second copy of the rule.
# The ad-hoc `.quantize()` that used to live here rounded half-even, which for
# ¥20 credited one quota unit *more* than the catalogue promised; the service now
# floors, so the probe has to check the same function it ships.
expected_usd = queue.usd_from_cny(AMOUNT, quote['fx_rate'])
rounded = (Decimal(AMOUNT) / Decimal(str(quote['fx_rate']))).quantize(Decimal('0.000001'))
report['quote'] = {'fx_rate': quote['fx_rate'], 'fx_source': quote['fx_source'],
                   'fx_as_of': quote['fx_as_of'], 'usd': quote['usd'],
                   'note': quote.get('fx_note'),
                   'usd_if_rounded': str(rounded)}
verdict = {
    'quote_uses_market_data': '东方财富' in str(quote['fx_source']),
    'quote_labels_rate_time': bool(quote['fx_as_of']),
    'quote_has_no_warning_when_fresh': 'fx_note' not in quote,
    'quote_math_matches_rate': quote['usd'] == str(expected_usd),
    # Flooring can only under-promise. A rounded value must never win, or the
    # page advertises a quota the ledger will not hand over.
    'quote_never_promises_more_than_the_ledger': Decimal(quote['usd']) <= rounded,
}

# 2) submit the quote ----------------------------------------------------
submitted = service.submit({'quote_id': quote['id'], 'reference': ''}, '127.0.0.1',
                           {'id': TARGET, 'username': 'probe',
                            'display_name': 'Probe', 'public_id': ''})
record = submitted['request']
print('提交:', record['id'], '| quote_id', record['quote_id'],
      '| 期望额度', record['quota'], '| 记录汇率', record['fx_rate'], record['fx_source'])
verdict['submit_keeps_quote_rate'] = record.get('fx_rate') == quote['fx_rate']
verdict['submit_records_source'] = record.get('fx_source') == quote['fx_source']
verdict['submit_is_not_duplicate'] = not submitted['duplicate']
verdict['pending_quota_matches_quote'] = record['quota'] == queue.quota_from_usd(quote['usd'])

summary = service.summary()
print('待核验:', summary['counts'], '| 行数:', len(summary['pending']))
verdict['admin_sees_pending'] = any(row['id'] == record['id'] for row in summary['pending'])
report['pending_row'] = {k: record.get(k) for k in
                         ('id', 'uid', 'cny', 'usd', 'quota', 'fx_rate', 'fx_source')}

# 3) the fallback ladder, on this host, without touching the live snapshot.
# The feed is stubbed out so the ladder is exercised deterministically instead of
# being papered over by a successful live fetch.
def feed_outage(*_args, **_kwargs):
    raise OSError('simulated feed outage')


original_refresh = fx_rates.refresh
fx_rates.refresh = feed_outage
try:
    empty = tempfile.mkdtemp()
    fallback_service = service_at(empty)
    fallback_service.fx_cache_path = Path(empty) / 'absent-fx.json'
    fallback = fallback_service.quote({'method': 'cny-qr', 'amount': AMOUNT})

    # A stale-but-recent market quote must be preferred over the hand-set number.
    stale_folder = tempfile.mkdtemp()
    stale_service = service_at(stale_folder)
    stale_service.fx_cache_path = Path(stale_folder) / 'fx-rates.json'
    stale_service.fx_cache_path.write_text(json.dumps({
        'base': 'USD', 'rates': {'USD': '1', 'CNY': '6.9000'},
        'source': fx_rates.EASTMONEY_SOURCE, 'as_of': '2026-09-19T00:00:00+00:00',
        'fetched_at': '2026-09-19T00:00:00+00:00',
    }), encoding='utf-8')
    stale = stale_service.quote({'method': 'cny-qr', 'amount': AMOUNT})
finally:
    fx_rates.refresh = original_refresh

print('无快照时的报价:', json.dumps({k: fallback.get(k) for k in
                                 ('fx_rate', 'fx_source', 'fx_as_of', 'fx_note')},
                                ensure_ascii=False))
report['fallback_quote'] = {k: fallback.get(k) for k in
                            ('fx_rate', 'fx_source', 'fx_as_of', 'fx_note')}
verdict['fallback_is_labelled'] = ('备用汇率' in str(fallback['fx_source']) and
                                   bool(fallback.get('fx_note')))
verdict['fallback_rate_matches_config'] = (
    fallback['fx_rate'] == str(service.config()['cny_per_usd']))

print('报价过期时的报价:', json.dumps({k: stale.get(k) for k in
                                 ('fx_rate', 'fx_source', 'fx_as_of', 'fx_note')},
                                ensure_ascii=False))
report['stale_quote'] = {k: stale.get(k) for k in
                         ('fx_rate', 'fx_source', 'fx_as_of', 'fx_note')}
verdict['recent_market_quote_beats_declared_rate'] = (
    Decimal(str(stale['fx_rate'])) == Decimal('6.9') and
    '最近一次报价' in str(stale['fx_source']))

# 4) the operator's live state must be untouched -------------------------
live_after = queue.counts(LIVE_EVENTS)
quotes_after = count_lines(LIVE_QUOTES)
verdict['live_queue_untouched'] = live_before == live_after
verdict['live_quote_log_untouched'] = quotes_before == quotes_after
print('线上真实队列:', live_after, '| 线上报价日志行数:', quotes_after)

report['steps'] = {'live_counts_before': live_before, 'live_counts_after': live_after,
                   'live_quote_lines_before': quotes_before,
                   'live_quote_lines_after': quotes_after}
report['verdict'] = verdict
report['not_covered'] = ('实际入账（credit）需要经营者控制台令牌，见 probe_credit_e2e.py；'
                         '登录接口受 Cloudflare Turnstile 保护，脚本不得代替人机验证。')
print()
print('VERDICT ' + json.dumps(verdict, ensure_ascii=False))
Path('/opt/ai-gateway/runtime/topup-fx-quote-e2e.json').write_text(
    json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
