"""New CNY manual quotes obey the shared limit; old receipt handling is untouched."""
from unittest.mock import patch
from tests.test_payment_methods import ServiceTestCase
class XorPayManualBounds(ServiceTestCase):
    def test_new_manual_cny_quotes_refuse_over_ten_thousand(self):
        with self.assertRaisesRegex(ValueError, '10000'):
            self.service.quote({'method': 'cny-qr', 'amount': '10000.01'})
    def test_maximum_still_quotes_with_the_resolved_rate(self):
        with patch.object(self.service, 'cny_rate', return_value=(__import__('decimal').Decimal('6.7195'), 'fixture', '2026-10-22', None)):
            result=self.service.quote({'method': 'cny-qr', 'amount': '10000'})
        self.assertEqual(result['quoted_amount'], '10000')
