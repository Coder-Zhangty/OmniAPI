#!/usr/bin/env python3
"""Rebuild the `quota_data` dashboard aggregate from raw consumption `logs`.

Why this exists: the dashboard charts read `quota_data`, not `logs`. That table
is only written while the `DataExportEnabled` option is on -- when it was off,
`LogQuotaData()` was never even called, so the aggregates for those hours do not
exist anywhere and turning the option back on cannot recreate them. The raw
consumption rows in `logs` are still there, so the history is recoverable; this
script does that recovery.

The aggregation mirrors New API's own `model.LogQuotaData` + `SaveQuotaDataCache`
exactly, so backfilled rows are indistinguishable from rows the service would
have written itself:

    created_at    floor to the hour   (log.created_at - log.created_at % 3600)
    count         how many requests fell in that bucket
    quota         sum of log.quota
    token_used    sum of prompt_tokens + completion_tokens
    use_group     logs."group"
    node_name     the running instance's NODE_NAME

Runs on the deployment host (it talks to PostgreSQL through `docker exec`).
Dry-run by default: it prints what it would insert and writes an evidence file.
Passing --apply is the only thing that writes to the database, and even then it
refuses to touch a bucket that `quota_data` already has, so it is safe to rerun.

    python3 scripts/backfill_quota_data.py                 # 预演
    python3 scripts/backfill_quota_data.py --apply         # 真的写
"""
import argparse
import json
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_REPORT = ROOT / 'runtime/backfill-quota-data.json'

# Column list `increaseQuotaData()` matches on. Kept here so the generated SQL and
# the "already exists" guard can never disagree with each other.
KEY_COLUMNS = ('user_id', 'username', 'model_name', 'created_at', 'use_group',
               'token_id', 'channel_id', 'node_name')
# node_name is a constant on this side, not a column of the aggregate subquery,
# so it must not be compared against `a.*` in the guard.
GUARD_COLUMNS = tuple(name for name in KEY_COLUMNS if name != 'node_name')
EXPECTED_COLUMNS = set(KEY_COLUMNS) | {'token_used', 'count', 'quota', 'id'}


def bucket(created_at):
    """The hourly bucket New API stores in quota_data."""
    return created_at - (created_at % 3600)


def aggregate_sql(since, until):
    """The group-by that turns raw logs into quota_data rows.

    `logs."group"` and the alias `cnt` avoid PostgreSQL's reserved words, and
    only type=2 (consume) rows are aggregated -- recharge and error rows are not
    usage and never reached LogQuotaData().
    """
    return (
        "SELECT l.user_id AS user_id,\n"
        "       COALESCE(l.username, '') AS username,\n"
        "       COALESCE(l.model_name, '') AS model_name,\n"
        "       (l.created_at - (l.created_at %% 3600)) AS bucket,\n"
        "       COALESCE(l.\"group\", '') AS use_group,\n"
        "       COALESCE(l.token_id, 0) AS token_id,\n"
        "       COALESCE(l.channel_id, 0) AS channel_id,\n"
        "       COUNT(*) AS cnt,\n"
        "       COALESCE(SUM(l.quota), 0) AS quota,\n"
        "       COALESCE(SUM(COALESCE(l.prompt_tokens, 0) + "
        "COALESCE(l.completion_tokens, 0)), 0) AS token_used\n"
        "FROM logs l\n"
        "WHERE l.type = 2 AND l.created_at >= %d AND l.created_at <= %d\n"
        "GROUP BY 1, 2, 3, 4, 5, 6, 7\n"
        "ORDER BY bucket ASC, user_id ASC, model_name ASC" % (since, until)
    )


def insert_sql(since, until, node_name):
    """Insert the aggregate, skipping buckets quota_data already covers."""
    guard = '\n     AND '.join('q.%s = a.%s' % (name, name) for name in GUARD_COLUMNS)
    return (
        "INSERT INTO quota_data\n"
        "  (user_id, username, model_name, created_at, use_group, token_id,\n"
        "   channel_id, node_name, token_used, \"count\", quota)\n"
        "SELECT a.user_id, a.username, a.model_name, a.bucket, a.use_group,\n"
        "       a.token_id, a.channel_id, %s, a.token_used, a.cnt, a.quota\n"
        "FROM (\n%s\n) a\n"
        "WHERE NOT EXISTS (\n"
        "  SELECT 1 FROM quota_data q\n"
        "  WHERE %s\n     AND q.node_name = %s\n);"
        % (quote(node_name), indent(aggregate_sql(since, until), '  '), guard,
           quote(node_name))
    )


def quote(value):
    return "'" + str(value).replace("'", "''") + "'"


def indent(text, prefix):
    return '\n'.join(prefix + line for line in text.splitlines())


def parse_rows(raw):
    """psql -t -A -F'|' output -> list of dicts."""
    rows = []
    for line in raw.splitlines():
        if not line.strip():
            continue
        parts = line.split('|')
        if len(parts) != 10:
            raise ValueError('无法解析查询结果行：%r' % line)
        rows.append({
            'user_id': int(parts[0]), 'username': parts[1], 'model_name': parts[2],
            'created_at': int(parts[3]), 'use_group': parts[4],
            'token_id': int(parts[5]), 'channel_id': int(parts[6]),
            'count': int(parts[7]), 'quota': int(parts[8]), 'token_used': int(parts[9]),
        })
    return rows


