"""Unit tests for the recharge submission queue and its admin service."""
import json
import os
import sys
import tempfile
import time
import unittest
from unittest import mock
from decimal import Decimal
from pathlib import Path

BASE = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(BASE / 'scripts'))
sys.path.insert(0, str(BASE / 'ops'))
sys.path.insert(0, str(BASE / 'ops' / 'topup_watch'))

import operator_session  # noqa: E402
import topup_requests as queue  # noqa: E402
import topup_watch  # noqa: E402
import user_identity  # noqa: E402
import user_notices  # noqa: E402

CONFIG = {'cny_per_usd': '6.7489', 'min_amount_cny': 1, 'accepts_payments': True,
          # A declared fallback rate has to carry its own date: the service stops
          # pricing CNY orders with a rate the operator has not revisited inside
          # fx.fallback_max_age_seconds, so a date-less config is not a valid one.
          'fx_as_of': time.strftime('%Y-%m-%d')}


def session_user(uid, public_id=None):
    """Exactly the dict Service.authenticated_user() returns."""
    return {'id': uid, 'username': 'gw_%d' % uid, 'display_name': 'User %d' % uid,
            'public_id': public_id or 'usr_%026d' % uid}


class StoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name) / 'requests.jsonl'

    def tearDown(self):
        self.tmp.cleanup()

    # -- amounts ---------------------------------------------------------
    def test_amount_accepts_plain_values_and_rounds_to_cents(self):
        self.assertEqual(queue.decimal_amount(20), Decimal('20.00'))
        self.assertEqual(queue.decimal_amount(' 19.5 '), Decimal('19.50'))
        self.assertEqual(queue.decimal_amount(1.005), Decimal('1.01'))

    def test_amount_rejects_non_money(self):
        for value in (None, True, False, '', 'abc', '-5', '0', 'nan', 'inf',
                      '1e9', '1e-7', '0x10', '  ', '20,0', [1], {}):
            with self.assertRaises(ValueError, msg=repr(value)):
                queue.decimal_amount(value)

    def test_build_request_uses_the_shared_rate_and_quota(self):
        event = queue.build_request(CONFIG, 2, '10')
        self.assertEqual(event['usd'], '1.481722')
        self.assertEqual(event['quota'], 740861)
        self.assertEqual(event['rate_cny_per_usd'], '6.7489')
        self.assertEqual(event['cny'], '10')
        self.assertEqual(event['type'], 'request')
        self.assertTrue(queue.REQUEST_ID.match(event['id']), event['id'])

    def test_quote_is_immutable_and_expires(self):
        now = __import__('datetime').datetime(2026, 9, 27, tzinfo=__import__('datetime').timezone.utc)
        quotes = Path(self.tmp.name) / 'quotes.jsonl'
        quote = queue.create_quote('payoneer', '20', 'USD', '20', '1', 'USD settlement',
                                   now.isoformat(), quotes, now=now)
        self.assertTrue(queue.quote_valid(quote, now=now))
        self.assertFalse(queue.quote_valid(quote, now=now + __import__('datetime').timedelta(seconds=901)))
        self.assertEqual(queue.find_quote(quotes, quote['id'])['usd'], '20')

    def test_reference_is_unique_across_requests(self):
        event = queue.build_request(CONFIG, 2, 20)
        event['reference'] = 'TX-ABC'
        queue.append_event(event, self.path)
        self.assertTrue(queue.reference_used(self.path, 'tx-abc'))
        self.assertFalse(queue.reference_used(self.path, 'TX-OTHER'))

    # -- append-only log -------------------------------------------------
    def test_events_survive_a_round_trip(self):
        first = queue.build_request(CONFIG, 2, 20)
        queue.append_event(first, self.path)
        queue.append_event(queue.build_request(CONFIG, 3, 50), self.path)
        rows = queue.fold(queue.load_events(self.path))
        self.assertEqual([row['uid'] for row in rows], [2, 3])
        self.assertEqual([row['status'] for row in rows], ['pending', 'pending'])
        self.assertEqual(queue.counts(self.path)['pending'], 2)

    def test_a_partial_last_line_does_not_break_the_log(self):
        queue.append_event(queue.build_request(CONFIG, 2, 20), self.path)
        with self.path.open('a', encoding='utf-8') as handle:
            handle.write('{"type": "request", "id": "T-20260101-aaaa')
        self.assertEqual(len(queue.load_events(self.path)), 1)

    def test_status_change_is_appended_and_wins(self):
        event = queue.build_request(CONFIG, 2, 20)
        queue.append_event(event, self.path)
        queue.close_request(event['id'], 'fulfilled', self.path, code='CODE-1', transaction='REC-1')
        rows = queue.load_events(self.path)
        self.assertEqual(len(rows), 2)
        self.assertEqual(rows[0]['type'], 'request')
        self.assertEqual(rows[1]['type'], 'fulfilled')
        record = queue.find(self.path, event['id'])
        self.assertEqual(record['status'], 'fulfilled')
        self.assertEqual(record['code'], 'CODE-1')
        self.assertEqual(record['transaction'], 'REC-1')
        self.assertEqual(queue.pending(self.path), [])

    def test_a_second_terminal_event_is_ignored(self):
        event = queue.build_request(CONFIG, 2, 20)
        queue.append_event(event, self.path)
        queue.close_request(event['id'], 'fulfilled', self.path, code='CODE-1')
        queue.append_event({'type': 'dismissed', 'id': event['id'], 'at': 'later'}, self.path)
        self.assertEqual(queue.find(self.path, event['id'])['status'], 'fulfilled')

    def test_events_for_unknown_requests_are_ignored(self):
        queue.append_event({'type': 'fulfilled', 'id': 'T-20260101-ffffff', 'at': 'now'}, self.path)
        self.assertEqual(queue.fold(queue.load_events(self.path)), [])

    def test_closing_refuses_unknown_and_already_closed(self):
        with self.assertRaises(ValueError):
            queue.close_request('T-20260101-ffffff', 'dismissed', self.path)
        event = queue.build_request(CONFIG, 2, 20)
        queue.append_event(event, self.path)
        queue.close_request(event['id'], 'dismissed', self.path)
        with self.assertRaises(ValueError):
            queue.close_request(event['id'], 'fulfilled', self.path, code='CODE-2')
        with self.assertRaises(ValueError):
            queue.close_request(event['id'], 'pending', self.path)


