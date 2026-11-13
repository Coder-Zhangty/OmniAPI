"""Live support workspace projections. JSONL events remain the source of truth."""
import copy
import datetime as dt
import json
import math
import re
import secrets

import support_store as store

TZ = dt.timezone(dt.timedelta(hours=8))
DEFAULT_CATEGORIES = [
    ('recharge', '充值到账', '#32a4f8'),
    ('api_access', 'API 接入', '#55bd62'),
    ('model_error', '模型与工具', '#8b66e8'),
    ('billing', '计费与扣额', '#f3a428'),
    ('account_security', '账号与安全', '#e97eae'),
    ('other', '其他问题', '#8493a8'),
]


def stamp(value):
    parsed = store._parse_iso(value)
    return parsed.astimezone(TZ) if parsed else None


def label(value):
    parsed = stamp(value)
    return parsed.strftime('%m/%d %H:%M') if parsed else '—'


def message_view(row):
    return {'role': {'operator': 'agent', 'ai': 'agent', 'note': 'note', 'system': 'system'}.get(row.get('role'), 'user'),
            'text': row.get('text') or '', 'time': label(row.get('created_at')),
            'seq': row.get('seq'), 'internal': bool(row.get('internal')),
            'files': [{'id': a['id'], 'name': a.get('name') or '图片',
                       'size': a.get('size') or 0, 'type': a.get('mime') or 'image/png'}
                      for a in (row.get('attachments') or [])]}


def ticket_view(row, admin=False):
    raw = row.get('status') or 'waiting_operator'
    status = ('resolved' if raw in ('resolved', 'closed') else
              'waiting' if raw == 'waiting_user' else
              'active' if row.get('assignee_id') else 'open')
    last = stamp(row.get('last_message_at'))
    wait = max(0, int((store.utcnow().astimezone(TZ) - last).total_seconds() / 60)) if last else 0
    result = {k: row.get(k) for k in ('id', 'subject', 'category', 'priority')}
    result.update(status=status, rawStatus=raw, assignee=str(row.get('assignee_id') or ''),
                  created=label(row.get('created_at')), updated=label(row.get('last_message_at')),
                  createdAt=row.get('created_at'), updatedAt=row.get('last_message_at'),
                  resolvedAt=row.get('resolved_at'), wait=wait,
                  unread=int(row.get('unread_operator' if admin else 'unread_user') or 0),
                  lastText=row.get('last_text') or '', lastRole=row.get('last_role') or '', messages=[])
    if admin:
        result.update(uid=row.get('uid'), username=row.get('username') or '')
    return result


