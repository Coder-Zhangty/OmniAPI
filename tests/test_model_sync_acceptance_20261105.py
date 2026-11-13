import json,unittest
from ops.model_sync_acceptance_20261105 import accept_function_protocol

class AcceptanceTests(unittest.TestCase):
 def test_responses_requires_real_function_result_stream_usage_and_finite_cap(self):
  sent=[]
  def send(path,body):
   sent.append(body)
   if len(sent)==1:return json.dumps({'model':'gpt-synthetic','usage':{'input_tokens':10,'output_tokens':3},'output':[{'type':'function_call','name':'ping','call_id':'fc_synthetic','arguments':'{"message":"OK"}'}]}).encode()
   if len(sent)==2:
    self.assertEqual(body['input'][-1]['call_id'],'fc_synthetic');self.assertEqual(body['input'][-1]['output'],'SYNTHETIC_RESULT')
    return b'data: {"type":"response.output_text.delta","delta":"SYNTHETIC_RESULT"}\n\ndata: {"type":"response.completed","response":{"model":"gpt-synthetic","usage":{"input_tokens":20,"output_tokens":3}}}\n\n'
   self.assertEqual(body['max_output_tokens'],16)
   return b'{"model":"gpt-synthetic","usage":{"input_tokens":10,"output_tokens":16},"output":[]}'
  row=accept_function_protocol('gpt-synthetic','/v1/responses',send,nonce='SYNTHETIC_RESULT')
  self.assertTrue(row['passed']);self.assertTrue(row['small_output_cap_observed']);self.assertEqual(len(sent),3)
 def test_wrong_identity_never_becomes_an_accepted_new_model(self):
  def send(*_):return b'{"model":"different","usage":{"input_tokens":1}}'
  with self.assertRaisesRegex(ValueError,'identity'):accept_function_protocol('gpt-synthetic','/v1/responses',send)
 def test_no_stream_terminal_is_quarantined(self):
  calls=[]
  def send(*_):
   calls.append(1)
   if len(calls)==1:return json.dumps({'model':'gpt-synthetic','usage':{'input_tokens':1},'output':[{'type':'function_call','name':'ping','call_id':'fc','arguments':'{"message":"OK"}'}]}).encode()
   return b'data: {"type":"response.output_text.delta","delta":"SYNTHETIC_RESULT"}\n\n'
  with self.assertRaisesRegex(ValueError,'terminal'):accept_function_protocol('gpt-synthetic','/v1/responses',send,nonce='SYNTHETIC_RESULT')
  self.assertEqual(len(calls),2)

if __name__=='__main__':unittest.main()
