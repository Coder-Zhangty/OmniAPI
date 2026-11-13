import copy,unittest
from ops.model_sync_core_20261105 import candidate_digest,quote_prices,finite_text_bound,text_candidate,prepare_accepted_instruction_override

class ModelSyncCoreTests(unittest.TestCase):
 def tariff(self):
  base={'input_price':1e-7,'output_price':5e-7,'cache_read_price':1e-8,'cache_write_price':1.25e-7,'cache_write_1h_price':2e-7}
  high={k:v*5 for k,v in base.items()}
  return {'billing_mode':'token',**base,'intervals':[{'min_tokens':0,'max_tokens':100000,**base},{'min_tokens':100000,'max_tokens':None,**high}]}
 def test_actual_cny_tariff_multiplied_once_and_all_context_tiers_protected(self):
  retail,cost,expression=quote_prices(self.tariff(),'.8',6696400,596018)
  self.assertAlmostEqual(cost['input_usd_per_million'],.08/6.6964)
  self.assertAlmostEqual(cost['tiers'][1]['prices']['output_usd_per_million'],2/6.6964)
  self.assertEqual(cost['tiers'][1]['min_tokens'],100001)
  self.assertEqual(retail['output'],'0.50110558')
  self.assertIn('cc1h',expression);self.assertFalse(cost['complete_prices_verified'])
 def test_unknown_dimension_never_defaults_to_free(self):
  tariff=self.tariff();del tariff['intervals'][1]['cache_write_1h_price']
  with self.assertRaisesRegex(ValueError,'missing'):quote_prices(tariff,'.8',6696400,596018)
 def test_unchanged_directory_does_not_trigger_paid_retest(self):
  model={'id':'gpt-synthetic','owned_by':'openai','created':123};tariff=self.tariff()
  first=candidate_digest(13,'fingerprint',model,tariff,84,'.16','v1')
  model['created']=999;self.assertEqual(first,candidate_digest(13,'fingerprint',model,tariff,84,'.16','v1'))
  self.assertNotEqual(first,candidate_digest(13,'rotated',model,tariff,84,'.16','v1'))
  changed=copy.deepcopy(tariff);changed['output_price']=1e-6
  self.assertNotEqual(first,candidate_digest(13,'fingerprint',model,changed,84,'.16','v1'))
 def test_finite_scope_has_no_unknown_media_or_unlimited_values(self):
  retail,_,_=quote_prices(self.tariff(),'.8',6696400,596018)
  bound=finite_text_bound(retail,65536,'a'*64,1)
  self.assertGreater(bound['max_quota'],0);self.assertTrue(bound['request_based_reservation'])
  self.assertTrue(text_candidate('claude-synthetic',self.tariff()));self.assertFalse(text_candidate('gpt-image-synthetic',self.tariff()))
  with self.assertRaises(ValueError):finite_text_bound(retail,0,'a'*64,1)
 def test_ambiguous_or_missing_long_context_quote_stays_isolated(self):
  tariff=self.tariff();tariff['intervals'][1]['min_tokens']=90000
  with self.assertRaisesRegex(ValueError,'overlap'):quote_prices(tariff,'.8',6696400,596018)
 def test_only_inspected_instruction_guard_is_applied_to_actual_probe(self):
  import json
  common=[{'path':'request_path','mode':'prefix','value':'/v1/responses'},{'path':'input','mode':'contains','value':'','pass_missing_key':False}]
  ops=[{'path':'instructions','mode':'set','value':'','logic':'AND','conditions':common+[{'path':'instructions','mode':'full','value':None,'pass_missing_key':True}]},{'path':'instructions','mode':'ensure_prefix','value':'SYNTHETIC_PREFIX\n','logic':'AND','conditions':common}]
  raw=json.dumps({'operations':ops});body={'model':'gpt-synthetic','input':['synthetic'],'max_output_tokens':16}
  result=prepare_accepted_instruction_override(raw,'/v1/responses',body);self.assertEqual(result['instructions'],'SYNTHETIC_PREFIX\n');self.assertNotIn('instructions',body)
  self.assertEqual(prepare_accepted_instruction_override(raw,'/v1/responses',result),result)
  self.assertEqual(prepare_accepted_instruction_override(raw,'/v1/messages',body),body)
  ops[0]['path']='max_output_tokens'
  with self.assertRaisesRegex(ValueError,'rewriting'):prepare_accepted_instruction_override(json.dumps({'operations':ops}),'/v1/responses',body)

if __name__=='__main__':unittest.main()
