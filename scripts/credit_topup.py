"""Turn a verified CNY receipt into credit in the user's wallet, once per transaction.

Two delivery modes:

- `balance`（默认）：直接给用户的额度加钱，调用原生管理接口
  `POST /api/user/manage {action: add_quota, mode: add}`。这是服务端的原子增量，
  没有读改写竞态；用户刷新钱包就能看到，不需要再自己去兑换。
- `code`：生成一张原生兑换码，由用户在钱包页自助兑换。多一步用户操作，
  保留作应急手段（例如管理接口行为变化时）。

The operator checks the actual money arrival in their own collection channel,
then runs this script. It converts the receipt with the same rate the recharge
page advertises and refuses a transaction id that was already handled.

Why the official admin API instead of writing the database:
- the gateway caches user records in memory, so a direct SQL write can be lost or
  overwritten; the API path goes through the layer that maintains that cache;
- `mode: add` is an atomic increment, so two concurrent credits cannot lose one.

Safety rules:
- Dry run unless --apply is passed. A dry run prints what would be issued.
- Refuses a duplicate transaction id, so a receipt cannot be credited twice.
- Refuses a user id that does not exist, and an amount below the configured min.
- Reads the balance before and after and records both in the ledger; if the
  change is not visible within the wait window it is flagged, never retried
  blindly (the ledger is written either way, so a retry cannot double-credit).
- Never invents a rate: the receipt is converted with config/topup.json.
"""
import argparse
import datetime as dt
import json
import os
import sys
import tempfile
import time
from decimal import Decimal
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from commercial_quote import quota_from_usd, usd_from_cny  # noqa: E402
from gateway import ROOT, API, Stack, credentials, write_json  # noqa: E402

REDEMPTION_API = '/api/redemption/'
QUOTA_API = '/api/user/manage'
DEFAULT_LEDGER = ROOT / 'runtime/topup-ledger.json'
# How long to wait for the new balance to become readable. The gateway keeps a
# user cache, so the value can lag the write by a few seconds.
BALANCE_WAIT_ATTEMPTS = 8
BALANCE_WAIT_SECONDS = 1.5
# The native validation rejects a name outside 1-20 characters (counted per
# character, so a CJK character costs one). The full mapping code -> transaction
# lives in the ledger, so the name only needs to be a recognisable hint.
MAX_CODE_NAME = 20


def plain(value):
    return format(Decimal(str(value)).normalize(), 'f')


def load_ledger(path):
    path = Path(path)
    if not path.exists():
        return {'records': []}
    return json.loads(path.read_text(encoding='utf-8'))


