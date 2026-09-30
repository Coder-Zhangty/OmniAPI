import json
import sys
import tempfile
import unittest
from decimal import Decimal
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
import credit_topup
import publish_topup
from commercial_quote import quota_from_usd, usd_from_cny


class RechargeCatalogTests(unittest.TestCase):
    """The recharge page must never advertise an amount the operator cannot honour."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        (self.root / 'nginx/brand').mkdir(parents=True)
        (self.root / 'nginx/brand/payment-qr.png').write_bytes(b'fixture')
        self.out = self.root / 'nginx/brand/topup.json'
        self.config_path = self.root / 'topup.json'
        publish_topup.ROOT = self.root
        publish_topup.CATALOG_PATH = self.out

    def tearDown(self):
        self.temp.cleanup()

    def config(self, **overrides):
        base = {'settlement_currency': 'CNY', 'cny_per_usd': 7, 'fx_source': 'test',
                'payment_mode': 'manual_verified_receipt', 'contact': 'a@example.com',
                'qr_path': '/brand/payment-qr.png', 'min_amount_cny': 10,
                'amounts_cny': [10, 20, 50]}
        base.update(overrides)
        self.config_path.write_text(json.dumps(base), encoding='utf-8')
        return self.config_path

    def test_amounts_convert_with_the_shared_formula(self):
        result = publish_topup.publish(self.config(), dry_run=True)
        rows = {r['cny']: r for r in result['catalog']['amounts']}
        self.assertEqual(rows['10']['usd'], '1.428571')
        self.assertEqual(rows['10']['quota'], 714285)
        self.assertEqual(rows['50']['quota'], 3571428)
        # Cross-check against the very formula the plan pricing uses.
        self.assertEqual(rows['20']['quota'], quota_from_usd(usd_from_cny(20, 7)))

    def test_missing_rate_is_blocked_and_writes_nothing(self):
        result = publish_topup.publish(self.config(cny_per_usd=None))
        self.assertEqual(result['status'], 'BLOCKED')
        self.assertTrue(any('cny_per_usd' in reason for reason in result['reasons']))
        self.assertFalse(self.out.exists())

    def test_amount_below_minimum_is_blocked(self):
        result = publish_topup.publish(self.config(min_amount_cny=50))
        self.assertEqual(result['status'], 'BLOCKED')
        self.assertTrue(any('低于 min_amount_cny' in reason for reason in result['reasons']))

    def test_missing_qr_image_is_blocked(self):
        result = publish_topup.publish(self.config(qr_path='/brand/nope.png'))
        self.assertEqual(result['status'], 'BLOCKED')
        self.assertTrue(any('收款码图片不存在' in reason for reason in result['reasons']))

    def test_qr_outside_brand_is_blocked(self):
        result = publish_topup.publish(self.config(qr_path='http://evil/x.png'))
        self.assertEqual(result['status'], 'BLOCKED')

    def test_duplicate_amounts_are_collapsed(self):
        result = publish_topup.publish(self.config(amounts_cny=[10, 10.0, 20]), dry_run=True)
        self.assertEqual([r['cny'] for r in result['catalog']['amounts']], ['10', '20'])

    def test_not_collecting_without_the_operator_flag(self):
        result = publish_topup.publish(self.config(accepting_payments=False), dry_run=True)
        self.assertFalse(result['catalog']['accepting_payments'])

    def test_accepting_without_contact_raises(self):
        with self.assertRaises(ValueError):
            publish_topup.publish(self.config(accepting_payments=True, contact=''), dry_run=True)

    def test_publish_writes_the_catalog(self):
        result = publish_topup.publish(self.config(accepting_payments=True))
        self.assertEqual(result['status'], 'PUBLISHED')
        payload = json.loads(self.out.read_text(encoding='utf-8'))
        self.assertTrue(payload['accepting_payments'])
        self.assertEqual(len(payload['amounts']), 3)
        self.assertEqual(payload['quota_per_usd'], 500000)


class CreditTopupTests(unittest.TestCase):
    """A receipt must convert identically to the price the page advertised."""

    def config(self, **overrides):
        base = {'cny_per_usd': 6.7489, 'min_amount_cny': 10}
        base.update(overrides)
        return base

    def test_amount_uses_the_same_rate_as_the_page(self):
        record = credit_topup.plan_record(self.config(), 7, '20', 'TX-1')
        self.assertEqual(record['usd'], '2.963445')
        self.assertEqual(record['quota'], 1481722)
        self.assertEqual(record['quota'],
                         quota_from_usd(usd_from_cny(Decimal('20'), Decimal('6.7489'))))

    def test_empty_transaction_is_refused(self):
        with self.assertRaises(ValueError):
            credit_topup.plan_record(self.config(), 7, '20', '   ')

    def test_amount_below_minimum_is_refused(self):
        with self.assertRaises(ValueError):
            credit_topup.plan_record(self.config(), 7, '5', 'TX-1')

    def test_missing_rate_is_refused(self):
        with self.assertRaises(ValueError):
            credit_topup.plan_record(self.config(cny_per_usd=None), 7, '20', 'TX-1')

    def test_code_name_stays_within_the_native_limit(self):
        for transaction in ('A', 'ZZ-DBG-1', '2026092612345678901234567890'):
            name = credit_topup.code_name({'amount_cny': '20', 'transaction_id': transaction})
            self.assertLessEqual(len(name), credit_topup.MAX_CODE_NAME)
            self.assertTrue(name.startswith('充值20元-'))

    def test_long_amount_still_fits_the_name_limit(self):
        name = credit_topup.code_name({'amount_cny': '12345678.90',
                                       'transaction_id': '2026092612345678901234567890'})
        self.assertLessEqual(len(name), credit_topup.MAX_CODE_NAME)

    def test_ledger_matches_a_credited_transaction(self):
        ledger = {'records': [{'transaction_id': 'TX-1', 'code': 'abc'}]}
        self.assertTrue(credit_topup.find_record(ledger, ' TX-1 '))
        self.assertIsNone(credit_topup.find_record(ledger, 'TX-2'))


class FakeGateway:
    """Stands in for New API: records calls and models the user-cache lag."""

    def __init__(self, quota=498211, lag=0, known=True):
        self.quota = quota
        self.lag = lag
        self.known = known
        self.pending = 0
        self.calls = []

    def _visible(self):
        if self.pending and self.lag > 0:
            self.lag -= 1
            return self.quota
        self.quota += self.pending
        self.pending = 0
        return self.quota

    def ok(self, path, body=None, method=None):
        self.calls.append((path, body, method))
        if path == credit_topup.QUOTA_API:
            assert body['action'] == 'add_quota' and body['mode'] == 'add', body
            assert body['value'] > 0, body
            self.pending += body['value']
            return {'success': True}
        if path.startswith('/api/user/'):
            if not self.known:
                return {}
            return {'id': int(path.rsplit('/', 1)[1]), 'username': 'gw_tester',
                    'quota': self._visible()}
        raise AssertionError('unexpected call %r' % ((path, body, method),))

    def adds(self):
        return [call for call in self.calls if call[0] == credit_topup.QUOTA_API]


class BalanceCreditTests(unittest.TestCase):
    """直接给用户钱包加额度：一次原子增量，用户无需再做任何操作。"""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.config = root / 'topup.json'
        self.config.write_text(json.dumps({'cny_per_usd': '6.7489', 'min_amount_cny': 1}),
                               encoding='utf-8')
        self.ledger = root / 'ledger.json'
        self.gateway = FakeGateway()
        self.original = (credit_topup.BALANCE_WAIT_ATTEMPTS, credit_topup.BALANCE_WAIT_SECONDS)
        credit_topup.BALANCE_WAIT_ATTEMPTS = 2
        credit_topup.BALANCE_WAIT_SECONDS = 0

    def tearDown(self):
        credit_topup.BALANCE_WAIT_ATTEMPTS, credit_topup.BALANCE_WAIT_SECONDS = self.original
        self.tmp.cleanup()

    def credit(self, amount='20', transaction='TX-1', apply=True, via='balance', user_id=7):
        return credit_topup.credit(self.config, self.ledger, user_id, amount, transaction,
                                   apply_changes=apply, api=self.gateway, via=via)

    def ledger_records(self):
        return json.loads(Path(self.ledger).read_text(encoding='utf-8'))['records']

    def test_one_atomic_increment_lands_in_the_wallet(self):
        result = self.credit()
        self.assertEqual(result['status'], 'PASS')
        self.assertTrue(result['ledger_written'])
        self.assertEqual(self.gateway.quota, 498211 + 1481722)
        adds = self.gateway.adds()
        self.assertEqual(len(adds), 1)
        self.assertEqual(adds[0][1]['value'], 1481722)
        self.assertEqual(adds[0][1]['id'], 7)
        record = self.ledger_records()[0]
        self.assertEqual(record['via'], 'balance')
        self.assertEqual(record['quota_before'], 498211)
        self.assertEqual(record['quota_after'], 498211 + 1481722)
        self.assertTrue(record['balance_verified'])
        self.assertNotIn('code', record)

    def test_a_receipt_cannot_be_credited_twice(self):
        self.credit()
        before = self.gateway.quota
        with self.assertRaises(ValueError):
            self.credit()
        self.assertEqual(self.gateway.quota, before)
        self.assertEqual(len(self.gateway.adds()), 1)
        self.assertEqual(len(self.ledger_records()), 1)

    def test_dry_run_touches_neither_the_wallet_nor_the_ledger(self):
        result = self.credit(apply=False)
        self.assertEqual(result['status'], 'DRY_RUN')
        self.assertEqual(self.gateway.adds(), [])
        self.assertEqual(self.gateway.quota, 498211)
        self.assertFalse(Path(self.ledger).exists())

    def test_a_lagging_balance_is_flagged_but_still_recorded(self):
        # 接口说成功，但余额一直读不到期望值：必须留痕，并且拒绝重试（防重复入账）。
        self.gateway.lag = 99
        result = self.credit()
        self.assertEqual(result['status'], 'PASS')
        record = self.ledger_records()[0]
        self.assertFalse(record['balance_verified'])
        self.assertIn('核对', result['note'])
        with self.assertRaises(ValueError):
            self.credit()

    def test_unknown_user_is_refused_before_any_credit(self):
        self.gateway.known = False
        with self.assertRaises(ValueError):
            self.credit()
        self.assertEqual(self.gateway.adds(), [])

    def test_unknown_delivery_mode_is_refused(self):
        with self.assertRaises(ValueError):
            self.credit(via='magic')
        self.assertEqual(self.gateway.adds(), [])

    def test_code_mode_still_produces_a_name_and_no_balance_change(self):
        class CodeGateway(FakeGateway):
            def __init__(self):
                super().__init__()
                self.codes = []

            def ok(self, path, body=None, method=None):
                if path.startswith('/api/redemption/'):
                    self.calls.append((path, body, method))
                    if body:
                        self.codes.append(body)
                        return ['CODE-1']
                    return {'items': [{'id': 5, 'key': 'CODE-1'}]}
                return super().ok(path, body, method)

        gateway = CodeGateway()
        result = credit_topup.credit(self.config, self.ledger, 7, '20', 'TX-9',
                                     apply_changes=True, api=gateway, via='code')
        self.assertEqual(result['issued']['code'], 'CODE-1')
        self.assertEqual(gateway.adds(), [])
        self.assertEqual(gateway.quota, 498211)
        self.assertEqual(gateway.codes[0]['quota'], 1481722)
        self.assertLessEqual(len(gateway.codes[0]['name']), credit_topup.MAX_CODE_NAME)

    def test_usd_net_receipt_credits_exact_usd_quota(self):
        result = credit_topup.credit_usd(self.ledger, 7, '20.50', 'PAYONEER-1',
                                         apply_changes=True, api=self.gateway,
                                         metadata={'method': 'payoneer'})
        self.assertEqual(result['issued']['quota'], 10250000)
        self.assertEqual(result['issued']['usd'], '20.5')
        self.assertEqual(result['issued']['method'], 'payoneer')


if __name__ == '__main__':
    unittest.main()
