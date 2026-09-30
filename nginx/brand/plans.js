const $=id=>document.getElementById(id);
let catalog, selected;
const money=n=>`¥${Number(n).toFixed(2).replace(/\.00$/,'')}`;
const validUser=()=>/^[1-9]\d{0,11}$/.test($('user-id').value.trim());
function updateButtons(){
  $('show-qr').disabled=!validUser();
  $('copy-request').disabled=!validUser() || !$('transaction').value.trim();
}
function choose(plan){
  selected=plan; $('chosen-plan').textContent=plan.title;
  $('chosen-price').textContent=money(plan.sale_cny);
  $('qr-section').hidden=true; $('request-text').hidden=true;
  $('copy-status').textContent=''; $('transaction').value=''; updateButtons();
}
async function load(){
  try{
    const response=await fetch('/brand/plans.json',{cache:'no-store'});
    if(!response.ok) throw Error('unavailable');
    catalog=await response.json();
    $('plans').replaceChildren();
    if(!catalog.plans.length) throw Error('unavailable');
    catalog.plans.forEach((p,i)=>{
      const row=document.createElement('label'); row.className='plan';
      const radio=document.createElement('input'); radio.type='radio'; radio.name='plan'; radio.value=String(p.id); radio.checked=i===0; radio.addEventListener('change',()=>choose(p));
      const title=document.createElement('span');
      const name=document.createElement('span'); name.className='plan-name'; name.textContent=p.title;
      const detail=document.createElement('span'); detail.className='plan-detail'; detail.textContent=`${p.duration_days} 天有效 · ${money(p.sale_cny)} 消费额度`;
      title.append(name,detail);
      const price=document.createElement('span'); price.className='plan-price'; price.textContent=money(p.sale_cny);
      const frequency=document.createElement('small'); frequency.textContent='一次购买'; price.append(frequency);
      row.append(radio,title,price); $('plans').append(row);
    });
    $('fx-note').textContent=`人民币按固定业务换算率 ¥${catalog.cny_per_usd} = US$1 计价，不随实时汇率浮动。`;
    $('contact').textContent=catalog.contact || '暂未公布';
    $('merchant-id').textContent=catalog.merchant_id; $('merchant-row').hidden=!catalog.merchant_id;
    if(!catalog.accepting_payments || !catalog.contact){
      $('availability').textContent='套餐准备中，暂不收款。开放后可在此查看付款与开通方式。';
      return;
    }
    $('availability').textContent='支持微信 / 支付宝扫码。付款后需人工核验，不会自动开通。';
    $('checkout').hidden=false; choose(catalog.plans[0]);
  }catch{
    $('plans').textContent='套餐暂时不可用，请稍后重试。';
    $('availability').textContent='暂不收款，请勿根据缓存信息付款。';
    $('checkout').hidden=true;
  }
}
$('user-id').addEventListener('input',()=>{ $('qr-section').hidden=true; $('request-text').hidden=true; updateButtons(); });
$('transaction').addEventListener('input',updateButtons);
$('show-qr').addEventListener('click',()=>{if(validUser()) $('qr-section').hidden=false;});
$('copy-request').addEventListener('click',async()=>{
  if(!validUser() || !$('transaction').value.trim() || !selected) return;
  const text=`OmniAPI 套餐开通申请\n用户 ID：${$('user-id').value.trim()}\n套餐：${selected.title}（ID ${selected.id}）\n应付金额：${money(selected.sale_cny)}\n交易单号：${$('transaction').value.trim()}\n请管理员核实实际到账后开通。`;
  $('request-text').value=text; $('request-text').hidden=false;
  try{await navigator.clipboard.writeText(text); $('copy-status').textContent='已复制，请发送给客服。尚未提交或开通。';}
  catch{$('request-text').select(); $('copy-status').textContent='请手动复制下方申请，并发送给客服。';}
});
load();
