"""Loopback-only API. JWT signature AND live gateway introspection are required."""
import base64
import json
import os
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
from store import Store, SyncError

def unbase(value):
    return base64.urlsafe_b64decode(value + '=' * (-len(value)%4))

def verify(token, key):
    import time
    try:
        header,payload,signature = token.split('.')
        h,c = json.loads(unbase(header)),json.loads(unbase(payload))
        if h.get('alg')!='EdDSA' or h.get('typ')!='JWT':
            raise ValueError()
        key.verify(unbase(signature),(header+'.'+payload).encode())
        now = int(time.time())
        # golang-jwt/v5 writes RegisteredClaims.Audience as a singleton array.
        audience = c.get('aud')
        if c.get('iss')!='https://omniapi.biz' or audience not in ('omniapi-user-sync',['omniapi-user-sync']) or type(c.get('exp')) is not int or not now<c['exp']<=now+65 or c.get('iat',now+1)>now+5:
            raise ValueError()
        return c
    except Exception:
        raise SyncError('UNAUTHORIZED',401) from None

class App:
    def __init__(self, store, public_key, introspect):
        self.store,self.public_key,self.introspect = store,public_key,introspect

    def auth(self, token):
        claims = verify(token,self.public_key)
        live = self.introspect(token)
        if not live or live.get('uid')!=claims.get('uid') or live.get('device')!=claims.get('device'):
            raise SyncError('SESSION_REVOKED',401)
        return live

    def route(self, method, path, token, body=None):
        if method=='GET' and path=='/health':
            return {'status':'ok','format':1}
        claims = self.auth(token)
        parsed = urlparse(path)
        parts = parsed.path.strip('/').split('/')
        if parts[:1] != ['v1']:
            raise SyncError('NOT_FOUND',404)
        if parts==['v1','devices']:
            return self.store.register(claims) if method=='POST' else self.store.devices(claims) if method=='GET' else self.bad_method()
        if len(parts)==3 and parts[1]=='devices' and method=='DELETE':
            return self.store.revoke(claims,parts[2])
        if parts==['v1','status'] and method=='GET':
            return self.store.status(claims)
        if parts==['v1','changes'] and method=='GET':
            try:
                after = int(parse_qs(parsed.query).get('after',['0'])[0])
            except ValueError:
                raise SyncError('INVALID_CURSOR') from None
            return self.store.changes(claims,after)
        if parts==['v1','keyring'] and method in ['GET','PUT']:
            return self.store.keyring(claims,body if method=='PUT' else None)
        if len(parts)==5 and parts[1]=='objects' and parts[3]=='versions' and method=='PUT':
            return self.store.put(claims,parts[2],parts[4],body)
        raise SyncError('NOT_FOUND',404)

    def bad_method(self):
        raise SyncError('METHOD_NOT_ALLOWED',405)

def main():
    root = os.environ.get('OMNI_SYNC_ROOT','/var/lib/omni-sync')
    public_key = Ed25519PublicKey.from_public_bytes(base64.b64decode(os.environ['OMNI_SYNC_PUBLIC_KEY'],validate=True))
    internal_secret = os.environ['OMNI_SYNC_INTROSPECTION_SECRET']
    # Fixed HTTPS origin: no user-controlled upstream URLs or forwarded headers.
    def introspect(token):
        request = urllib.request.Request('https://omniapi.biz/api/app/sync/introspect',data=json.dumps({'token':token}).encode(),headers={'Content-Type':'application/json','X-Omni-Sync-Service':internal_secret})
        try:
            with urllib.request.urlopen(request,timeout=8) as response:
                data = json.load(response)
                return data.get('data') if data.get('success') else None
        except (urllib.error.URLError,ValueError):
            raise SyncError('GATEWAY_UNAVAILABLE',503) from None
    app = App(Store(root+'/sync.sqlite3'),public_key,introspect)
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*args):
            pass  # never log URLs, tokens or encrypted/user content
        def dispatch(self):
            status = 200
            try:
                if self.headers.get('Transfer-Encoding'):
                    raise SyncError('TRANSFER_ENCODING_UNSUPPORTED',400)
                size = int(self.headers.get('Content-Length','0'))
                if not 0<=size<=3*1024*1024:
                    raise SyncError('BODY_TOO_LARGE',413)
                body = json.loads(self.rfile.read(size)) if size else None
                if size and not self.headers.get('Content-Type','').startswith('application/json'):
                    raise SyncError('JSON_REQUIRED',415)
                token = self.headers.get('Authorization','')
                result = app.route(self.command,self.path,token.removeprefix('Bearer '),body)
                payload = {'success':True,'data':result}
            except SyncError as error:
                status,payload = error.status,{'success':False,'code':error.code}
            except (ValueError,TypeError,KeyError):
                status,payload = 400,{'success':False,'code':'INVALID_REQUEST'}
            except Exception:
                status,payload = 503,{'success':False,'code':'SYNC_UNAVAILABLE'}
            raw = json.dumps(payload,separators=(',',':')).encode()
            self.send_response(status)
            self.send_header('Content-Type','application/json')
            self.send_header('Content-Length',str(len(raw)))
            self.send_header('Cache-Control','no-store')
            self.send_header('X-Content-Type-Options','nosniff')
            self.end_headers()
            self.wfile.write(raw)
        do_GET=do_POST=do_PUT=do_DELETE=dispatch
    # 0.0.0.0: nginx 容器经 host.docker.internal 回连宿主（与 8787/8790 同款）；
    # 公网仍由 ufw 挡在 docker 网段之外。
    server = ThreadingHTTPServer((os.environ.get('OMNI_SYNC_ADDR','0.0.0.0'),8792),Handler)
    server.daemon_threads=True
    server.serve_forever()

if __name__=='__main__':
    main()
