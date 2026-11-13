(() => {
  'use strict';
  const form = document.getElementById('routing-policy');
  if (!form) return;
  const status = document.getElementById('routing-status'), report = document.getElementById('routing-report'), reload = document.getElementById('routing-reload');
  let policy, loading = false, loaded = false;
  const field = name => form.elements.namedItem(name);
  function node(tag, text) { const el = document.createElement(tag); if (text !== undefined) el.textContent = text; return el; }
  function renderReport(rows) {
    if (!Array.isArray(rows) || !rows.length) { const empty = node('p', '最近 24 小时暂无线路记录。'); empty.className = 'empty muted'; report.replaceChildren(empty); return; }
    const table = node('table'); table.className = 'routing-table';
    const caption = node('caption', '最近24小时模型线路记录，成本为估算'); caption.className = 'sr-only'; table.append(caption);
    const head = node('thead'), header = node('tr'); ['模型', '线路', '结果', '次数', '平均首段耗时', '预估成本'].forEach(label => header.append(node('th', label))); head.append(header);
    const body = node('tbody'), outcomes = { success: '成功', failure: '失败', rejected: '已拒绝', cancelled: '已取消', timeout: '超时' };
    rows.forEach(row => {
      const tr = node('tr');
      const first = Number(row.average_first_millis);
      const cost = Number(row.estimated_cost_usd);
      [row.model, '#' + row.channel_id, outcomes[row.outcome] || row.outcome, Number(row.count).toLocaleString('zh-CN'), first > 0 ? (first / 1000).toFixed(2) + ' 秒' : '—', Number.isFinite(cost) ? '$' + cost.toFixed(4) : '—'].forEach(value => tr.append(node('td', value)));
      body.append(tr);
    });
    table.append(head, body); report.replaceChildren(table);
  }
  async function load() {
    if (loading) return;
    loading = true; if (reload) reload.disabled = true;
    try {
      const identity = await window.OmniCommerce.identity();
      if (Number(identity.role) < 10) throw Error('仅管理员可查看线路设置。');
      const result = await window.OmniCommerce.request('/api/omni/admin/routing');
      policy = result.data.policy;
      for (const name of ['first_seconds', 'idle_seconds', 'failure_threshold', 'cooldown_seconds']) field(name).value = policy[name];
      field('enabled').checked = policy.enabled; field('continuation_enabled').checked = policy.continuation_enabled;
      field('models').value = JSON.stringify(policy.models, null, 2);
      renderReport(result.data.last_24_hours);
      status.textContent = `价格版本：${result.data.pricing_version || '待核验'}。线路顺序以已验收主线路为首，仅允许已验收备用。`;
      status.classList.remove('error'); form.hidden = false; loaded = true;
    } catch (error) { status.textContent = error.message; status.classList.add('error'); form.hidden = true; }
    finally { loading = false; if (reload) reload.disabled = false; }
  }
  form.addEventListener('submit', async event => {
    event.preventDefault();
    const submit = form.querySelector('button[type="submit"]');
    if (submit.disabled) return;
    submit.disabled = true; submit.textContent = '正在保存…'; if (reload) reload.disabled = true;
    try {
      const next = { ...policy, enabled: field('enabled').checked, continuation_enabled: field('continuation_enabled').checked, models: JSON.parse(field('models').value) };
      for (const name of ['first_seconds', 'idle_seconds', 'failure_threshold', 'cooldown_seconds']) next[name] = Number(field(name).value);
      await window.OmniCommerce.request('/api/omni/admin/routing', next, false);
      await load(); if (!form.hidden) status.textContent += ' 设置已保存。';
    } catch (error) { status.textContent = error.message; status.classList.add('error'); }
    finally { submit.disabled = false; submit.textContent = '保存线路设置'; if (reload) reload.disabled = false; }
  });
  if (reload) reload.addEventListener('click', load);
  document.addEventListener('omni-admin-panel', event => { if (event.detail === 'routing-panel' && !loaded) load(); });
})();
