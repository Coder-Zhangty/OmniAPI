-- CNY 5 trial credit at the fixed advertised gift rate CNY 6.7489/USD.
-- 5 / 6.7489 * 500000 = 370430.746..., rounded down to match top-up ledger semantics.
BEGIN;
INSERT INTO options (key, value) VALUES
  ('RegisterEnabled', 'true'),
  ('PasswordRegisterEnabled', 'true'),
  ('QuotaForNewUser', '370430')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
COMMIT;
