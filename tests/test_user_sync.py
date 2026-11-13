import base64
import sys
import tempfile
import time
import unittest
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'ops/sync'))
from store import Store,SyncError,GIB

def cipher(text=b'x'):
    return {'v':1,'nonce':base64.b64encode(b'n'*12).decode(),'ciphertext':base64.b64encode(text+b't'*16).decode()}

class SyncTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory()
        self.store=Store(Path(self.tmp.name)/'sync.db')
        self.a={'uid':1,'device':'device_A_1234567890','quota':GIB,'end':int(time.time())+3600}
        self.b={**self.a,'uid':2,'device':'device_B_1234567890'}
        for claims in [self.a,self.b]:self.store.register(claims)
    def tearDown(self):self.tmp.cleanup()
    def put(self, revision='revision_12345678',parent=None):
        return self.store.put(self.a,'object_1234567890',revision,{'parent':parent,'deleted':False,'envelope':cipher()})
    def test_isolation_and_idempotency(self):
        first=self.put();self.assertEqual(self.put()['cursor'],first['cursor'])
        self.assertEqual(self.store.changes(self.b)['changes'],[])
        self.assertEqual(len(self.store.changes(self.a)['changes']),1)
        with self.assertRaises(SyncError):
            self.store.put(self.a,'object_1234567890','revision_12345678',{'parent':None,'deleted':False,'envelope':cipher(b'changed')})
    def test_conflict_preserves_both(self):
        first=self.put()
        one=self.put('revision_A_1234567',first['revision'])
        two=self.put('revision_B_1234567',first['revision'])
        self.assertFalse(one['conflict']);self.assertTrue(two['conflict'])
        self.assertEqual(len(self.store.changes(self.a)['changes']),3)
    def test_expired_readonly_revocation_and_quota(self):
        self.put()
        expired={**self.a,'end':int(time.time())-1}
        self.store.register(expired)
        with self.assertRaises(SyncError):self.store.register({**expired,'device':'new_device_12345678'})
        self.assertTrue(self.store.status(expired)['read_only'])
        with self.assertRaises(SyncError):self.store.put(expired,'object_1234567890','revision_new_123456',{'parent':None,'deleted':False,'envelope':cipher()})
        with self.assertRaises(SyncError):self.store.put({**self.a,'quota':1},'object_new_1234567','revision_new_123456',{'parent':None,'deleted':False,'envelope':cipher()})
        self.store.revoke(self.a,self.a['device'])
        with self.assertRaises(SyncError):self.store.changes(self.a)
        with self.assertRaises(SyncError):self.store.register(self.a)
    def test_no_purge_when_gateway_unavailable(self):
        self.put()
        def fail(uid):raise ConnectionError()
        with self.assertRaises(ConnectionError):self.store.cleanup(fail)
        self.assertEqual(len(self.store.changes(self.a)['changes']),1)
    def test_plaintext_fields_rejected(self):
        body={'parent':None,'deleted':False,'envelope':{**cipher(),'title':'secret'}}
        with self.assertRaises(SyncError):self.store.put(self.a,'object_1234567890','revision_12345678',body)
    def test_recycle_cleanup_preserves_a_concurrent_live_branch(self):
        first=self.put()
        self.put('revision_live_123456',first['revision'])
        deletion=self.store.put(self.a,'object_1234567890','revision_delete_1234',{'parent':first['revision'],'deleted':True,'envelope':cipher()})
        with self.store.transaction() as db:
            db.execute('UPDATE revisions SET created=? WHERE cursor=?',(int(time.time())-31*86400,deletion['cursor']))
        self.store.cleanup(lambda uid:self.a['end'])
        self.assertEqual(len(self.store.changes(self.a)['changes']),3)
    def test_recycle_cleanup_preserves_deletion_for_offline_devices(self):
        first=self.put()
        deletion=self.store.put(self.a,'object_1234567890','revision_delete_1234',{'parent':first['revision'],'deleted':True,'envelope':cipher()})
        with self.store.transaction() as db:
            db.execute('UPDATE revisions SET created=? WHERE cursor=?',(int(time.time())-31*86400,deletion['cursor']))
        self.store.cleanup(lambda uid:self.a['end'])
        changes=self.store.changes(self.a,first['cursor'])['changes']
        self.assertEqual(len(changes),1)
        self.assertTrue(changes[0]['purged'])
        self.assertTrue(changes[0]['deleted'])
    def test_expiry_cleanup_requires_current_entitlement(self):
        self.put()
        self.store.cleanup(lambda uid:int(time.time())+3600)
        self.assertEqual(len(self.store.changes(self.a)['changes']),1)
        self.store.cleanup(lambda uid:int(time.time())-91*86400)
        with self.store.connection() as db:self.assertEqual(db.execute('SELECT COUNT(*) FROM revisions').fetchone()[0],0)

if __name__=='__main__':unittest.main()
