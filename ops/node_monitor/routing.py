"""One persistent intent shared by the operator and the HA health guard."""
import json
import os
import pathlib
import re

DEFAULT = {'revision':1, 'primary':80, 'relay':20, 'relay_enabled':True, 'draining':None, 'blocked':[]}
PRIMARY_SLOTS = {
    'blue': ('new-api:3000', 'http://127.0.0.1:13000'),
    'green': ('new-api-green:3000', 'http://127.0.0.1:13002'),
    'green2': ('new-api-green2:3000', 'http://127.0.0.1:13003'),
}


def primary_slot(config):
    slot = config.get('active_slot', 'blue')
    if slot not in PRIMARY_SLOTS or (slot != 'blue' and config.get('deployment_mode') != 'single_primary'):
        raise ValueError('invalid gateway slot')
    return PRIMARY_SLOTS[slot]


def load(path):
    try:
        value=json.loads(pathlib.Path(path).read_text())
    except FileNotFoundError:
        return dict(DEFAULT)
    if not isinstance(value,dict) or any(type(value.get(k)) is not int for k in ('revision','primary','relay')):
        raise ValueError('invalid routing intent')
    if value['primary']+value['relay']!=100 or min(value['primary'],value['relay'])<0:
        raise ValueError('invalid weights')
    if type(value.get('relay_enabled')) is not bool or value.get('draining') not in (None,'primary','relay'):
        raise ValueError('invalid routing flags')
    if value.get('deployment_mode')=='single_primary' and (value['primary']!=100 or value['relay']!=0 or value['relay_enabled']):
        raise ValueError('single-primary mode requires primary 100%, relay disabled')
    primary_slot(value)
    return value


def save(path, config):
    path=pathlib.Path(path);path.parent.mkdir(parents=True,exist_ok=True)
    temporary=path.with_suffix('.tmp')
    temporary.write_text(json.dumps(config));temporary.chmod(0o600);os.replace(temporary,path)


def weights(config, relay_ready=True):
    if config.get('deployment_mode')=='single_primary':
        return {'primary':0 if 'primary' in config.get('blocked',[]) or config.get('draining')=='primary' else 100,'relay':0}
    primary,relay=config['primary'],config['relay']
    blocked=config.get('blocked',[])
    if 'primary' in blocked:
        return {'primary':0,'relay':100 if relay_ready and config['relay_enabled'] and 'relay' not in blocked else 0}
    if 'relay' in blocked:return {'primary':100,'relay':0}
    if not relay_ready or config['draining']=='relay':
        return {'primary':100,'relay':0}
    if config['draining']=='primary':
        return {'primary':0,'relay':100}
    if not config['relay_enabled']:
        return {'primary':100,'relay':0}
    return {'primary':primary,'relay':relay}


def render(base,config,relay_ready):
    current=weights(config,relay_ready)
    address, _ = primary_slot(config)
    if 'active_slot' in config and not config.get('text_only', False):
        def render_primary(match):
            return re.sub(r'(?<=server )new-api(?:-green)?:3000\b', address, match[0], count=1)
        base, count = re.subn(r'upstream omni_primary\s*\{[^}]*\}', render_primary, base, count=1)
        if count != 1:
            raise ValueError('primary upstream template not recognized')
    lines=[]
    if current['primary']:
        lines.append('    server %s weight=%d max_fails=1 fail_timeout=10s;' % (address, current['primary']))
    if current['relay']:
        lines.append('    server 172.30.10.1:13001 weight=%d max_fails=1 fail_timeout=10s;' % current['relay'])
    if not lines:lines.append('    server 127.0.0.1:9 down;')
    replacement='upstream omni_text {\n'+'\n'.join(lines)+'\n    keepalive 16;\n}'
    result,count=re.subn(r'upstream omni_text\s*\{[^}]*\}',lambda _:replacement,base,count=1)
    if count!=1:raise ValueError('routing template not recognized')
    return result,current
