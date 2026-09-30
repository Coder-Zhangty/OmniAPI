#!/usr/bin/env python3
"""End-to-end check of direct wallet credit, without touching the live queue.

Runs a real Service instance against the real gateway but with temporary
events/ledger files, so the operator's pending list is never modified. The
tester account's balance is restored at the end.
"""
import json
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path('/opt/ai-gateway')
sys.path.insert(0, str(ROOT / 'scripts'))
sys.path.insert(0, str(ROOT / 'ops' / 'topup_watch'))
import topup_watch  # noqa: E402
from gateway import API, ROOT as GATEWAY_ROOT, credentials  # noqa: E402

TARGET = 2
BASE = 'https://omniapi.biz'
LIVE_EVENTS = ROOT / 'runtime/topup-requests.jsonl'
api = API(BASE)
api.login(credentials(GATEWAY_ROOT, 'admin'))


def quota():
    time.sleep(1.5)  # 网关有用户缓存，读值可能滞后
    return api.ok('/api/user/%d' % TARGET).get('quota')


def live_counts():
    sys.path.insert(0, str(ROOT / 'scripts'))
    import topup_requests as queue
    return queue.counts(LIVE_EVENTS)


tmp = tempfile.mkdtemp()
events = Path(tmp) / 'requests.jsonl'
ledger = Path(tmp) / 'ledger.json'
service = topup_watch.Service(ROOT / '.secrets/topup-admin.json', events, ledger,
                              ROOT / 'config/topup.json', gateway_url=BASE)

live_before = live_counts()
before = quota()
print('入账前 gw_tester 额度:', before, '| 线上真实队列:', live_before)

probe_user = {'id': TARGET, 'username': 'probe', 'display_name': 'Probe',
              'public_id': ''}
quote = service.quote({'method': 'cny-qr', 'amount': '10'})
submitted = service.submit({'quote_id': quote['id']}, '127.0.0.1', probe_user)
record = submitted['request']
print('提交:', record['id'], '| 期望入账额度', record['quota'])
summary = service.summary()
print('待核验:', summary['counts'],
      '| 行:', [(r['id'], r['uid'], r['cny'], r.get('username')) for r in summary['pending']])

result = service.fulfill(record['id'], transaction='ZZ-BAL-%d' % int(time.time()))
issued = result['issued']['issued']
print('入账结果:', json.dumps({k: issued.get(k) for k in
                            ('via', 'quota_before', 'quota_after', 'balance_verified',
                             'username', 'transaction_id')}, ensure_ascii=False))
print('服务消息:', result['issued']['note'])

after = quota()
delta = (after or 0) - (before or 0)
print('入账后 gw_tester 额度:', after, '| delta =', delta, '| 期望', record['quota'])

try:
    service.fulfill(record['id'], transaction='ZZ-BAL-again')
    twice = '未被拒（异常）'
except ValueError as error:
    twice = str(error)
print('重复入账:', twice)

records = json.loads(Path(ledger).read_text(encoding='utf-8'))['records']
live_after = live_counts()
print('临时台账记录数:', len(records), '| 线上真实队列（应不变）:', live_after)

api.request('/api/user/manage',
            {'id': TARGET, 'action': 'add_quota', 'value': before, 'mode': 'override'}, 'POST')
restored = quota()
print('已还原额度:', restored, '|', '一致' if restored == before else '**不一致**')

verdict = {
    'submitted': bool(record.get('id')),
    'credit_landed': delta == record['quota'],
    'ledger_recorded': len(records) == 1 and records[0].get('via') == 'balance',
    'duplicate_blocked': '已处理' in twice,
    'balance_restored': restored == before,
    'live_queue_untouched': live_before == live_after,
}
print()
print('VERDICT ' + json.dumps(verdict, ensure_ascii=False))
Path('/opt/ai-gateway/runtime/topup-balance-e2e.json').write_text(json.dumps({
    'mode': 'balance（直接给用户钱包加额度）',
    'steps': {
        '提交': record,
        '入账前额度': before,
        '入账后额度': after,
        'delta': delta,
        '期望': record['quota'],
        '入账记录': {k: issued.get(k) for k in
                  ('via', 'quota_before', 'quota_after', 'balance_verified', 'transaction_id')},
        '重复入账': twice,
        '额度已还原': restored,
        '线上真实队列未改动': live_before == live_after,
    },
    'verdict': verdict,
    'note': '探针使用临时 events/ledger，未触碰经营者真实的待核验队列；测试账号额度已还原。',
}, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
