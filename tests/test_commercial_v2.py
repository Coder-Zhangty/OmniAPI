import copy
import datetime as dt
import sys
import unittest
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
from commercial_v2 import compile_snapshot


class FullCostTest(unittest.TestCase):
    def fixture(self):
        price = dict(input='.001', output='.002', cache_read='.0001', cache_write='.001', cache_write_1h='.002')
        route = dict(channel_id=1, supplier='0-0', accepted=True, mapping_verified=True, flat_prices_verified=True, prices=price, tool_fees={})
        backup = copy.deepcopy(route); backup.update(channel_id=5, supplier='H API')
        return dict(currency='USD', price_basis='actual_sale', source='https://api.0-0.pro/en',
                    verified_at=dt.datetime.now(dt.timezone.utc).isoformat(),
                    models={'tiny':dict(routes=[route, backup], text_only=True)})

    def test_exact_precision_and_all_cache_dimensions(self):
        result = compile_snapshot(self.fixture())['models']['tiny']
        self.assertEqual(result['retail']['input'], '0.0025')
        self.assertIn('cr * 0.00025', result['billing_expr'])
        self.assertTrue(result['eligible'])

    def test_unverified_fee_or_mapping_blocks_sale(self):
        f = self.fixture(); f['models']['tiny']['routes'][1]['mapping_verified'] = False
        self.assertFalse(compile_snapshot(f)['models'])
        f = self.fixture(); del f['models']['tiny']['routes'][0]['prices']['cache_write']
        self.assertFalse(compile_snapshot(f)['models'])

    def test_expensive_backup_excluded_from_subscription(self):
        f = self.fixture(); f['models']['tiny']['routes'][1]['prices']['output'] = '.003'
        self.assertFalse(compile_snapshot(f)['models']['tiny']['eligible'])

    def test_loss_making_backup_remains_available_for_wallet_routing(self):
        f=self.fixture(); f['models']['tiny']['routes'][1]['prices']['output']='1'
        result=compile_snapshot(f)['models']['tiny']
        self.assertTrue(result['loss_making_backup'])
        self.assertFalse(result['eligible'])

    def test_more_than_three_verified_routes(self):
        f=self.fixture()
        for channel in (6,7):
            route=copy.deepcopy(f['models']['tiny']['routes'][1]);route['channel_id']=channel
            f['models']['tiny']['routes'].append(route)
        self.assertEqual(len(compile_snapshot(f)['models']['tiny']['routes']),4)

    def test_list_price_is_never_accepted(self):
        f = self.fixture(); f['price_basis'] = 'list_price'
        with self.assertRaises(ValueError): compile_snapshot(f)

class UnsupportedFeesTest(unittest.TestCase):
    fixture = FullCostTest.fixture
    def test_media_requires_full_fee_audit(self):
        data = self.fixture(); data['models']['tiny']['text_only'] = False
        self.assertIn('tiny', compile_snapshot(data)['waiting'])
    def test_extra_price_dimension_cannot_be_silently_dropped(self):
        data = self.fixture(); data['models']['tiny']['routes'][0]['prices']['image'] = '1'
        self.assertIn('tiny', compile_snapshot(data)['waiting'])

    def test_reservation_cache_ratio_must_be_supported(self):
        data=self.fixture()
        for route in data['models']['tiny']['routes']: route['prices']['cache_write']='.02'
        self.assertIn('tiny',compile_snapshot(data)['waiting'])

class VerificationFlagTest(unittest.TestCase):
    fixture = FullCostTest.fixture
    def test_missing_flat_cost_verification_blocks_sale(self):
        f=self.fixture(); del f['models']['tiny']['routes'][0]['flat_prices_verified']
        self.assertIn('tiny', compile_snapshot(f)['waiting'])
    def test_string_flag_does_not_count_as_acceptance(self):
        f=self.fixture(); f['models']['tiny']['routes'][0]['accepted']='false'
        self.assertIn('tiny', compile_snapshot(f)['waiting'])

class InstallerStagingTest(unittest.TestCase):
    fixture = FullCostTest.fixture
    def test_staging_closes_gates_and_preserves_existing_price_records(self):
        import json, tempfile
        from unittest.mock import patch
        import install_commercial_v2 as installer
        class API:
            def __init__(self):
                self.values={'billing_setting.billing_expr':json.dumps({'old':'original-expression'}),'billing_setting.billing_mode':'{}','OmniAPICommercial':json.dumps({'sales_enabled':True,'models':{'old':{'eligible':True}}}),'OmniAPIRouting':json.dumps({'continuation_enabled':True})}; self.writes=[]; self.disabled=[]
            def login(self,credentials): pass
            def options(self): return dict(self.values)
            def option(self,key,value): self.values[key]=value; self.writes.append((key,json.loads(value)))
            def ok(self,path,body=None,method=None):
                if path=='/api/subscription/admin/plans': return [{'plan':{'id':3,'enabled':True}}]
                self.disabled.append((path,body,method))
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'snapshot.json'; path.write_text(json.dumps(self.fixture()))
            api=API()
            with patch.object(installer,'ROOT',Path(tmp)),patch.object(installer,'credentials',return_value={}): result=installer.install(path,apply=True,api=api)
            self.assertEqual(result['status'],'STAGED')
            self.assertEqual(api.writes[0][0],'OmniAPICommercial'); self.assertFalse(api.writes[0][1]['sales_enabled'])
            self.assertEqual(api.writes[1][0],'OmniAPIRouting'); self.assertFalse(api.writes[1][1]['continuation_enabled'])
            self.assertFalse(json.loads(api.values['OmniAPICommercial'])['models']['old']['eligible'])
            self.assertEqual(json.loads(api.values['billing_setting.billing_expr'])['old'],'original-expression')
            self.assertEqual(api.disabled,[('/api/subscription/admin/plans/3',{'enabled':False},'PATCH')])
