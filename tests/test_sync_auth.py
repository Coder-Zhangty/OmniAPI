"""Exercise actual Ed25519 verification and live revocation, using synthetic data."""
import base64
import json
import sys
import tempfile
import time
import unittest
from pathlib import Path
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'ops/sync'))
from server import App,verify
from store import Store,SyncError,GIB

def encode(value):
    return base64.urlsafe_b64encode(value).decode().rstrip('=')

class SyncAuthTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory()
        self.key=Ed25519PrivateKey.generate()
        self.live={'uid':1,'device':'synthetic_device_12345','quota':GIB,'end':int(time.time())+3600}
        self.store=Store(Path(self.tmp.name)/'sync.sqlite3')
        self.calls=0
        def introspect(token):
            self.calls+=1
            return self.live
        self.app=App(self.store,self.key.public_key(),introspect)
    def tearDown(self):self.tmp.cleanup()
    def token(self,**changes):
        now=int(time.time())
        claims={'iss':'https://omniapi.biz','aud':'omniapi-user-sync','iat':now,'exp':now+60,'uid':1,'device':'synthetic_device_12345'}
        claims.update(changes)
        text=encode(b'{"alg":"EdDSA","typ":"JWT"}')+'.'+encode(json.dumps(claims).encode())
        return text+'.'+encode(self.key.sign(text.encode()))
    def test_valid_signature_still_requires_live_revocation_check(self):
        token=self.token()
        self.app.route('POST','/v1/devices',token,{})
        self.assertEqual(self.app.route('GET','/v1/status',token)['quota_bytes'],GIB)
        self.live=None
        with self.assertRaises(SyncError) as caught:self.app.route('GET','/v1/status',token)
        self.assertEqual(caught.exception.code,'SESSION_REVOKED')
        self.assertEqual(self.calls,3)
    def test_live_identity_must_match_signed_identity(self):
        for changed in [{'uid':2},{'device':'other_device_1234567'}]:
            self.live={**self.live,**changed}
            with self.assertRaises(SyncError):self.app.route('POST','/v1/devices',self.token(),{})
            self.live={'uid':1,'device':'synthetic_device_12345','quota':GIB,'end':int(time.time())+3600}
    def test_expiry_audience_issuer_and_future_issue_time_rejected(self):
        now=int(time.time())
        for changed in [{'exp':now-1},{'exp':now+3600},{'aud':'archive-admin'},{'iss':'https://attacker.invalid'},{'iat':now+1000}]:
            with self.assertRaises(SyncError):verify(self.token(**changed),self.key.public_key())
        self.assertEqual(self.calls,0)
    def test_signature_and_algorithm_cannot_be_substituted(self):
        token=self.token()
        parts=token.split('.')
        for invalid in ['not-a-token',encode(b'{"alg":"none","typ":"JWT"}')+'.'+parts[1]+'.'+parts[2],'.'.join(parts[:2])+'.'+encode(b'0'*64)]:
            with self.assertRaises(SyncError):verify(invalid,self.key.public_key())
    def test_health_does_not_grant_data_access(self):
        self.assertEqual(self.app.route('GET','/health','')['status'],'ok')
        with self.assertRaises(SyncError):self.app.route('GET','/v1/status','')
    def test_gateway_jwt_audience_array_is_supported(self):
        # golang-jwt/v5 serializes jwt.ClaimStrings as an array by default.
        token=self.token(aud=['omniapi-user-sync'])
        self.app.route('POST','/v1/devices',token,{})
        self.assertEqual(self.app.route('GET','/v1/status',token)['quota_bytes'],GIB)
        for invalid in [[],['archive-admin'],['omniapi-user-sync','archive-admin']]:
            with self.assertRaises(SyncError):verify(self.token(aud=invalid),self.key.public_key())

if __name__=='__main__':unittest.main()
