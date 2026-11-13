"""Business-filesystem incidents: state changes only, with durable batched delivery."""
import json
import time

GIB = 1024 ** 3
MIB = 1024 ** 2
DEFAULT_DISK_PATHS = {
    'primary': [
        {'path': '/', 'label': '系统'},
        {'path': '/srv/omni-data', 'label': '业务数据', 'required_mount': '/srv/omni-data'},
        {'path': '/srv/omni-data/docker', 'label': '数据库与 Redis'},
        {'path': '/srv/omni-data/docker/volumes/omni-prod-primary-20261009/_data', 'label': 'PostgreSQL 数据'},
        {'path': '/srv/omni-data/docker/volumes/omni-prod-redis-20261009/_data', 'label': 'Redis 数据'},
        {'path': '/srv/omni-data/docker/volumes/omni-ha_ha-api-data/_data', 'label': '网关日志'},
        {'path': '/opt/ai-gateway/runtime', 'label': '业务运行与封存'},
        {'path': '/var/lib/omni-sync', 'label': '同步数据'},
        {'path': '/var/log', 'label': '系统日志'},
        {'path': '/var/backups', 'label': '备份'},
        {'path': '/opt/omni-node-monitor', 'label': '监控服务'},
    ],
    'relay': [
        {'path': '/', 'label': '系统'},
        {'path': '/var/lib/docker', 'label': '数据库与 Redis'},
        {'path': '/var/lib/docker/volumes/omni-prod-standby-20261009/_data', 'label': 'PostgreSQL 备用数据'},
        {'path': '/var/lib/docker/volumes/omni-ha-standby_omni-ha-standby-api/_data', 'label': '备用网关日志'},
        {'path': '/opt/omni-ha-stage', 'label': '备用运行数据'},
        {'path': '/var/log', 'label': '系统日志'},
        {'path': '/var/backups', 'label': '备份镜像'},
        {'path': '/opt/omni-node-monitor', 'label': '监控队列'},
    ],
}


