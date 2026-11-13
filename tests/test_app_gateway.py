"""omniapi-app-gateway 单元测试：完整登录流、refresh 轮换、同步令牌、吊销。

运行：python -m unittest tests.test_app_gateway -v
"""
import base64
import hashlib
import json
import sys
import tempfile
import time
import unittest
import io
from unittest.mock import patch
from urllib.error import HTTPError
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'ops' / 'app_gateway'))

from app_gateway import Gateway, Store, b64u  # noqa: E402


def sha256_b64u(text: str) -> str:
    return b64u(hashlib.sha256(text.encode()).digest())


class GatewayFlowTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = Store(Path(self.tmp.name) / 'test.sqlite3')
        self.gateway = Gateway(self.store, {
            'token_secret_b64u': b64u(b't' * 32),
            'sync_signing_key_b64u': b64u(b's' * 32),
            'sync_introspection_secret': 'test-secret',
            'sync_entitlement': {'enabled': True, 'end_days': 30, 'quota_bytes': 4096},
            'sync_url': 'https://omniapi.biz/api/sync',
        })

    def tearDown(self):
        try:
            self.tmp.cleanup()
        except PermissionError:
            pass  # Windows 下 SQLite WAL 句柄释放有延迟，留给系统临时目录清理

    def headers(self, token=None, origin=None):
        h = {}
        if token:
            h['Authorization'] = f'Bearer {token}'
        if origin:
            h['X-Omni-App-Origin'] = origin
        return h

    def login_flow(self):
        device = 'device-0123456789ab'
        verifier = 'v' * 43
        flow = self.gateway.route('POST', '/api/app/login/start', {}, {
            'challenge': sha256_b64u(verifier), 'device': device, 'platform': 'android'}, '10.0.0.1')
        self.assertIn('request', flow)
        self.assertTrue(flow['authorize_url'].startswith('https://omniapi.biz/brand/app-login.html?request='))
        return flow, verifier, device

    def test_webview_one_time_pkce_transfer_and_parent_revocation(self):
        flow, verifier, _ = self.login_flow()
        self.store.approve(flow['request'], 42, {'id': 42, '_web_parent': {'sid': 'dashboard-parent', 'uv': 3, 'sv': 2}})
        native = self.gateway.route('POST', '/api/app/login/exchange', {}, {'request': flow['request'], 'verifier': verifier}, '127.0.0.1')
        web = self.gateway.route('POST', '/api/app/web-session/start', self.headers(native['auth']['access_token']), {'challenge': sha256_b64u(verifier)}, '127.0.0.1')
        headers = {'X-Omni-Web-Service': 'w' * 32}
        self.gateway.config['web_bridge_secret'] = 'w' * 32
        body = {'request': web['request'], 'verifier': verifier}
        with self.assertRaises(Exception):
            self.gateway.route('POST', '/api/app/web-session/consume', {}, body, '127.0.0.1')
        with self.assertRaises(Exception):
            self.gateway.route('POST', '/api/app/web-session/consume', headers, {**body, 'verifier': 'x' * 43}, '127.0.0.1')
        transfer = self.gateway.route('POST', '/api/app/web-session/consume', headers, body, '127.0.0.1')
        self.assertNotIn('_web_parent', self.gateway.snapshot_for(42))
        self.assertEqual(transfer['uid'], 42)
        self.assertEqual(transfer['parent_sid'], 'dashboard-parent')
        with self.assertRaises(Exception):
            self.gateway.route('POST', '/api/app/web-session/consume', headers, body, '127.0.0.1')
        parent = {'uid': 42, 'sid': transfer['native_sid']}
        self.gateway.route('POST', '/api/app/web-session/parent', headers, parent, '127.0.0.1')
        self.store.revoke(transfer['native_sid'])
        with self.assertRaises(Exception):
            self.gateway.route('POST', '/api/app/web-session/parent', headers, parent, '127.0.0.1')

    def test_web_transfer_expires_and_cannot_cross_users(self):
        flow, verifier, _ = self.login_flow()
        self.store.approve(flow['request'], 42, {'id':42, '_web_parent':{'sid':'parent','uv':1,'sv':1}})
        native = self.gateway.route('POST','/api/app/login/exchange',{}, {'request':flow['request'],'verifier':verifier},'127.0.0.1')
        web = self.gateway.route('POST','/api/app/web-session/start',self.headers(native['auth']['access_token']),{'challenge':sha256_b64u(verifier)},'127.0.0.1')
        self.gateway.config['web_bridge_secret']='w'*32
        headers={'X-Omni-Web-Service':'w'*32}
        with self.store._connect() as db:db.execute('UPDATE web_transfer SET expires_at=0 WHERE request=?',(web['request'],))
        with self.assertRaises(Exception):self.gateway.route('POST','/api/app/web-session/consume',headers,{'request':web['request'],'verifier':verifier},'127.0.0.1')
        with self.assertRaises(Exception):self.gateway.route('POST','/api/app/web-session/parent',headers,{'uid':43,'sid':native['auth']['session']['sid']},'127.0.0.1')

    def test_verified_browser_approval_preserves_only_session_reference(self):
        flow, verifier, _ = self.login_flow()
        token='header.'+b64u(json.dumps({'iss':'new-api','aud':['new-api-dashboard'],'token_use':'access','sub':'42','sid':'parent-sid','uv':3,'sv':2}).encode())+'.signature'
        with patch.object(self.gateway,'new_api_self',return_value={'id':42,'username':'test'}):
            self.gateway.route('POST','/api/app/login/approve',self.headers(token),{'request':flow['request']},'127.0.0.1')
        native=self.gateway.route('POST','/api/app/login/exchange',{}, {'request':flow['request'],'verifier':verifier},'127.0.0.1')
        with self.store._connect() as db:
            row=db.execute('SELECT snapshot FROM device_session WHERE sid=?',(native['auth']['session']['sid'],)).fetchone()
        self.assertEqual(json.loads(row['snapshot'])['_web_parent'],{'sid':'parent-sid','uv':3,'sv':2})
        self.assertNotIn(token,row['snapshot'])
        self.assertNotIn('_web_parent',self.gateway.snapshot_for(42))

    def test_start_validates_platform_and_device(self):
        with self.assertRaises(Exception) as ctx:
            self.gateway.route('POST', '/api/app/login/start', {}, {
                'challenge': 'c' * 43, 'device': 'd' * 43, 'platform': 'ios'}, '10.0.0.1')
        self.assertEqual(ctx.exception.code, 'INVALID_PLATFORM')

    def test_exchange_budget_covers_five_minutes_of_client_polling(self):
        # Both clients poll every two seconds for up to five minutes.
        for _ in range(150):
            self.gateway.rate('exchange', '192.0.2.10')
        for _ in range(30):
            self.gateway.rate('exchange', '192.0.2.10')
        with self.assertRaises(Exception) as ctx:
            self.gateway.rate('exchange', '192.0.2.10')
        self.assertEqual(ctx.exception.code, 'RATE_LIMITED')

    def test_exchange_pending_then_approve_then_consume(self):
        flow, verifier, _ = self.login_flow()
        with self.assertRaises(Exception) as ctx:
            self.gateway.route('POST', '/api/app/login/exchange', {}, {
                'request': flow['request'], 'verifier': verifier}, '10.0.0.1')
        self.assertEqual(ctx.exception.code, 'AUTHORIZATION_PENDING')

        self.gateway.store.approve(flow['request'], 42, {'id': 42, 'username': 'demo'})
        session = self.gateway.route('POST', '/api/app/login/exchange', {}, {
            'request': flow['request'], 'verifier': verifier}, '10.0.0.1')
        self.assertEqual(session['user_id'], 42)
        self.assertEqual(session['device'], 'device-0123456789ab')
        self.assertTrue(session['refresh_token'])
        self.assertTrue(session['auth']['access_token'].startswith('omniapp1.'))
        self.assertGreater(session['auth']['access_expires_at'], time.time())
        self.assertTrue(session['auth']['session']['sid'])

        with self.assertRaises(Exception) as ctx2:
            self.gateway.route('POST', '/api/app/login/exchange', {}, {
                'request': flow['request'], 'verifier': verifier}, '10.0.0.1')
        self.assertEqual(ctx2.exception.code, 'REQUEST_CONSUMED')
        return session

    def test_pkce_rejects_wrong_verifier(self):
        flow, _, _ = self.login_flow()
        self.gateway.store.approve(flow['request'], 42, {'id': 42})
        with self.assertRaises(Exception) as ctx:
            self.gateway.route('POST', '/api/app/login/exchange', {}, {
                'request': flow['request'], 'verifier': 'x' * 43}, '10.0.0.1')
        self.assertEqual(ctx.exception.code, 'PKCE_FAILED')

    def test_client_logout_revokes_its_session_without_a_body_sid(self):
        session = self.test_exchange_pending_then_approve_then_consume()
        other = self.test_exchange_pending_then_approve_then_consume()
        self.gateway.route('POST', '/api/app/login/logout',
                           self.headers(session['auth']['access_token']), {}, '10.0.0.1')
        with self.assertRaises(Exception):
            self.gateway.route('POST', '/api/app/login/refresh', {}, {
                'refresh_token': session['refresh_token'],
                'sid': session['auth']['session']['sid']}, '10.0.0.1')
        with self.assertRaises(Exception):
            self.gateway.route('GET', '/api/user/self',
                               self.headers(session['auth']['access_token']), {}, '10.0.0.1')
        me = self.gateway.route('GET', '/api/user/self',
                                self.headers(other['auth']['access_token']), {}, '10.0.0.1')
        self.assertEqual(me['id'], 42)

    def test_refresh_rotates_and_old_token_dies(self):
        session = self.test_exchange_pending_then_approve_then_consume()
        refreshed = self.gateway.route('POST', '/api/app/login/refresh', {}, {
            'refresh_token': session['refresh_token'],
            'sid': session['auth']['session']['sid']}, '10.0.0.1')
        self.assertEqual(refreshed['user_id'], 42)
        self.assertNotEqual(refreshed['refresh_token'], session['refresh_token'])
        self.assertTrue(refreshed['auth']['access_token'].startswith('omniapp1.'))
        with self.assertRaises(Exception) as ctx:
            self.gateway.route('POST', '/api/app/login/refresh', {}, {
                'refresh_token': session['refresh_token'],
                'sid': session['auth']['session']['sid']}, '10.0.0.1')
        self.assertEqual(ctx.exception.code, 'SESSION_REVOKED')

    def test_access_token_gates_self_and_logout_revokes(self):
        session = self.test_exchange_pending_then_approve_then_consume()
        token = session['auth']['access_token']
        me = self.gateway.route('GET', '/api/user/self', self.headers(token), {}, '10.0.0.1')
        self.assertEqual(me['id'], 42)
        with self.assertRaises(Exception) as ctx:
            self.gateway.route('GET', '/api/user/self', self.headers('bogus'), {}, '10.0.0.1')
        self.assertEqual(ctx.exception.code, 'UNAUTHORIZED')
        self.gateway.route('POST', '/api/app/login/logout', self.headers(token), {
            'sid': session['auth']['session']['sid']}, '10.0.0.1')
        with self.assertRaises(Exception) as ctx2:
            self.gateway.route('GET', '/api/user/self', self.headers(token), {}, '10.0.0.1')
        self.assertEqual(ctx2.exception.code, 'SESSION_REVOKED')

    def test_sync_token_contract(self):
        session = self.test_exchange_pending_then_approve_then_consume()
        token = session['auth']['access_token']
        grant = self.gateway.route('POST', '/api/app/sync/token', self.headers(token), {
            'device': session['device']}, '10.0.0.1')
        self.assertEqual(grant['url'], 'https://omniapi.biz/api/sync')
        head, payload, sig = grant['token'].split('.')
        claims = json.loads(base64.urlsafe_b64decode(payload + '=' * (-len(payload) % 4)))
        self.assertEqual(claims['iss'], 'https://omniapi.biz')
        self.assertEqual(claims['aud'], ['omniapi-user-sync'])
        self.assertEqual(claims['uid'], 42)
        self.assertLessEqual(claims['exp'] - time.time(), 65)

        with self.assertRaises(Exception) as ctx:
            self.gateway.route('POST', '/api/app/sync/introspect',
                               {'X-Omni-Sync-Service': 'wrong'}, {'token': grant['token']}, '10.0.0.1')
        self.assertEqual(ctx.exception.code, 'FORBIDDEN')
        live = self.gateway.route('POST', '/api/app/sync/introspect',
                                  {'X-Omni-Sync-Service': 'test-secret'},
                                  {'token': grant['token']}, '10.0.0.1')
        self.assertEqual(live['uid'], 42)

    def test_introspect_rejects_revoked_device(self):
        session = self.test_exchange_pending_then_approve_then_consume()
        token = session['auth']['access_token']
        grant = self.gateway.route('POST', '/api/app/sync/token', self.headers(token), {
            'device': session['device']}, '10.0.0.1')
        self.gateway.route('POST', '/api/app/login/logout', self.headers(token), {
            'sid': session['auth']['session']['sid']}, '10.0.0.1')
        with self.assertRaises(Exception) as ctx:
            self.gateway.route('POST', '/api/app/sync/introspect',
                               {'X-Omni-Sync-Service': 'test-secret'},
                               {'token': grant['token']}, '10.0.0.1')
        self.assertEqual(ctx.exception.code, 'SESSION_REVOKED')

    def test_approve_accepts_new_api_bearer(self):
        """浏览器授权页（OmniCommerce）带 new-api dashboard JWT 走回环验证。"""
        flow, verifier, _ = self.login_flow()
        self.gateway.new_api_self = lambda token: {'id': 7, 'username': 'web-user'}
        result = self.gateway.route('POST', '/api/app/login/approve',
                                    {'Authorization': 'Bearer newapi-jwt'}, {
                                        'request': flow['request']}, '10.0.0.1')
        self.assertTrue(result['approved'])
        session = self.gateway.route('POST', '/api/app/login/exchange', {}, {
            'request': flow['request'], 'verifier': verifier}, '10.0.0.1')
        self.assertEqual(session['user_id'], 7)
        self.assertEqual(session['device'], 'device-0123456789ab')

    def test_web_approval_identity_uses_https_without_http_redirect(self):
        flow, verifier, _ = self.login_flow()
        def upstream(_opener, request, *args, **kwargs):
            if request.full_url.startswith('http:'):
                raise HTTPError(request.full_url, 308, 'Permanent Redirect', {}, None)
            self.assertEqual(request.full_url, 'https://omniapi.biz/api/user/self')
            self.assertEqual(request.get_header('Authorization'), 'Bearer synthetic-web-token')
            return io.BytesIO(json.dumps({'success': True, 'data': {'id': 7, 'username': 'web-user'}}).encode())
        with patch('app_gateway.urlrequest.OpenerDirector.open', autospec=True, side_effect=upstream):
            approved = self.gateway.route('POST', '/api/app/login/approve',
                self.headers('synthetic-web-token'), {'request': flow['request']}, '192.0.2.1')
        self.assertTrue(approved['approved'])
        session = self.gateway.route('POST', '/api/app/login/exchange', {},
            {'request': flow['request'], 'verifier': verifier}, '192.0.2.1')
        self.assertEqual(session['user_id'], 7)

    def test_invalid_web_identity_is_unauthorized_not_service_unavailable(self):
        def upstream(_opener, request, *args, **kwargs):
            raise HTTPError(request.full_url, 401, 'Unauthorized', {}, None)
        with patch('app_gateway.urlrequest.OpenerDirector.open', autospec=True, side_effect=upstream):
            with self.assertRaises(Exception) as ctx:
                self.gateway.new_api_self('synthetic-invalid-token')
        self.assertEqual(ctx.exception.code, 'UPSTREAM_TOKEN_INVALID')

    def test_approve_rejects_bad_new_api_token(self):
        flow, _, _ = self.login_flow()
        def boom(token):
            from app_gateway import GatewayError
            raise GatewayError('UPSTREAM_TOKEN_INVALID', 401)
        self.gateway.new_api_self = boom
        with self.assertRaises(Exception) as ctx:
            self.gateway.route('POST', '/api/app/login/approve',
                               {'Authorization': 'Bearer dead'}, {
                                   'request': flow['request']}, '10.0.0.1')
        self.assertEqual(ctx.exception.code, 'UPSTREAM_TOKEN_INVALID')


if __name__ == '__main__':
    unittest.main()
