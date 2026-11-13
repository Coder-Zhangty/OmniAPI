(() => {
 'use strict';
 const $=id=>document.getElementById(id),api=(...args)=>window.OmniCommerce.request(...args),date=n=>new Date(n*1000).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai'});
 let self,review,busy=false;
 function el(tag,text,cls){const node=document.createElement(tag);if(text!==undefined)node.textContent=String(text);if(cls)node.className=cls;return node;}
 function recordName(kind){const m=/^subscription:(v3_)?(19|39|69|99|199)$/.exec(kind);if(m)return m[2]+'元套餐购买';return ({'subscription:migrated_refill':'迁移额度补满','shared:create':'创建共享池','shared:add':'共享池加量','shared:purchase':'共享池续期','commerce:reset':'购买额度重置'})[kind]||'额度记录';}
 function table(headers,rows){const t=el('table'),head=el('thead'),tr=el('tr');headers.forEach(x=>tr.append(el('th',x)));head.append(tr);t.append(head);const body=el('tbody');for(const row of rows){const tr=el('tr');row.forEach(x=>{const td=el('td');if(x instanceof Node)td.append(x);else td.textContent=String(x);tr.append(td);});body.append(tr);}t.append(body);return t;}
 async function migration(){
  try{
   review=(await api('/api/omni/commerce/migration')).data;const item=review.migration,applications=review.applications||[];
   $('migration-applications').replaceChildren();$('request-migration-exit').disabled=true;
   if(!item?.applied_at){$('migration-state').textContent='当前没有已生效的统一迁移记录。';return;}
   const deadline=item.applied_at+30*86400;
   $('migration-state').textContent=`${date(item.applied_at)} 统一迁移，原有效期至 ${date(item.original_end)}。退出申请截止 ${date(deadline)}。`;
   $('request-migration-exit').disabled=!!item.exit_id||Date.now()>=Math.min(deadline,item.original_end)*1000;
   for(const request of applications){
    const row=el('div',undefined,'migration-application');row.append(el('p',`${date(request.requested_at)} · ${{pending:'审核中',approve:'已退款',reject:'未批准',withdraw:'已撤回'}[request.state]||request.state}`),el('p',`锁定待核退款：$${(request.refund_quota/self.quota_per_unit).toFixed(6)} 钱包额度`,'caption'));
    if(request.decision)row.append(el('p',request.decision,'caption'));
    if(request.state==='pending'){const button=el('button','撤回申请','quiet');button.onclick=async()=>{button.disabled=true;try{await api('/api/omni/commerce/migration/exit/'+encodeURIComponent(request.id)+'/withdraw',{});location.reload();}catch(e){$('migration-state').textContent=e.message;button.disabled=false;}};row.append(button);}
    $('migration-applications').append(row);
   }
  }catch(e){$('migration-state').textContent=e.message;}
 }
 function renderManagement(event){
  self=event.detail;const paused=!!self.subscription?.exit_application_id;
  $('wallet-continuation').checked=!!self.account?.wallet_overflow;$('wallet-continuation').disabled=false;$('claim-benefits').disabled=self.status!=='active'||!self.membership_enabled||paused;
  $('continuation-status').textContent=paused?'迁移退出审核中：对应订阅暂停，个人钱包及独立共享池不受影响。':'仅用于个人订阅额度不足时的资金选择。';if(self.billing_policy==='customer_actual_v1')$('continuation-status').textContent+=' 钱包续用仅控制额度耗尽后的新请求。已接收请求完整结算，套餐覆盖外按钱包 API 价扣款，可形成欠费；补足欠款后恢复新请求。';
  $('available-cards').replaceChildren();const cards=(self.cards||[]).filter(c=>c.state==='unused'&&c.expires*1000>Date.now());
  for(const card of cards){const row=el('div',undefined,'benefit-row');row.append(el('span',`${card.kind==='week'?'周额度卡':'五小时额度卡'} · ${date(card.expires)} 到期`));const button=el('button','使用','quiet');button.disabled=paused;button.onclick=async()=>{button.disabled=true;try{await api('/api/omni/cards/redeem',{code:card.id});$('refresh').click();}catch(e){$('benefit-result').textContent=e.message;button.disabled=false;}};row.append(button);$('available-cards').append(row);}
  $('history').replaceChildren(table(['时间','记录','实付金额'],(self.receipts||[]).map(r=>[date(r.created_at),recordName(r.kind||''),r.cents?'¥'+(r.cents/100).toFixed(2):'—'])));
  if(!(self.receipts||[]).length)$('history').replaceChildren(el('p','暂无购买记录。','caption'));
  $('subscription-notices').replaceChildren();for(const notice of self.notices||[]){const row=el('div',undefined,'migration-application');row.append(el('h2',notice.title),el('p',notice.body,'caption'));const button=el('button','知道了','quiet');button.onclick=async()=>{button.disabled=true;try{await api('/api/omni/notices/read',{key:notice.key});row.remove();}catch(e){button.disabled=false;$('benefit-result').textContent=e.message;}};row.append(button);$('subscription-notices').append(row);}
  void migration();
 }
 window.addEventListener('omni-membership-loaded',renderManagement);
 if(window.OmniMembershipSelf)renderManagement({detail:window.OmniMembershipSelf});
 $('wallet-continuation').onchange=async()=>{
  const input=$('wallet-continuation'),wanted=input.checked;input.disabled=true;
  try{await api('/api/omni/preferences',{wallet_overflow:wanted});const current=(await api('/api/omni/self')).data;if(!!current.account.wallet_overflow!==wanted)throw Error('设置尚未确认，请重新读取。');$('continuation-status').textContent='设置已生效。'+(current.billing_policy==='customer_actual_v1'?'钱包续用仅控制额度耗尽后的新请求。已接收请求完整结算，套餐覆盖外按钱包 API 价扣款，可形成欠费；补足欠款后恢复新请求。':'');}
  catch(e){input.checked=!!self?.account?.wallet_overflow;$('continuation-status').textContent=e.message;}
  finally{input.disabled=false;}
 };
 $('claim-benefits').onclick=async()=>{const button=$('claim-benefits');button.disabled=true;try{const result=(await api('/api/omni/cards/claim',{})).data;$('benefit-codes').replaceChildren();for(const code of result.codes||[])$('benefit-codes').append(el('code',code));$('benefit-result').textContent=result.codes?.length?'福利已发放，请保存卡码。':'本期福利已领取或暂无可领福利。';$('refresh').click();}catch(e){$('benefit-result').textContent=e.message;button.disabled=false;}};
 $('request-migration-exit').onclick=()=>{
  if(busy||!review?.migration?.id)return;
  const dialog=$('confirm');$('confirm-title').textContent='申请迁移退出核算';$('confirm-copy').textContent='提交后暂停对应迁移订阅，并按提交时剩余时长锁定待核退款。个人钱包与独立共享池仍可用。人工批准后退款回原付款钱包；撤回或驳回不延期、不再次补满额度。';$('confirm-error').textContent='';$('confirm-submit').textContent='确认申请退出';$('confirm-submit').disabled=false;
  const key=crypto.randomUUID();dialog.showModal();$('confirm-submit').onclick=async()=>{if(busy)return;busy=true;$('confirm-submit').disabled=true;try{await api('/api/omni/commerce/migration/exit',{request_key:key,reason:'不同意本次统一迁移，申请按原购买段核算退出。'});location.reload();}catch(e){$('confirm-error').textContent=e.message;busy=false;$('confirm-submit').disabled=false;}};
 };
})();
