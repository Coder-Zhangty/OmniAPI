"""Publish the retail plan catalog served at /brand/plans.json.

Reads the operator-approved commercial config, derives the draft quote through
commercial_quote.quote(), and writes the catalog the branded plans page fetches.

Safety rules:
- Never invents prices, merchant id or contact details. Anything absent keeps the
  catalog in a non-collecting state, so the page cannot invite payment.
- Refuses to publish a collecting catalog unless every plan is fully priced and
  the operator explicitly declared accepting_payments.
- Does not touch live billing: native subscription plans stay disabled until an
  administrator creates them in the backend.
"""
import argparse
import json
import sys
from decimal import Decimal
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from commercial_quote import quote  # noqa: E402
from gateway import ROOT, write_json  # noqa: E402

CATALOG_PATH = ROOT / 'nginx/brand/plans.json'
SUPPLIER_COSTS = ROOT / 'config/supplier-costs.json'
DEFAULT_CATALOG = ROOT / 'docs/REAL_MODELS.json'

REQUIRED_OPERATOR_FIELDS = ('contact',)


def plain_amount(value):
    """Render a Decimal without scientific notation, keeping meaningful digits."""
    return format(Decimal(str(value)).normalize(), 'f')


def enabled_models(models_catalog):
    """Model names that passed real upstream verification and may be sold."""
    return [row['model'] for row in models_catalog['results'] if row['status'] == 'PASS']


def supplier_costs():
    """Convert config/supplier-costs.json into the quote config model shape."""
    if not SUPPLIER_COSTS.exists():
        return {}
    raw = json.loads(SUPPLIER_COSTS.read_text(encoding='utf-8'))
    currency = raw.get('currency', 'USD')
    return {name: {'currency': currency,
                   'input_per_million': price[0],
                   'output_per_million': price[1]}
            for name, price in raw.get('models', {}).items()}


def usable_cost(cost):
    """True only when a cost entry carries both prices and a supported currency."""
    if not isinstance(cost, dict):
        return False
    if cost.get('currency') not in ('USD', 'CNY'):
        return False
    return all(isinstance(cost.get(key), (int, float, str))
               for key in ('input_per_million', 'output_per_million'))


def merge_models(config):
    """Operator model costs win; verified supplier costs fill the rest.

    Placeholder entries left null in a draft config are ignored instead of
    overwriting an already verified supplier cost.
    """
    merged = {name: cost for name, cost in supplier_costs().items() if usable_cost(cost)}
    for name, cost in (config.get('models') or {}).items():
        if usable_cost(cost):
            merged[name] = cost
    return merged


def catalog(config, models, accepting, quote_result):
    """Build the exact object nginx/brand/plans.js consumes."""
    plans = []
    for index, plan in enumerate(quote_result['plans'], start=1):
        source = next(p for p in config['plans'] if p['title'] == plan['title'])
        plans.append({
            'id': index,
            'title': plan['title'],
            'duration_days': int(plan['duration_value']),
            'sale_cny': plain_amount(plan['manual_receipt_cny']),
            'quota_usd': plan['price_amount'],
            'native_total_amount': plan['total_amount'],
            'enabled_in_backend': bool(plan['enabled']),
            'source_plan_title': source['title'],
        })
    collecting = bool(accepting and all(config.get(field) for field in REQUIRED_OPERATOR_FIELDS))
    return {
        'mode': quote_result['mode'],
        'accepting_payments': collecting,
        'cny_per_usd': str(Decimal(quote_result['cny_per_usd']).normalize()),
        'markup': quote_result['markup'],
        'merchant_id': config.get('merchant_id') or '',
        'contact': config.get('contact') or '',
        'payment_mode': config.get('payment_mode', 'manual_verified_receipt'),
        'plans': plans,
        'limitation': quote_result['limitation'],
    }


def blocked_reasons(config, models, required):
    """Explain precisely what still prevents publishing a priced catalog."""
    reasons = []
    if not config.get('cny_per_usd'):
        reasons.append('cny_per_usd 未填写：人民币结算换算率必须由经营者明确采用，不能用内置默认值。')
    missing_cost = sorted(set(required) - set(models))
    if missing_cost:
        reasons.append('缺少已验证模型的采购成本：' + ', '.join(missing_cost))
    for plan in config.get('plans') or []:
        if not plan.get('sale_cny'):
            reasons.append('套餐「' + plan['title'] + '」缺 sale_cny 实际售价。')
    return reasons


def publish(config_path, models_catalog_path, dry_run=False):
    config = json.loads(Path(config_path).read_text(encoding='utf-8'))
    models_catalog = json.loads(Path(models_catalog_path).read_text(encoding='utf-8'))
    required = enabled_models(models_catalog)
    models = merge_models(config)

    reasons = blocked_reasons(config, models, required)
    if reasons:
        return {'status': 'BLOCKED', 'reasons': reasons,
                'verified_models': len(required), 'priced_models': len(set(required) & set(models))}

    config = dict(config, models={name: models[name] for name in required})
    result = quote(config, required)

    for field in REQUIRED_OPERATOR_FIELDS:
        if config.get('accepting_payments') and not config.get(field):
            raise ValueError('accepting_payments 为真时 ' + field + ' 必须由经营者提供，脚本不会代填。')

    payload = catalog(config, models, config.get('accepting_payments'), result)
    if dry_run:
        return {'status': 'DRY_RUN', 'catalog': payload}
    CATALOG_PATH.parent.mkdir(parents=True, exist_ok=True)
    write_json(CATALOG_PATH, payload)
    return {'status': 'PUBLISHED', 'path': str(CATALOG_PATH),
            'accepting_payments': payload['accepting_payments'], 'plans': len(payload['plans'])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('config', type=Path, nargs='?', default=ROOT / 'config/commercial.json',
                        help='经营者填写的商业配置（默认 config/commercial.json）')
    parser.add_argument('--catalog', type=Path, default=DEFAULT_CATALOG,
                        help='真实模型验收结果（默认 docs/REAL_MODELS.json）')
    parser.add_argument('--dry-run', action='store_true', help='只输出草案，不写入 nginx/brand/plans.json')
    args = parser.parse_args()

    if not args.config.exists():
        parser.exit(2, 'BLOCKED: 缺少配置文件 ' + str(args.config) + '\n')
    try:
        result = publish(args.config, args.catalog, dry_run=args.dry_run)
    except ValueError as error:
        parser.exit(2, 'BLOCKED: ' + str(error) + '\n')
    print(json.dumps(result, ensure_ascii=False, indent=2))
    if result['status'] == 'BLOCKED':
        parser.exit(2)


if __name__ == '__main__':
    main()
