"""Retention maintenance only with authoritative, freshly queried gateway state."""
import json
import os
import urllib.request
from store import Store

def main():
    secret=os.environ['OMNI_SYNC_INTROSPECTION_SECRET']
    def end(uid):
        request=urllib.request.Request('https://omniapi.biz/api/app/sync/introspect',data=json.dumps({'user_id':uid}).encode(),headers={'Content-Type':'application/json','X-Omni-Sync-Service':secret})
        with urllib.request.urlopen(request,timeout=8) as response:
            body=json.load(response)
            if not body.get('success') or type(body.get('data',{}).get('end')) is not int:
                raise RuntimeError('Gateway entitlement unavailable; retention stopped')
            return body['data']['end']
    root=os.environ.get('OMNI_SYNC_ROOT','/var/lib/omni-sync')
    count=Store(root+'/sync.sqlite3').cleanup(end)
    print(json.dumps({'purged_accounts':count}))

if __name__=='__main__':main()
