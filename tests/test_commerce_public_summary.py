import unittest
from scripts.commerce_public_summary import summarize

class PublicSummaryTest(unittest.TestCase):
 def initial(self):
  return {'remaining_unexecuted':0,'planned_cases':1,'scope_hash':'scope','passed':0,'results':[{'model':'m','case':'functions','passed':False}]}
 def test_recheck_retains_original_failure_and_never_approves_cost(self):
  r=summarize(self.initial(),{'results':[{'model':'m','case':'functions','passed':True}]},{'results':[]})
  self.assertEqual(r['observed_passes'],1);self.assertFalse(r['results'][0]['initial_passed']);self.assertTrue(r['results'][0]['changed_conditions_in_rechecks']);self.assertFalse(r['all_provider_routes_certified']);self.assertFalse(r['whole_request_cost_certified']);self.assertFalse(r['sales_enabled'])
 def test_no_success_cannot_be_promoted(self):
  r=summarize(self.initial(),{'results':[]},{'results':[{'model':'m','case':'functions','passed':False}]})
  self.assertEqual(r['observed_passes'],0);self.assertEqual(len(r['remaining_failures']),1)
 def test_incomplete_initial_suite_is_rejected(self):
  i=self.initial();i['remaining_unexecuted']=1
  with self.assertRaises(AssertionError):summarize(i,{'results':[]},{'results':[]})
