import { newId, sealRecord, openRecord } from './crypto.js';

// Adapter must persist encrypted outbox + cursor atomically with applied changes.
// Local plaintext databases remain user/device scoped; never use archive storage.
export class SyncEngine {
  static TRUSTED_SYNC_ORIGINS = ['https://omniapi.biz/api/sync', 'https://backup.omniapi.biz/api/sync'];
  constructor({ user, device, adapter, token, fetch: http = globalThis.fetch }) {
    this.user = String(user); this.device = device; this.adapter = adapter;
    this.token = token; this.http = http; this.key = null; this.running = false;
    this.epoch = 0; this.controller = null;
  }
  unlock(key) { this.lock(); this.key = Uint8Array.from(key); }
  lock() { this.epoch++; this.controller?.abort(); this.key?.fill(0); this.key = null; }
  async request(path, method = 'GET', body) {
    if (!this.key) throw Error('SYNC_LOCKED');
    const epoch = this.epoch, signal = this.controller?.signal;
    const auth = await this.token(this.device);
    if (epoch !== this.epoch || !this.key) throw Error('SYNC_CANCELLED');
    if (!SyncEngine.TRUSTED_SYNC_ORIGINS.includes(auth.url)) throw Error('UNTRUSTED_SYNC_ORIGIN');
    const response = await this.http(auth.url + path, { method, redirect:'error', signal, headers: { Authorization: `Bearer ${auth.token}`, 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    if (epoch !== this.epoch || !this.key) throw Error('SYNC_CANCELLED');
    let result;
    try { result = await response.json(); }
    catch (error) { if (response.status === 401) throw Object.assign(Error('UNAUTHORIZED'),{status:401}); throw error; }
    if (epoch !== this.epoch || !this.key) throw Error('SYNC_CANCELLED');
    if (!response.ok || !result.success) throw Object.assign(Error(result.code || 'SYNC_UNAVAILABLE'),{status:response.status});
    return result.data;
  }
  async queue(object, parent, value, deleted = false) {
    if (!this.key) throw Error('SYNC_LOCKED');
    const revision = newId();
    const envelope = sealRecord(this.key, this.user, object, revision, parent, deleted, value);
    await this.adapter.enqueue({ object, revision, parent, deleted, envelope });
    return revision;
  }
  async run() {
    if (!this.key || this.running) return;
    this.running = true; const epoch = this.epoch; this.controller = new AbortController();
    try {
      await this.request('/v1/devices', 'POST', {});
      const status = await this.request('/v1/status');
      for (const pending of status.read_only ? [] : await this.adapter.outbox(100)) {
        if (epoch !== this.epoch || !this.key) return;
        const result = await this.request(`/v1/objects/${pending.object}/versions/${pending.revision}`, 'PUT', { parent: pending.parent, deleted: pending.deleted, envelope: pending.envelope });
        await this.adapter.acknowledge(pending.revision, result.conflict === true);
      }
      let more = true, pages = 0;
      while (more && pages++ < 10) {
        if (epoch !== this.epoch || !this.key) return;
        const page = await this.request(`/v1/changes?after=${await this.adapter.cursor()}`);
        // Fail the entire batch on any authentication failure; never advance past it.
        const values = page.changes.map(record => ({ record, value: record.purged ? null : openRecord(this.key, this.user, record) }));
        if (epoch !== this.epoch || !this.key) return;
        await this.adapter.applyBatch(values, page.cursor);
        more = page.has_more;
      }
    } finally { this.running = false; this.controller = null; }
  }
}
