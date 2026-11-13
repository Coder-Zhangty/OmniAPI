(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const money = cents => '¥' + (cents / 100).toFixed(2);
  const date = n => new Date(n * 1000).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' });
  const terms = { 1: '月付', 3: '季付', 6: '半年付', 12: '年付' };
  let catalog, self, busy = false;
  function el(tag, text, cls) { const e = document.createElement(tag); if (text !== undefined) e.textContent = text; if (cls) e.className = cls; return e; }
  async function confirm(title, copy, action) {
    if (busy) return;
	$('confirm-submit').textContent='确认并扣钱包';
    const key = crypto.randomUUID(); $('confirm-title').textContent = title; $('confirm-copy').textContent = copy;
    $('confirm-error').textContent = ''; $('confirm-submit').disabled = false; $('confirm').showModal();
    $('confirm-submit').onclick = async () => {
      if (busy) return; busy = true; $('confirm-submit').disabled = true;
      try { await action(key); $('confirm').close(); await loadSelf(); $('status').textContent = '操作成功，权益已更新。'; }
      catch (e) { $('confirm-error').textContent = e.message; }
      finally { busy = false; $('confirm-submit').disabled = false; }
    };
  }
  function feature(label, value) { const p = el('p'); p.append(el('span', label, 'feature-label'), el('span', value, 'feature-main')); return p; }
  function render() {
    const months = Number($('term').value); $('plans').replaceChildren();
    document.querySelectorAll('[data-term]').forEach(b => b.setAttribute('aria-pressed', String(Number(b.dataset.term) === months)));
    for (const [i, p] of catalog.plans.entries()) {
      const row = el('article', undefined, 'plan-row' + (i === 2 ? ' is-focus' : ''));
      row.append(el('h3', p.title, 'plan-name'), el('p', ['轻量使用', '日常使用', '持续使用', '高频使用', '更大用量'][i] || '个人订阅', 'plan-kicker'));
      const amount = el('div', undefined, 'plan-price');
      const [whole, decimal] = (p.prices[months] / 100).toFixed(2).split('.');
      amount.append(el('span', '¥', 'currency'), el('span', whole), el('span', decimal === '00' ? '' : '.' + decimal, 'price-decimal'));
      const saving = Math.floor((1 - p.prices[months] / (p.monthly_cents * months)) * 100 + 1e-8);
      row.append(amount, el('p', `${terms[months]} · 一次支付${months === 1 ? '' : '\n折合 ' + money(p.prices[months] / months) + ' / 月 · 约省' + saving + '%'}`, 'plan-period'));
      const b = el('button', catalog.sales_enabled ? '选择套餐' : '待开放'); b.disabled = !catalog.sales_enabled;
      b.onclick = () => confirm('确认购买订阅', `${p.title} · ${months}个自然月\n钱包一次支付 ${money(p.prices[months])}。\n同版本续费延长时间，不补额度；不同权益排队生效。`, key => window.OmniCommerce.request('/api/omni/commerce/purchase', { plan: p.key, months, profile_id: catalog.profile_id, request_key: key }));
      const features = el('div', undefined, 'plan-features');
      features.append(feature('每周额度', p.week_cents ? money(p.week_cents) + ' API等值' : '开售时公布'), feature('五小时限制', p.five_enabled ? (p.five_cents ? money(p.five_cents) + ' API等值' : '适用五小时额度') : '无五小时限制'), feature('恢复额度', '支持单独购买重置'));
      row.append(b, features); $('plans').append(row);
    }
  }
  async function loadSelf() {
    try {
      self = (await window.OmniCommerce.request('/api/omni/self')).data; const s = self.subscription||{}; $('current').replaceChildren();
      if (self.status !== 'active' && self.status !== 'paused') $('current').append(el('p', '当前没有生效中的订阅。', 'caption'));
      else {
        const name = catalog.plans.find(p => p.key === s.plan_key)?.title || ({ starter: '体验套餐', standard: '标准套餐', pro: '专业套餐' }[s.plan_key]) || s.plan_key;
        const title=el('div',undefined,'entitlement-title'); title.append(el('strong',name),el('span',self.status==='paused'?'已暂停':'生效中','status-pill '+self.status)); $('current').append(title,el('p','有效至 '+date(s.end),'window-summary'));
        for (const w of (self.windows||[]).filter(x => x.enabled !== false)) {
          const rate=(s.fx_micros||7000000)/1e6,convert=n=>money(n/self.quota_per_unit*rate*100),box=el('div',undefined,'allowance-window');
          const line=el('div',undefined,'allowance-heading');line.append(el('span',w.key==='five'?'五小时额度':'每周额度'),el('strong',convert(w.remaining)+' 剩余'));box.append(line);
          const bar=el('progress');bar.max=Math.max(1,w.limit);bar.value=Math.max(0,w.remaining);bar.setAttribute('aria-label',(w.key==='five'?'五小时':'周')+'剩余额度');box.append(bar);
          box.append(el('p','已用 '+convert(w.used??Math.max(0,w.limit-w.remaining))+' / 总额 '+convert(w.limit)+' · 人民币计价等值','caption'),el('p',date(w.reset_at)+' 恢复','caption'));$('current').append(box);
        }
        for (const term of self.queued_terms || []) $('current').append(el('p', `${catalog.plans.find(p=>p.key===term.plan_key)?.title||'待生效套餐'} · 已购${term.months}个月，预计 ${date(term.start)} 生效。`, 'window-summary'));
      }
      const included=self.included_models||catalog.models||[];$('models').replaceChildren(...included.map(name=>el('span',name,'model-token')));$('model-count').textContent=included.length+' 个可用模型';
      $('reset').disabled = !catalog.sales_enabled || self.status !== 'active' || !!s.exit_application_id;
      window.OmniMembershipSelf = self;
      window.dispatchEvent(new CustomEvent('omni-membership-loaded', { detail: self }));
    } catch (e) {
      const a = el('a', '前往登录 →', 'text-link'); a.href = '/sign-in?redirect=%2Fmembership'; a.setAttribute('data-console-link', '');
      $('current').replaceChildren(el('p', e.message, 'caption'));if(e.status===401||/登录/.test(e.message))$('current').append(a);
    }
  }
  $('term').onchange = () => catalog && render();
  document.querySelectorAll('[data-term]').forEach(b => b.onclick = () => { $('term').value = b.dataset.term; render(); });
  $('refresh').onclick = loadSelf;
  $('reset').onclick = async () => {
    try {
      const q = (await window.OmniCommerce.request('/api/omni/commerce/reset')).data, unit = self.quota_per_unit;
      const convert=n=>money(n/unit*(self.subscription?.fx_micros||7000000)/1e6*100);
      await confirm('恢复当前额度', `${q.five_enabled ? '五小时恢复 ' + convert(q.five_restore) + ' API等值\n' : ''}周额度恢复 ${convert(q.week_restore)} API等值\n价格 ${money(q.cents)}\n下次周恢复：${date(q.week_reset_at)}${q.five_enabled ? '\n下次五小时恢复：' + date(q.five_reset_at) : ''}\n不延长订阅，也不改变上述恢复时间。有请求进行中时不可执行。`, key => window.OmniCommerce.request('/api/omni/commerce/reset', { request_key: key, quote: q }));
    } catch (e) { $('reset-note').textContent = e.message; }
  };
  (async () => {
    try {
      const p = await (await fetch('/api/omni/catalog', { cache: 'no-store' })).json(); catalog = p.data.commerce_v3;
      if (!catalog) throw Error('套餐资料暂不可用，请稍后重试。'); render();
      $('status').textContent = catalog.sales_enabled ? '套餐已开放，具体权益以购买时确认的信息为准。' : '新套餐准备中 · 全部模型与能力验收完成后统一迁移并开放购买。';
      $('status').parentElement.dataset.live = String(catalog.sales_enabled);
      $('models').textContent = catalog.models?.length ? catalog.models.join(' · ') : '可用模型及具体额度将在开售时公布。'; await loadSelf();
    } catch (e) { $('status').textContent = e.message; }
  })();
})();
