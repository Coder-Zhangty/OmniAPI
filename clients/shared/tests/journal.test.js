import test from 'node:test';
import assert from 'node:assert/strict';
import { DatabaseSync } from 'node:sqlite';
import { EncryptedJournal } from '../journal.js';
import { sealRecord, newId } from '../crypto.js';
import { SyncEngine } from '../sync.js';

function driver(db) {
  const tx={run:async(sql,p)=>db.prepare(sql).run(...p),get:async(sql,p)=>db.prepare(sql).get(...p),all:async(sql,p)=>db.prepare(sql).all(...p)};
  return {...tx,transaction:async action=>{db.exec('BEGIN IMMEDIATE');try{const result=await action(tx);db.exec('COMMIT');return result;}catch(e){db.exec('ROLLBACK');throw e;}}};
}
test('encrypted journal survives restart, isolates users and rolls back a partial batch',async()=>{
  const db=new DatabaseSync(':memory:');const a=new EncryptedJournal(driver(db),1),b=new EncryptedJournal(driver(db),2);
  await a.initialize();await b.initialize();
  const key=new Uint8Array(32).fill(7),object=newId(),revision=newId();
  const record={object,revision,parent:null,deleted:false,envelope:sealRecord(key,1,object,revision,null,false,{text:'私密对话'})};
  await a.enqueue(record);
  const restarted=new EncryptedJournal(driver(db),1);
  assert.equal((await restarted.outbox()).length,1);assert.equal((await b.outbox()).length,0);
  assert.equal(JSON.stringify(db.prepare('SELECT * FROM omni_sync_journal').all()).includes('私密对话'),false);
  const first={...record,cursor:1};
  const broken={...record,revision:newId(),cursor:3};
  await assert.rejects(a.applyBatch([{record:first,value:{text:'私密对话'}},{record:broken,value:{}}],2),/INVALID_CHANGE/);
  assert.equal(await a.cursor(),0);assert.equal((await a.outbox()).length,1);
  await a.applyBatch([{record:first,value:{text:'私密对话'}}],1);
  assert.equal(await a.cursor(),1);assert.equal((await a.outbox()).length,0);
  await b.enqueue({...record,revision:newId()});await a.clear();assert.equal((await b.outbox()).length,1);
  db.close();
});
test('an expired subscription downloads changes without flushing pending uploads',async()=>{
  const calls=[];let applied=false;
  const engine=new SyncEngine({user:1,device:newId(),token:async()=>({url:'https://omniapi.biz/api/sync',token:'test'}),
    adapter:{outbox:async()=>{throw Error('must not read upload queue');},cursor:async()=>0,applyBatch:async()=>{applied=true;}},
    fetch:async url=>{calls.push(url);return {ok:true,json:async()=>({success:true,data:url.endsWith('/status')?{read_only:true}:{changes:[],cursor:0,has_more:false}})};}});
  engine.unlock(new Uint8Array(32));await engine.run();engine.lock();
  assert.equal(applied,true);assert.equal(calls.some(url=>url.includes('/objects/')),false);
});
