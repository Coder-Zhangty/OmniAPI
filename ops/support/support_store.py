#!/usr/bin/env python3
"""客服会话的 append-only 事件日志，以及从事件折叠出的内存状态。

沿用充值申请那条纪律：**事件日志是唯一事实来源，状态由折叠得出，从不覆写历史**。
区别只有一个：客服要实时把新消息推给另一端，所以这里额外维护一份内存索引，
但内存里的东西随时可以从磁盘重新折叠出来——它只是缓存，不是账本。

事件类型：

    conversation  {id, uid, username, source, created_at}
    message       {conv, seq, role, text, created_at, [ai]}
    read          {conv, by, upto_seq}
    handoff       {conv, reason}
    close         {conv, by, reason}

`role` 取 `user` / `operator` / `ai` / `system`。`system` 只用于「已转人工」这类
时间线提示，它不会被当成有人说过话。

文件按 UTC 日期分片：`runtime/support/YYYY-MM-DD.jsonl`，这样保留期可以直接删整天文件。
"""
import datetime as dt
import hashlib
import json
import os
import re
import secrets
import sqlite3
import threading
from pathlib import Path

CONV_ID = re.compile(r'^S-\d{8}-[0-9a-f]{6}$')
ROLES = ('user', 'operator', 'ai', 'system')
MAX_TEXT = 2000                  # 单条消息字符上限
MAX_MESSAGES_PER_CONV = 1000     # 内存里每个会话保留的消息数
MAX_CONVERSATIONS = 300          # 内存里保留的会话数；超出即拒绝开新会话
OPEN_STATUSES = ('open', 'waiting_operator', 'waiting_user')
ACTIVE_STATUSES = ('waiting_operator', 'waiting_user')
STATUSES = ACTIVE_STATUSES + ('resolved', 'closed')
CATEGORIES = ('recharge', 'api_access', 'model_error', 'billing', 'account_security', 'other')
PRIORITIES = ('low', 'normal', 'high')


def utcnow():
    return dt.datetime.now(dt.timezone.utc)


def iso(stamp=None):
    return (stamp or utcnow()).isoformat(timespec='seconds')


def day_of(stamp=None):
    return (stamp or utcnow()).strftime('%Y-%m-%d')


def new_conversation_id(now=None):
    """对外只用这个编号，不出现数字用户 ID。"""
    stamp = now or utcnow()
    return 'S-%s-%s' % (stamp.strftime('%Y%m%d'), secrets.token_hex(3))


def text_error(text, maximum=MAX_TEXT):
    """Return a reason the text is unusable, or None when it is fine."""
    if not isinstance(text, str):
        return '消息内容无效'
    stripped = text.strip()
    if not stripped:
        return '消息不能为空'
    if len(stripped) > maximum:
        return '消息过长（上限 %d 字）' % maximum
    if '\x00' in stripped:
        return '消息包含非法字符'
    return None


def append_event(event, path):
    """Append one event as a single JSON line, fsynced before returning.

    A crash between the write and the flush would leave the operator looking at a
    message the customer never sent (or worse, the reverse), so this does not
    return until the bytes are on disk.
    """
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    line = json.dumps(event, ensure_ascii=False, sort_keys=True)
    with path.open('a', encoding='utf-8') as handle:
        handle.write(line + '\n')
        handle.flush()
        os.fsync(handle.fileno())
    return line


def event_id(event):
    """Stable id for legacy rows, random id for newly appended rows."""
    if event.get('event_id'):
        return str(event['event_id'])
    raw = json.dumps(event, ensure_ascii=False, sort_keys=True,
                     separators=(',', ':')).encode('utf-8')
    return 'legacy-' + hashlib.sha256(raw).hexdigest()[:32]


def load_events(path):
    """Read a JSONL event file, ignoring the torn last line a crash can leave."""
    events = []
    path = Path(path)
    if not path.exists():
        return events
    with path.open('r', encoding='utf-8') as handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if isinstance(event, dict) and event.get('type'):
                events.append(event)
    return events


