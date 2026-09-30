-- Enable the verified gpt-6.1-sol model and publish one global announcement.
-- Idempotent: rerunning this file does not duplicate the model or announcement.
BEGIN;

UPDATE channels
SET models = models || ',gpt-6.1-sol'
WHERE name = '0-0 Main'
  AND position(',gpt-6.1-sol,' in ',' || models || ',') = 0;

INSERT INTO abilities ("group", model, channel_id, enabled, priority, weight)
SELECT "group", 'gpt-6.1-sol', channel_id, enabled, priority, weight
FROM abilities
WHERE model = 'gpt-6-sol'
ON CONFLICT DO NOTHING;

INSERT INTO models (
  model_name, description, icon, tags, vendor_id, endpoints, status,
  sync_official, created_time, updated_time, deleted_at, name_rule
)
SELECT
  'gpt-6.1-sol', description, icon, tags, vendor_id, endpoints, status,
  sync_official, created_time, updated_time, NULL, name_rule
FROM models
WHERE model_name = 'gpt-6-sol'
  AND NOT EXISTS (SELECT 1 FROM models WHERE model_name = 'gpt-6.1-sol');

UPDATE options
SET value = jsonb_set(value::jsonb, '{gpt-6.1-sol}', '0.5'::jsonb, true)::text
WHERE key = 'ModelRatio';

UPDATE options
SET value = jsonb_set(value::jsonb, '{gpt-6.1-sol}', '2'::jsonb, true)::text
WHERE key = 'CompletionRatio';

UPDATE options
SET value = jsonb_set(
  value::jsonb,
  '{gpt-6.1-sol}',
  to_jsonb('v1:p * 0.10 + c * 0.50'::text),
  true
)::text
WHERE key = 'billing_setting.billing_expr';

UPDATE options
SET value = jsonb_set(
  value::jsonb,
  '{gpt-6.1-sol}',
  to_jsonb('tiered_expr'::text),
  true
)::text
WHERE key = 'billing_setting.billing_mode';

INSERT INTO options (key, value)
VALUES ('console_setting.announcements_enabled', 'true')
ON CONFLICT (key) DO UPDATE SET value = 'true';

UPDATE options
SET value = (
  jsonb_build_array(jsonb_build_object(
    'id', 3,
    'type', 'success',
    'content', '🚀 GPT-6.1 Sol 模型现已上线！欢迎大家使用。模型名称：gpt-6.1-sol，可通过 OmniAPI 统一 Base URL 调用。',
    'publishDate', to_char(current_timestamp AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD"T"HH24:MI:SS') || '+08:00',
    'extra', '请在模型广场查看费率；API Base URL：https://omniapi.biz/v1'
  )) || value::jsonb
)::text
WHERE key = 'console_setting.announcements'
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(value::jsonb) item
    WHERE item->>'id' = '3'
  );

COMMIT;
