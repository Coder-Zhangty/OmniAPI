import copy,json,sys,time,unittest
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'ops'))
from model_sync_core_20261105 import canonical_json,digest
from model_sync_publication_20261105 import build_candidate,commit_preview,OPTION_KEYS

def fixture():
 now=int(time.time());tariff={'billing_mode':'token','input_price':1e-7,'output_price':5e-7,'cache_read_price':1e-8,'cache_write_price':1.25e-7,'cache_write_1h_price':2e-7}
 paths=['/v1/chat/completions','/v1/messages'];proof=[dict(model='claude-synthetic-new',path=path,passed=True,function_arguments_verified=True,multi_turn_result_consumed=True,stream_terminal=True,billing_usage_verified=True,small_output_cap_observed=True) for path in paths]
 candidate=dict(model='claude-synthetic-new',channel_id=12,tariff=tariff,multiplier='.8',paths=paths,acceptance=proof,verified_at=now,quote_source='https://synthetic.invalid/quote',key_fingerprint='1'*64,base_url='https://synthetic.invalid',header_override_hash=digest(''),model_mapping_hash=digest(''),param_override_hash=digest(''))
 options={'OmniAPIPricing':canonical_json({'models':{'old':{'retail':{'input':'1'}}}}),'OmniAPIRouting':canonical_json({'models':{'old':[1]},'costs':{'old':{}}}),'OmniAPIModelRegistry':canonical_json({'models':{'old':{'aliases':['old-alias'],'bindings':[]}}})}
 profile=dict(id='old-profile',models='{"old":[1]}',capability_matrix=canonical_json({'bounds':{'old':{'1':{}}}}),audit_expires_at=now+86400,fx_micros=6696400,worst_cost_ppm=596018,evidence='{}',price_snapshot='SYNTHETIC-UNCHANGED',payment_fee_ppm=15000)
 return options,profile,candidate

class PublicationTests(unittest.TestCase):
 def test_additive_scope_preserves_all_existing_prices_routes_and_financials(self):
  options,profile,candidate=fixture();before=copy.deepcopy((options,profile,candidate));plan=build_candidate(options,profile,candidate)
  self.assertEqual(before,(options,profile,candidate))
  for key in ['OmniAPIPricing','OmniAPIRouting','OmniAPIModelRegistry']:
   self.assertEqual(json.loads(options[key])['models']['old'],json.loads(plan[{'OmniAPIPricing':'pricing','OmniAPIRouting':'routing','OmniAPIModelRegistry':'registry'}[key]])['models']['old'])
  self.assertEqual(plan['profile']['price_snapshot'],profile['price_snapshot']);self.assertEqual(plan['profile']['audit_expires_at'],profile['audit_expires_at'])
  model=json.loads(plan['registry'])['models'][candidate['model']];self.assertEqual(model['bindings'][0]['supports'],['tools'])
 def test_no_inferred_missing_price_or_skipped_protocol(self):
  options,profile,candidate=fixture();del candidate['tariff']['cache_write_1h_price']
  with self.assertRaises(ValueError):build_candidate(options,profile,candidate)
  _,_,candidate=fixture();candidate['acceptance'][1]['small_output_cap_observed']=False
  with self.assertRaises(AssertionError):build_candidate(options,profile,candidate)
 def test_existing_model_never_repriced_and_expiry_never_faked(self):
  options,profile,candidate=fixture();candidate['model']='old'
  with self.assertRaisesRegex(ValueError,'never reprice'):build_candidate(options,profile,candidate)
  _,_,candidate=fixture();profile['audit_expires_at']=int(time.time())+300
  with self.assertRaisesRegex(ValueError,'renewal'):build_candidate(options,profile,candidate)
 def test_transaction_guards_lease_options_route_and_never_mutates_purchased_records(self):
  options,profile,candidate=fixture();plan=build_candidate(options,profile,candidate);values={key:'SYNTHETIC' for key in OPTION_KEYS};version='daily-model-'+digest(canonical_json(candidate))[:24]
  preview={'values':values,'profile':plan['profile'],'release':{'profile':plan['profile'],'version':version,'evidence_hash':plan['evidence_hash'],'approved_by':1},'expected_hashes':{key:'a'*64 for key in OPTION_KEYS},'financial_snapshots_changed':False};statements=[]
  quote=lambda v:"'"+str(v).replace("'","''")+"'"
  self.assertEqual(commit_preview(preview,{'profile_id':'old-profile','acceptance_id':'SYNTHETIC'},candidate,statements.append,quote,'lease-owner'),version)
  sql=statements[0]
  for guard in ['pg_advisory_xact_lock','FOR UPDATE','job lease expired','accepted channel changed','publication options changed','header_override','active release changed']:self.assertIn(guard,sql)
  for forbidden in ['UPDATE users','UPDATE omni_subscriptions','UPDATE omni_shared_periods','UPDATE omni_reservations','UPDATE omni_commerce_purchases','DELETE FROM']:self.assertNotIn(forbidden,sql)

if __name__=='__main__':unittest.main()
