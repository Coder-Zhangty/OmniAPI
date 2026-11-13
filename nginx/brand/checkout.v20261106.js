(() => {
  'use strict';
  const $ = id => document.getElementById(id), params = new URLSearchParams(location.search);
  const selecting = document.body.dataset.page === 'select';
  const wechat = /MicroMessenger/i.test(navigator.userAgent); // Payment container, not touch detection.
  const endpoint = '/api/omni/payments/xorpay/checkout/';
  const rejectedCodes = new Set(['fee_error', 'not_exist', 'sign_error', 'no_contract', 'no_alipay_contract', 'app_off', 'pay_type_error', 'alipay_api_error', 'wechat_api_error']);
  const rejected = o => o?.error_code === 'provider_rejected' && !o.qr && !o.qr_image && rejectedCodes.has(o.create_status || o.provider_status);
  let account, token = '', order = null, grant = '', busy = false, refreshing = false, offset = 0;
  let customMode = false,promotion=null;
  const activePromotion=()=>promotion?.enabled&&now()>=promotion.starts_at&&now()<promotion.ends_at;
  let method = 'wechat', available = new Set(), fx = null, intent = null, previous = null, deferred = null;
  let poll, timer, successAt = 0, redirecting = false, sessionExpired = false, qrBroken = false, handoffBlocked = false;
  let cashierForm = null, cashierURL = '', cashierQR = '';
  const names = { wechat: '微信', alipay: '支付宝' };
  const canUse = m => available.has(m);
  const money = (c, usd = false) => (usd ? '$' : '¥') + (Number(c) / 100).toFixed(2);
  const now = () => Date.now() / 1000 + offset;
  function quota(q) { try { const n = BigInt(String(q || 0)); return 'US$' + (n / 500000n) + '.' + ((n % 500000n) * 2n).toString().padStart(6, '0'); } catch (_) { return '—'; } }
  function cents(s) { if (!/^\d{1,6}(?:\.\d{1,2})?$/.test(s)) return 0; const [a, b = ''] = s.split('.'); return Number(a) * 100 + Number((b + '00').slice(0, 2)); }
  const navigate = url => location.assign(url);
  function note(message) { $('notice').textContent = message || ''; $('notice').hidden = !message; }
  async function json(url, options = {}) {
    const response = await fetch(url, { credentials: 'same-origin', cache: 'no-store', ...options });
    const date = response.headers?.get('date'); if (date && Number.isFinite(Date.parse(date))) offset = Date.parse(date) / 1000 - Date.now() / 1000;
    const p = await response.json().catch(() => ({}));
    if (!response.ok || p.success === false) { const e = Error(response.status >= 500 ? '服务暂时无法响应，请稍后检查原订单。' : p.message || '请求暂未完成，请重试原操作。'); e.status = response.status; e.data = p.data; throw e; }
    return Object.hasOwn(p, 'data') ? p.data : p;
  }
  async function identity() {
    const p = await json('/api/user/auth/refresh', { method: 'POST' });
    if (!p?.user?.id || !p.access_token) { const e = Error('请先登录。'); e.status = 401; throw e; }
    if (account && account.id !== p.user.id) { const e = Error('账号已切换，请刷新后继续。'); e.status = 401; throw e; }
    account = p.user; token = p.access_token; sessionExpired = false;
    $('account-name').textContent = account.display_name || account.username || '当前账户';
    if ($('checkout-account')) $('checkout-account').value = account.display_name || account.username || '当前账户';
  }
  async function api(path, body, retry = true) {
    try { return await json(path, { method: body === undefined ? 'GET' : 'POST', headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) }); }
    catch (e) { if (e.status === 401 && retry) { await identity(); return api(path, body, false); } throw e; }
  }
  const granted = action => json(endpoint + action, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ grant }) });
  function sessionError(e) {
    if (e.status !== 401 && e.status !== 403) return false;
    sessionExpired = true; clearInterval(poll); clearInterval(timer); note(e.status === 401 ? '请先登录后充值。' : '当前账户暂无付款权限，请重新登录后查看。');
    $('login-link').hidden = false; $('login-link').href = '/login?redirect=' + encodeURIComponent(location.pathname + location.search);
    if (!selecting) { $('open-alipay').hidden = true; $('open-alipay').removeAttribute('href'); $('open-upstream').hidden = true; $('open-upstream').removeAttribute('href'); $('launch-pay').hidden = true; $('retry-load').hidden = false; } else { $('pay-button').disabled = true; $('pay-button').textContent = '登录后充值'; $('express-wechat').disabled = true; if (!account) { $('account-name').textContent = '请先登录'; $('checkout-account').value = '登录后确认充值账户'; } }
    return true;
  }
  function readSaved() { try { return JSON.parse(localStorage.getItem('omni.checkout.' + account.id) || 'null'); } catch (_) { return null; } }
  function save(value) { try { value ? localStorage.setItem('omni.checkout.' + account.id, JSON.stringify(value)) : localStorage.removeItem('omni.checkout.' + account.id); } catch (_) {} }
  function manualURL(m = method) { return '/brand/topup-manual.html?method=' + encodeURIComponent(m) + '&amount=' + encodeURIComponent($('amount').value.trim()); }
  function valid() { const c = cents($('amount').value.trim()); return c >= 100 && c <= 1000000; }
  function promotionPreview(principal) {
    let box=$('promotion-breakdown');
    if(!box){box=document.createElement('dl');box.id='promotion-breakdown';$('summary-foot').before(box);}
    box.replaceChildren();box.hidden=!activePromotion();if(box.hidden||!valid())return;
    const gift=principal*BigInt(promotion.bonus_bps)/10000n;
    for(const [label,value] of [['预计本金',principal],['预计加送15%',gift],['预计总到账',principal+gift]]){
      const row=document.createElement('div');row.className='receipt-row';const dt=document.createElement('dt'),dd=document.createElement('dd');dt.textContent=label;dd.textContent=quota(value);row.append(dt,dd);box.append(row);
    }
  }
  function updateSelect() {
    const c = cents($('amount').value.trim()), uncertain = intent && !intent.id;
    $('summary-amount').textContent = money(c); $('summary-total').textContent = money(c);
    $('input-symbol').textContent = '¥'; $('presets').hidden = false;
    $('summary-fee').textContent = '¥0.00';
    if ($('receipt-amount')) $('receipt-amount').textContent = valid() ? money(c) : '—';
    $('summary-amount').textContent = valid() ? money(c) : '—';
    $('summary-total').textContent = valid() ? money(c) : '—';
    $('summary-foot').textContent = activePromotion()?'双十一充值统一加送15%，不限金额、上不封顶。以实际付款成功时间为准，11月12日24:00结束。':'下单后汇率锁定15分钟。充值本金按订单汇率到账。';
    $('amount-hint').textContent = '¥1 起充，可自定义';
    $('amount-error').textContent = '请输入 ¥1–¥10000 的金额，最多两位小数。';
    if(!activePromotion()&&$('promotion-breakdown'))$('promotion-breakdown').hidden=true;
    $('amount-error').hidden = valid(); $('amount').setAttribute('aria-invalid', String(!valid()));
    $('summary-quota').textContent = '付款前确认';
    if (valid() && fx && /^\d{1,2}(?:\.\d{1,8})?$/.test(fx.rate)) {
      const [a, b = ''] = fx.rate.split('.'), rate = BigInt(a + b);
      if (rate > 0n) {const principal=BigInt(c) * (10n ** BigInt(b.length)) * 10000n / rate / 2n;$('summary-quota').textContent=quota(principal);promotionPreview(principal);}
    }
    $('mobile-compact').textContent = $('account-name').textContent + ' · 预计 ' + $('summary-quota').textContent;
    $('amount').disabled = busy || !!uncertain;
    document.querySelectorAll('[data-amount]').forEach(b => { b.disabled = busy || !!uncertain; b.setAttribute('aria-pressed', String(!customMode && cents(b.dataset.amount) === c)); });
    if ($('custom-amount')) { $('custom-amount').disabled = busy || !!uncertain; $('custom-amount').setAttribute('aria-pressed', String(customMode || (valid() && ![...document.querySelectorAll('[data-amount]')].some(b => cents(b.dataset.amount) === c)))); }
    document.querySelectorAll('[name="method"]').forEach(r => { r.checked = r.value === method; r.disabled = busy || !!uncertain || !canUse(r.value); });
    $('pay-button').disabled = !account || sessionExpired || !valid() || busy || (!canUse(method) && !uncertain);
    if ($('express-wechat')) $('express-wechat').disabled = !account || sessionExpired || busy || !!uncertain || !canUse('wechat') || !valid();
    $('pay-button').textContent = busy ? '正在创建原订单…' : uncertain ? '继续原充值请求' : '支付 ' + money(c);
    $('action-note').textContent = canUse(method) ? '支付确认后自动到账，汇率锁定 15 分钟。' : '该充值通道暂不可用，请选择其他付款方式。';
    $('change-amount').hidden = !uncertain;
  }
  setInterval(()=>{if(selecting&&!document.hidden)updateSelect();},10000);
  function showResume(o) {
    previous = o; $('resume').hidden = !o;
    if (o) { $('resume-text').textContent = money(o.cents) + ' 的充值订单尚未确认到账。'; $('resume-link').href = '/brand/checkout.html?order=' + encodeURIComponent(o.id); }
  }
  function requireUnpaid(action) {
    deferred = action; $('switch-warning').hidden = false; $('topup-form').hidden = true;
    $('switch-yes').focus();
  }
  async function submitSelection() {
    if (!valid() || busy || !account) return;
    if (!Object.hasOwn(names, method)) { note('该付款方式已下架，请选择可用方式。'); return; }
    if (!canUse(method) && !intent) { note('该自动充值通道暂不可用，请稍后重试。'); return; }
    busy = true; note('');
    if (!intent) intent = { amount: $('amount').value.trim(), method, request_id: 'checkout_' + Array.from(crypto.getRandomValues(new Uint8Array(16)), b => b.toString(16).padStart(2, '0')).join('') };
    save(intent); updateSelect();
    try {
      const o = await api('/api/omni/topups', { amount: intent.amount, method: intent.method, request_id: intent.request_id });
      if (!o?.id) throw Error('订单结果待确认，请继续原充值请求。');
      intent.id = o.id; save(intent); navigate('/brand/checkout.html?order=' + encodeURIComponent(o.id));
    } catch (e) {
      if (e.data?.id && /^xp_[A-Za-z0-9_-]+$/.test(e.data.id)) { intent.id = e.data.id; save(intent); navigate('/brand/checkout.html?order=' + encodeURIComponent(intent.id)); }
      else if (!sessionError(e)) note(e.message + ' 请保留原请求，不要重复付款。');
    } finally { busy = false; updateSelect(); }
  }
  async function bootSelect() {
    if (params.get('manual') === '1') { navigate('/brand/topup-manual.html' + location.search); return; }
    document.querySelectorAll('[data-amount]').forEach(b => b.addEventListener('click', () => { customMode = false; $('amount').value = Number(b.dataset.amount).toFixed(2); updateSelect(); }));
    $('amount').addEventListener('input', () => { customMode = true; updateSelect(); });
    $('amount').addEventListener('blur', () => { if (valid()) $('amount').value = (cents($('amount').value.trim()) / 100).toFixed(2); });
    $('custom-amount')?.addEventListener('click', () => { if (busy || (intent && !intent.id)) return; customMode = true; updateSelect(); $('amount').focus(); $('amount').select(); });
    document.querySelectorAll('[name="method"]').forEach(r => r.addEventListener('change', () => { method = r.value; updateSelect(); }));
    $('topup-form').addEventListener('submit', e => { e.preventDefault(); if (previous && !intent) requireUnpaid(submitSelection); else submitSelection(); });
    $('express-wechat')?.addEventListener('click', () => {
      if (!canUse('wechat') || busy || intent || !valid()) return;
      method = 'wechat'; updateSelect(); $('topup-form').requestSubmit();
    });
    $('change-amount').addEventListener('click', () => requireUnpaid(() => { intent = null; save(null); updateSelect(); }));
    $('switch-yes').addEventListener('click', () => { $('switch-warning').hidden = true; $('topup-form').hidden = false; previous = null; const action = deferred; deferred = null; action?.(); });
    $('switch-no').addEventListener('click', () => { if (previous) navigate('/brand/checkout.html?order=' + encodeURIComponent(previous.id)); else { $('switch-warning').hidden = true; $('topup-form').hidden = false; } });
    try {
      await identity();
      const result = await Promise.allSettled([api('/api/omni/topups/settings'), json('/brand/api/subscription/fx'), api('/api/omni/topups')]);
      if (result[0].status === 'fulfilled' && result[0].value.enabled === true) {
        const settings = result[0].value;promotion=settings.promotion;
        available = new Set((settings.methods || []).filter(m => ['wechat', 'alipay'].includes(m) && (m !== 'wechat' || settings.checkout_flow === 'xorpay_cashier')));
      }
      if (result[1].status === 'fulfilled') fx = result[1].value;
      $('wechat-status').textContent = canUse('wechat') ? '使用微信扫码付款' : '自动充值暂未开放';
      $('manual-wechat').hidden = canUse('wechat');
      $('alipay-status').textContent = canUse('alipay') ? '使用支付宝扫码或打开支付宝付款' : '自动充值暂未开放';
      method = Object.hasOwn(names, params.get('method')) ? params.get('method') : ['wechat', 'alipay'].find(canUse) || 'wechat';
      const removedMethod = params.get('method') === 'international';
      if (removedMethod) note('国际信用卡支付入口已移除，请选择微信或支付宝。');
      if (!removedMethod && params.has('amount')) { $('amount').value = params.get('amount'); if (!valid()) $('amount').value = '10.00'; }
      const cached = readSaved();
      if (cached?.request_id && ['wechat', 'alipay'].includes(cached.method) && typeof cached.amount === 'string') {
        if (!cached.id) { intent = cached; method = cached.method; $('amount').value = cached.amount; note('上一次请求结果尚未确认，请继续同一笔请求。'); }
      }
      if (result[2].status === 'fulfilled' && Array.isArray(result[2].value)) {
        const rows = result[2].value.filter(o => o.state !== 'credited' && !(o.error_code === 'provider_rejected' && !o.qr) && (o.expires_at > now() || ['unknown', 'review', 'creating'].includes(o.state)));
        if (rows.length) showResume(rows[0]);
        else if (cached?.id) save(null);
      }
      updateSelect();
    } catch (e) { if (!sessionError(e)) note(e.message); }
  }
  function checkoutLink() { return grant ? 'https://omniapi.biz/brand/checkout.html#grant=' + grant : order?.payment_url || ''; }
  function currentLink() { return order?.payment_flow === 'xorpay_cashier' ? cashierURL : checkoutLink(); }
  function directQR(value) {
    if (typeof globalThis.qrcode !== 'function') throw Error('付款码暂时无法生成，请重新加载原订单。');
    const code = globalThis.qrcode(0, 'M'); code.addData(value, 'Byte'); code.make();
    const modules = code.getModuleCount(), scale = 5, quiet = 4;
    const canvas = document.createElement('canvas'); canvas.width = canvas.height = (modules + quiet * 2) * scale;
    const context = canvas.getContext('2d');
    if (!context) throw Error('付款码暂时无法生成，请重新加载原订单。');
    context.fillStyle = '#fff'; context.fillRect(0, 0, canvas.width, canvas.height); context.fillStyle = '#000';
    for (let row = 0; row < modules; row++) for (let col = 0; col < modules; col++) if (code.isDark(row, col)) context.fillRect((col + quiet) * scale, (row + quiet) * scale, scale, scale);
    return canvas.toDataURL('image/png');
  }
  async function prepareCashier() {
    if (cashierForm) return cashierForm;
    const payload = await granted('launch');
    if (!/^https:\/\/xorpay\.com\/api\/cashier\/\d+$/.test(payload.action) || payload.fields?.order_id !== order.id || payload.fields?.pay_type !== 'jsapi' || payload.fields?.price !== (Number(order.cents) / 100).toFixed(2)) throw Error('付款参数未通过检查，请保留原订单。');
    const expected = checkoutLink();
    if (payload.fields.return_url !== expected || payload.fields.cancel_url !== expected) throw Error('付款返回地址异常，请联系客服。');
    // XorPay supports GET and POST with identical signed values. The browser
    // prepares the official URL locally; no signature enters our access logs.
    const target = new URL(payload.action);
    for (const [name, value] of Object.entries(payload.fields)) target.searchParams.append(name, String(value));
    const image = !wechat ? directQR(target.href) : '';
    cashierURL = target.href; cashierQR = image; cashierForm = payload;
    return payload;
  }
  async function copyLink() {
    const link = currentLink(); if (!link) return;
    try { await navigator.clipboard.writeText(link); note('已复制。在微信聊天中粘贴链接并打开即可付款。'); }
    catch (_) { let input = $('copy-fallback'); if (!input) { input = document.createElement('textarea'); input.id = 'copy-fallback'; input.readOnly = true; input.className = 'copy-fallback'; $('pay-links').after(input); } input.value = link; input.focus(); input.select(); note('请复制下方订单链接，在微信中打开。'); }
  }
  function alipayURL() {
    if (order?.method !== 'alipay' || typeof order.qr !== 'string') return '';
    try {
      const url = new URL(order.qr);
      if (url.protocol !== 'https:' || url.port || url.username || url.password || !(url.hostname === 'xorpay.com' || url.hostname === 'alipay.com' || url.hostname.endsWith('.alipay.com'))) return '';
      return url.href;
    } catch (_) { return ''; }
  }
  function alipayLink() {
    const url = alipayURL();
    return url ? 'alipays://platformapi/startapp?appId=20000067&url=' + encodeURIComponent(url) : '';
  }
  function renderOrder() {
    if (!order) return;
    const credited = order.state === 'credited', expired = order.state === 'expired' || order.expires_at <= now(), review = order.state === 'review';
    const cashier = order.payment_flow === 'xorpay_cashier';
    $('summary-amount').textContent = money(order.cents); $('summary-total').textContent = money(order.cents); $('summary-quota').textContent = quota(order.quota);
    if($('summary-foot'))$('summary-foot').textContent = credited ? '实付 '+money(order.cents)+' · 本金 '+quota(order.quota)+' · 赠额 '+quota(order.bonus)+' · 总到账 '+quota(BigInt(String(order.quota||0))+BigInt(String(order.bonus||0)))+(order.promotion_state==='pending_payment_time'?' · 活动赠额待付款时间核验':'') : '以实际付款时间判断加送资格：11月11日12:00至11月12日24:00统一加送15%。';
    $('mobile-compact').textContent = $('account-name').textContent + ' · ' + (credited ? '已到账 ' : '基础额度 ') + quota(credited ? BigInt(String(order.quota || 0)) + BigInt(String(order.bonus || 0)) : order.quota);
    $('detail-id').textContent = order.id; $('detail-fx').textContent = order.fx_rate + ' CNY / USD'; $('detail-source').textContent = [order.fx_source, order.fx_as_of].filter(Boolean).join(' · ');
    $('detail-expiry').textContent = order.expires_at ? new Date(order.expires_at * 1000).toLocaleString('zh-CN') : '—';
    $('payment-title').textContent = credited ? '充值已到账' : review ? '订单需要核对' : expired ? '订单已过期' : '使用' + (order.method === 'alipay' ? '支付宝' : '微信') + '付款';
    $('payment-text').textContent = credited ? '本笔充值已计入原充值账户。' : review ? '请联系客服核对原订单，避免重复付款。' : expired ? '如已付款，仍可检查原订单的到账结果。' : cashier ? wechat ? '点击下方按钮，进入官方收银台完成付款。' : '打开微信扫一扫，支付后此页会自动更新。' : '请扫描本笔订单二维码，支付后此页会自动更新。';
    if (!credited && !cashier && !order.qr_image && !expired && !review) {
      $('payment-title').textContent = rejected(order) ? '付款未发起' : '付款结果待确认';
      $('payment-text').textContent = rejected(order) ? order.method === 'alipay' ? order.create_error_code === 'alipay_auth_invalid' ? '支付宝授权暂不可用，本次未生成付款码。请联系客服或返回充值页选择微信支付。' : '支付宝暂未生成付款码，本次未付款。请返回充值页选择微信支付。' : '通道未生成付款码，本次未付款。可返回充值页使用人工核验。' : '暂未取得付款码，请检查原订单结果，避免重复付款。';
    }
    $('status-mark').textContent = credited ? '✓' : review || expired ? '!' : order.method === 'alipay' ? '支' : '微';
    const alipay = order.method === 'alipay', link = alipayLink();
    $('payment-qr').alt = '使用' + (alipay ? '支付宝' : '微信') + '扫描此订单付款码';
    $('qr-instruction').textContent = '使用' + (alipay ? '支付宝' : '微信') + '扫一扫';
    const activeAlipay = alipay && !cashier && !credited && !review && !expired && !!link && !rejected(order);
    $('open-alipay').hidden = !activeAlipay;
    $('open-upstream').hidden = !activeAlipay;
    if (activeAlipay) $('open-upstream').href = alipayURL(); else $('open-upstream').removeAttribute('href');
    if (activeAlipay) $('open-alipay').href = link; else $('open-alipay').removeAttribute('href');
    if (alipay && !credited && !review && !expired && !rejected(order) && order.qr_image) $('payment-text').textContent = wechat ? '请使用支付宝扫一扫付款，或在系统浏览器打开本订单后点击打开支付宝。' : '请使用支付宝扫一扫，或点击下方按钮打开支付宝；付款后返回本页查看到账结果。';
    if (alipay) $('manual-emergency').hidden = true;
    const imageSource = cashier ? cashierQR : order.qr_image;
    const image = !qrBroken && !credited && !review && !expired && /^data:image\/png;base64,/.test(imageSource || '');
    $('payment-qr').parentElement.classList.toggle('direct-pay', cashier);
    $('qr-section').hidden = !image || (cashier && wechat);
    if (image) { if ($('payment-qr').getAttribute('src') !== imageSource) $('payment-qr').src = imageSource; $('save-qr').href = imageSource; }
    $('launch-pay').hidden = !cashier || !wechat || credited || expired || review;
    $('launch-pay').disabled = busy;
    $('wechat-instruction').hidden = !cashier || wechat || credited || expired || review;
    $('pay-links').hidden = !cashier || wechat || credited || expired || review || !cashierURL;
    $('save-qr').hidden = !image;
    $('check-payment').hidden = credited; $('check-payment').disabled = refreshing;
    $('credit-result').hidden = !credited; $('return-wallet').hidden = !credited; $('redirect-note').hidden = !credited;
    $('retry-load').hidden = !qrBroken && !handoffBlocked; $('login-link').hidden = true;
    if (credited) {
      $('credited-base').textContent = quota(order.quota); $('credited-bonus').textContent = quota(order.bonus); $('credited-total').textContent = quota(BigInt(String(order.quota || 0)) + BigInt(String(order.bonus || 0)));
      if (!successAt) { successAt = Date.now(); clearInterval(poll); }
      if (account) save(null);
    }
    tick();
  }
  function tick() {
    if (!order) return;
    const remaining = Math.max(0, Math.ceil(order.expires_at - now()));
    $('countdown').textContent = remaining ? Math.floor(remaining / 60).toString().padStart(2, '0') + ':' + (remaining % 60).toString().padStart(2, '0') + ' 内完成付款' : '付款入口已过期';
    if (!remaining && order.state !== 'credited') { $('launch-pay').hidden = true; $('open-alipay').hidden = true; $('open-alipay').removeAttribute('href'); $('open-upstream').hidden = true; $('open-upstream').removeAttribute('href'); $('qr-section').hidden = true; $('pay-links').hidden = true; $('wechat-instruction').hidden = true; if (order.state !== 'review') { $('payment-title').textContent = '订单已过期'; $('payment-text').textContent = '如已付款，请检查原订单，仍按原汇率入账。'; } }
    if (successAt) {
      const seconds = Math.max(0, Math.ceil((successAt + 5000 - Date.now()) / 1000));
      $('redirect-note').textContent = seconds + ' 秒后返回钱包；微信内未登录时需正常登录。';
      if (seconds === 0 && !document.hidden && !redirecting) { redirecting = true; navigate('/wallet'); }
    }
  }
  async function refresh(check = false, announce = false) {
    if (refreshing || document.hidden || !order) return;
    refreshing = true;
    try {
      const next = grant ? await granted(check ? 'check' : 'status') : await api('/api/omni/topups/' + encodeURIComponent(order.id) + (check ? '/check' : ''), check ? {} : undefined);
      if (next?.id !== order.id) throw Error('订单信息不一致，请返回原账户查看。');
      order = { ...order, ...next }; if (next.server_time) offset = next.server_time - Date.now() / 1000;
      renderOrder(); if (announce) note(order.state === 'credited' ? '' : '已检查原订单，暂未确认到账。');
    } catch (e) { if (!(grant ? false : sessionError(e))) note(e.message); }
    finally { refreshing = false; if ($('check-payment')) $('check-payment').disabled = false; }
  }
  async function launch() {
    if (busy || !grant || !wechat || !order || order.state === 'credited' || order.expires_at <= now()) return;
    busy = true; renderOrder(); note('');
    try {
      await refresh(true); if (order.state === 'credited' || order.state === 'review') return;
      const payload = await prepareCashier();
      const form = document.createElement('form'); form.method = 'POST'; form.action = payload.action; form.hidden = true;
      for (const [name, value] of Object.entries(payload.fields)) { const input = document.createElement('input'); input.type = 'hidden'; input.name = name; input.value = String(value); form.append(input); }
      document.body.append(form); form.submit();
    } catch (e) { note(e.message); }
    finally { busy = false; renderOrder(); }
  }
  async function bootCheckout() {
    clearInterval(poll); clearInterval(timer); qrBroken = false; handoffBlocked = false; cashierForm = null; cashierURL = ''; cashierQR = ''; note('');
    $('copy-link').onclick = copyLink; $('check-payment').onclick = () => refresh(true, true); $('launch-pay').onclick = launch; $('retry-load').onclick = bootCheckout;
    $('manual-emergency').onclick = e => { if (order && order.state !== 'credited' && !rejected(order)) { e.preventDefault(); $('manual-warning').hidden = false; $('payment-area').hidden = true; $('manual-yes').focus(); } };
    $('manual-no').onclick = () => { $('manual-warning').hidden = true; $('payment-area').hidden = false; };
    $('manual-yes').onclick = () => navigate('/brand/topup-manual.html?method=' + encodeURIComponent(order?.method || 'wechat') + '&amount=' + encodeURIComponent(((order?.cents || 1000) / 100).toFixed(2)));
    $('payment-qr').onerror = () => { qrBroken = true; $('qr-section').hidden = true; $('save-qr').hidden = true; note(order?.method === 'alipay' ? '二维码未能显示，可以点击打开支付宝，或重新加载原订单。' : '二维码未能显示，请复制付款链接在微信打开，或重新加载原订单。'); $('retry-load').hidden = false; };
    try {
      const id = params.get('order'); const supplied = new URLSearchParams(location.hash.slice(1)).get('grant');
      if (supplied) { grant = supplied; try { sessionStorage.setItem('omni.checkout.grant', grant); } catch (_) {} history.replaceState(null, '', location.pathname + location.search); }
      else if (!id) { try { grant = sessionStorage.getItem('omni.checkout.grant') || ''; } catch (_) {} }
      if (grant) order = await granted('status');
      else {
        if (!id || !/^xp_[A-Za-z0-9_-]{1,80}$/.test(id)) throw Error('请从充值页打开有效订单。');
        await identity(); order = await api('/api/omni/topups/' + encodeURIComponent(id));
        if (order.payment_url?.startsWith('https://omniapi.biz/brand/checkout.html#grant=')) grant = new URL(order.payment_url).hash.slice(7);
      }
      if (!order?.id) throw Error('订单暂不可用，请返回原账户查看。');
      if (order.server_time) offset = order.server_time - Date.now() / 1000;
      renderOrder(); poll = setInterval(() => refresh(false), 3000); timer = setInterval(tick, 1000);
      await refresh(true);
      if (!wechat && order.payment_flow === 'xorpay_cashier' && !['credited', 'review', 'expired'].includes(order.state) && order.expires_at > now()) {
        try { await prepareCashier(); renderOrder(); }
        catch (e) { handoffBlocked = true; renderOrder(); note(e.message); $('retry-load').hidden = false; }
      }
    } catch (e) { if (!sessionError(e)) { note(e.message); $('payment-title').textContent = '暂时无法加载订单'; $('payment-text').textContent = '请保留原订单，稍后重试。'; $('retry-load').hidden = false; } }
  }
  document.addEventListener('visibilitychange', () => { if (!selecting && !document.hidden) { tick(); refresh(true); } });
  document.addEventListener('securitypolicyviolation', e => {
    if (!selecting && e.effectiveDirective === 'form-action') {
      handoffBlocked = true; busy = false; renderOrder();
      note('未能打开微信收银台。请重新加载原订单后再试；如已付款，请先检查原订单结果。');
      $('retry-load').hidden = false;
    }
  });
  $('summary-toggle').addEventListener('click', () => {
    const collapsed = document.body.classList.toggle('summary-collapsed');
    $('summary-toggle').setAttribute('aria-expanded', String(!collapsed));
    $('summary-toggle').textContent = collapsed ? '显示明细 ⌄' : '收起明细 ⌃';
  });
  selecting ? bootSelect() : bootCheckout();
})();
