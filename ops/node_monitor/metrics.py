"""Host metrics and mergeable request histograms; no container limits are used."""
import bisect
import math
import os
import pathlib
import time

BUCKETS = [1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000,
           20000, 30000, 60000, 120000, 300000, 600000, 3600000]
OUTCOMES = ('success', 'failure', 'rejected', 'cancelled', 'edge_rejected')


def histogram(value):
    values = [0] * (len(BUCKETS) + 1)
    if value is not None:
        values[bisect.bisect_left(BUCKETS, max(0, value))] = 1
    return values


def percentile(values, percent):
    total = sum(values)
    if not total:
        return None
    target = math.ceil(total * percent / 100)
    seen = 0
    for index, count in enumerate(values):
        seen += count
        if seen >= target:
            return BUCKETS[index] if index < len(BUCKETS) else 86400000
    return None


def category(path, method='GET'):
    if path.startswith(('/static/', '/assets/', '/.well-known/', '/internal/')) or (path.startswith('/brand/') and not path.startswith('/brand/api/')):
        return None
    if path in ('/api/status', '/healthz') or '/node-monitor' in path:
        return None
    if path.startswith(('/v1/', '/v1beta/', '/pg/', '/mj/', '/suno/')):
        return 'model' if method == 'POST' or '/realtime' in path else 'management'
    if not path.startswith(('/api/', '/brand/api/', '/oauth/', '/sync/')):
        return None
    if any(x in path for x in ('/login', '/oauth/', '/logout', '/refresh')):
        return 'login'
    if any(x in path for x in ('/support/', '/support-api/', '/brand/api/chat', '/brand/api/admin/support')):
        return 'support'
    if '/sync' in path:
        return 'sync'
    if any(x in path for x in ('/topup', '/orders', '/purchase', '/credit', '/payment')):
        return 'recharge'
    return 'management'


class HostSampler:
    def __init__(self, proc='/proc', business_paths=None):
        self.proc = pathlib.Path(proc)
        self.business_paths = business_paths or []
        self.previous_cpu = None
        self.previous_net = None

    def sample(self):
        now = time.time()
        cpu = [int(x) for x in (self.proc/'stat').read_text().splitlines()[0].split()[1:9]]
        total, idle = sum(cpu), cpu[3]+cpu[4]
        usage = None
        if self.previous_cpu:
            old_total, old_idle = self.previous_cpu
            delta = total-old_total
            if delta > 0:
                usage = max(0, min(100, 100*(delta-(idle-old_idle))/delta))
        self.previous_cpu = (total, idle)
        memory = {}
        for line in (self.proc/'meminfo').read_text().splitlines():
            key, value = line.split(':', 1)
            memory[key] = int(value.strip().split()[0])*1024
        available = memory.get('MemAvailable', memory.get('MemFree', 0))
        size = memory['MemTotal']
        cores = os.cpu_count() or 1
        load = [float(x) for x in (self.proc/'loadavg').read_text().split()[:3]]
        disks, seen, mounts = [], set(), set()
        # ProtectSystem=strict makes the observer's own bind mounts read-only.
        # Host PID 1 describes the actual filesystem used by business services.
        host_mounts = True
        try:
            mountinfo = (self.proc/'1/mountinfo').read_text()
        except OSError:
            host_mounts = False
            mountinfo = (self.proc/'self/mountinfo').read_text()
        for line in mountinfo.splitlines():
            left, right = line.split(' - ', 1)
            fields, info = left.split(), right.split()
            if info[0] not in ('ext2', 'ext3', 'ext4', 'xfs', 'btrfs', 'zfs', 'f2fs', 'vfat', 'exfat', 'ntfs', 'ntfs3', 'fuseblk'):
                continue
            device, mount = fields[2], fields[4].replace('\\040', ' ')
            mounts.add(mount)
            if device in seen or mount.startswith(('/var/lib/docker/', '/srv/omni-data/docker/')):
                continue
            try:
                stat = os.statvfs(mount)
            except OSError:
                continue
            seen.add(device)
            capacity, free = stat.f_blocks*stat.f_frsize, stat.f_bavail*stat.f_frsize
            used = (stat.f_blocks-stat.f_bfree)*stat.f_frsize
            disks.append({'mount': mount, 'device': device, 'total': capacity,
                          'used': used, 'free': free, 'percent': 100*used/capacity if capacity else None,
                          'readonly': 'ro' in info[2].split(',') or (host_mounts and 'ro' in fields[5].split(',')),
                          'role': 'maintenance' if mount in ('/boot', '/boot/efi', '/efi') else 'display',
                          'business_paths': []})
        path_errors = self.map_business_paths(disks, mounts)
        interfaces = set()
        for line in (self.proc/'net/route').read_text().splitlines()[1:]:
            row = line.split()
            if row[1] == '00000000':
                interfaces.add(row[0])
        rx = tx = 0
        for line in (self.proc/'net/dev').read_text().splitlines()[2:]:
            name, counters = line.split(':', 1)
            if name.strip() in interfaces:
                values = counters.split()
                rx += int(values[0]); tx += int(values[8])
        rx_rate = tx_rate = None
        if self.previous_net:
            last, old_rx, old_tx = self.previous_net
            seconds = now-last
            if seconds > 0 and rx >= old_rx and tx >= old_tx:
                rx_rate, tx_rate = (rx-old_rx)/seconds, (tx-old_tx)/seconds
        self.previous_net = (now, rx, tx)
        return {'timestamp': now, 'cpu_cores': cores, 'cpu_percent': usage,
                'memory_total': size, 'memory_used': size-available,
                'memory_percent': 100*(size-available)/size, 'disks': disks,
                'disk_policy_version': 2, 'disk_path_errors': path_errors,
                'load': load, 'load_per_core': load[0]/cores,
                'network_rx': rx_rate, 'network_tx': tx_rate,
                'uptime': float((self.proc/'uptime').read_text().split()[0])}

    def map_business_paths(self, disks, mounts):
        """Resolve symlinks and real device IDs, never assume a path's longest prefix is its filesystem."""
        by_device = {disk['device']: disk for disk in disks}
        errors = []
        for target in self.business_paths:
            path = target['path']
            if target.get('required_mount') and target['required_mount'] not in mounts:
                errors.append({'path': path, 'error': 'required_mount_missing'})
                continue
            try:
                stat = os.stat(path)
                device = '%d:%d' % (os.major(stat.st_dev), os.minor(stat.st_dev))
                disk = by_device.get(device)
                if disk is None:
                    errors.append({'path': path, 'error': 'filesystem_unavailable'})
                    continue
                disk['role'] = 'business'
                if path not in disk['business_paths']:
                    disk['business_paths'].append(path)
            except OSError:
                errors.append({'path': path, 'error': 'path_unavailable'})
        return errors
