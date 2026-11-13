import json
import sys
import tempfile
import unittest
from pathlib import Path

ROOT=Path(__file__).resolve().parents[1]
sys.path[:0]=[str(ROOT/'scripts'),str(ROOT/'ops/topup_watch')]
import credit_topup
import topup_watch


class Double11BridgeTests(unittest.TestCase):
    def test_verified_time_reaches_atomic_gateway_and_bonus_is_server_derived(self):
        class API:
            def __init__(self): self.calls=[]
            def ok(self,path,body=None):
                self.calls.append((path,body))
                return {'id':91} if path.startswith('/api/user/') else {
                    'bonus':75000,'campaign_bonus':75000,'paid_at':1794369600,
                    'promotion_state':'awarded'}
        with tempfile.TemporaryDirectory() as tmp:
            api=API()
            result=credit_topup.credit_usd(Path(tmp)/'ledger.json',91,'1','verified-payment',
                apply_changes=True,api=api,commercial_v2=True,net_cny='6.73',paid_at=1794369600)
            body=next(body for path,body in api.calls if path=='/api/omni/admin/credit')
            self.assertEqual(body['quota'],500000)
            self.assertEqual(body['paid_at'],1794369600)
            self.assertEqual(body['paid_at_source'],'manual:verified_transaction')
            self.assertEqual(result['issued']['bonus_quota'],75000)
            self.assertNotIn('bonus',body)

    def test_ambiguous_old_receipt_never_credits_principal_again(self):
        class API:
            def ok(self,path,body=None):
                if not path.startswith('/api/user/'): raise AssertionError('ambiguous receipt issued money')
                return {'id':91}
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'ledger.json'
            path.write_text(json.dumps({'records':[{'transaction_id':'old','user_id':91,
                'quota':500000,'state':'prepared','applied':False}]}))
            with self.assertRaises(ValueError):
                credit_topup.credit_usd(path,91,'1','old',apply_changes=True,api=API(),commercial_v2=True,net_cny='6.73')

    def test_manual_payment_time_uses_beijing_time_and_allows_pending_evidence(self):
        self.assertEqual(topup_watch.verified_payment_time('2026-11-11T12:00:00'),1794369600)
        self.assertIsNone(topup_watch.verified_payment_time(''))
        with self.assertRaises(ValueError): topup_watch.verified_payment_time('not-a-payment-time')


if __name__=='__main__': unittest.main()
