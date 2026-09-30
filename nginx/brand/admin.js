const $=id=>document.getElementById(id);
const API='/brand/admin/api/';
const money=n=>`¥${Number(n).toFixed(2).replace(/\.00$/,'')}`;
const amountLabel=(n,c='CNY')=>`${c} ${Number(n).toFixed(2)}`;
const STATUS={pending:'待核验',fulfilled:'已发码',dismissed:'已忽略'};
// 刷新时保住已输入但还没提交的交易单号，避免重建表格把它清掉。
const typedTransactions=new Map();
let timer=null;
let paymentSettings=null;
let csrfToken='';

async function call(path,options){
  const response=await fetch(API+path,Object.assign({cache:'no-store',credentials:'same-origin'},options||{}));
  let payload={};
  try{payload=await response.json();}catch{}
  if(payload.csrf_token)csrfToken=payload.csrf_token;
  if(response.status===401) return {unauthorized:true,payload:payload};
  if(!response.ok||payload.success===false){
    const error=new Error(payload.message||('HTTP '+response.status));
    // Keep the server's machine-readable code: the page needs to tell "re-confirm
    // your console identity" apart from a genuine failure.
    error.code=payload.code||'';
    error.status=response.status;
    throw error;
  }
  return {payload:payload};
}
async function post(path,body){
  if(!csrfToken&&!['login','session'].includes(path))await call('session');
  const headers={'Content-Type':'application/json'};
  if(csrfToken)headers['X-CSRF-Token']=csrfToken;
  return call(path,{method:'POST',headers:headers,body:JSON.stringify(body)});
}
function when(value){
  if(!value) return '—';
  const date=new Date(value);
  return isNaN(date) ? String(value) : date.toLocaleString('zh-CN',{hour12:false});
}
function cell(text,className){
  const td=document.createElement('td');
  td.textContent=text;
  if(className) td.className=className;
  return td;
}
function pill(status){
  const span=document.createElement('span');
  span.className='pill '+(STATUS[status]?status:'');
  span.textContent=STATUS[status]||status||'—';
  return span;
}
function accountLabel(row){
  const name=row.username||'';
  return name ? `${name}（ID ${row.uid}）` : `ID ${row.uid}`;
}

function showGate(){
  $('gate').hidden=false;$('board').hidden=true;
  if(timer){clearInterval(timer);timer=null;}
}
function showBoard(){
  $('gate').hidden=true;$('board').hidden=false;
  if(!timer) timer=setInterval(()=>{if(document.visibilityState==='visible') load();},30000);
}

// 网关管理员会话只活在服务进程的内存里，经营者那张 30 天的 cookie 却比它长命，
// 所以服务一重启就会出现「面板打得开、按钮点不动」。这里让页面自己补种一次，
// 刷新才真的能把问题修好。补种走的是登录限流桶，失败后不再自动重试，只留手动按钮。
let gatewayAttemptAt=0,gatewayBlocked=false;
function showGatewayNotice(ok){
  $('gateway-panel').hidden=ok;
}
async function ensureGatewaySession(force){
  if(!force){
    if(gatewayBlocked) return false;
    if(Date.now()-gatewayAttemptAt<120000) return false;
  }
  gatewayAttemptAt=Date.now();
  const ok=await bootstrapFromConsole();
  gatewayBlocked=!ok;
  showGatewayNotice(ok);
  if(!ok)$('gateway-status').textContent='当前没有可用的控制台管理员会话，暂时无法入账。请确认你仍登录着控制台，再点下面的按钮。';
  return ok;
}

