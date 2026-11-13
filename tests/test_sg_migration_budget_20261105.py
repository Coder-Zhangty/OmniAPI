"""Pure budget and workload tests: no Docker, network, credentials or paid API."""
import copy,importlib.util,json,pathlib,tempfile,unittest
from unittest import mock
R=pathlib.Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('sg_budget',R/'ops/sg_migration_20261105_budget.py');b=importlib.util.module_from_spec(spec);spec.loader.exec_module(b)

def fixture():
 costs={'9':{'complete_prices_verified':True,'input_usd_per_million':0.08915569556301822,'output_usd_per_million':0.3566227822520729,'cache_read_usd_per_million':0.0017831139112603643},'14':{'complete_prices_verified':True,'input_usd_per_million':0.08908553696307404,'output_usd_per_million':0.3563421478522962,'cache_read_usd_per_million':0.001781710739261481}}
 routes={'enabled':True,'models':{b.MODEL:[9,14]},'costs':{b.MODEL:costs}}
 prices={'enabled':True,'cny_per_usd':'6.7351','models':{b.MODEL:{'retail':{'input':'0.22288924','output':'0.89155696'},'tiers':[]}}}
 registry={'enabled':True,'models':{b.MODEL:{'bindings':[{'channel_id':cid,'status':'verified','upstream_model':b.MODEL,'protocols':['openai']} for cid in [9,14]]}}}
 return {'binary_sha256':b.EXPECTED_BINARY,'options':{'OmniAPIRouting':json.dumps(routes),'OmniAPIPricing':json.dumps(prices),'OmniAPIModelRegistry':json.dumps(registry),'RetryTimes':'0','GroupRatio':'{"default":1}'},'channels':[{'id':cid,'status':1,'type':1,'models':b.MODEL,'group':'default','model_mapping':'{}','setting':'{"system_prompt":"synthetic bounded prefix"}','settings':'{}','param_override':json.dumps({'operations':[{'logic':'AND','path':'instructions','mode':'ensure_prefix','value':'fixture','conditions':[{'path':'request_path','mode':'prefix','value':'/v1/responses'}]}]})} for cid in [9,14]]}

class BudgetTests(unittest.TestCase):
 def test_two_routes_each_counted_even_if_failing(self):
  report=b.calculate(fixture())
  self.assertEqual(report['worst_supplier_cny'],'0.396444046717584469921486080')
  self.assertEqual(report['max_supplier_attempts'],10)
  self.assertEqual((report['personal_key_quota'],report['shared_key_quota']),(22086,14724))

 def test_retry_binary_and_tariff_changes_fail_closed(self):
  data=fixture();data['options']['RetryTimes']='1'
  with self.assertRaises(AssertionError):b.calculate(data)
  data=fixture();data['binary_sha256']='other'
  with self.assertRaises(AssertionError):b.calculate(data)
  data=fixture();route=json.loads(data['options']['OmniAPIRouting']);route['costs'][b.MODEL]['9']['input_usd_per_million']=100;data['options']['OmniAPIRouting']=json.dumps(route)
  with self.assertRaisesRegex(AssertionError,'exceeds CNY1'):b.calculate(data)

 def test_unknown_chat_body_override_refused(self):
  data=fixture();data['channels'][0]['param_override']='{"max_tokens":999999}'
  with self.assertRaises(AssertionError):b.calculate(data)
  data=fixture();value=json.loads(data['channels'][0]['param_override']);value['operations'][0]['conditions'][0]['value']='/v1/chat';data['channels'][0]['param_override']=json.dumps(value)
  with self.assertRaisesRegex(AssertionError,'rewrite not bounded'):b.calculate(data)

 def test_snapshot_change_is_observable(self):
  data=fixture();first=b.calculate(data)
  data['channels'][0]['setting']='{"system_prompt":"different valid bounded prefix"}'
  self.assertNotEqual(first['snapshot_sha256'],b.calculate(data)['snapshot_sha256'])

 def test_stream_cannot_enable_unbounded_text_continuation(self):
  body={'model':b.MODEL,'max_tokens':64,'stream':True,'messages':[{'role':'user','content':'Reply OK.'}]}
  with self.assertRaisesRegex(AssertionError,'disable auto text continuation'):b.validate_body(body,'/v1/chat/completions')
  body.update(tools=[{'type':'function','function':{'name':'ping'}}],tool_choice='none')
  self.assertTrue(b.validate_body(body,'/v1/chat/completions'))

 def test_larger_body_hosted_tool_and_output_rejected(self):
  body={'model':b.MODEL,'max_tokens':129,'stream':False,'messages':[{'role':'user','content':'OK'}]}
  with self.assertRaises(AssertionError):b.validate_body(body,'/v1/chat/completions')
  body['max_tokens']=128;body['messages'][0]['content']='x'*2048
  with self.assertRaises(AssertionError):b.validate_body(body,'/v1/chat/completions')
  body['messages'][0]['content']='OK';body.update(stream=True,tools=[{'type':'web_search'}],tool_choice='auto')
  with self.assertRaisesRegex(AssertionError,'hosted tools'):b.validate_body(body,'/v1/chat/completions')

