"""Apply approved commercial retail prices (supplier cost x 2) to live billing.

Snapshots the current native billing expressions before changing them so the
previous pricing can be restored from the snapshot file. Refuses to run while
any enabled model lacks a verified supplier cost. Does not touch channels,
upstream keys, subscriptions or user balances.
"""
import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from commercial_quote import quote  # noqa: E402
from gateway import ROOT, API, Stack, credentials, write_json  # noqa: E402
from publish_plans import enabled_models, merge_models  # noqa: E402

NEUTRAL_DESCRIPTION = '文本模型；按实际用量计费，单价以模型价格页为准。'
OPTION_KEYS = ('billing_setting.billing_expr', 'billing_setting.billing_mode')
VENDORS = (
    ('claude-', 1, 'Claude.Color'),
    ('deepseek-', 2, 'DeepSeek.Color'),
    ('gemini-', 3, 'Gemini.Color'),
    ('glm-', 4, 'Zhipu.Color'),
    ('gpt-', 5, 'OpenAI'),
    ('grok-', 6, 'XAI'),
)


def vendor_for(name):
    return next(((vendor_id, icon) for prefix, vendor_id, icon in VENDORS
                 if name.startswith(prefix)), (0, ''))


def planned(config, catalog_path):
    """Derive cost x 2 expressions for every enabled (PASS) model."""
    catalog = json.loads(Path(catalog_path).read_text(encoding='utf-8'))
    required = enabled_models(catalog)
    models = merge_models(config)
    missing = sorted(set(required) - set(models))
    if missing:
        raise ValueError('missing supplier costs: ' + ', '.join(missing))
    return quote(dict(config, models={name: models[name] for name in required}), required)


def pricing_rows(data):
    if isinstance(data, dict):
        data = data.get('data') or data.get('items') or []
    return data if isinstance(data, list) else []


def apply(config_path, catalog_path, snapshot_path, evidence_path, dry_run=False):
    config = json.loads(Path(config_path).read_text(encoding='utf-8'))
    result = planned(config, catalog_path)

    api = API(Stack().url)
    api.login(credentials(ROOT, 'admin'))
    options = api.options()
    before = {key: options.get(key, '{}') for key in OPTION_KEYS}
    write_json(Path(snapshot_path), before)

    expr = json.loads(before['billing_setting.billing_expr'])
    modes = json.loads(before['billing_setting.billing_mode'])
    for name, rate in result['models'].items():
        expr[name] = rate['billing_expr']
        modes[name] = 'tiered_expr'

    if dry_run:
        sample = {name: result['models'][name]['billing_expr']
                  for name in sorted(result['models'])[:3]}
        return {'status': 'DRY_RUN', 'models': len(result['models']),
                'snapshot_written': str(snapshot_path), 'sample': sample}

    api.option('billing_setting.billing_expr', json.dumps(expr))
    api.option('billing_setting.billing_mode', json.dumps(modes))

    existing = {row['model_name']: row for row in api.ok('/api/models/?p=1&page_size=100')['items']}
    write_json(Path(snapshot_path).with_name(Path(snapshot_path).stem + '-models.json'),
               {'models': list(existing.values())})
    described = 0
    for name in result['models']:
        if name in existing:
            metadata = dict(existing[name])
            vendor_id, icon = vendor_for(name)
            metadata.update(description=NEUTRAL_DESCRIPTION, tags='文本',
                            vendor_id=vendor_id, icon=icon)
            api.ok('/api/models/', metadata, 'PUT')
            described += 1

    applied = json.loads(api.options().get('billing_setting.billing_expr', '{}'))
    mismatch = sorted(name for name, rate in result['models'].items()
                      if applied.get(name) != rate['billing_expr'])
    status, pricing, _ = API(Stack().url).request('/api/pricing')
    rows = pricing_rows(pricing)
    sample_row = next((row for row in rows if row.get('model_name') == 'gpt-5.4-mini'), None)
    evidence = {
        'status': 'PASS' if not mismatch and status == 200 else 'FAIL',
        'models_updated': len(result['models']),
        'descriptions_updated': described,
        'vendors_updated': described,
        'expression_mismatches': mismatch,
        'snapshot': str(snapshot_path),
        'public_pricing_http': status,
        'sample_public_pricing_row': sample_row,
        'rollback': 'restore the two options from the snapshot, or rerun provider_visibility.py',
    }
    write_json(Path(evidence_path), evidence)
    return evidence


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('config', type=Path, nargs='?', default=ROOT / 'config/commercial.json')
    parser.add_argument('--catalog', type=Path, default=ROOT / 'docs/REAL_MODELS.json')
    parser.add_argument('--snapshot', type=Path, default=ROOT / 'runtime/commercial-before-apply.json')
    parser.add_argument('--evidence', type=Path, default=ROOT / 'runtime/commercial-apply.json')
    parser.add_argument('--dry-run', action='store_true',
                        help='snapshot and compute only; do not change live billing')
    args = parser.parse_args()
    try:
        result = apply(args.config, args.catalog, args.snapshot, args.evidence, dry_run=args.dry_run)
    except ValueError as error:
        parser.exit(2, 'BLOCKED: ' + str(error) + '\n')
    print(json.dumps(result, ensure_ascii=False, indent=2))
    if result.get('status') == 'FAIL':
        parser.exit(2)


if __name__ == '__main__':
    main()
