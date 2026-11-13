"""Synthetic-only durable capture, isolation, retry and recovery acceptance."""
import base64
import hashlib
import io
import json
import tempfile
import time
import unittest
import zipfile
from pathlib import Path
from unittest.mock import patch
from urllib.error import HTTPError
from urllib.parse import parse_qs, urlsplit
from ops.archive_v3.drive import Drive, DriveError
from ops.archive_v3.store import Store, encode, digest
from ops.archive_v3.service import Application
from ops.archive_v3.synthetic import seed


class ArchiveV3Tests(unittest.TestCase):
    def setUp(self):
        parent = Path(__file__).resolve().parents[1] / 'runtime' / 'archive-v3-tests'
        parent.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=parent)
        self.root = Path(self.temp.name)
        self.store = Store(self.root, min_free=0)

    def tearDown(self):
        self.temp.cleanup()

    def test_durable_reopen_user_scoped_segments_and_plaintext(self):
        seed(self.store)
        self.assertEqual(len(Store(self.root).directory()['users']),4)
        self.store.seal(force=True)
        with self.store.db() as db:
            objects = [dict(r) for r in db.execute('SELECT * FROM objects')]
        self.assertTrue(objects)
        for obj in objects:
            self.assertTrue(obj['path'].startswith('user-'+str(obj['user_id'])+'/'))
            raw=(self.root/'files'/obj['path']).read_bytes()
            self.assertEqual(digest(raw),obj['hash'])
            if obj['kind']=='segment':
                for line in raw.splitlines():
                    event=json.loads(line)
                    self.assertEqual(event['meta']['user_id'],obj['user_id'])
                    self.assertIn('text',event)
                    self.assertEqual(digest(event['transport_json'].encode()),event['event_hash'])
        self.assertTrue(any('导航' in (self.root/'files'/o['path']).read_text() for o in objects if o['kind']=='segment'))

    def test_retries_are_idempotent_and_reject_conflicting_bytes(self):
        event={'version':3,'meta':{'archive_id':'a'*32,'seq':0,'user_id':42,'kind':'request','created':time.time(),'prev_hash':''},'data':base64.b64encode(b'{}').decode()}
        raw=encode(event)
        self.assertEqual(self.store.ingest(raw),self.store.ingest(raw))
        event['data']=base64.b64encode(b'{"x":1}').decode()
        with self.assertRaises(ValueError):self.store.ingest(encode(event))
        event['meta']['seq']=2
        with self.assertRaises(ValueError):self.store.ingest(encode(event))

    def test_manifest_waits_for_segment_and_attachment_verification(self):
        seed(self.store);self.store.seal(force=True)
        claimed=[]
        while item:=self.store.claim():
            self.assertNotEqual(item['kind'],'manifest')
            claimed.append(item)
        self.assertTrue(claimed)
        for item in claimed:self.store.mark(item['id'],state='verified',lease=0)
        item=self.store.claim()
        self.assertEqual(item['kind'],'manifest')

    def test_cannot_group_across_users_or_share_attachments(self):
        seed(self.store)
        a=self.store.records({'user':'42'})['records'][0]
        b=self.store.records({'user':'57'})['records'][0]
        with self.assertRaises(ValueError):self.store.group([a['id'],b['id']],'p','c')
        self.store.add_attachment(a['id'],'test','text/plain',b'same')
        self.store.add_attachment(b['id'],'test','text/plain',b'same')
        with self.store.db() as db:
            paths=[r[0] for r in db.execute("SELECT path FROM objects WHERE hash=?",(digest(b'same'),))]
        self.assertEqual(len(paths),2)
        self.assertNotEqual(paths[0].split('/')[0],paths[1].split('/')[0])

    def test_chinese_search_filters_and_pagination(self):
        seed(self.store)
        self.assertTrue(self.store.records({'q':'验证码'})['records'])
        self.assertTrue(self.store.records({'q':'短关键词'})['records'])
        self.assertTrue(self.store.records({'q':'张先生'})['records'])
        page=self.store.records({'limit':'2'})
        self.assertEqual(len(page['records']),2)
        self.assertTrue(page['next_cursor'])
        second=self.store.records({'limit':'2','cursor':page['next_cursor']})
        self.assertFalse({r['id'] for r in page['records']}&{r['id'] for r in second['records']})
        self.assertEqual(self.store.records({'user':'68','abnormal':'1'})['records'][0]['status'],'failed')

    def test_export_is_real_and_contains_no_executable_html(self):
        seed(self.store)
        app=Application(self.root,{'min_free_bytes':0,'public_origin':'http://127.0.0.1'})
        job=app.create_export({'id':1,'role':100},{'format':'html','filters':{'user':'42'}})
        app.export_threads[job['id']].join(10)
        final=app.store.setting('export:'+job['id'])
        self.assertEqual(final['state'],'ready')
        with zipfile.ZipFile(self.root/'exports'/(job['id']+'.zip')) as archive:
            names=archive.namelist()
            self.assertTrue(any(n.endswith('conversation.html') for n in names))
            self.assertTrue(any('/attachments/' in n for n in names))
            self.assertFalse(any('user-57' in n for n in names))
            self.assertFalse(any('<script' in archive.read(n).decode() for n in names if n.endswith('.html')))

    def test_rebuild_without_original_sqlite_and_reject_tampering(self):
        from ops.archive_v3.cli import restore
        seed(self.store);self.store.seal(force=True);self.store.checkpoint()
        source=self.root/'files'
        result=restore(source,self.root/'rebuilt')
        self.assertEqual(result['requests'],10)
        rebuilt=Store(self.root/'rebuilt',min_free=0)
        self.assertEqual(rebuilt.directory()['users'][0]['name'],'张先生')
        self.assertTrue(rebuilt.records({'q':'验证码'})['records'])
        segment=next(source.glob('user-*/projects/*/conversations/*/records/*/*/*/*.jsonl'))
        segment.write_bytes(segment.read_bytes()+b'X')
        with self.assertRaises(ValueError):restore(source,self.root/'tampered')
        self.assertFalse((self.root/'tampered').exists())

    def test_final_event_survives_attachment_index_failure(self):
        rid = 'c' * 32
        previous = ''
        def put(seq, kind, value):
            nonlocal previous
            raw = encode({'version':3,'meta':{'archive_id':rid,'seq':seq,'user_id':42,'kind':kind,'prev_hash':previous,'status':'completed'},'data':base64.b64encode(encode(value)).decode()})
            previous = self.store.ingest(raw)
        put(0, 'request', {})
        put(1, 'input', {'messages':None})
        put(2, 'output', {'data':[{'b64_json':base64.b64encode(b'file').decode()}]})
        with patch.object(self.store,'add_attachment',side_effect=OSError('temporary')):
            put(3,'final',{})
        self.assertEqual(self.store.seal(force=True), 0)
        reopened = Store(self.root, min_free=0)
        reopened.finish_index()
        self.assertEqual(reopened.seal(force=True), 1)
        self.assertEqual(reopened.detail(rid)['attachments'][0]['size'], 4)

    def test_google_oauth_state_and_resumable_upload_verification(self):
        store = self.store
        seed(store)
        item = store.claim()
        original = (store.root / 'files' / item['path']).read_bytes()
        resume_from = min(3, len(original)-1)
        config = {'google_client_id':'client','google_client_secret':'secret','google_redirect_uri':'https://archive.invalid/archive-v3/api/oauth/callback'}
        drive = Drive(store, config)
        url = urlsplit(drive.start('admin-session'))
        query = parse_qs(url.query)
        state = query['state'][0]
        self.assertEqual(query['scope'],['https://www.googleapis.com/auth/drive.file'])
        with self.assertRaises(PermissionError):
            drive.finish(state,'demo','different-admin-session')
        session_url = 'https://www.googleapis.com/upload/drive/v3/files?upload_id=mock'
        parent = 'mock-folder'
        self.store.setting('drive_root',parent)
        remote = False
        correct_hash = False
        observed = []
        class Response(io.BytesIO):
            def __init__(self,value=None,headers=None):
                super().__init__(encode(value) if value is not None else b'')
                self.headers = headers or {}
        def mock_http(req, timeout=0):
            nonlocal remote
            request = urlsplit(req.full_url)
            if request.path == '/token':
                values = parse_qs(req.data.decode())
                verifier = values['code_verifier'][0]
                expected = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip('=')
                self.assertEqual(query['code_challenge'],[expected])
                return Response({'refresh_token':'test-refresh','access_token':'test-access','expires_in':3600})
            self.assertEqual(req.get_header('Authorization'),'Bearer test-access')
            if request.path.endswith('/about'):
                return Response({'storageQuota':{'limit':'10000','usage':'200'}})
            if request.path.endswith('/'+parent):
                return Response({'id':parent,'mimeType':'application/vnd.google-apps.folder'})
            if request.path.endswith('/generateIds'):
                return Response({'ids':['test-file']})
            if request.path.endswith('/test-file'):
                if remote:
                    return Response({'id':'test-file','parents':[parent],'size':str(len(original)),'sha256Checksum':item['hash'] if correct_hash else '0'*64})
                raise HTTPError(req.full_url,404,'missing',{},io.BytesIO(b''))
            if request.path.startswith('/upload/'):
                if req.get_method()=='POST':
                    self.assertEqual(json.loads(req.data)['id'],'test-file')
                    return Response(headers={'Location':session_url})
                span=req.get_header('Content-range')
                observed.append(span)
                if span.startswith('bytes */'):
                    raise HTTPError(req.full_url,308,'partial',{'Range':f'bytes=0-{resume_from-1}'},None)
                self.assertEqual(req.data,original[resume_from:])
                remote = True
                return Response({'id':'test-file'})
            raise AssertionError(req.full_url)
        with patch('urllib.request.build_opener') as opener, patch.object(drive,'parent_for',return_value=parent):
            opener.return_value.open.side_effect=mock_http
            drive.finish(state,'demo','admin-session')
            self.assertTrue(drive.status()['connected'])
            with self.assertRaises(DriveError) as failure:
                drive.upload(item)
            self.assertEqual(failure.exception.reason,'remote_integrity_error')
            with store.db() as db:
                retry_item = dict(db.execute('SELECT * FROM objects WHERE id=?',(item['id'],)).fetchone())
            self.assertNotEqual(retry_item['state'],'verified')
            self.assertEqual(retry_item['remote_id'],'test-file')
            self.assertEqual(observed,[f"bytes */{len(original)}",f"bytes {resume_from}-{len(original)-1}/{len(original)}"])
            correct_hash = True
            drive.upload(retry_item)
            with store.db() as db:
                self.assertEqual(db.execute('SELECT state FROM objects WHERE id=?',(item['id'],)).fetchone()[0],'verified')
            self.assertIsNotNone(drive.status()['last_sync'])


if __name__=='__main__':unittest.main()