class PersistentBudgetTests(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory(dir=R/'runtime');self.addCleanup(self.temp.cleanup)
  self.path=pathlib.Path(self.temp.name)/'acceptance-budget.json'
  self.certificate=b.calculate(fixture())
  self.body={'model':b.MODEL,'max_tokens':64,'stream':False,'messages':[{'role':'user','content':'Reply OK.'}]}

 def test_prepared_receipt_alone_blocks_replay(self):
  b.claim_workload(self.path,self.certificate)
  with self.assertRaisesRegex(AssertionError,'automatic replay forbidden'):b.claim_workload(self.path,self.certificate)
  self.assertEqual(json.loads(self.path.read_text())['calls_started'],0)

 def test_failure_after_reservation_preserves_total_across_reloads(self):
  value=b.claim_workload(self.path,self.certificate)
  self.assertEqual(b.reserve_call(self.path,value,self.body,'/v1/chat/completions'),1)
  # A killed transport has no completion record. Its persisted reservation counts.
  recovered=json.loads(self.path.read_text())
  self.assertEqual(recovered['calls_started'],1)
  self.assertEqual(recovered['attempts'][0]['state'],'reserved_before_transport')
  with self.assertRaisesRegex(AssertionError,'automatic replay forbidden'):b.claim_workload(self.path,self.certificate)
  self.assertEqual(json.loads(self.path.read_text())['calls_started'],1)

 def test_five_total_calls_are_a_durable_ceiling(self):
  value=b.claim_workload(self.path,self.certificate)
  for expected in range(1,6):
   # Read the persisted object afresh, as another process would see it.
   value=json.loads(self.path.read_text())
   self.assertEqual(b.reserve_call(self.path,value,self.body,'/v1/chat/completions'),expected)
  with self.assertRaisesRegex(AssertionError,'budget exhausted'):b.reserve_call(self.path,value,self.body,'/v1/chat/completions')
  self.assertEqual(json.loads(self.path.read_text())['calls_started'],5)

 def test_failed_fsync_never_authorizes_transport(self):
  value=b.claim_workload(self.path,self.certificate)
  with mock.patch.object(b.os,'fsync',side_effect=OSError('disk unavailable')):
   with self.assertRaises(OSError):b.reserve_call(self.path,value,self.body,'/v1/chat/completions')
  self.assertEqual(value['calls_started'],0)
  self.assertEqual(json.loads(self.path.read_text())['calls_started'],0)

 def test_failed_rename_never_overwrites_previous_committed_total(self):
  value=b.claim_workload(self.path,self.certificate)
  b.reserve_call(self.path,value,self.body,'/v1/chat/completions')
  with mock.patch.object(b.os,'replace',side_effect=OSError('rename unavailable')):
   with self.assertRaises(OSError):b.reserve_call(self.path,value,self.body,'/v1/chat/completions')
  self.assertEqual(value['calls_started'],1)
  self.assertEqual(json.loads(self.path.read_text())['calls_started'],1)

if __name__=='__main__':unittest.main()
