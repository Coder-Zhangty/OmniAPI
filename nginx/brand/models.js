(() => {
  'use strict';
  const root = document.getElementById('models'), more = document.getElementById('more');
  const count = document.getElementById('count'), error = document.getElementById('error');
  const money = n => { const parts = String(n).split('.'); return '$' + parts[0].replace(/\B(?=(\d{3})+(?!\d))/g, ',') + '.' + (parts[1] || '').padEnd(2, '0'); };
  function prices(expr) {
    const result = {};
    for (const variable of ['p', 'c', 'cr', 'cc', 'cc1h']) {
      const match = String(expr || '').match(new RegExp('(?:^|[:+\\s])' + variable + '\\s*\\*\\s*([0-9.]+(?:[eE][+-]?[0-9]+)?)', 'i'));
      if (match) {
        const number = match[1].match(/^(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/);
        if (!number) continue;
        const digits = number[1] + (number[2] || ''), point = number[1].length + Number(number[3] || 0);
        if (Math.abs(point) > 100) continue;
        const plain = point <= 0 ? '0.' + '0'.repeat(-point) + digits : point >= digits.length ? digits + '0'.repeat(point - digits.length) : digits.slice(0, point) + '.' + digits.slice(point);
        result[variable] = plain.replace(/^0+(?=\d)/, '');
      }
    }
    return result.p && result.c ? result : null;
  }
  async function json(url) { const r = await fetch(url, { cache: 'no-store' }); if (!r.ok) throw Error('HTTP ' + r.status); return r.json(); }
  Promise.all([json('/api/pricing'), json('/api/omni/pricing').catch(() => ({ data: { models: {}, waiting: {} } }))]).then(([native, audit]) => {
    const snapshot = audit.data || {}, verified = snapshot.models || {}, waiting = snapshot.waiting || {};
    const rows = (native.data || native.items || native || []).sort((a, b) => a.model_name.localeCompare(b.model_name));
    let common = 0;
    for (const m of rows) {
      const name = m.model_name, p = prices(m.billing_expr) || {}, meta = verified[name];
      const aligned = meta && meta.billing_expr === m.billing_expr;
      const imagePrice = aligned && meta.retail && meta.retail.image;
      const row = document.createElement('article'); row.className = 'model-row';
      const title = document.createElement('div'); title.className = 'name'; title.textContent = name;
      const input = document.createElement('div'); input.className = 'price'; input.textContent = imagePrice ? money(imagePrice) + ' / 张' : p.p ? money(p.p) : '待核验';
      const output = document.createElement('div'); output.className = 'price'; output.textContent = imagePrice ? '—' : p.c ? money(p.c) : '待核验';
      const mode = document.createElement('div'); mode.className = 'mode';
      mode.textContent = aligned ? (meta.protocols || []).join(' / ') + (meta.eligible ? ' · 新售订阅包含' : ' · 钱包计费') : '钱包计费 · 待核验';
      const detail = document.createElement('small'); detail.style.display = 'block';
      const specs = meta && meta.image_specs || {};
      const dimensions = (specs.sizes || [specs.size || '1024x1024']).map(s => s.replace('x','×')).join(' / ');
      detail.textContent = [!imagePrice && aligned && (meta.restrictions || []).includes('input_images_token_priced') && '支持读图 · PNG / JPEG · 图片按输入 Token 计费', imagePrice && ('图片按张计费 · ' + dimensions + ' · ' + (specs.qualities || ['low']).join(' / ') + ' · PNG'), p.cr && '缓存读取 ' + money(p.cr), p.cc && '缓存写入 ' + money(p.cc), p.cc1h && '1小时缓存 ' + money(p.cc1h), aligned && meta.verified_at && '更新 ' + meta.verified_at, waiting[name] || (!aligned && '暂不可用')].filter(Boolean).join(' · ');
      title.append(detail); row.append(title, input, output, mode);
      if (aligned && (imagePrice || p.p && p.c) && meta.common) { root.append(row); common++; } else more.append(row);
    }
    count.textContent = common + ' 个稳定常用模型 · ' + rows.length + ' 个价格记录';
    if (!common) error.textContent = '模型信息暂未加载，请前往模型广场查看。';
  }).catch(e => { count.textContent = '价格加载失败'; error.textContent = '暂时无法读取价格，请稍后刷新。'; console.error(e); });
})();
