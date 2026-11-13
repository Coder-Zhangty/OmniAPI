"""Offline patch builder; preserves synchronous durability and manual routing intent."""
import pathlib

def patched_guard(text):
    if 'omni-node-monitor' in text:return text
    text=text.replace('import json,pathlib,subprocess,time,urllib.request,os','import json,pathlib,subprocess,time,urllib.request,os,sys,fcntl\nsys.path.insert(0,"/opt/omni-node-monitor")\nimport routing')
    text=text.replace('def healthy():','DETAILS={}\ndef healthy():\n DETAILS.clear()')
    text=text.replace(" if required!='FIRST 1 (omni_standby)':return False"," DETAILS['sync_protection']=required=='FIRST 1 (omni_standby)'\n if required!='FIRST 1 (omni_standby)':return False")
    text=text.replace(" if count!='1':return False"," DETAILS['replication']=count=='1'\n if count!='1':return False")
    text=text.replace(" if p.returncode or p.stdout.strip()!='PONG':return False"," DETAILS['redis']=p.returncode==0 and p.stdout.strip()=='PONG'\n if p.returncode or p.stdout.strip()!='PONG':return False")
    text=text.replace(" if not available:base=base.replace('    server 172.30.10.1:13001 weight=1 max_fails=1 fail_timeout=10s;\\n','')",' intent=routing.load("/opt/omni-node-monitor/routing.json")\n base,_=routing.render(base,intent,available)')
    # A single lock covers file replacement and nginx reload across daemon/operation calls.
    text=text.replace('def evaluate():','def _evaluate():')
    text=text.replace(" report={'ready':ready,'utc':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())}",''' replication=DETAILS.get('replication');redis=DETAILS.get('redis');tunnel=False
 try:
  import socket
  with socket.create_connection(('172.30.10.1',15433),timeout=1):tunnel=True
 except Exception:pass
 report={'ready':ready,'replication':replication,'redis':redis,'tunnel':tunnel,'sync_protection':DETAILS.get('sync_protection'),'utc':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())}''')
    text=text.replace("if __name__=='__main__':",'''def evaluate():
 with open('/run/omni-ha-guard.lock','w') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX)
  return _evaluate()
if __name__=='__main__':''')
    compile(text,'guard','exec');return text

def patched_nginx(text):
    if '$omni_monitor_block' in text:return text
    text=text.replace('"duration":$request_time}', '"duration":$request_time,"epoch":$msec,"upstream_addr":"$upstream_addr"}')
    text=text.replace('log_format gateway escape=json', 'log_format gateway escape=json',1)
    marker='server {\n'
    maps='''map "$request_method:$uri" $omni_monitor_read {
 default 0;
 "GET:/node-monitor" 1;
 "GET:/node-monitor/" 1;
 "GET:/api/user/self" 1;
 ~^GET:/api/omni/admin/node-monitor/(snapshot|series|requests|events|operations)(/[^/]+)?$ 1;
 ~^GET:/static/ 1;
 "GET:/favicon.ico" 1;
}
map "$omni_route:$http_upgrade" $omni_model_text {
 default 0;
 "omni_text:" 1;
}
'''
    text=text.replace(marker,maps+marker,1)
    existing='if (-f /run/omni-ha/maintenance) { return 503; }'
    assert existing in text,'maintenance gate not found'
    replacement='''set $omni_maintenance 0;
    if (-f /run/omni-ha/maintenance) { set $omni_maintenance 1; }
    if ($omni_monitor_read = 1) { set $omni_maintenance 0; }
    if ($omni_maintenance = 1) { return 503; }
    set $omni_monitor_block 0;
    if (-f /run/omni-ha/draining-primary) { set $omni_monitor_block 1; }
    if ($omni_model_text = 1) { set $omni_monitor_block 0; }
    if ($omni_monitor_read = 1) { set $omni_monitor_block 0; }
    if ($omni_monitor_block = 1) { return 503; }'''
    text=text.replace(existing,replacement)
    # Internal collector route stays private even though gateway listens behind nginx.
    text=text.replace('    location / {','    location ^~ /internal/ { return 404; }\n    location / {')
    # A location-level proxy_set_header overrides inheritance, so set the ID in every proxy location.
    text=text.replace('proxy_set_header Host $host;','proxy_set_header Host $host;\n        proxy_set_header X-Omni-Monitor-ID $request_id;')
    return text

def patched_common(text):
    if 'X-Omni-Monitor-ID' not in text:text=text.replace('proxy_set_header Host $host;','proxy_set_header Host $host;\nproxy_set_header X-Omni-Monitor-ID $request_id;')
    return text
