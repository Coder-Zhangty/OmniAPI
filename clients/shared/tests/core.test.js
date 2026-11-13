import test from 'node:test';
import assert from 'node:assert/strict';
import { createKeyring, unlockPassword, unlockRecovery, sealRecord, openRecord, newId, b64, unb64 } from '../crypto.js';
import { searchKnowledge, requireToolApproval } from '../knowledge.js';

test('base64 preserves Unicode payload bytes and rejects malformed encoding', () => {
  const bytes = new TextEncoder().encode('中文😀'); assert.deepEqual(unb64(b64(bytes)), bytes);
  assert.throws(() => unb64('bad!!'));
});
test('password and offline recovery unlock the same key; wrong credentials and tampering fail', async () => {
  const created = await createKeyring(123, 'Independent-test-password-123');
  assert.deepEqual(await unlockPassword(created.keyring, 'Independent-test-password-123', 123), created.master);
  assert.deepEqual(unlockRecovery(created.keyring, created.recovery, 123), created.master);
  await assert.rejects(unlockPassword(created.keyring, 'Wrong-test-password-123', 123));
  await assert.rejects(unlockPassword(created.keyring, 'Independent-test-password-123', 456));
  const object = newId(), revision = newId();
  const record = { object, revision, parent: null, deleted: false, envelope: sealRecord(created.master, 123, object, revision, null, false, { text: '中文回答😀', apiKey: 'synthetic-secret' }) };
  assert.deepEqual(openRecord(created.master, 123, record), { text: '中文回答😀', apiKey: 'synthetic-secret' });
  assert.throws(() => openRecord(created.master, 456, record));
  assert.throws(() => openRecord(created.master, 123, { ...record, deleted: true }));
  const bytes = unb64(record.envelope.ciphertext); bytes[0] ^= 1;
  assert.throws(() => openRecord(created.master, 123, { ...record, envelope: { ...record.envelope, ciphertext: b64(bytes) } }));
  assert.ok(!record.envelope.ciphertext.includes('synthetic-secret'));
  created.master.fill(0);
});
test('mobile can search a portable desktop index without an online desktop', () => {
  const index = { v: 1, id: 'test', dimensions: 2, chunks: [{ id: 'a', text: '猫', vector: [1, 0] }, { id: 'b', text: '狗', vector: [0, 1] }] };
  assert.equal(searchKnowledge(index, [1, 0], 1)[0].text, '猫');
  assert.throws(() => searchKnowledge(index, [1, 0, 0]));
});
test('unknown tools and mutations need single-use authorization', () => {
  const tool = { operationId: 'write-1', classification: 'write', withinGrantedScope: true };
  assert.throws(() => requireToolApproval(tool));
  const grant = { operationId: 'write-1' }; requireToolApproval(tool, grant);
  assert.throws(() => requireToolApproval(tool, grant));
  requireToolApproval({ classification: 'read-only', withinGrantedScope: true });
  assert.throws(() => requireToolApproval({ classification: 'unknown', withinGrantedScope: true }));
});
