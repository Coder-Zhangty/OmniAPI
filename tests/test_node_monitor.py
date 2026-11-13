import pathlib
import sys
import tempfile
import time
import unittest

sys.path.insert(0,str(pathlib.Path(__file__).resolve().parents[1]/'ops/node_monitor'))
from alerts import Alerts
from metrics import histogram,percentile,category
from operations import Operations
from store import Store
import routing


class MonitorTest(unittest.TestCase):
    def setUp(self):
        parent=pathlib.Path(__file__).resolve().parents[1]/'runtime/node-monitor-tests'
        parent.mkdir(parents=True,exist_ok=True)
        self.temp=tempfile.TemporaryDirectory(dir=parent)
        self.root=pathlib.Path(self.temp.name)
        self.store=Store(self.root/'monitor.sqlite')
    def tearDown(self):
        self.store.db.close();self.temp.cleanup()
    def event(self,ident='request-1',**changes):
        return {'edge_id':ident,'event_id':ident,'timestamp':int(time.time()*1000),'node':'primary','category':'model','model':'test-model','outcome':'success','duration_ms':300,'first_ms':100,'attempts':2,'chain':['1','6'],**changes}
    def summary(self):return self.store.series({'start':time.time()-3600,'end':time.time()+1})['summary']
    def test_channel_retry_and_duplicate_report_count_one_user_request(self):
        event=self.event();self.assertTrue(self.store.ingest_request(event));self.assertFalse(self.store.ingest_request(event))
        result=self.summary();self.assertEqual(result['total'],1)
        rows=self.store.series({'start':time.time()-3600,'end':time.time()+1})['requests']
        self.assertEqual(rows[0]['attempts'],2)
    def test_gateway_stream_failure_replaces_edge_http200_without_double_count(self):
        self.store.ingest_request(self.event(model='',attempts=0,first_ms=None),quality=1)
        self.store.ingest_request(self.event(outcome='failure'),quality=2)
        self.store.ingest_request(self.event(model='',attempts=0),quality=1)
        result=self.summary();self.assertEqual(result['total'],1);self.assertEqual(result['failure'],1);self.assertEqual(result['success'],0)
    def test_business_rejection_and_cancellation_are_distinct_from_service_failures(self):
        self.store.ingest_request(self.event('rejected',outcome='rejected'))
        self.store.ingest_request(self.event('cancelled',outcome='cancelled'))
        result=self.summary();self.assertEqual(result['rejected'],1);self.assertEqual(result['cancelled'],1);self.assertEqual(result['success_rate'],0)
    def test_histograms_merge_counts_not_percentiles(self):
        samples=[histogram(10) for _ in range(99)]+[histogram(60000)]
        merged=[sum(x) for x in zip(*samples)]
        self.assertEqual(percentile(merged,95),10)
    def test_long_latency_overflow_does_not_understate_one_hour(self):
        self.assertEqual(percentile(histogram(2*3600000),95),86400000)
    def test_future_host_clock_is_not_healthy_fresh_metrics(self):
        self.store.sample('relay',{'timestamp':time.time()+60,'cpu_percent':10,'disks':[]})
        self.assertTrue(self.store.latest()['relay']['clock_skew'])
        self.store.sample('relay',{'timestamp':time.time(),'cpu_percent':20,'disks':[]})
        self.assertFalse(self.store.latest()['relay'].get('clock_skew',False))
    def test_backfill_never_replaces_current_node_status(self):
        now=time.time()
        self.store.sample('relay',{'timestamp':now,'cpu_percent':20,'disks':[]})
        self.store.sample('relay',{'timestamp':now+1000,'cpu_percent':90,'disks':[]},update_latest=False)
        self.assertEqual(self.store.latest()['relay']['cpu_percent'],20)
    def test_request_summary_drops_unapproved_sensitive_fields(self):
        self.store.ingest_request(self.event(body='private chat',authorization='private key'))
        rows=self.store.requests({'start':time.time()-3600,'end':time.time()+1})['items']
        self.assertNotIn('body',rows[0]);self.assertNotIn('authorization',rows[0])
    def test_disk_aggregates_keep_average_and_peak(self):
        now=time.time()//60*60+1
        for delta,value in [(0,20),(10,40)]:
            self.store.sample('primary',{'timestamp':now+delta,'disks':[{'device':'test-device','percent':value,'used':value,'free':100-value}]})
        data=self.store.series({'start':now-1,'end':now+20})['resources']
        self.assertEqual(max(row['peak']['disk:test-device:percent'] for row in data),40)
    def test_wecom_requires_application_success_not_only_http200(self):
        import importlib.util
        file=pathlib.Path(__file__).resolve().parents[1]/'ops/support/notifier.py'
        spec=importlib.util.spec_from_file_location('monitor_test_notifier',file)
        notifier=importlib.util.module_from_spec(spec);spec.loader.exec_module(notifier)
        secrets={'wecom':{'webhook':'https://example.invalid/test-only'}}
        for raw,expected in [('{"errcode":0}',True),('{"errcode":93000}',False),('invalid',False)]:
            result=notifier.publish('wecom',secrets,'test','test',transport=lambda *_:(200,raw))
            self.assertEqual(result['ok'],expected)
    def test_relay_independent_alarm_without_main_database_or_redis(self):
        from unittest.mock import patch
        from service import App
        app=App({'node':'relay','database':str(self.root/'relay.sqlite'),'gateway_origin':'http://127.0.0.1:13000','primary_origin':'http://private.test','gateway_token':'test-only','ingest_token':'test-only'})
        class Stop:
            cycles=0
            def is_set(self):return self.cycles>=3
            def wait(self,seconds):self.cycles+=1
        app.stop=Stop();app.gateway_sample=lambda:(True,{'events':[],'active':0,'model_active':0})
        app.sampler.sample=lambda:{'timestamp':time.time(),'cpu_percent':0,'disks':[]}
        sent=[];app.alerts.publish=lambda *args:(sent.append(args) or {'ok':True})
        with patch('service.request',side_effect=ConnectionError('simulated private tunnel unavailable')):
            app.loop();app.stop=Stop();app.alert_loop()
        self.assertEqual(len(sent),1);self.assertIn('可能是主机或网络故障',sent[0][1]);self.assertEqual(app.store.db.execute('SELECT count(*) FROM spool').fetchone()[0],3)
        app.store.db.close()
    def test_duplicate_host_sample_does_not_distort_aggregates(self):
        now=time.time();sample={'timestamp':now,'cpu_percent':50,'memory_percent':25,'disks':[]}
        self.store.sample('primary',sample);self.store.sample('primary',sample)
        row=self.store.db.execute('SELECT data FROM resources WHERE level=60').fetchone()
        import json
        self.assertEqual(json.loads(row[0])['count']['cpu_percent'],1)
    def test_static_assets_and_monitor_probes_are_not_business_requests(self):
        for path in ('/assets/app.js','/brand/support.html','/api/status','/api/omni/admin/node-monitor/snapshot'):
            self.assertIsNone(category(path))
        self.assertEqual(category('/v1/chat/completions','POST'),'model')
    def test_manual_relay_pause_survives_healthy_recovery(self):
        intent={**routing.DEFAULT,'relay_enabled':False}
        effective=routing.weights(intent,True)
        self.assertEqual(effective,{'primary':100,'relay':0})
        template='upstream omni_text { server old; }'
        result,_=routing.render(template,intent,True)
        self.assertNotIn('13001',result)
    def test_notification_backoff_and_persistent_dedup(self):
        clock=[1000.0];calls=[]
        alarm=Alerts(self.store,lambda *args:(calls.append(args) or {'ok':True}),clock=lambda:clock[0])
        for _ in range(3):alarm.check('cpu','primary',True,'High CPU','85%')
        self.assertEqual(len(calls),1)
        alarm=Alerts(self.store,lambda *args:(calls.append(args) or {'ok':True}),clock=lambda:clock[0])
        clock[0]+=10;alarm.check('cpu','primary',True,'High CPU','85%');self.assertEqual(len(calls),1)
        for _ in range(3):alarm.check('cpu','primary',False,'High CPU','normal')
        self.assertEqual(len(calls),2)
    def test_restart_timeout_cancels_without_restart_and_restores_intent(self):
        clock=[0.0]
        class Executor:
            calls=0
            def health(self):return {'primary':True,'relay':True,'durable':True}
            def active(self,node):return 1
            def apply(self,config):pass
            def restart(self,node):self.calls+=1
        executor=Executor();path=self.root/'routing.json'
        ops=Operations(self.store,path,executor,clock=lambda:clock[0],sleep=lambda seconds:clock.__setitem__(0,clock[0]+seconds),drain_timeout=5)
        job=ops.submit({'action':'restart_gateway','node':'relay','revision':1,'idempotency_key':'restart-once-1234'},100,start_thread=False)
        ops.run(job)
        self.assertEqual(executor.calls,0);self.assertEqual(ops.get(job['id'])['state'],'cancelled');self.assertIsNone(routing.load(path)['draining'])
    def test_completed_operation_replay_returns_existing_job(self):
        class Executor:
            def health(self):return {'primary':True,'relay':True,'durable':True}
            def apply(self,config):pass
        ops=Operations(self.store,self.root/'routing.json',Executor())
        body={'action':'set_weights','primary':90,'relay':10,'revision':1,'idempotency_key':'weights-once-1234'}
        job=ops.submit(body,100,start_thread=False);ops.run(job)
        self.assertEqual(ops.submit(body,100)['id'],job['id'])
        with self.assertRaises(ValueError):ops.submit(dict(body,primary=80,relay=20),100)

    def test_primary_drain_temporarily_routes_text_to_manually_paused_healthy_peer(self):
        config={**routing.DEFAULT,'relay_enabled':False,'draining':'primary'}
        self.assertEqual(routing.weights(config,True),{'primary':0,'relay':100})

    def test_failed_restart_remains_quarantined_after_health_recovers(self):
        class Executor:
            def health(self):return {'primary':True,'relay':True,'durable':True}
            def active(self,node):return 0
            def apply(self,config):pass
            def restart(self,node):raise RuntimeError('restart failed')
        path=self.root/'routing.json';ops=Operations(self.store,path,Executor(),sleep=lambda seconds:None)
        job=ops.submit({'action':'restart_gateway','node':'relay','revision':1,'idempotency_key':'failed-restart-1234'},100,start_thread=False)
        ops.run(job)
        config=routing.load(path)
        self.assertEqual(ops.get(job['id'])['state'],'failed')
        self.assertEqual(routing.weights(config,True),{'primary':100,'relay':0})

    def test_successful_restart_drains_before_command_and_restores_original_weights(self):
        calls=[]
        class Executor:
            def health(self):return {'primary':True,'relay':True,'durable':True}
            def active(self,node):calls.append('active');return 0
            def apply(self,config):calls.append('apply:'+str(config['draining']))
            def restart(self,node):calls.append('restart')
        path=self.root/'routing.json';ops=Operations(self.store,path,Executor(),sleep=lambda seconds:None)
        job=ops.submit({'action':'restart_gateway','node':'relay','revision':1,'idempotency_key':'success-restart-12'},100,start_thread=False);ops.run(job)
        self.assertEqual(calls[:4],['apply:relay','active','active','restart'])
        self.assertEqual(ops.get(job['id'])['state'],'succeeded')
        self.assertEqual(routing.load(path)['relay'],20)

    def test_retention_preserves_90day_trend_without_old_request_summaries(self):
        now=time.time();event=self.event(timestamp=(now-8*86400)*1000)
        self.store._bucket(event,1);self.store.db.commit()
        self.store.prune(now)
        series=self.store.series({'start':now-30*86400,'end':now})
        self.assertEqual(series['summary']['total'],1)
        self.assertEqual(self.store.requests({'start':now-30*86400,'end':now})['total'],0)

    def test_cross_node_counts_and_merged_latency_filters(self):
        self.store.ingest_request(self.event('main',node='primary',duration_ms=10))
        self.store.ingest_request(self.event('relay',node='relay',duration_ms=60000))
        result=self.summary();self.assertEqual(result['node_counts'],{'primary':1,'relay':1})
        one=self.store.series({'start':time.time()-3600,'end':time.time()+1,'node':'relay'})
        self.assertEqual(one['summary']['total'],1);self.assertEqual(one['summary']['p50_ms'],60000)

    def test_resource_thresholds_require_sustained_duration(self):
        clock=[1000.0];sent=[]
        alarm=Alerts(self.store,lambda *args:(sent.append(args) or {'ok':True}),clock=lambda:clock[0])
        alarm.check('cpu','primary',True,'CPU','high',duration=180)
        clock[0]+=179;alarm.check('cpu','primary',True,'CPU','high',duration=180)
        self.assertEqual(sent,[])
        clock[0]+=1;alarm.check('cpu','primary',True,'CPU','high',duration=180)
        self.assertEqual(len(sent),1)

    def test_website_recharge_and_support_routes_are_not_discarded_as_brand_assets(self):
        self.assertEqual(category('/brand/api/topup/request','POST'),'recharge')
        self.assertEqual(category('/brand/api/chat/messages','POST'),'support')

    def test_global_mutex_and_stale_revision_are_rejected(self):
        class Executor:
            def health(self):return {'primary':True,'relay':True,'durable':True}
            def apply(self,config):pass
        ops=Operations(self.store,self.root/'routing.json',Executor())
        body={'action':'set_weights','primary':80,'relay':20,'revision':1,'idempotency_key':'mutex-first-job-1234'}
        job=ops.submit(body,1,start_thread=False)
        with self.assertRaisesRegex(RuntimeError,'another operation'):ops.submit(dict(body,idempotency_key='mutex-second-job-1234'),1,start_thread=False)
        ops.run(job)
        with self.assertRaisesRegex(RuntimeError,'revision changed'):ops.submit(dict(body,idempotency_key='mutex-stale-job-1234'),1,start_thread=False)

    def test_executor_service_restart_quarantines_unfinished_gateway_job(self):
        class Executor:
            calls=0
            def apply(self,config):self.calls+=1
            def restart(self,node):raise AssertionError('restart must never be replayed')
        executor=Executor();path=self.root/'routing.json'
        first=Operations(self.store,path,executor)
        job=first.submit({'action':'restart_gateway','node':'relay','revision':1,'idempotency_key':'unfinished-job-1234'},1,start_thread=False)
        routing.save(path,dict(routing.load(path),draining='relay'))
        recovered=Operations(self.store,path,executor)
        self.assertEqual(recovered.get(job['id'])['state'],'failed')
        self.assertIn('relay',routing.load(path)['blocked']);self.assertIsNone(routing.load(path)['draining'])
        first.lock.release()