class DiskAlerts:
    def __init__(self, store, publish, clock=time.time):
        self.store, self.publish, self.clock = store, publish, clock
        self._close_legacy()

    def _close_legacy(self):
        if self.store.state('disk_policy_migrated_v2', False):
            return
        closed = []
        with self.store.lock, self.store.db:
            rows = self.store.db.execute("SELECT key,value FROM state WHERE key LIKE 'incident:disk:%'").fetchall()
            for row in rows:
                state = json.loads(row['value'])
                if state.get('open'):
                    closed.append(row['key'])
                state.update(open=False, bad_samples=0, recovery_pending=False, closed_reason='disk_policy_v2')
                self.store.set_state(row['key'], state)
            self.store.set_state('disk_policy_migrated_v2', True)
        if closed:
            self.store.event('rule_change', 'primary', {'title': '磁盘告警规则调整', 'closed_count': len(closed)})

    def states(self):
        with self.store.lock:
            return {row['key']: json.loads(row['value']) for row in self.store.db.execute(
                "SELECT key,value FROM state WHERE key LIKE 'disk_incident_v2:%'")}

    def evaluate(self, node, sample):
        observations = {}
        for disk in sample.get('disks', []):
            # Old observers can roll through the deployment without fabricated missing-path alarms.
            role = disk.get('role')
            if role is None:
                role = 'maintenance' if disk['mount'] in ('/boot', '/boot/efi', '/efi') else (
                    'business' if disk['mount'] in ('/', '/srv/omni-data') else 'display')
            if role == 'display':
                continue
            item = dict(disk, role=role)
            observations['disk_incident_v2:'+node+':'+disk['device']] = item
        for target in sample.get('disk_path_errors', []):
            observations['disk_incident_v2:'+node+':path:'+target['path']] = {
                'role': 'business', 'fault': target['error'], 'mount': target['path'],
                'business_paths': [target['path']], 'free': None, 'percent': None}
        for key, state in self.states().items():
            if state.get('node') == node and state.get('open') and ':path:' in key and key not in observations:
                path = key.split(':path:', 1)[1]
                recovered = next((disk for disk in sample.get('disks', []) if path in disk.get('business_paths', [])), None)
                if recovered:
                    observations[key] = dict(recovered, role='business')
        for key, item in observations.items():
            self._check(key, node, item, float(sample['timestamp']))
        # A vanished device identity is retired silently; missing required paths have their own incident.
        for key, state in self.states().items():
            if state.get('node') == node and key not in observations:
                was_open = state.get('open')
                state.update(open=False, level='normal', pending=None, closed_reason='filesystem_mapping_changed')
                state.update(candidate=None, candidate_samples=0, notified=False, notified_rank=0)
                self.store.set_state(key, state)
                if was_open:
                    self.store.event('rule_change', node, {'title': '磁盘映射更新', 'key': key})

    @staticmethod
    def level(item, current):
        if item['role'] == 'maintenance' and item.get('readonly'):
            return 'maintenance'
        if item.get('fault') or (item['role'] == 'business' and item.get('readonly')):
            return 'critical'
        percent, free = item.get('percent'), item.get('free')
        if percent is None or free is None:
            return None
        if item['role'] == 'maintenance':
            return 'maintenance' if percent >= (90 if current == 'maintenance' else 95) else 'normal'
        if free <= 512*MIB or (current == 'critical' and free <= 768*MIB):
            return 'critical'
        if (percent >= 90 and free <= 2*GIB) or (current in ('warning','critical') and percent >= 85 and free <= 2.5*GIB):
            return 'warning'
        return 'normal'

    def _check(self, key, node, item, timestamp):
        state = self.store.state(key, {'node': node, 'level': 'normal', 'open': False,
                                      'notified': False, 'notified_rank': 0, 'pending': None})
        if timestamp <= state.get('last_sample', float('-inf')):
            return
        if timestamp-state.get('last_sample', timestamp) > 30:
            state.update(candidate=None, candidate_samples=0)
        state['last_sample'] = timestamp
        current = state['level']
        desired = self.level(item, current)
        state['detail'] = self.detail(item)
        state['role'] = item['role']
        if desired is None:
            self.store.set_state(key, state)
            return
        if desired == current:
            state.update(candidate=None, candidate_samples=0)
        else:
            if state.get('candidate') != desired:
                state.update(candidate=desired, candidate_since=timestamp, candidate_samples=0)
            state['candidate_samples'] += 1
            ranks = {'normal': 0, 'maintenance': 1, 'warning': 1, 'critical': 2}
            rising = ranks[desired] > ranks[current] or desired == 'maintenance'
            fault = item.get('fault') or item.get('readonly')
            confirmed = state['candidate_samples'] >= 3 if fault or not rising else timestamp-state['candidate_since'] >= 60
            if confirmed:
                state.update(level=desired, open=desired != 'normal', candidate=None, candidate_samples=0)
                maintenance = item['role'] == 'maintenance'
                kind = 'maintenance' if maintenance else 'recovery' if desired == 'normal' else 'alert'
                title = (('启动分区维护风险恢复' if desired == 'normal' else '启动分区维护风险') if maintenance else
                         '磁盘恢复' if desired == 'normal' else
                         '业务磁盘严重告警' if desired == 'critical' else '业务磁盘容量预警')
                self.store.event(kind, node, {'title': title, 'detail': state['detail'], 'key': key, 'severity': desired})
                if desired == 'normal':
                    state['pending'] = {'title': title, 'severity': desired} if state.get('notified') else None
                elif ranks[desired] > state.get('notified_rank', 0):
                    state['pending'] = {'title': title, 'severity': desired}
                else:
                    state['pending'] = None
                if desired == 'normal' and not state.get('pending'):
                    state.update(notified=False, notified_rank=0)
        self.store.set_state(key, state)

    @staticmethod
    def detail(item):
        paths = '、'.join(item.get('business_paths') or [item['mount']])
        impact = '；可能影响系统更新或后续重启' if item['role'] == 'maintenance' else '；可能影响数据库、日志或业务数据写入'
        if item.get('fault'):
            return paths+'；业务路径不可用：'+item['fault']+impact
        if item.get('readonly'):
            return paths+('；启动文件系统只读' if item['role'] == 'maintenance' else '；业务文件系统只读')+impact
        if item.get('percent') is None or item.get('free') is None:
            return paths+'；容量数据不可用'
        return paths+'；'+item['mount']+' 已用 %.1f%%，剩余 %.2fGiB' % (item['percent'], item['free']/GIB)+impact

    def flush(self):
        now = self.clock()
        delivery = self.store.state('disk_delivery_v2', {'failures': 0, 'next_attempt': 0})
        if now < delivery['next_attempt']:
            return
        pending = {key: state for key, state in self.states().items() if state.get('pending')}
        if not pending:
            return
        # One durable batch for all nodes in this evaluation cycle; unchanged incidents never enter it.
        names = {'primary': '主服务器', 'relay': '备用服务器'}
        detail = '\n'.join(names.get(state['node'], state['node'])+'：'+state['pending']['title']+'；'+state['detail']
                           for state in pending.values())
        result = self.publish('磁盘状态变更（%d项）' % len(pending), detail, 'primary')
        ok = bool(result.get('ok'))
        self.store.event('notification', 'primary', {'title': '磁盘状态变更', 'ok': ok, 'count': len(pending),
                                                    'error': str(result.get('error') or '')[:160]})
        if ok:
            with self.store.lock, self.store.db:
                for key, state in pending.items():
                    level = state['pending']['severity']
                    state.update(pending=None, notified=level != 'normal',
                                 notified_rank={'normal': 0, 'maintenance': 1, 'warning': 1, 'critical': 2}[level])
                    self.store.set_state(key, state)
                self.store.set_state('disk_delivery_v2', {'failures': 0, 'next_attempt': 0})
        else:
            delivery['failures'] += 1
            delivery['next_attempt'] = now+min(900, 30*2**min(delivery['failures']-1, 5))
            self.store.set_state('disk_delivery_v2', delivery)
