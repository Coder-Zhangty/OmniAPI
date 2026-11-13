/* OmniAPI image studio. Session authentication and the existing image ledger only. */
(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const labels = {low:'基础',medium:'标准',high:'精细',default:'默认'};
  const modelLabels = {'gpt-image-2':'GPT Image 2','gpt-image-2-pro':'GPT Image 2 Pro','gpt-image-2.5':'GPT Image 2.5','gpt-image-2.5-flare':'GPT Image 2.5 Flare','gpt-image-2.5-sunburst':'GPT Image 2.5 Sunburst','gemini-nano-banana-2.1':'Gemini Nano Banana 2.1','gemini-3.1-flash-image-preview':'Gemini Flash Image'};
  const loginURL = '/sign-in?redirect=%2Fbrand%2Fimage-studio.html';
  let models = {}, token = '', busy = false, objectURL = '', unsaved = false, zoom = 100, toastTimer;
  let authKnown = false, catalogError = false, fontError = false;
  const toast = message => { clearTimeout(toastTimer); $('toast').textContent = message; $('toast').hidden = false; toastTimer = setTimeout(() => { $('toast').hidden = true; }, 5000); };
  const quote = () => models[$('model').value];
  function validSize(value) { const m=/^([1-9]\d{1,4})x([1-9]\d{1,4})$/.exec(value); return m && +m[1] <= 16384 && +m[2] <= 16384 && +m[1]*+m[2] <= 67108864; }
  function sizes(row) { const specs=row?.image_specs || {}; return [...new Set(specs.sizes?.length ? specs.sizes : specs.size ? [specs.size] : [])].filter(validSize); }
  function combinations(row) {
    const specs=row?.image_specs || {}, all=sizes(row);
    if (Array.isArray(specs.options) && specs.options.length) return specs.options.filter(x => all.includes(x.size) && typeof x.quality === 'string' && x.quality.length < 40);
    return all.flatMap(size => (specs.qualities || []).map(quality => ({size,quality})));
  }
  function ratioOf(size) { const [w,h]=size.split('x').map(Number); for(const [a,b] of [[1,1],[3,2],[2,3],[16,9]]) if(w*b===h*a)return a+':'+b; return 'other'; }
  function setOptions(select, values, format) { const old=select.value;select.replaceChildren(...values.map(value=>new Option(format(value),value))); if(values.includes(old))select.value=old; }
  function updateReady() { const ok=!!quote() && combinations(quote()).some(x=>x.size===$('size').value&&x.quality===$('quality').value); $('generate').disabled=busy||!token||!ok||catalogError||fontError; $('model').disabled=busy||!Object.keys(models).length; $('size').disabled=busy||!ok; $('quality').disabled=busy||!ok; $('service-tier').disabled=busy; document.querySelectorAll('.ratio').forEach(button => { button.disabled=busy||!sizes(quote()).some(s=>ratioOf(s)===button.dataset.ratio); button.title=button.disabled?'当前模型尚未开放此比例':''; }); }
  function markRatio() { const current=ratioOf($('size').value);document.querySelectorAll('.ratio').forEach(button=>button.setAttribute('aria-pressed',String(button.dataset.ratio===current))); }
  function renderPrice() {
    const row=quote(), qualityPrice=row?.quality_pricing?.[$('quality').value] || row || {};
    const cny=Number(qualityPrice.image_price_cny), usd=Number(qualityPrice.image_price_usd);
    const validCny=Number.isFinite(cny)&&cny>0, validUsd=Number.isFinite(usd)&&usd>0;
    $('price').classList.toggle('quote-note',!validCny&&!validUsd);
    if (validCny||validUsd) { const amount=validCny?qualityPrice.image_price_cny:qualityPrice.image_price_usd; $('price').replaceChildren(document.createTextNode((validCny?'¥':'$')+amount+' '));const unit=document.createElement('small');unit.textContent='/ 张';$('price').append(unit); }
    else $('price').textContent='按模型广场当前报价结算';
    updateReady();
  }
  function renderQualities() { const accepted=[...new Set(combinations(quote()).filter(x=>x.size===$('size').value).map(x=>x.quality))];setOptions($('quality'),accepted,v=>labels[v]||v);markRatio();renderPrice(); }
  function renderModel() {
    const row=quote(), specs=row?.image_specs||{};setOptions($('size'),sizes(row),v=>v.replace('x',' × '));if(sizes(row).includes(specs.size))$('size').value=specs.size;
    renderQualities();if([...$('quality').options].some(o=>o.value===specs.default_quality))$('quality').value=specs.default_quality;
    const tiers=Array.isArray(row?.request_options?.service_tier)?row.request_options.service_tier.filter(t=>typeof t==='string'):[];
    setOptions($('service-tier'),tiers,v=>v);$('extra-options').hidden=!tiers.length;renderPrice();
  }
  async function identity() {
    try {
      const r=await fetch('/api/user/auth/refresh',{method:'POST',credentials:'same-origin',cache:'no-store'}), p=await r.json();
      if(!r.ok||!p.success||!p.data?.access_token)throw Error('session');token=p.data.access_token;
      const name=p.data.user?.display_name||p.data.user?.username||'个人账户';$('identity').textContent='个人账户';$('identity').title='已登录 · '+name;$('account-link').href='/wallet';
    } catch (_) { token='';$('identity').textContent='登录账户';$('identity').title='登录后即可生成图片';$('account-link').href=loginURL;$('status').textContent='请先登录账户，再生成图片。'; }
    authKnown=true;updateReady();
  }
  async function catalog() {
    try {
      const r=await fetch('/api/omni/pricing',{cache:'no-store',credentials:'same-origin'}),p=await r.json();if(!r.ok)throw Error('catalog');
      models=Object.fromEntries(Object.entries(p.data?.models||{}).filter(([,row])=>row.image_specs&&row.availability!=='paused'&&row.eligible!==false&&combinations(row).length));
      const names=Object.keys(models).sort((a,b)=>a==='gpt-image-2'?-1:b==='gpt-image-2'?1:a.localeCompare(b));if(!names.length)throw Error('catalog-empty');
      setOptions($('model'),names,v=>modelLabels[v]||v);catalogError=false;renderModel();
    } catch (_) { catalogError=true;$('price').textContent='暂时无法读取报价';$('price').classList.add('quote-note');$('status').textContent='暂时无法读取已验收图片规格，请刷新后重试。';updateReady(); }
  }
  function count() { $('char-count').textContent=[...$('prompt').value].length; }
  function setZoom(value) { zoom=Math.max(25,Math.min(200,value));$('zoom-level').value=zoom+'%';const img=$('artwork').querySelector('img');if(img)img.style.transform='scale('+(zoom/100)+')';else $('artwork').style.backgroundSize=(276.78883*zoom/100)+'% '+(172.52174*zoom/100)+'%'; }
  function expand() { const box=$('lightbox').querySelector('.artwork');box.replaceChildren();box.style.backgroundSize='';box.classList.toggle('live-result',!!objectURL);if(objectURL){const img=new Image();img.src=objectURL;img.alt='本次生成的图片';img.className='result-image';box.append(img);}if(!$('lightbox').open)$('lightbox').showModal(); }
  function setState(state,text) { document.querySelector('.result-state').dataset.state=state;$('result-state').textContent=text; }
  function requireReconciliation(message,requestID) { $('status').textContent=message+' 未自动重试，请先核对调用记录。'+(requestID?' 请求编号：'+requestID:'');$('usage-link').hidden=false;setState('error','请核对记录'); }
  $('model').addEventListener('change',renderModel);$('size').addEventListener('change',renderQualities);$('quality').addEventListener('change',renderPrice);$('prompt').addEventListener('input',count);count();
  document.querySelectorAll('.ratio').forEach(button=>button.addEventListener('click',()=>{const size=sizes(quote()).find(s=>ratioOf(s)===button.dataset.ratio);if(size){$('size').value=size;renderQualities();}}));
  $('zoom-in').addEventListener('click',()=>setZoom(zoom+25));$('zoom-out').addEventListener('click',()=>setZoom(zoom-25));$('thumbnail').addEventListener('click',()=>setZoom(100));
  $('expand').addEventListener('click',expand);$('artwork').addEventListener('dblclick',expand);$('artwork').addEventListener('keydown',e=>{if(e.key==='Enter')expand();});
  $('lightbox').querySelector('.lightbox-close').addEventListener('click',()=>$('lightbox').close());$('lightbox').addEventListener('click',e=>{const r=$('lightbox').getBoundingClientRect();if(e.target===$('lightbox')&&(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom))$('lightbox').close();});
  $('font-error-close').addEventListener('click',()=>{$('font-error').hidden=true;});
  document.fonts?.load('400 16px "Omni Studio"','图片工作台 OmniAPI').then(fonts=>{if(!fonts.length)throw Error('font');}).catch(()=>{fontError=true;$('font-error').hidden=false;updateReady();});
  $('download').addEventListener('click',()=>{if(!objectURL)return;const link=document.createElement('a');link.href=objectURL;link.download=$('model').value+'-'+Date.now()+'.png';link.click();unsaved=false;toast('已开始下载，请确认图片保存成功。');});
  document.addEventListener('keydown',e=>{if((e.metaKey||e.ctrlKey)&&e.key==='Enter'&&!$('lightbox').open){e.preventDefault();if(!$('generate').disabled)$('creation-form').requestSubmit();}});
  $('followup-form').addEventListener('submit',e=>e.preventDefault());
  window.addEventListener('beforeunload',e=>{if(busy||unsaved){e.preventDefault();e.returnValue='';}});
  window.addEventListener('pagehide',()=>{if(objectURL)URL.revokeObjectURL(objectURL);});
  $('creation-form').addEventListener('submit',async e=>{
    e.preventDefault();if(busy||!token||!quote()||catalogError||fontError)return;
    const body={model:$('model').value,prompt:$('prompt').value.trim(),size:$('size').value,quality:$('quality').value,n:1,output_format:'png',response_format:'b64_json'};
    if(!body.prompt){$('status').textContent='请先描述想生成的画面。';$('prompt').focus();return;}
    if(!combinations(quote()).some(x=>x.size===body.size&&x.quality===body.quality)){toast('请选择已验收的尺寸和画质组合。');return;}
    if(!$('extra-options').hidden)body.service_tier=$('service-tier').value;
    busy=true;updateReady();$('generate').setAttribute('aria-busy','true');$('generate').querySelector('span').textContent='正在生成';document.querySelector('.canvas-stage').setAttribute('aria-busy','true');setState('busy','正在生成');
    $('status').textContent='正在生成，请保持页面打开。通常需要几十秒至数分钟。';$('usage-link').hidden=true;
    const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),660000);let requestID='';
    try {
      const r=await fetch('/api/omni/images/generations',{method:'POST',credentials:'same-origin',cache:'no-store',headers:{Authorization:'Bearer '+token,'Content-Type':'application/json'},body:JSON.stringify(body),signal:controller.signal});
      requestID=r.headers.get('X-Oneapi-Request-Id')||'';const p=await r.json();
      if(!r.ok){if(r.status===401){token='';$('identity').textContent='重新登录';$('account-link').href=loginURL;}throw Error(p.error?.message||p.message||'请求失败，请查看调用记录。');}
      if(p.data?.length!==1||typeof p.data[0].b64_json!=='string'||p.data[0].b64_json.length>96*1024*1024)throw Error('图片返回不完整。');
      const raw=atob(p.data[0].b64_json),bytes=Uint8Array.from(raw,c=>c.charCodeAt(0));
      if(bytes.length<24||![137,80,78,71,13,10,26,10].every((v,i)=>bytes[i]===v))throw Error('返回的图片格式不符。');
      const view=new DataView(bytes.buffer),width=view.getUint32(16),height=view.getUint32(20);
      if(body.size!==width+'x'+height)throw Error('实际图片尺寸与所选尺寸不符，请将请求编号发给客服。');
      const url=URL.createObjectURL(new Blob([bytes],{type:'image/png'})),img=new Image();img.src=url;img.alt='本次生成的图片';img.className='result-image';
      try{await img.decode();}catch(_){URL.revokeObjectURL(url);throw Error('返回的图片无法解码，请核对调用记录。');}
      const previous=objectURL;objectURL=url;unsaved=true;$('artwork').replaceChildren(img);$('artwork').classList.add('live-result');$('artwork').style.backgroundImage='none';$('artwork').setAttribute('aria-label','本次生成的图片');setZoom(100);
      const thumb=document.querySelector('.thumbnail-picture');thumb.classList.add('live-result');thumb.style.backgroundImage='url("'+url+'")';$('thumbnail').setAttribute('aria-label','查看本次图片');$('download').disabled=false;$('download').title='下载本次生成的 PNG 图片';
      $('image-meta').textContent=(modelLabels[body.model]||body.model)+' · '+width+' × '+height+' · '+(labels[body.quality]||body.quality);$('image-meta').classList.remove('sample-label');
      if(previous)URL.revokeObjectURL(previous);setState('done','生成完成');$('status').textContent='生成完成，请下载保存。'+(requestID?' 请求编号：'+requestID:'');
    } catch(error){requireReconciliation(error.name==='AbortError'?'等待超时，生成状态待核对。':error.message,requestID);}
    finally{clearTimeout(timer);busy=false;$('generate').removeAttribute('aria-busy');$('generate').querySelector('span').textContent='生成图片';document.querySelector('.canvas-stage').setAttribute('aria-busy','false');updateReady();}
  });
  catalog();identity();
})();