async function load(){
  const result=await call('summary');
  if(result.unauthorized){showGate();return;}
  const data=result.payload;
  if(data.gateway_session===false) await ensureGatewaySession(false);
  else showGatewayNotice(true);
  const pending=data.pending||[];
  const counts=data.counts||{};
  $('pending-count').textContent=String(counts.pending||0);
  $('pending-count').className='pill '+(counts.pending?'pending':'');

  const body=$('pending-body');
  body.replaceChildren();
  pending.forEach(row=>body.append(pendingRow(row)));
  $('pending-table').hidden=pending.length===0;
  $('pending-empty').hidden=pending.length>0;

  const recent=(data.recent||[]).filter(row=>row.status!=='pending');
  const recentBody=$('recent-body');
  recentBody.replaceChildren();
  recent.forEach(row=>{
    const tr=document.createElement('tr');
    tr.append(cell(row.id,'mono'),cell(accountLabel(row)),cell(row.method||'cny-qr'),cell(amountLabel(row.net_received||row.cny||row.quoted_amount,row.currency||'CNY')),cell('',''));
    tr.children[4].append(pill(row.status));
    tr.append(cell(row.code||'直接入账','mono'),cell(when(row.handled_at)));
    recentBody.append(tr);
  });
  $('recent-table').hidden=recent.length===0;
  $('recent-empty').hidden=recent.length>0;
  $('board-status').textContent='最后刷新 '+new Date().toLocaleTimeString('zh-CN',{hour12:false});
  if(paymentSettings===null) await loadSettings();
}

function settingField(label,value,type='text'){
  const wrap=document.createElement('label');wrap.className='setting-field';
  const title=document.createElement('span');title.textContent=label;
  const input=document.createElement('input');input.type=type;input.value=value==null?'':value;
  wrap.append(title,input);return {wrap,input};
}
// Operator-owned values are shown so the page reflects reality, but they are not
// editable here: an admin-page session must not be able to repoint where money
// is sent. They live in config/payment-methods.json.
function readOnlyField(label,value){
  const wrap=document.createElement('p');wrap.className='small setting-readonly';
  const title=document.createElement('span');title.textContent=label+'：';
  const shown=document.createElement('code');shown.textContent=value||'（未配置）';
  wrap.append(title,shown);return wrap;
}
async function uploadQr(method,file,status){
  if(!file)return;
  if(file.size>1048576)throw Error('二维码图片不能超过 1 MiB');
  const dataUrl=await new Promise((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(reader.result);reader.onerror=reject;reader.readAsDataURL(file);});
  status.textContent='正在上传二维码…';
  const result=await post('payment-qr',{method_id:method.id,data_url:dataUrl});
  method.qr_path=result.payload.qr_path;status.textContent='二维码已上传，保存设置后生效。';
}
function renderSettings(){
  const root=$('payment-settings');root.replaceChildren();
  paymentSettings.forEach(method=>{
    const card=document.createElement('div');card.className='payment-setting';
    const heading=document.createElement('h3');heading.textContent=method.title;
    const enabled=settingField('启用','', 'checkbox');enabled.input.checked=!!method.enabled;
    const file=settingField('二维码图片','', 'file');file.input.accept='image/png,image/jpeg,image/webp';
    const status=document.createElement('p');status.className='small';status.textContent=method.qr_path?'当前二维码：'+method.qr_path:'尚未上传二维码';
    enabled.input.addEventListener('change',()=>method.enabled=enabled.input.checked);
    file.input.addEventListener('change',()=>uploadQr(method,file.input.files[0],status).catch(e=>status.textContent=e.message));
    card.append(heading,enabled.wrap,
      readOnlyField('最低金额',method.minimum+' '+method.currency),
      readOnlyField('公开说明',method.instructions));
    if(method.kind==='crypto') card.append(readOnlyField('钱包地址',method.address),
      readOnlyField('币种 / 网络',(method.asset||'')+' · '+(method.network||'')));
    if(method.kind==='payoneer') card.append(readOnlyField('收款链接',method.link));
    card.append(file.wrap,status);root.append(card);
  });
}
async function loadSettings(){const result=await call('settings');if(result.unauthorized)return;paymentSettings=result.payload.methods||[];renderSettings();}
async function saveSettings(){
  $('settings-status').textContent='正在保存…';
  try{const result=await post('settings',{methods:paymentSettings});paymentSettings=result.payload.methods;$('settings-status').textContent='支付设置已保存。';renderSettings();}
  catch(error){$('settings-status').textContent=error.message;}
}

