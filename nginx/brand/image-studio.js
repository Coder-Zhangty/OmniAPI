(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  let models = {}, token = '', busy = false, objectURL = '', unsaved = false;
  const qualities = {low:'基础 · low', medium:'标准 · medium', high:'精细 · high',default:'默认'};
  function options(select, values) {
    const old = select.value;
    select.replaceChildren(...values.map(value => { const o = document.createElement('option'); o.value = value; o.textContent = qualities[value] || value.replace(/x/g,' × '); return o; }));
    if (values.includes(old)) select.value = old;
  }
  function renderQualities() {
    const row = models[$('model').value], specs = row?.image_specs || {};
    const accepted = specs.options?.filter(o => o.size === $('size').value).map(o => o.quality);
    options($('quality'), accepted?.length ? [...new Set(accepted)] : specs.qualities || ['low']);
    const tiers = row?.request_options?.service_tier || [];
    options($('service-tier'), tiers); $('extra-options').hidden = !tiers.length;
    const price = row?.quality_pricing?.[$('quality').value] || row || {};
    $('price').textContent = price.image_price_cny ? '¥' + price.image_price_cny + ' / 张' : price.image_price_usd ? '$' + price.image_price_usd + ' / 张' : '按模型广场当前报价结算';
    $('generate').disabled = busy || !token || !$('quality').value;
  }
  function renderModel() {
    const specs = models[$('model').value]?.image_specs || {};
    options($('size'), specs.sizes || [specs.size || '1024x1024']);
    if (specs.size) $('size').value = specs.size;
    renderQualities();
    if ([...$('quality').options].some(o => o.value === specs.default_quality)) { $('quality').value = specs.default_quality; renderQualities(); }
  }
  async function identity() {
    try {
      const r = await fetch('/api/user/auth/refresh', {method:'POST',credentials:'same-origin',cache:'no-store'});
      const p = await r.json();
      if (!r.ok || !p.success || !p.data?.access_token) throw Error();
      token = p.data.access_token;
      $('identity').textContent = '已登录 · ' + (p.data.user?.display_name || p.data.user?.username || 'OmniAPI 账户');
      $('login').textContent = '账户'; $('login').href = '/wallet';
    } catch (_) { token = ''; $('identity').textContent = '登录后即可使用账户余额生成图片'; }
    renderQualities();
  }
  async function catalog() {
    try {
      const r = await fetch('/api/omni/pricing', {cache:'no-store'}), p = await r.json();
      if (!r.ok) throw Error();
      models = Object.fromEntries(Object.entries(p.data?.models || {}).filter(([,row]) => row.image_specs && row.availability !== 'paused'));
      const names = Object.keys(models).sort((a,b) => a === 'gpt-image-2' ? -1 : b === 'gpt-image-2' ? 1 : a.localeCompare(b));
      if (!names.length) throw Error();
      options($('model'), names); $('model').disabled = false; renderModel();
    } catch (_) { $('status').textContent = '暂时无法读取图片模型，请稍后刷新。'; $('model').disabled = true; }
  }
  $('model').addEventListener('change', renderModel); $('size').addEventListener('change', renderQualities); $('quality').addEventListener('change', renderQualities);
  $('download').addEventListener('click', () => { unsaved = false; });
  window.addEventListener('beforeunload', e => { if (busy || unsaved) { e.preventDefault(); e.returnValue = ''; } });
  $('image-form').addEventListener('submit', async e => {
    e.preventDefault(); if (busy || !token || !models[$('model').value]) return;
    const body = {model:$('model').value,prompt:$('prompt').value.trim(),size:$('size').value,quality:$('quality').value,n:1,output_format:'png',response_format:'b64_json'};
    if (!body.prompt) { $('status').textContent = '请先描述想生成的画面。'; return; }
    if (!$('extra-options').hidden) body.service_tier = $('service-tier').value;
    busy = true; $('generate').disabled = true; document.querySelector('.preview').setAttribute('aria-busy','true');
    $('status').textContent = '正在生成，请保持页面打开。通常需要几十秒至数分钟。'; $('usage-link').hidden = true;
    const controller = new AbortController(), timer = setTimeout(() => controller.abort(), 660000);
    let requestID = '';
    try {
      const r = await fetch('/api/omni/images/generations', {method:'POST',credentials:'same-origin',cache:'no-store',headers:{Authorization:'Bearer ' + token,'Content-Type':'application/json'},body:JSON.stringify(body),signal:controller.signal});
      requestID = r.headers.get('X-Oneapi-Request-Id') || '';
      const p = await r.json();
      if (!r.ok) {
        if (r.status === 401) { token = ''; $('identity').textContent = '登录已失效，请重新登录'; $('login').textContent = '登录'; $('login').href = '/sign-in?redirect=%2Fbrand%2Fimage-studio.html'; }
        throw Error(p.error?.message || p.message || '请求失败，请查看调用记录。');
      }
      if (p.data?.length !== 1 || typeof p.data[0].b64_json !== 'string' || p.data[0].b64_json.length > 96 * 1024 * 1024) throw Error('图片返回不完整，请查看调用记录。');
      const raw = atob(p.data[0].b64_json), bytes = Uint8Array.from(raw, c => c.charCodeAt(0));
      if (bytes.length < 24 || ![137,80,78,71,13,10,26,10].every((v,i) => bytes[i] === v)) throw Error('返回的图片格式不符，请查看调用记录。');
      const view = new DataView(bytes.buffer), width = view.getUint32(16), height = view.getUint32(20);
      if (body.size !== width + 'x' + height) throw Error('实际图片尺寸与所选尺寸不符，请将请求编号发给客服。');
      const url = URL.createObjectURL(new Blob([bytes], {type:'image/png'}));
      if (objectURL) URL.revokeObjectURL(objectURL); objectURL = url; unsaved = true;
      $('result').src = url; $('result').hidden = false; $('empty').hidden = true; $('image-actions').hidden = false;
      $('download').href = url; $('download').download = body.model + '-' + Date.now() + '.png';
      $('image-meta').textContent = width + ' × ' + height + ' · ' + body.quality + ' · ' + body.model;
      $('status').textContent = '生成完成，请下载保存。' + (requestID ? ' 请求编号：' + requestID : '');
    } catch (error) {
      $('status').textContent = (error.name === 'AbortError' ? '等待超时，生成状态待核对。' : error.message) + ' 未自动重试，请先核对调用记录。' + (requestID ? ' 请求编号：' + requestID : '');
      $('usage-link').hidden = false;
    } finally { clearTimeout(timer); busy = false; document.querySelector('.preview').setAttribute('aria-busy','false'); renderQualities(); }
  });
  catalog(); identity();
})();
