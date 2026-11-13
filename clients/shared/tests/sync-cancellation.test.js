import test from 'node:test';
import assert from 'node:assert/strict';
import { SyncEngine } from '../sync.js';

test('locking during token acquisition prevents even the first transport request', async () => {
  let release, entered;
  const waiting = new Promise(resolve => { release = resolve; });
  const started = new Promise(resolve => { entered = resolve; });
  let requests = 0;
  const engine = new SyncEngine({ user: 1, device: 'synthetic-device', adapter: {},
    token: async () => { entered(); await waiting; return { url: 'https://omniapi.biz/api/sync', token: 'synthetic-token' }; },
    fetch: async () => { requests++; return { ok: true, json: async () => ({ success: true, data: {} }) }; },
  });
  engine.unlock(new Uint8Array(32));
  const syncing = engine.run();
  const failed = assert.rejects(syncing, /SYNC_CANCELLED/);
  await started; engine.lock(); release(); await failed;
  assert.equal(requests, 0);
});