function pendingRow(row){
  const tr=document.createElement('tr');
  if(row.escalated_at) tr.classList.add('is-overdue');
  tr.append(cell(row.id,'mono'),cell(accountLabel(row)),cell(row.method||'cny-qr'),cell(amountLabel(row.quoted_amount||row.cny,row.currency||'CNY')));

  const quotaTd=document.createElement('td');
  const usd=document.createElement('div'); usd.textContent=row.usd+' US$';
  const units=document.createElement('small'); units.className='small'; units.textContent=row.quota+' 额度';
  quotaTd.append(usd,units);
  const timeTd=cell(when(row.created_at));
  if(row.escalated_at){const overdue=document.createElement('strong');overdue.textContent='超时先行受理 · 待核验';overdue.style.cssText='display:block;color:#b45309;margin-top:4px';timeTd.append(overdue);}
  tr.append(quotaTd,timeTd);

  const actions=document.createElement('td');
  const wrap=document.createElement('div'); wrap.className='row-actions';
  const input=document.createElement('input');
  input.type='text'; input.maxLength=64; input.placeholder='交易单号（可空）';
  input.value=typedTransactions.get(row.id)||'';
  input.addEventListener('input',()=>typedTransactions.set(row.id,input.value));
  const net=document.createElement('input');
  net.type='number';net.min='0.01';net.step='0.01';net.placeholder=`净到账 ${row.currency||'CNY'}`;
  net.value=row.quote_id?(row.quoted_amount||''):'';
  const issue=document.createElement('button'); issue.type='button'; issue.textContent='核实到账，加额度';
  issue.addEventListener('click',()=>fulfill(row,net.value,str=>issue.textContent=str));
  const skip=document.createElement('button'); skip.type='button'; skip.textContent=row.escalated_at?'撤回受理':'忽略';
  skip.addEventListener('click',()=>dismiss(row,str=>skip.textContent=str));
  wrap.append(input,net,issue,skip);
  actions.append(wrap);
  tr.append(actions);
  return tr;
}

function fail(error){
  $('board-status').textContent=String(error.message||error);
}

async function fulfill(row,netReceived,label){
  if(row.quote_id&&(!netReceived||Number(netReceived)<=0)){fail(Error('请填写实际净到账金额'));return;}
  if(!confirm('确认真实到账后再入账。\n\n申请 '+row.id+'\n账号 '+accountLabel(row)+'\n支付方式 '+(row.method||'人民币扫码')+'\n实际净到账 '+(row.quote_id?amountLabel(netReceived,row.currency):money(row.cny))+'\n\n点「确定」将立即把折算额度加到用户钱包。')) return;
  label('入账中…');
  const body={id:row.id,transaction:typedTransactions.get(row.id)||'',net_received:row.quote_id?netReceived:null};
  try{
    let result;
    try{
      result=await post('fulfill',body);
    }catch(error){
      // 会话失效（服务重启或令牌过期）时补种一次再重试；额度台账本身防重复入账，
      // 所以这里的重试不会多发额度。
      if(error.code!=='gateway_session_missing'||!(await ensureGatewaySession(true))) throw error;
      label('已重新确认身份，入账中…');
      result=await post('fulfill',body);
    }
    typedTransactions.delete(row.id);
    showResult(row,result.payload);
    await load();
  }catch(error){
    fail(error);
  }finally{
    label('核实到账，加额度');
  }
}

async function dismiss(row,label){
  if(!confirm('忽略后不会发放任何额度：\n\n申请 '+row.id+'（'+accountLabel(row)+'，'+amountLabel(row.quoted_amount||row.cny,row.currency||'CNY')+'）')) return;
  label('处理中…');
  try{
    await post('dismiss',{id:row.id,note:'ignored from admin page'});
    typedTransactions.delete(row.id);
    await load();
  }catch(error){
    fail(error);
  }finally{
    label('忽略');
  }
}

