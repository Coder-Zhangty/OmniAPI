import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex } from '@noble/hashes/utils.js';
import { b64, unb64, createKeyring, unlockPassword, unlockRecovery, changePassword, openRecord } from './crypto.js';
import { SyncEngine } from './sync.js';

// Platform owners keep this object outside untrusted webviews. The SQL driver
// persists only ciphertext. No password or recovery secret enters secureStore.
export class SyncWorkspace {
  constructor({ user, device, journal, token, secureStore, fetch: http = globalThis.fetch }) {
    this.user = String(user); this.device = device; this.journal = journal;
    this.token = token; this.store = secureStore; this.http = http;
    if (!/^[1-9][0-9]{0,15}$/.test(this.user)) throw Error('INVALID_USER');
    this.master = null; this.epoch = 0; this.ring = null; this.version = 0;
    this.writes = Promise.resolve(); this.controller = new AbortController();
    this.engine = new SyncEngine({ user, device, adapter: journal, token, fetch: http });
    this.storageKey = `omniapi.sync.remembered-key.v1.${this.user}`;
  }
  async request(path, method = 'GET', body) {
    const epoch = this.epoch, signal = this.controller.signal;
    const auth = await this.token(this.device);
    if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
    if (!SyncEngine.TRUSTED_SYNC_ORIGINS.includes(auth.url)) throw Error('UNTRUSTED_SYNC_ORIGIN');
    const response = await this.http(auth.url + path, { method, redirect: 'error', signal,
      headers: { Authorization: `Bearer ${auth.token}`, 'Content-Type': 'application/json' },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
    let result;
    try { result = await response.json(); }
    catch (error) { if (response.status === 401) { await this.lock(true); throw Error('UNAUTHORIZED'); } throw error; }
    if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
    if (!response.ok || !result.success) {
      if (response.status === 401 || ['SESSION_REVOKED','DEVICE_REVOKED','UNAUTHORIZED'].includes(result.code)) await this.lock(true);
      throw Error(result.code || 'SYNC_UNAVAILABLE');
    }
    return result.data;
  }
  async initialize() {
    const epoch = this.epoch;
    await this.journal.initialize();
    await this.request('/v1/devices', 'POST', {});
    const current = await this.request('/v1/keyring');
    if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
    this.ring = current.keyring; this.version = current.version;
    const remembered = await this.store.get(this.storageKey);
    if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
    if (remembered && this.ring) {
      try {
        const saved = JSON.parse(remembered), key = unb64(saved.key);
        if (saved.version !== this.version || key.length !== 32) { key.fill(0); throw Error('STALE_DEVICE_KEY'); }
        this.accept(key); key.fill(0);
      } catch { await this.remember(null); }
    }
    return { configured: !!this.ring, unlocked: !!this.master };
  }
  accept(key) { this.master?.fill(0); this.master = Uint8Array.from(key); this.engine.unlock(key); }
  remember(key) {
    const value = key ? JSON.stringify({version:this.version,key:b64(key)}) : null;
    const epoch = this.epoch;
    this.writes = this.writes.catch(() => undefined).then(async () => {
      if (value && epoch !== this.epoch) return;
      if (value) await this.store.set(this.storageKey,value);
      else await this.store.delete(this.storageKey);
    });
    return this.writes;
  }
  async create(password, remember = false) {
    if (this.ring) throw Error('SYNC_ALREADY_CONFIGURED');
    const epoch = this.epoch, created = await createKeyring(this.user,password);
    try {
      if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
      const result = await this.request('/v1/keyring','PUT',{expected_version:0,keyring:created.keyring});
      if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
      this.ring = created.keyring; this.version = result.version;
      this.accept(created.master); await this.remember(remember ? created.master : null);
      if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
      return created.recovery;
    } finally { created.master.fill(0); }
  }
  async unlock(secret, mode = 'password', remember = false) {
    if (!this.ring) throw Error('SYNC_NOT_CONFIGURED');
    if (!['password','recovery'].includes(mode)) throw Error('INVALID_UNLOCK_MODE');
    const epoch = this.epoch;
    const key = mode === 'recovery' ? unlockRecovery(this.ring,secret,this.user) : await unlockPassword(this.ring,secret,this.user);
    try {
      if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
      this.accept(key); await this.remember(remember ? key : null);
      if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
    } finally { key.fill(0); }
  }
  async changePassword(password, remember = false) {
    if (!this.master || !this.ring) throw Error('SYNC_LOCKED');
    const epoch = this.epoch, key = Uint8Array.from(this.master);
    try {
      const ring = await changePassword(this.ring,key,password);
      if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
      const result = await this.request('/v1/keyring','PUT',{expected_version:this.version,keyring:ring});
      if (epoch !== this.epoch) throw Error('SYNC_CANCELLED');
      this.ring = ring; this.version = result.version;
      await this.remember(remember ? key : null);
    } finally { key.fill(0); }
  }
  async lock(forget = false) {
    this.epoch++; this.controller.abort(); this.controller = new AbortController();
    this.engine.lock(); this.master?.fill(0); this.master = null;
    if (forget) await this.remember(null);
  }
  objectId(kind, localId) {
    if (!['conversation','message','attachment','knowledge','provider','configuration'].includes(kind)
      || typeof localId !== 'string' || !localId || localId.length > 512) throw Error('INVALID_OBJECT');
    return bytesToHex(sha256(new TextEncoder().encode(JSON.stringify(['omniapi-object',this.user,kind,localId]))));
  }
  async put(kind, localId, value, parent = null, deleted = false) {
    if (!this.master) throw Error('SYNC_LOCKED');
    const object = this.objectId(kind,localId);
    const heads = await this.journal.heads(object);
    if (heads.length > 1 && parent === null) throw Error('CONFLICT_REQUIRES_SELECTION');
    if (parent !== null && !heads.some(head => head.revision === parent)) throw Error('INVALID_PARENT');
    return this.engine.queue(object,parent ?? heads[0]?.revision ?? null,{v:1,kind,localId,value},deleted);
  }
  async list({ limit = 100, offset = 0, deleted = false } = {}) {
    if (!this.master) throw Error('SYNC_LOCKED');
    const epoch = this.epoch;
    const rows = await this.journal.objects(limit,offset);
    if (epoch !== this.epoch || !this.master) throw Error('SYNC_CANCELLED');
    return rows.filter(row => !!row.deleted === deleted).map(row => ({ object:row.object,revision:row.revision,
      parent:row.parent,deleted:!!row.deleted,pending:!!row.pending,
      value:openRecord(this.master,this.user,{...row,deleted:!!row.deleted,envelope:JSON.parse(row.envelope)}) }));
  }
  async sync() {
    if (!this.master) throw Error('SYNC_LOCKED');
    try { await this.engine.run(); }
    catch (error) {
      if (error.status === 401 || ['SESSION_REVOKED','DEVICE_REVOKED','UNAUTHORIZED','AUTHORIZATION_REVOKED'].includes(error.message)) await this.lock(true);
      throw error;
    }
  }
  async status() { return this.request('/v1/status'); }
}
