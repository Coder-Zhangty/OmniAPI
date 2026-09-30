-- CNY 18 inviter reward at the fixed advertised reward rate CNY 6.7489/USD.
-- The transfer UI converts ¥18 back to quota with Math.round, yielding 1,333,551.
-- Keep the integer quota ledger exact at that boundary so an entered integer 18
-- can never be rejected as one unit greater than the displayed available reward.
BEGIN;
INSERT INTO options (key, value) VALUES
  ('QuotaForInviter', '1333551'),
  ('QuotaForInvitee', '0'),
  ('console_setting.announcements_enabled', 'true')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;

INSERT INTO options (key, value) VALUES (
  'console_setting.announcements',
  '[{"id":2,"content":"🤝 邀请奖励上线：每位用户在“钱包”中都有唯一邀请链接和邀请码。新用户注册时可选填邀请码；注册成功后，邀请人获得 ¥18 等值邀请额度，可在钱包中转入主余额。新用户仍可获得 ¥5 注册试用额度。","publishDate":"2026-09-28T09:30:00+08:00","type":"success","extra":"邀请码每次成功注册奖励一次；严禁批量注册、虚假邀请或其他滥用行为。"}]'
)
ON CONFLICT (key) DO UPDATE SET value = CASE
  WHEN options.value::jsonb @> '[{"id":2}]'::jsonb THEN options.value
  ELSE (options.value::jsonb || EXCLUDED.value::jsonb)::text
END;
COMMIT;
