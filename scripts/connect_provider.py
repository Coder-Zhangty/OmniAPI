"""Validate advertised text models, then publish only successful models natively."""
import concurrent.futures
import json
import time
import urllib.request
import urllib.error
from gateway import ROOT, API, Stack, credentials, write_json

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None

def main():
    secret = credentials(ROOT, 'provider-default')
    catalog = json.loads((ROOT/'runtime/upstream-model-catalog.json').read_text())['models']
    def probe(row):
        name = row['id']
        result = {'model': name}
        if any(x in name for x in ('image', 'tts', 'embedding')):
            return dict(result, status='NOT TESTED', reason='Requires separate media/embedding validation and pricing')
        request = urllib.request.Request(secret['base_url']+'/v1/chat/completions',
            data=json.dumps({'model': name, 'messages':[{'role':'user','content':'Reply with OK only.'}], 'max_tokens':32}).encode(),
            headers={'Authorization':'Bearer '+secret['api_key'], 'Content-Type':'application/json'})
        start=time.monotonic()
        try:
            opener=urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
            with opener.open(request, timeout=55) as response:
                data=json.load(response)
                content=(data.get('choices') or [{}])[0].get('message',{}).get('content')
                result.update(status='PASS' if content else 'FAIL', http_status=response.status, usage=data.get('usage'), seconds=round(time.monotonic()-start,3))
        except urllib.error.HTTPError as exc:
            result.update(status='FAIL', http_status=exc.code)
        except Exception as exc:
            result.update(status='FAIL', error_type=type(exc).__name__)
        print(name+': '+result['status'], flush=True)
        return result
    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
        results=list(pool.map(probe,catalog))
    write_json(ROOT/'docs/REAL_MODELS.json', {'advertised_count':len(catalog), 'results':results})
    models=[r['model'] for r in results if r['status']=='PASS']
    if not models:
        raise RuntimeError('No text model passed; nothing enabled')
    admin=API(Stack().url)
    admin.login(credentials(ROOT,'admin'))
    options=admin.options()
    for key,ratio in [('ModelRatio',0.5),('CompletionRatio',2)]:
        values=json.loads(options.get(key,'{}'))
        values.update({m:ratio for m in models})
        admin.option(key,json.dumps(values))
    prices=json.loads(options.get('ModelPrice','{}'))
    for m in models: prices.pop(m,None)
    admin.option('ModelPrice',json.dumps(prices))
    groups=json.loads(options.get('GroupRatio','{}')); groups['default']=1
    admin.option('GroupRatio',json.dumps(groups))
    admin.option('RetryTimes',0)
    channels=admin.ok('/api/channel/').get('items',[])
    existing=next((x for x in channels if x['name']=='0-0 Main'),None)
    channel={'type':1,'name':'0-0 Main','key':secret['api_key'],'base_url':secret['base_url'],
             'group':'default','models':','.join(models),'model_mapping':'{}','priority':30,'weight':10,'status':1,'auto_ban':0}
    if existing:
        channel['id']=existing['id']
        admin.ok('/api/channel/',channel,'PUT')
    else:
        admin.ok('/api/channel/',{'mode':'single','channel':channel})
    admin.ok('/api/channel/fix',{})
    from provider_visibility import main as publish_metadata
    publish_metadata()
    print('Published verified text models: '+str(len(models)),flush=True)

if __name__=='__main__':
    main()
