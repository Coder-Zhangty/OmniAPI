// 在 jsdom 里真实执行 nginx/brand/admin.js，验证「网关管理员会话丢失」这条路径。
//
// 背景：入账用的网关管理员会话只活在 topup_watch 进程的内存里，而经营者那张 30 天的
// 会话 cookie 比它长命。服务一重启就会出现「面板打得开、按钮点不动」：GET session 仍然
// 返回 authenticated=true，页面于是跳过补种会话，错误提示却让人「刷新页面」——刷新走的
// 还是同一条短路逻辑，永远修不好。这个文件就是钉住这个回归的。
//
// 运行：node tests/admin-page-check.js
const { JSDOM } = require('jsdom');
const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..');
const html = fs.readFileSync(path.join(ROOT, 'nginx/brand/admin.html'), 'utf8');
const js = fs.readFileSync(path.join(ROOT, 'nginx/brand/admin.js'), 'utf8');

const sleep = ms => new Promise(r => setTimeout(r, ms));
let failures = 0;

function check(label, actual, expected) {
  const ok = String(actual) === String(expected);
  if (!ok) failures++;
  console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${label}: ${JSON.stringify(actual)}${ok ? '' : '  期望 ' + JSON.stringify(expected)}`);
}

const PENDING = {
  id: 'T-20260927-abc123', uid: 7, username: 'Kiwi', status: 'pending',
  method: 'payoneer', currency: 'USD', quoted_amount: '20', usd: '20', quota: 1000000,
  quote_id: 'Q-20260927-abc123', created_at: '2026-09-27T08:00:00+00:00',
};

// cookie: 经营者的服务 cookie 是否有效（服务重启后仍然有效）
// consoleAdmin: 控制台 refresh 是否还能换出管理员令牌
// gatewaySession: 服务进程里是否已经有网关会话
// fulfill: 依次消费的响应；'ok' 或 'gateway_session_missing'
async function open({ cookie = true, consoleAdmin = true, gatewaySession = false,
                     fulfill = ['ok'] } = {}) {
  const dom = new JSDOM(html, { url: 'https://omniapi.biz/brand/admin.html',
                                runScripts: 'outside-only' });
  const w = dom.window;
  const calls = [];
  let seeded = gatewaySession;
  let fulfillIndex = 0;
  w.confirm = () => true;

  w.fetch = async (target, opts = {}) => {
    const href = String(target);
    const method = opts.method || 'GET';
    calls.push(method + ' ' + href);
    const json = (status, payload) => ({ ok: status < 400, status,
                                         json: async () => payload });

    if (href.startsWith('/api/user/auth/refresh')) {
      if (!consoleAdmin) return json(401, { success: false });
      return json(200, { success: true,
        data: { user: { id: 4, username: 'kiki', role: 10 }, access_token: 'TOKEN-4' } });
    }
    if (href.startsWith('/brand/admin/api/session')) {
      if (method === 'POST') {
        // The gateway is the only thing that can validate the token.
        if (!consoleAdmin) return json(403, { success: false, message: '需要本站管理员登录态' });
        seeded = true;
        return json(200, { success: true, user: { id: 4 }, gateway_session: true });
      }
      if (!cookie) return json(200, { success: true, authenticated: false,
                                      gateway_session: seeded });
      return json(200, { success: true, authenticated: true, gateway_session: seeded });
    }
    if (href.startsWith('/brand/admin/api/summary')) {
      if (!cookie) return json(401, { success: false, message: '需要管理员登录' });
      return json(200, { success: true, counts: { pending: 1, fulfilled: 0, dismissed: 0 },
                         pending: [PENDING], recent: [], gateway_session: seeded });
    }
    if (href.startsWith('/brand/admin/api/settings')) {
      return json(cookie ? 200 : 401, { success: true, methods: [] });
    }
    if (href.startsWith('/brand/admin/api/fulfill')) {
      const want = fulfill[Math.min(fulfillIndex++, fulfill.length - 1)];
      if (want === 'gateway_session_missing') {
        return json(409, { success: false, code: 'gateway_session_missing',
                           message: '尚未取得网关管理员会话：服务重启后需要重新确认一次身份。' });
      }
      return json(200, { success: true, request_id: PENDING.id, quota: 1000000,
                         quota_before: 0, quota_after: 1000000, balance_verified: true,
                         net_received: '20', credited_usd: '20', transaction: 'PAY-1',
                         message: '额度已直接计入用户钱包。' });
    }
    throw new Error('unexpected fetch: ' + href);
  };

  w.eval(js);
  await sleep(200);
  return { w, d: w.document, calls,
           fulfilled: () => fulfillIndex,
           seeded: () => seeded };
}

(async () => {
  console.log('\n[1] 会话仍在：正常进面板，不打扰经营者');
  {
    const { w, d, calls } = await open({ gatewaySession: true });
    check('直接显示面板', d.getElementById('board').hidden, false);
    check('不显示网关会话提示', d.getElementById('gateway-panel').hidden, true);
    check('没有多余补种请求', calls.filter(c => c === 'POST /brand/admin/api/session').length, 0);
    w.close();
  }

  console.log('\n[2] 服务重启后：cookie 还有效，但网关会话没了 —— 页面必须自己补种');
  {
    const { w, d, calls, seeded } = await open({ gatewaySession: false });
    check('面板照常显示（cookie 仍然有效）', d.getElementById('board').hidden, false);
    check('页面自动补种了网关会话', calls.filter(c => c === 'POST /brand/admin/api/session').length, 1);
    check('服务端确实拿到会话', seeded(), true);
    check('补种成功后不显示提示', d.getElementById('gateway-panel').hidden, true);
    w.close();
  }

  console.log('\n[3] 控制台也退出了：给出可见提示与手动按钮，而不是无声失败');
  {
    const { w, d } = await open({ gatewaySession: false, consoleAdmin: false });
    check('显示网关会话提示', d.getElementById('gateway-panel').hidden, false);
    check('提示说明当前无法入账', d.getElementById('gateway-status').textContent.includes('暂时无法入账'), true);
    check('提供手动按钮', d.getElementById('gateway-retry').textContent, '重新确认身份');
    w.close();
  }

  console.log('\n[4] 入账途中会话失效：补种一次并重试，经营者无感');
  {
    const { w, d, calls, fulfilled } = await open({
      gatewaySession: true, fulfill: ['gateway_session_missing', 'ok'] });
    const row = d.querySelector('#pending-body tr');
    check('列出待核验申请', !!row, true);
    row.querySelectorAll('input')[1].value = '20';   // 净到账
    [...row.querySelectorAll('button')].find(b => b.textContent.includes('核实到账')).click();
    await sleep(220);
    check('入账请求发了两次（一次 409，重试一次）', fulfilled(), 2);
    check('重试前补种了会话', calls.filter(c => c === 'POST /brand/admin/api/session').length >= 1, true);
    check('最终显示入账结果', d.getElementById('result-panel').hidden, false);
    check('结果为余额已核对', d.getElementById('result-note').textContent.includes('已直接入账'), true);
    w.close();
  }

  console.log('\n[5] 补种失败时不把额度算成功，且明确报错');
  {
    const { w, d, fulfilled } = await open({
      gatewaySession: true, fulfill: ['gateway_session_missing', 'gateway_session_missing'] });
    const row = d.querySelector('#pending-body tr');
    row.querySelectorAll('input')[1].value = '20';
    [...row.querySelectorAll('button')].find(b => b.textContent.includes('核实到账')).click();
    await sleep(220);
    check('重试后仍然失败，未伪造成功', fulfilled(), 2);
    check('结果面板保持隐藏', d.getElementById('result-panel').hidden, true);
    check('状态栏报出真实原因', d.getElementById('board-status').textContent.includes('网关管理员会话'), true);
    w.close();
  }

  console.log(failures === 0 ? '\n全部通过' : `\n有 ${failures} 项失败`);
  process.exit(failures === 0 ? 0 : 1);
})();
