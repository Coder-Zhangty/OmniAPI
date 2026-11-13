import { ed25519 } from '@noble/curves/ed25519.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex } from '@noble/hashes/utils.js';
import { unb64 } from './crypto.js';

export function verifyManifest(signed, publicKey, platform, current) {
  if (!signed || signed.v !== 1 || typeof signed.payload !== 'string' || signed.payload.length > 200000) throw Error('INVALID_MANIFEST');
  const bytes = unb64(signed.payload), signature = unb64(signed.signature);
  if (!ed25519.verify(signature, bytes, unb64(publicKey))) throw Error('MANIFEST_SIGNATURE_INVALID');
  const body = JSON.parse(new TextDecoder('utf-8', {fatal:true}).decode(bytes));
  if (body.v !== 1 || !Array.isArray(body.releases)) throw Error('INVALID_MANIFEST');
  const release = body.releases.find(row => row.platform === platform && row.accepted === true);
  if (!release) return null;
  if (!/^\d+\.\d+\.\d+$/.test(release.version) || !/^[a-f0-9]{64}$/.test(release.sha256) || !Number.isSafeInteger(release.bytes) || release.bytes <= 0) throw Error('INVALID_RELEASE');
  for (const link of [release.primary,release.mirror,release.source]) {
    const target = new URL(link);
    if (target.protocol !== 'https:' || !['omniapi.biz','backup.omniapi.biz'].includes(target.hostname) || !target.pathname.startsWith('/downloads/') || target.username || target.password) throw Error('UNTRUSTED_DOWNLOAD');
  }
  const a=release.version.split('.').map(Number), b=current.split('.').map(Number);
  if (b.length!==3 || !b.every(Number.isSafeInteger)) throw Error('INVALID_CURRENT_VERSION');
  for(let i=0;i<3;i++){if(a[i]<b[i])return null;if(a[i]>b[i])return release;}
  return null;
}
export function verifyPackage(bytes, release) {
  if(bytes.length!==release.bytes || bytesToHex(sha256(bytes))!==release.sha256)throw Error('PACKAGE_HASH_MISMATCH');
  return true;
}
export async function verifyPackageStream(chunks, release) {
  const hash = sha256.create(); let size = 0;
  for await (const chunk of chunks) {
    if (!(chunk instanceof Uint8Array)) throw Error('INVALID_PACKAGE_STREAM');
    size += chunk.length;
    if (size > release.bytes) throw Error('PACKAGE_HASH_MISMATCH');
    hash.update(chunk);
  }
  if (size !== release.bytes || bytesToHex(hash.digest()) !== release.sha256) throw Error('PACKAGE_HASH_MISMATCH');
  return true;
}