def day_files(folder, since_day=None):
    """Every day file under `folder`, oldest first."""
    folder = Path(folder)
    if not folder.is_dir():
        return []
    files = sorted(folder.glob('*.jsonl'))
    if since_day:
        files = [path for path in files if path.stem >= since_day]
    return files


def prune(folder, older_than_days, apply=False, now=None):
    """Delete whole day files older than the retention window (dry-run by default)."""
    cutoff = (now or utcnow()) - dt.timedelta(days=int(older_than_days))
    cutoff_day = cutoff.strftime('%Y-%m-%d')
    doomed = [path for path in day_files(folder) if path.stem < cutoff_day]
    removed = []
    attachment_names = []
    for path in doomed:
        for event in load_events(path):
            if event.get('type') == 'attachment' and event.get('stored_name'):
                attachment_names.append(Path(str(event['stored_name'])).name)
        if apply:
            path.unlink()
        removed.append(path.name)
    removed_attachments = []
    if apply:
        attachment_folder = Path(folder).parent / 'support-attachments'
        for name in sorted(set(attachment_names)):
            target = (attachment_folder / name).resolve()
            if target.parent == attachment_folder.resolve() and target.is_file():
                target.unlink(); removed_attachments.append(name)
    return {'cutoff_day': cutoff_day, 'files': removed,
            'attachments': removed_attachments if apply else sorted(set(attachment_names)),
            'applied': bool(apply)}


