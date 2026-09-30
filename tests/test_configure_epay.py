"""Unit tests for the epay configuration guard rails.

These cover the pure decision functions only: URL normalisation, public
reachability, and the reasons that must block a write. The point is that a
mistyped gateway address or a loopback callback can never reach the live
options table.
"""
import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
import configure_epay  # noqa: E402


class BaseAddressTests(unittest.TestCase):
    def test_accepts_plain_gateway_and_strips_trailing_slash(self):
        self.assertEqual(configure_epay.base_address('https://pay.example.com/')[0],
                         'https://pay.example.com')
        self.assertEqual(configure_epay.base_address('http://pay.example.com')[0],
                         'http://pay.example.com')

    def test_rejects_submit_php_because_go_epay_appends_it(self):
        value, error = configure_epay.base_address('https://pay.example.com/submit.php')
        self.assertIsNone(value)
        self.assertIn('submit.php', error)

    def test_rejects_relative_or_empty_address(self):
        for candidate in ('', '   ', 'pay.example.com', '/submit.php'):
            value, error = configure_epay.base_address(candidate)
            self.assertIsNone(value, candidate)
            self.assertTrue(error)


class PublicReachabilityTests(unittest.TestCase):
    def test_loopback_and_private_addresses_are_not_reachable(self):
        for host in ('http://127.0.0.1', 'http://localhost:80', 'http://10.0.0.5',
                     'http://192.168.1.10', 'http://172.16.0.1', 'http://172.31.255.1',
                     'http://box.local', ''):
            self.assertFalse(configure_epay.publicly_reachable(host), host)

    def test_public_host_and_172_outside_private_range_are_reachable(self):
        for host in ('https://api.example.com', 'http://160.202.46.77',
                     'http://172.32.0.1', 'http://8.8.8.8'):
            self.assertTrue(configure_epay.publicly_reachable(host), host)


class PayMethodsTests(unittest.TestCase):
    def test_parses_json_list_and_tolerates_junk(self):
        raw = json.dumps([{'name': '支付宝', 'type': 'alipay'}])
        self.assertEqual(configure_epay.pay_methods({'PayMethods': raw}),
                         [{'name': '支付宝', 'type': 'alipay'}])
        for broken in ('', None, 'not json', '{"a": 1}', '[]'):
            self.assertEqual(configure_epay.pay_methods({'PayMethods': broken}), [])


class BlockedReasonTests(unittest.TestCase):
    def options(self, **overrides):
        base = {
            'ServerAddress': 'https://gateway.example.com',
            'PayMethods': json.dumps([{'name': '支付宝', 'type': 'alipay'}]),
            'payment_setting.compliance_confirmed': 'true',
        }
        base.update(overrides)
        return base

    def config(self, **overrides):
        base = {'pay_address': 'https://pay.example.com', 'epay_id': '1001',
                'epay_key': 'secret', 'custom_callback_address': ''}
        base.update(overrides)
        return base

    def test_a_complete_configuration_passes(self):
        self.assertEqual(configure_epay.blocked_reasons(self.config(), self.options()), [])

    def test_missing_credentials_are_all_reported(self):
        reasons = configure_epay.blocked_reasons(
            {'pay_address': '', 'epay_id': ' ', 'epay_key': ''}, self.options())
        self.assertEqual(len(reasons), 3)
        self.assertTrue(any('pay_address' in r for r in reasons))
        self.assertTrue(any('epay_id' in r for r in reasons))
        self.assertTrue(any('epay_key' in r for r in reasons))

    def test_loopback_server_address_blocks_because_callbacks_cannot_arrive(self):
        reasons = configure_epay.blocked_reasons(
            self.config(), self.options(ServerAddress='http://127.0.0.1:80'))
        self.assertTrue(any('公网' in r for r in reasons))

    def test_empty_pay_methods_blocks(self):
        reasons = configure_epay.blocked_reasons(self.config(), self.options(PayMethods='[]'))
        self.assertEqual(reasons, [])

    def test_missing_compliance_acknowledgement_blocks(self):
        reasons = configure_epay.blocked_reasons(
            self.config(), self.options(**{'payment_setting.compliance_confirmed': 'false'}))
        self.assertTrue(any('合规条款' in r for r in reasons))

    def test_custom_callback_address_is_validated_when_present(self):
        reasons = configure_epay.blocked_reasons(
            self.config(custom_callback_address='https://cb.example.com/submit.php'),
            self.options())
        self.assertTrue(any('custom_callback_address' in r for r in reasons))
        self.assertEqual(
            configure_epay.blocked_reasons(
                self.config(custom_callback_address='https://cb.example.com'), self.options()),
            [])


