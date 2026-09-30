-- Public dashboard announcement for the CNY 5 new-user trial credit.
BEGIN;
INSERT INTO options (key, value) VALUES
  ('console_setting.announcements_enabled', 'true'),
  (
    'console_setting.announcements',
    '[{"id":1,"content":"🎁 新用户注册福利：即日起注册 OmniAPI 账号，即送 ¥5 试用额度。完成注册并登录后额度自动到账，可用于调用平台模型。API Base URL：https://omniapi.biz/v1","publishDate":"2026-09-28T00:00:00+08:00","type":"success","extra":"仅限新注册账号，每个账号赠送一次；试用额度不可提现。"}]'
  )
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
COMMIT;
