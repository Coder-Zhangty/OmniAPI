"""Rebuild an empty archive from a downloaded Drive folder, without old SQLite."""
import argparse
import json
import sqlite3
from pathlib import Path
from .store import Store,digest,encode


def scoped(source, relative, uid):
    path=(source/relative).resolve()
    if not path.is_relative_to(source.resolve()/f'user-{uid}') or not path.is_file():
        raise ValueError('Archive object missing or outside owner directory')
    return path


def restore(source,output):
    source,output=Path(source).resolve(),Path(output).resolve()
    if output.exists():
        raise ValueError('恢复目标必须是不存在的新目录，不覆盖现有档案')
    manifests=list(source.glob('user-*/projects/*/conversations/*/manifests/*/*/*/*.json'))
    if not manifests:
        raise ValueError('没有发现 v3 清单，请选择归档根目录')
    # Validate every segment and dependency before publishing a restored store.
    objects=[]
    for file in manifests:
        manifest=json.loads(file.read_bytes())
        uid=manifest['user_id']
        relative=str(file.relative_to(source)).replace('\\','/').replace('/manifests/','/records/').removesuffix('.json')+'.jsonl'
        segment=scoped(source,relative,uid)
        data=segment.read_bytes()
        if len(data)!=manifest['bytes'] or digest(data)!=manifest['sha256']:
            raise ValueError('归档分段校验失败')
        for attachment in manifest.get('attachment_metadata',[]):
            if attachment.get('path'):
                payload=scoped(source,attachment['path'],uid).read_bytes()
                if digest(payload)!=attachment['hash']:
                    raise ValueError('附件校验失败')
        objects.append((segment,manifest))
    output.mkdir(parents=True,mode=0o700)
    store=Store(output,min_free=0)
    staging=sqlite3.connect(output/'restore-staging.sqlite3')
    staging.execute('CREATE TABLE staged(rid TEXT,seq INTEGER,hash TEXT,raw BLOB,PRIMARY KEY(rid,seq))')
    count=0
    try:
        for segment,manifest in objects:
            with segment.open('rb') as stream:
                for line in stream:
                    event=json.loads(line)
                    raw=event['transport_json'].encode()
                    if digest(raw)!=event['event_hash']:
                        raise ValueError('事件校验失败')
                    envelope=json.loads(raw)
                    if envelope['meta']['user_id']!=manifest['user_id']:
                        raise ValueError('清单与事件用户不一致')
                    rid,seq=envelope['meta']['archive_id'],envelope['meta']['seq']
                    old=staging.execute('SELECT hash FROM staged WHERE rid=? AND seq=?',(rid,seq)).fetchone()
                    if old and old[0]!=event['event_hash']:
                        raise ValueError('重复事件内容冲突')
                    staging.execute('INSERT OR IGNORE INTO staged VALUES(?,?,?,?)',(rid,seq,event['event_hash'],raw))
        staging.commit()
        for (raw,) in staging.execute('SELECT raw FROM staged ORDER BY rid,seq'):
            store.ingest(raw);count+=1
        for _,manifest in objects:
            for attachment in manifest.get('attachment_metadata',[]):
                payload=scoped(source,attachment['path'],manifest['user_id']).read_bytes() if attachment.get('path') else None
                store.add_attachment(attachment['request_id'],attachment['name'],attachment['mime'],payload,attachment.get('external_url'))
        catalog_times={}
        for folder in source.glob('user-*/catalogs'):
            catalogs=sorted(folder.glob('*.json'))
            if catalogs:
                catalog=json.loads(catalogs[-1].read_bytes())
                uid=int(folder.parent.name.removeprefix('user-'))
                if uid != catalog['user']['id']:
                    raise ValueError('目录快照与所属用户不一致')
                with store.db() as db:
                    db.execute('INSERT INTO users VALUES(?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name',(uid,catalog['user']['name']))
                catalog_times[uid]=catalog.get('created',0)
        controls=[]
        for control in source.glob('user-*/controls/*.json'):
            value=json.loads(control.read_bytes())
            uid=int(control.parent.parent.name.removeprefix('user-'))
            if value.get('user_id',uid)!=uid:
                raise ValueError('整理记录与所属用户不一致')
            controls.append({**value,'user_id':uid})
        unresolved=0
        for value in sorted(controls,key=lambda v:v.get('time',0)):
            uid,action=value['user_id'],value.get('action')
            with store.db() as db:
                db.execute('INSERT OR IGNORE INTO users VALUES(?,?)',(uid,'用户 '+str(uid)))
            if action=='group':
                with store.db() as db:
                    rows=[db.execute('SELECT user_id FROM records WHERE id=?',(rid,)).fetchone() for rid in value['request_ids']]
                if any(row and row[0]!=uid for row in rows):
                    raise ValueError('整理记录引用其他用户')
                if any(row is None for row in rows):
                    unresolved+=1
                    continue
                store.group(value['request_ids'],value['project'],value['conversation'],journal=False)
            elif action=='user_name' and value.get('time',0)>catalog_times.get(uid,0):
                store.rename_user(uid,value['name'],journal=False)
            elif action=='collection':
                store.collection(uid,value['project'],value['conversation'],value.get('title',''),journal=False)
        # Incomplete source prefixes stay visibly incomplete.
        with store.db() as db:
            db.execute("UPDATE records SET status='interrupted' WHERE status='pending'")
        result={'state':'RESTORED_AWAITING_OPERATOR_REVIEW','events':count,'requests':store.metrics()['requests'],'segments':len(objects),'unresolved_controls':unresolved}
        (output/'restore-result.json').write_bytes(encode(result))
        return result
    except Exception:
        (output/'RESTORE_FAILED.txt').write_text('恢复未通过完整性检查，请勿将此目录作为正式档案。',encoding='utf-8')
        raise
    finally:
        staging.close()
        (output/'restore-staging.sqlite3').unlink(missing_ok=True)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    subs=parser.add_subparsers(dest='action',required=True)
    command=subs.add_parser('restore');command.add_argument('--source',required=True);command.add_argument('--output',required=True)
    command=subs.add_parser('checkpoint');command.add_argument('--data',required=True)
    args=parser.parse_args()
    if args.action=='restore':print(json.dumps(restore(args.source,args.output),ensure_ascii=False))
    else:Store(args.data).checkpoint();print('Catalog checkpoints queued')


if __name__=='__main__':main()
