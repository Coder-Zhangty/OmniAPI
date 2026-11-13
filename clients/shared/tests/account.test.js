import test from 'node:test';
import assert from 'node:assert/strict';
import { AccountClient } from '../account.js';
import { createHash } from 'node:crypto';

test('embedded website gets a PKCE ticket without creating another native login flow',async()=>{
  const calls=[];
  const client=new AccountClient({platform:'android',launch:async()=>{throw Error('MUST_STAY_EMBEDDED');},secureStore:{get:async()=>null},fetch:async(url,options)=>{
    calls.push({url,options});return {ok:true,json:async()=>({success:true,data:{request:'r'.repeat(32)}})};
  }});
  client.session={auth:{access_token:'native-synthetic',access_expires_at:Date.now()/1000+300}};
  const flow=await client.webLogin();
  assert.equal(calls.length,1);
  assert.equal(calls[0].url,'https://omniapi.biz/api/app/web-session/start');
  assert.equal(JSON.parse(calls[0].options.body).challenge,createHash('sha256').update(flow.verifier).digest('base64url'));
  assert.equal(flow.request,'r'.repeat(32));
  assert.equal(Object.keys(flow).length,2);
});

test('logout sends the current session identity before clearing local state', async()=>{
  let request;
  const client=new AccountClient({platform:'windows',launch:async()=>{},secureStore:{delete:async()=>{}},fetch:async(url,options)=>{
    request={url,body:JSON.parse(options.body)};
    return {ok:true,json:async()=>({success:true,data:{ok:true}})};
  }});
  client.session={user_id:1,auth:{access_token:'synthetic',session:{sid:'current-session'}}};
  await client.logout();
  assert.equal(request.body.sid,'current-session');
  assert.equal(client.session,null);
});

test('a delayed secure-store read cannot undo logout',async()=>{
  let read;
  const session={user_id:1,auth:{access_token:'old',access_expires_at:0,session:{sid:'session'}},refresh_token:'old-refresh'};
  const client=new AccountClient({platform:'windows',launch:async()=>{},secureStore:{get:async()=>new Promise(resolve=>{read=resolve;}),set:async()=>{},delete:async()=>{}}});
  const pending=client.load();await client.logout();read(JSON.stringify(session));
  assert.equal(await pending,null);assert.equal(client.session,null);
});

test('a late token refresh cannot restore a logged out account',async()=>{
  const data=new Map();const store={get:async k=>data.get(k)||null,set:async(k,v)=>data.set(k,v),delete:async k=>data.delete(k)};
  let resolve;
  const client=new AccountClient({secureStore:store,launch:async()=>{},platform:'android',fetch:async url=>{
    if(url.endsWith('/refresh'))await new Promise(r=>{resolve=r;});
    return {ok:true,json:async()=>({success:true,data:{auth:{access_token:'new',access_expires_at:Date.now()/1000+300,session:{sid:'session'}},refresh_token:'new-refresh'}})};
  }});
  client.session={user_id:1,auth:{access_token:'expired',access_expires_at:0,session:{sid:'session'}},refresh_token:'old-refresh'};
  const pending=client.access();await new Promise(r=>setTimeout(r,1));
  await client.logout();resolve();await assert.rejects(pending,/SESSION_CHANGED/);
  assert.equal(client.session,null);assert.equal(data.has('omniapi.native.session.v1'),false);
});
test('provider login cannot launch an attacker-controlled origin',async()=>{
  let launched=false;
  const client=new AccountClient({secureStore:{get:async()=>null,set:async()=>{},delete:async()=>{}},platform:'windows',launch:async()=>{launched=true;},fetch:async()=>({ok:true,json:async()=>({success:true,data:{authorize_url:'https://evil.example/brand/app-login.html',request:'bad'}})})});
  await assert.rejects(client.login(),/UNTRUSTED_LOGIN_ORIGIN/);assert.equal(launched,false);
});
