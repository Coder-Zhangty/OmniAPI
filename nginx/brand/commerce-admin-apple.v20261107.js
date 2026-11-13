(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const plans = { v3_19:'19元套餐',v3_39:'39元套餐',v3_69:'69元套餐',v3_99:'99元套餐',v3_199:'199元套餐', starter: '体验套餐', standard: '标准套餐', pro: '专业套餐' };
  const states = { fulfilled: '已开通', pending: '待处理', awaiting_payment: '待付款', cancelled: '已取消', expired: '已过期', failed: '失败', retired_unpaid:'历史未付款已关闭' };
  const closed = row => ['cancelled', 'expired', 'failed', 'retired_unpaid'].includes(row.state);
  const unfinished = row => row.state !== 'fulfilled' && !closed(row);
  const money = cents => Number.isFinite(Number(cents)) ? '¥' + (Number(cents) / 100).toFixed(2) : '—';
  const period = months => months === 12 ? '年付' : months === 1 ? '月付' : `${months} 个月`;
  const planName = row => plans[row.plan_key] || row.plan_key || '未知套餐';
  const payment = row => ({ wallet: '钱包余额', wechat: '微信（历史）', alipay: '支付宝（历史）' })[row.payment_method] || '未记录';
  const zone = { timeZone: 'Asia/Shanghai', hour12: false };
  let rows = [], page = 1, loading = false, loaded = false, currentOrder;
  function element(tag, text, className) {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    if (className) node.className = className;
    return node;
  }
  function tell(node, text, error = false) {
    node.textContent = text;
    node.hidden = !text;
    node.classList.toggle('error', error);
  }
  function badge(row) {
    let variant = '';
    if (row.state === 'fulfilled') variant = ' fulfilled';
    else if (unfinished(row)) variant = ' pending';
    else if (row.state === 'failed') variant = ' failed';
    return element('span', states[row.state] || '未知状态', 'state' + variant);
  }
  function datetime(stamp, full = false) {
    if (!Number(stamp)) return '—';
    const date = new Date(Number(stamp) * 1000);
    if (!Number.isFinite(date.getTime())) return '—';
    if (full) return date.toLocaleString('zh-CN', zone) + '（北京时间）';
    return date.toLocaleDateString('zh-CN', { ...zone, month: '2-digit', day: '2-digit' });
  }
  function showDetail(row) {
    currentOrder = row;
    $('order-dialog-title').textContent = planName(row) + ' · ' + period(row.months);
    $('copy-message').hidden = true;
    const list = element('dl', undefined, 'detail-list');
    const fields = [['订单号', row.id], ['用户', `用户 #${row.user_id}`], ['创建时间', datetime(row.created_at, true)], ['实付金额', money(row.cents)], ['支付方式', payment(row)], ['状态', states[row.state] || `未知状态（${row.state || '未记录'}）`]];
    if (row.payment_method !== 'wallet' && row.payment_reference) fields.push(['交易参考号', row.payment_reference]);
    fields.forEach(([label, value]) => { const item = element('div'); item.append(element('dt', label), element('dd', value)); list.append(item); });
    let note = '历史扫码订单请结合真实收款记录核对，到充值核验或客服工作台处理，避免重复收款。';
    if (row.payment_method === 'wallet') note = row.state === 'fulfilled' ? '此订单已使用钱包余额购买并开通，无需人工核验。' : '此钱包订单尚未完成，请先核对订单及用户订阅状态。';
    $('order-detail').replaceChildren(list, element('p', note, 'detail-note'));
    $('order-dialog').showModal();
  }
  function filtered() {
    const search = $('order-search').value.trim().toLowerCase();
    const state = $('order-state').value, plan = $('order-plan').value;
    return rows.filter(row => {
      if (search && !String(row.id).toLowerCase().includes(search) && !String(row.user_id).includes(search.replace(/^#/, ''))) return false;
      if (plan && row.plan_key !== plan) return false;
      if (state === 'fulfilled' && row.state !== 'fulfilled') return false;
      if (state === 'unfinished' && !unfinished(row)) return false;
      if (state === 'closed' && !closed(row)) return false;
      return true;
    });
  }
  function renderOrders() {
    const selected = filtered(), pages = Math.max(1, Math.ceil(selected.length / 10));
    page = Math.max(1, Math.min(page, pages));
    const start = (page - 1) * 10;
    const fragment = document.createDocumentFragment();
    selected.slice(start, start + 10).forEach(row => {
      const tr = element('tr');
      const dateCell = element('td', datetime(row.created_at));
      if (Number(row.created_at)) dateCell.append(element('span', new Date(Number(row.created_at) * 1000).toLocaleTimeString('zh-CN', { ...zone, hour: '2-digit', minute: '2-digit' }), 'cell-sub'));
      const userCell = element('td'); userCell.append(element('span', `用户 #${row.user_id}`, 'cell-primary'), element('span',row.id,'cell-sub order-number'));
      const planCell = element('td'); planCell.append(element('span', planName(row), 'cell-primary'), element('span', period(row.months), 'cell-sub'));
      const statusCell = element('td'); statusCell.append(badge(row));
      const numberCell = element('td'); numberCell.append(element('span', String(row.id).length > 17 ? String(row.id).slice(0, 7) + '…' + String(row.id).slice(-6) : row.id, 'order-number'));
      const action = element('td'), detail = element('button', '详情', 'text-button'); detail.type = 'button'; detail.setAttribute('aria-label', `查看用户 ${row.user_id} 的订单详情`); detail.addEventListener('click', () => showDetail(row)); action.append(detail);
      tr.append(userCell,planCell,element('td',money(row.cents),'numeric amount'),statusCell,dateCell,action); fragment.append(tr);
    });
    $('orders').replaceChildren(fragment);
    const activeFilter = Boolean($('order-search').value || $('order-state').value || $('order-plan').value);
    $('clear-filters').hidden = !activeFilter;
    $('order-count').textContent = `匹配 ${selected.length} 条`;
    $('orders-empty').hidden = selected.length > 0;
    $('empty-title').textContent = activeFilter ? '没有匹配的订单' : '暂无订阅订单';
    $('empty-description').textContent = activeFilter ? '尝试更换搜索内容，或清除筛选。' : '订阅购买后，订单记录会显示在这里。';
    $('page-info').textContent = selected.length ? `${start + 1}–${Math.min(start + 10, selected.length)} / ${selected.length} 条` : '0 条';
    $('prev-page').disabled = page === 1;
    $('next-page').disabled = page >= pages;
    $('orders-total').textContent = rows.length;
    $('orders-fulfilled').textContent = rows.filter(row => row.state === 'fulfilled').length;
    const pending = rows.filter(unfinished).length;
    $('orders-pending').textContent = pending;
    $('pending-count').textContent = pending; $('pending-count').hidden = !pending;
  }
  async function load() {
    if (loading) return;
    loading = true; $('reload').disabled = true; $('reload').textContent = '正在刷新…';
    try {
      const user = await window.OmniCommerce.identity();
      if (!(Number(user?.role) >= 10)) { $('workbench').hidden = true; throw Error('此页面仅供管理员使用，请使用管理员账号登录。'); }
      $('status').textContent = '管理员身份已验证'; $('status').classList.add('verified');
      const result = await window.OmniCommerce.request('/api/omni/admin/orders');
      if (!Array.isArray(result.data)) throw Error('订单数据格式异常，请稍后刷新。');
      rows = result.data; loaded = true; renderOrders(); $('workbench').hidden = false;
      $('page-retry').hidden = true;
      $('orders-updated').textContent = '更新于 ' + new Date().toLocaleTimeString('zh-CN', { ...zone, hour: '2-digit', minute: '2-digit' });
      tell($('page-message'), '');
    } catch (error) {
      if (!loaded || error.status === 401 || error.status === 403) { $('workbench').hidden = true; $('status').textContent = '身份或数据未验证'; $('status').classList.remove('verified'); }
      tell($('page-message'), loaded ? '刷新失败，当前保留上次读取的订单。' + error.message : error.message, true);
      $('page-retry').hidden = false;
    } finally { loading = false; $('reload').disabled = false; $('reload').textContent = '↻ 刷新订单'; $('page-retry').disabled = false; }
  }
  async function loadFx() {
    try {
      const response = await fetch('/brand/api/subscription/fx', { cache: 'no-store' }); const fx = await response.json();
      if (!response.ok || !fx.success) throw Error();
      $('fx-note').textContent = `¥${fx.rate} / US$ · ${fx.source} · ${fx.as_of}${fx.note ? ' · ' + fx.note : ''}`;
    } catch { $('fx-note').textContent = '汇率参考暂时无法读取，订单列表仍可正常查看。'; }
  }
  ['order-search', 'order-state', 'order-plan'].forEach(id => $(id).addEventListener(id === 'order-search' ? 'input' : 'change', () => { page = 1; renderOrders(); }));
  $('clear-filters').addEventListener('click', () => { ['order-search', 'order-state', 'order-plan'].forEach(id => { $(id).value = ''; }); page = 1; renderOrders(); $('order-search').focus(); });
  $('prev-page').addEventListener('click', () => { page--; renderOrders(); }); $('next-page').addEventListener('click', () => { page++; renderOrders(); });
  $('reload').addEventListener('click', () => { load(); loadFx(); });
  $('page-retry').addEventListener('click', () => { $('page-retry').disabled = true; load(); loadFx(); });
  ['close-order', 'done-order'].forEach(id => $(id).addEventListener('click', () => $('order-dialog').close()));
  async function copy(text, node) { try { await navigator.clipboard.writeText(text); tell(node, '已复制。'); } catch { tell(node, '复制失败，请手动选择并复制。', true); } }
  $('copy-order').addEventListener('click', () => copy(currentOrder?.id || '', $('copy-message')));
  $('copy-issued').addEventListener('click', () => copy($('issued').textContent, $('grant-message')));
  ['grant', 'revoke'].forEach(id => {
    const form = $(id); let busy = false;
    form.addEventListener('submit', async event => {
      event.preventDefault(); if (busy) return; busy = true;
      const button = form.querySelector('button[type="submit"]'), original = button.textContent, data = new FormData(form), feedback = $(id + '-message');
      button.disabled = true; button.textContent = id === 'grant' ? '正在发放…' : '正在撤销…'; tell(feedback, '');
      try {
        if (id === 'grant') {
          const result = await window.OmniCommerce.request('/api/omni/admin/cards', { user_id: Number(data.get('uid')), kind: data.get('kind'), count: Number(data.get('count')) }, false);
          $('issued').textContent = (result.data.codes || []).join('\n'); $('issued-result').hidden = false;
          tell(feedback, `已向用户 #${data.get('uid')} 发放 ${result.data.codes.length} 张重置卡。`);
        } else {
          const result = await window.OmniCommerce.request('/api/omni/admin/cards/revoke', { id: data.get('id') }, false);
          tell(feedback, result.data ? '卡片已撤销。' : '卡片不存在或已使用，未撤销。', !result.data);
        }
      } catch (error) { tell(feedback, error.message + (id === 'grant' && (!error.status || error.status >= 500) ? ' 请先核对用户卡片，确认结果后再重试。' : ''), true); }
      finally { busy = false; button.disabled = false; button.textContent = original; }
    });
  });
  load(); loadFx();
})();
