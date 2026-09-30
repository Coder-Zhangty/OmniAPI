import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]/'scripts'))
from commercial_quote import quote

class CommercialQuoteTests(unittest.TestCase):
    def config(self):
        return {'markup':2,'cny_per_usd':7,
                'models':{'sample':{'currency':'CNY','input_per_million':7,'output_per_million':14}},
                'plans':[{'title':'sample','sale_cny':70,'duration_days':30,'quota_reset_period':'never'}]}

    def test_twice_cost_without_double_markup_on_quota(self):
        result=quote(self.config(),['sample'])
        self.assertEqual(result['models']['sample']['input_usd_per_million'],'2.00')
        self.assertEqual(result['models']['sample']['output_usd_per_million'],'4.00')
        self.assertEqual(result['plans'][0]['total_amount'],5000000)
        self.assertEqual(result['plans'][0]['maximum_supplier_cost_cny_before_fees'],'35')
        self.assertFalse(result['plans'][0]['enabled'])

    def test_retail_price_rounds_up_to_public_cents(self):
        config=self.config()
        config['models']['sample']={'currency':'USD','input_per_million':0.1125,'output_per_million':0.5625}
        result=quote(config,['sample'])['models']['sample']
        self.assertEqual(result['input_usd_per_million'],'0.23')
        self.assertEqual(result['output_usd_per_million'],'1.13')
        self.assertEqual(result['billing_expr'],'v1:p * 0.23 + c * 1.13')

    def test_missing_cost_rejected(self):
        with self.assertRaises(ValueError): quote(self.config(),['unpriced'])

    def test_missing_fx_rejected(self):
        config=self.config(); config['cny_per_usd']=None
        with self.assertRaises(ValueError): quote(config,['sample'])

    def test_unlimited_or_reset_quota_rejected(self):
        for amount,reset in [(0,'never'),(70,'daily')]:
            config=self.config(); config['plans'][0].update(sale_cny=amount,quota_reset_period=reset)
            with self.assertRaises(ValueError): quote(config,['sample'])

if __name__=='__main__': unittest.main()