class DiskPolicyTest(unittest.TestCase):
    def setUp(self):
        from disk_alerts import DiskAlerts
        parent=pathlib.Path(__file__).resolve().parents[1]/'runtime/node-monitor-tests'
        parent.mkdir(parents=True,exist_ok=True)
        self.temp=tempfile.TemporaryDirectory(dir=parent)
        self.store=Store(pathlib.Path(self.temp.name)/'monitor.sqlite')
        self.clock=[1000.0];self.sent=[]
        self.alarm=DiskAlerts(self.store,self.publish,clock=lambda:self.clock[0])
    def tearDown(self):
        self.store.db.close();self.temp.cleanup()
    def publish(self,*args):
        self.sent.append(args);return {'ok':True}
    def disk(self,device='8:1',mount='/',percent=92,free=1.5*1024**3,role='business',**fields):
        return {'device':device,'mount':mount,'percent':percent,'free':free,'role':role,'business_paths':[mount],**fields}
    def tick(self,disks,seconds=10,node='primary',errors=None,flush=True):
        self.clock[0]+=seconds
        self.alarm.evaluate(node,{'timestamp':self.clock[0],'disks':disks,'disk_policy_version':2,'disk_path_errors':errors or []})
        if flush:self.alarm.flush()
    def confirm(self,disks,node='primary'):
        for seconds in (10,30,30):self.tick(disks,seconds,node)
    def test_tiny_boot_and_efi_are_quiet_and_large_busy_disk_requires_low_free(self):
        disks=[self.disk(mount='/boot',role='maintenance',percent=13,free=.69*1024**3),
               self.disk(device='8:2',mount='/boot/efi',role='maintenance',percent=6,free=90*1024**2),
               self.disk(device='8:3',percent=95,free=5*1024**3),
               self.disk(device='8:4',percent=50,free=1.5*1024**3)]
        self.confirm(disks);self.tick(disks,24*3600)
        self.assertEqual(self.sent,[])
        self.assertFalse(any(state['open'] for state in self.alarm.states().values()))
    def test_capacity_warn_confirms_after_60_seconds_and_never_reminds_or_replays(self):
        from disk_alerts import DiskAlerts
        disks=[self.disk()]
        self.tick(disks,0)
        for _ in range(5):self.tick(disks,10)
        self.tick(disks,9)
        self.assertEqual(self.sent,[])
        self.tick(disks,1);self.assertEqual(len(self.sent),1)
        self.tick(disks,1801)
        self.alarm=DiskAlerts(self.store,self.publish,clock=lambda:self.clock[0])
        self.tick(disks,24*3600)
        self.assertEqual(len(self.sent),1)
    def test_critical_absolute_free_escalation_and_recovery_hysteresis(self):
        self.confirm([self.disk()])
        self.confirm([self.disk(percent=50,free=500*1024**2)])
        self.assertEqual(len(self.sent),2)
        for _ in range(3):self.tick([self.disk(percent=92,free=700*1024**2)])
        self.assertEqual(next(iter(self.alarm.states().values()))['level'],'critical')
        for _ in range(3):self.tick([self.disk(percent=92,free=800*1024**2)])
        self.assertEqual(next(iter(self.alarm.states().values()))['level'],'warning')
        for _ in range(3):self.tick([self.disk(percent=89,free=2.4*1024**3)])
        self.assertEqual(len(self.sent),2)
        for _ in range(3):self.tick([self.disk(percent=89,free=2.6*1024**3)])
        self.assertEqual(len(self.sent),3);self.assertIn('磁盘恢复',self.sent[-1][1])
        self.tick([self.disk(percent=89,free=2.6*1024**3)],1801)
        self.assertEqual(len(self.sent),3)
    def test_boot_maintenance_notifies_once_and_recovers_below_90(self):
        self.confirm([self.disk(mount='/boot',role='maintenance',percent=95,free=1)])
        self.assertEqual(next(iter(self.alarm.states().values()))['level'],'maintenance')
        for _ in range(3):self.tick([self.disk(mount='/boot',role='maintenance',percent=91,free=1)])
        self.assertEqual(next(iter(self.alarm.states().values()))['level'],'maintenance')
        for _ in range(3):self.tick([self.disk(mount='/boot',role='maintenance',percent=89,free=1)])
        self.assertFalse(next(iter(self.alarm.states().values()))['open']);self.assertEqual(len(self.sent),2)
        self.assertIn('系统更新或后续重启',self.sent[0][1])
    def test_readonly_boot_is_a_maintenance_risk_not_a_business_outage(self):
        disk=self.disk(mount='/boot',role='maintenance',percent=10,free=100*1024**2,readonly=True)
        for _ in range(3):self.tick([disk])
        state=next(iter(self.alarm.states().values()))
        self.assertEqual(state['level'],'maintenance');self.assertEqual(len(self.sent),1)
        self.assertIn('启动文件系统只读',self.sent[0][1])
    def test_one_batch_covers_both_nodes_and_multiple_filesystems(self):
        for seconds in (0,30,30):
            self.tick([self.disk(),self.disk(device='8:2',mount='/srv/omni-data')],seconds,flush=False)
            self.tick([self.disk()],0,node='relay',flush=False)
            self.alarm.flush()
        self.assertEqual(len(self.sent),1)
        self.assertIn('3项',self.sent[0][0]);self.assertIn('备用服务器',self.sent[0][1])
    def test_repeated_cached_sample_cannot_confirm_readonly_or_missing_path(self):
        disks=[self.disk(readonly=True)]
        self.tick(disks,0);self.tick(disks,0);self.tick(disks,0)
        self.assertEqual(self.sent,[])
        self.tick(disks);self.tick(disks)
        self.assertEqual(len(self.sent),1)
        self.assertIn('只读',self.sent[0][1])
    def test_missing_required_mount_confirms_in_three_samples_and_recovery_is_delivered(self):
        errors=[{'path':'/srv/omni-data','error':'required_mount_missing'}]
        for _ in range(3):self.tick([],errors=errors)
        self.assertEqual(len(self.sent),1)
        recovered=[self.disk(mount='/srv/omni-data',percent=30,free=10*1024**3)]
        for _ in range(3):self.tick(recovered)
        self.assertEqual(len(self.sent),2);self.assertIn('恢复',self.sent[-1][1])
    def test_failed_batch_survives_restart_and_unsent_recovery_is_quiet(self):
        from disk_alerts import DiskAlerts
        failures=[]
        self.alarm.publish=lambda *args:(failures.append(args) or {'ok':False,'error':'offline'})
        self.confirm([self.disk()]);self.assertEqual(len(failures),1)
        self.alarm=DiskAlerts(self.store,self.publish,clock=lambda:self.clock[0])
        self.tick([self.disk()],29);self.assertEqual(self.sent,[])
        self.tick([self.disk()],1);self.assertEqual(len(self.sent),1)
        self.alarm.publish=lambda *args:{'ok':False}
        self.confirm([self.disk(device='8:2')])
        self.alarm.publish=self.publish
        for _ in range(3):self.tick([self.disk(device='8:2',percent=30,free=10*1024**3)],1)
        self.alarm.flush();self.assertEqual(len(self.sent),1)
    def test_legacy_boot_incidents_close_silently_and_old_observer_is_compatible(self):
        from disk_alerts import DiskAlerts
        self.store.set_state('disk_policy_migrated_v2',False)
        self.store.set_state('incident:disk:primary:8:1',{'open':True,'bad_samples':100,'last_sent':1})
        self.alarm=DiskAlerts(self.store,self.publish,clock=lambda:self.clock[0])
        self.assertFalse(self.store.state('incident:disk:primary:8:1')['open'])
        self.assertEqual(self.sent,[])
        old={'device':'8:1','mount':'/boot/efi','percent':6,'free':90*1024**2}
        self.confirm([old]);self.assertEqual(self.sent,[])
    def test_sample_gap_does_not_count_as_continuous_capacity_shortage(self):
        self.tick([self.disk()],0);self.tick([self.disk()],61)
        self.assertEqual(self.sent,[])
        self.tick([self.disk()],30);self.tick([self.disk()],30)
        self.assertEqual(len(self.sent),1)
    def test_maintenance_does_not_turn_overall_business_health_red(self):
        from service import App
        from unittest.mock import patch
        app=App({'node':'primary','database':str(pathlib.Path(self.temp.name)/'snapshot.sqlite'),
                 'routing_path':str(pathlib.Path(self.temp.name)/'routing.json')})
        app.store.db.close();app.store=self.store;app.disk_alerts=self.alarm;app.operations.store=self.store
        self.confirm([self.disk(mount='/boot',role='maintenance',percent=95,free=1)])
        sample={'timestamp':self.clock[0],'gateway_healthy':True,'model_active':0,'disks':[]}
        for node in ('primary','relay'):self.store.sample(node,sample,received=self.clock[0])
        app.health_cache={'durable':True,'primary':True,'relay':True,'checked_at':self.clock[0]}
        with patch('service.time.time',return_value=self.clock[0]):snapshot=app.snapshot()
        self.assertTrue(snapshot['overall_healthy'])
        self.assertEqual(snapshot['disk_issues'][0]['severity'],'maintenance')
    def test_business_paths_resolve_symlinks_and_deduplicate_devices_and_missing_mount(self):
        from metrics import HostSampler
        from unittest.mock import patch
        from types import SimpleNamespace
        targets=[{'path':'/opt/ai-gateway/runtime'},{'path':'/var/lib/omni-sync'},
                 {'path':'/srv/omni-data','required_mount':'/srv/omni-data'}]
        sampler=HostSampler(business_paths=targets)
        disks=[self.disk(device='8:2',role='display',business_paths=[])]
        with patch('metrics.os.stat',return_value=SimpleNamespace(st_dev=42)),patch('metrics.os.major',create=True,return_value=8),patch('metrics.os.minor',create=True,return_value=2):
            errors=sampler.map_business_paths(disks,{'/'})
        self.assertEqual(disks[0]['role'],'business')
        self.assertEqual(disks[0]['business_paths'],['/opt/ai-gateway/runtime','/var/lib/omni-sync'])
        self.assertEqual(errors,[{'path':'/srv/omni-data','error':'required_mount_missing'}])
    def test_observer_sandbox_readonly_bind_is_not_a_host_disk_fault(self):
        from metrics import HostSampler
        from unittest.mock import patch
        from types import SimpleNamespace
        proc=pathlib.Path(self.temp.name)/'proc';(proc/'self').mkdir(parents=True);(proc/'net').mkdir()
        for file,content in {'stat':'cpu 1 1 1 1 1 1 1 1\n','meminfo':'MemTotal: 1000 kB\nMemAvailable: 500 kB\n',
                             'loadavg':'0 0 0 1/1 1','uptime':'100 50','net/route':'Iface Destination\n',
                             'net/dev':'header\nheader\n','self/mountinfo':'1 0 8:1 / / ro - ext4 /dev/vda1 rw\n'}.items():
            (proc/file).write_text(content)
        fake=SimpleNamespace(f_blocks=100,f_frsize=1024**3,f_bavail=50,f_bfree=50,f_flag=1)
        with patch('metrics.os.statvfs',create=True,return_value=fake):
            sample=HostSampler(proc).sample()
        self.assertFalse(sample['disks'][0]['readonly'])
        (proc/'1').mkdir();(proc/'1/mountinfo').write_text('1 0 8:1 / / ro - ext4 /dev/vda1 ro\n')
        with patch('metrics.os.statvfs',create=True,return_value=fake):
            sample=HostSampler(proc).sample()
        self.assertTrue(sample['disks'][0]['readonly'])

if __name__=='__main__':unittest.main()
