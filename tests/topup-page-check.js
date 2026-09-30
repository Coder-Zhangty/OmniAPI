// 在 jsdom 里真实执行 nginx/brand/topup.js，验证当前的「多支付方式 + 报价锁定」
// 充值页：账号自动识别、金额档位、报价（含汇率来源与备用汇率标注）、交易参考号、
// 以及「我已付款」提交。这是支付页面，逻辑必须实测而不是靠推断。
//
// rc.40 的登录态是 httpOnly refresh cookie，静态页面读不到 localStorage；充值页调用
// POST /api/user/auth/refresh 换取当前用户。这里用 mock fetch 模拟这些接口。
//
// 运行：node tests/topup-page-check.js
// 依赖：一次性 npm install jsdom
const { JSDOM } = require('jsdom');
const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..');
const html = fs.readFileSync(path.join(ROOT, 'nginx/brand/topup.html'), 'utf8');
const js = fs.readFileSync(path.join(ROOT, 'nginx/brand/topup.js'), 'utf8');
const catalog = JSON.parse(fs.readFileSync(path.join(ROOT, 'nginx/brand/topup.json'), 'utf8'));

const METHODS = [
  { id: 'cny-qr', kind: 'cny_qr', title: '微信 / 支付宝扫码', currency: 'CNY', minimum: '1',
    enabled: true, sort: 10, qr_path: '/brand/payment-qr.png', instructions: '请支付准确金额。' },
  { id: 'payoneer', kind: 'payoneer', title: 'Payoneer', currency: 'USD', minimum: '20',
    enabled: true, sort: 20, link: 'https://link.payoneer.com/Token?t=EXAMPLE',
    qr_path: '/brand/payoneer-qr.png', instructions: '仅收 USD。' },
];

const FALLBACK_NOTE = '实时行情暂不可用（行情源报价已超出可用时限），本单按经营者设定的备用汇率折算，请知悉。';
const RATES = {
  '10': { usd: '1.481722', rate: '6.7489', source: '东方财富行情（USD 货币对，延迟行情）' },
  '39': { usd: '5.778720', rate: '6.7489', source: '东方财富行情（USD 货币对，延迟行情）' },
};
const sleep = ms => new Promise(r => setTimeout(r, ms));
let failures = 0;

