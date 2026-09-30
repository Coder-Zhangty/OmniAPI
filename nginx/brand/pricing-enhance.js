(()=>{if(location.pathname!=='/pricing')return;
const codes=['USD','CNY','EUR','GBP','JPY','HKD','CAD','AUD'];
const symbols={USD:'$',CNY:'¥',EUR:'€',GBP:'£',JPY:'¥',HKD:'HK$',CAD:'C$',AUD:'A$'};
let currency=localStorage.getItem('omniapi_currency')||'USD',rates={USD:1},stale=true,asOf='';
const expression=/^v1:p\s*\*\s*([0-9.]+)\s*\+\s*c\s*\*\s*([0-9.]+)$/;
function value(n){const converted=Number(n)*(Number(rates[currency])||1);return `${symbols[currency]}${converted.toFixed(currency==='JPY'?0:2)}`;}
function label(raw){const m=raw.match(expression);return m?`输入 ${value(m[1])} / 1M Tokens · 输出 ${value(m[2])} / 1M Tokens`:raw;}
function enhance(root=document){const walker=document.createTreeWalker(root,NodeFilter.SHOW_ELEMENT);let node=root.nodeType===1?root:null;do{if(node&&node.children.length===0){const raw=(node.dataset&&node.dataset.omniapiExpr)||node.textContent.trim();if(expression.test(raw)){node.dataset.omniapiExpr=raw;const next=label(raw);if(node.textContent!==next)node.textContent=next;node.title=`USD 基准：${raw.replace('v1:p *','输入 $').replace('+ c *','· 输出 $')} / 1M Tokens`;}}node=walker.nextNode();}while(node);}
let queued=false;const observer=new MutationObserver(()=>{if(queued)return;queued=true;requestAnimationFrame(()=>{queued=false;enhance();});});
function widget(){const wrap=document.createElement('div');wrap.id='omniapi-currency';const title=document.createElement('span');title.textContent='显示币种';const select=document.createElement('select');select.setAttribute('aria-label','模型价格显示币种');codes.forEach(code=>{const o=document.createElement('option');o.value=code;o.textContent=code;o.disabled=code!=='USD'&&stale;o.selected=code===currency;select.append(o);});const note=document.createElement('small');note.textContent=stale?'实时汇率不可用，当前显示 USD':`Xe · ${asOf}`;select.addEventListener('change',()=>{currency=select.value;localStorage.setItem('omniapi_currency',currency);document.querySelectorAll('[data-omniapi-expr]').forEach(el=>el.textContent=label(el.dataset.omniapiExpr));});wrap.append(title,select,note);document.body.append(wrap);
// 与其它气泡一样可以拖走、位置自己记住（共用模块缺席时只是不能拖）。
if(window.OmniFloat)OmniFloat.attach(wrap,{storageKey:'currency'});}
fetch('/brand/api/fx/rates',{cache:'no-store'}).then(r=>r.json()).then(p=>{if(p.success&&p.rates&&!p.stale){rates=p.rates;stale=false;asOf=new Date(p.as_of).toLocaleString('zh-CN',{hour12:false});}else{currency='USD';}}).catch(()=>{currency='USD';}).finally(()=>{widget();enhance();observer.observe(document.body,{childList:true,subtree:true,characterData:true});});
})();