class SlidingWindowTests(unittest.TestCase):
    def test_window_blocks_then_expires(self):
        now = [1000.0]
        gate = queue.SlidingWindow(2, 60, clock=lambda: now[0])
        self.assertTrue(gate.allow('a'))
        self.assertTrue(gate.allow('a'))
        self.assertFalse(gate.allow('a'))
        self.assertGreater(gate.retry_after('a'), 0)
        now[0] += 61
        self.assertTrue(gate.allow('a'))
        self.assertEqual(gate.retry_after('a'), 0)

    def test_keys_are_independent(self):
        gate = queue.SlidingWindow(1, 60, clock=lambda: 0.0)
        self.assertTrue(gate.allow('a'))
        self.assertTrue(gate.allow('b'))
        self.assertFalse(gate.allow('a'))


class ServiceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.events = root / 'requests.jsonl'
        self.ledger = root / 'ledger.json'
        self.secrets = root / 'secrets.json'
        self.config = root / 'topup.json'
        self.config.write_text(json.dumps(CONFIG), encoding='utf-8')
        topup_watch.init_secrets(self.secrets)
        self.service = topup_watch.Service(self.secrets, self.events, self.ledger, self.config,
                                           gateway_url='http://127.0.0.1:1')
        self.service.username = lambda user_id: 'tester'

    def tearDown(self):
        self.tmp.cleanup()

    def claim(self, uid=2, amount='20', method='cny-qr', client='203.0.113.9', **extra):
        """Submit the way the route does: quote first, identity passed in."""
        quote = self.service.quote({'method': method, 'amount': amount})
        payload = {'quote_id': quote['id']}
        payload.update(extra)
        return self.service.submit(payload, client, session_user(uid))

    def test_a_client_supplied_uid_is_ignored(self):
        """The credited account must come from the session, never the body."""
        quote = self.service.quote({'method': 'cny-qr', 'amount': '20'})
        result = self.service.submit({'uid': 999, 'quote_id': quote['id']},
                                     '203.0.113.9', session_user(2))
        self.assertEqual(result['request']['uid'], 2)
        self.assertEqual(result['request']['uid_source'], 'verified-session')

    def test_a_claim_without_a_quote_is_refused(self):
        with self.assertRaises(ValueError):
            self.service.submit({'uid': 2, 'cny': 20}, '203.0.113.9', session_user(2))

    def test_submit_records_a_claim(self):
        result = self.claim()
        self.assertFalse(result['duplicate'])
        record = result['request']
        self.assertEqual(record['uid'], 2)
        self.assertEqual(record['quoted_amount'], '20')
        self.assertEqual(record['quota'], 1481722)
        self.assertEqual(result['counts']['pending'], 1)

    def test_new_claim_notification_contains_review_context_not_payment_proof(self):
        notify_config = Path(self.tmp.name) / 'support.json'
        notify_secret = Path(self.tmp.name) / 'support-notify.json'
        notify_config.write_text(json.dumps({'notify': {'channel': 'wecom'}}), encoding='utf-8')
        notify_secret.write_text(json.dumps({'wecom': {'webhook': 'https://example.invalid/redacted'}}),
                                 encoding='utf-8')
        self.service.notify_config_path = notify_config
        self.service.notify_secrets_path = notify_secret
        record = self.claim(reference='SECRET-PAYMENT-REFERENCE')['request']
        with mock.patch.object(topup_watch.notifier, 'publish',
                               return_value={'channel': 'wecom', 'ok': True}) as publish:
            result = self.service.notify_topup(record)
        self.assertTrue(result['ok'])
        title, body, link = publish.call_args.args[2:5]
        self.assertIn('充值待核验', title)
        self.assertIn(record['id'], body)
        self.assertIn('20 CNY', body)
        self.assertNotIn('SECRET-PAYMENT-REFERENCE', body)
        self.assertTrue(link.endswith('/brand/admin.html'))
        notice = queue.load_events(self.events)[-1]
        self.assertEqual(notice['type'], 'notification')
        self.assertEqual(notice['status'], 'ok')

    def test_overdue_claim_is_escalated_once_without_credit(self):
        notify_config = Path(self.tmp.name) / 'support.json'
        notify_secret = Path(self.tmp.name) / 'support-notify.json'
        notify_config.write_text(json.dumps({'notify': {'channel': 'wecom'}}), encoding='utf-8')
        notify_secret.write_text(json.dumps({'wecom': {'webhook': 'https://example.invalid/redacted'}}),
                                 encoding='utf-8')
        self.service.notify_config_path = notify_config
        self.service.notify_secrets_path = notify_secret
        record = self.claim()['request']
        old = topup_watch.utcnow() + topup_watch.dt.timedelta(seconds=301)
        with mock.patch.object(topup_watch.notifier, 'publish', return_value={'ok': True}):
            self.assertEqual(self.service.escalate_overdue(now=old), [record['id']])
            self.assertEqual(self.service.escalate_overdue(now=old), [])
        current = queue.find(self.events, record['id'])
        self.assertEqual(current['status'], 'pending')
        self.assertTrue(current['escalated_at'])
        self.assertFalse(self.ledger.exists())
        notices = user_notices.unread(self.service.notices_path, 1, role=10)
        self.assertEqual(notices[0]['kind'], 'topup-overdue')

    def test_targeted_notice_is_visible_only_to_its_user_and_once(self):
        user_notices.publish(self.service.notices_path, '补偿到账', '已赠送 ¥30', uid=16,
                             notice_id='compensation-user-16')
        self.assertEqual(len(self.service.notices_for({'id': 16, 'role': 1})), 1)
        self.assertEqual(self.service.notices_for({'id': 17, 'role': 1}), [])
        self.assertTrue(self.service.acknowledge_notice({'id': 16, 'role': 1},
                                                        'compensation-user-16'))
        self.assertEqual(self.service.notices_for({'id': 16, 'role': 1}), [])

    def test_an_identical_claim_is_not_counted_twice(self):
        first = self.claim()
        again = self.claim()
        self.assertTrue(again['duplicate'])
        self.assertEqual(again['request']['id'], first['request']['id'])
        self.assertEqual(again['counts']['pending'], 1)

    def test_a_different_amount_is_a_new_claim(self):
        self.claim(amount='20')
        self.assertFalse(self.claim(amount='39')['duplicate'])

    def test_submission_rate_limit_is_per_source(self):
        self.service.submit_gate = queue.SlidingWindow(2, 600)
        self.claim(uid=2, client='198.51.100.1')
        self.claim(uid=3, client='198.51.100.1')
        with self.assertRaises(PermissionError):
            self.claim(uid=4, client='198.51.100.1')
        self.claim(uid=5, client='198.51.100.2')

    def test_summary_lists_pending_first(self):
        first = self.claim(uid=2, amount='20')['request']
        self.claim(uid=3, amount='50')
        queue.close_request(first['id'], 'dismissed', self.events, operator='admin-page')
        summary = self.service.summary()
        self.assertEqual([row['uid'] for row in summary['pending']], [3])
        self.assertEqual(summary['counts'], {'pending': 1, 'fulfilled': 0, 'dismissed': 1})
        self.assertEqual(summary['pending'][0]['username'], 'tester')

    def test_fulfill_refuses_a_closed_or_unknown_request(self):
        record = self.claim()['request']
        queue.close_request(record['id'], 'dismissed', self.events)
        with self.assertRaises(ValueError):
            self.service.fulfill(record['id'])
        with self.assertRaises(ValueError):
            self.service.fulfill('T-20260101-ffffff')

    def test_payoneer_quote_enforces_twenty_dollar_minimum(self):
        with self.assertRaises(ValueError):
            self.service.quote({'method': 'payoneer', 'amount': '19.99'})
        quote = self.service.quote({'method': 'payoneer', 'amount': '20'})
        self.assertEqual(quote['currency'], 'USD')
        self.assertEqual(quote['usd'], '20')

    def test_a_payoneer_request_does_not_require_a_reference(self):
        """The reference is optional: the operator verifies against their own
        statement, so demanding it only blocked users who paid without one."""
        result = self.claim(method='payoneer', amount='20')
        self.assertEqual(result['request']['method'], 'payoneer')
        self.assertEqual(result['request']['reference'], '')
        self.assertFalse(result['duplicate'])

    def test_a_supplied_reference_is_still_single_use(self):
        first = self.claim(method='payoneer', amount='20', reference='PAY-1')
        self.assertEqual(first['request']['reference'], 'PAY-1')
        with self.assertRaises(ValueError):
            self.claim(method='payoneer', amount='50', reference='pay-1')

    def test_a_missing_gateway_session_is_not_a_validation_error(self):
        """A restart wipes the in-memory operator session while the 30-day
        cookie survives, so this state must be reportable on its own -- a 400
        made it read like bad input and stranded the operator."""
        record = self.claim(amount='20')['request']
        with self.assertRaises(topup_watch.SessionMissing) as caught:
            self.service.fulfill(record['id'], net_received='20')
        self.assertEqual(caught.exception.code, 'gateway_session_missing')
        # The request must survive a failed credit attempt so it can be retried.
        self.assertEqual(queue.find(self.events, record['id'])['status'], 'pending')

    def test_summary_reports_whether_the_gateway_session_is_seeded(self):
        self.assertFalse(self.service.has_api())
        self.assertFalse(self.service.summary()['gateway_session'])
        self.service._api = object()
        self.assertTrue(self.service.has_api())
        self.assertTrue(self.service.summary()['gateway_session'])


class AuthTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name) / 'secrets.json'

    def tearDown(self):
        self.tmp.cleanup()

    def test_password_is_never_stored_in_the_clear(self):
        document, password = topup_watch.init_secrets(self.path)
        raw = self.path.read_text(encoding='utf-8')
        self.assertNotIn(password, raw)
        # Windows only tracks the read-only bit, so the mode is asserted on POSIX.
        if os.name == 'posix':
            self.assertEqual(oct(self.path.stat().st_mode)[-3:], '600')
        self.assertTrue(topup_watch.password_matches(document, password))
        self.assertFalse(topup_watch.password_matches(document, password + 'x'))
        self.assertFalse(topup_watch.password_matches(document, ''))
        self.assertFalse(topup_watch.password_matches(None, password))

    def test_generated_passwords_differ_and_avoid_ambiguous_characters(self):
        first = topup_watch.generate_password()
        second = topup_watch.generate_password()
        self.assertNotEqual(first, second)
        self.assertEqual(len(first), 16)
        self.assertFalse(set(first) & set('01lIO'))

    def test_session_cookie_round_trip(self):
        document, _ = topup_watch.init_secrets(self.path)
        cookie = topup_watch.sign_session(document, int(time.time()) + 60)
        self.assertTrue(topup_watch.session_valid(document, cookie))
        self.assertFalse(topup_watch.session_valid(document, cookie + 'x'))
        self.assertFalse(topup_watch.session_valid(document, 'v1.1.2'))
        self.assertFalse(topup_watch.session_valid(document, ''))
        self.assertFalse(topup_watch.session_valid(None, cookie))

    def test_expired_cookie_is_rejected(self):
        document, _ = topup_watch.init_secrets(self.path)
        expired = topup_watch.sign_session(document, int(time.time()) - 1)
        self.assertFalse(topup_watch.session_valid(document, expired))

    def test_cookie_from_another_install_is_rejected(self):
        first, _ = topup_watch.init_secrets(self.path)
        other = Path(self.tmp.name) / 'other.json'
        second, _ = topup_watch.init_secrets(other)
        cookie = topup_watch.sign_session(first, int(time.time()) + 60)
        self.assertTrue(topup_watch.session_valid(first, cookie))
        self.assertFalse(topup_watch.session_valid(second, cookie))

    def test_scope_is_carried_and_signed(self):
        document, _ = topup_watch.init_secrets(self.path)
        expires = int(time.time()) + 60
        cookie = topup_watch.sign_session(document, expires, 'na:1')
        self.assertEqual(topup_watch.session_scope(document, cookie), 'na:1')
        self.assertEqual(topup_watch.session_scope(
            document, topup_watch.sign_session(document, expires, 'pw')), 'pw')
        # 换了 scope 但没换签名，必须被拒
        forged = 'v1.%d.na:2.%s' % (expires, cookie.rsplit('.', 1)[1])
        self.assertIsNone(topup_watch.session_scope(document, forged))

    def test_scope_charset_is_restricted(self):
        document, _ = topup_watch.init_secrets(self.path)
        with self.assertRaises(ValueError):
            topup_watch.sign_session(document, int(time.time()) + 60, 'bad scope')
        with self.assertRaises(ValueError):
            topup_watch.sign_session(document, int(time.time()) + 60, '')


