"""Payment-method configuration and CNY rate resolution.

Two behaviours are pinned here because both are easy to regress and expensive to
get wrong in production:

1. Collection destinations (links, wallet addresses) come from
   config/payment-methods.json, not from code, and an admin-page save may not
   rewrite them.
2. A CNY quote always resolves to *some* labelled rate: live market data when it
   is usable, the operator's declared rate when it is not, and a clear refusal
   only once even that is too old.

The FX feed is stubbed out for the whole module: a unit test must never reach
the real upstream, and it must not depend on whether one happens to be reachable.
"""
import datetime as dt
import json
import sys
import tempfile
import unittest
from decimal import Decimal
from pathlib import Path

BASE = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(BASE / 'scripts'))
sys.path.insert(0, str(BASE / 'ops' / 'topup_watch'))

import fx_rates  # noqa: E402
import topup_watch  # noqa: E402

CONFIG = {
    'cny_per_usd': '6.7489', 'min_amount_cny': 1, 'qr_path': '/brand/payment-qr.png',
    'fx_as_of': '2026-09-25',
    'fx': {'enabled': True, 'provider': 'eastmoney', 'refresh_seconds': 300,
           'max_age_seconds': 345600, 'fallback_max_age_seconds': 2592000},
}
METHODS = [
    {'id': 'payoneer', 'kind': 'payoneer', 'title': 'Payoneer', 'currency': 'USD',
     'minimum': '20', 'enabled': True, 'sort': 20,
     'link': 'https://link.payoneer.com/Token?t=EXAMPLE', 'qr_path': '',
     'instructions': '仅收 USD。'},
    {'id': 'usdt-trc20', 'kind': 'crypto', 'title': 'USDT · TRC20', 'currency': 'USD',
     'asset': 'USDT', 'network': 'TRC20', 'minimum': '20', 'enabled': False, 'sort': 30,
     'address': 'T-example-address', 'qr_path': '/brand/api/payment-assets/usdt.png',
     'instructions': '仅收 TRC20。'},
]


def all_methods(**switches):
    """A full admin-page payload, as the real settings page sends it."""
    ids = ['cny-qr'] + [row['id'] for row in topup_watch.BUILTIN_METHODS]
    return [{'id': method_id, 'enabled': switches.get(method_id, method_id == 'cny-qr')}
            for method_id in ids]


class ServiceTestCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.config_path = root / 'topup.json'
        self.config_path.write_text(json.dumps(CONFIG), encoding='utf-8')
        self.methods_path = root / 'payment-methods.json'
        self.write_methods(METHODS)
        self.service = topup_watch.Service(
            root / 'secrets.json', root / 'requests.jsonl', root / 'ledger.json',
            self.config_path, gateway_url='http://127.0.0.1:1',
            methods_config_path=self.methods_path)
        self._refresh = topup_watch.fx_rates.refresh
        topup_watch.fx_rates.refresh = self.refuse
        self.refreshed = 0

    def tearDown(self):
        topup_watch.fx_rates.refresh = self._refresh
        self.tmp.cleanup()

    @staticmethod
    def refuse(*_args, **_kwargs):
        """Stand in for an unreachable feed."""
        raise OSError('feed unavailable')

    def write_methods(self, rows):
        self.methods_path.write_text(json.dumps({'version': 1, 'methods': rows}),
                                     encoding='utf-8')

    def configured(self, method_id):
        """Read a method regardless of whether it is currently enabled."""
        rows = {row['id']: row for row in self.service.payment_methods(public=False)}
        return rows[method_id]

    def seed_snapshot(self, as_of, cny='6.80', fetched_at=None):
        """Write a cache file. Defaults to "just fetched" so no refresh is attempted."""
        snapshot = {'base': 'USD', 'rates': {'USD': '1', 'CNY': cny},
                    'source': fx_rates.EASTMONEY_SOURCE, 'as_of': as_of,
                    'fetched_at': fetched_at or dt.datetime.now(dt.timezone.utc).isoformat(),
                    'license_status': fx_rates.EASTMONEY_LICENSE_STATUS}
        self.service.fx_cache_path.write_text(json.dumps(snapshot), encoding='utf-8')


