"""HTTP-level tests for the support service, driven through its real routes.

Runs an actual instance on 127.0.0.1 with throwaway files, so the things that
matter about this service — who the caller is, which writes need CSRF, what the
rate gates do, whether the stream really pushes — are exercised rather than
inferred. The gateway lookup is stubbed at its own seam (`user_identity.API`);
nothing here touches the network.
"""
import json
import re
import socket
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer
from pathlib import Path

BASE = Path(__file__).resolve().parents[1]
for extra in ('scripts', 'ops', 'ops/support'):
    sys.path.insert(0, str(BASE / extra))

import operator_session  # noqa: E402
import support_service  # noqa: E402
import support_store as store  # noqa: E402
import user_identity  # noqa: E402

CONFIG = {
    'enabled': True,
    'service_hours': '12:00–24:00',
    'greeting': '你好',
    'privacy_note': '消息会保存',
    'max_text': 100,
    'retention_days': 180,
    'handoff_keywords': ['退款', '人工'],
    'notify': {'channel': 'none', 'min_interval_seconds': 0},
    'ai': {'enabled': False},
}


class FakeGateway:
    """Stands in for the gateway's /api/user/self."""

    users = {
        'token-kiwi': {'id': 7, 'username': 'kiwi', 'display_name': 'KiKi',
                       'status': 1, 'public_id': 'usr_kiwi'},
        'token-disabled': {'id': 8, 'username': 'off', 'status': 2},
    }

    def __init__(self, url):
        self.url = url

    def request(self, path, body=None, method=None, token=None):
        if path != '/api/user/self':
            return 404, {'success': False}, {}
        user = self.users.get(token)
        if user is None:
            return 401, {'success': False, 'message': 'invalid'}, {}
        return 200, {'success': True, 'data': user}, {}


class ServiceCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.events = root / 'support'
        self.secrets_path = root / 'secrets.json'
        self.state_path = root / 'admin-session-state.json'
        self.config_path = root / 'support.json'
        self.config_path.write_text(json.dumps(CONFIG), encoding='utf-8')
        self.document, _ = operator_session.init_secrets(self.secrets_path)

        self.original_api = user_identity.API
        user_identity.API = FakeGateway
        self.addCleanup(setattr, user_identity, 'API', self.original_api)

        self.service = support_service.Service(
            self.secrets_path, self.events, self.state_path, self.config_path,
            gateway_url='http://gateway.test', allow_insecure_cookie=True,
            public_origin='http://127.0.0.1')
        self.server = ThreadingHTTPServer(('127.0.0.1', 0),
                                         support_service.handler_factory(self.service))
        self.server.daemon_threads = True
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.server.shutdown)
        self.base = 'http://127.0.0.1:%d' % self.port

    def call(self, *args, **kwargs):
        status, payload, _ = self.call_full(*args, **kwargs)
        return status, payload

    def call_full(self, path, payload=None, token=None, cookie=None, csrf=None,
                  origin='http://127.0.0.1', method=None):
        """Like `call`, but also hands back the response headers.

        Needed for logout: the interesting part is the Set-Cookie that clears the
        session, which `call` throws away.
        """
        body = json.dumps(payload).encode('utf-8') if payload is not None else None
        headers = {}
        if body:
            headers['Content-Type'] = 'application/json'
        if token:
            headers['Authorization'] = 'Bearer ' + token
        if cookie:
            headers['Cookie'] = '%s=%s' % (operator_session.COOKIE_NAME, cookie)
        if csrf:
            headers['X-CSRF-Token'] = csrf
        if origin is not None:
            headers['Origin'] = origin
        request = urllib.request.Request(self.base + path, data=body, headers=headers,
                                         method=method)
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                return (response.status,
                        json.loads(response.read().decode() or '{}'),
                        dict(response.headers))
        except urllib.error.HTTPError as error:
            raw = error.read().decode('utf-8', 'replace')
            try:
                parsed = json.loads(raw or '{}')
            except ValueError:
                parsed = {'raw': raw[:200]}
            return error.code, parsed, dict(error.headers)

    def operator(self):
        """A cookie the service will accept, plus the CSRF token it expects."""
        cookie = operator_session.sign_session(
            self.document, int(time.time()) + 600, 'pw', 1)
        return cookie, operator_session.csrf_token(self.document, cookie)

    def start_conversation(self, token='token-kiwi', source='console'):
        status, payload = self.call('/brand/api/support/start', {'source': source},
                                    token=token)
        self.assertEqual(status, 200, payload)
        return payload['conversation']['id']

    def read_until(self, response, needle, seconds=3.0):
        """Read until `needle` shows up, polling with a short socket timeout.

        A single blocking read would report only "timed out", which cannot tell a
        missing message from a broken stream; this returns what actually arrived
        so the assertion message is useful.
        """
        wanted = needle.encode('utf-8')
        raw_socket = response.fp.raw._sock
        raw_socket.settimeout(0.25)
        buffer = b''
        deadline = time.time() + seconds
        while time.time() < deadline and wanted not in buffer:
            try:
                chunk = response.read(1)
            except (TimeoutError, socket.timeout):
                continue
            except (ValueError, OSError):
                break
            if not chunk:
                break
            # Accumulate bytes: decoding per byte splits multi-byte UTF-8 and
            # turns every Chinese character into a replacement char.
            buffer += chunk
        return buffer.decode('utf-8', 'replace')

    def read_stream(self, path, token=None, cookie=None, seconds=2.0, expect=1):
        """Read an SSE response until `expect` events have arrived."""
        request = urllib.request.Request(self.base + path)
        if token:
            request.add_header('Authorization', 'Bearer ' + token)
        if cookie:
            request.add_header('Cookie', '%s=%s' % (operator_session.COOKIE_NAME, cookie))
        chunks = []
        events = []
        with urllib.request.urlopen(request, timeout=seconds + 4) as response:
            self.assertEqual(response.headers.get('Content-Type'),
                             'text/event-stream; charset=utf-8')
            self.assertEqual(response.headers.get('X-Accel-Buffering'), 'no')
            deadline = time.time() + seconds
            while time.time() < deadline and len(events) < expect:
                raw = response.read(1)
                if not raw:
                    break
                chunks.append(raw.decode('utf-8', 'replace'))
                if ''.join(chunks).endswith('\n\n'):
                    block = ''.join(chunks)
                    chunks = []
                    if block.startswith('event:'):
                        events.append(block)
        return events


class PublicRouteTests(ServiceCase):
    def test_healthz_and_config_need_no_identity(self):
        status, payload = self.call('/healthz')
        self.assertEqual(status, 200)
        self.assertTrue(payload['service'] == 'omniapi-support')
        status, payload = self.call('/brand/api/support/config')
        self.assertEqual(status, 200)
        self.assertTrue(payload['enabled'])
        self.assertEqual(payload['max_text'], 100)
        self.assertIn('privacy_note', payload)
        # The operator's internal switches are not part of the public view.
        self.assertNotIn('handoff_keywords', payload)

    def test_a_request_without_a_token_is_refused(self):
        status, payload = self.call('/brand/api/support/start', {})
        self.assertEqual(status, 401)
        self.assertIn('登录', payload['message'])

    def test_a_disabled_account_is_refused(self):
        status, _ = self.call('/brand/api/support/start', {}, token='token-disabled')
        self.assertEqual(status, 401)

    def test_an_unknown_route_is_a_404(self):
        status, _ = self.call('/brand/api/support/nope')
        self.assertEqual(status, 404)


