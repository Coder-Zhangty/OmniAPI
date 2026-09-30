"""Publish the recharge catalog served at /brand/topup.json.

Reads the operator-approved recharge config and derives every amount, USD value
and native quota through the same conversion the plan pricing uses, so the
recharge page, the plans page and the billing expression cannot drift apart.

Safety rules:
- Never invents a rate, a contact or an amount. A catalog that cannot be paid
  safely is published in a non-collecting state, so the page cannot invite a
  payment the operator cannot honour.
- Refuses to publish a collecting catalog unless the operator declared
  accepting_payments and supplied a contact.
- Writes no quota anywhere: the native balance only moves when the operator
  verifies a receipt and issues a redemption code through credit_topup.py.
"""
import argparse
import json
import sys
from decimal import Decimal
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from commercial_quote import QUOTA_PER_UNIT, quota_from_usd, usd_from_cny  # noqa: E402
from gateway import ROOT, write_json  # noqa: E402

CATALOG_PATH = ROOT / 'nginx/brand/topup.json'
REQUIRED_OPERATOR_FIELDS = ('contact',)

LIMITATION = ('额度按人民币实收额等值折算，普通文本用量扣费；缓存、工具与媒体类费用'
              '按模型价格页另计。付款后需人工核验到账，不会自动到账。')


def plain_amount(value):
    """Render a Decimal without scientific notation, keeping meaningful digits."""
    return format(Decimal(str(value)).normalize(), 'f')


def amounts(config):
    """Configured preset amounts, in the operator's order, de-duplicated."""
    seen, result = set(), []
    for value in config.get('amounts_cny') or []:
        key = str(Decimal(str(value)).normalize())
        if key not in seen:
            seen.add(key)
            result.append(Decimal(str(value)))
    return result


def blocked_reasons(config, presets):
    """Explain precisely what still prevents publishing a payable catalog."""
    reasons = []
    if not config.get('cny_per_usd'):
        reasons.append('cny_per_usd 未填写：人民币结算换算率必须由经营者明确采用，不能用内置默认值。')
    if not presets:
        reasons.append('amounts_cny 为空：至少需要一个可充值的金额档位。')
    minimum = config.get('min_amount_cny')
    if minimum in (None, ''):
        reasons.append('min_amount_cny 未填写：无法判断最小充值金额。')
    else:
        too_small = [plain_amount(v) for v in presets if Decimal(str(v)) < Decimal(str(minimum))]
        if too_small:
            reasons.append('以下档位低于 min_amount_cny：' + ', '.join(too_small))
    qr = config.get('qr_path') or ''
    # ./nginx/brand is mounted at /usr/share/nginx/html/brand, so /brand/<name>
    # is exactly nginx/brand/<name> on disk.
    if not qr.startswith('/brand/'):
        reasons.append('qr_path 必须指向 /brand/ 下已挂载的收款码图片。')
    elif not (ROOT / 'nginx' / 'brand' / qr[len('/brand/'):]).exists():
        reasons.append('收款码图片不存在：' + qr)
    return reasons


def catalog(config, presets):
    """Build the exact object nginx/brand/topup.js consumes."""
    fx = Decimal(str(config['cny_per_usd']))
    fx_config = config.get('fx') or {}
    rows = []
    for value in presets:
        usd = usd_from_cny(value, fx)
        rows.append({'cny': plain_amount(value), 'usd': plain_amount(usd),
                     'quota': quota_from_usd(usd)})
    collecting = bool(config.get('accepting_payments')
                      and all(config.get(field) for field in REQUIRED_OPERATOR_FIELDS))
    max_age = int(fx_config.get('max_age_seconds') or 345600)
    return {
        'mode': 'DRAFT',
        'accepting_payments': collecting,
        'contact': config.get('contact') or '',
        'qr_path': config.get('qr_path') or '',
        'payment_mode': config.get('payment_mode', 'manual_verified_receipt'),
        'cny_per_usd': plain_amount(fx),
        'fx_enabled': bool(fx_config.get('enabled')),
        'fx_provider': fx_config.get('provider', 'xe'),
        # The page quotes the rate's own date on every order, so it must not
        # claim the rate is fixed when the operator switched to market data.
        'fx_max_age_days': max(1, max_age // 86400),
        'fx_fallback_max_age_days': max(
            1, int(fx_config.get('fallback_max_age_seconds') or 2592000) // 86400),
        'quota_per_usd': QUOTA_PER_UNIT,
        'min_amount_cny': plain_amount(config['min_amount_cny']),
        'amounts': rows,
        'settlement_note': config.get('settlement_note') or '',
        'limitation': LIMITATION,
    }


def publish(config_path, dry_run=False):
    config = json.loads(Path(config_path).read_text(encoding='utf-8'))
    presets = amounts(config)
    reasons = blocked_reasons(config, presets)
    if reasons:
        return {'status': 'BLOCKED', 'reasons': reasons,
                'amounts': len(presets)}
    if config.get('accepting_payments') and not config.get('contact'):
        raise ValueError('accepting_payments 为真时 contact 必须由经营者提供，脚本不会代填。')
    payload = catalog(config, presets)
    if dry_run:
        return {'status': 'DRY_RUN', 'catalog': payload}
    CATALOG_PATH.parent.mkdir(parents=True, exist_ok=True)
    write_json(CATALOG_PATH, payload)
    return {'status': 'PUBLISHED', 'path': str(CATALOG_PATH),
            'accepting_payments': payload['accepting_payments'], 'amounts': len(payload['amounts'])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('config', type=Path, nargs='?', default=ROOT / 'config/topup.json',
                        help='经营者填写的充值配置（默认 config/topup.json）')
    parser.add_argument('--dry-run', action='store_true', help='只输出草案，不写入 nginx/brand/topup.json')
    args = parser.parse_args()
    if not args.config.exists():
        parser.exit(2, 'BLOCKED: 缺少配置文件 ' + str(args.config) + '\n')
    try:
        result = publish(args.config, dry_run=args.dry_run)
    except ValueError as error:
        parser.exit(2, 'BLOCKED: ' + str(error) + '\n')
    print(json.dumps(result, ensure_ascii=False, indent=2))
    if result['status'] == 'BLOCKED':
        parser.exit(2)


if __name__ == '__main__':
    main()