class SupportIndex:
    """Disposable SQLite search index. JSONL remains the source of truth."""

    def __init__(self, path):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.db = None
        try:
            self._initialize()
        except sqlite3.DatabaseError:
            # The index is disposable. Preserve the broken bytes for diagnosis,
            # then rebuild from the append-only JSONL log.
            self.close()
            if self.path.exists():
                broken = self.path.with_name(self.path.name + '.corrupt-' + utcnow().strftime('%Y%m%d%H%M%S'))
                os.replace(self.path, broken)
            for suffix in ('-wal', '-shm'):
                try: self.path.with_name(self.path.name + suffix).unlink()
                except FileNotFoundError: pass
            self._initialize()

    def _initialize(self):
        self.db = self._open()
        self.db.execute('PRAGMA journal_mode=WAL')
        self.db.executescript('''
        CREATE TABLE IF NOT EXISTS events(
          event_id TEXT PRIMARY KEY, ticket_id TEXT NOT NULL, kind TEXT NOT NULL,
          created_at TEXT NOT NULL, payload TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS tickets(
          id TEXT PRIMARY KEY, uid INTEGER, username TEXT, source TEXT,
          subject TEXT, category TEXT, priority TEXT, status TEXT,
          assignee_id TEXT, created_at TEXT, updated_at TEXT,
          first_response_at TEXT, resolved_at TEXT, closed_at TEXT,
          last_seq INTEGER DEFAULT 0, last_message_at TEXT, last_text TEXT,
          last_role TEXT, unread_operator INTEGER DEFAULT 0,
          unread_user INTEGER DEFAULT 0, needs_human INTEGER DEFAULT 0,
          handoff_reason TEXT DEFAULT '');
        CREATE TABLE IF NOT EXISTS messages(
          ticket_id TEXT NOT NULL, seq INTEGER NOT NULL, role TEXT NOT NULL,
          text TEXT NOT NULL, created_at TEXT NOT NULL, ai TEXT,
          attachments TEXT NOT NULL DEFAULT '[]', PRIMARY KEY(ticket_id, seq));
        CREATE TABLE IF NOT EXISTS notes(
          event_id TEXT PRIMARY KEY, ticket_id TEXT NOT NULL, text TEXT NOT NULL,
          created_at TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS attachments(
          id TEXT PRIMARY KEY, ticket_id TEXT NOT NULL, seq INTEGER,
          original_name TEXT, mime TEXT, size INTEGER, sha256 TEXT,
          stored_name TEXT, uploader_role TEXT, created_at TEXT);
        CREATE TABLE IF NOT EXISTS settings(
          key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS idx_tickets_queue
          ON tickets(status, priority, last_message_at);
        CREATE INDEX IF NOT EXISTS idx_tickets_uid ON tickets(uid, updated_at);
        CREATE INDEX IF NOT EXISTS idx_messages_ticket ON messages(ticket_id, seq);
        ''')
        self.db.commit()
        self.db.close()
        self.db = None
        os.chmod(self.path, 0o600)

    def _open(self):
        db = sqlite3.connect(str(self.path), check_same_thread=False, timeout=10)
        db.row_factory = sqlite3.Row
        db.execute('PRAGMA synchronous=FULL')
        return db

    def close(self):
        if self.db is not None:
            self.db.close(); self.db = None

    def reset(self):
        self.db = self._open()
        with self.db:
            for table in ('events', 'tickets', 'messages', 'notes', 'attachments', 'settings'):
                self.db.execute('DELETE FROM ' + table)
        self.close()

    def apply(self, event):
        eid = event_id(event)
        conv = str(event.get('conv') or event.get('id') or '')
        if not conv:
            return False
        payload = json.dumps(event, ensure_ascii=False, sort_keys=True)
        self.db = self._open()
        try:
            with self.db:
                self.db.execute('INSERT INTO events VALUES(?,?,?,?,?)',
                                (eid, conv, str(event.get('type') or ''),
                                 str(event.get('created_at') or ''), payload))
                self._project(event, conv, eid)
            return True
        except sqlite3.IntegrityError:
            return False
        finally:
            self.close()

    def _project(self, event, conv, eid):
        kind = event.get('type')
        stamp = event.get('created_at') or iso()
        if kind == 'conversation':
            self.db.execute('''INSERT OR IGNORE INTO tickets
              (id,uid,username,source,subject,category,priority,status,assignee_id,
               created_at,updated_at,last_message_at,last_text,last_role)
              VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)''',
              (conv, event.get('uid'), event.get('username') or '', event.get('source') or '',
               event.get('subject') or '客服咨询', event.get('category') or 'other',
               event.get('priority') or 'normal', event.get('status') or 'waiting_operator',
               event.get('assignee_id'), stamp, stamp, stamp, '', ''))
        elif kind == 'message':
            seq = int(event.get('seq') or 0)
            role = str(event.get('role') or '')
            attachments = json.dumps(event.get('attachments') or [], ensure_ascii=False)
            self.db.execute('INSERT OR IGNORE INTO messages VALUES(?,?,?,?,?,?,?)',
                            (conv, seq, role, event.get('text') or '', stamp,
                             json.dumps(event.get('ai'), ensure_ascii=False) if event.get('ai') else None,
                             attachments))
            target = 'waiting_operator' if role == 'user' else (
                'waiting_user' if role == 'operator' else None)
            if role == 'user':
                unread = 'unread_operator=unread_operator+1'
            elif role in ('operator', 'ai'):
                unread = 'unread_user=unread_user+1'
            else:
                unread = 'unread_user=unread_user'
            status_sql = ',status=?' if target else ''
            args = [seq, stamp, (event.get('text') or '')[:120], role, stamp]
            if target:
                args.append(target)
            args.append(conv)
            self.db.execute('''UPDATE tickets SET last_seq=?,last_message_at=?,
              last_text=?,last_role=?,updated_at=?,%s%s WHERE id=?''' % (unread, status_sql), args)
            if role == 'operator':
                self.db.execute('UPDATE tickets SET first_response_at=COALESCE(first_response_at,?) WHERE id=?',
                                (stamp, conv))
        elif kind == 'read':
            field = 'unread_operator' if event.get('by') == 'operator' else 'unread_user'
            self.db.execute('UPDATE tickets SET %s=0,updated_at=? WHERE id=?' % field,
                            (stamp, conv))
        elif kind == 'handoff':
            self.db.execute('UPDATE tickets SET needs_human=1,handoff_reason=?,updated_at=? WHERE id=?',
                            (str(event.get('reason') or ''), stamp, conv))
        elif kind in ('close', 'status'):
            status = 'closed' if kind == 'close' else str(event.get('status') or '')
            if status not in STATUSES:
                return
            self.db.execute('''UPDATE tickets SET status=?,updated_at=?,
              resolved_at=CASE WHEN ?='resolved' THEN ? ELSE resolved_at END,
              closed_at=CASE WHEN ?='closed' THEN ? ELSE closed_at END WHERE id=?''',
              (status, stamp, status, stamp, status, stamp, conv))
        elif kind == 'note':
            self.db.execute('INSERT OR IGNORE INTO notes VALUES(?,?,?,?)',
                            (eid, conv, event.get('text') or '', stamp))
        elif kind == 'attachment':
            self.db.execute('''INSERT OR IGNORE INTO attachments
              VALUES(?,?,?,?,?,?,?,?,?,?)''',
              (str(event.get('attachment_id') or ''), conv, event.get('seq'),
               event.get('original_name') or '', event.get('mime') or '',
               int(event.get('size') or 0), event.get('sha256') or '',
               event.get('stored_name') or '', event.get('uploader_role') or '', stamp))
        elif kind == 'settings':
            for key, value in (event.get('values') or {}).items():
                self.db.execute('INSERT OR REPLACE INTO settings VALUES(?,?,?)',
                                (str(key), json.dumps(value, ensure_ascii=False), stamp))

    def replay(self, folder):
        applied = skipped = 0
        for path in day_files(folder):
            for event in load_events(path):
                if self.apply(event):
                    applied += 1
                else:
                    skipped += 1
        return {'applied': applied, 'skipped': skipped}

    def search(self, text='', category='', status='', priority='', unread=False,
               limit=100, offset=0):
        where, args = [], []
        if text:
            where.append('(id LIKE ? OR username LIKE ? OR subject LIKE ? OR last_text LIKE ?)')
            needle = '%' + str(text)[:100] + '%'
            args.extend([needle] * 4)
        for field, value, allowed in (('category', category, CATEGORIES),
                                      ('status', status, STATUSES),
                                      ('priority', priority, PRIORITIES)):
            if value in allowed:
                where.append(field + '=?'); args.append(value)
        if unread:
            where.append('unread_operator>0')
        clause = (' WHERE ' + ' AND '.join(where)) if where else ''
        sql = '''SELECT * FROM tickets%s ORDER BY
          CASE WHEN status='waiting_operator' AND datetime(last_message_at) <= datetime('now','-30 minutes') THEN 0 ELSE 1 END,
          CASE priority WHEN 'high' THEN 0 WHEN 'normal' THEN 1 ELSE 2 END,
          CASE WHEN status='waiting_operator' THEN last_message_at END ASC,
          updated_at DESC LIMIT ? OFFSET ?''' % clause
        db = self._open()
        try:
            rows = db.execute(sql, args + [max(1, min(int(limit), 200)), max(0, int(offset))]).fetchall()
            return [dict(row) for row in rows]
        finally:
            db.close()

    def messages(self, conv, since=0, include_notes=False):
        db = self._open()
        rows = [dict(row) for row in db.execute(
            'SELECT * FROM messages WHERE ticket_id=? AND seq>? ORDER BY seq',
            (conv, int(since))).fetchall()]
        for row in rows:
            row.pop('ticket_id', None)
            row['ai'] = json.loads(row['ai']) if row.get('ai') else None
            row['attachments'] = json.loads(row.get('attachments') or '[]')
        if include_notes:
            notes = [dict(row) for row in db.execute(
                'SELECT event_id,text,created_at FROM notes WHERE ticket_id=? ORDER BY created_at',
                (conv,)).fetchall()]
            for note in notes:
                note.update({'role': 'note', 'internal': True, 'seq': 0})
            rows.extend(notes); rows.sort(key=lambda row: row.get('created_at') or '')
        db.close(); return rows

    def attachments(self, conv):
        db = self._open()
        try:
            return [dict(row) for row in db.execute(
                'SELECT * FROM attachments WHERE ticket_id=? ORDER BY created_at', (conv,)).fetchall()]
        finally: db.close()

    def attachment(self, attachment_id):
        db = self._open()
        try:
            row = db.execute('SELECT * FROM attachments WHERE id=?',
                             (str(attachment_id),)).fetchone()
            return dict(row) if row else None
        finally: db.close()


