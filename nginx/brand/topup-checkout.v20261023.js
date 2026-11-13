(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const params = new URLSearchParams(location.search);
  if (params.get('manual') === '1') { location.replace('/brand/topup-manual.html' + location.search); return; }
  const names = { alipay: '支付宝', wechat: '微信支付' };
  const rejectedCodes = new Set(['fee_error', 'not_exist', 'sign_error', 'no_contract', 'no_alipay_contract', 'app_off', 'pay_type_error', 'alipay_api_error', 'wechat_api_error']);
  let account, token = '', order = null, creation = null, busy = false, checking = false, enabled = false, listReady = false;
  let policy = { channels: {} }, fx = null, method = 'alipay', view = '', phase = '', poll, countdownTimer, clockOffset = 0, qrBroken = false, intent = null;
  const now = () => Date.now() / 1000 + clockOffset;
  const yuan = n => '¥' + (n / 100).toFixed(2);
  function cents(value) { if (!/^\d{1,5}(?:\.\d{1,2})?$/.test(value)) return 0; const [whole, fraction = ''] = value.split('.'); return Number(whole) * 100 + Number((fraction + '00').slice(0, 2)); }
  const valid = () => cents($('amount').value.trim()) >= 100 && cents($('amount').value.trim()) <= 1000000;
  const key = () => 'omni.xorpay.' + account.id;
  function save(value) { try { value ? localStorage.setItem(key(), JSON.stringify(value)) : localStorage.removeItem(key()); } catch (_) {} }
  function saved() { try { return JSON.parse(localStorage.getItem(key()) || 'null'); } catch (_) { return null; } }
  const canUse = m => enabled && listReady && policy.channels?.[m]?.available === true;
  const anyAutomatic = () => Object.keys(names).some(canUse);
  const rejected = row => row && !row.qr && row.error_code === 'provider_rejected' && rejectedCodes.has(row.create_status || row.provider_status);
  const expired = row => row?.expires_at > 0 && row.expires_at <= now();
  const uncertain = () => !!creation || !!(order && order.state !== 'credited' && !rejected(order));
  const resumable = row => row.state !== 'credited' && !rejected(row) && (!expired(row) || ['unknown', 'creating', 'review'].includes(row.state));
  function notice(text) { $('notice').textContent = text || ''; $('notice').hidden = !text; }
  function orderMessage(text) { $('order-message').textContent = text || ''; $('order-message').hidden = !text; }
  function show(next, focus = false) {
    view = next;
    for (const id of ['loading', 'login-view', 'load-error', 'choose-view', 'payment-view', 'switch-confirm']) $(id).hidden = id !== next;
    $('resume-banner').hidden = next !== 'choose-view' || !order || order.state === 'credited' || rejected(order);
    if (focus) (next === 'switch-confirm' ? $('confirm-title') : next === 'payment-view' ? $('payment-title') : $('amount')).focus({ preventScroll: true });
  }
  async function readJSON(path, options = {}, clock = false) {
    const r = await fetch(path, { credentials: 'same-origin', cache: 'no-store', ...options });
    const stamp = clock && r.headers?.get('date');
    if (stamp && Number.isFinite(Date.parse(stamp))) clockOffset = Date.parse(stamp) / 1000 - Date.now() / 1000;
    const p = await r.json().catch(() => ({}));
    if (!r.ok || p.success === false) {
      const error = Error(r.status >= 500 ? '服务暂时无法响应，请稍后重试。' : p.message || '请求未完成，请重试。');
      error.status = r.status; throw error;
    }
    return Object.hasOwn(p, 'data') ? p.data : p;
  }
  async function identity() {
    const previous = account?.id;
    const result = await readJSON('/api/user/auth/refresh', { method: 'POST' }, true);
    if (!result?.user?.id || !result.access_token) { const e = Error('请先登录。'); e.status = 401; throw e; }
    account = result.user; token = result.access_token;
    $('account-name').textContent = account.display_name || account.username || '当前账户';
    $('account-check').hidden = false;
    if (previous && previous !== account.id) { order = null; creation = null; const e = Error('登录账号已切换，请刷新后继续。'); e.status = 401; throw e; }
  }
  async function api(path, body, retry = true) {
    try { return await readJSON(path, { method: body === undefined ? 'GET' : 'POST', headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) }, true); }
    catch (e) { if (e.status === 401 && retry) { await identity(); return api(path, body, false); } throw e; }
  }
  function sessionFailure(e) {
    if (e.status !== 401 && e.status !== 403) return false;
    clearInterval(poll); clearInterval(countdownTimer); token = ''; $('account-name').textContent = '请重新登录'; $('account-check').hidden = true;
    notice('登录状态已失效。重新登录后可以继续查看原订单。'); show('login-view'); return true;
  }
  function formatQuota(quota) {
    const q = BigInt(String(quota || 0));
    return (q / 500000n).toString() + '.' + ((q % 500000n) * 2n).toString().padStart(6, '0');
  }
  function estimate() {
    $('estimate').hidden = !anyAutomatic() || !valid() || !fx || !/^\d{1,2}(?:\.\d{1,8})?$/.test(fx.rate);
    if ($('estimate').hidden) return;
    const [whole, decimal = ''] = fx.rate.split('.'), rate = BigInt(whole + decimal);
    if (rate <= 0n) { $('estimate').hidden = true; return; }
    const micros = BigInt(cents($('amount').value.trim())) * (10n ** BigInt(decimal.length)) * 10000n / rate;
    $('estimate-value').textContent = 'US$' + formatQuota(micros / 2n);
  }
  function updateForm() {
    const automatic = anyAutomatic(), frozen = busy || !!creation;
    if (automatic && !canUse(method)) method = Object.keys(names).find(canUse);
    $('amount').disabled = frozen;
    $('amount').setAttribute('aria-invalid', String(!valid())); $('amount-shell').classList.toggle('is-invalid', !valid());
    $('amount-error').hidden = valid();
    document.querySelectorAll('[data-amount]').forEach(b => { b.disabled = frozen; b.setAttribute('aria-pressed', String(cents(b.dataset.amount) === cents($('amount').value.trim()) && valid())); });
    for (const m of Object.keys(names)) {
      const input = document.querySelector('input[name="method"][value="' + m + '"]');
      input.disabled = frozen || !canUse(m); input.checked = automatic && m === method;
      $('method-' + m).classList.toggle('is-selected', input.checked); $('method-' + m).classList.toggle('is-unavailable', !canUse(m));
      $(m + '-status').textContent = canUse(m) ? (m === 'wechat' ? '微信扫码 · 自动到账' : '扫码 / 手机支付') : '暂不可用';
    }
    $('method-caption').textContent = automatic ? '付款确认后自动到账' : '自动通道暂不可用';
    $('availability-note').hidden = automatic;
    $('availability-text').textContent = '自动充值通道维护中。你仍可使用人工充值，付款后提交核验。';
    $('pay-button').disabled = !account || !valid() || frozen || !listReady;
    $('pay-button').textContent = busy ? '正在创建付款单…' : automatic ? '确认金额，前往付款' : '使用人工充值';
    $('action-note').textContent = automatic ? '按本单锁定汇率折算，手续费由平台承担。' : '人工充值需要管理员核验，不会立即到账。';
    $('manual-link').hidden = !automatic;
    $('manual-link').href = manualURL();
    $('resume-title').textContent = order ? yuan(order.cents) + ' · ' + names[order.method] + '充值待确认' : '';
    $('resume-note').textContent = '如已付款，请先查看这笔订单的到账结果。';
    const isAdmin = Number(account?.role) >= 10;
    $('admin-note').hidden = !isAdmin;
    $('admin-note-text').textContent = Object.keys(names).map(m => names[m] + '：' + (canUse(m) ? '已开放。' : policy.channels?.[m]?.admin_reason || '尚未开放自动充值。')).join(' ');
    estimate();
  }
  function manualURL() {
    return '/brand/topup.html?manual=1&method=' + encodeURIComponent(method || 'alipay') + '&amount=' + encodeURIComponent($('amount').value.trim());
  }
  function stage() {
    if (creation && !order) return busy ? 'creating' : 'uncertain';
    if (!order) return 'uncertain';
    if (order.state === 'credited') return 'credited';
    if (order.state === 'review') return 'review';
    if (rejected(order)) return 'rejected';
    if (['unknown', 'creating'].includes(order.state) || !order.qr) return 'uncertain';
    if (expired(order) || order.state === 'expired') return 'expired';
    if (policy.channels?.[order.method]?.available !== true) return 'maintenance';
    if (!order.qr_image || qrBroken) return 'image-error';
    return 'paying';
  }
  function applyOrder(row) {
    if (!row?.id) throw Error('订单结果未确认，请继续检查这次下单。');
    if (row.id !== order?.id || row.qr_image !== order?.qr_image) qrBroken = false;
    order = row; creation = null; save({ id: row.id });
    if (row.state === 'credited') { clearInterval(poll); window.dispatchEvent(new Event('omni:wallet-refresh')); }
    if (view === 'payment-view' || view === 'switch-confirm') drawPayment(); else updateForm();
  }
  function drawPayment(focus = false) {
    phase = stage(); clearInterval(countdownTimer);
    const m = order?.method || creation?.method || method, amount = order?.cents || cents(creation?.amount || '0');
    let title, description, badge, tone = 'neutral';
    if (phase === 'paying') { title = '使用' + names[m] + '付款'; description = '付款确认后自动计入钱包，无需提交核验。'; badge = '等待付款'; }
    else if (phase === 'credited') { title = '充值已到账'; description = '余额已计入当前账户，可以返回钱包查看。'; badge = '已完成'; tone = 'success'; }
    else if (phase === 'expired') { title = '付款码已过期'; description = '这张付款码已停止展示。如已付款，请先查询到账结果。'; badge = '已过期'; tone = 'warning'; }
    else if (phase === 'maintenance') { title = names[m] + '暂不可用'; description = '该支付通道正在维护。如你已付款，仍可查询原订单到账结果。'; badge = '通道维护'; tone = 'warning'; }
    else if (phase === 'image-error') { title = '付款码暂时无法显示'; description = '重新加载这笔订单的付款码即可，无需再次下单。'; badge = '加载异常'; tone = 'warning'; }
    else if (phase === 'review') { title = '这笔充值需要核对'; description = '请联系站内支持核对原订单，暂时不要再次付款。'; badge = '核对中'; tone = 'warning'; }
    else if (phase === 'rejected') { title = '这次未能发起付款'; description = names[m] + '通道暂不可用，未生成付款码。可返回选择其他充值方式。'; badge = '未发起付款'; tone = 'warning'; }
    else if (phase === 'creating') { title = '正在创建付款单'; description = '正在确认金额与付款方式，请稍候。'; badge = '处理中'; }
    else { title = '正在确认订单结果'; description = '暂未收到明确结果，请继续查询这笔订单，避免再次付款。'; badge = '待确认'; tone = 'warning'; }
    $('payment-title').textContent = title; $('payment-description').textContent = description;
    $('payment-badge').textContent = badge; $('payment-badge').dataset.tone = tone;
    $('payment-amount').textContent = amount ? yuan(amount) : ''; $('payment-amount').hidden = phase === 'credited';
    $('qr-area').hidden = phase !== 'paying'; $('countdown').hidden = phase !== 'paying';
    $('open-alipay').hidden = phase !== 'paying' || m !== 'alipay';
    if (phase === 'paying') {
      if ($('payment-qr').getAttribute('src') !== order.qr_image) $('payment-qr').src = order.qr_image;
      $('qr-instruction').textContent = m === 'wechat' ? '使用另一部手机的微信扫一扫，核对金额后付款。' : '使用支付宝扫一扫，或在手机上直接打开支付宝。';
      $('save-qr').href = order.qr_image; $('save-qr').download = order.id + '.png';
      if (m === 'alipay') $('open-alipay').href = 'alipays://platformapi/startapp?appId=20000067&url=' + encodeURIComponent(order.qr);
      const tick = () => { const left = Math.max(0, Math.ceil(order.expires_at - now())); if (!left) { drawPayment(); return; } $('countdown').textContent = Math.floor(left / 60) + ':' + String(left % 60).padStart(2, '0') + ' 后过期'; };
      tick(); if (phase === 'paying') countdownTimer = setInterval(tick, 1000);
    }
    $('result-summary').hidden = phase !== 'credited';
    if (phase === 'credited') { $('credited-value').textContent = 'US$' + formatQuota((order.quota || 0) + (order.bonus || 0)); $('credited-note').textContent = order.bonus ? '基础额度 US$' + formatQuota(order.quota) + '，另含赠送 US$' + formatQuota(order.bonus) + '。' : '已按本单锁定汇率计入钱包。'; }
    $('check-order').hidden = !order || ['credited', 'creating', 'image-error', 'rejected'].includes(phase);
    $('check-order').className = ['maintenance', 'expired'].includes(phase) ? 'text-button' : 'button ' + (phase === 'paying' ? 'secondary' : 'primary');
    $('check-order').textContent = checking ? '正在查询…' : '查询到账状态';
    $('check-order').disabled = checking || busy;
    $('retry-create').hidden = !creation || busy; $('retry-create').disabled = busy;
    $('reload-qr').hidden = phase !== 'image-error'; $('reload-qr').disabled = checking;
    $('go-wallet').hidden = phase !== 'credited'; $('back-to-form').disabled = busy;
    $('back-to-form').className = ['maintenance', 'expired', 'rejected'].includes(phase) ? 'button primary' : 'text-button';
    $('back-to-form').textContent = phase === 'credited' ? '再充值一笔' : phase === 'expired' ? '重新选择金额' : ['rejected', 'maintenance'].includes(phase) ? '选择其他充值方式' : '返回充值';
    $('order-details').hidden = !order;
    if (order) {
      $('order-id').textContent = order.id; $('order-method').textContent = names[m]; $('order-rate').textContent = order.fx_rate + ' CNY / USD';
      $('order-usd').textContent = 'US$' + (order.usd || formatQuota(order.quota)); $('order-source').textContent = [order.fx_source, order.fx_as_of, order.fx_note].filter(Boolean).join(' · ');
    }
    show('payment-view', focus);
  }
  async function refresh(check = false) {
    if (!order || busy || checking || document.hidden) return;
    const id = order.id; checking = true;
    if (view === 'payment-view') drawPayment();
    try {
      const result = await api('/api/omni/topups/' + encodeURIComponent(id) + (check ? '/check' : ''), check ? {} : undefined);
      if (order?.id !== id) return;
      applyOrder(result);
      if (check) orderMessage(result.state === 'credited' ? '充值已到账。' : '已查询原订单，尚未确认到账。');
    } catch (e) { if (!sessionFailure(e)) orderMessage('暂时无法确认到账，请稍后再次查询原订单。'); }
    finally { checking = false; if (view === 'payment-view') drawPayment(); }
  }
  function startPoll() { clearInterval(poll); if (order?.state !== 'credited') poll = setInterval(() => refresh(), 3000); }
  function askSwitch(callback) { intent = callback; notice(''); show('switch-confirm', true); }
  function goManual(href = manualURL()) {
    if (busy) return;
    if (uncertain()) { askSwitch(() => { location.assign(href); }); return; }
    location.assign(href);
  }
  async function createOrder(retryOriginal = false) {
    if (busy || !account) return;
    if (!retryOriginal && !valid()) { $('amount-error').hidden = false; $('amount').focus(); return; }
    if (!retryOriginal && !anyAutomatic()) { goManual(); return; }
    if (!retryOriginal && uncertain()) { askSwitch(() => { order = null; creation = null; save(null); createOrder(); }); return; }
    if (!creation) creation = { request_id: crypto.randomUUID(), method, amount: $('amount').value.trim() };
    order = null; save(creation); busy = true; notice(''); orderMessage(''); updateForm(); drawPayment(true);
    try { applyOrder(await api('/api/omni/topups', creation)); startPoll(); }
    catch (e) { if (!sessionFailure(e)) orderMessage('这次下单结果尚未确认。点击“重试确认这次下单”，系统会使用原请求继续核对。'); }
    finally { busy = false; updateForm(); if (view === 'payment-view') drawPayment(); }
  }
  function showForm() {
    if (busy) return;
    if (creation) { drawPayment(); orderMessage('请先确认这次下单结果，再更换充值金额或方式。'); return; }
    clearInterval(countdownTimer);
    if (order?.state === 'credited' || rejected(order)) { order = null; creation = null; save(null); }
    notice(''); orderMessage(''); updateForm(); show('choose-view', true);
  }
  $('topup-form').addEventListener('submit', e => { e.preventDefault(); createOrder(); });
  $('amount').addEventListener('input', updateForm);
  document.querySelectorAll('[data-amount]').forEach(b => b.addEventListener('click', () => { $('amount').value = b.dataset.amount; updateForm(); }));
  document.querySelectorAll('input[name="method"]').forEach(b => b.addEventListener('change', () => { method = b.value; updateForm(); }));
  $('manual-link').addEventListener('click', e => { e.preventDefault(); goManual(); });
  document.querySelector('.other-methods a:last-child').addEventListener('click', e => { if (uncertain()) { e.preventDefault(); goManual(e.currentTarget.href); } });
  $('check-order').addEventListener('click', () => refresh(true)); $('retry-create').addEventListener('click', () => createOrder(true));
  $('reload-qr').addEventListener('click', () => { qrBroken = false; $('payment-qr').removeAttribute('src'); refresh(); });
  $('payment-qr').addEventListener('error', () => { if (view === 'payment-view' && phase === 'paying') { qrBroken = true; drawPayment(); } });
  $('back-to-form').addEventListener('click', showForm); $('resume-order').addEventListener('click', () => drawPayment(true));
  $('confirm-check').addEventListener('click', () => { drawPayment(true); if (order) refresh(true); });
  $('confirm-cancel').addEventListener('click', () => drawPayment(true)); $('confirm-continue').addEventListener('click', () => { const next = intent; intent = null; if (next) next(); });
  $('copy-order').addEventListener('click', async () => { try { await navigator.clipboard.writeText(order.id); $('copy-order').textContent = '已复制'; } catch (_) { orderMessage('可在订单详情中选中订单号复制。'); } });
  $('reload-page').addEventListener('click', () => location.reload());
  document.addEventListener('visibilitychange', () => { if (!document.hidden && order) refresh(true); });
  async function boot() {
    try {
      await identity();
      const results = await Promise.allSettled([api('/api/omni/topups/settings'), api('/api/omni/topups'), readJSON('/brand/topup-channel-status.json'), readJSON('/brand/api/subscription/fx')]);
      if (results[0].status === 'rejected' && sessionFailure(results[0].reason)) return;
      enabled = results[0].status === 'fulfilled' && results[0].value.enabled === true;
      listReady = results[1].status === 'fulfilled';
      const rows = listReady ? results[1].value || [] : [];
      policy = results[2].status === 'fulfilled' ? results[2].value : { channels: {} };
      fx = results[3].status === 'fulfilled' ? results[3].value : null;
      const prior = saved(), pending = rows.find(resumable);
      if (prior?.request_id && /^(alipay|wechat)$/.test(prior.method) && cents(String(prior.amount)) >= 100) {
        creation = prior; method = prior.method; $('amount').value = prior.amount;
        updateForm(); await createOrder(true); return;
      }
      if (prior?.id) {
        try {
          let restored = await api('/api/omni/topups/' + encodeURIComponent(prior.id));
          if ((rejected(restored) || expired(restored)) && restored.state !== 'review' && !['unknown', 'creating'].includes(restored.state) && pending && pending.id !== restored.id) restored = await api('/api/omni/topups/' + encodeURIComponent(pending.id));
          if (resumable(restored)) { applyOrder(restored); drawPayment(); startPoll(); }
          else { save(null); if (restored.state === 'credited') notice('上一笔充值已到账，可在充值记录中查看。'); else if (expired(restored)) notice('上一笔付款码已过期。如你已付款，请在充值记录中查询原订单。'); }
        } catch (e) { if (e.status === 404) save(null); else throw e; }
      }
      if (!order && pending) { applyOrder(await api('/api/omni/topups/' + encodeURIComponent(pending.id))); drawPayment(); startPoll(); }
      updateForm();
      if (!order) { show('choose-view'); if (!listReady) { notice('充值记录暂时无法加载。请重新加载页面，以免重复下单。'); $('pay-button').disabled = true; } }
    } catch (e) {
      if (sessionFailure(e)) return;
      $('load-error-text').textContent = '充值信息暂时无法加载，请重新加载后继续。'; show('load-error');
    }
  }
  boot();
})();
