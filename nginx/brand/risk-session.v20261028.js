(()=>{'use strict';if(window.__omniRiskSession)return;window.__omniRiskSession=true;
 const nativeFetch=window.fetch.bind(window);let dialog=null,commerce=null,seen=new Set(),polling=false;
 function reviewNotice(data){const n=data?.notices?.find(n=>!n.acknowledged_at&&!n.legacy_read&&!seen.has(n.id));if(n&&show(n.title,n.body+(data.reason_category?"\n原因类别："+data.reason_category+"。"+(data.current_restriction||""):""),n.id,!data.review_pending)){seen.add(n.id);ack(n.id,{displayed:true})}}
 function show(title,body,id,requestOnly=false){if(dialog||document.hidden)return false;const d=document.createElement('dialog');d.setAttribute('aria-label',title);d.style.cssText='box-sizing:border-box;width:min(520px,calc(100% - 32px));max-height:calc(100vh - 32px);max-height:calc(100dvh - 32px);overflow:auto;border:1px solid #d7e8e0;border-radius:16px;padding:28px;color:#173b33;background:white;box-shadow:0 24px 80px #0003';const h=document.createElement('h2'),p=document.createElement('p'),a=document.createElement('a'),close=document.createElement('button');h.textContent=title;p.textContent=body;p.style.cssText='line-height:1.8;white-space:pre-wrap';a.textContent='查看本人通知与复核入口';a.href='/brand/risk-review.html';a.style.cssText='display:inline-block;background:#00866f;color:white;padding:12px 16px;border-radius:8px;text-decoration:none';close.textContent=requestOnly?'关闭':id?'我已知晓':'稍后验证';close.style.cssText='margin-left:12px;padding:12px;border:1px solid #cadbd3;border-radius:8px;background:white;color:#285347';close.onclick=()=>{if(id)ack(id,{acknowledged:true});d.close()};d.append(h,p);if(!requestOnly)d.append(a);d.append(close);d.addEventListener('close',()=>{d.remove();dialog=null});document.body.append(d);dialog=d;try{d.showModal();return true}catch{d.remove();dialog=null;return false}}
 function observe(payload,status){if(payload?.success&&payload.data?.restricted){location.replace('/brand/risk-review.html');return}const code=payload?.code||payload?.error?.code||'';
 if(['trial_quote_unavailable','trial_request_budget','trial_account_budget','trial_concurrency_limit'].includes(code)){show('试用请求未发送',payload?.error?.message||payload?.message||'请缩短请求或稍后重试。',null,true);return}
 if(['risk_review_required','risk_protection_limit','risk_quote_unavailable'].includes(code))show('账户安全保护','当前请求受安全保护限制，请查看控制台状态或稍后重试。',null,true);else if(status===401&&['AUTH_SESSION_REVOKED','AUTH_USER_DISABLED'].includes(code))show('请重新验证身份','当前会话已失效，请重新登录。',null,true)}
 window.fetch=async(input,init)=>{const r=await nativeFetch(input,init);try{const u=new URL(typeof input==='string'?input:input.url,location.origin);if(u.origin===location.origin&&(u.pathname.startsWith('/api/')||u.pathname.startsWith('/v1/')||u.pathname.startsWith('/pg/'))&&!u.pathname.startsWith('/api/omni/risk/'))r.clone().json().then(p=>observe(p,r.status)).catch(()=>{})}catch{}return r};
 const open=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(method,url,...rest){let monitor=false;try{const u=new URL(url,location.origin);monitor=u.origin===location.origin&&(u.pathname.startsWith('/api/')||u.pathname.startsWith('/v1/')||u.pathname.startsWith('/pg/'))&&!u.pathname.startsWith('/api/omni/risk/')}catch{}if(monitor)this.addEventListener('load',()=>{try{observe(typeof this.response==='object'?this.response:JSON.parse(this.responseText),this.status)}catch{}},{once:true});return open.call(this,method,url,...rest)};
 function client(){if(window.OmniCommerce)return Promise.resolve(window.OmniCommerce);if(!commerce)commerce=new Promise((resolve,reject)=>{const s=document.createElement('script');s.src='/brand/commerce-session.js';s.onload=()=>resolve(window.OmniCommerce);s.onerror=reject;document.head.append(s)});return commerce}
 const trialCardId = 'omni-new-account-trial';
 let lastTrialPoll = 0, trialLoading = false;
 function renderTrial(status) {
   const main = document.querySelector('main,[role="main"]');
   if (!main || !onDashboard()) return;
   const existing = document.getElementById(trialCardId);
   if (!status?.active) { existing?.remove(); return; }
   const money = micros => '¥' + (micros / 1000000).toLocaleString('zh-CN', {maximumFractionDigits: 6});
   const revision = JSON.stringify(status);
   if (existing?.dataset.revision === revision) return;
   const card = document.createElement('section');
   card.dataset.revision = revision;
   card.id = trialCardId;
   card.setAttribute('aria-label', '新账号 API 试用');
   card.style.cssText = 'margin:0 0 16px;padding:16px;border:1px solid var(--border,#d7e8e0);border-radius:12px;min-width:0';
   const heading = document.createElement('h2');
   heading.textContent = '新账号 API 试用';
   heading.style.fontWeight = '600';
   const details = document.createElement('p');
   const end = new Date(status.ends_at * 1000).toLocaleString('zh-CN', {timeZone:'Asia/Shanghai', hour12:false});
   details.textContent = '采购成本保护额度剩余 ' + money(status.remaining_micros) + ' / ' + money(status.total_micros) +
     '，单次最多 ' + money(status.request_micros) + '，并发上限 ' + status.concurrency + '。北京时间 ' + end +
     ' 结束。此额度与钱包余额分别计算。';
   const sample = document.createElement('pre');
   sample.style.cssText = 'overflow:auto;margin:12px 0;white-space:pre-wrap;overflow-wrap:anywhere';
   sample.textContent = [
     "curl https://omniapi.biz/v1/chat/completions",
     "  -H 'Authorization: Bearer YOUR_API_KEY'",
     "  -H 'Content-Type: application/json'",
     "  -d '" + JSON.stringify({model:'deepseek-v4.1-flash', messages:[{role:'user', content:'Say hello in one sentence.'}], max_tokens:128}) + "'"
   ].join(' ' + String.fromCharCode(92,10));
   const copy = document.createElement('button');
   copy.type = 'button';
   copy.textContent = '复制首次调用示例';
   copy.style.cssText = 'padding:6px 10px;border:1px solid currentColor;border-radius:6px';
   copy.onclick = async () => {
     try { await navigator.clipboard.writeText(sample.textContent); copy.textContent = '已复制'; }
     catch { const range = document.createRange(); range.selectNodeContents(sample); const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range); }
   };
   card.append(heading, details, sample, copy);
   if (existing) existing.replaceWith(card); else main.prepend(card);
 }
 function onDashboard() { return /^\/dashboard(?:\/|$)/.test(location.pathname); }
 async function pollTrial() {
   if (!onDashboard() || document.hidden || trialLoading ||
       !document.cookie.includes('new_api_has_session=1')) return;
   if (Date.now() - lastTrialPoll < 15000) return;
   if (!document.querySelector('main,[role="main"]')) return;
   lastTrialPoll = Date.now();
   trialLoading = true;
   try { renderTrial((await (await client()).request('/api/omni/risk/trial')).data); }
   catch {}
   finally { trialLoading = false; }
 }
 setInterval(pollTrial, 3000);
 addEventListener('visibilitychange', pollTrial);
 pollTrial();
 async function ack(id,body){try{const r=await nativeFetch('/api/omni/risk/notices/'+encodeURIComponent(id)+'/ack',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(r.status===401)(await client()).request('/api/omni/risk/notices/'+encodeURIComponent(id)+'/ack',body).catch(()=>{})}catch{}}
 async function poll(){if(polling||document.hidden||location.pathname==='/brand/risk-review.html'||!document.cookie.includes('new_api_has_session=1'))return;polling=true;try{const c=await client(),p=await c.request('/api/omni/risk/self');reviewNotice(p.data)}catch{}finally{polling=false}};setInterval(poll,15000);addEventListener('visibilitychange',poll);poll();
})();
