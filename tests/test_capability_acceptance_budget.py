import tempfile,unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from ops.capability_acceptance_budget_20261105 import AcceptanceBudget,BudgetExceeded

class BudgetTests(unittest.TestCase):
 def setUp(self):
  root=Path(__file__).resolve().parents[1]/'runtime/customer-billing-r10-20261105/budget-tests';root.mkdir(parents=True,exist_ok=True)
  self.tmp=tempfile.TemporaryDirectory(dir=root)
  assert Path(self.tmp.name).resolve().is_relative_to(root.resolve())
  self.addCleanup(self.tmp.cleanup)
  self.path=Path(self.tmp.name)/'round.sqlite';self.b=AcceptanceBudget(self.path)
 def test_restart_and_call_replay_never_duplicate(self):
  self.assertTrue(self.b.reserve('one',10_000_000,'synthetic bound'))
  self.assertFalse(AcceptanceBudget(self.path).reserve('one',10_000_000,'synthetic bound'))
  self.b.settle('one',3_000_000);self.b.settle('one',3_000_000)
  self.assertEqual(self.b.summary()['committed_cny_micros'],3_000_000)
 def test_concurrent_claims_cannot_exceed_cap(self):
  def claim(i):
   try:return self.b.reserve(str(i),10_000_000,'synthetic finite bound')
   except BudgetExceeded:return False
  with ThreadPoolExecutor(max_workers=8) as pool:accepted=list(pool.map(claim,range(40)))
  self.assertEqual(sum(accepted),20);self.assertEqual(self.b.summary()['committed_cny_micros'],200_000_000)
 def test_unknown_actual_keeps_full_hold(self):
  self.b.reserve('unknown',200_000_000,'synthetic upper')
  with self.assertRaises(BudgetExceeded):self.b.reserve('next',1,'synthetic upper')
 def test_unknown_bound_not_treated_as_free(self):
  for value in (0,-1,None):
   with self.assertRaises(ValueError):self.b.reserve('invalid',value,'missing quote')
 def test_breach_truthfully_recorded(self):
  self.b.reserve('bad-bound',100,'synthetic bad bound')
  with self.assertRaises(BudgetExceeded):self.b.settle('bad-bound',200_000_001)
  self.assertEqual(self.b.summary()['bound_breaches'],1)
  with self.assertRaises(BudgetExceeded):self.b.reserve('later',1,'synthetic')

if __name__=='__main__':unittest.main()
