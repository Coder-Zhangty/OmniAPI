-- Repair the one-quota-unit boundary mismatch for the CNY 18 referral reward.
-- Idempotency guard: only runs while the old reward value is still active.
BEGIN;
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM options
    WHERE key = 'QuotaForInviter' AND value = '1333550'
  ) THEN
    -- Each historical successful invite was short by exactly one quota unit.
    -- Credit that tiny correction directly to the main balance: some users may
    -- already have transferred the old reward, while one unit on its own is
    -- below the affiliate transfer minimum.
    UPDATE users
       SET quota = quota + aff_count,
           aff_history = aff_history + aff_count
     WHERE aff_count > 0;

    UPDATE options
       SET value = '1333551'
     WHERE key = 'QuotaForInviter';
  END IF;
END $$;
COMMIT;
