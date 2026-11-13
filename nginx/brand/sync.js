(() => {
  'use strict';
  const status=document.querySelector('#status'),price=document.querySelector('#price'),purchase=document.querySelector('#purchase');
  let policy, purchaseKey='';
  async function load(){
    try{
      const user=await OmniCommerce.identity();
      const {data}=await OmniCommerce.request('/api/app/sync/self');policy=data.policy;
      price.textContent=policy.enabled?`月费 ¥${(policy.monthly_cents/100).toFixed(2)} · 1GB`:'套餐尚未开放购买';
      purchase.disabled=!policy.enabled;
      status.textContent=data.subscription.end>Date.now()/1000?`有效期至 ${new Date(data.subscription.end*1000).toLocaleString('zh-CN')}`:data.subscription.end?'套餐已到期，保留期内可在客户端读取与下载。':'尚未开通同步套餐';
      if(user.role>=10){document.querySelector('#admin').hidden=false;document.querySelector('#monthly').value=(policy.monthly_cents/100).toFixed(2);document.querySelector('#enabled').checked=policy.enabled;}
    }catch(error){status.textContent=error.message;}
  }
  purchase.addEventListener('click',async()=>{
    if(!policy?.enabled||!confirm(`将从钱包扣除 ¥${(policy.monthly_cents/100).toFixed(2)}，购买一个月1GB同步权益。确认购买？`))return;
    purchase.disabled=true;purchaseKey ||= crypto.randomUUID();
    try{await OmniCommerce.request('/api/app/sync/purchase',{key:purchaseKey});purchaseKey='';await load();}
    catch(error){status.textContent=error.message;purchase.disabled=false;}
  });
  document.querySelector('#save').addEventListener('click',async()=>{
    const yuan=document.querySelector('#monthly').value;
    if(!/^\d+(?:\.\d{1,2})?$/.test(yuan)){status.textContent='请输入最多两位小数的有效月费。';return;}
    const cents=Math.round(Number(yuan)*100),enabled=document.querySelector('#enabled').checked;
    if(enabled&&cents<=0){status.textContent='开放购买前必须设置大于零的月费。';return;}
    try{await OmniCommerce.request('/api/app/admin/sync-policy',{enabled,monthly_cents:cents,quota_bytes:1073741824});await load();}
    catch(error){status.textContent=error.message;}
  });load();
})();
