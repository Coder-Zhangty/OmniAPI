"""Configure rc.40 native EPay for automatic *wallet* top-up.

Reads operator-supplied merchant credentials from config/epay.json and writes
the options the backend needs before it will offer online top-up. Nothing is
invented: a credential that is missing, or a callback address that the payment
provider cannot reach, produces BLOCKED and leaves the instance untouched.

Why each check exists:
- go-epay appends "/submit.php" to PayAddress itself (path.Join), so a
  PayAddress that already ends in /submit.php becomes /submit.php/submit.php
  and every purchase 404s. This is the single most common setup mistake.
- Both callback URLs are derived from the callback address
  (CustomCallbackAddress, falling back to ServerAddress). A loopback or
  private address is unreachable from the provider, which means the payment is
  taken and the order is never credited.
- rc.40 also requires the operator to accept its payment compliance terms in
  the console. This script checks that acknowledgement but never records it on
  the operator's behalf.
- Native subscription EPay remains disabled. rc.40 sends plan.price_amount to
  the gateway without the CNY display conversion and can undercharge.

Money is reported, never silently adjusted. `--preview` prints, per plan, what
the browser shows the buyer and what the gateway will actually charge, because
those two numbers are not guaranteed to agree (see the notes in the output).
"""
import argparse
import getpass
import json
import os
import sys
from decimal import Decimal
from pathlib import Path
from urllib.parse import urlparse

sys.path.insert(0, str(Path(__file__).resolve().parent))
from gateway import ROOT, API, Stack, credentials, write_json  # noqa: E402

CONFIG_PATH = ROOT / 'config/epay.json'
SNAPSHOT_PATH = ROOT / 'runtime/epay-before-apply.json'
OPTION_KEYS = ('PayAddress', 'EpayId', 'EpayKey', 'CustomCallbackAddress')
PLACEHOLDER_TYPES = {'custom1', 'custom2', 'custom3'}
SAFE_PAY_METHODS = [
    {'name': '支付宝', 'type': 'alipay', 'color': '#1677ff', 'min_topup': '1'},
    {'name': '微信支付', 'type': 'wxpay', 'color': '#07c160', 'min_topup': '1'},
]
AMOUNT_OPTIONS = [1, 3, 5, 10, 20, 50]

CONFIG_TEMPLATE = {
    'pay_address': 'https://your-epay-gateway.example.com',
    'epay_id': '商户ID（服务商后台的 PID）',
    'epay_key': '商户密钥（服务商后台的 KEY，仅保存在服务端）',
    'custom_callback_address': '',
    'note': '推荐使用 --init-secret 隐藏输入；pay_address 只填基础地址，不要带 /submit.php。',
}


def base_address(value):
    """Validate the gateway base URL and return (normalised, error)."""
    text = (value or '').strip()
    if not text:
        return None, 'pay_address 未填写：易支付网关地址必须由服务商提供，脚本不会代填。'
    if 'submit.php' in text:
        return None, ('pay_address 不能包含 submit.php：go-epay 会自行追加 /submit.php，'
                      '重复拼接会让下单地址变成 /submit.php/submit.php。')
    parsed = urlparse(text)
    if parsed.scheme not in ('http', 'https') or not parsed.netloc:
        return None, 'pay_address 必须是完整的 http(s):// 地址。'
    return text.rstrip('/'), None


def publicly_reachable(address):
    """True when the callback host looks reachable from outside this machine."""
    host = (urlparse(address or '').hostname or '').lower()
    if not host:
        return False
    if host in ('localhost', '::1') or host.endswith('.local'):
        return False
    if host.startswith('127.') or host.startswith('10.') or host.startswith('192.168.'):
        return False
    if host.startswith('172.'):
        try:
            second = int(host.split('.')[1])
        except (IndexError, ValueError):
            return False
        if 16 <= second <= 31:
            return False
    return True


