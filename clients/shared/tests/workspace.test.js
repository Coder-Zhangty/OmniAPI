import test from 'node:test';
import assert from 'node:assert/strict';
import { DatabaseSync } from 'node:sqlite';
import { SyncWorkspace, EncryptedJournal, newId } from '../index.js';

const password = 'Independent-test-sync-password';
function makeDevice(server, user = 1) {
  const db = new DatabaseSync(':memory:');
  const tx = {run:async(s,p)=>db.prepare(s).run(...p),get:async(s,p)=>db.prepare(s).get(...p),all:async(s,p)=>db.prepare(s).all(...p)};
  let chain = Promise.resolve();
  const driver = {...tx,transaction(action) {
    const result = chain.then(async()=>{db.exec('BEGIN IMMEDIATE');try{const out=await action(tx);db.exec('COMMIT');return out;}catch(error){db.exec('ROLLBACK');throw error;}});
    chain=result.catch(()=>undefined); return result;
  }};
  const memory = new Map();
  const options = {user,device:newId(),journal:new EncryptedJournal(driver,user),
    secureStore:{get:async k=>memory.get(k)||null,set:async(k,v)=>{memory.set(k,v);},delete:async k=>{memory.delete(k);}},
    token:async()=>({url:'https://omniapi.biz/api/sync',token:String(user)}),
    fetch:server.fetch};
  return {workspace:new SyncWorkspace(options),options,db,memory};
}
function server() {
  const users = new Map(), bodies=[];
  return {bodies,async fetch(url, options) {
    const user=options.headers.Authorization.split(' ')[1];
    const data=users.get(user)||{ring:null,version:0,records:[],cursor:0};users.set(user,data);
    const path=new URL(url).pathname.replace('/api/sync','');
    const body=options.body?JSON.parse(options.body):null;
    bodies.push({path,body});
    const okay=value=>({ok:true,json:async()=>({success:true,data:value})});
    if(path==='/v1/devices')return okay({});
    if(path==='/v1/status')return okay({read_only:false,quota_bytes:1024**3});
    if(path==='/v1/keyring'){
      if(options.method==='PUT'){
        if(body.expected_version!==data.version)return {ok:false,json:async()=>({success:false,code:'KEYRING_CONFLICT'})};
        data.ring=body.keyring;data.version++;return okay({version:data.version});
      }
      return okay({version:data.version,keyring:data.ring});
    }
    if(path.startsWith('/v1/objects/')){
      const [, , ,object,,revision]=path.split('/');
      if(!data.records.some(r=>r.revision===revision))data.records.push({...body,object,revision,cursor:++data.cursor});
      const conflict=data.records.filter(r=>r.object===object&&r.parent===body.parent).length>1;
      return okay({conflict});
    }
    if(path==='/v1/changes'){
      const after=Number(new URL(url).searchParams.get('after'));
      return okay({changes:data.records.filter(r=>r.cursor>after),cursor:data.cursor,has_more:false});
    }
    throw Error('unexpected request');
  }};
}
test('two devices recover encrypted records, preserve concurrent branches and restore a deletion',async()=>{
  const cloud=server(),a=makeDevice(cloud),b=makeDevice(cloud),other=makeDevice(cloud,2);
  await a.workspace.initialize();const recovery=await a.workspace.create(password);
  assert.equal(a.memory.size,0,'remember device defaults to off');
  await a.workspace.put('message','local-message',{text:'合成中文对话',apiKey:'synthetic-channel-secret'});
  await a.workspace.sync();
  await b.workspace.initialize();await b.workspace.unlock(recovery,'recovery');await b.workspace.sync();
  const initial=await b.workspace.list();assert.equal(initial[0].value.value.text,'合成中文对话');
  await a.workspace.put('message','local-message',{text:'电脑离线修改'});
  await b.workspace.put('message','local-message',{text:'手机离线修改'});
  await a.workspace.sync();await b.workspace.sync();await a.workspace.sync();
  const branches=await a.workspace.list();assert.equal(branches.length,2);
  await assert.rejects(a.workspace.put('message','local-message',{}),/CONFLICT_REQUIRES_SELECTION/);
  const selected=branches[0];await a.workspace.put('message','local-message',selected.value.value,selected.revision,true);
  assert.equal((await a.workspace.list({deleted:true})).length,1);
  const deleted=(await a.workspace.list({deleted:true}))[0];
  await a.workspace.put('message','local-message',deleted.value.value,deleted.revision,false);
  assert.equal((await a.workspace.list({deleted:true})).length,0);
  await other.workspace.initialize();assert.equal(other.workspace.ring,null);
  assert.equal(JSON.stringify(cloud.bodies).includes('synthetic-channel-secret'),false);
  assert.equal(JSON.stringify(a.db.prepare('SELECT * FROM omni_sync_journal').all()).includes('合成中文对话'),false);
  await a.workspace.lock(true);await assert.rejects(a.workspace.list(),/SYNC_LOCKED/);
  for(const device of [a,b,other]){await device.workspace.lock(true);device.db.close();}
});
test('remembered key is protected by platform store, invalidated on rotation and removed by lock',async()=>{
  const cloud=server(),a=makeDevice(cloud),b=makeDevice(cloud);
  await a.workspace.initialize();await a.workspace.create(password,true);
  const restarted=new SyncWorkspace(a.options);assert.equal((await restarted.initialize()).unlocked,true);
  await b.workspace.initialize();await b.workspace.unlock(password);
  await b.workspace.changePassword('Changed-independent-sync-password');
  const stale=new SyncWorkspace(a.options);assert.equal((await stale.initialize()).unlocked,false);
  assert.equal(a.memory.size,0);
  await stale.unlock('Changed-independent-sync-password','password',true);
  assert.equal(a.memory.size,1);await stale.lock(true);assert.equal(a.memory.size,0);
  await a.workspace.lock(true);await restarted.lock(true);await b.workspace.lock(true);
  a.db.close();b.db.close();
});
test('logout during password derivation cannot resurrect sync keys',async()=>{
  const cloud=server(),a=makeDevice(cloud);
  await a.workspace.initialize();await a.workspace.create(password);
  const unlocking=a.workspace.unlock(password,'password',true);
  await a.workspace.lock(true);await assert.rejects(unlocking,/SYNC_CANCELLED/);
  assert.equal(a.memory.size,0);await assert.rejects(a.workspace.list(),/SYNC_LOCKED/);a.db.close();
});

test('HTTP 401 clears device keys even when the server gives no named error code',async()=>{
  const cloud=server(), original=cloud.fetch; let revoked=false;
  cloud.fetch=async(...args)=>revoked?{ok:false,status:401,json:async()=>({success:false})}:original(...args);
  const a=makeDevice(cloud);
  await a.workspace.initialize(); await a.workspace.create(password,true);
  assert.equal(a.memory.size,1); revoked=true;
  await assert.rejects(a.workspace.sync(),/SYNC_UNAVAILABLE/);
  assert.equal(a.workspace.master === null,true);
  assert.equal(a.memory.size,0);
  a.db.close();
});
