#!/usr/bin/env python3
"""线上验收：客服链路的公网行为（只读，不产生任何会话）。

只检查「单元测试证明不了」的那一层——真实部署路径：

    Internet → nginx（SSE 专用 location）→ 宿主机的 omniapi-support

包括 nginx 是否真的把 /brand/api/support/ 转到了客服服务（而不是充值服务）、
公网上的身份与写保护是否如设计那样拒绝、站点是否注入了客服入口。

完整的「用户发起 → 经营者回复 → 双方实时收到」流程由
`tests/test_support_service.py` 在临时实例上端到端驱动（31 项），那里可以造出
真实登录态；真实浏览器令牌受 Turnstile 保护，脚本拿不到，所以不在本探针里伪造。

用法（服务器上）：python3 ops/support/probe_support_e2e.py
"""
import json
import sys
import urllib.error
import urllib.request
from pathlib import Path

BASE = 'https://omniapi.biz'
REPORT = Path('/opt/ai-gateway/runtime/support-e2e.json')
verdict = {}
steps = {}


def call(path, method='GET', payload=None, headers=None, base=None):
    body = json.dumps(payload).encode('utf-8') if payload is not None else None
    request = urllib.request.Request((base or BASE) + path, data=body, method=method)
    if body:
        request.add_header('Content-Type', 'application/json')
    for name, value in (headers or {}).items():
        request.add_header(name, value)
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            raw = response.read().decode('utf-8', 'replace')
            try:
                return response.status, json.loads(raw or '{}')
            except ValueError:
                return response.status, {'raw': raw[:200]}
    except urllib.error.HTTPError as error:
        raw = error.read().decode('utf-8', 'replace')
        try:
            return error.code, json.loads(raw or '{}')
        except ValueError:
            return error.code, {'raw': raw[:200]}


def page(path, base=None):
    """Fetch a page as text. `call()` truncates non-JSON bodies to 200 chars,
    which is fine for an error message and useless for finding an injected
    script tag at the end of the document."""
    request = urllib.request.Request((base or BASE) + path)
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return response.status, response.read().decode('utf-8', 'replace')
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode('utf-8', 'replace')


# 1) 静态页面与入口注入 ------------------------------------------------
for path in ('/brand/support.html', '/brand/support-admin.html',
             '/brand/support.js', '/brand/support-admin.js', '/brand/support-widget.js'):
    status, _ = call(path)
    steps[path] = status
    verdict['page_%s' % path.split('/')[-1].replace('.', '_')] = status == 200

status, console = page('/console')
verdict['console_injects_the_support_entry'] = (status == 200 and 'support-widget.js' in console)

# 2) 公共配置：只有该公开的字段 ---------------------------------------
status, config = call('/brand/api/support/config')
steps['config'] = {'http': status, **{key: config.get(key) for key in
                                      ('enabled', 'ai_enabled', 'notify_channel', 'max_text')}}
verdict['config_is_public'] = status == 200 and config.get('enabled') is True
verdict['config_has_privacy_note'] = bool(config.get('privacy_note'))
verdict['config_hides_operator_switches'] = 'handoff_keywords' not in config

# 3) nginx 必须把客服路径转到客服服务（而不是充值服务）----------------
status, payload = call('/brand/api/support/start', method='POST', payload={},
                       headers={'Origin': BASE})
steps['start_without_token'] = {'http': status, 'message': payload.get('message')}
# 落到充值服务会是 404「not found」；落到客服服务才是这句 401。
verdict['support_path_reaches_the_support_service'] = (
    status == 401 and '登录' in str(payload.get('message')))

status, payload = call('/brand/api/support/stream?conversation_id=S-20260927-000000')
steps['stream_without_token'] = {'http': status}
verdict['a_stream_needs_an_identity'] = status == 401

# 4) 经营者侧写保护 ---------------------------------------------------
status, payload = call('/brand/admin/api/support/conversations')
steps['admin_list_without_cookie'] = {'http': status, 'message': payload.get('message')}
verdict['admin_read_needs_the_cookie'] = status == 401

status, payload = call('/brand/admin/api/support/reply', method='POST', payload={})
steps['admin_write_without_origin'] = {'http': status, 'message': payload.get('message')}
verdict['admin_write_from_a_foreign_origin_is_refused'] = status == 403

status, payload = call('/brand/admin/api/support/reply', method='POST', payload={},
                       headers={'Origin': BASE})
steps['admin_write_with_origin_but_no_cookie'] = {'http': status, 'message': payload.get('message')}
verdict['origin_alone_is_not_enough'] = status == 401

status, payload = call('/brand/admin/api/support/session')
steps['admin_session_anonymous'] = {'http': status, **payload}
verdict['admin_session_reports_no_identity'] = (
    status == 200 and payload.get('authenticated') is False
    and payload.get('csrf_token') == '')

# 5) 客服服务自身健康与状态 -------------------------------------------
# 它的 /healthz 只监听内网（公网的 /healthz 是 nginx 自己的），所以直连本地端口。
LOCAL = 'http://127.0.0.1:8790'
try:
    request = urllib.request.Request(LOCAL + '/healthz')
    with urllib.request.urlopen(request, timeout=10) as response:
        status = response.status
        payload = json.loads(response.read().decode() or '{}')
except Exception as error:                       # noqa: BLE001 - reported, not raised
    status, payload = 0, {'error': '%s: %s' % (type(error).__name__, error)}
steps['healthz'] = payload
verdict['service_is_listening'] = status == 200 and payload.get('service') == 'omniapi-support'
verdict['healthz_reports_no_open_streams'] = payload.get('streams') == 0

# 6) 充值服务没有被这次改动带坏（两个服务共用会话与身份代码）---------
status, payload = call('/brand/admin/api/summary')
steps['topup_summary_without_cookie'] = {'http': status}
verdict['the_recharge_service_still_answers'] = status == 401

status, _ = page('/brand/topup.html')
verdict['the_recharge_page_still_loads'] = status == 200

report = {
    'probe': 'ops/support/probe_support_e2e.py',
    'scope': '公网只读验收；不创建会话、不发送任何消息、不写任何文件',
    'steps': steps,
    'verdict': verdict,
    'not_covered': ('完整对话流程（用户发起 → 经营者回复 → 双方实时收到）由 '
                    'tests/test_support_service.py 在临时实例上驱动，那里可以造真实登录态；'
                    '浏览器令牌受 Turnstile 保护，脚本拿不到。'),
}
print(json.dumps(steps, ensure_ascii=False, indent=2))
print()
print('VERDICT ' + json.dumps(verdict, ensure_ascii=False))
REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
print('证据: %s' % REPORT)
sys.exit(0 if all(verdict.values()) else 1)
