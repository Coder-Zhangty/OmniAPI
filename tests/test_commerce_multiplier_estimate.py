import unittest
from scripts.commerce_v3_quote import quote

class MultiplierEstimateTest(unittest.TestCase):
 def fixture(self):
  from tests.test_commerce_v3_quote import CommerceQuoteTest
  s=CommerceQuoteTest().fixture();s['cost_basis']='platform_multiplier_estimate';s['assessed_at']=s.pop('verified_at');s['estimation_authorization']={'approved':True,'mode':'platform_multiplier_estimate','source':'synthetic-operator','operator_instruction':'estimate using current multipliers'}
  def estimated(v):
   if isinstance(v,dict):
    if 'verified' in v:v.update(verified=False,estimated=True,basis='synthetic explicit estimate',source='synthetic://estimate')
    for child in v.values():estimated(child)
   elif isinstance(v,list):
    for child in v:estimated(child)
  estimated(s);r=s['models']['synthetic-model']['routes'][0];r.update(basic_transport_passed=True,mapping_verified=True,accepted=False);return s
 def test_estimation_is_explicit_and_does_not_claim_verified_procurement(self):
  s=self.fixture();q=quote(s)
  self.assertEqual(q['profile']['worst_cost_ppm'],400000);self.assertEqual(q['cost_basis'],'platform_multiplier_estimate');self.assertFalse(q['procurement_verified']);self.assertFalse(q['sales_enabled']);self.assertFalse(q['profit_guarantee']);self.assertIn('"verified": false',q['profile']['evidence'])
 def test_estimate_without_operator_authorization_fails(self):
  s=self.fixture();s['estimation_authorization']['approved']=False
  with self.assertRaises(ValueError):quote(s)
 def test_authorized_estimate_does_not_allow_missing_numeric_prices(self):
  s=self.fixture();s['models']['synthetic-model']['routes'][0]['context_tiers'][0]['dimensions']['input']['procurement']=None
  with self.assertRaises((ValueError,TypeError,ArithmeticError)):quote(s)
 def test_assumption_flag_cannot_enter_verified_mode(self):
  s=self.fixture();s['cost_basis']='verified_procurement';s['verified_at']=s['assessed_at']
  with self.assertRaises(ValueError):quote(s)

if __name__=='__main__':unittest.main()
