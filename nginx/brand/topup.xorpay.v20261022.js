(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  let account, token = '', method = 'alipay', amount = '10', order = null, busy = false, unresolved = false;
  let requestID = '', poll, tick, newOrdersEnabled = false;
  const params = new URLSearchParams(location.search);
  const message = text => { $('verification-message').hidden = !text; $('verification-message').textContent = text || ''; };
  const yuan = cents => '¥' + (cents / 100).toFixed(2);
  async function identity() {
    const response = await fetch('/api/user/auth/refresh', { method: 'POST', credentials: 'same-origin', cache: 'no-store' });
    const p = await response.json(); account = p.data?.user; token = p.data?.access_token || '';
    if (!response.ok || !account?.id || !token) throw Error('请先登录后充值。');
    $('account-name').textContent = account.display_name || account.username;
    $('account-public-id').textContent = account.public_id || '额度计入当前登录账号';
  }
  async function api(path, body, retry = true) {
    const r = await fetch(path, { method: body === undefined ? 'GET' : 'POST', credentials: 'same-origin', cache: 'no-store', headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    if (r.status === 401 && retry) { await identity(); return api(path, body, false); }
    const p = await r.json(); if (!r.ok || p.success === false) { const e = Error(p.message || '请求失败，请检查原订单，勿重复付款。'); e.status = r.status; throw e; } return p.data;
  }
  function manual() {
    document.querySelector('.subtitle').textContent = '人工应急收款：付款后提交申请，由经营者核验到账。';
    const script = document.createElement('script'); script.src = '/brand/topup-manual.v20261022.js'; document.body.append(script);
  }
  function key() { return 'omni.xorpay.' + account.id; }
  function store(value) { try { if (value) localStorage.setItem(key(), JSON.stringify(value)); else localStorage.removeItem(key()); } catch (_) {} }
  function saved() { try { return JSON.parse(localStorage.getItem(key()) || 'null'); } catch (_) { return null; } }
  function rejectedWithoutCode(row) {
    return !row.qr && row.error_code === 'provider_rejected' && ['fee_error', 'not_exist', 'sign_error', 'no_contract', 'no_alipay_contract', 'app_off', 'pay_type_error', 'alipay_api_error', 'wechat_api_error'].includes(row.create_status || row.provider_status);
  }
  function inactive(row) {
    return rejectedWithoutCode(row) || (row.qr && row.expires_at <= Date.now() / 1000 && row.state !== 'review');
  }
  function valid() { return /^\d{1,5}(\.\d{1,2})?$/.test(amount) && Number(amount) >= 1 && Number(amount) <= 10000; }
  function update() {
    $('pay-button').disabled = busy || !account || !valid() || (!newOrdersEnabled && !order && !unresolved);
    $('pay-button').textContent = order && order.state !== 'credited' ? '查看当前订单 →' : unresolved ? '检查原下单结果 →' : '获取付款二维码 →';
    $('summary-amount').textContent = valid() ? yuan(Math.round(Number(amount) * 100)) : '—';
    $('amount-error').hidden = valid(); $('amount-error').textContent = '请输入 ¥1–¥10000，最多两位小数。';
    document.querySelectorAll('.method,.amount').forEach(b => { b.disabled = busy || !!order || unresolved; });
    $('custom-amount').disabled = busy || !!order || unresolved;
  }
  function render(next) {
    order = next; unresolved = false; store({ id: order.id });
    method = order.method; amount = (order.cents / 100).toFixed(2);
    $('summary-method').textContent = method === 'wechat' ? '微信支付' : '支付宝';
    document.querySelectorAll('.method').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.method === method)));
    const paid = order.state === 'credited';
    const creationStatus = order.create_status || order.provider_status;
    const safeToRetry = rejectedWithoutCode(order);
    let rejectionTitle = '暂无法发起支付，请稍后重试', rejectionHelp = '通道未生成付款码，本次未付款。请稍后重试或使用人工应急入口。';
    if (order.create_error_code === 'alipay_contract_required') {
      rejectionTitle = '支付宝通道尚未完成签约';
      rejectionHelp = '经营者需查看支付宝发来的签约邮件并完成签约，完成后重新下单。本次未生成付款码，未付款。';
    } else if (order.create_error_code === 'alipay_auth_invalid') {
      rejectionTitle = '支付宝商户授权异常';
      rejectionHelp = '经营者需联系 XorPay 客服修复支付宝授权，完成后重新下单。本次未生成付款码，未付款。';
    } else if (['no_contract', 'no_alipay_contract'].includes(creationStatus)) {
      rejectionTitle = '支付通道尚未完成签约'; rejectionHelp = '经营者需在商户后台完成对应通道签约。本次未生成付款码，未付款。';
    } else if (creationStatus === 'fee_error') {
      rejectionTitle = '支付通道手续费余额不足'; rejectionHelp = '经营者需在 XorPay 商户后台补足手续费余额，完成后重新下单。本次未生成付款码，未付款。';
    } else if (['alipay_api_error', 'wechat_api_error'].includes(creationStatus)) {
      rejectionTitle = (order.method === 'alipay' ? '支付宝' : '微信') + '通道暂不可用'; rejectionHelp = '支付平台拒绝了下单，经营者需核对商户授权及风控提示。本次未生成付款码，未付款。';
    }
    const labels = { creating: '正在创建支付单', pending: '等待付款', unknown: '支付结果待确认，请勿重复付款', expired: '付款码已过期', review: '付款异常，待经营者核对', credited: '充值已到账' };
    const state = safeToRetry ? rejectionTitle : order.state === 'pending' && order.expires_at <= Date.now() / 1000 ? labels.expired : labels[order.state] || '支付结果待确认';
    $('summary-status').textContent = state; $('dialog-kicker').textContent = '自动充值 · ' + (order.method === 'wechat' ? '微信' : '支付宝');
    $('dialog-title').textContent = state; $('dialog-description').textContent = '订单 ' + order.id + ' · ' + yuan(order.cents) + (paid ? '，已自动计入当前账户。' : safeToRetry ? '，' + rejectionHelp : '，付款后自动到账，无需提交人工核验。');
    $('step-two').classList.add('active'); $('step-three').classList.toggle('active', paid);
    const usable = !paid && order.qr_image && order.expires_at > Date.now() / 1000;
    $('qr-payment').hidden = !usable; $('payment-dialog').classList.toggle('has-qr', !!usable);
    if (usable) {
      $('collection-qr').src = order.qr_image; $('collection-qr').alt = '本订单专属付款二维码';
      $('qr-amount').textContent = yuan(order.cents); $('save-qr').href = order.qr_image; $('save-qr').download = order.id + '.png';
      $('qr-instruction').textContent = order.method === 'wechat' ? '请使用另一部手机的微信扫一扫；微信通道不保证支持相册或长按识别。' : '请使用支付宝扫一扫，也可在手机上点击下方按钮打开支付宝。';
    }
    $('live-payment-link').hidden = !(usable && order.method === 'alipay');
    if (usable && order.method === 'alipay') {
      $('live-payment-link').href = 'alipays://platformapi/startapp?appId=20000067&url=' + encodeURIComponent(order.qr);
      $('live-payment-link').textContent = '打开支付宝付款 ↗';
    }
    $('dialog-submit-verification').hidden = true; $('submit-verification').hidden = true;
    $('live-reference').hidden = true; document.querySelector('label[for="live-reference"]').hidden = true;
    $('payment-domain').hidden = true; $('simulate-return').hidden = true;
    $('verification-hint').textContent = paid ? '充值已到账，可返回钱包查看。' : state;
    $('live-quote-source').textContent = '汇率来源：' + order.fx_source + ' · ' + order.fx_as_of + (order.fx_note ? ' · ' + order.fx_note : '');
    clearInterval(tick);
    const countdown = () => { const s = Math.max(0, Math.ceil(order.expires_at - Date.now() / 1000)); $('live-quote-meta').textContent = `本单汇率 ${order.fx_rate} CNY / USD · 基础额度 US$${order.usd} · ` + (paid ? '已到账' : s ? `付款剩余 ${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}` : '付款码已过期；如已付款，请检查原订单。'); if (!s) $('qr-payment').hidden = true; };
    countdown(); if (!paid) tick = setInterval(countdown, 1000);
    $('check-order').hidden = paid; $('new-order').hidden = !newOrdersEnabled || (!safeToRetry && !paid && order.expires_at > Date.now() / 1000);
    $('new-order').textContent = paid ? '再充值一笔' : safeToRetry ? '重新尝试充值' : '重新下单（请先确认原单未付款）';
    if (paid) { clearInterval(poll); window.dispatchEvent(new Event('omni:wallet-refresh')); }
    update();
  }
  async function refresh(check = false) {
    if (!order || busy || document.hidden) return;
    try { const row = await api('/api/omni/topups/' + encodeURIComponent(order.id) + (check ? '/check' : ''), check ? {} : undefined); render(row); if (check) message(row.state === 'credited' ? '充值已到账。' : '已检查原订单，' + $('summary-status').textContent + '。'); }
    catch (e) { message(e.message); }
  }
  function startPoll() { clearInterval(poll); poll = setInterval(() => refresh(), 3000); }
  function chooseAmount(value, custom = false) {
    amount = value; requestID = ''; document.querySelectorAll('.amount').forEach(b => b.setAttribute('aria-pressed', String(custom ? b.dataset.custom === 'true' : b.dataset.value === value)));
    $('custom-field').hidden = !custom; update();
  }
  function chooseMethod(value) {
    if (value === 'international') { location.href = '/brand/topup.html?manual=1&method=international'; return; }
    method = value; requestID = ''; document.querySelectorAll('.method').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.method === value)));
    $('summary-method').textContent = value === 'wechat' ? '微信支付' : '支付宝'; update();
  }
  async function boot() {
    if (params.get('manual') === '1') { manual(); return; }
    $('pay-button').disabled = true;
    try {
      await identity(); const settings = await api('/api/omni/topups/settings'); newOrdersEnabled = settings.enabled;
      const initialRows = (await api('/api/omni/topups')) || [];
      const pending = initialRows.find(r => r.state !== 'credited' && r.created_at > Date.now() / 1000 - 86400 && !inactive(r));
      if (!newOrdersEnabled && !saved()?.id && !saved()?.request_id && !pending) { manual(); return; }
      document.querySelector('.subtitle').textContent = '选择支付方式与金额，付款后自动充值到钱包。';
      $('step-three').lastChild.textContent = '自动到账'; $('temporary-note').textContent = '订单专属收款码，请核对金额；到账后自动发放额度。';
      $('amount-note').textContent = '最低 ¥1，最高 ¥10000；按实付全额折算，手续费由平台承担。';
      $('cta-hint').textContent = '付款后自动到账，无需提交核验。';
      document.querySelectorAll('strong').forEach(info => {
        if (info.textContent === '付款后提交核验') { info.textContent = '付款后自动到账'; info.parentElement.querySelectorAll('p').forEach(p => { p.textContent = '系统确认付款后计入钱包，请勿重复付款。'; }); }
      });
      document.querySelectorAll('.method span').forEach(n => { if (!n.children.length && n.textContent === '临时收款') n.textContent = '自动到账'; });
      const basis = document.querySelector('.total p'); if (basis) basis.textContent = '按本单锁定汇率和实付全额折算。';
      $('return-panel').querySelector('strong').textContent = '付款后自动到账';
      $('return-panel').querySelector('p').textContent = '如到账延迟，请检查原订单，勿重复付款。';
      $('custom-amount').max = '10000'; $('custom-amount').step = '0.01';
      $('custom-amount').min = '1'; $('custom-currency').textContent = 'CNY';
      $('currency').textContent = $('summary-currency').textContent = 'CNY / 人民币';
      $('provider-note').hidden = true; $('submit-verification').hidden = true;
      const amounts = $('amounts'); amounts.replaceChildren();
      for (const value of ['10', '20', '50', '100', '200', '500', 'custom']) {
        const b = document.createElement('button'); b.type = 'button'; b.className = 'amount';
        if (value === 'custom') { b.dataset.custom = 'true'; b.textContent = '自定义金额'; }
        else { b.dataset.value = value; b.textContent = '¥' + value; }
        amounts.append(b);
      }
      document.querySelectorAll('.qr-reminder,.qr-real-note').forEach(n => { n.textContent = '一单一码，请勿重复付款；到账状态以系统确认为准。'; });
      document.querySelector('.qr-real-note').hidden = true;
      const recipient = document.querySelector('.qr-details p'); if (recipient) recipient.textContent = '请核对支付应用显示的商户及金额。';
      $('qr-payment').nextElementSibling.textContent = '付款成功后系统自动入账，可返回钱包查看。';
      document.querySelector('.footer-bottom span:last-child').textContent = '付款确认后自动计入钱包。';
      const fallback = document.createElement('a'); fallback.href = '/brand/topup.html?manual=1'; fallback.textContent = '人工应急收款（需人工核验）'; fallback.className = 'source-link'; $('cta-hint').append(document.createElement('br'), fallback);
      const check = document.createElement('button'); check.id = 'check-order'; check.type = 'button'; check.className = 'primary'; check.textContent = '检查付款结果'; check.hidden = true; check.addEventListener('click', () => refresh(true));
      const anew = document.createElement('button'); anew.id = 'new-order'; anew.type = 'button'; anew.className = 'close'; anew.hidden = true;
      anew.addEventListener('click', () => { clearInterval(poll); clearInterval(tick); order = null; requestID = ''; store(null); $('payment-dialog').close(); $('summary-status').textContent = '待付款'; update(); });
      $('close-dialog').before(check, anew);
      document.querySelectorAll('.method').forEach(b => b.addEventListener('click', () => chooseMethod(b.dataset.method)));
      document.querySelectorAll('.amount').forEach(b => b.addEventListener('click', () => { chooseAmount(b.dataset.custom ? '' : b.dataset.value, !!b.dataset.custom); if (b.dataset.custom) $('custom-amount').focus(); }));
      $('custom-amount').addEventListener('input', () => chooseAmount($('custom-amount').value, true));
      $('pay-button').addEventListener('click', async () => {
        if (busy || !valid()) return;
        if (order) { $('payment-dialog').showModal(); return; }
        busy = true; unresolved = true; update();
        if (!requestID) requestID = crypto.randomUUID();
        store({ request_id: requestID, method, amount });
        try { render(await api('/api/omni/topups', { method, amount, request_id: requestID })); message(''); $('payment-dialog').showModal(); startPoll(); }
        catch (e) { message(e.message + ' 请检查原订单，勿重复付款。'); }
        finally { busy = false; update(); }
      });
      $('close-dialog').addEventListener('click', () => $('payment-dialog').close());
      document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(true); });
      const prior = saved(); chooseMethod('alipay'); chooseAmount('10');
      if (prior?.id) {
        try {
          let restored = await api('/api/omni/topups/' + encodeURIComponent(prior.id));
          // A failed/expired local selection must not mask a newer payable order.
          // Unknown creation results and review orders keep their original ID.
          if (inactive(restored) && pending && pending.id !== restored.id) restored = await api('/api/omni/topups/' + encodeURIComponent(pending.id));
          render(restored); startPoll();
        } catch (e) { if (e.status !== 404) throw e; store(null); }
      }
      else if (prior?.request_id) { requestID = prior.request_id; amount = prior.amount; method = prior.method; unresolved = true; update(); render(await api('/api/omni/topups', { method, amount, request_id: requestID })); startPoll(); }
      else if (pending) { render(await api('/api/omni/topups/' + pending.id)); startPoll(); }
      update();
    } catch (e) { message(e.message); if (account) update(); }
  }
  boot();
})();