def summarise(rows):
    by_model = {}
    by_day = {}
    for row in rows:
        model = by_model.setdefault(row['model_name'] or '(未记录)',
                                    {'count': 0, 'quota': 0, 'token_used': 0})
        model['count'] += row['count']
        model['quota'] += row['quota']
        model['token_used'] += row['token_used']
        day = time.strftime('%Y-%m-%d', time.gmtime(row['created_at']))
        entry = by_day.setdefault(day, {'count': 0, 'quota': 0})
        entry['count'] += row['count']
        entry['quota'] += row['quota']
    return {
        'rows': len(rows),
        'requests': sum(row['count'] for row in rows),
        'quota': sum(row['quota'] for row in rows),
        'token_used': sum(row['token_used'] for row in rows),
        'hours': sorted({row['created_at'] for row in rows}),
        'by_model': by_model,
        'by_day': dict(sorted(by_day.items())),
    }


def run_sql(sql, container, db, user, timeout=120):
    result = subprocess.run(
        ['docker', 'exec', '-i', container, 'psql', '-U', user, '-d', db,
         '-t', '-A', '-F', '|', '-v', 'ON_ERROR_STOP=1', '-c', sql],
        capture_output=True, text=True, timeout=timeout)
    if result.returncode != 0:
        raise SystemExit('BLOCKED: 查询失败\n%s' % (result.stderr.strip()[:2000]))
    return result.stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--container', default='ai-gateway-database-1')
    parser.add_argument('--db', default='gateway')
    parser.add_argument('--user', default='gateway')
    parser.add_argument('--node-name', default=None,
                        help='quota_data.node_name；默认读取 new-api 容器的 NODE_NAME')
    parser.add_argument('--since', type=int, default=0, help='起始 unix 秒（含）')
    parser.add_argument('--until', type=int, default=None, help='结束 unix 秒（含）')
    parser.add_argument('--apply', action='store_true',
                        help='真的写入；不加这个参数只预演')
    parser.add_argument('--report', type=Path, default=DEFAULT_REPORT)
    args = parser.parse_args()

    until = args.until if args.until is not None else int(time.time()) + 3600

    # 1) self-check the schema: a renamed column must stop us, not corrupt data.
    columns = set(run_sql(
        "SELECT column_name FROM information_schema.columns "
        "WHERE table_name = 'quota_data';", args.container, args.db, args.user
    ).split())
    if not EXPECTED_COLUMNS.issubset(columns):
        raise SystemExit('BLOCKED: quota_data 缺少列 %s（实际：%s）'
                         % (sorted(EXPECTED_COLUMNS - columns), sorted(columns)))

    # 2) the switch is the whole reason this script exists; report its state.
    switch = run_sql(
        "SELECT key || '=' || value FROM options WHERE key IN "
        "('DataExportEnabled', 'DataExportInterval', 'DataExportDefaultTime') "
        "ORDER BY key;", args.container, args.db, args.user).split()
    enabled = any(item == 'DataExportEnabled=true' for item in switch)
    before = int(run_sql("SELECT count(*) FROM quota_data;", args.container,
                         args.db, args.user).strip() or 0)

    node_name = args.node_name
    if not node_name:
        probe = subprocess.run(
            ['docker', 'exec', 'ai-gateway-new-api-1', 'printenv', 'NODE_NAME'],
            capture_output=True, text=True)
        node_name = probe.stdout.strip() or 'unknown-node'
    print('开关:', ' '.join(switch) or '（未设置）')
    print('node_name:', node_name, '| quota_data 现有行数:', before)

    # 3) what the backfill would add.
    rows = parse_rows(run_sql(aggregate_sql(args.since, until), args.container,
                              args.db, args.user))
    summary = summarise(rows)
    print(json.dumps({k: summary[k] for k in
                      ('rows', 'requests', 'quota', 'token_used', 'by_model')},
                     ensure_ascii=False, indent=2))

    report = {
        'script': 'scripts/backfill_quota_data.py',
        'mode': 'apply' if args.apply else 'dry-run',
        'window': {'since': args.since, 'until': until},
        'options': switch,
        'data_export_enabled': enabled,
        'node_name': node_name,
        'quota_data_rows_before': before,
        'candidate': summary,
    }

    if not enabled:
        # Backfilling while the switch is off is not wrong, but it leaves the
        # charts frozen the moment the operator looks at them, which reads like
        # the fix did not work. Say so rather than silently doing it.
        print('注意: DataExportEnabled 仍为 false —— 回填只会让图表显示到'
              '「此刻」为止，之后的用量依然不会记录。建议先打开开关。')

    if not args.apply:
        report['result'] = 'DRY-RUN（未写入任何数据）'
        print('\n这是预演，没有写入数据库。加 --apply 才会真的写入。')
    elif not rows:
        report['result'] = 'SKIPPED（时间窗内没有可回填的消费日志）'
        print('\n没有需要回填的行。')
    else:
        run_sql(insert_sql(args.since, until, node_name), args.container,
                args.db, args.user)
        after = int(run_sql("SELECT count(*) FROM quota_data;", args.container,
                            args.db, args.user).strip() or 0)
        report['quota_data_rows_after'] = after
        report['inserted'] = after - before
        report['result'] = 'PASS'
        print('\n已写入 %d 行（quota_data %d -> %d）。' % (after - before, before, after))

    args.report.parent.mkdir(parents=True, exist_ok=True)
    args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n',
                           encoding='utf-8')
    print('证据: %s' % args.report)
    return 0


if __name__ == '__main__':
    sys.exit(main())
