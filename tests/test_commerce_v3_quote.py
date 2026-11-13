import copy,datetime as dt,unittest
from scripts.commerce_v3_quote import quote,TERMS

class CommerceQuoteTest(unittest.TestCase):
    def fixture(self):
        evidence={name:{'verified':True,'source':'synthetic-only://'+name} for name in ('payment','retries','free_benefits','fx')}
        dims={name:{'verified':True,'procurement':'0.4','retail':'1'} for name in ('input','output','cache_read','cache_write','cache_write_1h')}
        coverage={name:dict(verified=True,source='synthetic-only://'+name,supported=name in ('context','cache')) for name in ('context','cache','images','audio','tools')}
        route=dict(channel_id=7,accepted=True,mapping_verified=True,source='synthetic-only://supplier',coverage=coverage,context_tiers=[dict(min_tokens=0,max_tokens=None,dimensions=dims)])
        return dict(profile_id='SYNTHETIC-NEVER-SELL',verified_at=dt.datetime.now(dt.timezone.utc).isoformat(),fx_micros=7000000,payment_fee_ppm=10000,risk_reserve_ppm=10000,fixed_fee_cents=1,evidence=evidence,models={'synthetic-model':{'routes':[route]}})
    def test_cost_bound_prices_and_closed_output(self):
        result=quote(self.fixture());self.assertIs(result['sales_enabled'],False);self.assertIs(result['approval_required'],True);self.assertEqual(result['profile']['worst_cost_ppm'],400000)
        self.assertEqual(result['plans'][0]['prices'],{1:1900,3:5500,6:9900,12:18900})
        for i,p in enumerate(result['plans']):
            self.assertEqual(p['five_enabled'],i<2)
            for months,(_,windows) in TERMS.items():
                paid=p['prices'][months];max_cost=p['week_cents']*windows*400000/1000000
                self.assertLessEqual(max_cost,paid*.78-1)
        self.assertFalse(result['profit_guarantee'])
    def test_unknown_cache_media_or_route_blocks(self):
        for mutate in (lambda r:r['context_tiers'][0]['dimensions'].pop('cache_write_1h'),lambda r:r['coverage']['images'].update(supported=True),lambda r:r.update(accepted=False),lambda r:r['coverage']['tools'].update(verified=False)):
            s=self.fixture();mutate(s['models']['synthetic-model']['routes'][0])
            with self.assertRaises((ValueError,KeyError)):quote(s)
    def test_existing_free_reset_cost_is_included(self):
        result=quote(self.fixture())
        for plan in result['plans']:
            for months,(_,windows) in TERMS.items():
                granted=(plan['prices'][months]*780000//1000000-1)*1000000//400000
                natural=plan['week_cents']*windows
                free=plan['week_cents']*months+2*plan['five_cents']*months
                self.assertLessEqual(natural+free,granted)
    def test_tier_gaps_and_stale_sources_block(self):
        s=self.fixture();s['models']['synthetic-model']['routes'][0]['context_tiers'][0]['min_tokens']=10
        with self.assertRaises(ValueError):quote(s)
        s=self.fixture();s['verified_at']=(dt.datetime.now(dt.timezone.utc)-dt.timedelta(days=8)).isoformat()
        with self.assertRaises(ValueError):quote(s)
    def test_backup_cost_controls_grant(self):
        s=self.fixture();base=quote(s);backup=copy.deepcopy(s['models']['synthetic-model']['routes'][0]);backup['channel_id']=8;backup['context_tiers'][0]['dimensions']['cache_read']['procurement']='0.9';s['models']['synthetic-model']['routes'].append(backup);result=quote(s)
        self.assertEqual(result['profile']['worst_cost_ppm'],900000);self.assertLess(result['plans'][0]['week_cents'],base['plans'][0]['week_cents'])
        s['evidence']['free_benefits']['verified']=False
        with self.assertRaises(ValueError):quote(s)

if __name__=='__main__':unittest.main()
