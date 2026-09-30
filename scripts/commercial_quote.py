"""Build a reviewable retail price proposal; never changes live billing."""
import argparse
import json
from decimal import Decimal, ROUND_CEILING, ROUND_DOWN
from pathlib import Path

QUOTA_PER_UNIT = 500000
RETAIL_PRICE_STEP = Decimal('0.01')


def positive(value, name):
    if value is None:
        raise ValueError(name + ' is required')
    result = Decimal(str(value))
    if not result.is_finite() or result <= 0:
        raise ValueError(name + ' must be finite and positive')
    return result


def usd_from_cny(amount_cny, cny_per_usd):
    """Convert a CNY receipt into the USD amount the native API stores."""
    return (positive(amount_cny, 'amount_cny') / positive(cny_per_usd, 'cny_per_usd')
            ).quantize(Decimal('0.000001'), rounding=ROUND_DOWN)


def quota_from_usd(usd):
    """Native quota for a USD amount; truncated, never rounded up."""
    return int((Decimal(str(usd)) * QUOTA_PER_UNIT).to_integral_value(rounding=ROUND_DOWN))

def quote(config, required_models):
    fx = positive(config.get('cny_per_usd'), 'cny_per_usd')
    if Decimal(str(config.get('markup'))) != 2:
        raise ValueError('Approved markup is 2')
    missing = sorted(set(required_models) - set(config.get('models', {})))
    if missing:
        raise ValueError('Missing supplier costs for: ' + ', '.join(missing))
    rates = {}
    for model in required_models:
        cost = config['models'][model]
        if cost.get('currency') not in ('USD', 'CNY'):
            raise ValueError('Each model cost requires currency USD or CNY')
        divisor = fx if cost['currency'] == 'CNY' else Decimal(1)
        # Public prices are shown with at most two decimals.  Round upward so
        # the displayed amount is also the exact billed amount and never drops
        # below the approved supplier-cost x 2 floor.
        inp = (positive(cost.get('input_per_million'), model + ' input cost') / divisor * 2
               ).quantize(RETAIL_PRICE_STEP, rounding=ROUND_CEILING)
        out = (positive(cost.get('output_per_million'), model + ' output cost') / divisor * 2
               ).quantize(RETAIL_PRICE_STEP, rounding=ROUND_CEILING)
        rates[model] = {'input_usd_per_million': str(inp), 'output_usd_per_million': str(out),
                        'billing_expr': f'v1:p * {inp} + c * {out}'}
    plans = []
    for plan in config['plans']:
        sale = positive(plan.get('sale_cny'), plan['title'] + ' sale_cny')
        days = int(plan['duration_days'])
        if days <= 0 or plan['quota_reset_period'] != 'never':
            raise ValueError('Use positive duration and no quota resets')
        usd = usd_from_cny(sale, fx)
        quota = quota_from_usd(usd)
        if quota <= 0 or usd > 9999:
            raise ValueError('Plan amount outside native limits')
        plans.append({'title': plan['title'], 'price_amount': str(usd), 'currency': 'USD',
                      'manual_receipt_cny': str(sale), 'duration_unit': 'day', 'duration_value': days,
                      'total_amount': quota, 'quota_reset_period': 'never', 'enabled': False,
                      'maximum_supplier_cost_cny_before_fees': str(sale / 2)})
    return {'mode': 'DRAFT', 'markup': 2, 'cny_per_usd': str(fx), 'models': rates, 'plans': plans,
            'limitation': 'Ordinary text only; cache/tool/media costs require separate validation. Fees and operating costs are not included.'}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('config', type=Path)
    parser.add_argument('--catalog', type=Path, default=Path(__file__).resolve().parents[1]/'docs/REAL_MODELS.json')
    args = parser.parse_args()
    config = json.loads(args.config.read_text(encoding='utf-8'))
    catalog = json.loads(args.catalog.read_text(encoding='utf-8'))
    try:
        print(json.dumps(quote(config, [r['model'] for r in catalog['results'] if r['status']=='PASS']), ensure_ascii=False, indent=2))
    except ValueError as error:
        parser.exit(2, 'BLOCKED: '+str(error)+'\n')

if __name__ == '__main__': main()
