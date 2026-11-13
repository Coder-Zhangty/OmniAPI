from pathlib import Path
from decimal import Decimal as D
import json,runpy,unittest
ROOT=Path(__file__).resolve().parents[1];prepare=runpy.run_path(str(ROOT/'ops/primary-margin-pricing.py'))['prepare']
class PrimaryPricing(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  cls.state=json.loads((ROOT/'runtime/hapi-routing-fix/primary-margin-before.json').read_text());cls.c=prepare(cls.state);cls.p=json.loads(cls.c['values']['OmniAPIPricing'])
 def test_currency_once_and_primary_not_expensive_backup(self):
  fx=D(str(self.state['fx']['rate']))
  for name,i,o in [('kimi-k3',D(4),D(20)),('deepseek-v4.1-flash',D('.6'),D('2.4'))]:
   for d,v in [('input',i),('output',o)]:
    price=D(self.p['models'][name]['retail'][d]);self.assertLess(abs(price-v/fx/D('.4')),D('.00000002'))
 def test_verified_dimensions_reach_sixty_percent(self):
  for a in self.c['audit']:
   if a['margin'] is not None:self.assertGreaterEqual(D(a['margin']),D('.6'))
 def test_unverified_cache_never_becomes_zero_cost(self):
  for a in self.c['audit']:
   if a['cost'] is None:self.assertEqual(D(a['new_price']),D(str(a['old_price'])))
 def test_display_and_actual_billing_match(self):
  expr=json.loads(self.c['values']['billing_setting.billing_expr'])
  for n,row in self.p['models'].items():self.assertEqual(expr[n],row['billing_expr'])
 def test_routes_and_procurement_metadata_preserved(self):
  old=json.loads(self.state['options']['OmniAPIPricing'])
  self.assertNotIn('OmniAPIRouting',self.c['values']);self.assertNotIn('OmniAPIModelRegistry',self.c['values'])
  for n,row in self.p['models'].items():self.assertEqual(row['routes'],old['models'][n]['routes']);self.assertEqual(row['costs'],old['models'][n]['costs'])
 def test_lower_prices_are_actually_allowed(self):
  self.assertTrue(any(D(a['new_price'])<D(str(a['old_price'])) for a in self.c['audit']))
if __name__=='__main__':unittest.main()
