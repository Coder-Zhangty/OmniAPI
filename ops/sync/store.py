"""Opaque, user-isolated incremental sync. No archive database access."""
import base64
import hashlib
import json
import re
import sqlite3
import shutil
import time
from contextlib import contextmanager
from pathlib import Path

IDENTIFIER = re.compile(r'^[A-Za-z0-9_-]{16,80}$')
GIB = 1024 ** 3
MAX_OBJECT = 2 * 1024 * 1024

class SyncError(Exception):
    def __init__(self, code, status=400):
        self.code, self.status = code, status
        super().__init__(code)

def identifier(value):
    if not isinstance(value, str) or not IDENTIFIER.fullmatch(value):
        raise SyncError('INVALID_IDENTIFIER')
    return value

def envelope(value):
    # Ciphertext is deliberately uninterpreted; reject extra plaintext fields.
    if not isinstance(value, dict) or set(value) != {'v', 'nonce', 'ciphertext'} or value['v'] != 1:
        raise SyncError('INVALID_ENVELOPE')
    try:
        nonce = base64.b64decode(value['nonce'], validate=True)
        ciphertext = base64.b64decode(value['ciphertext'], validate=True)
    except (ValueError, TypeError):
        raise SyncError('INVALID_ENVELOPE') from None
    if len(nonce) != 12 or not 16 <= len(ciphertext) <= MAX_OBJECT:
        raise SyncError('INVALID_ENVELOPE')
    return json.dumps(value, separators=(',', ':'), sort_keys=True), len(ciphertext) + len(nonce)