def save_ledger(path, ledger):
    """Durably replace the financial ledger without exposing a partial JSON file."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix=path.name + '.', dir=str(path.parent))
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as handle:
            json.dump(ledger, handle, ensure_ascii=False, indent=2)
            handle.write('\n')
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(name, 0o640)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def reserve(ledger_path, ledger, record, allow_replay=False):
    """Write the idempotency key before touching the wallet.

    An ``applied`` record may be replayed by the request service solely to finish
    its terminal event after a crash. A ``prepared`` record is deliberately
    ambiguous and needs manual reconciliation; retrying it could add twice.
    """
    existing = find_record(ledger, record['transaction_id'])
    if existing:
        if existing.get('state') == 'applied' and allow_replay:
            return existing
        state = existing.get('state') or ('applied' if existing.get('applied') else 'unknown')
        raise ValueError('transaction already recorded with state ' + state)
    record['state'] = 'prepared'
    record['applied'] = False
    ledger.setdefault('records', []).append(record)
    ledger['updated_at'] = record['created_at']
    save_ledger(ledger_path, ledger)
    return None


def finish(ledger_path, ledger, record):
    record['state'] = 'applied'
    record['applied'] = True
    ledger['updated_at'] = iso = dt.datetime.now(dt.timezone.utc).isoformat(timespec='seconds')
    record['applied_at'] = iso
    save_ledger(ledger_path, ledger)


def find_record(ledger, transaction_id):
    key = str(transaction_id).strip()
    return next((r for r in ledger.get('records', []) if str(r.get('transaction_id', '')).strip() == key), None)


def plan_record(config, user_id, amount_cny, transaction_id):
    """Validate the request and derive the amount to credit. Raises on refusal."""
    transaction_id = str(transaction_id or '').strip()
    if not transaction_id:
        raise ValueError('transaction_id is required; a receipt must be traceable')
    if not config.get('cny_per_usd'):
        raise ValueError('config cny_per_usd is missing; the rate must come from the operator')
    amount = Decimal(str(amount_cny))
    minimum = Decimal(str(config.get('min_amount_cny') or 0))
    if amount < minimum:
        raise ValueError('amount below the configured minimum of ' + plain(minimum) + ' CNY')
    usd = usd_from_cny(amount, config['cny_per_usd'])
    quota = quota_from_usd(usd)
    if quota <= 0:
        raise ValueError('derived quota is not positive; check the amount and rate')
    return {
        'transaction_id': transaction_id,
        'user_id': int(user_id),
        'amount_cny': plain(amount),
        'usd': plain(usd),
        'quota': quota,
        'currency': 'USD',
        'rate_cny_per_usd': plain(config['cny_per_usd']),
    }


def code_name(record):
    """Short, traceable name; the native API caps names at 20 characters."""
    tail = str(record['transaction_id'])[-10:]
    name = '充值%s元-%s' % (record['amount_cny'], tail)
    return name[:MAX_CODE_NAME]


def describe(record):
    if record.get('code'):
        return '兑换码 ' + str(record['code'])
    return '%s 直接入账' % record.get('via', 'balance')


def read_quota(api, user_id):
    return api.ok('/api/user/%d' % user_id).get('quota')


def add_balance(api, user_id, quota):
    """Add quota through the native admin API, then wait until it is readable.

    `mode: add` is an atomic increment executed by the gateway, so two credits
    can never clobber each other. The read-back exists only to prove the money
    landed: the gateway keeps a user cache, so the new value can lag slightly.
    """
    before = read_quota(api, user_id)
    api.ok(QUOTA_API, {'id': int(user_id), 'action': 'add_quota',
                       'value': int(quota), 'mode': 'add'})
    expected = (before or 0) + int(quota)
    after = before
    for _ in range(BALANCE_WAIT_ATTEMPTS):
        time.sleep(BALANCE_WAIT_SECONDS)
        after = read_quota(api, user_id)
        if after == expected:
            break
    return before, after, expected


def credit(config_path, ledger_path, user_id, amount_cny, transaction_id, apply_changes=False,
           api=None, via='balance', allow_replay=False):
    """Credit one receipt. Pass `api` to reuse a logged-in session.

    POST /api/user/login sits in New API's critical bucket (20 requests per 20
    minutes per client IP). A long-running caller such as the recharge service
    logs in once and reuses the session, so a burst of receipts cannot exhaust
    that budget; the CLI keeps logging in per run.
    """
    if via not in ('balance', 'code'):
        raise ValueError("via must be 'balance' or 'code'")
    config = json.loads(Path(config_path).read_text(encoding='utf-8'))
    record = plan_record(config, user_id, amount_cny, transaction_id)
    ledger = load_ledger(ledger_path)
    if api is None:
        api = API(Stack().url)
        api.login(credentials(ROOT, 'admin'))
    user = api.ok('/api/user/%d' % record['user_id'])
    if not user or not user.get('id'):
        raise ValueError('user %d does not exist' % record['user_id'])
    record['username'] = user.get('username', '')
    record['via'] = via
    record['created_at'] = dt.datetime.now(dt.timezone.utc).isoformat(timespec='seconds')
    if via == 'code':
        record['name'] = code_name(record)
        if not 0 < len(record['name']) <= MAX_CODE_NAME:
            raise ValueError('generated redemption code name is outside the 1-%d character limit'
                             % MAX_CODE_NAME)

    if not apply_changes:
        return {'status': 'DRY_RUN', 'would_issue': record, 'ledger_written': False}
    replay = reserve(ledger_path, ledger, record, allow_replay=allow_replay)
    if replay:
        return {'status': 'REPLAY', 'issued': replay, 'ledger_written': True,
                'note': '钱包入账已经完成；本次仅恢复请求终态。'}

    if via == 'balance':
        before, after, expected = add_balance(api, record['user_id'], record['quota'])
        record['quota_before'] = before
        record['quota_after'] = after
        record['balance_verified'] = after == expected
        if after == expected:
            note = '额度已直接计入用户钱包，用户刷新钱包页即可看到。'
        else:
            note = ('管理接口返回成功，但 %d 秒内读到余额仍是 %s（期望 %s）。'
                    '请到后台核对用户额度后再决定是否需要补发；本次已记入台账，重试会被拒绝。'
                    % (int(BALANCE_WAIT_ATTEMPTS * BALANCE_WAIT_SECONDS), after, expected))
    else:
        body = {'name': record['name'], 'quota': record['quota'], 'count': 1, 'expired_time': 0}
        keys = api.ok(REDEMPTION_API, body)
        code = keys[0] if isinstance(keys, list) and keys else ''
        record['code'] = code
        created = [r for r in api.ok(REDEMPTION_API + '?p=1&page_size=100').get('items', [])
                   if r.get('key') == code]
        record['redemption_id'] = created[0]['id'] if created else None
        record['redeemed'] = False
        note = '把兑换码发给用户，用户在本站钱包页「兑换码充值」输入后额度到账。'

    ledger['cny_per_usd'] = record['rate_cny_per_usd']
    finish(ledger_path, ledger, record)
    return {'status': 'PASS', 'issued': record, 'ledger_written': True, 'note': note}


def credit_usd(ledger_path, user_id, amount_usd, transaction_id, apply_changes=False,
               api=None, metadata=None, allow_replay=False):
    """Credit an already reconciled USD net receipt exactly once.

    Payoneer and stablecoins settle in USD.  CNY quote orders are converted with
    their immutable quote rate before reaching this function.  `metadata` is
    copied into the audit ledger, but cannot override accounting fields.
    """
    transaction_id = str(transaction_id or '').strip()
    if not transaction_id:
        raise ValueError('transaction_id is required; a receipt must be traceable')
    usd = Decimal(str(amount_usd)).quantize(Decimal('0.000001'))
    if not usd.is_finite() or usd <= 0:
        raise ValueError('credited USD amount must be positive')
    quota = quota_from_usd(usd)
    if quota <= 0:
        raise ValueError('derived quota is not positive')
    ledger = load_ledger(ledger_path)
    if api is None:
        api = API(Stack().url)
        api.login(credentials(ROOT, 'admin'))
    user = api.ok('/api/user/%d' % int(user_id))
    if not user or not user.get('id'):
        raise ValueError('user %d does not exist' % int(user_id))
    record = dict(metadata or {})
    record.update({'transaction_id': transaction_id, 'user_id': int(user_id),
                   'username': user.get('username', ''), 'usd': plain(usd),
                   'quota': quota, 'currency': 'USD', 'via': 'balance',
                   'created_at': dt.datetime.now(dt.timezone.utc).isoformat(timespec='seconds')})
    if not apply_changes:
        return {'status': 'DRY_RUN', 'would_issue': record, 'ledger_written': False}
    replay = reserve(ledger_path, ledger, record, allow_replay=allow_replay)
    if replay:
        return {'status': 'REPLAY', 'issued': replay, 'ledger_written': True,
                'note': '钱包入账已经完成；本次仅恢复请求终态。'}
    before, after, expected = add_balance(api, record['user_id'], record['quota'])
    record.update({'quota_before': before, 'quota_after': after,
                   'balance_verified': after == expected})
    finish(ledger_path, ledger, record)
    note = ('额度已直接计入用户钱包，用户刷新钱包页即可看到。' if after == expected else
            '管理接口已返回成功，但余额读回值未达到预期；请人工核对，台账已阻止重复入账。')
    return {'status': 'PASS', 'issued': record, 'ledger_written': True, 'note': note}


def sync(ledger_path):
    """Report the status of issued codes. Balance-mode records need no follow-up."""
    ledger = load_ledger(ledger_path)
    api = API(Stack().url)
    api.login(credentials(ROOT, 'admin'))
    rows = {r.get('key'): r for r in api.ok(REDEMPTION_API + '?p=1&page_size=100').get('items', [])}
    for record in ledger.get('records', []):
        if not record.get('code'):
            record['settled'] = record.get('balance_verified', True)
            continue
        row = rows.get(record.get('code'))
        record['redeemed'] = bool(row and row.get('used_user_id'))
        record['code_status'] = row.get('status') if row else None
    ledger['updated_at'] = dt.datetime.now(dt.timezone.utc).isoformat(timespec='seconds')
    write_json(Path(ledger_path), ledger)
    return {'status': 'PASS', 'records': ledger.get('records', [])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--user-id', type=int, help='充值的本站用户 ID')
    parser.add_argument('--amount-cny', help='实际到账的人民币金额')
    parser.add_argument('--transaction', help='付款交易单号，用于幂等与对账')
    parser.add_argument('--config', type=Path, default=ROOT / 'config/topup.json')
    parser.add_argument('--ledger', type=Path, default=DEFAULT_LEDGER)
    parser.add_argument('--via', choices=('balance', 'code'), default='balance',
                        help='balance=直接给用户钱包加额度（默认）；code=生成兑换码让用户自助兑换')
    parser.add_argument('--apply', action='store_true', help='真正入账；不加则只预演')
    parser.add_argument('--list', action='store_true', help='列出台账并同步兑换状态')
    args = parser.parse_args()
    try:
        if args.list:
            result = sync(args.ledger)
        else:
            for field in ('user_id', 'amount_cny', 'transaction'):
                if getattr(args, field) in (None, ''):
                    parser.error('需要 --user-id、--amount-cny 与 --transaction（或使用 --list）')
            result = credit(args.config, args.ledger, args.user_id, args.amount_cny,
                            args.transaction, apply_changes=args.apply, via=args.via)
    except ValueError as error:
        parser.exit(2, 'BLOCKED: ' + str(error) + '\n')
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
