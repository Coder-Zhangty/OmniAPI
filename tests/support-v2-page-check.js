'use strict';
const assert=require('assert');const fs=require('fs');const path=require('path');const {JSDOM}=require('jsdom');
const root=path.resolve(__dirname,'..');const read=p=>fs.readFileSync(path.join(root,p),'utf8');const tick=ms=>new Promise(r=>setTimeout(r,ms||20));
function response(status,payload){return Promise.resolve({ok:status>=200&&status<300,status,json:()=>Promise.resolve(payload),blob:()=>Promise.resolve(new Blob())});}
async function user(fetchImpl){const dom=new JSDOM(read('nginx/brand/support.html'),{url:'https://omniapi.biz/brand/support.html',runScripts:'outside-only'});dom.window.fetch=fetchImpl;dom.window.open=()=>{};dom.window.eval(read('nginx/brand/support-v2.js'));await tick(80);return dom.window;}
async function admin(fetchImpl){const dom=new JSDOM(read('nginx/brand/support-admin.html'),{url:'https://omniapi.biz/brand/support-admin.html',runScripts:'outside-only'});dom.window.fetch=fetchImpl;dom.window.eval(read('nginx/brand/support-admin-v2.js'));await tick(80);return dom.window;}
(async()=>{
 console.log('[1] 用户未登录时显示明确提示');
 let w=await user(url=>String(url).includes('/auth/refresh')?response(200,{data:{}}):response(404,{}));
 assert.match(w.document.body.textContent,/请先登录/);

 console.log('[2] 工单列表和消息以 textContent 渲染');
 const evil='<img src=x onerror=alert(1)>';
 const ticket={id:'S-20260929-abc123',subject:evil,category:'other',priority:'normal',status:'waiting_user',last_message_at:'2026-09-29T00:00:00Z',unread_user:1};
 w=await user(url=>{url=String(url);if(url.includes('/auth/refresh'))return response(200,{data:{access_token:'memory-only'}});if(url.endsWith('/config'))return response(200,{enabled:true,categories:[],greeting:'欢迎'});if(url.includes('/v2/ticket?'))return response(200,{success:true,ticket,messages:[{seq:1,role:'operator',text:evil,created_at:'2026-09-29T00:00:00Z'}]});if(url.includes('/v2/tickets'))return response(200,{success:true,tickets:[ticket],unread:1});if(url.includes('/v2/read'))return response(200,{success:true});return response(404,{});});
 assert.strictEqual(w.document.querySelectorAll('img[src="x"]').length,0);assert.ok(w.document.body.textContent.includes(evil));

 console.log('[3] 未发送草稿只进入 sessionStorage');
 const input=w.document.getElementById('message-text');input.value='未发送草稿';input.dispatchEvent(new w.Event('input'));
 assert.strictEqual(w.sessionStorage.getItem('omniapi-support-draft-'+ticket.id),'未发送草稿');assert.strictEqual(w.localStorage.length,0);

 console.log('[4] 管理写请求带 CSRF 且内部备注不会走回复接口');
 const writes=[];let sessionCalls=0;
 const aw=await admin((url,opt={})=>{url=String(url);if(url.endsWith('/session')&&(!opt.method||opt.method==='GET')){sessionCalls++;return response(200,{success:true,authenticated:true,csrf_token:'csrf-test'});}if(url.endsWith('/settings'))return response(200,{success:true,quick_replies:[],ai:{enabled:false}});if(url.includes('/v2/tickets'))return response(200,{success:true,tickets:[ticket],stats:{}});if(url.includes('/v2/ticket?'))return response(200,{success:true,ticket:Object.assign({uid:7,username:'u'},ticket),messages:[],context:{uid:7,username:'u',tickets:[]}});if(url.endsWith('/read'))return response(200,{success:true});if(opt.method==='POST'){writes.push({url,opt});return response(201,{success:true,note:{}});}return response(404,{});});
 await tick(30);const first=aw.document.querySelector('.desk-ticket');assert.ok(first);first.click();await tick(40);const area=aw.document.querySelector('.composer textarea');area.value='内部判断';Array.from(aw.document.querySelectorAll('.composer button')).find(b=>b.textContent.includes('内部备注')).click();await tick(30);assert.ok(writes.some(x=>x.url.endsWith('/v2/note')));assert.ok(writes.every(x=>x.opt.headers['X-CSRF-Token']==='csrf-test'));

 console.log('[5] 源码不使用 innerHTML 渲染服务端字段，令牌不持久化');
 for(const file of ['nginx/brand/support-v2.js','nginx/brand/support-admin-v2.js','nginx/brand/support-widget-v2.js']){const source=read(file);assert.ok(!/localStorage\.setItem/.test(source),file);assert.ok(!/\.innerHTML\s*=/.test(source),file);}
 console.log('PASS: OmniAPI 客服 v2 前端检查通过');
})().catch(error=>{console.error(error);process.exitCode=1;});
