import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
import create_plans
import publish_plans


class CreatePlansTests(unittest.TestCase):
    """Native plans must mirror the quoted catalog and never double-grant."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.costs = root / 'supplier-costs.json'
        self.catalog = root / 'REAL_MODELS.json'
        self.config = root / 'commercial.json'
        self.costs.write_text(json.dumps({
            'currency': 'USD', 'models': {'sample': [1.0, 2.0], 'unverified': [9.0, 9.0]},
        }), encoding='utf-8')
        self.catalog.write_text(json.dumps({'results': [
            {'model': 'sample', 'status': 'PASS'},
            {'model': 'broken', 'status': 'FAIL'},
        ]}), encoding='utf-8')
        self.config.write_text(json.dumps({
            'markup': 2, 'settlement_currency': 'CNY', 'cny_per_usd': 7,
            'payment_mode': 'manual_verified_receipt', 'models': {},
            'plans': [{'title': '体验包', 'duration_days': 7, 'sale_cny': 70,
                       'quota_reset_period': 'never'}],
        }), encoding='utf-8')
        # merge_models() reads the supplier table through publish_plans.
        publish_plans.SUPPLIER_COSTS = self.costs
    def tearDown(self):
        self.temp.cleanup()

    def payloads(self):
        return create_plans.planned(self.config, self.catalog)[1]

    def test_payload_matches_the_quote_and_is_always_disabled(self):
        payload = self.payloads()[0]
        self.assertEqual(payload['title'], '体验包')
        self.assertEqual(payload['price_amount'], 10.0)
        self.assertEqual(payload['total_amount'], 5000000)
        self.assertEqual(payload['currency'], 'USD')
        self.assertEqual(payload['duration_unit'], 'day')
        self.assertEqual(payload['duration_value'], 7)
        self.assertEqual(payload['quota_reset_period'], 'never')
        self.assertFalse(payload['enabled'])
        self.assertEqual(payload['subtitle'], '')

    def test_price_amount_is_a_number_not_a_string(self):
        # The native Go struct binds price_amount to float64; a string would be rejected.
        self.assertIsInstance(self.payloads()[0]['price_amount'], float)

    def test_internal_receipt_field_is_not_sent_to_the_backend(self):
        payload = self.payloads()[0]
        self.assertIn('_manual_receipt_cny', payload)
        body = {k: v for k, v in payload.items() if not k.startswith('_')}
        self.assertNotIn('_manual_receipt_cny', body)
        self.assertEqual(body['total_amount'], 5000000)

    def test_operator_subtitle_is_used_when_supplied(self):
        config = json.loads(self.config.read_text(encoding='utf-8'))
        config['plans'][0]['subtitle'] = '经营者填写的说明'
        self.config.write_text(json.dumps(config), encoding='utf-8')
        self.assertEqual(self.payloads()[0]['subtitle'], '经营者填写的说明')

    def test_existing_title_with_the_same_quota_is_reused_not_duplicated(self):
        payloads = self.payloads()
        existing = [{'id': 7, 'title': ' 体验包 ', 'total_amount': 5000000,
                     'duration_value': 7, 'duration_unit': 'day', 'enabled': True}]
        self.assertEqual(create_plans.conflicting_titles(payloads, existing), [])
        self.assertEqual(create_plans.by_title(existing)['体验包']['id'], 7)

    def test_existing_title_with_a_different_quota_is_refused(self):
        payloads = self.payloads()
        existing = [{'id': 7, 'title': '体验包', 'total_amount': 999,
                     'duration_value': 7, 'duration_unit': 'day', 'enabled': False}]
        self.assertEqual(create_plans.conflicting_titles(payloads, existing), ['体验包'])
        self.assertEqual(create_plans.conflicting_titles(payloads, []), [])

    def test_existing_title_with_a_different_duration_is_refused(self):
        payloads = self.payloads()
        existing = [{'id': 7, 'title': '体验包', 'total_amount': 5000000,
                     'duration_value': 30, 'duration_unit': 'day', 'enabled': False}]
        self.assertEqual(create_plans.conflicting_titles(payloads, existing), ['体验包'])

    def test_missing_supplier_cost_is_refused(self):
        self.costs.write_text(json.dumps({'currency': 'USD', 'models': {}}), encoding='utf-8')
        with self.assertRaises(ValueError):
            self.payloads()


if __name__ == '__main__':
    unittest.main()
