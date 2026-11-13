import unittest
from decimal import Decimal as D
from scripts.zero_reprice import quote,reprice
class ZeroPrices(unittest.TestCase):
 def test_usd_multiplier(self):
  self.assertEqual(D(quote('claude-opus-4-8')['retail']['input']),D('1.5'))
  self.assertEqual(D(quote('claude-opus-5-5')['retail']['output']),D('6'))
  self.assertEqual(D(quote('deepseek-v4.1-flash')['retail']['input']),D('.06'))
  self.assertEqual(D(quote('gpt-5.4-mini')['retail']['output']),D('.225'))
 def test_cache_rules(self):
  row=quote('claude-sonnet-4-6')['retail']
  self.assertEqual(D(row['cache_read']),D('.09'));self.assertEqual(D(row['cache_write']),D('1.125'));self.assertEqual(D(row['cache_write_1h']),D('1.8'))
 def test_private_reference(self):
  row=quote('claude-opus-4-7');self.assertTrue(row['reference_is_estimate']);self.assertEqual(row['reference_model'],'claude-opus-4-8')
 def test_unavailable_remains_unavailable(self):
  row=reprice({'models':{},'waiting':{'sora-2':'paused'}});self.assertFalse(row['models']);self.assertIn('sora-2',row['pricing_quotes'])
