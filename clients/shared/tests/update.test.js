import test from 'node:test';
import assert from 'node:assert/strict';
import { ed25519 } from '@noble/curves/ed25519.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex } from '@noble/hashes/utils.js';
import { b64 } from '../crypto.js';
import { verifyManifest, verifyPackage, verifyPackageStream } from '../update.js';

test('updates require a pinned signature, trusted mirrors, newer version and matching bytes',()=>{
  const key=new Uint8Array(32).fill(3), publicKey=b64(ed25519.getPublicKey(key));
  const bytes=new Uint8Array([1,2,3]);
  const release={platform:'windows-x64',version:'0.1.1',accepted:true,bytes:3,sha256:bytesToHex(sha256(bytes)),primary:'https://omniapi.biz/downloads/a.exe',mirror:'https://backup.omniapi.biz/downloads/a.exe',source:'https://omniapi.biz/downloads/source.zip'};
  const sign=body=>{const payload=new TextEncoder().encode(JSON.stringify(body));return{v:1,payload:b64(payload),signature:b64(ed25519.sign(payload,key))};};
  const signed=sign({v:1,releases:[release]});
  assert.deepEqual(verifyManifest(signed,publicKey,'windows-x64','0.1.0'),release);
  assert.equal(verifyManifest(signed,publicKey,'windows-x64','0.1.1'),null);
  assert.throws(()=>verifyManifest({...signed,signature:b64(new Uint8Array(64))},publicKey,'windows-x64','0.1.0'));
  assert.throws(()=>verifyManifest(sign({v:1,releases:[{...release,primary:'https://evil.example/downloads/a.exe'}]}),publicKey,'windows-x64','0.1.0'));
  assert.equal(verifyPackage(bytes,release),true);
  assert.throws(()=>verifyPackage(new Uint8Array([1,2,4]),release));
});

test('streaming package verification rejects truncated, oversized and corrupted downloads', async()=>{
  const bytes=Uint8Array.from({length:2097171},(_,i)=>i%251);
  const release={bytes:bytes.length,sha256:bytesToHex(sha256(bytes))};
  async function* chunks(value) {
    for(let i=0;i<value.length;i+=1048576) yield value.subarray(i,i+1048576);
  }
  assert.equal(await verifyPackageStream(chunks(bytes),release),true);
  await assert.rejects(verifyPackageStream(chunks(bytes.subarray(0,bytes.length-1)),release),/PACKAGE_HASH_MISMATCH/);
  await assert.rejects(verifyPackageStream(chunks(bytes),{...release,bytes:bytes.length-1}),/PACKAGE_HASH_MISMATCH/);
  const corrupt=bytes.slice(); corrupt[1048577]^=1;
  await assert.rejects(verifyPackageStream(chunks(corrupt),release),/PACKAGE_HASH_MISMATCH/);
  async function* invalid(){ yield 'untrusted'; }
  await assert.rejects(verifyPackageStream(invalid(),release),/INVALID_PACKAGE_STREAM/);
});
