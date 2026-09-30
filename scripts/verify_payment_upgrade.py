"""Read-only/public acceptance checks for pricing, FX and payment metadata.

Runs against the live site without admin credentials, so everything here is what
an ordinary visitor can already see. Quote endpoints do append to the quote log
(as any real visitor's visit would); nothing else is modified.

    python scripts/verify_payment_upgrade.py --base https://omniapi.biz
"""
import argparse
import json
import re
import sys
import urllib.request
import urllib.error
from decimal import Decimal
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'scripts'))
import topup_requests  # noqa: E402  (single source of truth for the conversion)


def request(base, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base.rstrip('/') + path, data=data,
                                 headers={'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(req, timeout=20) as response:
            return response.status, response.read(), dict(response.headers)
    except urllib.error.HTTPError as error:
        return error.code, error.read(), dict(error.headers)


def configured_payoneer_link():
    """Expected link, read from the operator's config so the two cannot drift."""
    try:
        document = json.loads((ROOT / 'config/payment-methods.json').read_text(encoding='utf-8'))
    except (OSError, ValueError):
        return None
    for row in document.get('methods') or []:
        if row.get('id') == 'payoneer':
            return row.get('link')
    return None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--base', default='https://omniapi.biz')
    args = parser.parse_args()
    checks = {}

    # -- pricing page ----------------------------------------------------
    status, html, _ = request(args.base, '/pricing')
    text = html.decode('utf-8', errors='replace')
    checks['native_pricing'] = status == 200 and '/brand/models.js' not in text
    checks['pricing_enhancer'] = '/brand/pricing-enhance.js' in text

    status, raw, _ = request(args.base, '/api/pricing')
    pricing = json.loads(raw)
    rows = pricing.get('data') or []
    expr = re.compile(r'^v1:p \* \d+\.\d{2} \+ c \* \d+\.\d{2}$')
    checks['pricing_rows_29'] = status == 200 and len(rows) == 29
    checks['two_decimal_billing'] = all(expr.match(row.get('billing_expr', '')) for row in rows)
    checks['vendor_logos'] = all(row.get('vendor_id') and row.get('icon') for row in rows)

    # -- payment methods -------------------------------------------------
    status, raw, _ = request(args.base, '/brand/api/payment-methods')
    methods = json.loads(raw).get('methods') or []
    checks['payment_methods'] = status == 200 and {m['id'] for m in methods} >= {'cny-qr', 'payoneer'}
    # A rail that is offered must carry everything needed to pay it.
    checks['offered_methods_are_payable'] = all(
        (m.get('link') if m.get('kind') == 'payoneer' else m.get('qr_path'))
        for m in methods)

    # -- FX snapshot -----------------------------------------------------
    status, raw, _ = request(args.base, '/brand/api/fx/rates')
    fx = json.loads(raw)
    cny_rates = fx.get('rates') or {}
    checks['fx_enabled'] = status == 200 and fx.get('enabled') is True
    checks['fx_has_cny'] = bool(cny_rates.get('CNY'))
    checks['fx_reports_quote_time'] = bool((fx.get('rate_as_of') or {}).get('CNY')) or bool(fx.get('as_of'))
    # The provider must be named, and the basis recorded, so a quote is traceable.
    checks['fx_names_source'] = bool(fx.get('source')) and bool(fx.get('license_status'))

    # -- CNY quote: converted, and labelled with its rate and date -------
    status, raw, _ = request(args.base, '/brand/api/topup/quote',
                             {'method': 'cny-qr', 'amount': '20'})
    cny = json.loads(raw).get('quote') or {}
    rate = Decimal(str(cny.get('fx_rate') or '0'))
    checks['cny_quote'] = status == 200 and cny.get('currency') == 'CNY' and rate > 0
    checks['cny_quote_labels_rate'] = bool(cny.get('fx_source')) and bool(cny.get('fx_as_of'))
    # Compare against the shared converter, not a second copy of the rule. The
    # ad-hoc `.quantize()` that used to sit here rounded half-even, so for ¥20 it
    # accepted a USD figure one quota unit above what the ledger would grant; the
    # service now floors (`topup_requests.usd_from_cny`), and this check has to
    # hold it to that.
    checks['cny_quote_math'] = cny.get('usd') == str(topup_requests.usd_from_cny('20', rate))
    checks['cny_quote_never_overpromises'] = (
        Decimal(str(cny.get('usd') or '0')) <= (Decimal('20') / rate).quantize(Decimal('0.000001')))

    # -- USD quote: 1:1, no conversion needed ----------------------------
    status, raw, _ = request(args.base, '/brand/api/topup/quote',
                             {'method': 'payoneer', 'amount': '19.99'})
    checks['payoneer_minimum'] = status == 400
    status, raw, _ = request(args.base, '/brand/api/topup/quote',
                             {'method': 'payoneer', 'amount': '20'})
    quote = json.loads(raw).get('quote') or {}
    checks['payoneer_quote'] = status == 200 and quote.get('usd') == '20' and quote.get('currency') == 'USD'
    expected_link = configured_payoneer_link()
    checks['payoneer_link_matches_config'] = (
        bool(expected_link) and
        (quote.get('method_details') or {}).get('link') == expected_link)

    result = {'status': 'PASS' if all(checks.values()) else 'FAIL', 'checks': checks,
              'models': len(rows), 'public_methods': [m.get('id') for m in methods],
              'cny_rate': cny.get('fx_rate'), 'cny_source': cny.get('fx_source')}
    print(json.dumps(result, ensure_ascii=False, indent=2))
    raise SystemExit(0 if result['status'] == 'PASS' else 2)


if __name__ == '__main__':
    sys.exit(main())