def pay_methods(options):
    """The configured payment methods, or [] when the JSON is unusable."""
    raw = options.get('PayMethods') or ''
    try:
        parsed = json.loads(raw)
    except (TypeError, ValueError):
        return []
    return parsed if isinstance(parsed, list) else []


def compliance_confirmed(options):
    """Return rc.40's explicit operator compliance acknowledgement state."""
    return str(options.get('payment_setting.compliance_confirmed', '')).lower() == 'true'


def secret_file_is_private(path):
    """On POSIX, refuse a merchant key file readable by group or other users."""
    if os.name == 'nt':
        return True
    return (path.stat().st_mode & 0o077) == 0


def blocked_reasons(config, options):
    """Everything that still prevents a working online top-up link."""
    reasons = []
    _, error = base_address(config.get('pay_address'))
    if error:
        reasons.append(error)
    if not (config.get('epay_id') or '').strip():
        reasons.append('epay_id 未填写：缺少商户 ID，后端不会初始化支付客户端。')
    if not (config.get('epay_key') or '').strip():
        reasons.append('epay_key 未填写：缺少商户密钥，无法签名。')
    server = (options.get('ServerAddress') or '').strip()
    if not server:
        reasons.append('服务器地址（ServerAddress）为空：后台要求先填服务器地址，回调地址由它推导。')
    elif not publicly_reachable(server):
        reasons.append('服务器地址 %s 不是公网可达地址：支付平台无法回调，付款后不会到账。' % server)
    callback = (config.get('custom_callback_address') or '').strip()
    if callback:
        _, callback_error = base_address(callback)
        if callback_error:
            reasons.append('custom_callback_address 无效：' + callback_error)
        elif not publicly_reachable(callback):
            reasons.append('custom_callback_address 不是公网可达地址：支付平台无法回调。')
    if not compliance_confirmed(options):
        reasons.append('尚未在后台确认 rc.40 支付合规条款：请由运营者本人在支付设置中阅读并确认。')
    return reasons


def warnings(config, options):
    """Things that will very likely bite the operator even though they pass."""
    notes = []
    server = (options.get('ServerAddress') or '').strip()
    if server.startswith('http://'):
        notes.append('服务器地址使用明文 HTTP：多数易支付平台要求回调走 HTTPS，'
                     '管理员密码与 API Key 也会明文传输。建议先上 HTTPS 再开支付。')
    if server and publicly_reachable(server) and urlparse(server).hostname.replace('.', '').isdigit():
        notes.append('服务器地址使用 IP：部分易支付平台只接受域名回调，下单前先确认服务商策略。')
    placeholders = sorted({m.get('type') for m in pay_methods(options)
                           if isinstance(m, dict) and m.get('type') in PLACEHOLDER_TYPES})
    if placeholders:
        notes.append('支付方式里仍有默认占位项 %s：它们只是模板，网关不支持时点下去会下单失败，'
                     '请改成服务商实际支持的渠道。' % ', '.join(placeholders))
    return notes


def callback_urls(options, config):
    """The exact URLs the provider will be asked to call back."""
    callback = (config.get('custom_callback_address') or '').strip() \
        or (options.get('ServerAddress') or '').strip()
    callback = callback.rstrip('/')
    server = (options.get('ServerAddress') or '').strip().rstrip('/')
    return {
        'callback_base': callback,
        'wallet_notify': callback + '/api/user/epay/notify',
        'wallet_return': server + '/usage-logs',
        'plan_notify': callback + '/api/subscription/epay/notify',
        'plan_return': callback + '/api/subscription/epay/return',
    }


