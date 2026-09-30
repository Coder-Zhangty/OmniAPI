import datetime as dt
import json
import sys
import tempfile
import unittest
from pathlib import Path

BASE = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(BASE / 'scripts'))
import fx_rates


def snapshot(as_of, rates=None, fetched_at=None, rate_as_of=None):
    return {'base': 'USD', 'rates': rates or {'USD': '1', 'CNY': '6.75'},
            'source': 'test', 'as_of': as_of,
            'fetched_at': fetched_at or as_of, 'rate_as_of': rate_as_of or {}}


class FxRateTests(unittest.TestCase):
    def test_normalizes_all_supported_currencies(self):
        payload = {'timestamp': '2026-09-27T00:00:00+00:00', 'rates': {
            'CNY': 7, 'EUR': .85, 'GBP': .74, 'JPY': 150,
            'HKD': 7.8, 'CAD': 1.35, 'AUD': 1.5}}
        result = fx_rates.normalize(payload, dt.datetime(2026, 9, 27, tzinfo=dt.timezone.utc))
        self.assertEqual(result['base'], 'USD')
        self.assertEqual(result['rates']['USD'], '1')
        self.assertEqual(result['rates']['CNY'], '7')

    def test_missing_or_non_positive_rate_is_rejected(self):
        payload = {'rates': {code: 1 for code in fx_rates.CURRENCIES[1:]}}
        payload['rates']['JPY'] = 0
        with self.assertRaises(ValueError):
            fx_rates.normalize(payload)

    def test_normalizes_eastmoney_pairs_and_keeps_per_rate_times(self):
        values = {
            'USDCNYC': 6.75, 'USDEUR': .88, 'USDGBP': .75,
            'USDJPY': 157.2, 'USDHKD': 7.84, 'USDCAD': 1.41,
            'USDAUD': 1.42,
        }
        payload = {'rc': 0, 'data': {'diff': [
            {'f12': code, 'f2': value, 'f124': 1790369940}
            for code, value in values.items()
        ]}}
        result = fx_rates.normalize_eastmoney(
            payload, dt.datetime(2026, 9, 27, tzinfo=dt.timezone.utc))
        self.assertEqual(result['rates']['CNY'], '6.75')
        self.assertEqual(result['rates']['EUR'], '0.88')
        self.assertEqual(set(result['rate_as_of']), set(fx_rates.CURRENCIES[1:]))
        # The snapshot must record on what basis the feed is used, so a quote
        # can be traced back to the operator's recorded decision.
        self.assertEqual(result['license_status'], fx_rates.EASTMONEY_LICENSE_STATUS)
        self.assertIn('东方财富', result['source'])

    def test_eastmoney_missing_symbol_is_rejected(self):
        payload = {'rc': 0, 'data': {'diff': []}}
        with self.assertRaisesRegex(ValueError, 'missing Eastmoney symbol'):
            fx_rates.normalize_eastmoney(payload)

    def test_eastmoney_without_any_quote_time_is_rejected(self):
        values = {secid.split('.', 1)[1]: 1
                  for secid in fx_rates.EASTMONEY_SYMBOLS.values()}
        payload = {'rc': 0, 'data': {'diff': [
            {'f12': code, 'f2': value} for code, value in values.items()]}}
        with self.assertRaisesRegex(ValueError, 'no quote times'):
            fx_rates.normalize_eastmoney(payload)

    def test_a_daily_fixing_is_usable_across_a_weekend(self):
        """The CNY fixing is published once per trading day; 15 minutes is wrong."""
        published = '2026-09-25T08:00:00+00:00'   # Friday
        fresh = fx_rates.evaluate(snapshot(published), 'CNY',
                                  now=dt.datetime(2026, 9, 28, tzinfo=dt.timezone.utc))
        self.assertTrue(fresh['usable'], 'Monday must still accept Friday\'s fixing')
        self.assertEqual(fresh['rate'], '6.75')
        self.assertIsNone(fresh['reason'])

    def test_a_rate_beyond_the_window_is_refused_not_priced(self):
        published = '2026-09-25T08:00:00+00:00'
        old = fx_rates.evaluate(snapshot(published), 'CNY',
                                now=dt.datetime(2026, 10, 2, tzinfo=dt.timezone.utc))
        self.assertFalse(old['usable'])
        self.assertEqual(old['reason'], 'quote_too_old')

    def test_evaluate_reports_why_a_rate_cannot_be_used(self):
        now = dt.datetime(2026, 9, 27, tzinfo=dt.timezone.utc)
        missing = fx_rates.evaluate(snapshot('2026-09-27T00:00:00+00:00',
                                             rates={'USD': '1'}), 'CNY', now=now)
        self.assertFalse(missing['usable'])
        self.assertEqual(missing['reason'], 'rate_missing')
        self.assertIsNone(missing['rate'])

        undated = fx_rates.evaluate(snapshot(None), 'CNY', now=now)
        self.assertFalse(undated['usable'])
        self.assertEqual(undated['reason'], 'quote_time_unknown')

        usable = fx_rates.evaluate(snapshot('2026-09-27T00:00:00+00:00'), 'CNY', now=now)
        self.assertTrue(usable['usable'])

    def test_a_currency_is_aged_by_its_own_quote_time(self):
        """A fresh snapshot must not make an old per-currency quote look current."""
        now = dt.datetime(2026, 9, 27, 12, tzinfo=dt.timezone.utc)
        mixed = snapshot('2026-09-27T11:00:00+00:00',
                         rate_as_of={'CNY': '2026-09-20T00:00:00+00:00'})
        verdict = fx_rates.evaluate(mixed, 'CNY', now=now)
        self.assertFalse(verdict['usable'])
        self.assertEqual(verdict['reason'], 'quote_too_old')
        # USD takes its age from the snapshot itself and is therefore fine.
        self.assertEqual(verdict['as_of'], '2026-09-20T00:00:00+00:00')

    def test_cache_staleness_follows_the_configured_window(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'fx.json'
            as_of = dt.datetime(2026, 9, 27, tzinfo=dt.timezone.utc)
            path.write_text(json.dumps(snapshot(as_of.isoformat())), encoding='utf-8')
            loaded = fx_rates.load(path, as_of + dt.timedelta(hours=95))
            self.assertFalse(loaded['stale'])
            self.assertEqual(loaded['max_age_seconds'], fx_rates.MAX_AGE)
            self.assertTrue(fx_rates.load(path, as_of + dt.timedelta(hours=97))['stale'])
            # An explicit window wins over the default.
            self.assertTrue(fx_rates.load(path, as_of + dt.timedelta(hours=2),
                                          max_age=3600)['stale'])

    def test_refresh_cadence_follows_the_fetch_time_not_the_quote_time(self):
        """A closed market keeps quoting the same rate; that must not trigger a refetch."""
        old_quote = '2026-09-25T08:00:00+00:00'
        now = dt.datetime(2026, 9, 27, 12, tzinfo=dt.timezone.utc)
        just_pulled = snapshot(old_quote, fetched_at='2026-09-27T11:58:00+00:00')
        self.assertFalse(fx_rates.needs_refresh(just_pulled, 300, now=now))
        stale_pull = snapshot(old_quote, fetched_at='2026-09-27T11:00:00+00:00')
        self.assertTrue(fx_rates.needs_refresh(stale_pull, 300, now=now))
        self.assertTrue(fx_rates.needs_refresh(snapshot(old_quote, fetched_at=None),
                                               300, now=now))
        # A freshly fetched snapshot still prices with Friday's fixing.
        self.assertTrue(fx_rates.evaluate(just_pulled, 'CNY', now=now)['usable'])

    def test_missing_cache_is_reported_as_unavailable(self):
        with tempfile.TemporaryDirectory() as folder:
            loaded = fx_rates.load(Path(folder) / 'absent.json')
            self.assertEqual(loaded['source'], 'unavailable')
            self.assertTrue(loaded['stale'])
            self.assertIsNone(loaded['rates'].get('CNY'))
            self.assertFalse(fx_rates.evaluate(loaded, 'CNY')['usable'])

            # A cache that cannot be read or parsed must degrade the same way:
            # the checkout page falls back to a labelled rate instead of a 500.
            corrupt = Path(folder) / 'corrupt.json'
            corrupt.write_text('{not json', encoding='utf-8')
            self.assertEqual(fx_rates.load(corrupt)['source'], 'unreadable snapshot')
            self.assertEqual(fx_rates.load(Path(folder))['source'], 'unavailable')


if __name__ == '__main__':
    unittest.main()