class StartQuotaTests(ServiceCase):
    """打开页面不该把用户锁在外面。

    线上真实发生过：用户刷新了几次客服页，`POST start` 就返回 429 —— 因为
    每次加载页面都会调用它，而当时每个 IP 每 10 分钟只允许 6 次。
    """

    def test_reloading_the_page_many_times_never_rate_limits(self):
        for index in range(12):
            status, payload = self.call('/brand/api/support/start',
                                        {'source': 'support-page'}, token='token-kiwi')
            with self.subTest(call=index + 1):
                self.assertEqual(status, 200, payload)

    def test_all_those_loads_return_the_same_conversation(self):
        first = self.start_conversation()   # 返回的是会话编号
        for _ in range(5):
            status, payload = self.call('/brand/api/support/start', {}, token='token-kiwi')
            self.assertEqual(status, 200, payload)
            self.assertEqual(payload['conversation']['id'], first)

    def test_opening_new_conversations_is_still_capped(self):
        """宽松的是「取回已有会话」，不是「无限开新会话」。"""
        opened = 0
        for _ in range(20):
            status, payload = self.call('/brand/api/support/start', {}, token='token-kiwi')
            if status == 200:
                self.call('/brand/api/support/close',
                          {'conversation_id': payload['conversation']['id']}, token='token-kiwi')
                opened += 1
            else:
                self.assertEqual(status, 429, payload)
                break
        self.assertLess(opened, 20, '开新会话竟然没有上限')
        self.assertGreaterEqual(opened, 3, '上限低到正常使用都会撞上：%d' % opened)


class UserConversationTests(ServiceCase):
    def test_the_conversation_id_does_not_carry_the_user_id(self):
        conv_id = self.start_conversation()
        self.assertTrue(store.CONV_ID.match(conv_id), conv_id)
        parts = conv_id.split('-')
        # 结构是 S-<日期>-<6 位随机十六进制>：后缀是随机的，不是计数器，也不是 uid，
        # 而且没有任何一段等于 uid —— 对外只暴露会话编号。
        self.assertEqual(parts[0], 'S')
        self.assertEqual(len(parts[2]), 6)
        self.assertNotIn('7', parts)

    def test_starting_twice_reuses_the_open_conversation(self):
        self.assertEqual(self.start_conversation(), self.start_conversation())

    def test_sending_appends_numbered_messages(self):
        conv_id = self.start_conversation()
        status, first = self.call('/brand/api/support/send',
                                  {'conversation_id': conv_id, 'text': '你好'},
                                  token='token-kiwi')
        self.assertEqual(status, 200, first)
        self.assertEqual(first['message']['seq'], 1)
        status, second = self.call('/brand/api/support/send',
                                   {'conversation_id': conv_id, 'text': '在吗'},
                                   token='token-kiwi')
        self.assertEqual(second['message']['seq'], 2)

    def test_a_blank_or_oversized_message_is_refused(self):
        conv_id = self.start_conversation()
        for text in ('', '   ', 'x' * 101):
            status, payload = self.call('/brand/api/support/send',
                                        {'conversation_id': conv_id, 'text': text},
                                        token='token-kiwi')
            self.assertEqual(status, 400, repr(text))

    def test_another_account_cannot_touch_the_conversation(self):
        conv_id = self.start_conversation()
        FakeGateway.users['token-other'] = {'id': 99, 'username': 'other', 'status': 1}
        self.addCleanup(FakeGateway.users.pop, 'token-other', None)
        status, _ = self.call('/brand/api/support/send',
                              {'conversation_id': conv_id, 'text': 'hi'},
                              token='token-other')
        self.assertEqual(status, 400)
        status, _ = self.call('/brand/api/support/history?conversation_id=' + conv_id,
                              token='token-other')
        self.assertEqual(status, 400)

    def test_send_rate_limit_is_per_account(self):
        conv_id = self.start_conversation()
        self.service.send_uid_gate = support_service.queue.SlidingWindow(2, 600)
        for text in ('一', '二'):
            status, _ = self.call('/brand/api/support/send',
                                  {'conversation_id': conv_id, 'text': text},
                                  token='token-kiwi')
            self.assertEqual(status, 200)
        status, payload = self.call('/brand/api/support/send',
                                    {'conversation_id': conv_id, 'text': '三'},
                                    token='token-kiwi')
        self.assertEqual(status, 429)
        self.assertIn('过于频繁', payload['message'])

    def test_a_handoff_keyword_flags_the_conversation(self):
        conv_id = self.start_conversation()
        status, payload = self.call('/brand/api/support/send',
                                    {'conversation_id': conv_id, 'text': '我要申请退款'},
                                    token='token-kiwi')
        self.assertEqual(status, 200)
        self.assertTrue(payload['needs_human'])
        rows = self.service.store.history(conv_id)
        self.assertEqual([row['role'] for row in rows], ['user', 'system'])

    def test_identity_checks_have_their_own_budget(self):
        self.service.identify_gate = support_service.queue.SlidingWindow(2, 600)
        self.call('/brand/api/support/start', {}, token='bad')
        self.call('/brand/api/support/start', {}, token='bad')
        status, _ = self.call('/brand/api/support/start', {}, token='bad')
        self.assertEqual(status, 429)

    def test_closing_ends_the_conversation_and_a_new_one_can_start(self):
        conv_id = self.start_conversation()
        status, _ = self.call('/brand/api/support/close', {'conversation_id': conv_id},
                              token='token-kiwi')
        self.assertEqual(status, 200)
        self.assertNotEqual(self.start_conversation(), conv_id)