def money_preview(api, options):
    """Per-plan comparison of displayed price and the amount the gateway charges."""
    fx = Decimal(str(options.get('USDExchangeRate') or 1))
    rows, problems = [], []
    try:
        plans = api.ok('/api/subscription/admin/plans')
    except Exception as error:  # noqa: BLE001 - report, never crash the plan list
        return rows, ['读取后台套餐失败：' + str(error)], fx
    for row in plans or []:
        plan = row.get('plan', row)
        amount = Decimal(str(plan.get('price_amount') or 0))
        charged = amount.quantize(Decimal('0.01'))
        displayed = amount * fx
        mismatch = charged != displayed.quantize(Decimal('0.01'))
        rows.append({
            'title': plan.get('title'),
            'price_amount': str(amount),
            'displayed_to_buyer': str(displayed.quantize(Decimal('0.01'))),
            'charged_by_gateway': str(charged),
            'enabled': bool(plan.get('enabled')),
            'mismatch': mismatch,
        })
        if mismatch:
            problems.append('套餐「%s」：页面显示 %s，网关实收 %s。' %
                            (plan.get('title'), displayed.quantize(Decimal('0.01')), charged))
    return rows, problems, fx


def public_option_snapshot(options):
    return {key: ('<configured>' if key == 'EpayKey' and options.get(key) else options.get(key))
            for key in OPTION_KEYS}


def snapshot(api):
    before = public_option_snapshot(api.options())
    write_json(SNAPSHOT_PATH, {'options': before,
                               'source': 'options before configuring the epay gateway'})
    return before


def disable_native_subscription_plans(api):
    rows = api.ok('/api/subscription/admin/plans') or []
    disabled = []
    for item in rows:
        plan = item.get('plan', item)
        if plan.get('enabled'):
            api.ok('/api/subscription/admin/plans/' + str(plan['id']), {'enabled': False}, 'PATCH')
            disabled.append(plan['id'])
    return disabled


def apply_config(config, api):
    """Write the credentials, verify them, and report what changed."""
    before = snapshot(api)
    disabled_plans = disable_native_subscription_plans(api)
    # Write non-secret policy first. Credentials are last so a partial run
    # cannot enable checkout with stale methods or unsafe subscription plans.
    api.option('PayMethods', json.dumps(SAFE_PAY_METHODS, ensure_ascii=False, separators=(',', ':')))
    api.option('payment_setting.amount_options', json.dumps(AMOUNT_OPTIONS, separators=(',', ':')))
    api.option('payment_setting.amount_discount', '{}')
    api.option('Price', '6.7489')
    api.option('MinTopUp', '1')
    api.option('TopupGroupRatio', '{"default":1,"svip":1,"vip":1}')
    api.option('PayAddress', base_address(config['pay_address'])[0])
    api.option('EpayId', str(config['epay_id']).strip())
    api.option('EpayKey', str(config['epay_key']).strip())
    callback = (config.get('custom_callback_address') or '').strip()
    if callback:
        api.option('CustomCallbackAddress', callback.rstrip('/'))
    after = api.options()
    # API.ok() already unwraps the envelope, so this is the payload itself.
    info = api.ok('/api/user/topup/info') or {}
    return {
        'before': before,
        'after': public_option_snapshot(after),
        'disabled_subscription_plan_ids': disabled_plans,
        'wallet_only': True,
        'enable_online_topup': bool(info.get('enable_online_topup')),
    }


