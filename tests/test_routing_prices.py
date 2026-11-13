import unittest
from decimal import Decimal
from scripts.build_routing_prices import build

class RoutingPrices(unittest.TestCase):
 def fixture(self):
  pricing=dict(billing_mode='token',input_price=.000001,output_price=.000003,cache_read_price=.0000001,cache_write_price=.00000125,cache_write_1h_price=.000002,intervals=[])
  public={'data':{'groups':[{'id':7,'name':'GPT Plus','rate_multiplier':.18,'models':[{'name':'gpt-5.5','pricing':pricing}]}]}}
  channels=[dict(id=1,status=1,models='gpt-5.5,unknown'),dict(id=3,status=1,models='gpt-5.5,backup-only')]
  return channels,public,pricing
 def test_actual_sale_and_per_fee_currency(self):
  channels,public,_=self.fixture();result=build(channels,public,Decimal('6'))
  row=result['models']['gpt-5.5']
  self.assertEqual(Decimal(row['retail']['input']),Decimal('.250'))
  self.assertEqual(Decimal(row['retail']['output']),Decimal('1.50'))
  self.assertEqual(Decimal(row['retail']['cache_write']),Decimal('.250'))
  self.assertEqual(row['routes'],[1,3]);self.assertIsInstance(row['costs'][3]['input_usd_per_million'],float)
  self.assertIn('unknown',result['waiting']);self.assertIn('backup-only',result['waiting'])
 def test_missing_cache_quote_never_defaults(self):
  channels,public,pricing=self.fixture();pricing['cache_write_price']=None
  result=build(channels,public,Decimal('6'));self.assertNotIn('gpt-5.5',result['models'])
  self.assertIn('gpt-5.5',result['waiting'])
 def test_context_tiers_preserve_exact_markup(self):
  channels,public,pricing=self.fixture()
  pricing['intervals']=[dict(pricing,min_tokens=0,max_tokens=272000),dict(pricing,min_tokens=272001,max_tokens=None,input_price=.000002,output_price=.000006)]
  row=build(channels,public,Decimal('6'))['models']['gpt-5.5']
  self.assertEqual(row['tiers'],[])
  self.assertEqual(Decimal(row['retail']['input']),Decimal('.250'))
  self.assertEqual(row['costs'][3]['tiers'][1]['prices']['input_usd_per_million'],.06)
