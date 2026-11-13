"""Behaviour checks for the public-only index, independent of live accounting."""
import copy
from datetime import date, timedelta
import hashlib
import json
from pathlib import Path
import sys
import unittest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'ops/rankings'))
import snapshot as s


class RankingsDisplayTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.config = json.loads((ROOT / 'config/rankings-display.json').read_text(encoding='utf8'))
        cls.raw = {'recovery': True, 'lag_seconds': 2, 'as_of': s.midnight('2026-10-16') + 13 * 3600,
                   'rows': []}
        # Deliberate missing real day, >20 models, unknown model, tie and primes.
        for day in range(20):
            if day == 9:
                continue
            for i in range(25):
                cls.raw['rows'].append([s.midnight(date(2026, 9, 26) + timedelta(days=day)) + 13 * 3600,
                                        f'gpt-fixture-{i:02}', 101 + i * 7 + day])
        cls.history = s.freeze_history(cls.raw, cls.config, {})

    def test_immutable_and_deterministic_history(self):
        self.assertEqual(self.history, s.freeze_history(self.raw, self.config, {}))
        future = copy.deepcopy(self.raw)
        future['as_of'] += 300
        future['rows'].append([future['as_of'], 'new-unknown-model', 103])
        s.generate(future, self.config, {}, self.history)
        self.assertEqual(self.history, s.freeze_history(self.raw, self.config, {}))
        altered = copy.deepcopy(self.history)
        altered['rows'][0][2] += 1
        with self.assertRaises(AssertionError):
            s.generate(self.raw, self.config, {}, altered)

    def test_calendar_and_verified_lifetimes(self):
        self.assertEqual(s.calendar_factor(date(2025,10,5),self.config),.6)
        self.assertEqual(s.calendar_factor(date(2025,10,11),self.config),1)
        self.assertEqual(s.calendar_factor(date(2026,2,14),self.config),1)
        self.assertEqual(s.calendar_factor(date(2026,2,17),self.config),.6)
        self.assertEqual(s.calendar_factor(date(2026,1,11),self.config),.8)
        first = min(s.local_day(r[0]) for r in self.history['rows'])
        last = max(s.local_day(r[0]) for r in self.history['rows'])
        self.assertEqual(str(first),'2025-10-05')
        self.assertEqual(str(last),'2026-09-25')
        for ts, model, value in self.history['rows']:
            day=str(s.local_day(ts))
            if 'haiku' in model:self.assertGreaterEqual(day,'2025-10-16')
            if 'opus' in model:self.assertLess(day,'2026-08-05')
            if model=='DeepSeek-V3.2':self.assertGreaterEqual(day,'2025-12-02')
            self.assertNotIn('fixture',model)

    def test_all_period_totals_sha_and_no_truncation(self):
        manifest,files,audit=s.generate(self.raw,self.config,{},self.history)
        self.assertEqual(set(files),{'today','week','month','year','all'})
        for period,content in files.items():
            obj=json.loads(content)
            self.assertEqual(obj['version'],manifest['version'])
            self.assertEqual(hashlib.sha256(content).hexdigest(),manifest['periods'][period]['sha256'])
            s.validate_snapshot(obj['data'])
        all_data=json.loads(files['all'])['data']
        self.assertEqual(all_data['total_tokens'],sum(r[2] for r in self.history['rows'])+sum(r[2] for r in self.raw['rows'])*200)
        self.assertGreater(all_data['total_tokens'],sum(m['total_tokens'] for m in all_data['models']))
        self.assertIsNone(all_data['models'][0]['growth_pct'])
        self.assertEqual(audit['scaled_real_tokens'],audit['raw_tokens']*200)

    def test_real_dates_exactly_scaled_once_and_gaps_not_filled(self):
        _,files,_=s.generate(self.raw,self.config,{},self.history)
        month=json.loads(files['month'])['data']
        chart=month['models_history']
        names={m['name'] for m in chart['models'] if m['name']!='其他'}
        from collections import Counter
        expected,actual=Counter(),Counter()
        for ts,name,tokens in self.raw['rows']:
            if ts>=__import__('datetime').datetime.fromisoformat(month['window']['start']).timestamp():
                expected[str(s.local_day(ts)),name if name in names else '其他']+=tokens*200
        first_real='2026-09-26'
        for point in chart['points']:
            day=point['ts'][:10]
            if day>=first_real:actual[day,point['model']]+=point['tokens']
        self.assertEqual(+actual,+expected)
        missing='2026-10-05'
        self.assertEqual(sum(v for (d,m),v in actual.items() if d==missing),0)

    def test_rolling_boundary_beijing_and_cross_year(self):
        ts=s.midnight('2026-01-01')+12*3600
        history={'start':'2025-10-05','end_exclusive':'2026-01-01'}
        rows=[[ts-86400,'gpt-a',11*200],[ts-86400+1,'gpt-a',13*200],[ts,'gpt-a',17*200]]
        data=s.make_snapshot(rows,{},'today',ts,history)
        # End is exclusive as_of+1, so exactly 24h old row is outside.
        self.assertEqual(data['total_tokens'],6000)
        self.assertEqual(data['models'][0]['growth_pct'],(6000-2200)/2200*100)
        self.assertTrue(any(p['label'].startswith('12-31') for p in data['models_history']['points']))
        s.validate_snapshot(data)

    def test_fail_closed_on_primary_lag_empty_and_unsafe_integers(self):
        for key,value in [('recovery',False),('lag_seconds',121),('rows',[])]:
            raw=copy.deepcopy(self.raw);raw[key]=value
            with self.assertRaises(AssertionError):s.generate(raw,self.config,{},self.history)
        raw=copy.deepcopy(self.raw);raw['rows'][0][2]=2**53
        with self.assertRaises(AssertionError):s.generate(raw,self.config,{},self.history)


if __name__=='__main__':unittest.main()