class NotifyAuditTests(ServiceCase):
    def test_a_disabled_channel_is_recorded_rather_than_silently_dropped(self):
        conv_id = self.start_conversation()
        self.call('/brand/api/support/send', {'conversation_id': conv_id, 'text': '你好'},
                  token='token-kiwi')
        events = []
        for path in store.day_files(self.events):
            events.extend(store.load_events(path))
        notes = [event for event in events if event.get('type') == 'notify']
        self.assertTrue(notes, '推送尝试必须留证')
        self.assertEqual(notes[0]['channel'], 'none')
        self.assertEqual(notes[0]['status'], 'failed')
        # A notification attempt is not part of the timeline.
        self.assertEqual([row['role'] for row in self.service.store.history(conv_id)],
                         ['user'])


class OperatorRouteTests(ServiceCase):
    def test_reading_needs_the_operator_cookie(self):
        status, _ = self.call('/brand/admin/api/support/conversations')
        self.assertEqual(status, 401)

    def test_a_wrong_origin_is_refused(self):
        cookie, csrf = self.operator()
        status, payload = self.call('/brand/admin/api/support/reply',
                                    {'conversation_id': 'S-20260927-aaaaaa', 'text': 'hi'},
                                    cookie=cookie, csrf=csrf, origin='https://evil.test')
        self.assertEqual(status, 403)

    def test_a_write_without_csrf_is_refused(self):
        conv_id = self.start_conversation()
        cookie, _ = self.operator()
        status, payload = self.call('/brand/admin/api/support/reply',
                                    {'conversation_id': conv_id, 'text': '在的'},
                                    cookie=cookie)
        self.assertEqual(status, 403)
        self.assertIn('CSRF', payload['message'])
        self.assertEqual(self.service.store.history(conv_id), [])

    def test_the_operator_sees_the_queue_and_can_reply(self):
        conv_id = self.start_conversation()
        self.call('/brand/api/support/send', {'conversation_id': conv_id, 'text': '你好'},
                  token='token-kiwi')
        cookie, csrf = self.operator()
        status, listing = self.call('/brand/admin/api/support/conversations', cookie=cookie)
        self.assertEqual(status, 200, listing)
        self.assertEqual(listing['counts']['unread_operator'], 1)
        self.assertEqual(listing['conversations'][0]['id'], conv_id)

        status, payload = self.call('/brand/admin/api/support/reply',
                                    {'conversation_id': conv_id, 'text': '你好，请问有什么问题'},
                                    cookie=cookie, csrf=csrf)
        self.assertEqual(status, 200, payload)
        self.assertEqual(payload['message']['role'], 'operator')
        # Replying clears the unread badge for this conversation.
        self.assertEqual(self.service.store.get(conv_id)['unread_operator'], 0)
        # The operator half must not be able to move money.
        self.assertEqual(self.service.store.history(conv_id)[-1]['text'],
                         '你好，请问有什么问题')

    def test_mark_read_clears_the_badge(self):
        conv_id = self.start_conversation()
        self.call('/brand/api/support/send', {'conversation_id': conv_id, 'text': 'hi'},
                  token='token-kiwi')
        cookie, csrf = self.operator()
        status, _ = self.call('/brand/admin/api/support/read',
                              {'conversation_id': conv_id}, cookie=cookie, csrf=csrf)
        self.assertEqual(status, 200)
        self.assertEqual(self.service.store.get(conv_id)['unread_operator'], 0)

    def test_the_session_route_reports_identity_and_hands_out_csrf(self):
        status, payload = self.call('/brand/admin/api/support/session')
        self.assertEqual(status, 200)
        self.assertFalse(payload['authenticated'])
        self.assertEqual(payload['csrf_token'], '')

        cookie, csrf = self.operator()
        status, payload = self.call('/brand/admin/api/support/session', cookie=cookie)
        self.assertTrue(payload['authenticated'])
        self.assertEqual(payload['csrf_token'], csrf)

    def test_a_console_token_can_seed_the_operator_cookie(self):
        FakeGateway.users['token-root'] = {'id': 1, 'username': 'root', 'role': 100,
                                          'status': 1}
        self.addCleanup(FakeGateway.users.pop, 'token-root', None)
        status, payload = self.call('/brand/admin/api/support/session',
                                    {'access_token': 'token-root'})
        self.assertEqual(status, 200, payload)
        self.assertEqual(payload['user']['role'], 100)
        # An ordinary account must not be able to seed one.
        status, payload = self.call('/brand/admin/api/support/session',
                                    {'access_token': 'token-kiwi'})
        self.assertEqual(status, 403)

    def test_settings_expose_the_switches_but_not_the_credentials(self):
        cookie, _ = self.operator()
        status, payload = self.call('/brand/admin/api/support/settings', cookie=cookie)
        self.assertEqual(status, 200)
        self.assertEqual(payload['notify']['channel'], 'none')
        self.assertFalse(payload['ai']['enabled'])
        self.assertIn('notify_credentials_present', payload)
        self.assertNotIn('webhook', json.dumps(payload))

    def test_operator_history_reads_a_known_conversation(self):
        conv_id = self.start_conversation()
        self.call('/brand/api/support/send', {'conversation_id': conv_id, 'text': '你好'},
                  token='token-kiwi')
        cookie, _ = self.operator()
        status, payload = self.call(
            '/brand/admin/api/support/history?conversation_id=' + conv_id, cookie=cookie)
        self.assertEqual(status, 200)
        self.assertEqual(payload['messages'][0]['text'], '你好')