class ConsoleIdentityTests(unittest.TestCase):
    """经营者已经登录控制台，不该再输一遍密码 —— 用控制台身份换会话。"""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        config = root / 'topup.json'
        config.write_text(json.dumps(CONFIG), encoding='utf-8')
        self.service = topup_watch.Service(root / 'secrets.json', root / 'e.json',
                                           root / 'l.json', config,
                                           gateway_url='http://gateway.test')
        # The lookup lives in user_identity now, so that is the seam to stub.
        self.original = user_identity.API
        self.calls = []
        self.response = (200, {'success': True, 'data': {'id': 1, 'username': 'gw_operator',
                                                         'role': 100, 'status': 1}}, {})
        self.error = None
        holder = self

        class StubAPI:
            def __init__(self, url):
                self.url = url

            def request(self, path, body=None, method=None, token=None):
                holder.calls.append((path, token))
                if holder.error:
                    raise holder.error
                return holder.response

        user_identity.API = StubAPI

    def tearDown(self):
        user_identity.API = self.original
        self.tmp.cleanup()

    def test_root_account_is_recognised(self):
        admin = self.service.newapi_admin('a-token')
        self.assertEqual(admin, {'id': 1, 'username': 'gw_operator', 'role': 100})
        self.assertEqual(self.calls, [('/api/user/self', 'a-token')])

    def test_admin_role_is_enough_and_ordinary_user_is_not(self):
        for role, expected in ((10, True), (100, True), (1, False), (0, False)):
            self.response = (200, {'success': True,
                                   'data': {'id': 9, 'username': 'x', 'role': role, 'status': 1}}, {})
            self.assertEqual(self.service.newapi_admin('t') is not None, expected, role)

    def test_disabled_or_missing_account_is_rejected(self):
        self.response = (200, {'success': True,
                               'data': {'id': 9, 'role': 100, 'status': 2}}, {})
        self.assertIsNone(self.service.newapi_admin('t'))
        self.response = (200, {'success': False, 'message': 'Unauthorized'}, {})
        self.assertIsNone(self.service.newapi_admin('t'))

    def test_a_rejected_token_never_reaches_the_gateway_twice(self):
        self.assertIsNone(self.service.newapi_admin(''))
        self.assertIsNone(self.service.newapi_admin('   '))
        self.assertIsNone(self.service.newapi_admin('x' * 5000))
        self.assertEqual(self.calls, [])

    def test_gateway_failure_is_not_an_admit(self):
        self.error = RuntimeError('gateway unreachable')
        self.assertIsNone(self.service.newapi_admin('a-token'))

    def test_a_console_token_becomes_the_credit_session(self):
        """The operator's own token is what credits balances — no password login."""
        self.assertIsNotNone(self.service.newapi_admin('a-token'))
        session = self.service.api()
        self.assertEqual(session.access_token, 'a-token')
        self.assertEqual(session.user_id, 1)

    def test_a_missing_session_is_reported_instead_of_attempting_a_login(self):
        """Login is Turnstile-protected, so the service must not even try it."""
        with self.assertRaisesRegex(RuntimeError, 'Turnstile'):
            self.service.api()
        self.assertEqual(self.calls, [], 'no gateway call may be made without a session')