class PaymentMethodConfigTests(ServiceTestCase):
    def test_collection_details_come_from_the_operator_file(self):
        payoneer = self.service.method('payoneer')
        self.assertEqual(payoneer['link'], 'https://link.payoneer.com/Token?t=EXAMPLE')
        self.assertEqual(payoneer['minimum'], '20')

    def test_editing_the_file_changes_the_link_without_a_code_change(self):
        self.write_methods([dict(METHODS[0], link='https://link.payoneer.com/Token?t=CHANGED'),
                            METHODS[1]])
        self.assertEqual(self.service.method('payoneer')['link'],
                         'https://link.payoneer.com/Token?t=CHANGED')

    def test_an_unknown_id_in_the_file_is_refused_not_ignored(self):
        self.write_methods(METHODS + [{'id': 'paypa1', 'kind': 'payoneer'}])
        with self.assertRaisesRegex(ValueError, '未知支付方式'):
            self.service.payment_methods()

    def test_a_payoneer_without_a_link_is_not_offered(self):
        self.write_methods([dict(METHODS[0], link=''), METHODS[1]])
        self.assertNotIn('payoneer',
                         [row['id'] for row in self.service.payment_methods(public=True)])

    def test_the_admin_save_cannot_rewrite_a_collection_destination(self):
        payload = all_methods(payoneer=True)
        for row in payload:
            if row['id'] == 'payoneer':
                row.update(link='https://evil.example/collect', minimum='99')
            if row['id'] == 'usdt-trc20':
                row.update(address='attacker-wallet')
        self.service.save_payment_methods(payload, operator='operator')
        payoneer = self.service.method('payoneer')
        self.assertEqual(payoneer['link'], 'https://link.payoneer.com/Token?t=EXAMPLE')
        self.assertEqual(payoneer['minimum'], '20')
        self.assertEqual(self.configured('usdt-trc20')['address'], 'T-example-address')

    def test_the_admin_save_still_persists_the_switch(self):
        self.service.save_payment_methods(all_methods())
        self.assertNotIn('payoneer',
                         [row['id'] for row in self.service.payment_methods(public=True)])
        self.service.save_payment_methods(all_methods(payoneer=True))
        self.assertIn('payoneer',
                      [row['id'] for row in self.service.payment_methods(public=True)])
        # What gets written back must not carry any collection destination.
        stored = json.loads(self.service.payment_settings_path.read_text(encoding='utf-8'))
        self.assertEqual(stored['version'], 2)
        self.assertNotIn('link', json.dumps(stored))
        self.assertNotIn('address', json.dumps(stored))

    def test_a_legacy_settings_file_cannot_restore_an_old_link(self):
        self.service.payment_settings_path.write_text(json.dumps({
            'version': 1, 'methods': [
                {'id': 'payoneer', 'enabled': True, 'sort': 20,
                 'link': 'https://legacy.example/collect', 'currency': 'USD',
                 'minimum': '20'}]}), encoding='utf-8')
        payoneer = self.service.method('payoneer')
        self.assertEqual(payoneer['link'], 'https://link.payoneer.com/Token?t=EXAMPLE')
        self.assertTrue(payoneer['enabled'])

    def test_enabling_an_unconfigured_rail_is_refused_with_a_reason(self):
        self.write_methods([METHODS[0], dict(METHODS[1], address='', qr_path='')])
        with self.assertRaisesRegex(ValueError, 'config/payment-methods.json'):
            self.service.save_payment_methods(all_methods(payoneer=True, **{'usdt-trc20': True}))

    def test_an_uploaded_qr_is_accepted_only_from_the_managed_directory(self):
        assets = self.service.payment_assets_path
        assets.mkdir(parents=True, exist_ok=True)
        (assets / 'usdt-trc20.png').write_bytes(b'\x89PNG\r\n\x1a\n')
        payload = all_methods()
        for row in payload:
            if row['id'] == 'usdt-trc20':
                row['qr_path'] = '/brand/api/payment-assets/usdt-trc20.png'
        self.service.save_payment_methods(payload)
        self.assertEqual(self.configured('usdt-trc20')['qr_path'],
                         '/brand/api/payment-assets/usdt-trc20.png')
        # An arbitrary path is ignored rather than stored.
        payload = all_methods()
        for row in payload:
            if row['id'] == 'usdt-trc20':
                row['qr_path'] = 'https://evil.example/qr.png'
        self.service.save_payment_methods(payload)
        self.assertEqual(self.configured('usdt-trc20')['qr_path'],
                         '/brand/api/payment-assets/usdt-trc20.png')

    def test_a_missing_methods_file_falls_back_to_safe_skeletons(self):
        self.methods_path.unlink()
        ids = [row['id'] for row in self.service.payment_methods(public=True)]
        self.assertEqual(ids, ['cny-qr'], 'unconfigured rails must stay unofferable')


