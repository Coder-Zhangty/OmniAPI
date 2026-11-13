"""Asynchronous allowlisted jobs; restart never interrupts an un-drained gateway."""
import hashlib
import json
import threading
import time
import uuid

import routing


class Operations:
    def __init__(self,store,config_path,executor,clock=time.monotonic,sleep=time.sleep,drain_timeout=300):
        self.store,self.path,self.executor=store,config_path,executor
        self.clock,self.sleep,self.timeout=clock,sleep,drain_timeout
        self.lock=threading.Lock()
        interrupted=[]
        for job in self.list():
            if job['state'] not in ('succeeded','cancelled','failed'):
                if job['action']=='restart_gateway':interrupted.append(job['node'])
                job.update(state='failed',message='Monitor restarted; no restart command was replayed')
                self.update(job)
        config=routing.load(self.path)
        if config['draining'] or interrupted:
            config['blocked']=list(set(config.get('blocked',[])+interrupted))
            config['draining']=None;config['revision']+=1;routing.save(self.path,config)
            self.executor.apply(config)

    def list(self):
        with self.store.lock:
            return [json.loads(x[0]) for x in self.store.db.execute('SELECT data FROM jobs ORDER BY ts DESC LIMIT 50')]

    def get(self,ident):
        with self.store.lock:
            row=self.store.db.execute('SELECT data FROM jobs WHERE id=?',(ident,)).fetchone()
            if not row:raise KeyError('operation not found')
            return json.loads(row[0])

    def update(self,job):
        job['updated_at']=time.time()
        with self.store.lock,self.store.db:
            self.store.db.execute('UPDATE jobs SET data=? WHERE id=?',(json.dumps(job),job['id']))

    def submit(self,body,actor,start_thread=True):
        action=body.get('action');node=body.get('node')
        if action not in ('set_weights','set_relay_enabled','restart_gateway'):
            raise ValueError('operation not allowed')
        if action=='restart_gateway' and node not in ('primary','relay'):
            raise ValueError('invalid node')
        if action=='set_weights' and (type(body.get('primary')) is not int or type(body.get('relay')) is not int or body['primary']+body['relay']!=100 or min(body['primary'],body['relay'])<0):
            raise ValueError('weights must sum to 100')
        if action=='set_relay_enabled' and type(body.get('enabled')) is not bool:
            raise ValueError('invalid enabled flag')
        allowed={'action','node','primary','relay','enabled','idempotency_key','revision'}
        if set(body)-allowed:raise ValueError('unknown operation fields')
        idem=str(body.get('idempotency_key',''))
        if not 16<=len(idem)<=128 or type(body.get('revision')) is not int:
            raise ValueError('idempotency key and revision required')
        fingerprint=hashlib.sha256(json.dumps(body,sort_keys=True).encode()).hexdigest()
        with self.store.lock:
            row=self.store.db.execute('SELECT data FROM jobs WHERE idem=?',(idem,)).fetchone()
            if row:
                job=json.loads(row[0])
                if job['fingerprint']!=fingerprint or job['actor']!=actor:raise ValueError('idempotency key conflict')
                return job
        if not self.lock.acquire(blocking=False):raise RuntimeError('another operation is active')
        try:
            config=routing.load(self.path)
            if config['revision']!=body['revision']:raise RuntimeError('routing revision changed; refresh and retry')
            job={'id':uuid.uuid4().hex,'action':action,'node':node,'actor':actor,'state':'queued','message':'Queued','created_at':time.time(),'body':body,'fingerprint':fingerprint}
            with self.store.lock,self.store.db:
                self.store.db.execute('INSERT INTO jobs VALUES (?,?,?,?)',(job['id'],idem,job['created_at'],json.dumps(job)))
            self.store.event('operation',node or 'primary',{'job_id':job['id'],'action':action,'actor':actor,'state':'queued'})
            if start_thread:
                threading.Thread(target=self.run,args=(job,),daemon=True).start()
            return job
        except Exception:
            self.lock.release();raise

    def run(self,job):
        original=routing.load(self.path)
        changed=False
        try:
            body=job['body'];config=dict(original)
            health=self.executor.health()
            if job['action']=='set_weights':
                if body['relay'] and (not health['relay'] or not config['relay_enabled']):raise RuntimeError('relay is not accepting requests')
                if body['primary'] and not health['primary']:raise RuntimeError('primary gateway unavailable')
                config.update(primary=body['primary'],relay=body['relay'])
                config['blocked']=[node for node in config.get('blocked',[]) if not body[node]]
            elif job['action']=='set_relay_enabled':
                if body['enabled'] and not health['relay']:raise RuntimeError('relay gateway unavailable')
                if not body['enabled'] and not health['primary']:raise RuntimeError('primary gateway unavailable')
                config['relay_enabled']=body['enabled']
                if body['enabled']:config['blocked']=[node for node in config.get('blocked',[]) if node!='relay']
            else:
                target=body['node'];other='relay' if target=='primary' else 'primary'
                if not health['durable'] or not health[other]:raise RuntimeError('healthy peer and synchronous replication required')
                config['draining']=target
            config['revision']+=1;routing.save(self.path,config);changed=True;self.executor.apply(config)
            if job['action']=='restart_gateway':
                job.update(state='draining',message='Waiting for all gateway requests to finish');self.update(job)
                deadline=self.clock()+self.timeout
                while True:
                    health=self.executor.health()
                    if not health['durable'] or not health[other]:raise RuntimeError('peer or synchronous replication became unavailable')
                    active=self.executor.active(target)
                    if active==0:break
                    if self.clock()>=deadline:
                        job.update(state='cancelled',message='Drain timeout; gateway was not restarted');break
                    job['active_requests']=active;job['remaining_seconds']=max(0,int(deadline-self.clock()));self.update(job);self.sleep(2)
                if job['state']!='cancelled':
                    # Verify twice after removing the target from routing; monitor endpoints are excluded.
                    self.sleep(1)
                    if self.executor.active(target)!=0:raise RuntimeError('new requests appeared while draining')
                    job.update(state='restarting',message='Gateway drained; restarting');self.update(job)
                    self.executor.restart(target)
                    job.update(state='verifying',message='Checking gateway recovery');self.update(job)
                    deadline=self.clock()+60
                    while not self.executor.health()[target]:
                        if self.clock()>=deadline:raise RuntimeError('gateway did not recover')
                        self.sleep(2)
                restore=dict(original,revision=config['revision']+1,draining=None)
                routing.save(self.path,restore);self.executor.apply(restore);changed=False
            if job['state']!='cancelled':job.update(state='succeeded',message='Operation completed')
        except Exception as error:
            job.update(state='failed',message=str(error)[:200])
            if changed:
                # Keep a failed gateway excluded by health, but always release the temporary drain state.
                restore=dict(original,revision=routing.load(self.path)['revision']+1,draining=None)
                if job['action']=='restart_gateway' and job.get('state')=='failed':
                    restore['blocked']=list(set(original.get('blocked',[])+[body['node']]))
                routing.save(self.path,restore)
                try:self.executor.apply(restore)
                except Exception:pass
        finally:
            self.update(job)
            self.store.event('operation',job.get('node') or 'primary',{'job_id':job['id'],'action':job['action'],'actor':job['actor'],'state':job['state'],'message':job['message']})
            self.lock.release()
