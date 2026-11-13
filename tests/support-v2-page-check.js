'use strict';
const assert=require('assert');const fs=require('fs');const path=require('path');const {JSDOM}=require('jsdom');
const root=path.resolve(__dirname,'..');const read=p=>fs.readFileSync(path.join(root,p),'utf8');const tick=ms=>new Promise(r=>setTimeout(r,ms||20));
function response(status,payload){return Promise.resolve({ok:status>=200&&status<300,status,json:()=>Promise.resolve(payload),blob:()=>Promise.resolve(new Blob())});}
async function user(fetchImpl){const dom=new JSDOM(read('nginx/brand/support.html'),{url:'https://omniapi.biz/brand/support.html',runScripts:'outside-only',pretendToBeVisual:true});dom.window.refreshCallbacks=[];dom.window.setInterval=fn=>{dom.window.refreshCallbacks.push(fn);return 1;};dom.window.fetch=fetchImpl;dom.window.open=()=>{};dom.window.eval(read('nginx/brand/support-ui.js'));dom.window.eval(read('nginx/brand/support-v2.js'));await tick(80);return dom.window;}
async function admin(fetchImpl){const dom=new JSDOM(read('nginx/brand/support-admin.html'),{url:'https://omniapi.biz/brand/support-admin.html',runScripts:'outside-only',pretendToBeVisual:true});dom.window.refreshCallbacks=[];dom.window.setInterval=fn=>{dom.window.refreshCallbacks.push(fn);return 1;};dom.window.fetch=fetchImpl;dom.window.eval(read('nginx/brand/support-ui.js'));dom.window.eval(read('nginx/brand/support-admin-v2.js'));await tick(80);return dom.window;}
(async()=>{
 console.log('[1] 用户未登录时显示明确提示');
 let w=await user(url=>String(url).includes('/auth/refresh')?response(200,{data:{}}):response(404,{}));
 assert.match(w.document.body.textContent,/请先登录/);

 console.log('[2] 工单列表和消息以 textContent 渲染');
 const evil='<img src=x onerror=alert(1)>';
 const ticket={id:'S-20260929-abc123',subject:evil,category:'other',priority:'normal',status:'waiting_user',last_message_at:'2026-09-29T00:00:00Z',unread_user:1};
 let userMessages=[{seq:1,role:'operator',text:evil,created_at:'2026-09-29T00:00:00Z'}],userPosts=[];w=await user((url,opt={})=>{url=String(url);if(opt.method==='POST'&&url.endsWith('/message'))userPosts.push(JSON.parse(opt.body));if(url.includes('/auth/refresh'))return response(200,{data:{access_token:'memory-only'}});if(url.endsWith('/config'))return response(200,{enabled:true,categories:[],greeting:'欢迎'});if(url.includes('/v2/ticket?'))return response(200,{success:true,ticket,messages:userMessages});if(url.includes('/v2/tickets'))return response(200,{success:true,tickets:[ticket],unread:1});if(url.includes('/v2/read'))return response(200,{success:true});return response(404,{});});
 assert.strictEqual(w.document.querySelectorAll('img[src="x"]').length,0);assert.ok(w.document.body.textContent.includes(evil));

 console.log('[3] 未发送草稿只进入 sessionStorage');
 const input=w.document.getElementById('message-text');input.value='未发送草稿';input.dispatchEvent(new w.Event('input'));
 assert.strictEqual(w.sessionStorage.getItem('omniapi-support-draft-'+ticket.id),'未发送草稿');assert.strictEqual(w.localStorage.length,0);

 userMessages.push({seq:2,role:'operator',text:'新客服回复',created_at:'2026-09-29T00:01:00Z'});w.refreshCallbacks[0]();await tick(50);assert.strictEqual(w.document.getElementById('message-text'),input);assert.equal(input.value,'未发送草稿');assert.ok(w.document.getElementById('timeline').textContent.includes('新客服回复'));input.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',isComposing:true,bubbles:true}));input.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',shiftKey:true,bubbles:true}));await tick(10);assert.equal(userPosts.length,0);input.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',bubbles:true}));input.dispatchEvent(new w.KeyboardEvent('keydown',{key:'Enter',bubbles:true}));await tick(50);assert.equal(userPosts.length,1,'Enter 与并发发送保护');
 console.log('[4] 管理写请求带 CSRF 且内部备注不会走回复接口');
 const writes=[];let sessionCalls=0;
 const aw=await admin((url,opt={})=>{url=String(url);if(url.endsWith('/session')&&(!opt.method||opt.method==='GET')){sessionCalls++;return response(200,{success:true,authenticated:true,csrf_token:'csrf-test'});}if(url.endsWith('/settings'))return response(200,{success:true,quick_replies:[],ai:{enabled:false}});if(url.includes('/v2/tickets'))return response(200,{success:true,tickets:[ticket],stats:{}});if(url.includes('/v2/ticket?'))return response(200,{success:true,ticket:Object.assign({uid:7,username:'u'},ticket),messages:[],context:{uid:7,username:'u',tickets:[]}});if(url.endsWith('/read'))return response(200,{success:true});if(opt.method==='POST'){writes.push({url,opt});return response(201,{success:true,note:{}});}return response(404,{});});
 await tick(30);const first=aw.document.querySelector('.desk-ticket');assert.ok(first);first.click();await tick(40);const area=aw.document.querySelector('.composer textarea');area.value='内部判断';area.dispatchEvent(new aw.Event('input'));aw.refreshCallbacks[0]();await tick(50);assert.equal(aw.document.querySelector('.composer textarea'),area);assert.equal(area.value,'内部判断');Array.from(aw.document.querySelectorAll('.composer button')).find(b=>b.textContent==='内部备注').click();assert.equal(writes.length,0,'切换备注标签不发送内容');aw.document.getElementById('admin-send').click();await tick(30);assert.ok(writes.some(x=>x.url.endsWith('/v2/note')));assert.ok(writes.every(x=>x.opt.headers['X-CSRF-Token']==='csrf-test'));

 console.log('[5] 用户资料可展开关闭，设置支持焦点返回与 Escape');
 const contextButton=aw.document.querySelector('.context-toggle');assert.equal(contextButton.getAttribute('aria-expanded'),'false');contextButton.click();assert.equal(contextButton.getAttribute('aria-expanded'),'true');assert(aw.document.getElementById('desk-grid').classList.contains('context-open'));assert.equal(aw.document.activeElement.id,'context-close');
 aw.document.dispatchEvent(new aw.KeyboardEvent('keydown',{key:'Escape',bubbles:true}));assert.equal(contextButton.getAttribute('aria-expanded'),'false');assert.equal(aw.document.activeElement,contextButton);
 const settingsButton=aw.document.getElementById('settings-btn');settingsButton.focus();settingsButton.click();assert.equal(aw.document.getElementById('settings-pop').hidden,false);assert.equal(aw.document.activeElement.id,'set-hours');
 aw.document.dispatchEvent(new aw.KeyboardEvent('keydown',{key:'Tab',shiftKey:true,bubbles:true,cancelable:true}));assert.equal(aw.document.activeElement.id,'settings-cancel');
 aw.document.dispatchEvent(new aw.KeyboardEvent('keydown',{key:'Escape',bubbles:true}));assert.equal(aw.document.getElementById('settings-pop').hidden,true);assert.equal(aw.document.activeElement,settingsButton);
 assert.equal(aw.document.querySelector('.desk-ticket').getAttribute('aria-pressed'),'true');assert.equal(aw.document.querySelector('#admin-timeline').tabIndex,0);assert.equal(w.document.querySelector('#timeline').tabIndex,0);
 const log=w.document.getElementById('timeline');Object.defineProperties(log,{scrollHeight:{value:3000,configurable:true},clientHeight:{value:400,configurable:true}});log.scrollTop=400;userMessages.push({seq:3,role:'operator',text:'滚动位置验证',created_at:'2026-09-29T00:02:00Z'});w.refreshCallbacks[0]();await tick(50);assert.equal(log.scrollTop,400,'阅读历史时新增回复保留滚动位置');
 const wheel=new w.WheelEvent('wheel',{bubbles:true,cancelable:true,deltaY:120});log.dispatchEvent(wheel);assert.equal(wheel.defaultPrevented,false);
 console.log('[6] 源码不使用 innerHTML 渲染服务端字段，令牌不持久化');
 for(const file of ['nginx/brand/support-v2.js','nginx/brand/support-admin-v2.js','nginx/brand/support-widget-v2.js']){const source=read(file);assert.ok(!/localStorage\.setItem/.test(source),file);assert.ok(!/\.innerHTML\s*=/.test(source),file);}
 w.close();aw.close();console.log('PASS: OmniAPI 客服 v2 前端检查通过');
})().catch(error=>{console.error(error);process.exit(1);});