if __name__ == '__main__':
    unittest.main()


class IdentityTests(unittest.TestCase):
    """authenticated_user() is the only place the credited account is decided.

    It talks to the gateway, so every case here stubs the gateway: what matters
    is which answers are accepted, and that a bad or oversized token is refused
    before any request leaves the process.
    """

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        config = root / 'topup.json'
        config.write_text(json.dumps(CONFIG), encoding='utf-8')
        secrets = root / 'secrets.json'
        topup_watch.init_secrets(secrets)
        self.service = topup_watch.Service(secrets, root / 'events.jsonl',
                                           root / 'ledger.json', config,
                                           gateway_url='http://127.0.0.1:1')
        self.calls = []

    def tearDown(self):
        self.tmp.cleanup()

    def stub_gateway(self, status, payload):
        calls = self.calls

        class FakeAPI:
            def __init__(self, base):
                pass

            def request(self, path, body=None, method=None, token=None):
                calls.append((path, token))
                return status, payload, {}

        original = user_identity.API
        user_identity.API = FakeAPI
        self.addCleanup(setattr, user_identity, 'API', original)

    def test_a_valid_token_resolves_to_its_owner(self):
        self.stub_gateway(200, {'success': True, 'data': {
            'id': 7, 'username': 'kiki', 'display_name': 'KiKi',
            'status': 1, 'public_id': 'usr_abcdef'}})
        user = self.service.authenticated_user('a-token')
        self.assertEqual(user['id'], 7)
        self.assertEqual(user['public_id'], 'usr_abcdef')
        self.assertEqual(user['display_name'], 'KiKi')
        self.assertEqual(self.calls, [('/api/user/self', 'a-token')])

    def test_a_disabled_account_is_refused(self):
        self.stub_gateway(200, {'success': True, 'data': {'id': 7, 'status': 2}})
        self.assertIsNone(self.service.authenticated_user('a-token'))

    def test_a_rejected_token_is_refused(self):
        self.stub_gateway(401, {'success': False, 'message': 'invalid'})
        self.assertIsNone(self.service.authenticated_user('a-token'))

    def test_a_malformed_gateway_answer_is_refused(self):
        for payload in ({'success': True, 'data': 'nope'},
                        {'success': True, 'data': {'id': 0, 'status': 1}},
                        {'success': True, 'data': {'id': 'x', 'status': 1}},
                        {'success': False, 'data': {'id': 7, 'status': 1}}):
            self.stub_gateway(200, payload)
            self.assertIsNone(self.service.authenticated_user('a-token'), payload)

    def test_an_empty_or_oversized_token_never_reaches_the_gateway(self):
        self.stub_gateway(200, {'success': True, 'data': {'id': 7, 'status': 1}})
        self.assertIsNone(self.service.authenticated_user(''))
        self.assertIsNone(self.service.authenticated_user(None))
        self.assertIsNone(self.service.authenticated_user('   '))
        self.assertIsNone(
            self.service.authenticated_user('x' * (topup_watch.MAX_TOKEN_LENGTH + 1)))
        self.assertEqual(self.calls, [])

    def test_identity_checks_have_their_own_budget(self):
        """A bad token must not become a free stream of gateway lookups."""
        self.stub_gateway(401, {'success': False})
        self.service.identify_gate = queue.SlidingWindow(2, 600)
        client = '198.51.100.9'
        self.assertIsNone(self.service.identify('t', client))
        self.assertIsNone(self.service.identify('t', client))
        with self.assertRaises(PermissionError):
            self.service.identify('t', client)
        self.assertIsNone(self.service.identify('t', '198.51.100.10'))

    def test_a_regular_user_is_accepted_but_is_not_an_admin(self):
        self.stub_gateway(200, {'success': True, 'data': {
            'id': 2, 'username': 'gw_tester', 'display_name': 'Gateway Test User',
            'role': 1, 'status': 1}})
        self.assertEqual(self.service.authenticated_user('a-token')['id'], 2)
        self.assertIsNone(self.service.newapi_admin('a-token'))

