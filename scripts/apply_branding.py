"""Apply native OmniAPI branding and verify public assets."""
import json
from gateway import ROOT, API, Stack, credentials, write_json

def main():
    stack=Stack()
    admin=API(stack.url)
    admin.login(credentials(ROOT,'admin'))
    admin.option('SystemName','OmniAPI')
    admin.option('Logo','/brand/omniapi.svg')
    admin.option('ServerAddress','http://localhost')
    public=API(stack.url)
    data=public.ok('/api/status')
    result={key:data.get(key) for key in ('system_name','logo','server_address')}
    result['assets']={}
    for path in ('/brand/omniapi.svg','/logo.png','/favicon.ico'):
        with public.opener.open(stack.url+path,timeout=15) as response:
            result['assets'][path]={'status':response.status,'type':response.headers.get('Content-Type'),
                                   'valid_svg':b'<svg' in response.read()}
    assert result['system_name']=='OmniAPI'
    assert all(value['valid_svg'] and value['type']=='image/svg+xml' for value in result['assets'].values())
    result['status']='PASS'
    write_json(ROOT/'docs/BRANDING_CHECK.json',result)
    print(json.dumps(result))

if __name__=='__main__': main()
