import sys,tempfile,unittest
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'scripts'))
from credit_topup import credit_usd,load_ledger
class AtomicCreditBridgeTest(unittest.TestCase):
    def test_retries_use_core_business_key_and_one_local_record(self):
        class API:
            def __init__(self): self.calls=[]
            def ok(self,path,body=None):
                self.calls.append((path,body))
                return {'id':91,'username':'test'} if path.startswith('/api/user/') else {'bonus':15000}
        with tempfile.TemporaryDirectory() as tmp:
            ledger=Path(tmp)/'receipts.json';api=API()
            for i in range(2): credit_usd(ledger,91,'1','bank-id',apply_changes=True,api=api,commercial_v2=True,net_cny='200')
            rows=load_ledger(ledger)['records'];self.assertEqual(len(rows),1);self.assertEqual(rows[0]['billing_version'],2)
            credits=[body for path,body in api.calls if path=='/api/omni/admin/credit']
            self.assertEqual(credits,[{'key':'bank-id','user_id':91,'cents':20000,'quota':500000}]*2)
            self.assertFalse(any(path.startswith('/api/user/manage') for path,_ in api.calls))
    def test_missing_verified_net_refuses_core_credit(self):
        class API:
            def ok(self,path,body=None):
                if not path.startswith('/api/user/'): raise AssertionError('unverified credit attempted')
                return {'id':91}
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(ValueError): credit_usd(Path(tmp)/'ledger.json',91,'1','receipt',apply_changes=True,api=API(),commercial_v2=True)

    def test_refunded_receipt_cannot_be_reported_as_new_credit(self):
        class API:
            def ok(self,path,body=None):
                return {'id':91} if path.startswith('/api/user/') else {'refunded':True,'bonus':0}
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(ValueError): credit_usd(Path(tmp)/'ledger.json',91,'1','refunded-bank-id',apply_changes=True,api=API(),commercial_v2=True,net_cny='200')