class SessionCounterIsReadFresh(unittest.TestCase):
    """代际号必须每次现读，不能缓存在内存里。

    充值服务原本在启动时把它读进内存，于是**从客服侧注销**（或轮换口令）之后，充值侧
    照样认那张已注销的 cookie —— 注销只断了一边，而且表面上没有任何异常。
    `ops/support/probe_logout_e2e.py` 在隔离实例上抓到的就是这个：客服侧注销后，
    充值侧仍然返回 `authenticated: true`。
    """

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        config = root / 'topup.json'
        config.write_text(json.dumps(CONFIG), encoding='utf-8')
        self.service = topup_watch.Service(root / 'secrets.json', root / 'e.json',
                                           root / 'l.json', config,
                                           gateway_url='http://gateway.test')
        self.state = root / 'admin-session-state.json'

    def tearDown(self):
        self.tmp.cleanup()

    def test_the_counter_is_not_cached_in_memory(self):
        self.assertFalse(hasattr(self.service, 'session_generation'),
                         '代际号不该缓存在内存里：另一侧写入后必须立刻生效')
        self.assertEqual(self.service.generation(), 1)
        operator_session.bump_generation(self.state)
        self.assertEqual(self.service.generation(), 2,
                         '另一侧注销后，这里必须立刻看到新的代际号')

    def test_a_cookie_signed_before_an_external_bump_is_refused(self):
        document, _ = operator_session.init_secrets(self.service.secrets_path,
                                                    password='probe-password')
        cookie = operator_session.sign_session(document, int(time.time()) + 600, 'pw', 1)
        self.assertIsNotNone(operator_session.session_scope(
            document, cookie, generation=self.service.generation()))
        operator_session.bump_generation(self.state)     # 另一侧注销 / 轮换口令
        self.assertIsNone(operator_session.session_scope(
            document, cookie, generation=self.service.generation()))

    def test_revoking_here_bumps_the_same_shared_counter(self):
        before = operator_session.load_generation(self.state)
        self.assertEqual(self.service.revoke_sessions(), before + 1)
        self.assertEqual(operator_session.load_generation(self.state), before + 1)
