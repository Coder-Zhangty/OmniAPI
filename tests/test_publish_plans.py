import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
import publish_plans


class PublishPlansTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.costs = root / 'supplier-costs.json'
        self.catalog = root / 'REAL_MODELS.json'
        self.out = root / 'plans.json'
        self.costs.write_text(json.dumps({
            'currency': 'USD',
            'models': {'sample': [1.0, 2.0], 'unverified': [9.0, 9.0]},
        }), encoding='utf-8')
        self.catalog.write_text(json.dumps({'results': [
            {'model': 'sample', 'status': 'PASS'},
            {'model': 'broken', 'status': 'FAIL'},
        ]}), encoding='utf-8')
        publish_plans.SUPPLIER_COSTS = self.costs
        publish_plans.CATALOG_PATH = self.out

    def tearDown(self):
        self.temp.cleanup()

    def config(self, **overrides):
        base = {'markup': 2, 'settlement_currency': 'CNY', 'cny_per_usd': 7,
                'payment_mode': 'manual_verified_receipt', 'models': {},
                'plans': [{'title': '体验包', 'duration_days': 7, 'sale_cny': 70,
                           'quota_reset_period': 'never'}]}
        base.update(overrides)
        path = Path(self.temp.name) / 'commercial.json'
        path.write_text(json.dumps(base), encoding='utf-8')
        return path

    def run_publish(self, config_path, dry_run=False):
        return publish_plans.publish(config_path, self.catalog, dry_run=dry_run)

    def test_unpriced_config_is_blocked_and_writes_nothing(self):
        result = self.run_publish(self.config(cny_per_usd=None))
        self.assertEqual(result['status'], 'BLOCKED')
        self.assertTrue(any('cny_per_usd' in reason for reason in result['reasons']))
        self.assertFalse(self.out.exists())

    def test_missing_supplier_cost_is_blocked_with_model_named(self):
        self.costs.write_text(json.dumps({'currency': 'USD', 'models': {}}), encoding='utf-8')
        result = self.run_publish(self.config())
        self.assertEqual(result['status'], 'BLOCKED')
        self.assertTrue(any('sample' in reason for reason in result['reasons']))
        self.assertFalse(self.out.exists())

    def test_failed_models_are_not_required_to_be_priced(self):
        result = self.run_publish(self.config())
        self.assertEqual(result['status'], 'PUBLISHED')
        payload = json.loads(self.out.read_text(encoding='utf-8'))
        self.assertFalse(payload['accepting_payments'])
        self.assertEqual(len(payload['plans']), 1)
        self.assertEqual(payload['plans'][0]['sale_cny'], '70')
        self.assertFalse(payload['plans'][0]['enabled_in_backend'])

    def test_not_collecting_by_default(self):
        self.run_publish(self.config())
        payload = json.loads(self.out.read_text(encoding='utf-8'))
        self.assertFalse(payload['accepting_payments'])
        self.assertEqual(payload['merchant_id'], '')

    def test_merchant_id_is_optional(self):
        self.run_publish(self.config(accepting_payments=True, contact='客服 A'))
        payload = json.loads(self.out.read_text(encoding='utf-8'))
        self.assertTrue(payload['accepting_payments'])
        self.assertEqual(payload['merchant_id'], '')

    def test_collecting_requires_operator_supplied_details(self):
        self.run_publish(self.config(accepting_payments=True, merchant_id='M-1',
                                     contact='客服 A'))
        payload = json.loads(self.out.read_text(encoding='utf-8'))
        self.assertTrue(payload['accepting_payments'])
        self.assertEqual(payload['merchant_id'], 'M-1')

    def test_accepting_payments_without_contact_raises(self):
        with self.assertRaises(ValueError):
            publish_plans.publish(self.config(accepting_payments=True, merchant_id='M-1'),
                                  self.catalog, dry_run=True)

    def test_dry_run_does_not_write(self):
        result = self.run_publish(self.config(), dry_run=True)
        self.assertEqual(result['status'], 'DRY_RUN')
        self.assertEqual(result['catalog']['plans'][0]['native_total_amount'], 5000000)
        self.assertFalse(self.out.exists())

    def test_operator_model_costs_override_supplier_table(self):
        path = self.config(models={'sample': {'currency': 'USD', 'input_per_million': 0.5,
                                             'output_per_million': 1.0}})
        result = self.run_publish(path, dry_run=True)
        self.assertEqual(result['catalog']['cny_per_usd'], '7')

    def test_placeholder_entry_does_not_clobber_verified_cost(self):
        path = self.config(models={'sample': {'currency': 'USD', 'input_per_million': None,
                                             'output_per_million': None}})
        result = self.run_publish(path, dry_run=True)
        self.assertEqual(result['status'], 'DRY_RUN')
        self.assertEqual(result['catalog']['plans'][0]['native_total_amount'], 5000000)

    def test_unusable_currency_entry_is_ignored(self):
        path = self.config(models={'sample': {'currency': 'JPY', 'input_per_million': 1,
                                             'output_per_million': 2}})
        result = self.run_publish(path, dry_run=True)
        self.assertEqual(result['status'], 'DRY_RUN')


if __name__ == '__main__':
    unittest.main()
