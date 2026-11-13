import copy,unittest
from scripts.commerce_v3_quote import quote
from tests import test_commerce_v3_quote as token_fixture

class FixedImageQuoteTest(unittest.TestCase):
    def fixture(self):
        sheet=token_fixture.CommerceQuoteTest().fixture();route=sheet['models']['synthetic-model']['routes'][0]
        route.update(billing_mode='image',enforced_image_count=1)
        route.pop('context_tiers')
        dims={'per_image':{'verified':True,'procurement':'0.02','retail':'0.35'}}
        for category,item in route['coverage'].items():item['supported']=category=='images'
        route['coverage']['images']['dimensions']=dims
        route['image_specs']=[{'size':'2048x2048','quality':'low','verified':True,'source':'synthetic-only://image-spec','dimensions':copy.deepcopy(dims)}]
        return sheet,route
    def test_fixed_image_quote_does_not_invent_token_prices(self):
        sheet,route=self.fixture();result=quote(sheet)
        self.assertEqual(result['profile']['worst_cost_ppm'],57143)
        self.assertNotIn('context_tiers',route);self.assertFalse(result['sales_enabled'])
    def test_high_cost_spec_controls_quote(self):
        sheet,route=self.fixture();other=copy.deepcopy(route['image_specs'][0]);other['quality']='high';other['dimensions']['per_image']['procurement']='0.10';route['image_specs'].append(other)
        self.assertEqual(quote(sheet)['profile']['worst_cost_ppm'],285715)
    def test_missing_count_spec_or_prices_cannot_be_approved(self):
        for mutate in [lambda r:r.pop('enforced_image_count'),lambda r:r.update(image_specs=[]),lambda r:r['image_specs'][0].update(verified=False),lambda r:r['image_specs'][0]['dimensions']['per_image'].update(procurement=None),lambda r:r['coverage']['images'].update(supported=False)]:
            sheet,route=self.fixture();mutate(route)
            with self.assertRaises((ValueError,TypeError,KeyError,ArithmeticError)):quote(sheet)
    def test_additional_hosted_work_is_not_silently_ignored(self):
        sheet,route=self.fixture();route['coverage']['tools'].update(supported=True,dimensions={'hosted_work':{'verified':True,'procurement':'0.8','retail':'1'}})
        self.assertEqual(quote(sheet)['profile']['worst_cost_ppm'],800000)

if __name__=='__main__':unittest.main()
