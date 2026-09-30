#!/usr/bin/env python3
"""Append-only, server-side one-time notices for OmniAPI users and admins."""
import argparse
import datetime as dt
import json
import os
import re
import secrets
from pathlib import Path

DEFAULT_PATH = Path(__file__).resolve().parents[1] / 'runtime/user-notices.jsonl'
NOTICE_ID = re.compile(r'^[A-Za-z0-9:_-]{1,96}$')


def iso():
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec='seconds')


def append(event, path=DEFAULT_PATH):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    line = (json.dumps(event, ensure_ascii=False, sort_keys=True) + '\n').encode('utf-8')
    fd = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o640)
    try:
        os.write(fd, line)
        os.fsync(fd)
    finally:
        os.close(fd)
    return event


def load(path=DEFAULT_PATH):
    try:
        lines = Path(path).read_text(encoding='utf-8').splitlines()
    except FileNotFoundError:
        return []
    events = []
    for line in lines:
        try:
            item = json.loads(line)
            if isinstance(item, dict):
                events.append(item)
        except ValueError:
            continue
    return events


def publish(path, title, body, uid=None, audience='user', notice_id=None,
            href='', action='', kind='notice'):
    if audience not in ('user', 'admin'):
        raise ValueError('audience must be user or admin')
    if audience == 'user' and (not isinstance(uid, int) or uid <= 0):
        raise ValueError('a positive uid is required')
    notice_id = notice_id or ('N-' + secrets.token_hex(12))
    if not NOTICE_ID.fullmatch(notice_id):
        raise ValueError('invalid notice id')
    existing = {str(e.get('id')) for e in load(path) if e.get('type') == 'notice'}
    if notice_id in existing:
        return None
    event = {'type': 'notice', 'id': notice_id, 'audience': audience,
             'title': str(title).strip()[:120], 'body': str(body).strip()[:1200],
             'kind': str(kind).strip()[:40], 'created_at': iso()}
    if uid is not None:
        event['uid'] = int(uid)
    if href:
        event['href'] = str(href).strip()[:300]
    if action:
        event['action'] = str(action).strip()[:40]
    return append(event, path)


def unread(path, uid, role=1):
    uid = int(uid)
    events = load(path)
    read_ids = {str(e.get('id')) for e in events
                if e.get('type') == 'read' and int(e.get('uid') or 0) == uid}
    result = []
    seen = set()
    for event in events:
        if event.get('type') != 'notice':
            continue
        notice_id = str(event.get('id') or '')
        if not notice_id or notice_id in seen or notice_id in read_ids:
            continue
        targeted = event.get('audience') == 'user' and int(event.get('uid') or 0) == uid
        admin = event.get('audience') == 'admin' and int(role or 0) >= 10
        if targeted or admin:
            seen.add(notice_id)
            result.append({key: event.get(key) for key in
                           ('id', 'title', 'body', 'kind', 'created_at', 'href', 'action')})
    return result[-20:]


def acknowledge(path, notice_id, uid, role=1):
    visible = {row['id'] for row in unread(path, uid, role)}
    if notice_id not in visible:
        return False
    append({'type': 'read', 'id': notice_id, 'uid': int(uid), 'at': iso()}, path)
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--path', type=Path, default=DEFAULT_PATH)
    parser.add_argument('--uid', type=int, required=True)
    parser.add_argument('--title', required=True)
    parser.add_argument('--body', required=True)
    parser.add_argument('--id', dest='notice_id')
    args = parser.parse_args()
    event = publish(args.path, args.title, args.body, uid=args.uid,
                    notice_id=args.notice_id)
    print(json.dumps({'status': 'CREATED' if event else 'EXISTS',
                      'id': args.notice_id or (event or {}).get('id')}, ensure_ascii=False))


if __name__ == '__main__':
    main()
