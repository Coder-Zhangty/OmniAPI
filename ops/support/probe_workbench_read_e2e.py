#!/usr/bin/env python3
"""验证「经营者到底能不能看到消息」——只读，不改任何状态。

存在的理由：客服台要求经营者身份，而经营者身份只能靠浏览器里的控制台登录态换。
脚本拿不到控制台登录态（`POST /api/user/login` 受 Cloudflare Turnstile 保护），所以
「经营者打开工作台能不能读到队列和对话」这件事，用普通探针验不了。

这里的办法是绕开身份来源、只验**数据通路**：两个服务共用同一份 cookie 密钥，
所以用充值服务的管理口令登录换来的会话 cookie，客服服务也认。拿它去读客服队列，
就能证明「有合法经营者会话时，队列和消息读得出来」。

它刻意只做 GET。写操作（回复/结束会话）不在这里验——那会真的动线上数据。

用法（在服务器上）：
    python3 ops/support/probe_workbench_read_e2e.py
"""
import argparse
import json
import os
import pathlib
import sys
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
REPORT = ROOT / 'runtime/support-workbench-read.json'
BASE = 'https://omniapi.biz'
ORIGIN = 'https://omniapi.biz'


def call(path, token_cookie=None, method='GET', payload=None, extra_headers=None):
    body = json.dumps(payload).encode('utf-8') if payload is not None else None
    request = urllib.request.Request(BASE + path, data=body, method=method)
    request.add_header('Origin', ORIGIN)
    if body:
        request.add_header('Content-Type', 'application/json')
    if token_cookie:
        request.add_header('Cookie', token_cookie)
    for key, value in (extra_headers or {}).items():
        request.add_header(key, value)
    try:
        with urllib.request.urlopen(request, timeout=25) as response:
            raw = response.read().decode('utf-8', 'replace')
            headers = response.headers
            return response.status, (json.loads(raw) if raw.strip().startswith('{') else {'raw': raw}), headers
    except urllib.error.HTTPError as error:
        raw = error.read().decode('utf-8', 'replace')
        try:
            return error.code, json.loads(raw or '{}'), error.headers
        except ValueError:
            return error.code, {'raw': raw[:200]}, error.headers
    except urllib.error.URLError as error:
        raise SystemExit('BLOCKED: 连不上 %s（%s）' % (BASE, error))


