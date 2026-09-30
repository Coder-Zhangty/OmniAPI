"""Ensure the approved subscription plans exist in the native backend, disabled.

Removes the transcription risk of typing quota numbers into the admin UI: the
plan payload comes from the same quote() the plans page and the pricing script
use, so the page, the billing expression and the native plan can never drift.

Safety rules:
- Dry run unless --apply is passed. A dry run only lists the backend state.
- Idempotent: a plan that already exists with the same total_amount is left
  alone, so a rerun cannot grant a second copy of the same quota. A title that
  exists with a different quota is refused instead of being silently rewritten.
- Every plan is forced to enabled = false after creation. The backend column
  default is true and GORM omits a false zero value on insert, so the create
  request alone cannot keep a plan disabled; the status API is called explicitly.
  The operator enables a plan in the admin UI once the retail prices are live.
- Never touches model pricing, balances or user subscriptions.
- v0.13.2 exposes no plan delete endpoint, so rollback is to disable the plan
  or edit it before it is ever enabled. Nothing is granted on creation.
"""
import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from commercial_quote import quote  # noqa: E402
from gateway import ROOT, API, Stack, credentials, write_json  # noqa: E402
from publish_plans import enabled_models, merge_models  # noqa: E402

PLANS_API = '/api/subscription/admin/plans'


def planned(config_path, catalog_path):
    """Derive the native plan payloads from the same quote as the public page."""
    config = json.loads(Path(config_path).read_text(encoding='utf-8'))
    catalog = json.loads(Path(catalog_path).read_text(encoding='utf-8'))
    required = enabled_models(catalog)
    models = merge_models(config)
    missing = sorted(set(required) - set(models))
    if missing:
        raise ValueError('missing supplier costs: ' + ', '.join(missing))
    result = quote(dict(config, models={name: models[name] for name in required}), required)

    payloads = []
    for index, plan in enumerate(result['plans'], start=1):
        source = next(p for p in config['plans'] if p['title'] == plan['title'])
        payloads.append({
            'title': plan['title'],
            # Never invent marketing copy: only an operator-supplied subtitle is used.
            'subtitle': source.get('subtitle') or '',
            'price_amount': float(plan['price_amount']),
            'currency': 'USD',
            'duration_unit': plan['duration_unit'],
            'duration_value': plan['duration_value'],
            'custom_seconds': 0,
            'enabled': False,
            'sort_order': index,
            'stripe_price_id': '',
            'creem_product_id': '',
            'max_purchase_per_user': 0,
            'upgrade_group': '',
            'total_amount': plan['total_amount'],
            'quota_reset_period': plan['quota_reset_period'],
            'quota_reset_custom_seconds': 0,
            '_manual_receipt_cny': plan['manual_receipt_cny'],
        })
    return config, payloads


def existing_plans(api):
    rows = api.ok(PLANS_API) or []
    return [row.get('plan', row) for row in rows]


def by_title(existing):
    return {str(row.get('title', '')).strip(): row for row in existing}


def conflicting_titles(payloads, existing):
    """Existing plans whose quota or duration differs.

    A rerun must not silently rewrite what a user may already hold, so a drift
    is reported for the operator to resolve instead of being overwritten.
    """
    known = by_title(existing)
    conflicts = []
    for payload in payloads:
        row = known.get(payload['title'].strip())
        if row is None:
            continue
        if (row.get('total_amount') != payload['total_amount']
                or row.get('duration_value') != payload['duration_value']
                or row.get('duration_unit') != payload['duration_unit']):
            conflicts.append(payload['title'])
    return sorted(conflicts)


def disable(api, plan_id):
    api.ok(PLANS_API + '/' + str(plan_id), {'enabled': False}, 'PATCH')


def create(config_path, catalog_path, evidence_path, apply_changes=False):
    config, payloads = planned(config_path, catalog_path)
    api = API(Stack().url)
    api.login(credentials(ROOT, 'admin'))
    before = existing_plans(api)
    conflicts = conflicting_titles(payloads, before)
    if conflicts:
        raise ValueError('plan exists with a different quota: ' + ', '.join(conflicts))

    summary = [{'title': p['title'], 'price_amount': p['price_amount'],
                'total_amount': p['total_amount'], 'manual_receipt_cny': p['_manual_receipt_cny'],
                'enabled': False} for p in payloads]
    if not apply_changes:
        return {'status': 'DRY_RUN', 'native_plans_before': len(before),
                'would_create': summary, 'changed_nothing': True}

    known = by_title(before)
    created, reused, forced = [], [], []
    for payload in payloads:
        title = payload['title'].strip()
        if title in known:
            plan_id = known[title].get('id')
            reused.append(title)
        else:
            body = {k: v for k, v in payload.items() if not k.startswith('_')}
            plan = api.ok(PLANS_API, {'plan': body})
            plan_id = plan.get('id')
            created.append({'id': plan_id, 'title': plan.get('title'),
                            'total_amount': plan.get('total_amount'),
                            'enabled_on_insert': plan.get('enabled')})
        if known.get(title, {}).get('enabled') is not False:
            disable(api, plan_id)
        forced.append(plan_id)

    after = existing_plans(api)
    rows = by_title(after)
    mismatch = sorted(p['title'] for p in payloads
                      if rows.get(p['title'].strip(), {}).get('enabled') is not False
                      or rows.get(p['title'].strip(), {}).get('total_amount') != p['total_amount'])
    evidence = {
        'status': 'PASS' if not mismatch else 'FAIL',
        'native_plans_before': len(before),
        'native_plans_after': len(after),
        'created': created,
        'reused': reused,
        'verified_disabled_ids': forced,
        'mismatches': mismatch,
        'rollback': 'plans are left disabled; enable them in the admin UI only after the '
                    'retail prices are live. v0.13.2 has no plan delete API.',
    }
    write_json(Path(evidence_path), evidence)
    return evidence


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('config', type=Path, nargs='?', default=ROOT / 'config/commercial.json')
    parser.add_argument('--catalog', type=Path, default=ROOT / 'docs/REAL_MODELS.json')
    parser.add_argument('--evidence', type=Path, default=ROOT / 'runtime/plans-create.json')
    parser.add_argument('--apply', action='store_true',
                        help='create and disable the plans; without it nothing is written')
    args = parser.parse_args()
    try:
        result = create(args.config, args.catalog, args.evidence, apply_changes=args.apply)
    except ValueError as error:
        parser.exit(2, 'BLOCKED: ' + str(error) + '\n')
    print(json.dumps(result, ensure_ascii=False, indent=2))
    if result.get('status') == 'FAIL':
        parser.exit(2)


if __name__ == '__main__':
    main()