class ConversationStore:
    """The in-memory view of the event log, plus the only writer for new events.

    Single process by design: every SSE stream terminates in this process, so the
    broadcast does not need Redis. `lock` guards both the index and the append, so
    the sequence numbers a reader sees can never go backwards.
    """

    def __init__(self, folder, max_conversations=MAX_CONVERSATIONS,
                 max_messages=MAX_MESSAGES_PER_CONV, load_days=8):
        self.folder = Path(folder)
        self.max_conversations = max_conversations
        self.max_messages = max_messages
        self.load_days = load_days
        self.lock = threading.RLock()
        self.conversations = {}
        self.messages = {}
        self.condition = threading.Condition(self.lock)
        # Bumped on every appended timeline event. Waiters compare it against the
        # revision they read their snapshot at, which is what makes the
        # fetch-then-wait sequence free of lost wakeups: a message that lands
        # between the two makes the comparison fail and the loop run again
        # immediately, instead of sleeping until the next heartbeat.
        self.revision = 0
        self.index = SupportIndex(self.folder / 'support-index.sqlite3')
        # Idempotently fill gaps left by a crash between fsync(JSONL) and SQLite.
        self.index.replay(self.folder)
        self._load_recent()

    # -- load ------------------------------------------------------------
    def _load_recent(self):
        today = utcnow()
        since = (today - dt.timedelta(days=max(0, self.load_days - 1))).strftime('%Y-%m-%d')
        events = []
        for path in day_files(self.folder, since_day=since):
            events.extend(load_events(path))
        self.fold(events)
        # Without this the memory cap would be fiction: a long log would be folded
        # in full at every start. Evicted conversations stay on disk and `history()`
        # reads them back on demand.
        self.evict()

    def path_for(self, stamp=None):
        return self.folder / (day_of(stamp) + '.jsonl')

    # -- folding ---------------------------------------------------------
    def fold(self, events):
        """Rebuild state from events. Safe to call with the full log at any time."""
        for event in events:
            kind = event.get('type')
            conv = event.get('conv') or event.get('id')
            if not conv:
                continue
            if kind == 'conversation':
                self.conversations[conv] = {
                    'id': conv,
                    'uid': event.get('uid'),
                    'username': event.get('username') or '',
                    'source': event.get('source') or '',
                    'created_at': event.get('created_at') or iso(),
                    'subject': event.get('subject') or '客服咨询',
                    'category': event.get('category') or 'other',
                    'priority': event.get('priority') or 'normal',
                    'status': event.get('status') or 'waiting_operator',
                    'assignee_id': event.get('assignee_id'),
                    'first_response_at': None,
                    'resolved_at': None,
                    'closed_at': None,
                    'last_seq': 0,
                    'last_message_at': event.get('created_at') or iso(),
                    'needs_human': False,
                    'handoff_reason': '',
                    'ai_rounds': 0,
                    # Queue preview. Without it the operator has to open every
                    # conversation to learn what the customer wants.
                    'last_text': '',
                    'last_role': '',
                    'unread_operator': 0,
                    'unread_user': 0,
                }
                self.messages.setdefault(conv, [])
                continue
            state = self.conversations.get(conv)
            if state is None:
                continue          # an event for an unknown conversation is ignored
            if kind == 'message':
                role = event.get('role')
                if role not in ROLES:
                    continue
                seq = int(event.get('seq') or state['last_seq'] + 1)
                if seq <= state['last_seq']:
                    continue      # already have it (replay of an overlapping file)
                state['last_seq'] = seq
                state['last_message_at'] = event.get('created_at') or iso()
                self.messages.setdefault(conv, []).append({
                    'seq': seq, 'role': role, 'text': event.get('text') or '',
                    'created_at': event.get('created_at') or iso(),
                    'ai': event.get('ai'), 'attachments': event.get('attachments') or [],
                })
                if role != 'system':
                    # `system` rows are notices ("已转人工"), not something
                    # anybody said, so they must not become the queue preview.
                    state['last_text'] = (event.get('text') or '')[:120]
                    state['last_role'] = role
                if role == 'user':
                    state['unread_operator'] += 1
                    state['status'] = 'waiting_operator'
                elif role in ('operator', 'ai'):
                    state['unread_user'] += 1
                    if role == 'operator':
                        state['status'] = 'waiting_user'
                        state['first_response_at'] = state.get('first_response_at') or state['last_message_at']
                    if role == 'ai':
                        state['ai_rounds'] += 1
            elif kind == 'read':
                by = str(event.get('by') or '')
                if by == 'operator':
                    state['unread_operator'] = 0
                elif by == 'user':
                    state['unread_user'] = 0
            elif kind == 'handoff':
                state['needs_human'] = True
                state['handoff_reason'] = str(event.get('reason') or '')
            elif kind == 'close':
                state['status'] = 'closed'
                state['closed_at'] = event.get('created_at') or iso()
            elif kind == 'status':
                status = str(event.get('status') or '')
                if status in STATUSES:
                    state['status'] = status
                    if status == 'resolved':
                        state['resolved_at'] = event.get('created_at') or iso()
                    elif status == 'closed':
                        state['closed_at'] = event.get('created_at') or iso()
            elif kind == 'ticket_update':
                for key in ('subject', 'category', 'priority', 'assignee_id'):
                    if key in event:
                        state[key] = event.get(key)
        return self

    # -- queries ---------------------------------------------------------
    def open_for(self, uid):
        for state in self.conversations.values():
            if state['uid'] == uid and state['status'] in OPEN_STATUSES:
                return state
        return None

    def get(self, conv_id):
        return self.conversations.get(conv_id)

    def snapshot(self, conv_id, since_seq=0):
        """New messages plus the revision they belong to, read atomically.

        Taken under the same lock the writers use, so a waiter can tell whether
        anything arrived while it was busy sending: that is what removes the
        lost-wakeup window between fetching and waiting.
        """
        with self.condition:
            return self.since(conv_id, since_seq), self.revision

    def since(self, conv_id, since_seq=0, limit=None):
        rows = [row for row in self.messages.get(conv_id, []) if row['seq'] > int(since_seq or 0)]
        if limit is not None:
            rows = rows[-int(limit):]
        return rows

    def history(self, conv_id, since_seq=0):
        """Messages for a conversation, falling back to disk when it is not indexed.

        A conversation closed days ago will have been evicted from memory; the log
        is authoritative, so read it back rather than telling the operator the
        history is gone.
        """
        indexed = self.index.messages(conv_id, since_seq)
        if indexed:
            return indexed
        if conv_id in self.messages:
            return self.since(conv_id, since_seq)
        found = []
        for path in day_files(self.folder):
            for event in load_events(path):
                if (event.get('type') == 'message' and event.get('conv') == conv_id
                        and int(event.get('seq') or 0) > int(since_seq or 0)):
                    found.append({'seq': int(event['seq']), 'role': event.get('role'),
                                  'text': event.get('text') or '',
                                  'created_at': event.get('created_at') or '',
                                  'ai': event.get('ai')})
        found.sort(key=lambda row: row['seq'])
        return found

    def listing(self, include_closed=True):
        """Open conversations first, then the ones waiting for a human, newest first.

        Two stable passes rather than one clever key: sorting is cheap here and the
        intent stays readable when someone changes the ordering later.
        """
        rows = [state for state in self.conversations.values()
                if include_closed or state['status'] in OPEN_STATUSES]
        rows.sort(key=lambda state: str(state['last_message_at'] or ''), reverse=True)
        rows.sort(key=lambda state: (state['status'] not in OPEN_STATUSES,
                                     not state['needs_human']))
        return rows

    def search(self, **filters):
        return self.index.search(**filters)

    # -- mutation --------------------------------------------------------
    def _append(self, event):
        event = dict(event)
        event.setdefault('event_id', secrets.token_hex(16))
        append_event(event, self.path_for(_parse_iso(event.get('created_at'))))
        self.index.apply(event)
        self.fold([event])
        self.revision += 1
        self.condition.notify_all()

    def start(self, uid, username='', source='', subject='客服咨询', category='other',
              priority='normal', reuse=True):
        """Open a conversation, or return the one already open for this account."""
        with self.lock:
            existing = self.open_for(uid) if reuse else None
            if existing:
                return existing
            if len(self.conversations) >= self.max_conversations:
                self.evict()
            if len(self.conversations) >= self.max_conversations:
                raise ValueError('客服会话已达上限，请稍后再试或发邮件联系')
            conv_id = new_conversation_id()
            if category not in CATEGORIES:
                raise ValueError('工单分类无效')
            if priority not in PRIORITIES:
                raise ValueError('工单优先级无效')
            event = {'type': 'conversation', 'id': conv_id, 'uid': uid,
                     'username': username, 'source': source, 'subject': str(subject).strip()[:120],
                     'category': category, 'priority': priority,
                     'status': 'waiting_operator', 'created_at': iso()}
            self._append(event)
            return self.conversations[conv_id]

    def add_message(self, conv_id, role, text, ai=None, attachments=None):
        """Append one message. Returns the stored row (with its sequence number)."""
        if role not in ROLES:
            raise ValueError('消息角色无效')
        reason = text_error(text) if role != 'system' else None
        if reason:
            raise ValueError(reason)
        with self.lock:
            state = self.conversations.get(conv_id)
            if state is None:
                raise ValueError('会话不存在')
            if state['status'] not in OPEN_STATUSES:
                raise ValueError('会话已结束，请重新发起')
            if len(self.messages.get(conv_id, [])) >= self.max_messages:
                raise ValueError('该会话消息过多，请结束会话后重新发起')
            seq = state['last_seq'] + 1
            event = {'type': 'message', 'conv': conv_id, 'seq': seq, 'role': role,
                     'text': text.strip() if role != 'system' else text,
                     'created_at': iso()}
            if ai:
                event['ai'] = ai
            if attachments:
                event['attachments'] = list(attachments)[:3]
            self._append(event)
            return self.messages[conv_id][-1]

    def append_quiet(self, event):
        """Record an event that is not part of the conversation timeline.

        Notification attempts go through here: they must be auditable, but they
        are not messages and they must not wake every open stream.
        """
        with self.lock:
            event = dict(event)
            event.setdefault('event_id', secrets.token_hex(16))
            append_event(event, self.path_for(_parse_iso(event.get('created_at'))))
            self.index.apply(event)

    def mark_read(self, conv_id, by):
        if by not in ('user', 'operator'):
            raise ValueError('读取方无效')
        with self.lock:
            if conv_id not in self.conversations:
                raise ValueError('会话不存在')
            self._append({'type': 'read', 'conv': conv_id, 'by': by,
                          'upto_seq': self.conversations[conv_id]['last_seq'],
                          'created_at': iso()})

    def handoff(self, conv_id, reason=''):
        with self.lock:
            state = self.conversations.get(conv_id)
            if state is None:
                raise ValueError('会话不存在')
            if state['needs_human']:
                return state
            self._append({'type': 'handoff', 'conv': conv_id, 'reason': reason,
                          'created_at': iso()})
            return self.conversations[conv_id]

    def close(self, conv_id, by='operator', reason=''):
        with self.lock:
            state = self.conversations.get(conv_id)
            if state is None:
                raise ValueError('会话不存在')
            if state['status'] not in OPEN_STATUSES:
                raise ValueError('会话已经结束')
            self._append({'type': 'close', 'conv': conv_id, 'by': by,
                          'reason': reason, 'created_at': iso()})
            return self.conversations[conv_id]

    def set_status(self, conv_id, status, by='operator', reason=''):
        if status not in STATUSES:
            raise ValueError('工单状态无效')
        with self.lock:
            state = self.conversations.get(conv_id)
            if state is None:
                raise ValueError('工单不存在')
            if status in ACTIVE_STATUSES and state['status'] == 'closed':
                closed = _parse_iso(state.get('closed_at'))
                if not closed or utcnow() - closed.astimezone(dt.timezone.utc) > dt.timedelta(days=7):
                    raise ValueError('工单关闭超过 7 天，无法重新打开')
            self._append({'type': 'status', 'conv': conv_id, 'status': status,
                          'by': by, 'reason': str(reason)[:200], 'created_at': iso()})
            return self.conversations[conv_id]

    def update_ticket(self, conv_id, **values):
        with self.lock:
            state = self.conversations.get(conv_id)
            if state is None:
                raise ValueError('工单不存在')
            clean = {}
            if 'subject' in values:
                subject = str(values['subject']).strip()
                if not subject or len(subject) > 120:
                    raise ValueError('工单标题为 1–120 字')
                clean['subject'] = subject
            if 'category' in values:
                if values['category'] not in CATEGORIES:
                    raise ValueError('工单分类无效')
                clean['category'] = values['category']
            if 'priority' in values:
                if values['priority'] not in PRIORITIES:
                    raise ValueError('工单优先级无效')
                clean['priority'] = values['priority']
            if 'assignee_id' in values:
                clean['assignee_id'] = values['assignee_id']
            if clean:
                self._append({'type': 'ticket_update', 'conv': conv_id,
                              **clean, 'created_at': iso()})
            return self.conversations[conv_id]

    def add_note(self, conv_id, text):
        reason = text_error(text)
        if reason:
            raise ValueError(reason)
        with self.lock:
            if conv_id not in self.conversations:
                raise ValueError('工单不存在')
            event = {'type': 'note', 'conv': conv_id, 'text': text.strip(), 'created_at': iso()}
            self.append_quiet(event)
            return {'role': 'note', 'internal': True, 'text': text.strip(),
                    'created_at': event['created_at']}

    def add_attachment(self, conv_id, attachment):
        with self.lock:
            if conv_id not in self.conversations:
                raise ValueError('工单不存在')
            event = {'type': 'attachment', 'conv': conv_id,
                     **dict(attachment), 'created_at': iso()}
            self.append_quiet(event)
            return event

    def admin_history(self, conv_id, since_seq=0):
        return self.index.messages(conv_id, since_seq, include_notes=True)

    def archive_resolved(self, now=None):
        now = now or utcnow()
        changed = []
        for state in list(self.conversations.values()):
            resolved = _parse_iso(state.get('resolved_at'))
            if state.get('status') == 'resolved' and resolved:
                if now - resolved.astimezone(dt.timezone.utc) >= dt.timedelta(days=7):
                    self.set_status(state['id'], 'closed', by='system', reason='resolved 7 days')
                    changed.append(state['id'])
        return changed

    # -- maintenance -----------------------------------------------------
    def evict(self):
        """Drop the oldest closed conversations from memory (the log keeps them)."""
        if len(self.conversations) <= self.max_conversations:
            return []
        closed = sorted((state for state in self.conversations.values()
                         if state['status'] not in OPEN_STATUSES),
                        key=lambda state: state['last_message_at'])
        dropped = []
        for state in closed:
            if len(self.conversations) <= self.max_conversations:
                break
            self.conversations.pop(state['id'], None)
            self.messages.pop(state['id'], None)
            dropped.append(state['id'])
        return dropped

    def counts(self):
        rows = list(self.conversations.values())
        return {
            'conversations': len(rows),
            'open': sum(1 for state in rows if state['status'] in OPEN_STATUSES),
            'needs_human': sum(1 for state in rows if state['needs_human']
                               and state['status'] in OPEN_STATUSES),
            'unread_operator': sum(state['unread_operator'] for state in rows),
        }


def _parse_iso(value):
    """Best-effort ISO -> datetime, so a file is always named after the event."""
    try:
        return dt.datetime.fromisoformat(str(value))
    except (TypeError, ValueError):
        return None