class StreamTests(ServiceCase):
    def test_the_user_stream_announces_itself(self):
        conv_id = self.start_conversation()
        events = self.read_stream('/brand/api/support/stream?conversation_id=' + conv_id,
                                  token='token-kiwi', expect=1)
        self.assertTrue(events[0].startswith('event: ready'), events)

    def test_a_reply_reaches_an_open_stream(self):
        conv_id = self.start_conversation()
        request = urllib.request.Request(
            self.base + '/brand/api/support/stream?conversation_id=' + conv_id)
        request.add_header('Authorization', 'Bearer token-kiwi')
        response = urllib.request.urlopen(request, timeout=10)
        self.addCleanup(response.close)
        self.assertIn(b'event: ready', response.read(40))

        cookie, csrf = self.operator()
        status, payload = self.call('/brand/admin/api/support/reply',
                                    {'conversation_id': conv_id, 'text': '收到，我看一下'},
                                    cookie=cookie, csrf=csrf)
        # Without this the failure mode is "the stream went quiet", which says
        # nothing about why.
        self.assertEqual(status, 200, payload)
        buffer = self.read_until(response, '收到')
        self.assertIn('event: message', buffer)
        self.assertIn('收到', buffer)

    def test_a_message_that_lands_immediately_still_reaches_the_stream(self):
        """Regression: fetching and waiting must not have a gap.

        If the fetch-then-wait pair is not tied together, a message that arrives
        in that window loses its wakeup and sits until the next 20s heartbeat --
        which is the difference between a chat and a thing that looks broken.
        """
        conv_id = self.start_conversation()
        request = urllib.request.Request(
            self.base + '/brand/api/support/stream?conversation_id=' + conv_id)
        request.add_header('Authorization', 'Bearer token-kiwi')
        response = urllib.request.urlopen(request, timeout=8)
        self.addCleanup(response.close)
        self.assertIn(b'event: ready', response.read(40))
        cookie, csrf = self.operator()
        status, payload = self.call('/brand/admin/api/support/reply',
                                    {'conversation_id': conv_id, 'text': '马上到'},
                                    cookie=cookie, csrf=csrf)
        self.assertEqual(status, 200, payload)
        buffer = self.read_until(response, '马上到')
        self.assertIn('马上到', buffer)

    def test_resuming_with_since_does_not_repeat_messages(self):
        conv_id = self.start_conversation()
        self.call('/brand/api/support/send', {'conversation_id': conv_id, 'text': '第一条'},
                  token='token-kiwi')
        self.call('/brand/api/support/send', {'conversation_id': conv_id, 'text': '第二条'},
                  token='token-kiwi')
        status, payload = self.call(
            '/brand/api/support/history?conversation_id=%s&since=1' % conv_id,
            token='token-kiwi')
        self.assertEqual([row['text'] for row in payload['messages']], ['第二条'])

    def test_the_operator_stream_needs_the_cookie(self):
        status, _ = self.call('/brand/admin/api/support/stream')
        self.assertEqual(status, 401)

    def test_the_stream_slot_cap_degrades_instead_of_hanging(self):
        conv_id = self.start_conversation()
        self.service._streams = support_service.MAX_STREAMS
        status, payload = self.call('/brand/api/support/stream?conversation_id=' + conv_id,
                                    token='token-kiwi')
        self.assertEqual(status, 503)
        self.assertEqual(payload['retry_after'], support_service.STREAM_RETRY_AFTER)