function check(label, actual, expected) {
  const ok = String(actual) === String(expected);
  if (!ok) failures++;
  console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${label}: ${JSON.stringify(actual)}${ok ? '' : '  期望 ' + JSON.stringify(expected)}`);
}

// session: 登录用户对象；null 表示未登录（refresh 返回 401）
// submit: 'ok' | 'duplicate' | 'fail'
// quote: {fallback:true} 让报价带上备用汇率说明
async function open(url, session, { submit = 'ok', quote = {} } = {}) {
  const dom = new JSDOM(html, { url, runScripts: 'outside-only' });
  const w = dom.window;
  const calls = [];
  const submitted = [];
  const quotes = [];
  w.fetch = async (target, opts = {}) => {
    const href = String(target);
    calls.push((opts.method || 'GET') + ' ' + href);
    if (href.startsWith('/api/user/auth/refresh')) {
      if (!session) return { ok: false, status: 401, json: async () => ({ success: false }) };
      return { ok: true, status: 200,
        json: async () => ({ success: true,
          data: { user: session, access_token: 'TOKEN-' + session.id } }) };
    }
    if (href.startsWith('/brand/api/payment-methods')) {
      return { ok: true, status: 200, json: async () => ({ success: true, methods: METHODS }) };
    }
    if (href.startsWith('/brand/api/topup/quote')) {
      const body = JSON.parse(opts.body);
      quotes.push(body);
      const row = METHODS.find(m => m.id === body.method);
      // A USD order credits the net amount 1:1; a CNY order is converted.
      const known = row.currency === 'USD'
        ? { usd: String(body.amount), rate: '1', source: 'USD settlement', asOf: null }
        : { usd: (RATES[String(body.amount)] || RATES['10']).usd, rate: '6.7489',
            source: '东方财富行情（USD 货币对，延迟行情）', asOf: '2026-09-26T08:00:00+00:00' };
      return {
        ok: true, status: 200,
        json: async () => ({ success: true, quote: {
          id: 'Q-20260927-abc123', method: body.method, currency: row.currency,
          quoted_amount: String(body.amount), usd: known.usd, fx_rate: known.rate,
          fx_source: known.source, fx_as_of: known.asOf,
          fx_note: quote.fallback ? FALLBACK_NOTE : undefined,
          expires_at: new Date(Date.now() + 900000).toISOString(),
          method_details: row,
        } }),
      };
    }
    if (href.startsWith('/brand/api/topup/request')) {
      const body = JSON.parse(opts.body);
      submitted.push(Object.assign({}, body,
        { __auth: (opts.headers && opts.headers.Authorization) || null }));
      if (submit === 'fail') {
        return { ok: false, status: 502, json: async () => ({ success: false, message: '服务暂时不可用' }) };
      }
      return {
        ok: true, status: 200,
        json: async () => ({
          success: true, duplicate: submit === 'duplicate',
          request_id: 'T-20260927-abc123', message: '已记录你的充值申请。',
          account: { id: session.id, username: session.username,
                     display_name: session.display_name || session.username,
                     public_id: 'usr_server0000000000000000000' },
          counts: { pending: 1 },
        }),
      };
    }
    if (href.startsWith('/brand/topup.json')) {
      return { ok: true, status: 200, json: async () => catalog };
    }
    throw new Error('unexpected fetch: ' + href);
  };
  w.navigator.clipboard = { writeText: async () => {} };
  w.eval(js);
  await sleep(160);
  return { w, d: w.document, calls, submitted, quotes };
}

(async () => {
  console.log('catalog: min =', catalog.min_amount_cny, '| 档位 =', catalog.amounts.map(a => a.cny).join('/'),
    '| fx =', catalog.fx_enabled, catalog.fx_provider);

  console.log('\n[1] 已登录用户打开页面（靠 refresh cookie 识别）');
  {
    const { w, d, calls } = await open('https://omniapi.biz/brand/topup.html',
      { id: 7, username: 'kiwi', display_name: 'Kiwi',
        public_id: 'usr_7k2m9q4x8n3p5rtvw2yzb6c' });
    check('自动识别账号并显示公开标识', d.getElementById('account-id').textContent,
      'Kiwi（usr_7k2m9q4x8n3p5rtvw2yzb6c）');
    check('页面上不出现数字用户 ID', d.body.textContent.includes('ID 7'), false);
    check('确实调了 refresh 接口', calls.some(c => c.includes('/api/user/auth/refresh')), true);
    check('渲染了两种支付方式', d.querySelectorAll('#methods .method-option').length, METHODS.length);
    check('默认方式为人民币扫码', d.querySelector('#methods .method-option.active').dataset.id, 'cny-qr');
    check('金额档位数量', d.querySelectorAll('#amounts .plan').length, catalog.amounts.length);
    check('默认选中 ¥10', d.getElementById('chosen-price').textContent, '¥10.00');
    check('未取得报价前不能提交', d.getElementById('submit-request').disabled, true);
    check('已无用户 ID 输入框', d.getElementById('user-id'), 'null');
    check('汇率说明提到每单显示汇率', d.getElementById('fx-note').textContent.includes('本单使用的汇率'), true);
    check('汇率说明提到最长沿用天数', d.getElementById('fx-note').textContent.includes(String(catalog.fx_max_age_days)), true);
    check('备用汇率提示默认隐藏', d.getElementById('quote-warning').hidden, true);
    w.close();
  }

  console.log('\n[2] ?uid= 参数被忽略（入账账号由服务端从会话推导）');
  {
    const { w, d, calls } = await open('https://omniapi.biz/brand/topup.html?uid=2', null);
    check('未登录时不认 URL 里的账号', d.getElementById('account-id').textContent, '未检测到登录状态');
    check('仍靠 refresh 判定登录态', calls.some(c => c.includes('/api/user/auth/refresh')), true);
    check('未登录不能提交', d.getElementById('submit-request').disabled, true);
    w.close();
  }
  {
    const { w, d, calls } = await open('https://omniapi.biz/brand/topup.html?uid=2',
      { id: 7, username: 'kiwi', public_id: 'usr_session00000000000000000' });
    check('登录后显示的是会话账号', d.getElementById('account-id').textContent,
      'kiwi（usr_session00000000000000000）');
    check('URL 参数不参与身份判定',
      calls.some(c => c.includes('/api/user/auth/refresh')), true);
    w.close();
  }

  console.log('\n[3] 未登录访问');
  {
    const { w, d } = await open('https://omniapi.biz/brand/topup.html', null);
    check('提示未登录', d.getElementById('account-id').textContent, '未检测到登录状态');
    check('引导登录', d.getElementById('account-note').textContent.includes('请先登录'), true);
    check('获取报价按钮被禁用', d.getElementById('create-quote').disabled, true);
    w.close();
  }

  console.log('\n[4] 自定义金额边界（按当前支付方式的最低金额）');
  {
    const { w, d } = await open('https://omniapi.biz/brand/topup.html', { id: 2, username: 'gw_tester' });
    const input = d.getElementById('custom-amount');
    const type = v => { input.value = v; input.dispatchEvent(new w.Event('input', { bubbles: true })); };
    check('人民币最低金额', d.getElementById('custom-amount').min, '1');

    type('15'); await sleep(30);
    check('15 元应付', d.getElementById('chosen-price').textContent, '¥15.00');
    check('15 元无报错', d.getElementById('custom-error').textContent, '');

    type('1'); await sleep(30);
    check('1 元可用（最低档）', d.getElementById('chosen-price').textContent, '¥1.00');

    type('0.5'); await sleep(30);
    check('0.5 元被拒', d.getElementById('custom-error').textContent, '最低 ¥1.00。');
    check('被拒后不能取报价', d.getElementById('create-quote').disabled, true);

    type('33'); await sleep(30);
    check('33 元恢复可用', d.getElementById('create-quote').disabled, false);
    w.close();
  }

  console.log('\n[5] 取报价：显示本单汇率、来源与报价时间');
  {
    const { w, d, quotes } = await open('https://omniapi.biz/brand/topup.html',
      { id: 2, username: 'gw_tester' });
    const input = d.getElementById('custom-amount');
    input.value = '39'; input.dispatchEvent(new w.Event('input', { bubbles: true }));
    await sleep(30);
    d.getElementById('create-quote').click();
    await sleep(80);
    check('报价请求带支付方式', quotes[0].method, 'cny-qr');
    check('报价请求带金额字符串', quotes[0].amount, '39');
    check('展示本单汇率', d.getElementById('quote-rate').textContent.includes('6.7489 CNY = US$1'), true);
    check('展示预计额度', d.getElementById('quote-rate').textContent.includes('US$5.778720'), true);
    check('展示汇率来源', d.getElementById('quote-source').textContent.includes('东方财富行情'), true);
    check('展示报价时间', d.getElementById('quote-source').textContent.includes('2026-09-26 08:00'), true);
    check('正常报价不显示备用汇率提示', d.getElementById('quote-warning').hidden, true);
    check('人民币扫码不需要交易参考号', d.getElementById('payment-reference').hidden, true);
    check('报价后可以提交', d.getElementById('submit-request').disabled, false);
    check('展示收款码', d.getElementById('payment-qr').getAttribute('src'), '/brand/payment-qr.png');
    w.close();
  }

  console.log('\n[6] 备用汇率必须显式告知用户');
  {
    const { w, d } = await open('https://omniapi.biz/brand/topup.html',
      { id: 2, username: 'gw_tester' }, { quote: { fallback: true } });
    d.getElementById('create-quote').click();
    await sleep(80);
    check('备用汇率提示可见', d.getElementById('quote-warning').hidden, false);
    check('提示文案来自报价', d.getElementById('quote-warning').textContent, FALLBACK_NOTE);
    w.close();
  }

  console.log('\n[7] Payoneer（USD）保留交易参考号但不再强制');
  {
    const { w, d, quotes } = await open('https://omniapi.biz/brand/topup.html',
      { id: 2, username: 'gw_tester' });
    d.querySelectorAll('#methods .method-option')[1].click();
    await sleep(30);
    check('切换后最低金额为 US$20', d.getElementById('custom-amount').min, '20');
    check('默认金额为 US$20', d.getElementById('chosen-price').textContent, '$20.00');
    d.getElementById('create-quote').click();
    await sleep(80);
    check('报价请求走到 Payoneer', quotes[0].method, 'payoneer');
    check('展示收款链接', d.getElementById('payment-link').getAttribute('href'),
      'https://link.payoneer.com/Token?t=EXAMPLE');
    check('USD 订单额度等于到账金额', d.getElementById('quote-rate').textContent, '预计额度 US$20');
    check('保留交易参考号输入框', d.getElementById('payment-reference').hidden, false);
    check('标签标明选填', d.getElementById('reference-label').textContent, '交易参考号（选填）');
    check('提交按钮不依赖参考号', d.getElementById('submit-request').disabled, false);
    w.close();
  }

  console.log('\n[8] 提交「我已付款」——经营者端才会收到待核验记录');
  {
    const { w, d, submitted } = await open('https://omniapi.biz/brand/topup.html',
      { id: 7, username: 'kiwi', display_name: 'Kiwi',
        public_id: 'usr_7k2m9q4x8n3p5rtvw2yzb6c' });
    d.getElementById('create-quote').click();
    await sleep(80);
    d.getElementById('submit-request').click();
    await sleep(80);
    check('提交了 1 次', submitted.length, 1);
    check('提交体不再包含 uid', 'uid' in submitted[0], false);
    check('提交携带访问令牌', submitted[0].__auth, 'Bearer TOKEN-7');
    check('提交携带报价编号', submitted[0].quote_id, 'Q-20260927-abc123');
    check('页面显示申请编号', d.getElementById('request-id').textContent, 'T-20260927-abc123');
    check('回执采用服务端返回的账号',
      d.getElementById('request-text').value.includes('usr_server0000000000000000000'), true);
    check('确认区已展开', d.getElementById('submitted').hidden, false);
    check('提交后按钮禁用（防重复）', d.getElementById('submit-request').disabled, true);
    w.close();
  }

  console.log('\n[9] 提交接口不可用时退回人工（复制申请）');
  {
    const { w, d } = await open('https://omniapi.biz/brand/topup.html',
      { id: 7, username: 'kiwi' }, { submit: 'fail' });
    d.getElementById('create-quote').click();
    await sleep(80);
    d.getElementById('submit-request').click();
    await sleep(80);
    check('提示提交失败', d.getElementById('submit-status').textContent.includes('提交失败'), true);
    check('按钮可重试', d.getElementById('submit-request').disabled, false);
    w.close();
  }

  console.log('\n[10] 支付方式未启用时不邀请付款');
  {
    const dom = new JSDOM(html, { url: 'https://omniapi.biz/brand/topup.html', runScripts: 'outside-only' });
    const w = dom.window;
    w.fetch = async target => {
      const href = String(target);
      if (href.startsWith('/brand/api/payment-methods')) {
        return { ok: true, status: 200, json: async () => ({ success: true, methods: [] }) };
      }
      if (href.startsWith('/brand/topup.json')) {
        return { ok: true, status: 200, json: async () => catalog };
      }
      return { ok: false, status: 401, json: async () => ({ success: false }) };
    };
    w.navigator.clipboard = { writeText: async () => {} };
    w.eval(js);
    await sleep(160);
    check('隐藏结账区', w.document.getElementById('checkout').hidden, true);
    check('说明不可用原因', w.document.getElementById('availability').textContent.includes('暂无可用支付方式'), true);
    w.close();
  }

  console.log(failures === 0 ? '\n全部通过' : `\n有 ${failures} 项失败`);
  process.exit(failures === 0 ? 0 : 1);
})();
