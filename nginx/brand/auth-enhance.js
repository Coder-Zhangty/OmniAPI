/* OmniAPI risk notice and scoped Turnstile metadata; credentials stay opaque. */
(()=>{'use strict';
 function wrap(ts){if(!ts||ts.__omniRiskAction)return;try{let render=ts.render;Object.defineProperty(ts,'render',{configurable:true,get(){return typeof render==='function'?function(target,options,...args){return render.call(ts,target,{...options,action:'omni_auth'},...args)}:render},set(fn){render=fn}});Object.defineProperty(ts,'__omniRiskAction',{value:true})}catch{}}
 // The early loader hook attaches action metadata after vendor initialization.
 // Predeclaring the global breaks Cloudflare's property-existence load guard.
 wrap(window.turnstile);
 if(!window.__omniRiskScriptLoading){window.__omniRiskScriptLoading=true;const s=document.createElement('script');s.src='/brand/risk-session.v20261028.js';s.defer=true;document.head.append(s)}
})();
/* Display-only Google/GitHub branding. OAuth identifiers and requests stay intact. */
(() => {
  'use strict';
  if (window.__omniProviderIdentityInstalled) return;
  window.__omniProviderIdentityInstalled = true;
  const assets = {
    google: '/brand/google-signin.v20261014-1.png',
    github: '/brand/github-invertocat-black.v20261014-1.svg',
  };
  let profile = null;
  let queued = false;
  const frame = window.requestAnimationFrame?.bind(window) || (callback => window.setTimeout(callback, 0));
  const style = document.createElement('style');
  style.textContent = '.omni-provider-logo{display:inline-block!important;width:20px!important;height:20px!important;min-width:20px!important;object-fit:contain!important;flex-shrink:0!important;vertical-align:middle!important}.omni-provider-logo~svg{display:none!important}.omni-provider-google-slot{background:#fff!important;border-radius:6px}.dark .omni-provider-github{filter:invert(1)}.omni-provider-account{overflow-wrap:anywhere;white-space:normal!important;overflow:visible!important;text-overflow:clip!important}';
  document.head.append(style);
  function queue() {
    if (queued) return;
    queued = true;
    frame(() => { queued = false; decorate(); });
  }
  function logo(parent, provider) {
    if (!parent) return;
    const existing = parent.querySelector(':scope > .omni-provider-logo');
    if (existing?.dataset.provider === provider) return;
    // Only replace the icon slot, retaining the original OAuth button/handler.
    // React retains ownership of its SVG nodes. Hide those via sibling CSS,
    // rather than deleting them and breaking later reconciliation.
    for (const child of parent.querySelectorAll(':scope > .omni-provider-logo')) child.remove();
    const img = document.createElement('img');
    img.className = 'omni-provider-logo omni-provider-' + provider;
    img.dataset.provider = provider;
    img.src = assets[provider];
    img.alt = '';
    img.setAttribute('aria-hidden', 'true');
    parent.prepend(img);
    if (provider === 'google' && !parent.matches('button')) parent.classList.add('omni-provider-google-slot');
  }
  function decorate() {
    if (/^\/(login|register|sign-in|sign-up)\/?$/.test(location.pathname)) {
      for (const button of document.querySelectorAll('button[type="button"]')) {
        const text = button.textContent.trim();
        if (!/继续|登录|Continue|Redirecting|Sign in|Log in/i.test(text)) continue;
        if (/\bGoogle\b/i.test(text)) logo(button, 'google');
        else if (/\bGitHub\b/i.test(text)) logo(button, 'github');
      }
    }
    for (const label of document.querySelectorAll('li p[title]')) {
      const name = label.getAttribute('title');
      const provider = name === 'OIDC' || name === 'Google' ? 'google' : name === 'GitHub' ? 'github' : '';
      if (!provider) continue;
      const cell = label.closest('li');
      const info = label.parentElement.parentElement;
      const icon = cell.firstElementChild?.firstElementChild;
      if (!info || !icon || !icon.querySelector('svg,.omni-provider-logo')) continue;
      logo(icon, provider);
      if (provider === 'google' && name !== 'Google') {
        label.textContent = 'Google';
        label.title = 'Google';
      }
      const value = info.querySelector(':scope > p');
      if (!value || !profile) continue;
      const field = provider === 'google' ? 'oidc' : 'github';
      const id = profile[field + '_id'];
      const account = profile[field + '_account'];
      if (!id || !account) continue;
      // Never project one user's cached email onto a different binding or an unbound row.
      const matches = value.textContent.trim() === id || value.dataset.providerBoundId === id;
      if (!matches) continue;
      if (value.textContent !== account) value.textContent = account;
      value.title = account;
      value.dataset.providerBoundId = id;
      value.classList.add('omni-provider-account');
    }
  }
  function capture(payload) {
    if (!payload?.success) return;
    const user = payload.data?.user || payload.data;
    if (!user || !Number.isInteger(user.id)) return;
    const next = { id: user.id };
    for (const key of ['oidc_id', 'github_id', 'oidc_account', 'github_account']) {
      next[key] = typeof user[key] === 'string' ? user[key] : '';
    }
    profile = next; // Deliberately excludes access tokens, cookies and unrelated profile data.
    queue();
  }
  function path(url) {
    try { return new URL(url, location.origin).pathname; } catch (_) { return ''; }
  }
  const isProfile = url => /^\/api\/user\/(self|login|auth\/(login|refresh))$/.test(path(url));
  const isLogout = url => /^\/api\/user\/(logout|auth\/logout)$/.test(path(url));
  const open = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function (method, url, ...rest) {
    if (isProfile(url)) this.addEventListener('load', () => {
      try { capture(this.responseType === 'json' ? this.response : JSON.parse(this.responseText)); } catch (_) {}
    }, { once: true });
    if (isLogout(url)) this.addEventListener('load', () => { profile = null; }, { once: true });
    return open.call(this, method, url, ...rest);
  };
  const fetch = window.fetch.bind(window);
  window.fetch = function (input, init) {
    const url = typeof input === 'string' || input instanceof URL ? String(input) : input.url;
    return fetch(input, init).then(response => {
      if (isProfile(url) && typeof response.clone === 'function') response.clone().json().then(capture).catch(() => {});
      if (isLogout(url) && response.ok) profile = null;
      return response;
    });
  };
  new MutationObserver(queue).observe(document.documentElement, { subtree: true, childList: true });
  for (const method of ['pushState', 'replaceState']) {
    const original = history[method];
    history[method] = function (...args) { const result = original.apply(this, args); queue(); return result; };
  }
  addEventListener('popstate', queue);
  queue();
})();