class OperatorLogoutTests(ServiceCase):
    """退出登录必须真的把两侧的管理会话一起断掉。

    线上真实发生过：经营者从控制台退出后，充值核验和客服工作台照旧能进 ——
    控制台的登出只撤销它自己的会话，而这张管理 cookie 有 8 小时 TTL，它根本
    不知道对方的存在。共用电脑上这意味着下一个人可以直接给用户加额度。
    """

    LOGOUT = '/brand/admin/api/support/logout'

    def session(self, cookie=None):
        return self.call('/brand/admin/api/support/session', cookie=cookie)

    def test_logout_invalidates_the_cookie_immediately(self):
        cookie, csrf = self.operator()
        status, payload = self.session(cookie)
        self.assertTrue(payload['authenticated'])
        status, payload = self.call(self.LOGOUT, {}, cookie=cookie, csrf=csrf)
        self.assertEqual(status, 200, payload)
        # 同一张 cookie 立刻作废，不需要等 TTL。
        status, payload = self.session(cookie)
        self.assertEqual(status, 200)
        self.assertFalse(payload['authenticated'])
        self.assertEqual(payload['csrf_token'], '')

    def test_logout_bumps_the_counter_that_both_services_read(self):
        before = operator_session.load_generation(self.state_path)
        cookie, csrf = self.operator()
        self.call(self.LOGOUT, {}, cookie=cookie, csrf=csrf)
        after = operator_session.load_generation(self.state_path)
        self.assertEqual(after, before + 1)
        # 旧代际签的 cookie 失效、新代际的仍然有效 —— 这正是「一次 bump、两边同时断」，
        # 因为充值服务读的是同一个文件的同一个计数。
        self.assertIsNone(operator_session.session_scope(self.document, cookie,
                                                         generation=after))
        fresh = operator_session.sign_session(self.document,
                                              int(time.time()) + 600, 'pw', after)
        self.assertIsNotNone(operator_session.session_scope(self.document, fresh,
                                                            generation=after))

    def test_logout_requires_csrf(self):
        cookie, _ = self.operator()
        status, payload = self.call(self.LOGOUT, {}, cookie=cookie)
        self.assertEqual(status, 403)
        self.assertIn('CSRF', str(payload.get('message')))

    def test_logout_requires_the_matching_origin(self):
        cookie, csrf = self.operator()
        status, _ = self.call(self.LOGOUT, {}, cookie=cookie, csrf=csrf,
                              origin='https://evil.example')
        self.assertEqual(status, 403)

    def test_a_stranger_cannot_force_a_logout(self):
        """没有会话时既不该改计数，也不该给任何提示性的成功。"""
        before = operator_session.load_generation(self.state_path)
        status, _ = self.call(self.LOGOUT, {})
        self.assertEqual(status, 401)
        self.assertEqual(operator_session.load_generation(self.state_path), before)

    def test_the_cookie_clearing_header_is_sent(self):
        cookie, csrf = self.operator()
        status, _, headers = self.call_full(self.LOGOUT, {}, cookie=cookie, csrf=csrf)
        self.assertEqual(status, 200)
        cleared = headers.get('Set-Cookie', '')
        self.assertIn(operator_session.COOKIE_NAME + '=', cleared)
        self.assertIn('Max-Age=0', cleared)


