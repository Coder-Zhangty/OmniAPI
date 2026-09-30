"""Generate a transaction for applying reviewed pricing without dashboard login.

The public login is protected by Turnstile, so unattended deployment scripts
cannot obtain a dashboard token.  This generator emits a reviewable SQL file;
the operator runs it inside the PostgreSQL container and restarts New API so
its one-minute pricing cache and model metadata are refreshed.
"""
import argparse
import json
from pathlib import Path

from apply_commercial_pricing import NEUTRAL_DESCRIPTION, planned, vendor_for
from gateway import ROOT


def literal(value):
    return "'" + str(value).replace("'", "''") + "'"


def generate(config_path, catalog_path):
    config = json.loads(Path(config_path).read_text(encoding='utf-8'))
    result = planned(config, catalog_path)
    expressions = {name: row['billing_expr'] for name, row in result['models'].items()}
    modes = {name: 'tiered_expr' for name in expressions}
    values = []
    for name in sorted(expressions):
        vendor_id, icon = vendor_for(name)
        values.append('(%s,%d,%s)' % (literal(name), vendor_id, literal(icon)))
    return '\n'.join([
        'BEGIN;',
        "UPDATE options SET value=(value::jsonb || $price$%s$price$::jsonb)::text "
        "WHERE key='billing_setting.billing_expr';" % json.dumps(expressions, ensure_ascii=False),
        "UPDATE options SET value=(value::jsonb || $mode$%s$mode$::jsonb)::text "
        "WHERE key='billing_setting.billing_mode';" % json.dumps(modes, ensure_ascii=False),
        "UPDATE models AS m SET vendor_id=v.vendor_id, icon=v.icon, description=%s, "
        "tags='文本', updated_time=extract(epoch from now())::bigint FROM (VALUES\n  %s\n) "
        "AS v(model_name,vendor_id,icon) WHERE m.model_name=v.model_name AND m.deleted_at IS NULL;"
        % (literal(NEUTRAL_DESCRIPTION), ',\n  '.join(values)),
        'COMMIT;',
        '',
    ])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, default=ROOT / 'config/commercial.json')
    parser.add_argument('--catalog', type=Path, default=ROOT / 'docs/REAL_MODELS.json')
    parser.add_argument('--output', type=Path, default=ROOT / 'runtime/apply-pricing.sql')
    args = parser.parse_args()
    sql = generate(args.config, args.catalog)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(sql, encoding='utf-8', newline='\n')
    print(json.dumps({'status': 'GENERATED', 'path': str(args.output),
                      'sha256_hint': 'verify with sha256sum before execution'}, ensure_ascii=False))


if __name__ == '__main__':
    main()
