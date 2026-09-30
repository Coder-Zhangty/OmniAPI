BEGIN;
INSERT INTO options (key, value) VALUES
  ('console_setting.api_info_enabled', 'true'),
  (
    'console_setting.api_info',
    '[{"id":1,"url":"https://omniapi.biz/v1","route":"OmniAPI 主线路","description":"OpenAI 兼容 API · HTTPS","color":"green"}]'
  )
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
COMMIT;