class SharedSessionCounterTests(unittest.TestCase):
    """「一次注销断两边」成立的前提是两个服务读同一个代际计数文件。

    这条一旦被改坏（比如给某个服务单独设了状态文件），注销就会只断一边，
    而且是静默的 —— 所以钉在这里。
    """

    def unit(self, name):
        return (BASE / 'ops/systemd' / name).read_text(encoding='utf-8')

    def test_the_support_unit_points_at_the_shared_counter(self):
        match = re.search(r'SUPPORT_STATE=(\S+)', self.unit('omniapi-support.service'))
        self.assertIsNotNone(match, '客服单元没有设置状态文件')
        self.assertTrue(match.group(1).endswith('/admin-session-state.json'),
                        match.group(1))

    def test_the_recharge_service_uses_the_same_default(self):
        source = (BASE / 'ops/topup_watch/topup_watch.py').read_text(encoding='utf-8')
        # 充值服务不设该环境变量，用的是 runtime 下的默认文件名；两者必须同名。
        self.assertIn("runtime / 'admin-session-state.json'", source)
        self.assertNotIn('TOPUP_WATCH_STATE', self.unit('omniapi-topup-watch.service'),
                         '充值服务若显式设了状态文件，就必须和客服指向同一个')

    def test_there_is_only_one_writer_of_that_counter(self):
        """不许有第二份「读+1+写」的实现：两个写者迟早会互相覆盖。"""
        session = (BASE / 'ops/operator_session.py').read_text(encoding='utf-8')
        self.assertIn('def bump_generation(', session)
        for name in ('ops/topup_watch/topup_watch.py', 'ops/support/support_service.py'):
            source = (BASE / name).read_text(encoding='utf-8')
            self.assertNotIn('write_generation(', source,
                             '%s 不该自己写代际，应该用 bump_generation' % name)


class ConfigTests(unittest.TestCase):
    def test_nested_blocks_are_merged_not_replaced(self):
        merged = support_service.merge_config({'ai': {'enabled': True}})
        self.assertTrue(merged['ai']['enabled'])
        self.assertEqual(merged['ai']['max_rounds_per_conversation'],
                         support_service.DEFAULT_CONFIG['ai']['max_rounds_per_conversation'])
        self.assertEqual(merged['max_text'], support_service.DEFAULT_CONFIG['max_text'])

    def test_comment_keys_are_dropped(self):
        merged = support_service.merge_config({'_comment_ai': 'x', 'enabled': False})
        self.assertFalse(merged['enabled'])
        self.assertNotIn('_comment_ai', merged)


if __name__ == '__main__':
    unittest.main()