class Store:
    def __init__(self, path):
        self.path = str(path)
        Path(path).parent.mkdir(parents=True, exist_ok=True)
        with self.connection() as db:
            db.executescript('''
            PRAGMA journal_mode=WAL;
            CREATE TABLE IF NOT EXISTS accounts(uid INTEGER PRIMARY KEY, end INTEGER NOT NULL, quota INTEGER NOT NULL);
            CREATE TABLE IF NOT EXISTS devices(uid INTEGER NOT NULL, id TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL, PRIMARY KEY(uid,id));
            CREATE TABLE IF NOT EXISTS revisions(cursor INTEGER PRIMARY KEY AUTOINCREMENT, uid INTEGER NOT NULL, object TEXT NOT NULL, revision TEXT NOT NULL, parent TEXT, device TEXT NOT NULL, payload TEXT NOT NULL, bytes INTEGER NOT NULL, deleted INTEGER NOT NULL, created INTEGER NOT NULL, UNIQUE(uid,revision));
            CREATE INDEX IF NOT EXISTS sync_changes ON revisions(uid,cursor);
            CREATE INDEX IF NOT EXISTS sync_objects ON revisions(uid,object,cursor);
            CREATE TABLE IF NOT EXISTS keyrings(uid INTEGER PRIMARY KEY, version INTEGER NOT NULL, payload TEXT NOT NULL);
            ''')

    @contextmanager
    def connection(self):
        db = sqlite3.connect(self.path, timeout=15, isolation_level=None)
        db.row_factory = sqlite3.Row
        db.execute('PRAGMA foreign_keys=ON')
        db.execute('PRAGMA synchronous=FULL')
        try:
            yield db
        finally:
            db.close()

    @contextmanager
    def transaction(self):
        with self.connection() as db:
            db.execute('BEGIN IMMEDIATE')
            try:
                yield db
                db.execute('COMMIT')
            except BaseException:
                db.execute('ROLLBACK')
                raise

    def authorize(self, claims, write=False):
        uid = claims.get('uid')
        if type(uid) is not int or uid <= 0:
            raise SyncError('UNAUTHORIZED', 401)
        device = identifier(claims.get('device'))
        end, quota = claims.get('end', 0), claims.get('quota', 0)
        if type(end) is not int or type(quota) is not int or not 0 <= quota <= GIB:
            raise SyncError('INVALID_ENTITLEMENT', 403)
        now = int(time.time())
        if end <= 0 or now >= end + 90 * 86400:
            raise SyncError('SYNC_NOT_ENTITLED', 403)
        if write and end <= now:
            raise SyncError('SYNC_EXPIRED_READ_ONLY', 403)
        with self.transaction() as db:
            db.execute('INSERT INTO accounts VALUES(?,?,?) ON CONFLICT(uid) DO UPDATE SET end=excluded.end,quota=excluded.quota', (uid,end,quota))
            row = db.execute('SELECT revoked FROM devices WHERE uid=? AND id=?', (uid,device)).fetchone()
            if row is None:
                raise SyncError('DEVICE_NOT_REGISTERED', 403)
            if row['revoked']:
                raise SyncError('DEVICE_REVOKED', 403)
        return uid, device

    def register(self, claims):
        uid, device = claims['uid'], identifier(claims['device'])
        end, quota = claims.get('end', 0), claims.get('quota', 0)
        if (type(uid) is not int or uid <= 0 or type(end) is not int
                or type(quota) is not int or not 0 <= quota <= GIB
                or end <= 0 or time.time() >= end + 90 * 86400):
            raise SyncError('SYNC_NOT_ENTITLED',403)
        with self.transaction() as db:
            existing = db.execute('SELECT revoked FROM devices WHERE uid=? AND id=?',(uid,device)).fetchone()
            if existing and existing['revoked']:
                raise SyncError('DEVICE_REVOKED',403)
            # Existing devices can download retained data after expiry. Adding a
            # new device requires an active subscription.
            if existing is None and end <= time.time():
                raise SyncError('SYNC_EXPIRED_READ_ONLY',403)
            count = db.execute('SELECT COUNT(*) FROM devices WHERE uid=? AND revoked=0',(uid,)).fetchone()[0]
            if existing is None and count >= 20:
                raise SyncError('DEVICE_LIMIT',409)
            db.execute('INSERT OR IGNORE INTO devices VALUES(?,?,0,?)',(uid,device,int(time.time())))
        return {'id': device}

    def devices(self, claims):
        uid,_ = self.authorize(claims)
        with self.connection() as db:
            return [dict(row) for row in db.execute('SELECT id,revoked,created FROM devices WHERE uid=?',(uid,))]

    def revoke(self, claims, device):
        uid,_ = self.authorize(claims)
        with self.transaction() as db:
            db.execute('UPDATE devices SET revoked=1 WHERE uid=? AND id=?',(uid,identifier(device)))
        return {'revoked': True}

    def put(self, claims, object_id, revision, body):
        uid, device = self.authorize(claims, write=True)
        identifier(object_id); identifier(revision)
        if set(body) != {'parent','deleted','envelope'} or type(body['deleted']) is not bool:
            raise SyncError('INVALID_REVISION')
        parent = body['parent']
        if parent is not None:
            identifier(parent)
        payload,size = envelope(body['envelope'])
        deleted = int(body['deleted'])
        with self.transaction() as db:
            old = db.execute('SELECT * FROM revisions WHERE uid=? AND revision=?',(uid,revision)).fetchone()
            if old:
                if (old['object'],old['parent'],old['deleted'],old['payload']) != (object_id,parent,deleted,payload):
                    raise SyncError('IDEMPOTENCY_CONFLICT',409)
                return {'cursor':old['cursor'],'revision':revision,'duplicate':True}
            if shutil.disk_usage(Path(self.path).parent).free < 8 * GIB + size:
                raise SyncError('SERVER_STORAGE_RESERVE',507)
            if parent is not None and not db.execute('SELECT 1 FROM revisions WHERE uid=? AND object=? AND revision=?',(uid,object_id,parent)).fetchone():
                raise SyncError('UNKNOWN_PARENT',409)
            # Count all retained versions, attachments and non-secret keyring bytes.
            used = db.execute('SELECT COALESCE(SUM(bytes),0) FROM revisions WHERE uid=?',(uid,)).fetchone()[0]
            keyring = db.execute('SELECT payload FROM keyrings WHERE uid=?',(uid,)).fetchone()
            used += len(keyring['payload'].encode()) if keyring else 0
            if used + size > claims['quota']:
                raise SyncError('SYNC_QUOTA_EXCEEDED',413)
            latest = db.execute('SELECT revision FROM revisions WHERE uid=? AND object=? ORDER BY cursor DESC LIMIT 1',(uid,object_id)).fetchone()
            conflict = latest is not None and latest['revision'] != parent
            cursor = db.execute('INSERT INTO revisions(uid,object,revision,parent,device,payload,bytes,deleted,created) VALUES(?,?,?,?,?,?,?,?,?)',(uid,object_id,revision,parent,device,payload,size,deleted,int(time.time()))).lastrowid
            return {'cursor':cursor,'revision':revision,'conflict':conflict}

    def changes(self, claims, after=0, limit=100):
        uid,_ = self.authorize(claims)
        if type(after) is not int or after < 0:
            raise SyncError('INVALID_CURSOR')
        with self.connection() as db:
            rows = list(db.execute('SELECT cursor,object,revision,parent,device,payload,deleted,created FROM revisions WHERE uid=? AND cursor>? ORDER BY cursor LIMIT ?',(uid,after,max(1,min(limit,100)))))
        result = []
        for row in rows:
            value = dict(row)
            value['envelope'] = json.loads(value.pop('payload'))
            if value['envelope'].get('purged'):
                value['purged'] = True
                value['envelope'] = None
            value['deleted'] = bool(value['deleted'])
            result.append(value)
        return {'changes':result,'cursor':rows[-1]['cursor'] if rows else after,'has_more':len(rows)==min(limit,100)}

    def status(self, claims):
        uid,_ = self.authorize(claims)
        with self.connection() as db:
            used = db.execute('SELECT COALESCE(SUM(bytes),0) FROM revisions WHERE uid=?',(uid,)).fetchone()[0]
            ring = db.execute('SELECT payload FROM keyrings WHERE uid=?',(uid,)).fetchone()
            used += len(ring['payload'].encode()) if ring else 0
        return {'used_bytes':used,'quota_bytes':claims['quota'],'end':claims['end'],'read_only':claims['end']<=time.time()}

    def keyring(self, claims, body=None):
        uid,_ = self.authorize(claims, write=body is not None)
        if body is None:
            with self.connection() as db:
                row = db.execute('SELECT version,payload FROM keyrings WHERE uid=?',(uid,)).fetchone()
                return {'version':row['version'],'keyring':json.loads(row['payload'])} if row else {'version':0,'keyring':None}
        if set(body) != {'expected_version','keyring'} or type(body['expected_version']) is not int:
            raise SyncError('INVALID_KEYRING')
        # Whitelist the portable v1 keyring; private keys/passwords are never fields.
        ring = body['keyring']
        if not isinstance(ring,dict) or set(ring) != {'v','uid','salt','password_wrap','recovery_wrap','kdf'} or ring['v']!=1 or str(ring['uid'])!=str(uid):
            raise SyncError('INVALID_KEYRING')
        if ring['kdf'] != {'name':'argon2id','m':65536,'t':3,'p':1}:
            raise SyncError('INVALID_KDF')
        try:
            if len(base64.b64decode(ring['salt'],validate=True))!=16:
                raise ValueError()
        except (ValueError,TypeError):
            raise SyncError('INVALID_KEYRING') from None
        for name in ['password_wrap','recovery_wrap']:
            _,length = envelope(ring[name])
            if length != 60:
                raise SyncError('INVALID_KEYRING')
        raw = json.dumps(ring,sort_keys=True,separators=(',',':'))
        if len(raw)>4096:
            raise SyncError('INVALID_KEYRING')
        with self.transaction() as db:
            row = db.execute('SELECT version,payload FROM keyrings WHERE uid=?',(uid,)).fetchone()
            version = row['version'] if row else 0
            if version != body['expected_version']:
                raise SyncError('KEYRING_CONFLICT',409)
            size = len(raw.encode())
            used = db.execute('SELECT COALESCE(SUM(bytes),0) FROM revisions WHERE uid=?',(uid,)).fetchone()[0]
            if used + size > claims['quota']:
                raise SyncError('SYNC_QUOTA_EXCEEDED',413)
            if shutil.disk_usage(Path(self.path).parent).free < 8 * GIB + size:
                raise SyncError('SERVER_STORAGE_RESERVE',507)
            db.execute('INSERT INTO keyrings VALUES(?,?,?) ON CONFLICT(uid) DO UPDATE SET version=excluded.version,payload=excluded.payload',(uid,version+1,raw))
        return {'version':version+1}

    def cleanup(self, authoritative_end):
        """Caller must fetch fresh gateway entitlement; network failure means no purge."""
        now = int(time.time())
        with self.connection() as db:
            users = [row[0] for row in db.execute('SELECT uid FROM accounts')]
        purged = 0
        for uid in users:
            end = authoritative_end(uid)  # exception aborts, preserving data
            if type(end) is not int or end <= 0:
                continue
            with self.transaction() as db:
                db.execute('UPDATE accounts SET end=? WHERE uid=?',(end,uid))
                if now >= end + 90*86400:
                    # Revoked tombstones survive: old device tokens cannot re-register.
                    db.execute('UPDATE devices SET revoked=1 WHERE uid=?',(uid,))
                    db.execute('DELETE FROM revisions WHERE uid=?',(uid,))
                    db.execute('DELETE FROM keyrings WHERE uid=?',(uid,))
                    purged += 1
                    continue
                objects = list(db.execute('SELECT DISTINCT object FROM revisions WHERE uid=?',(uid,)))
                for obj in objects:
                    # The latest arrival is not necessarily the only branch.
                    # Preserve every unresolved live branch, even if another
                    # device deleted its concurrent version thirty days ago.
                    heads = list(db.execute('''SELECT r.cursor,r.deleted,r.created FROM revisions r
                        WHERE r.uid=? AND r.object=? AND NOT EXISTS
                        (SELECT 1 FROM revisions child WHERE child.uid=r.uid
                        AND child.object=r.object AND child.parent=r.revision)''',(uid,obj['object'])))
                    if heads and all(head['deleted'] and head['created'] <= now-30*86400 for head in heads):
                        # Keep a small change tombstone so offline devices see the deletion.
                        retained = tuple(head['cursor'] for head in heads)
                        placeholders = ','.join('?' for _ in retained)
                        db.execute(f'DELETE FROM revisions WHERE uid=? AND object=? AND cursor NOT IN ({placeholders})',(uid,obj['object'],*retained))
                        db.execute(f'UPDATE revisions SET bytes=0,payload=? WHERE cursor IN ({placeholders})',(json.dumps({'v':1,'nonce':'','ciphertext':'','purged':True}),*retained))
        return purged
