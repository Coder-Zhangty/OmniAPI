"""Verify standalone gate keeps local durability and cannot route to retired relay."""
import ast,importlib.util,pathlib,types,unittest
R=pathlib.Path(__file__).resolve().parents[1]
class SinglePrimary(unittest.TestCase):
 def gate(self,settings,redis=True,primary=True,single=True):
  calls=[]
  def sql(q):calls.append(q);return settings[q]
  class Response:
   status=200
   def __enter__(self):return self
   def __exit__(self,*a):pass
  tree=ast.parse((R/'ops/migration/ha_guard.py').read_text());func=next(x for x in tree.body if isinstance(x,ast.FunctionDef) and x.name=='healthy')
  ns={'DETAILS':{},'single_primary':lambda:single,'sql':sql,'subprocess':types.SimpleNamespace(run=lambda *a,**k:types.SimpleNamespace(returncode=0 if redis else 1,stdout='PONG')),'urllib':types.SimpleNamespace(request=types.SimpleNamespace(urlopen=lambda *a,**k:Response())),'json':types.SimpleNamespace(load=lambda r:{'success':primary}),'routing':types.SimpleNamespace(load=lambda *a:{},primary_slot=lambda *a:('new-api:3000','http://127.0.0.1:13000'))}
  exec(compile(ast.Module(body=[func],type_ignores=[]),'gate','exec'),ns)
  return ns['healthy'](),ns['DETAILS'],calls
 def test_standalone_healthy_without_replica(self):
  ok,detail,calls=self.gate({'SHOW synchronous_standby_names;':'','SHOW synchronous_commit;':'on','SELECT pg_is_in_recovery();':'f'})
  self.assertTrue(ok);self.assertTrue(detail['local_commit']);self.assertIsNone(detail['sync_protection']);self.assertFalse(any('pg_stat_replication' in q for q in calls))
 def test_standalone_fails_closed_on_unsafe_local_state(self):
  base={'SHOW synchronous_standby_names;':'','SHOW synchronous_commit;':'on','SELECT pg_is_in_recovery();':'f'}
  for key,value in [('SHOW synchronous_standby_names;','FIRST 1 (omni_standby)'),('SHOW synchronous_commit;','off'),('SELECT pg_is_in_recovery();','t')]:
   with self.subTest(key=key):self.assertFalse(self.gate(dict(base,**{key:value}))[0])
  self.assertFalse(self.gate(base,redis=False)[0]);self.assertFalse(self.gate(base,primary=False)[0])
 def test_ha_still_requires_replica(self):
  self.assertFalse(self.gate({'SHOW synchronous_standby_names;':''},single=False)[0])
 def test_no_fallback_to_retired_relay(self):
  spec=importlib.util.spec_from_file_location('single_routing',R/'ops/node_monitor/routing.py');module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
  config={'deployment_mode':'single_primary','primary':100,'relay':0,'relay_enabled':False,'blocked':[],'draining':None}
  self.assertEqual(module.weights(config,False),{'primary':100,'relay':0})
  self.assertEqual(module.weights(dict(config,blocked=['primary']),True),{'primary':0,'relay':0})
  self.assertEqual(module.weights(dict(config,draining='primary'),True),{'primary':0,'relay':0})
  base=(R/'nginx/ha-upstream.conf').read_text()
  green=dict(config,active_slot='green')
  rendered,effective=module.render(base,green,False)
  self.assertEqual(effective,{'primary':100,'relay':0})
  self.assertEqual(rendered.count('new-api-green:3000'),2)
  self.assertEqual(module.primary_slot(green)[1],'http://127.0.0.1:13002')
  self.assertNotIn('server 127.0.0.1:9 down',rendered)
if __name__=='__main__':unittest.main()
