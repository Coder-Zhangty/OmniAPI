import unittest
from scripts.commerce_acceptance_worklist import tasks_for_route

class CapabilityEvidenceTest(unittest.TestCase):
    def route(self):
        return {'channel_id':7,'already_open_capabilities':['tools','vision','web_search','cache_write_1h'],'basic_transport_passed':True,'protocols':['openai'],'configured_context_ceiling':1000000,'basic_check':{'width':2048,'height':2048}}
    def test_basic_success_does_not_certify_history_tools_or_cost(self):
        tasks=tasks_for_route({'model':'gpt-test','capability':'text'},self.route(),{})
        by={t['case']:t['status'] for t in tasks}
        self.assertEqual(by['basic_transport'],'passed')
        for case in ['function_call_and_result','image_input_content_and_charge','hosted_search_call_and_invoice','stored_history_context_charge_bound','full_request_cost_ceiling','cache_1h_creation_hit_and_invoice']:
            self.assertEqual(by[case],'pending')
    def test_wrong_image_dimensions_are_not_a_pass(self):
        pricing={'image_specs':{'size':'1254x1254','default_quality':'low','options':[{'size':'1254x1254','quality':'low'}]}}
        tasks=tasks_for_route({'model':'image-test','capability':'image'},self.route(),pricing)
        image=next(t for t in tasks if t['case']=='image_dimensions_and_single_charge')
        self.assertEqual(image['status'],'needs_recheck');self.assertEqual(image['detail']['observed_dimensions'],'2048x2048')
    def test_unadvertised_capability_is_not_added(self):
        route=self.route();route['already_open_capabilities']=[]
        cases={t['case'] for t in tasks_for_route({'model':'text-model','capability':'text'},route,{})}
        self.assertNotIn('hosted_search_call_and_invoice',cases)
        self.assertNotIn('function_call_and_result',cases)
        self.assertIn('full_request_cost_ceiling',cases)

if __name__=='__main__':unittest.main()
