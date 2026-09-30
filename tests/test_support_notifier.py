"""Unit tests for the push notifier and its throttle.

No network: the transport is injected, which is also what lets the failure paths
be exercised. Those matter most, because "I never got the push" has to be
answerable from a record rather than from memory.
"""
import json
import sys
import unittest
from pathlib import Path

BASE = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(BASE / 'ops' / 'support'))

import notifier  # noqa: E402

SECRETS = {
    'wecom': {'webhook': 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc'},
    'bark': {'url': 'https://api.day.app/device-token'},
    'telegram': {'token': '123:abc', 'chat_id': '42'},
}


def transport_returning(status, body='{}'):
    def transport(request, timeout):
        return status, body
    return transport


class BuildTests(unittest.TestCase):
    def test_wecom_posts_json(self):
        url, payload, method = notifier.build('wecom', SECRETS, '标题', '正文', '链接')
        self.assertIn('webhook/send', url)
        self.assertEqual(method, 'POST')
        body = json.loads(payload)
        self.assertEqual(body['msgtype'], 'text')
        self.assertIn('正文', body['text']['content'])
        self.assertIn('链接', body['text']['content'])

    def test_bark_uses_a_path_and_escapes_it(self):
        url, payload, method = notifier.build('bark', SECRETS, 'OmniAPI 客服', '有 新 消息', '')
        self.assertIsNone(payload)
        self.assertEqual(method, 'GET')
        self.assertNotIn(' ', url)
        self.assertTrue(url.startswith('https://api.day.app/device-token/'))

    def test_telegram_targets_the_configured_chat(self):
        url, payload, method = notifier.build('telegram', SECRETS, 't', 'b', '')
        self.assertIn('bot123:abc/sendMessage', url)
        self.assertEqual(json.loads(payload)['chat_id'], '42')

    def test_missing_credentials_are_reported_not_guessed(self):
        for channel in ('wecom', 'bark', 'telegram'):
            self.assertIsNone(notifier.build(channel, {}, 't', 'b', ''), channel)

    def test_the_body_is_truncated_for_a_notification(self):
        _, payload, _ = notifier.build('wecom', SECRETS, 't', 'x' * 1000, '')
        content = json.loads(payload)['text']['content']
        self.assertLessEqual(len(content), notifier.MESSAGE_LIMIT + 20)
        self.assertTrue(content.endswith('…'))


class PublishTests(unittest.TestCase):
    def test_no_channel_means_skipped(self):
        result = notifier.publish('none', SECRETS, 't', 'b')
        self.assertFalse(result['ok'])
        self.assertTrue(result['skipped'])

    def test_an_unknown_channel_is_refused(self):
        self.assertFalse(notifier.publish('carrier-pigeon', SECRETS, 't', 'b')['ok'])

    def test_a_successful_send_is_recorded(self):
        result = notifier.publish('telegram', SECRETS, 't', 'b',
                                 transport=transport_returning(200, '{"ok":true}'))
        self.assertTrue(result['ok'])
        self.assertEqual(result['status'], 200)

    def test_wecom_checks_the_json_business_code(self):
        success = notifier.publish('wecom', SECRETS, 't', 'b',
                                   transport=transport_returning(200, '{"errcode":0,"errmsg":"ok"}'))
        revoked = notifier.publish('wecom', SECRETS, 't', 'b',
                                   transport=transport_returning(200, '{"errcode":93000,"errmsg":"invalid webhook"}'))
        malformed = notifier.publish('wecom', SECRETS, 't', 'b',
                                     transport=transport_returning(200, 'not-json'))
        self.assertTrue(success['ok'])
        self.assertFalse(revoked['ok'])
        self.assertIn('93000', revoked['error'])
        self.assertFalse(malformed['ok'])

    def test_a_failure_is_reported_never_raised(self):
        result = notifier.publish('wecom', SECRETS, 't', 'b',
                                 transport=transport_returning(500, 'boom'))
        self.assertFalse(result['ok'])
        self.assertEqual(result['status'], 500)
        self.assertIn('500', result['error'])

    def test_an_exception_is_reported_never_raised(self):
        def broken(request, timeout):
            raise OSError('network is down')

        result = notifier.publish('wecom', SECRETS, 't', 'b', transport=broken)
        self.assertFalse(result['ok'])
        self.assertIn('network is down', result['error'])

    def test_missing_credentials_surface_as_a_clear_error(self):
        result = notifier.publish('wecom', {}, 't', 'b',
                                 transport=transport_returning(200))
        self.assertFalse(result['ok'])
        self.assertIn('凭据', result['error'])


class ThrottleTests(unittest.TestCase):
    def test_a_burst_is_merged_into_one_notification(self):
        now = [1000.0]
        throttle = notifier.Throttle(60, clock=lambda: now[0])
        self.assertTrue(throttle.allow('conv-a'))
        self.assertFalse(throttle.allow('conv-a'))
        now[0] += 30
        self.assertFalse(throttle.allow('conv-a'))
        now[0] += 31
        self.assertTrue(throttle.allow('conv-a'))

    def test_each_conversation_has_its_own_budget(self):
        throttle = notifier.Throttle(60, clock=lambda: 0.0)
        self.assertTrue(throttle.allow('a'))
        self.assertTrue(throttle.allow('b'))

    def test_an_urgent_message_can_reset_the_window(self):
        throttle = notifier.Throttle(60, clock=lambda: 0.0)
        self.assertTrue(throttle.allow('a'))
        self.assertFalse(throttle.allow('a'))
        throttle.reset('a')
        self.assertTrue(throttle.allow('a'))

    def test_the_interval_can_be_overridden_per_call(self):
        throttle = notifier.Throttle(60, clock=lambda: 0.0)
        self.assertTrue(throttle.allow('a', 0))
        self.assertTrue(throttle.allow('a', 0))


if __name__ == '__main__':
    unittest.main()
