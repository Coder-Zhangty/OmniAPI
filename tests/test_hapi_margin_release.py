from pathlib import Path
from decimal import Decimal as D
import json,unittest,sys
ROOT=Path(__file__).resolve().parents[1];sys.path.insert(0,str(ROOT/'ops'))
from supplier_route_policy import validate
class MarginRelease(unittest.TestCase):
 def setUp(self):
  out=ROOT/'runtime/hapi-routing-fix';self.c=json.loads((out/'policy-candidate.json').read_text());self.values={k:json.loads(v) for k,v in self.c['values'].items()};self.audit=json.loads((out/'margin-audit.json').read_text())
 def test_new_routes_and_aliases_preserved(self):
  p=self.values['OmniAPIRouting'];validate(p)
  for name,cid in [('kimi-k3',8),('deepseek-v4-flash',9),('deepseek-v4-pro',9),('deepseek-v4.1-flash',9)]:self.assertEqual(p['models'][name],[cid,7]);self.assertTrue(self.values['OmniAPIModelRegistry']['models'][name]['aliases'])
 def test_cny_after_discount_is_converted_once(self):
  b=next(b for b in self.values['OmniAPIModelRegistry']['models']['kimi-k3']['bindings'] if b['channel_id']==8)
  self.assertAlmostEqual(b['procurement']['input_usd_per_million'],float(D(4)/D('6.7298')),places=10)
 def test_all_priced_dimensions_cover_the_most_expensive_supplier(self):
  for row in self.audit['rows']:
   if row['maximum_cost_usd_per_million'] is not None:self.assertGreaterEqual(D(row['gross_margin']),D('.60'),str(row))
 def test_real_billing_expression_matches_displayed_price(self):
  expr=self.values['billing_setting.billing_expr']
  for name,p in self.values['OmniAPIPricing']['models'].items():
   self.assertEqual(expr[name],p['billing_expr'])
   for dim,var in {'input':'p','output':'c','cache_read':'cr','cache_write':'cc','cache_write_1h':'cc1h'}.items():self.assertIn(var+' * '+p['retail'][dim],expr[name])
 def test_unquoted_cache_is_not_registered_as_supported(self):
  p=self.values['OmniAPIRouting']
  for name,row in self.values['OmniAPIModelRegistry']['models'].items():
   for b in row['bindings']:
    if b['status']!='verified':continue
    cost=p['costs'].get(name,{}).get(str(b['channel_id']),{})
    for dim in ['cache_write','cache_write_1h']:
     if dim in b.get('supports',[]):self.assertIn(dim+'_usd_per_million',cost)
if __name__=='__main__':unittest.main()
