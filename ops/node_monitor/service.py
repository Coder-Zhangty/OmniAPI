"""Private host observer, independent alert delivery and allowlisted operation runner."""
import argparse
import calendar
import hashlib
import hmac
import http.server
import json
import os
import pathlib
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

from alerts import Alerts
from disk_alerts import DEFAULT_DISK_PATHS, DiskAlerts
from metrics import HostSampler, category
from operations import Operations
import routing
from store import Store

NODES={'primary':{'ip':'103.121.93.231','name':'主服务器','role':'primary'},
       'relay':{'ip':'160.202.46.124','name':'备用服务器','role':'relay'}}


def request(origin,path,token,body=None,timeout=3):
    data=None if body is None else json.dumps(body).encode()
    req=urllib.request.Request(origin.rstrip('/')+path,data=data,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
    with urllib.request.urlopen(req,timeout=timeout) as response:
        return json.loads(response.read(2<<20))


class Executor:
    def __init__(self,app):self.app=app

    def health(self):
        result={'durable':False,'primary':False,'relay':False}
        try:
            gate=json.loads(pathlib.Path(self.app.config['gate_path']).read_text())
            ts=calendar.timegm(time.strptime(gate['utc'],'%Y-%m-%dT%H:%M:%SZ'))
            result['durable']=gate['ready'] and time.time()-ts<30
            result['checked_at']=ts
            for key in ('replication','tunnel','redis','sync_protection','deployment_mode','local_commit'):
                result[key]=gate.get(key) if time.time()-ts<30 else None
        except (OSError,ValueError,KeyError):pass
        for node,origin in self.app.config['gateways'].items():
            if node == 'primary':
                origin = self.app.primary_origin(origin)
            try:
                with urllib.request.urlopen(origin+'/api/status',timeout=1) as response:
                    result[node]=response.status==200 and json.load(response).get('success') is True
            except Exception:pass
        return result

    def active(self,node):
        for _ in range(22):
            origin = self.app.config['gateways'][node]
            if node == 'primary':
                origin = self.app.primary_origin(origin)
            data=request(origin,'/internal/node-monitor',self.app.config['gateway_token'])['data']
            events=data.get('events',[])
            for event in events:self.app.store.ingest_request(dict(event,node=node),2)
            if events:request(origin,'/internal/node-monitor',self.app.config['gateway_token'],{'sequence':events[-1]['sequence']})
            if len(events)<500:return int(data['active'])
        raise RuntimeError('monitor events did not drain')

    def apply(self,config):
        stage=pathlib.Path(self.app.config['stage'])
        flag=stage/'gate/draining-primary'
        if config['draining']=='primary':flag.touch()
        elif flag.exists():flag.unlink()
        # The guard is the only renderer, including during a manual operation.
        subprocess.run(['python3',str(stage/'ha_guard.py'),'--once'],check=True,capture_output=True,timeout=15)
        self.app.health_cache=self.health()

    def restart(self,node):
        if node=='primary':
            subprocess.run(['docker','restart','--time','30','omni-ha-new-api-1'],check=True,capture_output=True,timeout=60)
        else:
            result=request(self.app.config['relay_control_origin'],'/internal/restart',self.app.config['control_token'],{'action':'restart_gateway'},timeout=65)
            if not result.get('success'):raise RuntimeError('relay restart failed')


class App:
    def __init__(self,config):
        self.config=config;self.node=config['node']
        self.nodes={k:v for k,v in NODES.items() if k=='primary' or config.get('deployment_mode')!='single_primary'}
        self.store=Store(config['database']);self.sampler=HostSampler(business_paths=config.get('disk_business_paths',DEFAULT_DISK_PATHS[self.node]))
        self.health_cache={};self.stop=threading.Event()
        self.alerts=Alerts(self.store,self.publish)
        self.disk_alerts=DiskAlerts(self.store,self.publish)
        self.operations=None
        if self.node=='primary':
            self.executor=Executor(self)
            self.operations=Operations(self.store,config['routing_path'],self.executor)
        self.access_offset=self.store.state('access_offset',{'inode':None,'offset':0})

    def publish(self,title,detail,node):
        if not self.config.get('alerts_enabled',False):return {'ok':False,'error':'alerts disabled'}
        sys.path.insert(0,self.config['notifier_path'])
        import notifier
        secrets=notifier.load_secrets(self.config['notify_secrets'])
        result=notifier.publish('wecom',secrets,'OmniAPI 节点监控：'+title,
            NODES[node]['name']+' '+NODES[node]['ip']+'；观察节点 '+self.node+'；'+detail,
            'https://omniapi.biz/node-monitor')
        # Do not retain upstream response bodies or exception text containing URLs.
        return {'ok':bool(result.get('ok')),'error':'' if result.get('ok') else 'Notification delivery failed; status='+str(result.get('status','network'))}

    def snapshot(self):
        latest=self.store.latest();now=time.time();nodes=[]
        for node,identity in self.nodes.items():
            metrics=latest.get(node)
            fresh=bool(metrics and now-metrics['timestamp']<=30 and not metrics.get('clock_skew'))
            nodes.append({'id':node,**identity,'fresh':fresh,'metrics':metrics if fresh else None,
                          'last_seen':metrics['timestamp'] if metrics else None,
                          'high_load':bool(fresh and self.store.state('incident:load_per_core:'+node,{}).get('open') and self.store.state('incident:load_per_core:'+node,{}).get('bad_samples')),
                          'gateway_healthy':metrics.get('gateway_healthy') if fresh else None})
        intent=routing.load(self.config['routing_path'])
        health=dict(self.health_cache)
        if now-health.get('checked_at',0)>30:
            health={'durable':False,'primary':None,'relay':None,'tunnel':None,'replication':None,'redis':None}
        effective=routing.weights(intent,health.get('relay',False))
        if not health.get('durable'):effective=None
        summary=self.store.series({'category':'model','start':now-3600,'end':now})['summary']
        counts=summary['node_counts'];total=sum(counts.get(x,0) for x in self.nodes)
        actual={node:100*counts.get(node,0)/total for node in NODES} if total else None
        active=sum(x['metrics']['model_active'] for x in nodes if x['fresh'] and x['metrics'].get('model_active') is not None)
        active_known=all(x['fresh'] and x['metrics'].get('model_active') is not None for x in nodes)
        recent=[event for event in self.store.events({'page':1})['items'] if event['kind'] in ('alert','recovery')][:3]
        with self.store.lock:
            incidents=[json.loads(row[0]) for row in self.store.db.execute("SELECT value FROM state WHERE key LIKE 'incident:%'")]
        unresolved=any(incident.get('open') and incident.get('bad_samples') for incident in incidents)
        disk_states={k:v for k,v in self.disk_alerts.states().items() if v.get('node') in self.nodes}
        unresolved=unresolved or any(state.get('open') and state.get('level') in ('warning','critical') for state in disk_states.values())
        for node in nodes:
            if node.get('metrics'):
                for disk in node['metrics'].get('disks',[]):
                    state=disk_states.get('disk_incident_v2:'+node['id']+':'+disk['device'],{})
                    disk['alert_level']=state.get('level','normal')
        disk_issues=[{'key':key,'node':state['node'],'severity':state['level'],'detail':state.get('detail','')} for key,state in disk_states.items() if state.get('open')]
        overall=bool(health.get('durable') and all(node['fresh'] and node['gateway_healthy'] and not node['high_load'] for node in nodes) and not unresolved)
        return {'deployment_mode':self.config.get('deployment_mode','ha'),'timestamp':now,'nodes':nodes,'health':health,'overall_healthy':overall,'recent_exceptions':recent,'routing':{'configured':intent,'effective':effective,'actual':actual,'actual_period_seconds':3600},
                'model_active':active if active_known else None,'started_at':self.store.state('started_at'),
                'operations':self.operations.list() if self.operations else [],'collector_error':self.store.state('collector_error'),'spool_loss':self.store.state('spool_loss'),'disk_issues':disk_issues}

    def primary_origin(self, default):
        if self.node != 'primary':
            return default
        intent = routing.load(self.config['routing_path'])
        return routing.primary_slot(intent)[1] if 'active_slot' in intent else default

    def gateway_sample(self):
        origin=self.primary_origin(self.config['gateway_origin'])
        healthy=False;data=None
        try:
            with urllib.request.urlopen(origin+'/api/status',timeout=2) as response:
                healthy=response.status==200 and json.load(response).get('success') is True
            data=request(origin,'/internal/node-monitor',self.config['gateway_token'])['data']
            data['_sample_origin'] = origin
        except Exception:pass
        return healthy,data

    def access_events(self):
        if self.config.get('access_container'):
            return self.container_access_events()
        path=pathlib.Path(self.config.get('access_log','/nonexistent'))
        if not path.exists():return []
        stat=path.stat();position=self.access_offset
        if position['inode']!=stat.st_ino or position['offset']>stat.st_size:
            position={'inode':stat.st_ino,'offset':0}
        events=[]
        with path.open('rb') as stream:
            stream.seek(position['offset'])
            for _ in range(5000):
                begin=stream.tell();line=stream.readline(16384)
                if not line:break
                if not line.endswith(b'\n'):
                    stream.seek(begin);break
                try:
                    row=json.loads(line);kind=category(row['path'],row['method'])
                    if not kind:continue
                    address=row.get('upstream_addr','')
                    node='relay' if '172.30.10.1:13001' in address else 'primary' if address else 'unknown'
                    status=int(row['status'])
                    outcome='success' if status<400 else 'failure' if status>=500 else 'edge_rejected' if not address else 'rejected'
                    events.append({'timestamp':float(row['epoch'])*1000,'edge_id':row['edge_id'],'event_id':row['edge_id'],'request_id':row.get('request_id',''),
                        'node':node,'category':kind,'model':'','outcome':outcome,'duration_ms':float(row['duration'])*1000,
                        'first_ms':None,'attempts':0,'chain':[],'status':status})
                except (ValueError,KeyError,TypeError):continue
            position['offset']=stream.tell()
        self.access_offset=position
        return events

    def container_access_events(self):
        since=self.store.state('access_since',time.time()-30)
        now=time.time()
        result=subprocess.run(['docker','logs','--since',str(since),'--tail','10000',self.config['access_container']],capture_output=True,text=True,timeout=5)
        if result.returncode:raise RuntimeError('entry log unavailable')
        events=[]
        for line in result.stdout.splitlines():
            try:
                row=json.loads(line);kind=category(row['path'],row['method'])
                if not kind:continue
                address=row.get('upstream_addr','');status=int(row['status'])
                if status==499:outcome='cancelled'
                elif status>=500:outcome='failure'
                elif status>=400:outcome='rejected' if address else 'edge_rejected'
                else:outcome='success'
                events.append({'timestamp':float(row['epoch'])*1000,'edge_id':row['edge_id'],'event_id':row['edge_id'],'request_id':row.get('request_id',''),
                    'node':'relay' if '172.30.10.1:13001' in address else 'primary' if address else 'unknown','category':kind,'model':'','outcome':outcome,'duration_ms':float(row['duration'])*1000,
                    'first_ms':None,'attempts':0,'chain':[],'status':status})
            except (ValueError,KeyError,TypeError):continue
        # Advance only after every parsed event is durably ingested. Repeat overlap deduplicates by edge ID.
        for event in events:self.store.ingest_request(event,1)
        self.store.set_state('access_since',now-2)
        return []

    def ingest(self,payload):
        node=payload['node']
        if node not in self.nodes:raise ValueError('unknown node')
        sample=payload['sample']
        if not isinstance(sample,dict) or len(json.dumps(sample))>65536:raise ValueError('invalid sample')
        self.store.sample(node,sample,update_latest=not payload.get('backfill',False))
        for event in payload.get('events',[]):
            # Gateway attribution is derived from the authenticated reporting node.
            event=dict(event,node=node)
            self.store.ingest_request(event,2)

    def loop(self):
        if not self.store.state('started_at'):self.store.set_state('started_at',time.time())
        cycles=0
        while not self.stop.is_set():
            began=time.monotonic()
            try:
                sample=self.sampler.sample()
                healthy,gateway=self.gateway_sample()
                sample.update(gateway_healthy=healthy,active=gateway.get('active') if gateway else None,
                              model_active=gateway.get('model_active') if gateway else None,observer_dropped=gateway.get('dropped') if gateway else None)
                events=gateway.get('events',[]) if gateway else []
                payload={'node':self.node,'sample':sample,'events':events}
                ident=self.store.spool(payload)
                if gateway and events:
                    origin=gateway.get('_sample_origin', self.config['gateway_origin'])
                    request(origin,'/internal/node-monitor',self.config['gateway_token'],{'sequence':events[-1]['sequence']})
                # Publish the current sample first. A large recovery queue must
                # not keep the live node marked disconnected while it backfills.
                if self.node=='primary':self.ingest(payload)
                else:request(self.config['primary_origin'],'/internal/ingest',self.config['ingest_token'],payload)
                self.store.ack_spool([ident])
                acknowledged=[]
                for pending_id,pending in self.store.pending():
                    pending=dict(pending,backfill=True)
                    if self.node=='primary':self.ingest(pending)
                    else:request(self.config['primary_origin'],'/internal/ingest',self.config['ingest_token'],pending)
                    acknowledged.append(pending_id)
                self.store.ack_spool(acknowledged)
                if self.node=='primary':
                    for event in self.access_events():self.store.ingest_request(event,1)
                    self.store.set_state('access_offset',self.access_offset)
                    self.health_cache=self.executor.health()
                cycles+=1
                if self.store.state('collector_error'):self.store.set_state('collector_error',None)
                if cycles%60==0:self.store.prune()
            except Exception as error:
                # Monitor failures never propagate into business services.
                self.store.set_state('collector_error',{'timestamp':time.time(),'type':type(error).__name__})
            self.stop.wait(max(0,10-(time.monotonic()-began)))

    def alert_loop(self):
        # Notification network waits must never delay host/resource sampling.
        while not self.stop.is_set():
            began=time.monotonic()
            try:
                if self.node=='primary':self.evaluate_primary()
                else:
                    bad=False;title='主机监控或加密链路不可达'
                    try:request(self.config['primary_origin'],'/internal/ping',self.config['ingest_token'])
                    except urllib.error.HTTPError:
                        bad=True;title='主机监控接口异常'
                    except Exception:bad=True
                    self.alerts.check('primary-unreachable','primary',bad,title,'备用机观察主机监控服务；可能是主机或网络故障，未确认整机宕机，未执行接管')
                self.store.set_state('alert_error',None)
            except Exception as error:
                self.store.set_state('alert_error',{'timestamp':time.time(),'type':type(error).__name__})
            self.stop.wait(max(0,10-(time.monotonic()-began)))

    def evaluate_primary(self):
        now=time.time();intent=routing.load(self.config['routing_path']);latest=self.store.latest()
        newest={}
        for job in self.operations.list():
            node=job.get('node') or 'primary'
            if node not in newest:newest[node]=job
        for node,job in newest.items():
            self.alerts.check('operation:'+node,node,job['state']=='failed','网关运维操作失败',job['message'])
        for node in self.nodes:
            sample=latest.get(node);fresh=bool(sample and now-sample['timestamp']<=30 and not sample.get('clock_skew'))
            self.alerts.check('sample:'+node,node,not fresh,'采集失联','宿主机监控数据超过30秒未更新')
            if not fresh:continue
            current_gateway=self.health_cache.get(node,sample['gateway_healthy'])
            self.alerts.check('gateway:'+node,node,current_gateway is False,'网关不可用','模型网关健康检查失败',suppress=intent.get('draining')==node)
            for metric,threshold in (('cpu_percent',85),('memory_percent',90),('load_per_core',1)):
                value=sample.get(metric)
                if value is not None:self.alerts.check(metric+':'+node,node,value>=threshold,'持续高负荷：'+metric,'当前值 %.1f，阈值 %.1f'%(value,threshold),duration=180)
            self.disk_alerts.evaluate(node,sample)
        self.disk_alerts.flush()
        self.alerts.check('durability','primary',not self.health_cache.get('durable'),'账务停写保护已关闭入口','本机数据库持久提交、Redis或主网关未达到健康条件' if self.config.get('deployment_mode')=='single_primary' else '同步复制、Redis或主网关未达到健康条件')
        self.alerts.check('sync-protection','primary',self.health_cache.get('sync_protection') is False,'同步保护被关闭','数据库不再要求指定同步备机；账务入口暂停，未自动降为异步')
        self.alerts.check('tunnel','primary',self.health_cache.get('tunnel') is False,'数据库加密隧道不可达','主机无法连接数据库隧道转发端口')


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self,*args):pass

    def response(self,status,data):
        raw=json.dumps({'success':status<400,'data':data if status<400 else None,'message':str(data) if status>=400 else ''},ensure_ascii=False).encode()
        self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Cache-Control','no-store');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)

    def handle_request(self):
        app=self.server.app
        path=urllib.parse.urlsplit(self.path)
        is_admin=path.path.startswith('/admin/')
        key=app.config.get('admin_token','') if is_admin else app.config.get('control_token','') if path.path=='/internal/restart' else app.config['ingest_token']
        if len(key)<32 or not hmac.compare_digest(self.headers.get('Authorization',''),'Bearer '+key):
            self.response(403,'Access denied');return
        try:
            params={k:v[-1] for k,v in urllib.parse.parse_qs(path.query,max_num_fields=12).items()}
            if len(path.query)>4096:raise ValueError('query too large')
            body=None
            if self.command=='POST':
                length=int(self.headers.get('Content-Length','0'))
                if not 1<=length<=1048576:raise ValueError('invalid body size')
                body=json.loads(self.rfile.read(length))
            if path.path=='/internal/ingest' and self.command=='POST' and app.node=='primary':
                app.ingest(body);data={'accepted':True}
            elif path.path=='/internal/ping' and self.command=='GET':data={'node':app.node,'timestamp':time.time()}
            elif path.path=='/internal/restart' and self.command=='POST' and app.node=='relay':
                if not app.config.get('operations_enabled',False):raise RuntimeError('operations disabled')
                if body!={'action':'restart_gateway'}:raise ValueError('operation not allowed')
                # Main coordinator must already have drained; enforce again locally.
                current=request(app.config['gateway_origin'],'/internal/node-monitor',app.config['gateway_token'])['data']
                if current['active']!=0:raise RuntimeError('gateway still has active requests')
                subprocess.run(['docker','restart','--time','30','omni-ha-standby-new-api-1'],check=True,capture_output=True,timeout=60)
                data={'restarted':True}
            elif not is_admin or app.node!='primary':self.response(404,'Not found');return
            elif path.path=='/admin/snapshot' and self.command=='GET':data=app.snapshot()
            elif path.path=='/admin/series' and self.command=='GET':data=app.store.series(params)
            elif path.path=='/admin/requests' and self.command=='GET':data=app.store.requests(params)
            elif path.path=='/admin/events' and self.command=='GET':data=app.store.events(params)
            elif path.path=='/admin/operations' and self.command=='GET':data={'items':app.operations.list()}
            elif path.path.startswith('/admin/operations/') and self.command=='GET':data=app.operations.get(path.path.rsplit('/',1)[-1])
            elif path.path=='/admin/operations' and self.command=='POST':
                if not app.config.get('operations_enabled',False):raise RuntimeError('Operations are not enabled')
                actor=int(self.headers.get('X-Omni-Actor-ID','0'))
                if actor<=0:raise ValueError('authenticated actor required')
                data=app.operations.submit(body,actor)
            else:self.response(404,'Not found');return
            self.response(200,data)
        except ValueError as error:self.response(400,str(error))
        except KeyError:self.response(404,'Not found')
        except RuntimeError as error:self.response(409,str(error))
        except Exception:self.response(503,'Monitor service unavailable')

    def do_GET(self):self.handle_request()
    def do_POST(self):self.handle_request()


def main():
    parser=argparse.ArgumentParser();parser.add_argument('--config',required=True);args=parser.parse_args()
    config=json.loads(pathlib.Path(args.config).read_text());app=App(config)
    server=http.server.ThreadingHTTPServer((config['bind'],config.get('port',18790)),Handler)
    server.daemon_threads=True
    server.app=app
    threading.Thread(target=app.loop,daemon=True).start()
    threading.Thread(target=app.alert_loop,daemon=True).start()
    server.serve_forever()


if __name__=='__main__':main()