def safe_status(api, options):
    """Return payment readiness without exposing PID, key, or gateway details."""
    info = api.ok('/api/user/topup/info') or {}
    rows = api.ok('/api/subscription/admin/plans') or []
    enabled_plans = []
    for item in rows:
        plan = item.get('plan', item)
        if plan.get('enabled'):
            enabled_plans.append({'id': plan.get('id'), 'title': plan.get('title')})
    return {
        'status': 'READY' if info.get('enable_online_topup') and compliance_confirmed(options)
        else 'BLOCKED',
        'server_address': options.get('ServerAddress'),
        'payment_compliance_confirmed': compliance_confirmed(options),
        'merchant_gateway_configured': all(bool(options.get(key))
                                           for key in ('PayAddress', 'EpayId', 'EpayKey')),
        'enable_online_topup': bool(info.get('enable_online_topup')),
        'visible_methods': [m.get('type') for m in (info.get('pay_methods') or [])],
        'enabled_subscription_plans': enabled_plans,
        'wallet_only_required': True,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('config', type=Path, nargs='?', default=CONFIG_PATH,
                        help='商户凭据配置（默认 config/epay.json）')
    parser.add_argument('--apply', action='store_true', help='写入线上配置；默认只做预演')
    parser.add_argument('--preview', action='store_true', help='只输出套餐金额对照，不校验凭据')
    parser.add_argument('--init-secret', action='store_true',
                        help='通过隐藏输入创建权限 0600 的商户凭据文件')
    parser.add_argument('--status', action='store_true', help='只输出不含密钥的支付就绪状态')
    args = parser.parse_args()

    if args.init_secret:
        if args.config.exists():
            parser.exit(2, 'BLOCKED: 凭据文件已存在，不会覆盖：' + str(args.config) + '\n')
        address = input('EPay 基础地址（不带 /submit.php）: ').strip()
        merchant_id = input('商户 PID: ').strip()
        merchant_key = getpass.getpass('商户 KEY（隐藏输入）: ').strip()
        payload = {'pay_address': address, 'epay_id': merchant_id,
                   'epay_key': merchant_key, 'custom_callback_address': ''}
        args.config.parent.mkdir(parents=True, exist_ok=True)
        args.config.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        if os.name != 'nt':
            os.chmod(args.config, 0o600)
        print(json.dumps({'status': 'CREATED', 'path': str(args.config),
                          'secret_values_omitted': True}, ensure_ascii=False, indent=2))
        return 0

    api = API(Stack().url)
    api.login(credentials(ROOT, 'admin'))
    options = api.options()

    if args.status:
        print(json.dumps(safe_status(api, options), ensure_ascii=False, indent=2))
        return 0

    if args.preview:
        rows, problems, fx = money_preview(api, options)
        print(json.dumps({'usd_exchange_rate': str(fx), 'plans': rows,
                          'problems': problems, 'status': 'BLOCKED' if problems else 'PASS'},
                         ensure_ascii=False, indent=2))
        return 0 if not problems else 2

    if not args.config.exists():
        print(json.dumps({'status': 'BLOCKED',
                          'reasons': ['缺少商户凭据文件 ' + str(args.config)],
                          'template': CONFIG_TEMPLATE}, ensure_ascii=False, indent=2))
        return 2
    config = json.loads(args.config.read_text(encoding='utf-8'))

    if args.apply and not secret_file_is_private(args.config):
        print(json.dumps({'status': 'BLOCKED', 'reasons': [
            '商户凭据文件权限过宽：请执行 chmod 600 ' + str(args.config)]},
            ensure_ascii=False, indent=2))
        return 2

    reasons = blocked_reasons(config, options)
    result = {
        'status': 'BLOCKED' if reasons else ('APPLIED' if args.apply else 'DRY_RUN'),
        'reasons': reasons,
        'warnings': warnings(config, options),
        'callbacks': callback_urls(options, config),
        'submit_url': (base_address(config.get('pay_address'))[0] or '') + '/submit.php'
        if base_address(config.get('pay_address'))[0] else '',
    }
    if reasons:
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 2

    rows, problems, fx = money_preview(api, options)
    result['usd_exchange_rate'] = str(fx)
    result['plans'] = rows
    result['amount_problems'] = problems

    if args.apply:
        result['verification'] = apply_config(config, api)
        if not result['verification']['enable_online_topup']:
            result['status'] = 'FAILED'
            result['reasons'] = ['配置已写入，但在线充值开关仍然关闭：请回读 PayAddress/EpayId/EpayKey 与 PayMethods。']
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0 if result['status'] in ('DRY_RUN', 'APPLIED') else 2


if __name__ == '__main__':
    sys.exit(main())
