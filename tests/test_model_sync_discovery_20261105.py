import json,sys,unittest
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'ops'))
import model_sync_worker_20261105 as worker

class DiscoveryTests(unittest.TestCase):
 def fixture(self):
  channel={'id':12,'key':'SYNTHETIC-ONLY','base_url':'https://synthetic.invalid','header_override':'','model_mapping':'','param_override':''}
  rule={'channel_id':12,'group_id':69,'multiplier':'0.8','prefixes':['claude-'],'quote_source':'https://synthetic.invalid/quotes'}
  tariff={'billing_mode':'token','input_price':1e-7,'output_price':5e-7,'cache_read_price':1e-8,'cache_write_price':1.25e-7,'cache_write_1h_price':2e-7}
  billing={'billing_scope':'token','peak_rate_enabled':False,'group_rate_multiplier':.8,'resolved_rate_multiplier':.8,'effective_rate_multiplier':.8}
  return channel,rule,tariff,billing
 def test_unchanged_directory_only_discovers_once_and_never_spends(self):
  channel,rule,tariff,billing=self.fixture();records={};stored=[];requests=[]
  def read(url,*args,**kwargs):
   requests.append(url)
   if url.endswith('/quotes'):body={'data':{'groups':[{'id':69,'rate_multiplier':.8,'models':[{'name':'claude-synthetic-new','pricing':tariff}]}]}}
   elif url.endswith('/billing'):body=billing
   else:body={'data':[{'id':'claude-synthetic-new','created':len(requests)}]}
   return json.dumps(body).encode()
  def sql(query):
   if query.startswith('SELECT state'):return next((value for key,value in records.items() if key in query),'')
   return '{"models":{}}'
  def store(row,state):stored.append((row,state));records[row['fingerprint']]=state
  with patch.object(worker,'read_url',side_effect=read),patch.object(worker,'sql',side_effect=sql),patch.object(worker,'store',side_effect=store),patch.object(worker,'reserve_call') as spend:
   profile={'models':'{}','fx_micros':6696400,'worst_cost_ppm':596018}
   worker.discover({12:channel},[rule],profile);worker.discover({12:channel},[rule],profile)
   self.assertEqual(len(stored),1);self.assertEqual(stored[0][1],'candidate');spend.assert_not_called()
   self.assertNotIn('SYNTHETIC-ONLY',json.dumps(stored))
 def test_actual_key_multiplier_drift_and_peak_mode_are_rejected(self):
  channel,rule,_,billing=self.fixture();billing['effective_rate_multiplier']=1
  with patch.object(worker,'read_url',return_value=json.dumps(billing).encode()):
   with self.assertRaisesRegex(ValueError,'multiplier'):worker.billing_evidence(channel,rule)
  billing['effective_rate_multiplier']=.8;billing['peak_rate_enabled']=True
  with patch.object(worker,'read_url',return_value=json.dumps(billing).encode()):
   with self.assertRaisesRegex(ValueError,'billing mode'):worker.billing_evidence(channel,rule)
 def test_route_transform_changes_invalidate_acceptance(self):
  channel,_,_,_=self.fixture();before=worker.route_fingerprint(channel);channel['param_override']='{"max_tokens":0}'
  self.assertNotEqual(before,worker.route_fingerprint(channel))

if __name__=='__main__':unittest.main()
