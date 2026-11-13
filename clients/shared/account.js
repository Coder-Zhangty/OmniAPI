import { sha256 } from '@noble/hashes/sha2.js';
import { b64, newId } from './crypto.js';

const ORIGIN = 'https://omniapi.biz';
const STORAGE_KEY = 'omniapi.native.session.v1';
const DEVICE_KEY = 'omniapi.native.device.v1';
const encode = bytes => b64(bytes).replaceAll('+','-').replaceAll('/','_').replaceAll('=','');
export class AccountClient {
  constructor({ secureStore, launch, platform, fetch: http = globalThis.fetch }) {
    if (!['windows','android'].includes(platform)) throw Error('UNSUPPORTED_PLATFORM');
    this.store = secureStore; this.launch = launch; this.platform = platform; this.http = http;
    this.session = null; this.refreshing = null; this.storageQueue = Promise.resolve(); this.epoch = 0;
  }
  async persist(snapshot) {
    this.storageQueue = this.storageQueue.catch(() => {}).then(async () => {
      if (snapshot === this.session && snapshot !== null) await this.store.set(STORAGE_KEY,JSON.stringify(snapshot));
      if (snapshot === null) await this.store.delete(STORAGE_KEY);
    });
    return this.storageQueue;
  }
  async load() {
    const epoch = this.epoch;
    const raw = await this.store.get(STORAGE_KEY);
    if (epoch !== this.epoch) return this.session?.user_id || null;
    if (raw) {
      try { const parsed = JSON.parse(raw); if (!parsed.auth?.access_token || !parsed.auth?.session?.sid || !parsed.refresh_token || !Number.isInteger(parsed.user_id) || parsed.user_id <= 0) throw Error(); this.session = parsed; }
      catch { await this.store.delete(STORAGE_KEY); }
    }
    return this.session?.user_id || null;
  }
  async device() {
    let id = await this.store.get(DEVICE_KEY);
    if (!id) { id = newId(); await this.store.set(DEVICE_KEY, id); }
    return id;
  }
  async call(path, body, token, signal) {
    if (!path.startsWith('/api/')) throw Error('UNTRUSTED_API_PATH');
    const response = await this.http(ORIGIN + path, { method: body === undefined ? 'GET' : 'POST', redirect:'error', signal, headers: { 'Content-Type':'application/json', ...(token ? { Authorization:`Bearer ${token}` } : {}) }, ...(body === undefined ? {} : {body:JSON.stringify(body)}) });
    const payload = await response.json();
    if (!response.ok || payload.success !== true) { const error = Error(payload.code || 'REQUEST_FAILED'); error.status = response.status; throw error; }
    return payload.data;
  }
  async login(signal) {
    const epoch = ++this.epoch;
    const verifier = newId() + newId();
    const challenge = encode(sha256(new TextEncoder().encode(verifier)));
    const device = await this.device();
    const flow = await this.call('/api/app/login/start', { challenge, device, platform:this.platform }, undefined, signal);
    const target = new URL(flow.authorize_url);
    if (target.origin !== ORIGIN || target.pathname !== '/brand/app-login.html') throw Error('UNTRUSTED_LOGIN_ORIGIN');
    // Browser launch failure must stop the flow, rather than leave a rejected
    // promise while polling an authorization page the user never saw.
    await this.launch(target.href);
    const deadline = Date.now() + 5 * 60 * 1000;
    while (Date.now() < deadline) {
      if (signal?.aborted) throw Error('LOGIN_CANCELLED');
      try {
        const session = await this.call('/api/app/login/exchange', {request:flow.request,verifier},undefined,signal);
        if (!session.auth?.access_token || !session.refresh_token || session.device !== device) throw Error('INVALID_LOGIN_RESPONSE');
        if (epoch !== this.epoch || signal?.aborted) throw Error('LOGIN_CANCELLED');
        this.session = session; await this.persist(session); return session.user_id;
      } catch (error) { if (error.message !== 'AUTHORIZATION_PENDING') throw error; }
      await new Promise(resolve => setTimeout(resolve,2000));
    }
    throw Error('LOGIN_EXPIRED');
  }
  async access() {
    if (!this.session) throw Error('LOGIN_REQUIRED');
    if (this.session.auth.access_expires_at > Date.now()/1000+60) return this.session.auth.access_token;
    if (!this.refreshing) this.refreshing = (async () => {
      const current = this.session;
      const refreshed = await this.call('/api/app/login/refresh',{refresh_token:current.refresh_token,sid:current.auth.session.sid});
      // An in-flight refresh cannot resurrect an account after logout.
      if (this.session !== current) throw Error('SESSION_CHANGED');
      this.session = {...current,...refreshed}; await this.persist(this.session);
      return this.session.auth.access_token;
    })().finally(() => {this.refreshing=null;});
    return this.refreshing;
  }
  async request(path,body) { return this.call(path,body,await this.access()); }
  async syncToken(device) { return this.request('/api/app/sync/token',{device}); }
  async webLogin() {
    const verifier = newId() + newId();
    const challenge = encode(sha256(new TextEncoder().encode(verifier)));
    const flow = await this.request('/api/app/web-session/start',{challenge});
    return {request:flow.request,verifier};
  }
  async logout() {
    this.epoch++;
    const token = this.session?.auth?.access_token;
    const sid = this.session?.auth?.session?.sid;
    const user = this.session?.user_id;
    this.session = null;
    await this.persist(null);
    await this.store.delete('omniapi.sync.remembered-key.v1');
    if (user) await this.store.delete(`omniapi.sync.remembered-key.v1.${user}`);
    if (token) await this.call('/api/app/login/logout',{sid},token);
  }
}