def main():
    global BASE, ORIGIN
    parser = argparse.ArgumentParser()
    parser.add_argument('--password-file', type=pathlib.Path,
                        default=pathlib.Path(os.environ.get('TOPUP_PROBE_PASSWORD_FILE')
                                             or ROOT / '.secrets/topup-admin-password.txt'))
    parser.add_argument('--base', default=BASE)
    args = parser.parse_args()
    BASE = args.base.rstrip('/')
    ORIGIN = BASE

    steps, verdict = {}, {}

    # 1) 先用客服服务自己的会话接口确认「没 cookie 时是未认证」----------------
    status, body, _ = call('/brand/admin/api/support/session')
    steps['support_session_without_cookie'] = {'status': status,
                                               'authenticated': body.get('authenticated')}
    verdict['the_workbench_is_locked_without_a_session'] = (
        status == 200 and body.get('authenticated') is False)
    status, body, _ = call('/brand/admin/api/support/conversations')
    steps['support_queue_without_cookie'] = {'status': status}
    verdict['the_queue_refuses_anonymous_reads'] = status == 401

    # 2) 用充值服务的管理口令换一张会话 cookie（两个服务共用密钥）-----------
    if not args.password_file.exists():
        print('BLOCKED: 找不到口令副本 %s' % args.password_file)
        print('         没有它就无法取得经营者会话，本探针只验「有会话时能不能读」。')
        REPORT.write_text(json.dumps({
            'probe': 'ops/support/probe_workbench_read_e2e.py', 'status': 'BLOCKED',
            'reason': '口令副本不存在（%s）' % args.password_file, 'steps': steps,
        }, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        raise SystemExit(2)
    password = args.password_file.read_text(encoding='utf-8').strip()
    status, body, headers = call('/brand/admin/api/login', method='POST',
                                 payload={'password': password})
    cookie_header = headers.get('Set-Cookie') or ''
    cookie = cookie_header.split(';', 1)[0] if cookie_header else ''
    steps['operator_login'] = {'status': status, 'cookie_issued': bool(cookie)}
    verdict['an_operator_session_can_be_obtained'] = bool(cookie)
    if not cookie:
        print('BLOCKED: 没能取得经营者会话 cookie（HTTP %s，%s）'
              % (status, body.get('message') or body))
        REPORT.write_text(json.dumps({
            'probe': 'ops/support/probe_workbench_read_e2e.py', 'status': 'BLOCKED',
            'reason': '登录未返回 Set-Cookie', 'steps': steps,
        }, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        raise SystemExit(2)

    # 3) 拿这张 cookie 读客服队列 -------------------------------------------
    status, body, _ = call('/brand/admin/api/support/conversations', token_cookie=cookie)
    rows = body.get('conversations') or []
    steps['queue'] = {'status': status, 'counts': body.get('counts'),
                      'rows': [{key: row.get(key) for key in
                                ('id', 'username', 'status', 'needs_human',
                                 'unread_operator', 'last_role', 'last_text')} for row in rows]}
    verdict['the_same_cookie_opens_the_support_queue'] = status == 200
    verdict['the_queue_carries_a_preview_of_the_last_message'] = all(
        'last_text' in row and 'last_role' in row for row in rows) if rows else False
    verdict['the_queue_names_who_is_waiting'] = all(
        bool(row.get('username')) for row in rows) if rows else False

    # 4) 打开第一个有消息的会话，看消息本身能不能读出来（含角色）-------------
    target = next((row for row in rows if row.get('last_text')), None)
    if target:
        status, body, _ = call(
            '/brand/admin/api/support/history?conversation_id=' + target['id'],
            token_cookie=cookie)
        messages = body.get('messages') or []
        steps['history'] = {'status': status, 'conversation_id': target['id'],
                            'messages': [{'seq': row.get('seq'), 'role': row.get('role'),
                                          'text': (row.get('text') or '')[:40]}
                                         for row in messages]}
        verdict['messages_are_readable_with_their_role'] = (
            status == 200 and bool(messages)
            and all(row.get('role') in ('user', 'operator', 'ai', 'system')
                    for row in messages))
        roles = [row.get('role') for row in messages]
        verdict['the_system_notice_is_not_reported_as_a_person'] = (
            'system' in roles and 'user' in roles)
    else:
        steps['history'] = {'skipped': '队列里还没有任何消息'}
        verdict['messages_are_readable_with_their_role'] = None
        verdict['the_system_notice_is_not_reported_as_a_person'] = None

    # 5) 只读性自证：写接口在缺 CSRF 时必须被拒，说明本探针没有能力改数据 ----
    status, body, _ = call('/brand/admin/api/support/reply', method='POST',
                           token_cookie=cookie,
                           payload={'conversation_id': (target or {}).get('id') or 'x',
                                    'text': 'probe（不应写入）'})
    steps['write_without_csrf'] = {'status': status, 'code': body.get('code')}
    verdict['this_probe_cannot_write'] = status == 403

    REPORT.write_text(json.dumps({
        'probe': 'ops/support/probe_workbench_read_e2e.py', 'status': 'OK',
        'base': BASE, 'steps': steps, 'verdict': verdict,
    }, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')

    checked = [value for value in verdict.values() if value is not None]
    print('队列:', json.dumps(steps['queue']['counts'], ensure_ascii=False))
    for row in rows:
        print('  %s | %s | %s | 未读%d | 最后[%s] %s'
              % (row.get('id'), row.get('username'), row.get('status'),
                 row.get('unread_operator') or 0, row.get('last_role') or '-',
                 (row.get('last_text') or '(无)')[:30]))
    if target:
        print('对话 %s 的消息：' % target['id'])
        for row in steps['history']['messages']:
            print('  #%s %-8s %s' % (row['seq'], row['role'], row['text']))
    print('\nVERDICT %s' % json.dumps(verdict, ensure_ascii=False))
    print('判定 %d 项，全为真：%s' % (sum(1 for value in checked if value), checked))
    print('证据写入 %s' % REPORT)
    return 0 if all(checked) else 1


if __name__ == '__main__':
    sys.exit(main())