class SupportWorkspace:
    def __init__(self, service):
        self.service = service
        self.sync_categories()

    def catalog(self):
        db = self.service.store.index._open()
        try:
            row = db.execute("SELECT value FROM settings WHERE key='workspace_catalog'").fetchone()
        finally:
            db.close()
        if row:
            return json.loads(row['value'])
        config = self.service.config()
        return {'categories': [{'id': key, 'name': name, 'color': color, 'enabled': True,
                                'priority': 'high' if key == 'account_security' else 'normal', 'sla': 30}
                               for key, name, color in DEFAULT_CATEGORIES],
                'replies': [{'id': i + 1, 'title': text[:18], 'text': text}
                            for i, text in enumerate(config.get('quick_replies') or [])],
                'team': [{'id': '客服', 'name': '客服'}], 'sla': 30}

    def sync_categories(self):
        self.service.store.category_ids = {c['id'] for c in self.catalog()['categories']}

    def allowed_category(self, key, active=True):
        return any(c['id'] == key and (c['enabled'] or not active) for c in self.catalog()['categories'])

    def save_catalog(self, payload):
        if not isinstance(payload, dict): raise ValueError('设置格式无效')
        # A single append under the store lock prevents concurrent edits overwriting history.
        with self.service.store.lock:
            catalog = copy.deepcopy(self.catalog())
            kind = str(payload.get('kind') or '')
            values = payload.get('values') or {}
            if not isinstance(values, dict): raise ValueError('设置内容无效')
            if kind == 'category':
                key = str(values.get('id') or '')
                current = next((c for c in catalog['categories'] if c['id'] == key), None)
                if key and current is None:
                    raise ValueError('分类不存在，请刷新后重试')
                name = str(values.get('name') or '').strip()
                if not 1 <= len(name) <= 20 or any(c['id'] != key and c['name'].casefold() == name.casefold() for c in catalog['categories']):
                    raise ValueError('分类名称须为 1–20 字，且不能重复')
                color = str(values.get('color') or '')
                sla = values.get('sla')
                if not re.fullmatch(r'#[0-9a-fA-F]{6}', color) or not isinstance(sla, int) or isinstance(sla, bool) or not 1 <= sla <= 1440:
                    raise ValueError('分类颜色或响应目标无效')
                if values.get('priority') not in ('normal', 'high'):
                    raise ValueError('默认优先级无效')
                entry = {'id': key or 'cat_' + secrets.token_hex(6), 'name': name, 'color': color,
                         'priority': values['priority'], 'sla': sla, 'enabled': current['enabled'] if current else True}
                if current:
                    current.update(entry)
                else:
                    if len(catalog['categories']) >= 40: raise ValueError('分类最多 40 个')
                    catalog['categories'].append(entry)
            elif kind in ('category_toggle', 'category_move'):
                key = str(values.get('id') or '')
                index = next((i for i,c in enumerate(catalog['categories']) if c['id'] == key), None)
                if index is None: raise ValueError('分类不存在')
                item = catalog['categories'][index]
                if kind == 'category_toggle':
                    enabled = values.get('enabled')
                    if not isinstance(enabled, bool): raise ValueError('分类状态无效')
                    if not enabled and sum(c['enabled'] for c in catalog['categories']) <= 1 and item['enabled']:
                        raise ValueError('请至少保留一个可用分类')
                    item['enabled'] = enabled
                else:
                    direction = values.get('direction')
                    if direction not in (-1, 1): raise ValueError('排序方向无效')
                    other = index + direction
                    if 0 <= other < len(catalog['categories']):
                        catalog['categories'][index], catalog['categories'][other] = catalog['categories'][other], item
            elif kind == 'reply':
                key = values.get('id')
                title, text = str(values.get('title') or '').strip(), str(values.get('text') or '').strip()
                if not 1 <= len(title) <= 40 or not 1 <= len(text) <= 300: raise ValueError('标题或回复内容长度无效')
                current = next((r for r in catalog['replies'] if r['id'] == key), None)
                if key and current is None: raise ValueError('快捷回复不存在')
                if current: current.update(title=title, text=text)
                else:
                    if len(catalog['replies']) >= 20: raise ValueError('快捷回复最多 20 条')
                    catalog['replies'].append({'id': secrets.randbelow(1_000_000_000)+1, 'title': title, 'text': text})
            elif kind == 'team':
                name = str(values.get('name') or '').strip()
                if not 1 <= len(name) <= 30 or any(p['name'] == name for p in catalog['team']): raise ValueError('负责人名称无效或重复')
                if len(catalog['team']) >= 30: raise ValueError('负责人最多 30 个')
                catalog['team'].append({'id': name, 'name': name})
            elif kind == 'sla':
                value = values.get('sla')
                if not isinstance(value, int) or isinstance(value, bool) or not 1 <= value <= 1440: raise ValueError('响应目标无效')
                catalog['sla'] = value
            else:
                raise ValueError('设置操作无效')
            self.service.store.append_quiet({'type': 'settings', 'id': 'workspace-catalog',
                'values': {'workspace_catalog': catalog}, 'created_at': store.iso()})
            self.sync_categories()
            return catalog

    def rows(self, uid=None):
        db = self.service.store.index._open()
        try:
            where, args = (' WHERE uid=?', [uid]) if uid is not None else ('', [])
            return [dict(row) for row in db.execute('SELECT * FROM tickets'+where+' ORDER BY updated_at DESC LIMIT 5001', args)]
        finally:
            db.close()

    def analytics(self, days):
        days = 30 if str(days) == '30' else 7
        today = store.utcnow().astimezone(TZ).date()
        first = today - dt.timedelta(days=days-1)
        buckets = {str(first+dt.timedelta(days=i)): {'new':0, 'solved':0, 'label':(first+dt.timedelta(days=i)).strftime('%m/%d')} for i in range(days)}
        rows = self.rows()
        durations = []
        created_in_range = resolved_in_range = new_today = 0
        for row in rows:
            created, resolved, responded = stamp(row.get('created_at')), stamp(row.get('resolved_at')), stamp(row.get('first_response_at'))
            if created:
                key = str(created.date())
                if key in buckets: buckets[key]['new'] += 1
                if created.date() == today: new_today += 1
                if first <= created.date() <= today:
                    created_in_range += 1
                    if resolved: resolved_in_range += 1
                    if responded: durations.append(max(0,(responded-created).total_seconds()/60))
            if resolved and str(resolved.date()) in buckets: buckets[str(resolved.date())]['solved'] += 1
        return {'days':days, 'series':list(buckets.values()), 'newToday':new_today,
                'firstResponseMinutes':round(sum(durations)/len(durations),1) if durations else None,
                'resolutionRate':round(100*resolved_in_range/created_in_range,1) if created_in_range else None,
                'cohortSize':created_in_range, 'timezone':'Asia/Shanghai'}

    def snapshot(self, uid=None, days=7):
        catalog = self.catalog()
        rows = self.rows(uid)
        if len(rows) > 5000: raise ValueError('工单超过工作台加载上限，请联系管理员使用历史导出')
        config = self.service.public_config()
        result = {'categories': catalog['categories'], 'tickets':[ticket_view(row, uid is None) for row in rows],
                  'settings':{'greeting':config['greeting'], 'hours':config['service_hours'], 'sla':catalog['sla'], 'maxText':config['max_text']},
                  'privacyNote':config['privacy_note'], 'enabled':config['enabled']}
        if uid is None:
            team = copy.deepcopy(catalog['team'])
            for row in rows:
                value = str(row.get('assignee_id') or '')
                if value and not any(p['id']==value for p in team): team.append({'id':value,'name':value})
            result.update(replies=catalog['replies'],team=team,analytics=self.analytics(days))
        else:
            result.update(replies=[],team=[])
        return result
