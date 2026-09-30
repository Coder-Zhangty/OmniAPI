"""Unit tests for the support conversation event log and its folded state."""
import datetime as dt
import json
import sys
import tempfile
import unittest
from pathlib import Path

BASE = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(BASE / 'ops' / 'support'))

import support_store as store  # noqa: E402


class EventLogTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name) / '2026-09-27.jsonl'

    def tearDown(self):
        self.tmp.cleanup()

    def test_events_round_trip(self):
        store.append_event({'type': 'conversation', 'id': 'S-20260927-aaaaaa'}, self.path)
        store.append_event({'type': 'close', 'conv': 'S-20260927-aaaaaa'}, self.path)
        events = store.load_events(self.path)
        self.assertEqual([event['type'] for event in events], ['conversation', 'close'])

    def test_a_torn_last_line_does_not_break_the_log(self):
        store.append_event({'type': 'conversation', 'id': 'S-20260927-bbbbbb'}, self.path)
        with self.path.open('a', encoding='utf-8') as handle:
            handle.write('{"type": "message", "conv": "S-2026')
        self.assertEqual(len(store.load_events(self.path)), 1)

    def test_garbage_lines_are_skipped(self):
        self.path.write_text('not json\n[1,2]\n{"no_type": true}\n', encoding='utf-8')
        self.assertEqual(store.load_events(self.path), [])

    def test_missing_file_is_an_empty_log(self):
        self.assertEqual(store.load_events(Path(self.tmp.name) / 'absent.jsonl'), [])


class TextRuleTests(unittest.TestCase):
    def test_blank_and_oversized_text_is_refused(self):
        for value in ('', '   ', '\n\t ', None, 123, ['a']):
            self.assertIsNotNone(store.text_error(value), repr(value))
        self.assertIsNotNone(store.text_error('x' * (store.MAX_TEXT + 1)))
        self.assertIsNone(store.text_error('x' * store.MAX_TEXT))
        self.assertIsNone(store.text_error(' 你好 '))

    def test_control_characters_are_refused(self):
        self.assertIsNotNone(store.text_error('hello\x00world'))


class ConversationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.folder = Path(self.tmp.name)
        self.store = store.ConversationStore(self.folder)

    def tearDown(self):
        self.tmp.cleanup()

    def test_conversation_id_is_not_a_user_id(self):
        conv_id = store.new_conversation_id()
        self.assertTrue(store.CONV_ID.match(conv_id), conv_id)
        self.assertNotIn('7', conv_id.split('-')[0])

    def test_starting_twice_returns_the_open_conversation(self):
        first = self.store.start(7, 'KiKi', 'console')
        second = self.store.start(7, 'KiKi', 'wallet')
        self.assertEqual(first['id'], second['id'])
        self.assertEqual(self.store.counts()['conversations'], 1)

    def test_sequence_numbers_increase_and_since_filters(self):
        conv = self.store.start(7)
        self.store.add_message(conv['id'], 'user', '你好')
        self.store.add_message(conv['id'], 'operator', '您好')
        rows = self.store.history(conv['id'])
        self.assertEqual([row['seq'] for row in rows], [1, 2])
        self.assertEqual([row['text'] for row in self.store.since(conv['id'], 1)], ['您好'])

    def test_roles_are_limited(self):
        conv = self.store.start(7)
        with self.assertRaises(ValueError):
            self.store.add_message(conv['id'], 'admin', 'hi')

    def test_messages_carry_ai_metadata_when_present(self):
        conv = self.store.start(7)
        self.store.add_message(conv['id'], 'ai', '按 token 计费',
                               ai={'model': 'gpt-5.6-luna', 'tokens': 42})
        row = self.store.history(conv['id'])[0]
        self.assertEqual(row['ai']['tokens'], 42)
        self.assertEqual(self.store.get(conv['id'])['ai_rounds'], 1)

    def test_unread_counts_track_each_side(self):
        conv = self.store.start(7)
        self.store.add_message(conv['id'], 'user', '在吗')
        self.store.add_message(conv['id'], 'user', '急')
        self.assertEqual(self.store.get(conv['id'])['unread_operator'], 2)
        self.store.mark_read(conv['id'], 'operator')
        self.assertEqual(self.store.get(conv['id'])['unread_operator'], 0)
        self.store.add_message(conv['id'], 'operator', '在的')
        self.assertEqual(self.store.get(conv['id'])['unread_user'], 1)
        self.store.mark_read(conv['id'], 'user')
        self.assertEqual(self.store.get(conv['id'])['unread_user'], 0)

    def test_a_closed_conversation_cannot_be_written_to(self):
        conv = self.store.start(7)
        self.store.close(conv['id'], by='operator')
        with self.assertRaises(ValueError):
            self.store.add_message(conv['id'], 'user', '还在吗')
        with self.assertRaises(ValueError):
            self.store.close(conv['id'], by='operator')

    def test_a_new_conversation_opens_after_close(self):
        first = self.store.start(7)
        self.store.close(first['id'])
        second = self.store.start(7)
        self.assertNotEqual(first['id'], second['id'])
        self.assertEqual(self.store.counts()['open'], 1)

    def test_handoff_is_recorded_once(self):
        conv = self.store.start(7)
        self.store.handoff(conv['id'], 'keyword')
        self.store.handoff(conv['id'], 'user_request')
        self.assertTrue(self.store.get(conv['id'])['needs_human'])
        self.assertEqual(self.store.get(conv['id'])['handoff_reason'], 'keyword')

    def test_unknown_conversation_is_refused(self):
        for call in (lambda: self.store.add_message('S-20260927-ffffff', 'user', 'hi'),
                     lambda: self.store.mark_read('S-20260927-ffffff', 'user'),
                     lambda: self.store.close('S-20260927-ffffff')):
            with self.assertRaises(ValueError):
                call()

    def test_history_falls_back_to_disk_after_eviction(self):
        """A conversation closed yesterday is out of memory but not gone."""
        conv = self.store.start(7)
        self.store.add_message(conv['id'], 'user', '三天前的消息')
        self.store.close(conv['id'])
        reopened = store.ConversationStore(self.folder, max_conversations=0)
        self.assertNotIn(conv['id'], reopened.messages)
        rows = reopened.history(conv['id'])
        self.assertEqual([row['text'] for row in rows], ['三天前的消息'])

    def test_state_is_recovered_from_the_log(self):
        conv = self.store.start(9, 'tester', 'pricing')
        self.store.add_message(conv['id'], 'user', '多少钱')
        self.store.add_message(conv['id'], 'operator', '看定价页')
        self.store.handoff(conv['id'], 'keyword')
        recovered = store.ConversationStore(self.folder)
        state = recovered.get(conv['id'])
        self.assertEqual(state['uid'], 9)
        self.assertEqual(state['username'], 'tester')
        self.assertTrue(state['needs_human'])
        self.assertEqual(len(recovered.history(conv['id'])), 2)

    def test_listing_puts_open_and_waiting_first(self):
        a = self.store.start(1)
        b = self.store.start(2)
        c = self.store.start(3)
        self.store.close(c['id'])
        self.store.add_message(a['id'], 'user', 'A')
        self.store.add_message(b['id'], 'user', 'B')
        self.store.handoff(b['id'], 'keyword')
        order = [state['id'] for state in self.store.listing()]
        self.assertEqual(order[0], b['id'])       # waiting for a human
        self.assertEqual(order[-1], c['id'])      # closed goes last
        self.assertNotIn(c['id'], [state['id'] for state in self.store.listing(False)])

    def test_conversation_cap_is_refused_not_silently_dropped(self):
        tight = store.ConversationStore(self.folder, max_conversations=1)
        tight.start(1)
        with self.assertRaises(ValueError):
            tight.start(2)

    def test_events_go_to_the_file_named_after_the_day(self):
        conv = self.store.start(7)
        self.store.add_message(conv['id'], 'user', 'hi')
        files = list(self.folder.glob('*.jsonl'))
        self.assertEqual(len(files), 1)
        self.assertEqual(files[0].stem, store.day_of())
        rows = [json.loads(line) for line in files[0].read_text(encoding='utf-8').splitlines()]
        self.assertEqual([row['type'] for row in rows], ['conversation', 'message'])


class PruneTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.folder = Path(self.tmp.name)
        self.now = dt.datetime(2026, 9, 27, 12, 0, tzinfo=dt.timezone.utc)

    def tearDown(self):
        self.tmp.cleanup()

    def _day(self, name):
        path = self.folder / (name + '.jsonl')
        path.write_text('{"type": "conversation", "id": "S-20260101-aaaaaa"}\n',
                        encoding='utf-8')
        return path

    def test_prune_is_a_dry_run_unless_asked(self):
        old = self._day('2026-01-01')
        recent = self._day('2026-09-26')
        report = store.prune(self.folder, 30, now=self.now)
        self.assertEqual(report['cutoff_day'], '2026-08-28')
        self.assertEqual(report['files'], ['2026-01-01.jsonl'])
        self.assertFalse(report['applied'])
        self.assertTrue(old.exists())
        self.assertTrue(recent.exists())

    def test_prune_with_apply_removes_only_old_files(self):
        old = self._day('2026-01-01')
        recent = self._day('2026-09-26')
        report = store.prune(self.folder, 30, apply=True, now=self.now)
        self.assertTrue(report['applied'])
        self.assertFalse(old.exists())
        self.assertTrue(recent.exists())


if __name__ == '__main__':
    unittest.main()
