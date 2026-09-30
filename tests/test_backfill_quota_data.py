"""Unit tests for the dashboard aggregate backfill.

The script talks to PostgreSQL through `docker exec`, so only the pure parts are
tested here: the SQL it builds, the rows it parses back, and the summary it
reports. The SQL matters most — it has to match New API's own aggregation or the
backfilled rows would disagree with rows the service writes itself.
"""
import sys
import unittest
from pathlib import Path

BASE = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(BASE / 'scripts'))

import backfill_quota_data as backfill  # noqa: E402


class SqlTests(unittest.TestCase):
    def test_bucket_floors_to_the_hour(self):
        self.assertEqual(backfill.bucket(1790388000), 1790388000)
        self.assertEqual(backfill.bucket(1790388001), 1790388000)
        self.assertEqual(backfill.bucket(1790388000 + 3599), 1790388000)
        self.assertEqual(backfill.bucket(1790388000 + 3600), 1790388000 + 3600)

    def test_aggregate_only_counts_consumption_logs(self):
        sql = backfill.aggregate_sql(1000, 2000)
        # 2 = LogTypeConsume. Recharge and error rows never reached LogQuotaData.
        self.assertIn('l.type = 2', sql)
        self.assertIn('l.created_at >= 1000', sql)
        self.assertIn('l.created_at <= 2000', sql)
        # `group` is reserved in PostgreSQL and has to be quoted.
        self.assertIn('l."group"', sql)
        # The modulo must survive %-formatting as a single percent sign.
        self.assertIn('l.created_at % 3600', sql)
        self.assertNotIn('%%', sql)
        self.assertIn('prompt_tokens', sql)
        self.assertIn('completion_tokens', sql)

    def test_aggregate_groups_by_every_key_column(self):
        sql = backfill.aggregate_sql(0, 1)
        for column in ('user_id', 'username', 'model_name', 'use_group',
                       'token_id', 'channel_id'):
            self.assertIn(column, sql)
        self.assertIn('GROUP BY 1, 2, 3, 4, 5, 6, 7', sql)

    def test_insert_skips_buckets_that_already_exist(self):
        sql = backfill.insert_sql(0, 1, 'node-a')
        self.assertTrue(sql.startswith('INSERT INTO quota_data'))
        self.assertIn('WHERE NOT EXISTS', sql)
        # Rerunnable: an existing bucket must be left alone.
        for column in backfill.GUARD_COLUMNS:
            self.assertIn('q.%s = a.%s' % (column, column), sql)

    def test_insert_never_compares_node_name_against_the_subquery(self):
        """The aggregate has no node_name column; comparing it would fail at
        runtime with 'column a.node_name does not exist'."""
        sql = backfill.insert_sql(0, 1, 'node-a')
        self.assertNotIn('a.node_name', sql)
        self.assertIn("q.node_name = 'node-a'", sql)
        self.assertIn("'node-a'", sql.split('FROM (')[0])

    def test_node_name_is_escaped(self):
        sql = backfill.insert_sql(0, 1, "o'brien-node")
        self.assertIn("'o''brien-node'", sql)

    def test_key_and_guard_columns_stay_in_step(self):
        self.assertEqual(set(backfill.GUARD_COLUMNS),
                         set(backfill.KEY_COLUMNS) - {'node_name'})
        self.assertIn('node_name', backfill.KEY_COLUMNS)


class ParsingTests(unittest.TestCase):
    ROW = '4|KiKi|grok-4.5|1790388000|default|11|1|49|16693|216313'

    def test_parses_a_psql_row(self):
        rows = backfill.parse_rows(self.ROW + '\n\n')
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0], {
            'user_id': 4, 'username': 'KiKi', 'model_name': 'grok-4.5',
            'created_at': 1790388000, 'use_group': 'default', 'token_id': 11,
            'channel_id': 1, 'count': 49, 'quota': 16693, 'token_used': 216313,
        })

    def test_rejects_a_truncated_row(self):
        with self.assertRaises(ValueError):
            backfill.parse_rows('4|KiKi|grok-4.5\n')

    def test_empty_output_is_no_rows(self):
        self.assertEqual(backfill.parse_rows(''), [])

    def test_summary_totals_and_groupings(self):
        rows = backfill.parse_rows('\n'.join([
            self.ROW,
            '5|750482476@qq.com|gpt-5.4|1790398800|default|3|1|2|1215|9435',
            '4|KiKi|grok-4.5|1790398800|default|11|1|1|100|7',
        ]))
        summary = backfill.summarise(rows)
        self.assertEqual(summary['rows'], 3)
        self.assertEqual(summary['requests'], 52)
        self.assertEqual(summary['quota'], 16693 + 1215 + 100)
        self.assertEqual(summary['token_used'], 216313 + 9435 + 7)
        self.assertEqual(summary['by_model']['grok-4.5']['count'], 50)
        self.assertEqual(summary['hours'], [1790388000, 1790398800])
        # Both buckets land on the same UTC day; the report must not merge days.
        self.assertEqual(list(summary['by_day']), ['2026-09-26'])

    def test_summary_splits_days_in_utc(self):
        rows = backfill.parse_rows('\n'.join([
            '4|KiKi|grok-4.5|1790388000|default|11|1|1|1|1',   # 2026-09-26
            '4|KiKi|grok-4.5|1790474400|default|11|1|1|1|1',   # 2026-09-27
        ]))
        self.assertEqual(sorted(backfill.summarise(rows)['by_day']),
                         ['2026-09-26', '2026-09-27'])


if __name__ == '__main__':
    unittest.main()
