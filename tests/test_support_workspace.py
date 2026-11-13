"""HTTP regressions for the production workspace; only throwaway support data."""
import datetime as dt
import json
from pathlib import Path

from tests.test_support_service import ServiceCase


class WorkspaceCase(ServiceCase):
    def admin(self, path, payload=None, **kwargs):
        cookie,csrf=self.operator()
        return self.call('/brand/admin/api/support/'+path,payload,cookie=cookie,csrf=csrf,**kwargs)

    def create(self, category='recharge', text='付款记录已提交'):
        code,payload=self.call('/brand/api/support/v2/tickets',{'subject':'订单核验','description':text,'category':category},token='token-kiwi')
        self.assertEqual(code,201,payload)
        return payload['ticket']['id']

    def test_workspace_requires_auth_and_scopes_customers(self):
        self.assertEqual(self.call('/brand/admin/api/support/v3/workspace')[0],401)
        self.assertEqual(self.call('/brand/api/support/v3/workspace')[0],401)
        mine=self.create()
        other=self.service.store.start(999,'another-user',reuse=False)
        self.service.store.add_message(other['id'],'user','PRIVATE OTHER TEXT')
        self.service.store.add_note(mine,'PRIVATE INTERNAL NOTE')
        code,payload=self.call('/brand/api/support/v3/workspace',token='token-kiwi')
        self.assertEqual(code,200)
        self.assertEqual([t['id'] for t in payload['data']['tickets']],[mine])
        self.assertNotIn('PRIVATE',json.dumps(payload))
        self.assertNotIn('uid',payload['data']['tickets'][0])
        self.assertEqual(payload['data']['replies'],[])
        self.assertNotIn('analytics',payload['data'])
        code,admin=self.admin('v3/workspace')
        self.assertEqual(code,200)
        self.assertEqual(len(admin['data']['tickets']),2)
        self.assertEqual(admin['data']['analytics']['newToday'],2)

    def test_catalog_csrf_persistence_and_legacy_ids(self):
        cookie,_=self.operator()
        body={'kind':'category','values':{'name':'Office 文件','color':'#123abc','sla':20,'priority':'normal'}}
        self.assertEqual(self.call('/brand/admin/api/support/v3/catalog',body,cookie=cookie)[0],403)
        code,payload=self.admin('v3/catalog',body)
        self.assertEqual(code,200,payload)
        custom=payload['catalog']['categories'][-1]
        self.assertTrue(custom['id'].startswith('cat_'))
        self.assertEqual(self.admin('v3/catalog',body)[0],400)
        own=self.create(custom['id'])
        self.assertEqual(self.service.store.search(category=custom['id'])[0]['id'],own)
        self.assertEqual(self.admin('v3/catalog',{'kind':'category_toggle','values':{'id':custom['id'],'enabled':False}})[0],200)
        self.assertEqual(self.call('/brand/api/support/v2/tickets',{'subject':'new','description':'new','category':custom['id']},token='token-kiwi')[0],400)
        self.assertEqual(self.service.store.get(own)['category'],custom['id'])
        # The append-only event survives a fresh service and SQLite replay.
        import support_service
        recovered=support_service.Service(self.secrets_path,self.events,self.state_path,self.config_path,gateway_url='http://gateway.test')
        saved=recovered.workspace.catalog()['categories'][-1]
        self.assertEqual(saved['id'],custom['id']);self.assertFalse(saved['enabled'])
        self.assertIn('recharge',recovered.store.category_ids)

    def test_user_category_does_not_allow_another_ticket(self):
        mine=self.create()
        other=self.service.store.start(999,'other',reuse=False)
        self.assertEqual(self.call('/brand/api/support/v3/category',{'ticket_id':other['id'],'category':'billing'},token='token-kiwi')[0],400)
        self.assertEqual(self.service.store.get(other['id'])['category'],'other')
        self.assertEqual(self.call('/brand/api/support/v3/category',{'ticket_id':mine,'category':'billing'},token='token-kiwi')[0],200)

    def test_real_statistics_and_nulls_no_demo_numbers(self):
        _,empty=self.admin('v3/workspace?days=30')
        a=empty['data']['analytics']
        self.assertEqual(a['newToday'],0);self.assertIsNone(a['resolutionRate']);self.assertIsNone(a['firstResponseMinutes'])
        self.assertEqual(len(a['series']),30)
        mine=self.create()
        self.service.store.add_message(mine,'operator','收到，已核对')
        self.service.store.set_status(mine,'resolved',by='operator')
        _,result=self.admin('v3/workspace')
        a=result['data']['analytics']
        self.assertEqual(a['newToday'],1);self.assertEqual(a['resolutionRate'],100)
        self.assertEqual(sum(r['new'] for r in a['series']),1)
        self.assertEqual(sum(r['solved'] for r in a['series']),1)
        self.assertGreaterEqual(a['firstResponseMinutes'],0)

    def test_replies_team_and_category_not_executable(self):
        body={'kind':'reply','values':{'title':'核对','text':'请提供 Request ID'}}
        self.assertEqual(self.admin('v3/catalog',body)[0],200)
        self.assertEqual(self.admin('v3/catalog',{'kind':'team','values':{'name':'客服甲'}})[0],200)
        _,snapshot=self.admin('v3/workspace')
        self.assertIn('客服甲',[p['name'] for p in snapshot['data']['team']])
        self.assertIn('核对',[r['title'] for r in snapshot['data']['replies']])
        self.assertEqual(self.admin('v3/catalog',{'kind':'category','values':{'name':'bad','color':'red;script','sla':20,'priority':'normal'}})[0],400)

    def test_last_category_cannot_be_disabled(self):
        categories=self.service.workspace.catalog()['categories']
        for category in categories[:-1]:
            self.assertEqual(self.admin('v3/catalog',{'kind':'category_toggle','values':{'id':category['id'],'enabled':False}})[0],200)
        self.assertEqual(self.admin('v3/catalog',{'kind':'category_toggle','values':{'id':categories[-1]['id'],'enabled':False}})[0],400)

    def test_cold_ticket_is_hydrated_for_history_and_reopen(self):
        mine=self.create()
        self.service.store.set_status(mine,'resolved',by='operator')
        self.service.store.conversations.pop(mine)
        self.service.store.messages.pop(mine)
        self.assertEqual(self.service.store.get(mine)['id'],mine)
        self.assertEqual(self.call('/brand/api/support/v2/status',{'ticket_id':mine,'action':'reopen'},token='token-kiwi')[0],200)

    def test_secondary_domain_origin_is_explicitly_scoped(self):
        self.service.allowed_origins={'https://omniapi.biz','https://omniapi.hyeroomy.com'}
        body={'kind':'team','values':{'name':'客服乙'}}
        self.assertEqual(self.admin('v3/catalog',body,origin='https://evil.invalid')[0],403)
        self.assertEqual(self.admin('v3/catalog',body,origin='https://omniapi.hyeroomy.com')[0],200)
