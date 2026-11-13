import { gcm } from '@noble/ciphers/aes.js';
import { argon2idAsync } from '@noble/hashes/argon2.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex, hexToBytes, randomBytes } from '@noble/hashes/utils.js';

const text = new TextEncoder();
const decode = new TextDecoder('utf-8', { fatal: true });
export const KDF = Object.freeze({ name: 'argon2id', m: 65536, t: 3, p: 1 });
const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
export function b64(bytes) {
  let result = '';
  for (let i = 0; i < bytes.length; i += 3) {
    const a = bytes[i], b = bytes[i + 1] || 0, c = bytes[i + 2] || 0;
    result += alphabet[a >> 2] + alphabet[((a & 3) << 4) | (b >> 4)];
    result += i + 1 < bytes.length ? alphabet[((b & 15) << 2) | (c >> 6)] : '=';
    result += i + 2 < bytes.length ? alphabet[c & 63] : '=';
  }
  return result;
}
export function unb64(value) {
  if (typeof value !== 'string' || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value)) throw Error('INVALID_BASE64');
  const out = [];
  for (let i = 0; i < value.length; i += 4) {
    const a = alphabet.indexOf(value[i]), b = alphabet.indexOf(value[i + 1]);
    const c = alphabet.indexOf(value[i + 2]), d = alphabet.indexOf(value[i + 3]);
    out.push((a << 2) | (b >> 4));
    if (c >= 0) out.push(((b & 15) << 4) | (c >> 2));
    if (d >= 0) out.push(((c & 3) << 6) | d);
  }
  return Uint8Array.from(out);
}
const uid = value => {
  const str = String(value);
  if (!/^[1-9][0-9]{0,15}$/.test(str)) throw Error('INVALID_USER');
  return str;
};
export const newId = () => bytesToHex(randomBytes(16));
const aad = (...parts) => text.encode(JSON.stringify(['omniapi-sync', 1, ...parts]));
function encrypt(key, plaintext, associated) {
  const nonce = randomBytes(12);
  return { v: 1, nonce: b64(nonce), ciphertext: b64(gcm(key, nonce, associated).encrypt(plaintext)) };
}
function decrypt(key, wrapped, associated) {
  if (!wrapped || wrapped.v !== 1 || Object.keys(wrapped).sort().join(',') !== 'ciphertext,nonce,v') throw Error('INVALID_ENVELOPE');
  const nonce = unb64(wrapped.nonce), ciphertext = unb64(wrapped.ciphertext);
  if (nonce.length !== 12 || ciphertext.length < 16 || ciphertext.length > 2 * 1024 * 1024) throw Error('INVALID_ENVELOPE');
  return gcm(key, nonce, associated).decrypt(ciphertext);
}
async function passwordKey(password, salt) {
  if (typeof password !== 'string' || password.length < 16 || password.length > 256) throw Error('PASSWORD_REQUIRES_16_TO_256_CHARACTERS');
  const input = text.encode(password);
  try { return await argon2idAsync(input, salt, { m: KDF.m, t: KDF.t, p: KDF.p, dkLen: 32 }); }
  finally { input.fill(0); }
}
function recoveryKey(secret, user) {
  if (!/^OMNI-R1-[a-f0-9]{64}$/.test(secret)) throw Error('INVALID_RECOVERY_KEY');
  const raw = hexToBytes(secret.slice(8));
  try { return hkdf(sha256, raw, undefined, aad('recovery', uid(user)), 32); }
  finally { raw.fill(0); }
}
function validateRing(ring) {
  if (!ring || ring.v !== 1 || Object.keys(ring).sort().join(',') !== 'kdf,password_wrap,recovery_wrap,salt,uid,v') throw Error('INVALID_KEYRING');
  uid(ring.uid);
  if (ring.kdf?.name !== KDF.name || ring.kdf.m !== KDF.m || ring.kdf.t !== KDF.t || ring.kdf.p !== KDF.p) throw Error('UNSUPPORTED_KDF');
  const salt = unb64(ring.salt);
  if (salt.length !== 16) throw Error('INVALID_KEYRING');
  return salt;
}
export async function createKeyring(user, password) {
  const userId = uid(user), master = randomBytes(32), salt = randomBytes(16);
  const recovery = 'OMNI-R1-' + bytesToHex(randomBytes(32));
  let protect, recover;
  try {
    protect = await passwordKey(password, salt); recover = recoveryKey(recovery, userId);
    return { master, recovery, keyring: { v: 1, uid: userId, salt: b64(salt), kdf: { ...KDF }, password_wrap: encrypt(protect, master, aad('password-wrap', userId)), recovery_wrap: encrypt(recover, master, aad('recovery-wrap', userId)) } };
  } catch (error) { master.fill(0); throw error; }
  finally { protect?.fill(0); recover?.fill(0); }
}
export async function unlockPassword(ring, password, expectedUser) {
  const salt = validateRing(ring);
  if (uid(expectedUser) !== uid(ring.uid)) throw Error('USER_MISMATCH');
  const protect = await passwordKey(password, salt);
  try { const master = decrypt(protect, ring.password_wrap, aad('password-wrap', uid(ring.uid))); if (master.length !== 32) { master.fill(0); throw Error('INVALID_KEY'); } return master; }
  finally { protect.fill(0); }
}
export function unlockRecovery(ring, recovery, expectedUser) {
  validateRing(ring);
  if (uid(expectedUser) !== uid(ring.uid)) throw Error('USER_MISMATCH');
  const protect = recoveryKey(recovery, ring.uid);
  try { const master = decrypt(protect, ring.recovery_wrap, aad('recovery-wrap', uid(ring.uid))); if (master.length !== 32) { master.fill(0); throw Error('INVALID_KEY'); } return master; }
  finally { protect.fill(0); }
}
export async function changePassword(ring, master, password) {
  validateRing(ring);
  if (master.length !== 32) throw Error('INVALID_KEY');
  const salt = randomBytes(16), protect = await passwordKey(password, salt);
  try { return { ...ring, salt: b64(salt), password_wrap: encrypt(protect, master, aad('password-wrap', uid(ring.uid))) }; }
  finally { protect.fill(0); }
}
function recordKey(master, user, object, revision) {
  if (master.length !== 32 || ![object, revision].every(x => /^[A-Za-z0-9_-]{16,80}$/.test(x))) throw Error('INVALID_RECORD');
  return hkdf(sha256, master, undefined, aad('record-key', uid(user), object, revision), 32);
}
export function sealRecord(master, user, object, revision, parent, deleted, value) {
  const key = recordKey(master, user, object, revision);
  let plaintext;
  try {
    const serialized = JSON.stringify(value);
    if (serialized === undefined) throw Error('INVALID_CONTENT');
    plaintext = text.encode(serialized);
    if (plaintext.length > 1024 * 1024) throw Error('CHUNK_TOO_LARGE');
    return encrypt(key, plaintext, aad('record', uid(user), object, revision, parent, deleted));
  } finally { key.fill(0); plaintext?.fill(0); }
}
export function openRecord(master, user, record) {
  const key = recordKey(master, user, record.object, record.revision);
  let plaintext;
  try {
    plaintext = decrypt(key, record.envelope, aad('record', uid(user), record.object, record.revision, record.parent, record.deleted));
    return JSON.parse(decode.decode(plaintext));
  } finally { key.fill(0); plaintext?.fill(0); }
}
