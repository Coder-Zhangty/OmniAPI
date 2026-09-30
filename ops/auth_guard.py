#!/usr/bin/env python3
"""Email-only gate for New API password login and registration."""
import http.client
import json
import os
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

UPSTREAM = urlsplit(os.environ.get('UPSTREAM_AUTH_ORIGIN', 'http://new-api:3000'))
EMAIL = re.compile(r'^[^\s@]+@[^\s@]+\.[^\s@]+$')
MAX_BODY = 64 * 1024
HOP = {'connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization',
       'te', 'trailers', 'transfer-encoding', 'upgrade', 'content-length'}


def accepted(body):
    try:
        data = json.loads(body)
    except (UnicodeDecodeError, json.JSONDecodeError):
        return False
    identity = str(data.get('username', '')).strip()
    return bool(EMAIL.fullmatch(identity))


class Handler(BaseHTTPRequestHandler):
    server_version = 'OmniAuthGuard/1'

    def log_message(self, *_):
        return

    def reply(self, status, payload):
        body = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json; charset=utf-8')
        self.send_header('Cache-Control', 'no-store')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == '/healthz':
            return self.reply(200, {'ok': True})
        return self.reply(404, {'success': False})

    def do_POST(self):
        if self.path.split('?', 1)[0] not in ('/api/user/login', '/api/user/register'):
            return self.reply(404, {'success': False})
        try:
            length = int(self.headers.get('Content-Length', '0'))
        except ValueError:
            return self.reply(400, {'success': False, 'message': '请求格式错误'})
        if length <= 0 or length > MAX_BODY:
            return self.reply(400, {'success': False, 'message': '请求格式错误'})
        body = self.rfile.read(length)
        if not accepted(body):
            return self.reply(200, {'success': False, 'message': '仅支持邮箱和密码登录'})
        conn = http.client.HTTPConnection(UPSTREAM.hostname, UPSTREAM.port or 80, timeout=25)
        headers = {'Content-Type': self.headers.get('Content-Type', 'application/json'),
                   'Content-Length': str(len(body)),
                   'X-Real-IP': self.headers.get('X-Real-IP', self.client_address[0]),
                   'X-Forwarded-For': self.headers.get('X-Forwarded-For', self.client_address[0]),
                   'X-Forwarded-Proto': self.headers.get('X-Forwarded-Proto', 'https')}
        try:
            conn.request('POST', self.path, body=body, headers=headers)
            response = conn.getresponse()
            result = response.read()
            self.send_response(response.status)
            for key, value in response.getheaders():
                if key.lower() not in HOP:
                    self.send_header(key, value)
            self.send_header('Content-Length', str(len(result)))
            self.send_header('Cache-Control', 'no-store')
            self.end_headers()
            self.wfile.write(result)
        except OSError:
            self.reply(502, {'success': False, 'message': '认证服务暂时不可用'})
        finally:
            conn.close()


if __name__ == '__main__':
    ThreadingHTTPServer(('0.0.0.0', 8788), Handler).serve_forever()
