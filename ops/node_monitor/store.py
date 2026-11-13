"""Monitoring-only SQLite storage; independent of the synchronous business DB."""
import contextlib
import json
import pathlib
import sqlite3
import threading
import time
import uuid

from metrics import BUCKETS, OUTCOMES, histogram, percentile


class Store:
    def __init__(self, path):
        pathlib.Path(path).parent.mkdir(parents=True, exist_ok=True)
        self.lock = threading.RLock()
        self.db = sqlite3.connect(path, timeout=2, check_same_thread=False)
        self.db.row_factory = sqlite3.Row
        self.db.executescript('''
        PRAGMA journal_mode=WAL;
        PRAGMA synchronous=NORMAL;
        PRAGMA busy_timeout=2000;
        CREATE TABLE IF NOT EXISTS latest(node TEXT PRIMARY KEY,received REAL,data TEXT);
        CREATE TABLE IF NOT EXISTS sample_ids(node TEXT,ts REAL,PRIMARY KEY(node,ts));
        CREATE TABLE IF NOT EXISTS resources(node TEXT,level INTEGER,bucket INTEGER,data TEXT,
          PRIMARY KEY(node,level,bucket));
        CREATE TABLE IF NOT EXISTS requests(id TEXT PRIMARY KEY,ts REAL,node TEXT,category TEXT,
          model TEXT,outcome TEXT,quality INTEGER,data TEXT);
        CREATE INDEX IF NOT EXISTS requests_time ON requests(ts DESC);
        CREATE TABLE IF NOT EXISTS request_buckets(bucket INTEGER,node TEXT,category TEXT,
          model TEXT,outcome TEXT,count INTEGER,attempts INTEGER,latency TEXT,first TEXT,
          PRIMARY KEY(bucket,node,category,model,outcome));
        CREATE TABLE IF NOT EXISTS events(id TEXT PRIMARY KEY,ts REAL,kind TEXT,node TEXT,data TEXT);
        CREATE INDEX IF NOT EXISTS events_time ON events(ts DESC);
        CREATE TABLE IF NOT EXISTS incidents(key TEXT PRIMARY KEY,data TEXT);
        CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,idem TEXT UNIQUE,ts REAL,data TEXT);
        CREATE TABLE IF NOT EXISTS spool(id TEXT PRIMARY KEY,ts REAL,data TEXT);
        CREATE TABLE IF NOT EXISTS state(key TEXT PRIMARY KEY,value TEXT);
        ''')
        self.db.commit()

    def state(self, key, default=None):
        with self.lock:
            row = self.db.execute('SELECT value FROM state WHERE key=?', (key,)).fetchone()
            return json.loads(row[0]) if row else default

    def set_state(self, key, value):
        with self.lock, self.db:
            self.db.execute('INSERT OR REPLACE INTO state VALUES (?,?)', (key, json.dumps(value)))

    def sample(self, node, sample, received=None, update_latest=True):
        received = received or time.time()
        ts = float(sample['timestamp'])
        if update_latest and abs(ts-received) > 30:
            sample = dict(sample, clock_skew=True)
        with self.lock, self.db:
            inserted=self.db.execute('INSERT OR IGNORE INTO sample_ids VALUES (?,?)',(node,ts)).rowcount
            if not inserted:return
            old = self.db.execute('SELECT data FROM latest WHERE node=?', (node,)).fetchone()
            previous=json.loads(old[0]) if old else None
            recovered=bool(previous and previous.get('clock_skew') and not sample.get('clock_skew'))
            if update_latest and (not previous or recovered or previous['timestamp'] <= ts):
                self.db.execute('INSERT OR REPLACE INTO latest VALUES (?,?,?)', (node, received, json.dumps(sample)))
            for level in (10, 60, 300):
                bucket = int(ts)//level*level
                row = self.db.execute('SELECT data FROM resources WHERE node=? AND level=? AND bucket=?', (node, level, bucket)).fetchone()
                item = json.loads(row[0]) if row else {'sum': {}, 'count': {}, 'max': {}, 'last': sample}
                item['last'] = sample
                values={key:sample.get(key) for key in ('cpu_percent', 'memory_percent', 'load_per_core', 'network_rx', 'network_tx')}
                for disk in sample.get('disks',[]):
                    for metric in ('percent','used','free'):
                        values['disk:'+disk['device']+':'+metric]=disk.get(metric)
                for key,value in values.items():
                    if value is not None:
                        item['sum'][key] = item['sum'].get(key, 0)+value
                        item['count'][key] = item['count'].get(key, 0)+1
                        item['max'][key] = max(item['max'].get(key, value), value)
                self.db.execute('INSERT OR REPLACE INTO resources VALUES (?,?,?,?)', (node, level, bucket, json.dumps(item)))

    def latest(self):
        with self.lock:
            return {row['node']: dict(json.loads(row['data']), received=row['received'])
                    for row in self.db.execute('SELECT * FROM latest')}

    def ingest_request(self, event, quality=2):
        allowed={'event_id','edge_id','request_id','timestamp','node','category','model','outcome','status','duration_ms','first_ms','attempts','chain'}
        event = {key:value for key,value in event.items() if key in allowed}
        key = event.get('edge_id') or event.get('event_id')
        if not isinstance(key, str) or not 1 <= len(key) <= 160:
            raise ValueError('invalid event identity')
        ts = float(event['timestamp'])/1000
        if not time.time()-86400*7 <= ts <= time.time()+120:
            return False
        event['duration_ms'] = max(0, min(float(event.get('duration_ms', 0)), 86400000))
        event['attempts'] = max(0, min(int(event.get('attempts', 0)), 1000))
        event['model'] = str(event.get('model') or '')[:128]
        chain=event.get('chain') or []
        event['chain']=[str(value)[:128] for value in chain[:1000]] if isinstance(chain,list) else []
        if event.get('outcome') not in OUTCOMES:
            event['outcome'] = 'failure'
        with self.lock, self.db:
            old = self.db.execute('SELECT data,quality FROM requests WHERE id=?', (key,)).fetchone()
            if old and old['quality'] >= quality:
                return False
            if old:
                self._bucket(json.loads(old['data']), -1)
            self._bucket(event, 1)
            self.db.execute('INSERT OR REPLACE INTO requests VALUES (?,?,?,?,?,?,?,?)',
                (key, ts, event['node'], event['category'], event['model'], event['outcome'], quality, json.dumps(event)))
        return True

    def _bucket(self, event, sign):
        bucket = int(float(event['timestamp'])/1000)//60*60
        keys = (bucket, event['node'], event['category'], event['model'], event['outcome'])
        row = self.db.execute('SELECT * FROM request_buckets WHERE bucket=? AND node=? AND category=? AND model=? AND outcome=?', keys).fetchone()
        latency = json.loads(row['latency']) if row else [0]*(len(BUCKETS)+1)
        first = json.loads(row['first']) if row else [0]*(len(BUCKETS)+1)
        incoming = histogram(event['duration_ms'])
        initial = histogram(event.get('first_ms'))
        latency = [a+sign*b for a, b in zip(latency, incoming)]
        first = [a+sign*b for a, b in zip(first, initial)]
        self.db.execute('INSERT OR REPLACE INTO request_buckets VALUES (?,?,?,?,?,?,?,?,?)',
            keys+((row['count'] if row else 0)+sign, (row['attempts'] if row else 0)+sign*event['attempts'], json.dumps(latency), json.dumps(first)))

    def filters(self, params, timestamp='ts'):
        now = time.time()
        start = float(params.get('start', now-3600)); end = float(params.get('end', now))
        if start < now-90*86400 or end > now+120 or start >= end:
            raise ValueError('range must be within the last 90 days')
        clauses, values = [timestamp+'>=?', timestamp+'<=?'], [start, end]
        for key in ('node', 'category', 'model', 'outcome'):
            if params.get(key):
                clauses.append(key+'=?'); values.append(params[key])
        return ' AND '.join(clauses), values, start, end

    def series(self, params):
        where, args, start, end = self.filters(params, 'bucket')
        seconds = end-start
        step = 60 if seconds <= 86400 else 300 if seconds <= 7*86400 else 3600
        combined = {}
        with self.lock:
            for row in self.db.execute('SELECT * FROM request_buckets WHERE '+where, args):
                key = (row['bucket']//step*step, row['node'])
                item = combined.setdefault(key, {'timestamp': key[0], 'node': key[1], 'count': 0, 'attempts': 0, 'latency': [0]*(len(BUCKETS)+1), 'first': [0]*(len(BUCKETS)+1), **{x:0 for x in OUTCOMES}})
                item['count'] += row['count']; item['attempts'] += row['attempts']; item[row['outcome']] += row['count']
                item['latency'] = [a+b for a,b in zip(item['latency'],json.loads(row['latency']))]
                item['first'] = [a+b for a,b in zip(item['first'],json.loads(row['first']))]
            resources = []
            level = 10 if seconds <= 3600 else 60 if seconds <= 7*86400 else 300
            resource_where = 'level=? AND bucket>=? AND bucket<=?'
            resource_args = [level, start, end]
            if params.get('node'):
                resource_where += ' AND node=?';resource_args.append(params['node'])
            resource_groups={}
            resource_step=max(level, int((seconds+799*level)//(800*level))*level)
            for row in self.db.execute('SELECT node,bucket,data FROM resources WHERE '+resource_where+' ORDER BY bucket', resource_args):
                data = json.loads(row['data'])
                group=(row['node'],row['bucket']//resource_step*resource_step)
                aggregate=resource_groups.setdefault(group,{'sum':{},'count':{},'max':{}})
                for metric,value in data['sum'].items():
                    aggregate['sum'][metric]=aggregate['sum'].get(metric,0)+value
                    aggregate['count'][metric]=aggregate['count'].get(metric,0)+data['count'][metric]
                    aggregate['max'][metric]=max(aggregate['max'].get(metric,data['max'][metric]),data['max'][metric])
            resources=[{'timestamp':key[1],'node':key[0],**{k:v/data['count'][k] for k,v in data['sum'].items()},'peak':data['max']} for key,data in sorted(resource_groups.items(),key=lambda x:x[0][1])]
        requests = []
        total, total_hist, first_hist = 0, [0]*(len(BUCKETS)+1), [0]*(len(BUCKETS)+1)
        outcomes = {x:0 for x in OUTCOMES}
        node_counts = {}
        for item in sorted(combined.values(), key=lambda x:(x['timestamp'],x['node'])):
            total += item['count']; node_counts[item['node']] = node_counts.get(item['node'],0)+item['count']
            for name in OUTCOMES:outcomes[name]+=item[name]
            total_hist=[a+b for a,b in zip(total_hist,item['latency'])]
            first_hist=[a+b for a,b in zip(first_hist,item['first'])]
            item['p50_ms']=percentile(item['latency'],50);item['p95_ms']=percentile(item['latency'],95)
            item['first_p50_ms']=percentile(item['first'],50);item['first_p95_ms']=percentile(item['first'],95)
            item['rpm']=item['count']*60/step
            del item['latency'];del item['first'];requests.append(item)
        health_total=total
        return {'requests':requests,'resources':resources,'step':step,'summary':{'total':total,**outcomes,'node_counts':node_counts,'success_rate':100*outcomes['success']/health_total if health_total else None,'p50_ms':percentile(total_hist,50),'p95_ms':percentile(total_hist,95),'first_p50_ms':percentile(first_hist,50),'first_p95_ms':percentile(first_hist,95)}}

    def requests(self, params):
        where, args, _, _ = self.filters(params)
        page = max(1, int(params.get('page', 1))); size = min(100, max(1,int(params.get('page_size',25))))
        with self.lock:
            total=self.db.execute('SELECT count(*) FROM requests WHERE '+where,args).fetchone()[0]
            rows=self.db.execute('SELECT data FROM requests WHERE '+where+' ORDER BY ts DESC LIMIT ? OFFSET ?',args+[size,(page-1)*size]).fetchall()
        return {'items':[json.loads(x[0]) for x in rows],'total':total,'page':page,'page_size':size,'retention_days':7}

    def event(self, kind, node, data):
        with self.lock,self.db:
            self.db.execute('INSERT INTO events VALUES (?,?,?,?,?)',(uuid.uuid4().hex,time.time(),kind,node,json.dumps(data)))

    def events(self, params):
        page=max(1,int(params.get('page',1)))
        with self.lock:
            total=self.db.execute('SELECT count(*) FROM events').fetchone()[0]
            rows=self.db.execute('SELECT * FROM events ORDER BY ts DESC LIMIT 50 OFFSET ?',((page-1)*50,)).fetchall()
            return {'items':[{'id':x['id'],'timestamp':x['ts'],'kind':x['kind'],'node':x['node'],**json.loads(x['data'])} for x in rows],'total':total,'page':page,'page_size':50}

    def prune(self, now=None):
        now=now or time.time()
        with self.lock,self.db:
            for level,days in ((10,1),(60,7),(300,90)):
                self.db.execute('DELETE FROM resources WHERE level=? AND bucket<?',(level,now-days*86400))
            self.db.execute('DELETE FROM requests WHERE ts<?',(now-7*86400,))
            self.db.execute('DELETE FROM sample_ids WHERE ts<?',(now-7*86400,))
            self.db.execute('DELETE FROM request_buckets WHERE bucket<?',(now-90*86400,))
            self.db.execute('DELETE FROM events WHERE ts<?',(now-90*86400,))
            self.db.execute('DELETE FROM spool WHERE ts<?',(now-86400,))
            self.db.execute('PRAGMA incremental_vacuum(100)')

    def spool(self, payload):
        ident=uuid.uuid4().hex
        with self.lock,self.db:
            self.db.execute('INSERT INTO spool VALUES (?,?,?)',(ident,time.time(),json.dumps(payload)))
            count=self.db.execute('SELECT count(*) FROM spool').fetchone()[0]
            if count>8640:
                self.db.execute('DELETE FROM spool WHERE id IN (SELECT id FROM spool ORDER BY ts LIMIT ?)',(count-8640,))
            size=self.db.execute('SELECT coalesce(sum(length(data)),0) FROM spool').fetchone()[0]
            if size>64*1024**2:
                self.db.execute('DELETE FROM spool WHERE id IN (SELECT id FROM spool ORDER BY ts LIMIT 100)')
                self.db.execute("INSERT OR REPLACE INTO state VALUES ('spool_loss',?)",(json.dumps({'timestamp':time.time(),'reason':'queue capacity exceeded'}),))
        return ident

    def pending(self):
        with self.lock:
            return [(x['id'],json.loads(x['data'])) for x in self.db.execute('SELECT * FROM spool ORDER BY ts LIMIT 10')]

    def ack_spool(self, identities):
        with self.lock,self.db:
            self.db.executemany('DELETE FROM spool WHERE id=?',[(x,) for x in identities])
