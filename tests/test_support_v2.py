import base64
import io
import json
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'ops' / 'support'))
sys.path.insert(0, str(ROOT / 'ops'))
sys.path.insert(0, str(ROOT / 'scripts'))

import support_store as store
import support_service
from tests.test_support_service import ServiceCase


class TicketIndexTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.folder = Path(self.temp.name) / 'events'
        self.store = store.ConversationStore(self.folder)

    def tearDown(self):
        self.store.index.close()
        self.temp.cleanup()

    def test_ticket_fields_and_state_machine_are_indexed(self):
        ticket = self.store.start(7, 'tester', 'drawer', '请求报错',
                                  'model_error', 'high', reuse=False)
        self.store.add_message(ticket['id'], 'user', '模型返回 500')
        self.assertEqual(self.store.get(ticket['id'])['status'], 'waiting_operator')
        self.store.add_message(ticket['id'], 'operator', '正在排查')
        self.assertEqual(self.store.get(ticket['id'])['status'], 'waiting_user')
        self.store.set_status(ticket['id'], 'resolved')
        rows = self.store.search(status='resolved')
        self.assertEqual(rows[0]['subject'], '请求报错')
        self.assertEqual(rows[0]['priority'], 'high')

    def test_internal_note_never_enters_public_history(self):
        ticket = self.store.start(7, reuse=False)
        self.store.add_message(ticket['id'], 'user', '公开消息')
        self.store.add_note(ticket['id'], '只给管理员看的备注')
        self.assertEqual([r['text'] for r in self.store.history(ticket['id'])], ['公开消息'])
        admin = self.store.admin_history(ticket['id'])
        self.assertTrue(any(r.get('internal') for r in admin))

    def test_jsonl_repairs_a_missing_sqlite_event(self):
        ticket = self.store.start(8, subject='索引恢复', reuse=False)
        self.store.add_message(ticket['id'], 'user', '落盘后模拟索引丢失')
        db = sqlite3.connect(str(self.folder / 'support-index.sqlite3'))
        with db:
            db.execute('DELETE FROM events')
            db.execute('DELETE FROM messages')
            db.execute('DELETE FROM tickets')
        db.close(); self.store.index.close()
        recovered = store.ConversationStore(self.folder)
        try:
            self.assertEqual(recovered.search(text='索引恢复')[0]['id'], ticket['id'])
            self.assertEqual(recovered.history(ticket['id'])[0]['text'], '落盘后模拟索引丢失')
        finally:
            recovered.index.close()

    def test_closed_ticket_can_only_reopen_within_seven_days(self):
        ticket = self.store.start(9, reuse=False)
        self.store.set_status(ticket['id'], 'closed')
        self.store.set_status(ticket['id'], 'waiting_operator')
        self.assertEqual(self.store.get(ticket['id'])['status'], 'waiting_operator')


class ImageValidationTests(unittest.TestCase):
    def test_non_image_is_rejected(self):
        if support_service.Image is None:
            self.skipTest('Pillow unavailable')
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'config.json').write_text('{}', encoding='utf-8')
            (root / 'secret.json').write_text('{}', encoding='utf-8')
            service = support_service.Service(root / 'secret.json', root / 'events',
                root / 'state.json', root / 'config.json', public_origin='http://127.0.0.1')
            ticket = service.store.start(1, reuse=False)
            with self.assertRaises(ValueError):
                service.save_images(ticket['id'], [{'name': '../x.svg',
                    'data': base64.b64encode(b'<svg onload=alert(1)>').decode()}], 'user')
            service.store.index.close()


class TicketV2HttpTests(ServiceCase):
    def create_ticket(self, **overrides):
        body = {'category': 'api_access', 'subject': '如何接入',
                'description': '请问 Base URL 怎么填写？'}
        body.update(overrides)
        status, payload = self.call('/brand/api/support/v2/tickets', body,
                                    token='token-kiwi')
        self.assertEqual(status, 201, payload)
        return payload['ticket']

    def test_user_creates_and_lists_a_typed_ticket(self):
        ticket = self.create_ticket(priority='high')
        self.assertEqual(ticket['category'], 'api_access')
        self.assertEqual(ticket['priority'], 'normal')  # client cannot self-escalate
        status, payload = self.call('/brand/api/support/v2/tickets',
                                    token='token-kiwi', method='GET')
        self.assertEqual(status, 200)
        self.assertEqual(payload['tickets'][0]['subject'], '如何接入')

    def test_user_cannot_read_another_accounts_ticket(self):
        ticket = self.create_ticket()
        self.service.identify_gate = support_service.queue.SlidingWindow(100, 60)
        support_service.user_identity.API.users['token-other'] = {
            'id': 99, 'username': 'other', 'status': 1}
        status, _ = self.call('/brand/api/support/v2/ticket?id=' + ticket['id'],
                              token='token-other', method='GET')
        self.assertEqual(status, 400)

    def test_internal_note_is_admin_only(self):
        ticket = self.create_ticket(); cookie, csrf = self.operator()
        status, _ = self.call('/brand/admin/api/support/v2/note',
            {'ticket_id': ticket['id'], 'text': '仅管理员可见'}, cookie=cookie, csrf=csrf)
        self.assertEqual(status, 201)
        _, public = self.call('/brand/api/support/v2/ticket?id=' + ticket['id'],
                              token='token-kiwi', method='GET')
        self.assertNotIn('仅管理员可见', json.dumps(public, ensure_ascii=False))
        _, private = self.call('/brand/admin/api/support/v2/ticket?id=' + ticket['id'],
                               cookie=cookie, method='GET')
        self.assertIn('仅管理员可见', json.dumps(private, ensure_ascii=False))

    def test_runtime_settings_require_csrf(self):
        cookie, csrf = self.operator()
        status, _ = self.call('/brand/admin/api/support/v2/settings',
                              {'greeting': '新的欢迎语'}, cookie=cookie)
        self.assertEqual(status, 403)
        status, payload = self.call('/brand/admin/api/support/v2/settings',
                                    {'greeting': '新的欢迎语'}, cookie=cookie, csrf=csrf)
        self.assertEqual(status, 200, payload)
        self.assertEqual(self.service.config()['greeting'], '新的欢迎语')

    def test_svg_disguised_as_image_is_rejected(self):
        ticket = self.create_ticket()
        fake = base64.b64encode(b'<svg onload="alert(1)"></svg>').decode()
        status, _ = self.call('/brand/api/support/v2/upload',
            {'ticket_id': ticket['id'], 'files': [{'name': 'proof.png', 'data': fake}]},
            token='token-kiwi')
        self.assertEqual(status, 400)


if __name__ == '__main__':
    unittest.main()
