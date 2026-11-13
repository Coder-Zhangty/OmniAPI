import copy
import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'ops'))
from remediation_policy_20261016 import build_candidate,KEYS


class RemediationPolicy(unittest.TestCase):
    def state(self):
        registry={'enabled':True,'version':'old','models':{n:{'version':n,'capability':'text','aliases':[],'bindings':[{'channel_id':c,'upstream_model':n,'status':'verified','protocols':['openai'],'supports':['tools']} for c in ids]} for n,ids in {'old':[1,7],'gone':[7]}.items()}}
        pricing={'enabled':True,'version':'old','cny_per_usd':'7','models':{n:{'retail':{'input':'5','output':'10','cache_read':'1','cache_write':'5','cache_write_1h':'5'},'billing_expr':'old-expression','restrictions':[]} for n in ['old','gone']},'waiting':{}}
        routing={'enabled':True,'models':{'old':[1,7],'gone':[7]},'costs':{'old':{'1':{'input_usd_per_million':1,'output_usd_per_million':2},'7':{'input_usd_per_million':2,'output_usd_per_million':4}},'gone':{'7':{}}},'fallback_only_channels':[7],'primary_exceptions':['gone']}
        return {k:json.dumps(v) for k,v in zip(KEYS,[registry,pricing,routing,{'old':'old-expression','gone':'gone-expression'},{'old':'tiered_expr'}])}

    def row(self,name='old'):
        return {'model':name,'label':'gpt','passed':True,'context_verified':32768,'protocol':'openai','capabilities':['text','tools'],'tariff':{'multiplier':.16,'pricing':{'input_price':1e-6,'output_price':5e-6,'cache_read_price':1e-7}}}

    def test_replaces_only_retired_position_and_preserves_existing_prices(self):
        raw=self.state();before=copy.deepcopy(raw)
        candidate=build_candidate(raw,[self.row()],[],{'gpt':11})
        routing=json.loads(candidate['values'][KEYS[2]]);pricing=json.loads(candidate['values'][KEYS[1]])
        self.assertEqual(raw,before);self.assertEqual(routing['models']['old'],[1,11]);self.assertNotIn('gone',routing['models'])
        self.assertEqual(pricing['models']['old']['retail']['input'],'5.00000000');self.assertIn('gone',pricing['waiting'])
        self.assertEqual(json.loads(candidate['values'][KEYS[3]])['gone'],'gone-expression')
        self.assertNotIn('7',routing['costs']['old']);self.assertEqual(routing['fallback_only_channels'],[])

    def test_new_model_is_primary_and_unverified_capabilities_are_not_added(self):
        candidate=build_candidate(self.state(),[self.row('new-model')],[],{'gpt':11})
        registry=json.loads(candidate['values'][KEYS[0]]);pricing=json.loads(candidate['values'][KEYS[1]])
        self.assertEqual(json.loads(candidate['values'][KEYS[2]])['models']['new-model'],[11])
        self.assertNotIn('vision',registry['models']['new-model']['bindings'][0]['supports'])
        self.assertNotIn('cache_write',registry['models']['new-model']['bindings'][0]['supports'])
        self.assertNotIn('input_images_token_priced',pricing['models']['new-model']['restrictions'])
        for row in candidate['floor_audit']:
            if row['gross_margin'] is not None:self.assertGreaterEqual(float(row['gross_margin']),.60-1e-12)

    def test_unverified_stream_does_not_activate_a_binding(self):
        row=self.row();row.pop('context_verified')
        candidate=build_candidate(self.state(),[row],[],{'gpt':11})
        self.assertEqual(json.loads(candidate['values'][KEYS[2]])['models']['old'],[1])
        self.assertTrue(any(v['reason']=='stream_context_acceptance_pending' for v in candidate['blocked']))

    def test_image_quote_exposes_only_real_accepted_combinations(self):
        image={'model':'gpt-image-2.5','label':'image','passed':True,'maximum_cost_cny':.05,'options':[{'size':'2048x2048','quality':'low'},{'size':'4096x4096','quality':'low'}]}
        candidate=build_candidate(self.state(),[],[image],{'image':11})
        quote=json.loads(candidate['values'][KEYS[1]])['models']['gpt-image-2.5']
        self.assertEqual(quote['image_specs']['sizes'],['2048x2048','4096x4096'])
        self.assertNotIn('1024x1024',quote['image_specs']['sizes'])
        self.assertEqual(quote['image_specs']['qualities'],['low'])
        self.assertGreaterEqual(float(quote['image_price_cny']),.125)


if __name__=='__main__':unittest.main()
