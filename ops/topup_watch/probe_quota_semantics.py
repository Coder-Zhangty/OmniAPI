#!/usr/bin/env python3
"""Find out what POST /api/user/manage {action: add_quota} really does.

Runs against the throwaway tester account. Prints the observed delta for each
variant so the credit path can be built on measured behaviour, not on a guess.
"""
import json
import sys
import time
from pathlib import Path

ROOT = Path('/opt/ai-gateway')
sys.path.insert(0, str(ROOT / 'scripts'))
from gateway import API, ROOT as GATEWAY_ROOT, credentials  # noqa: E402

TARGET = 2  # gw_tester
api = API('https://omniapi.biz')
api.login(credentials(GATEWAY_ROOT, 'admin'))


def quota():
    time.sleep(1.5)  # 缓存同步给点时间
    return api.ok('/api/user/%d' % TARGET).get('quota')


def attempt(label, body):
    before = quota()
    status, payload, _ = api.request('/api/user/manage', body, 'POST')
    after = quota()
    print('%-46s HTTP %-4s delta=%-12s %s' % (
        label, status, (after or 0) - (before or 0),
        json.dumps(payload, ensure_ascii=False)[:110]))
    return before, after


print('初始额度:', quota())
print()

attempt('add_quota value=1000（无 mode）', {'id': TARGET, 'action': 'add_quota', 'value': 1000})
attempt('add_quota value=-1000（无 mode）', {'id': TARGET, 'action': 'add_quota', 'value': -1000})
attempt('add_quota value=1000 mode=add', {'id': TARGET, 'action': 'add_quota', 'value': 1000, 'mode': 'add'})
attempt('add_quota value=-1000 mode=add', {'id': TARGET, 'action': 'add_quota', 'value': -1000, 'mode': 'add'})
attempt('add_quota value=1000 mode=override',
        {'id': TARGET, 'action': 'add_quota', 'value': 1000, 'mode': 'override'})
attempt('add_quota value=0（探测是否覆盖）', {'id': TARGET, 'action': 'add_quota', 'value': 0})

print()
print('结束额度:', quota())