function showResult(row,payload){
  const hasCode=!!payload.code;
  const verified=!!payload.balance_verified;
  $('result-panel').hidden=false;
  $('result-code-wrap').hidden=!hasCode;
  $('copy-code').hidden=!hasCode;
  $('result-code').textContent=payload.code||'';
  if(hasCode){
    $('result-note').textContent=`账号 ${accountLabel(row)}，净到账 ${payload.net_received||row.cny} ${row.currency||'CNY'}，折算 US$${payload.credited_usd||row.usd}，额度 ${payload.quota||row.quota}，交易单号 ${payload.transaction||row.id}。`;
    $('result-status').textContent='兑换码只显示这一次，请立刻复制并发给用户。';
  }else if(verified){
    $('result-note').textContent=`${accountLabel(row)} 的额度已直接入账：${payload.quota_before} → ${payload.quota_after}（+${payload.quota||row.quota}）。用户刷新钱包页就能看到，不需要做任何操作。`;
    $('result-status').textContent='已到账。可以把下面的说明发给用户告知一声。';
  }else{
    $('result-note').textContent='管理接口返回成功，但读到的余额没有变成期望值。请到后台核对该用户额度；本次已记入台账，重复入账会被拒绝。';
    $('result-status').textContent='请先核对，再决定是否补发。';
  }
  $('result-message').value=hasCode ? [
    '你的 OmniAPI 充值已到账。',
    '净到账：'+(payload.net_received||row.cny)+' '+(row.currency||'CNY'),
    '到账额度：'+(payload.quota||row.quota),
    '兑换码：'+(payload.code||''),
    '',
    '请打开 https://omniapi.biz/wallet ，在「兑换码充值」处输入上面的兑换码，额度立即到账。',
  ].join('\n') : [
    '你的 OmniAPI 充值已到账。',
    '净到账：'+(payload.net_received||row.cny)+' '+(row.currency||'CNY'),
    '到账额度：'+(payload.quota||row.quota),
    '当前钱包余额：'+(payload.quota_after!=null?payload.quota_after:'见钱包页'),
    '',
    '额度已直接计入你的账户，无需任何操作，打开 https://omniapi.biz/wallet 刷新即可看到。',
  ].join('\n');
  $('result-panel').scrollIntoView({behavior:'smooth',block:'nearest'});
}

function copyFrom(text,status){
  return async()=>{
    try{await navigator.clipboard.writeText(text); status.textContent='已复制。';}
    catch{status.textContent='复制失败，请手动选中复制。';}
  };
}

// 经营者本来就登录着控制台，正常情况下不该再输一遍口令：用控制台的 refresh
// cookie 换一个短时 access_token，交给充值服务验证管理员身份（role >= 10），
// 验证通过就直接发会话 cookie。口令登录保留为兜底手段。
async function bootstrapFromConsole(){
  try{
    const response=await fetch('/api/user/auth/refresh',{method:'POST',cache:'no-store',credentials:'same-origin'});
    if(!response.ok) return false;
    const payload=await response.json();
    const data=(payload&&payload.data)||{};
    const user=data.user||{};
    if(!data.access_token||Number(user.role||0)<10) return false;
    await post('session',{access_token:data.access_token});
    return true;
  }catch(error){
    return false;
  }
}
async function enter(){
  const session=await call('session');
  if(session.payload&&session.payload.authenticated){showBoard();await load();return;}
  if(await bootstrapFromConsole()){showBoard();await load();return;}
  $('gate-status').textContent='未检测到管理员登录态。请先登录控制台，或使用下方管理口令。';
  showGate();
}

$('console-login').addEventListener('click',async()=>{
  $('gate-status').textContent='正在核对控制台身份…';
  if(await bootstrapFromConsole()){$('gate-status').textContent='';showBoard();await load();}
  else $('gate-status').textContent='控制台当前不是管理员登录态，请先登录控制台。';
});
$('login').addEventListener('click',async()=>{
  $('gate-status').textContent='登录中…';
  try{
    await post('login',{password:$('password').value});
    $('password').value='';
    $('gate-status').textContent='';
    showBoard();
    await load();
  }catch(error){
    $('gate-status').textContent=String(error.message||error);
  }
});
$('password').addEventListener('keydown',event=>{if(event.key==='Enter') $('login').click();});
$('logout').addEventListener('click',async()=>{
  try{await post('logout',{});}catch{}
  showGate();
});
$('refresh').addEventListener('click',()=>load().catch(fail));
$('gateway-retry').addEventListener('click',async()=>{
  $('gateway-status').textContent='正在确认控制台身份…';
  if(await ensureGatewaySession(true)) await load();
});
$('save-settings').addEventListener('click',saveSettings);
$('copy-code').addEventListener('click',copyFrom($('result-code').textContent,$('result-status')));
$('copy-message').addEventListener('click',copyFrom($('result-message').value,$('result-status')));

enter().catch(fail);
