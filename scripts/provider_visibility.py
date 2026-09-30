"""Keep supplier details administrative; publish neutral native model metadata."""
import json
from gateway import ROOT, API, Stack, credentials, write_json

def main():
    api=API(Stack().url)
    api.login(credentials(ROOT,'admin'))
    report=json.loads((ROOT/'docs/REAL_MODELS.json').read_text(encoding='utf-8'))
    enabled=[r['model'] for r in report['results'] if r['status']=='PASS']
    # v0.13.2 locks completion multipliers for several model prefixes.
    # Native expression billing preserves public model names and the agreed price.
    options=api.options()
    expressions=json.loads(options.get('billing_setting.billing_expr','{}'))
    modes=json.loads(options.get('billing_setting.billing_mode','{}'))
    expressions.update({m:'v1:p * 1 + c * 2' for m in enabled})
    modes.update({m:'tiered_expr' for m in enabled})
    api.option('billing_setting.billing_expr',json.dumps(expressions))
    api.option('billing_setting.billing_mode',json.dumps(modes))
    existing={m['model_name']:m for m in api.ok('/api/models/?p=1&page_size=100')['items']}
    for model in enabled:
        metadata={'model_name':model, 'description':'文本模型；内部测试价格：输入 $1 / 百万 Token，输出 $2 / 百万 Token。',
                  'vendor_id':0,'tags':'文本,测试价格','status':1,'sync_official':0,'name_rule':0,
                  'endpoints':json.dumps({'openai':'/v1/chat/completions', **({'openai-response':'/v1/responses'} if model=='gpt-5.4-mini' else {})})}
        if model in existing:
            metadata['id']=existing[model]['id']
            api.ok('/api/models/',metadata,'PUT')
        else:
            api.ok('/api/models/',metadata)
    user=API(Stack().url); user.login(credentials(ROOT,'tester'))
    public=API(Stack().url)
    token=credentials(ROOT,'test-token')['key']
    secret=credentials(ROOT,'provider-default')['api_key']
    evidence={}
    for label,client,path,key in [('api_models',public,'/v1/models',token),('public_pricing',public,'/api/pricing',None),
                                  ('user_pricing',user,'/api/pricing',None),('user_logs',user,'/api/log/self/?p=1&page_size=100',None)]:
        status,data,_=client.request(path,token=key)
        serialized=json.dumps(data,ensure_ascii=False).lower()
        clean=not any(term.lower() in serialized for term in ('0-0','api.0-0.pro',secret))
        evidence[label]={'status':'PASS' if status==200 and data.get('success') is not False and clean else 'FAIL',
                         'http_status':status,'supplier_details_absent':clean}
        if label=='api_models':
            names=[r['id'] for r in data.get('data',[])]
            evidence[label]['model_count']=len(names)
            evidence[label]['matches_verified_models']=set(names)==set(enabled)
    write_json(ROOT/'docs/PROVIDER_VISIBILITY.json',evidence)
    print(json.dumps(evidence,ensure_ascii=False))

if __name__=='__main__': main()
