// Driver transactions must be exclusive and commit/roll back atomically.
// The journal stores ciphertext only; decrypted values belong to the active UI.
const SCHEMA = [
  'CREATE TABLE IF NOT EXISTS omni_sync_cursor (uid TEXT PRIMARY KEY, cursor INTEGER NOT NULL)',
  `CREATE TABLE IF NOT EXISTS omni_sync_journal (
    uid TEXT NOT NULL, revision TEXT NOT NULL, object TEXT NOT NULL,
    parent TEXT, deleted INTEGER NOT NULL, envelope TEXT,
    pending INTEGER NOT NULL, conflict INTEGER NOT NULL DEFAULT 0,
    cursor INTEGER, purged INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(uid,revision))`,
  'CREATE INDEX IF NOT EXISTS omni_sync_outbox ON omni_sync_journal(uid,pending)',
];
export class EncryptedJournal {
  constructor(driver, user) {
    this.db = driver; this.user = String(user);
    if (!/^[1-9][0-9]{0,15}$/.test(this.user)) throw Error('INVALID_USER');
  }
  async initialize() {
    await this.db.transaction(async tx => {
      for (const sql of SCHEMA) await tx.run(sql, []);
      await tx.run('INSERT OR IGNORE INTO omni_sync_cursor VALUES (?,0)',[this.user]);
    });
  }
  async enqueue(record) {
    await this.db.transaction(async tx => {
      await tx.run(`INSERT INTO omni_sync_journal
        (uid,revision,object,parent,deleted,envelope,pending) VALUES (?,?,?,?,?,?,1)`,
      [this.user,record.revision,record.object,record.parent,Number(record.deleted),JSON.stringify(record.envelope)]);
    });
  }
  async outbox(limit = 100) {
    const rows = await this.db.all(`SELECT revision,object,parent,deleted,envelope FROM omni_sync_journal
      WHERE uid=? AND pending=1 ORDER BY rowid LIMIT ?`,[this.user,Math.max(1,Math.min(limit,100))]);
    return rows.map(row => ({...row,deleted:!!row.deleted,envelope:JSON.parse(row.envelope)}));
  }
  async acknowledge(revision, conflict) {
    await this.db.transaction(tx => tx.run(`UPDATE omni_sync_journal SET pending=0,conflict=?
      WHERE uid=? AND revision=?`,[Number(conflict),this.user,revision]));
  }
  async cursor() {
    const row = await this.db.get('SELECT cursor FROM omni_sync_cursor WHERE uid=?',[this.user]);
    if (!row) throw Error('JOURNAL_NOT_INITIALIZED');
    return row.cursor;
  }
  async applyBatch(values, cursor) {
    if (!Number.isSafeInteger(cursor) || cursor < 0) throw Error('INVALID_CURSOR');
    await this.db.transaction(async tx => {
      const before = await tx.get('SELECT cursor FROM omni_sync_cursor WHERE uid=?',[this.user]);
      if (!before || cursor < before.cursor) throw Error('CURSOR_REGRESSION');
      for (const {record} of values) {
        if (!Number.isSafeInteger(record.cursor) || record.cursor <= before.cursor || record.cursor > cursor) throw Error('INVALID_CHANGE');
        if (record.purged) {
          await tx.run('DELETE FROM omni_sync_journal WHERE uid=? AND object=? AND pending=0',[this.user,record.object]);
        }
        await tx.run(`INSERT INTO omni_sync_journal
          (uid,revision,object,parent,deleted,envelope,pending,cursor,purged)
          VALUES (?,?,?,?,?,?,0,?,?) ON CONFLICT(uid,revision) DO UPDATE SET
          pending=0,cursor=excluded.cursor,purged=excluded.purged,envelope=excluded.envelope`,
        [this.user,record.revision,record.object,record.parent,Number(record.deleted),record.purged?null:JSON.stringify(record.envelope),record.cursor,Number(!!record.purged)]);
      }
      await tx.run('UPDATE omni_sync_cursor SET cursor=? WHERE uid=?',[cursor,this.user]);
    });
  }
  async heads(object) {
    return this.db.all(`SELECT r.* FROM omni_sync_journal r WHERE r.uid=? AND r.object=?
      AND NOT EXISTS (SELECT 1 FROM omni_sync_journal c WHERE c.uid=r.uid AND c.object=r.object AND c.parent=r.revision)
      ORDER BY r.rowid`,[this.user,object]);
  }
  async objects(limit = 100, offset = 0) {
    if (!Number.isSafeInteger(offset) || offset < 0) throw Error('INVALID_OFFSET');
    return this.db.all(`SELECT r.* FROM omni_sync_journal r WHERE r.uid=? AND r.purged=0
      AND NOT EXISTS (SELECT 1 FROM omni_sync_journal c WHERE c.uid=r.uid AND c.object=r.object AND c.parent=r.revision)
      ORDER BY r.rowid DESC LIMIT ? OFFSET ?`, [this.user,Math.max(1,Math.min(limit,100)),offset]);
  }
  async clear() {
    await this.db.transaction(async tx => {
      await tx.run('DELETE FROM omni_sync_journal WHERE uid=?',[this.user]);
      await tx.run('UPDATE omni_sync_cursor SET cursor=0 WHERE uid=?',[this.user]);
    });
  }
}
