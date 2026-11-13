/* Reconcile missing payment-time evidence inside the existing admin page. */
(() => {
  'use strict';
  if (!location.pathname.endsWith('/admin.html')) return;
  const panel=document.createElement('section');panel.className='panel';
  const title=document.createElement('h2');title.textContent='活动赠额待核验';
  const status=document.createElement('p');status.setAttribute('role','status');
  const rows=document.createElement('div');
  const reload=document.createElement('button');reload.type='button';reload.textContent='刷新待核验记录';
  panel.append(title,status,rows,reload);(document.querySelector('#admin-board')||document.querySelector('main')).append(panel);
  async function api(path,body){
    const auth=await fetch('/api/user/auth/refresh',{method:'POST',credentials:'same-origin',cache:'no-store'}).then(r=>r.json());
    const token=auth.data?.access_token;if(!token)throw Error('请先登录管理员账户');
    const response=await fetch(path,{method:body?'POST':'GET',credentials:'same-origin',cache:'no-store',headers:{Authorization:'Bearer '+token,'Content-Type':'application/json'},...(body?{body:JSON.stringify(body)}:{})});
    const p=await response.json();if(!response.ok||p.success===false)throw Error(p.message||'核验未完成');return p.data;
  }
  async function load(){
    reload.disabled=true;
    try {
      const pending=await api('/api/omni/admin/promotion-pending');rows.replaceChildren();status.textContent=pending.length?'本金已到账。请核对交易上的付款成功时间，保存后自动补发符合条件的赠额。':'没有待核验的活动赠额。';
      for(const r of pending){
        const form=document.createElement('form');form.className='row-actions';
        const label=document.createElement('label');label.textContent=`账号 #${r.user_id} · 实付 ¥${(r.cents/100).toFixed(2)} · 凭据 ${r.key} · 已核实付款时间（北京时间） `;
        const time=document.createElement('input');time.type='datetime-local';time.step='1';time.required=true;label.append(time);
        const submit=document.createElement('button');submit.textContent='核验并补发';submit.type='submit';form.append(label,submit);
        form.onsubmit=async event=>{event.preventDefault();submit.disabled=true;try{const paidAt=Math.floor(Date.parse(time.value+'+08:00')/1000);if(!Number.isFinite(paidAt))throw Error('请填写真实交易时间');await api('/api/omni/admin/credit',{key:r.key,user_id:r.user_id,cents:r.cents,quota:r.quota,paid_at:paidAt,paid_at_source:'manual:verified_transaction'});await load();}catch(e){status.textContent=e.message;submit.disabled=false;}};
        rows.append(form);
      }
    }catch(e){status.textContent=e.message;}finally{reload.disabled=false;}
  }
  reload.onclick=load;load();
})();