(()=>{'use strict';
const isAuth=()=>/^\/(login|register|sign-in|sign-up)\/?$/.test(location.pathname);
// 让其它注入脚本共用同一个「这是登录/注册页吗」的判断，别各自再写一份会走样的路径列表。
window.OmniAuth={isAuthPage:isAuth};
let lastCard=null,lastReset=0;
function emailInput(){return document.querySelector('input[name="username"],input#username,input[autocomplete="username"],input[placeholder*="用户名"]')}
function cleanup(){
 document.body.classList.remove('omni-auth');
 document.querySelectorAll('.omni-auth-card').forEach(el=>el.classList.remove('omni-auth-card'));
 document.querySelectorAll('.omni-auth-note,.omni-oauth,.omni-turnstile-visible,.omni-auth-aside').forEach(el=>el.remove());
 lastCard=null;
}
function refresh(){
 if(!isAuth()){cleanup();return}
 document.body.classList.add('omni-auth');
 const pwd=document.querySelector('input[type="password"]');if(!pwd)return;
 let card=pwd.parentElement;while(card&&card!==document.body){if(card.querySelectorAll('input').length>=2&&card.querySelector('button'))break;card=card.parentElement}
 if(card&&card!==document.body){card.classList.add('omni-auth-card');lastCard=card}
 const email=emailInput();if(email){email.type='email';email.autocomplete='email';email.inputMode='email';email.placeholder='邮箱地址';email.setAttribute('aria-label','邮箱地址')}
 if(/^\/(register|sign-up)\/?$/.test(location.pathname)&&lastCard&&!lastCard.querySelector('.omni-aff-field')){
  const form=lastCard.matches('form')?lastCard:lastCard.querySelector('form');
  if(form){
   const fromUrl=new URLSearchParams(location.search).get('aff')?.trim()||'';
   if(fromUrl)localStorage.setItem('aff',fromUrl);
   const field=document.createElement('div');field.className='omni-aff-field';
   const label=document.createElement('label');label.htmlFor='omni-aff-code';label.textContent='邀请码（选填）';
   const input=document.createElement('input');input.id='omni-aff-code';input.name='omni_aff_code';input.type='text';input.autocomplete='off';input.inputMode='text';input.maxLength=32;input.placeholder='没有邀请码可留空';input.value=localStorage.getItem('aff')||'';
   const hint=document.createElement('p');hint.textContent='填写有效邀请码后，邀请人可获得 ¥18 邀请奖励。';
   const save=()=>{const value=input.value.trim();input.value=value;if(value)localStorage.setItem('aff',value);else localStorage.removeItem('aff');const ok=!value||/^[A-Za-z0-9]{4,32}$/.test(value);input.setCustomValidity(ok?'':'邀请码应为 4–32 位字母或数字')};
   input.addEventListener('input',save);input.addEventListener('change',save);save();field.append(label,input,hint);
   // 邀请码属于注册来源信息，紧跟邮箱展示；放到两个密码框后面很容易被首屏忽略。
   let anchor=email||null;
   while(anchor&&anchor.parentElement!==form)anchor=anchor.parentElement;
   if(anchor)anchor.after(field);else{const submit=form.querySelector('button[type="submit"]');submit?form.insertBefore(field,submit):form.append(field)}
  }
 }
 document.querySelectorAll('label,span,p').forEach(el=>{if(el.children.length===0&&/用户名/.test(el.textContent))el.textContent=el.textContent.replace('用户名或电子邮件','邮箱').replace('用户名','邮箱')});
 if(lastCard&&!lastCard.querySelector('.omni-auth-note')){const note=document.createElement('p');note.className='omni-auth-note';note.textContent='可使用邮箱密码或已启用的快捷登录方式';lastCard.append(note)}
 // 快捷登录由原生 OAuthProviders 按后台开关渲染，沿用身份状态和账号绑定校验。
}
function validate(e){if(!isAuth())return;const email=emailInput();if(!email)return;const ok=/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.value.trim());email.setCustomValidity(ok?'':'请输入有效的邮箱地址');if(!ok){e.preventDefault();e.stopImmediatePropagation();email.reportValidity()}}
document.addEventListener('submit',validate,true);document.addEventListener('click',e=>{if(e.target.closest('button[type="submit"]'))validate(e)},true);
// 人机验证令牌单次有效：登录/注册请求结束后重置组件，避免拿旧令牌重试。
// **页面上只应该有 New API 自己渲染的那一个组件**：服务端就是从 query 参数 `turnstile`
// 读令牌的（middleware/turnstile-check.go: `c.Query("turnstile")`），而它自己的前端就是这么发的，
// 所以另渲染一个既没用、又会让登录页出现两个验证框（线上真出过）。reset() 不带参数即重置全部。
function resetTurnstile(){if(!isAuth()||Date.now()-lastReset<1800)return;lastReset=Date.now();try{if(window.turnstile?.reset)window.turnstile.reset()}catch(_){}}
// 只做一件事：登录/注册请求结束后重置人机验证组件。
// **不再改写请求 URL**：令牌由 New API 自己的前端放进 `?turnstile=`，我们再塞一个只会互相打架。
function installResetHook(){
 const originalOpen=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(method,url,...rest){const isAuthCall=/^\/api\/user\/(login|register)/.test(new URL(url,location.origin).pathname);if(isAuthCall)this.addEventListener('loadend',()=>{if(isAuth())setTimeout(resetTurnstile,50)},{once:true});return originalOpen.call(this,method,url,...rest)};
 const originalFetch=window.fetch.bind(window);window.fetch=(input,init)=>{const url=typeof input==='string'?input:input.url;return originalFetch(input,init).then(response=>{if(/^\/api\/user\/(login|register)/.test(new URL(url,location.origin).pathname)&&isAuth())setTimeout(resetTurnstile,50);return response})};
}
// 控制台的「退出登录」只撤销控制台自己的会话，**不会**动「充值核验 / 客服工作台」那张
// 8 小时的管理 cookie。结果是：退了控制台，别人在这台电脑上打开充值核验照样能真入账。
// 两个服务读同一个代际计数文件，所以这里调一次就把两边一起断掉（服务端 bump_generation）。
function revokeOperatorSessions(){
  try{window.sessionStorage.removeItem('omniapi.console_role')}catch(_){}
  return fetch('/brand/admin/api/session',{cache:'no-store',credentials:'same-origin'})
    .then(r=>r.ok?r.json():null)
    .then(payload=>{
      // 判据必须是 authenticated，不能是「有没有 csrf_token」：令牌是从 cookie 值算出来的，
      // 递一张已作废的 cookie 也会拿到一个非空令牌。
      if(!payload||!payload.authenticated) return null;   // 本来就没有有效的管理会话
      const token=payload.csrf_token;
      if(!token) return null;
      return fetch('/brand/admin/api/logout',{method:'POST',cache:'no-store',
        credentials:'same-origin',
        headers:{'Content-Type':'application/json','X-CSRF-Token':token},
        body:'{}'}).catch(()=>null);
    })
    .catch(()=>null);
}
// 挂在 XHR 与 fetch 上：SPA 用的是 axios(XHR)，但两种都拦着更稳。
function installLogoutHook(){
  const isLogout=url=>/^\/api\/user\/(auth\/logout|logout)$/.test(new URL(url,location.origin).pathname);
  const originalOpen=XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open=function(method,url,...rest){if(isLogout(url))this.addEventListener('loadend',()=>{revokeOperatorSessions()},{once:true});return originalOpen.call(this,method,url,...rest)};
  const originalFetch=window.fetch.bind(window);
  window.fetch=(input,init)=>{const url=typeof input==='string'?input:input.url;return originalFetch(input,init).then(response=>{if(isLogout(url))revokeOperatorSessions();return response})};
}
function sync(){refresh()}
new MutationObserver(ms=>{sync();if(!isAuth())return;const text=ms.flatMap(m=>[...m.addedNodes]).map(n=>n.textContent||'').join(' ');if(/登录失败|注册失败|密码错误|Turnstile|invalid|incorrect/i.test(text))resetTurnstile()}).observe(document.documentElement,{subtree:true,childList:true});
for(const method of ['pushState','replaceState']){const original=history[method];history[method]=function(...args){const result=original.apply(this,args);queueMicrotask(sync);return result}}
addEventListener('popstate',()=>queueMicrotask(sync));
installResetHook();installLogoutHook();sync();
})();
