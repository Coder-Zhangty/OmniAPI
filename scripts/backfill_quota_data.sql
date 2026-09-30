-- Rebuild New API's dashboard aggregation from authoritative consume logs.
--
-- Run only while new-api is stopped. Its in-memory quota-data cache is flushed
-- during graceful shutdown; rebuilding while it is running can double-count a
-- bucket when that cache is saved later.
--
-- Log type 2 is LogTypeConsume in v1.0.0-rc.40. The dimensions and one-hour
-- bucket exactly mirror model.LogQuotaData in that release.
BEGIN;

LOCK TABLE logs IN SHARE MODE;
LOCK TABLE quota_data IN ACCESS EXCLUSIVE MODE;

TRUNCATE TABLE quota_data RESTART IDENTITY;

INSERT INTO quota_data (
    user_id, username, model_name, created_at, token_used, count, quota,
    use_group, token_id, channel_id, node_name
)
SELECT
    user_id,
    COALESCE(username, ''),
    COALESCE(model_name, ''),
    created_at - (created_at % 3600),
    SUM(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0)),
    COUNT(*),
    SUM(COALESCE(quota, 0)),
    COALESCE("group", ''),
    COALESCE(token_id, 0),
    COALESCE(channel_id, 0),
    'gateway-local-1'
FROM logs
WHERE type = 2
GROUP BY
    user_id, COALESCE(username, ''), COALESCE(model_name, ''),
    created_at - (created_at % 3600), COALESCE("group", ''),
    COALESCE(token_id, 0), COALESCE(channel_id, 0);

-- Keep dashboard collection enabled and flush its in-memory buckets every
-- minute, reducing the amount temporarily absent after an unclean restart.
INSERT INTO options (key, value)
VALUES ('DataExportEnabled', 'true'), ('DataExportInterval', '1')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;

COMMIT;

SELECT
    COUNT(*) AS aggregate_rows,
    COALESCE(SUM(count), 0) AS consume_calls,
    COALESCE(SUM(quota), 0) AS quota_total,
    COALESCE(SUM(token_used), 0) AS token_total
FROM quota_data;