class CnyRateTests(ServiceTestCase):
    def test_a_usable_snapshot_prices_the_order_and_is_labelled(self):
        self.seed_snapshot('2026-09-27T04:00:00+00:00', cny='6.80')
        quote = self.service.quote({'method': 'cny-qr', 'amount': '100'})
        self.assertEqual(Decimal(quote['fx_rate']), Decimal('6.80'))
        self.assertIn('东方财富', quote['fx_source'])
        self.assertNotIn('fx_note', quote)

    def test_an_old_quote_is_labelled_as_the_latest_published_one(self):
        self.seed_snapshot('2026-09-24T08:00:00+00:00', cny='6.80')
        quote = self.service.quote({'method': 'cny-qr', 'amount': '100'})
        self.assertEqual(Decimal(quote['fx_rate']), Decimal('6.80'))
        self.assertIn('最新已公布报价', quote['fx_source'])
        self.assertEqual(quote['fx_as_of'], '2026-09-24T08:00:00+00:00')
        self.assertNotIn('fx_note', quote)

    def test_a_broken_feed_falls_back_to_the_declared_rate_and_says_so(self):
        quote = self.service.quote({'method': 'cny-qr', 'amount': '100'})
        self.assertEqual(Decimal(quote['fx_rate']), Decimal('6.7489'))
        self.assertIn('备用汇率', quote['fx_source'])
        self.assertIn('备用汇率', quote['fx_note'])
        self.assertEqual(quote['fx_as_of'], '2026-09-25')

    def test_a_failed_refresh_keeps_serving_the_last_good_snapshot(self):
        self.seed_snapshot('2026-09-27T04:00:00+00:00', cny='6.80',
                           fetched_at='2026-09-20T00:00:00+00:00')
        quote = self.service.quote({'method': 'cny-qr', 'amount': '100'})
        self.assertEqual(Decimal(quote['fx_rate']), Decimal('6.80'))
        self.assertIn('东方财富', quote['fx_source'])

    def test_a_snapshot_older_than_the_market_window_is_not_priced(self):
        """Past the market window the last real quote still beats a hand-set number."""
        self.seed_snapshot('2026-01-01T00:00:00+00:00', cny='6.80')
        quote = self.service.quote({'method': 'cny-qr', 'amount': '100'})
        self.assertEqual(Decimal(quote['fx_rate']), Decimal('6.7489'))
        self.assertIn('备用汇率', quote['fx_note'])

    def test_a_weekend_old_quote_is_reused_before_the_declared_rate(self):
        """Four days is beyond the market window but well inside the fallback one."""
        recent = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(hours=5 * 24)
                  ).replace(microsecond=0).isoformat()
        self.seed_snapshot(recent, cny='6.80')
        quote = self.service.quote({'method': 'cny-qr', 'amount': '100'})
        self.assertEqual(Decimal(quote['fx_rate']), Decimal('6.80'))
        self.assertIn('最近一次报价', quote['fx_source'])
        self.assertIn('最近一次行情报价', quote['fx_note'])

    def test_an_expired_declared_rate_refuses_instead_of_guessing(self):
        config = dict(CONFIG)
        config['fx_as_of'] = '2026-01-01'
        self.config_path.write_text(json.dumps(config), encoding='utf-8')
        with self.assertRaisesRegex(ValueError, '备用汇率'):
            self.service.quote({'method': 'cny-qr', 'amount': '100'})

    def test_a_disabled_fx_service_still_prices_with_the_declared_rate(self):
        config = dict(CONFIG)
        config['fx'] = dict(CONFIG['fx'], enabled=False)
        self.config_path.write_text(json.dumps(config), encoding='utf-8')
        quote = self.service.quote({'method': 'cny-qr', 'amount': '100'})
        self.assertEqual(Decimal(quote['fx_rate']), Decimal('6.7489'))
        self.assertIn('备用汇率', quote['fx_source'])

    def test_the_snapshot_endpoint_reports_a_failure_without_a_traceback(self):
        self.seed_snapshot('2026-09-27T04:00:00+00:00', cny='6.80',
                           fetched_at='2026-09-20T00:00:00+00:00')
        snapshot = self.service.fx_snapshot()
        self.assertIn('OSError', snapshot['refresh_error'])
        self.assertEqual(snapshot['rates']['CNY'], '6.80')
        self.assertTrue(snapshot['enabled'])

    def test_a_usd_order_needs_no_rate_at_all(self):
        quote = self.service.quote({'method': 'payoneer', 'amount': '20'})
        self.assertEqual(Decimal(quote['fx_rate']), Decimal('1'))
        self.assertEqual(quote['fx_source'], 'USD settlement')


if __name__ == '__main__':
    unittest.main()