class WarningTests(unittest.TestCase):
    def test_http_ip_and_placeholder_methods_are_flagged(self):
        options = {'ServerAddress': 'http://160.202.46.77',
                   'PayMethods': json.dumps([{'name': '自定义1', 'type': 'custom1'}])}
        notes = configure_epay.warnings({}, options)
        joined = ' '.join(notes)
        self.assertIn('HTTPS', joined)
        self.assertIn('IP', joined)
        self.assertIn('custom1', joined)

    def test_https_domain_with_real_methods_has_no_warnings(self):
        options = {'ServerAddress': 'https://api.example.com',
                   'PayMethods': json.dumps([{'name': '支付宝', 'type': 'alipay'}])}
        self.assertEqual(configure_epay.warnings({}, options), [])


class CallbackUrlTests(unittest.TestCase):
    def test_defaults_to_server_address_and_lists_every_endpoint(self):
        options = {'ServerAddress': 'https://api.example.com/'}
        urls = configure_epay.callback_urls(options, {'custom_callback_address': ''})
        self.assertEqual(urls['wallet_notify'], 'https://api.example.com/api/user/epay/notify')
        self.assertEqual(urls['plan_notify'], 'https://api.example.com/api/subscription/epay/notify')
        self.assertEqual(urls['plan_return'], 'https://api.example.com/api/subscription/epay/return')
        # Wallet return uses ServerAddress, not the callback override.
        self.assertEqual(urls['wallet_return'], 'https://api.example.com/usage-logs')

    def test_custom_callback_overrides_notify_but_not_wallet_return(self):
        options = {'ServerAddress': 'https://api.example.com'}
        urls = configure_epay.callback_urls(options, {'custom_callback_address': 'https://cb.example.com'})
        self.assertEqual(urls['wallet_notify'], 'https://cb.example.com/api/user/epay/notify')
        self.assertEqual(urls['wallet_return'], 'https://api.example.com/usage-logs')


class SecretHandlingTests(unittest.TestCase):
    def test_option_snapshot_never_returns_merchant_key(self):
        snap = configure_epay.public_option_snapshot({'EpayId': '42', 'EpayKey': 'super-secret'})
        self.assertEqual(snap['EpayId'], '42')
        self.assertEqual(snap['EpayKey'], '<configured>')
        self.assertNotIn('super-secret', json.dumps(snap))

    class FakeStatusAPI:
        def ok(self, path):
            if path == '/api/user/topup/info':
                return {'enable_online_topup': False, 'pay_methods': []}
            if path == '/api/subscription/admin/plans':
                return [{'plan': {'id': 1, 'title': '体验包', 'enabled': True}}]
            raise RuntimeError(path)

    def test_status_exposes_presence_only(self):
        options = {'ServerAddress': 'https://api.example.com', 'PayAddress': 'https://pay',
                   'EpayId': 'merchant-42', 'EpayKey': 'super-secret',
                   'payment_setting.compliance_confirmed': 'false'}
        status = configure_epay.safe_status(self.FakeStatusAPI(), options)
        output = json.dumps(status)
        self.assertNotIn('super-secret', output)
        self.assertNotIn('merchant-42', output)
        self.assertTrue(status['merchant_gateway_configured'])
        self.assertEqual(status['status'], 'BLOCKED')


class MoneyPreviewTests(unittest.TestCase):
    class FakeAPI:
        def __init__(self, plans):
            self.plans = plans

        def ok(self, path):
            if path == '/api/subscription/admin/plans':
                return self.plans
            raise RuntimeError('unexpected path ' + path)

    def test_detects_the_displayed_versus_charged_gap(self):
        api = self.FakeAPI([{'plan': {'title': '体验包', 'price_amount': 2.815273, 'enabled': True}}])
        rows, problems, fx = configure_epay.money_preview(api, {'USDExchangeRate': '6.7489'})
        self.assertEqual(str(fx), '6.7489')
        self.assertEqual(rows[0]['displayed_to_buyer'], '19.00')
        self.assertEqual(rows[0]['charged_by_gateway'], '2.82')
        self.assertTrue(rows[0]['mismatch'])
        self.assertEqual(len(problems), 1)

    def test_reports_agreement_when_the_rate_is_one(self):
        api = self.FakeAPI([{'plan': {'title': '体验包', 'price_amount': 19, 'enabled': True}}])
        rows, problems, _ = configure_epay.money_preview(api, {'USDExchangeRate': '1'})
        self.assertFalse(rows[0]['mismatch'])
        self.assertEqual(problems, [])


if __name__ == '__main__':
    unittest.main()
