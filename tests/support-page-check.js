// 在 jsdom 里真实执行客服页与客服台的脚本。
//
// 这是用户和经营者都要用的界面，逻辑必须实测：身份从哪来、消息怎么渲染、SSE 推来的
// 内容会不会真的出现、经营者回复有没有带 CSRF。都是模拟接口，不碰任何真实服务。
//
// 运行：node tests/support-page-check.js
const { JSDOM } = require('jsdom');
const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..');
const sleep = ms => new Promise(r => setTimeout(r, ms));
let failures = 0;

function check(label, actual, expected) {
  const ok = String(actual) === String(expected);
  if (!ok) failures++;
  console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${label}: ${JSON.stringify(actual)}${ok ? '' : '  期望 ' + JSON.stringify(expected)}`);
}

const CONFIG = {
  enabled: true, service_hours: '12:00–24:00（北京时间）',
  greeting: '你好，这里是 OmniAPI 客服。', offline_hint: '现在不在服务时间，留言我们会看到。',
  privacy_note: '客服消息会保存在我们的服务器上。', max_text: 2000,
  ai_enabled: false, notify_channel: 'wecom',
};

// 一个够用的 SSE 假响应：脚本只用到 status / ok / body.getReader()。
function streamResponse(chunks) {
  let index = 0;
  return {
    ok: true, status: 200,
    body: {
      getReader: () => ({
        read: async () => (index < chunks.length
          ? { done: false, value: new TextEncoder().encode(chunks[index++]) }
          : await new Promise(() => {})),   // 之后一直挂着，模拟长连接
      }),
    },
  };
}

function load(name) {
  return {
    html: fs.readFileSync(path.join(ROOT, 'nginx/brand', name + '.html'), 'utf8'),
    js: fs.readFileSync(path.join(ROOT, 'nginx/brand', name + '.js'), 'utf8'),
  };
}

function open(html, js, url) {
  const dom = new JSDOM(html, { url, runScripts: 'outside-only' });
  const w = dom.window;
  w.TextEncoder = TextEncoder;
  w.TextDecoder = TextDecoder;
  w.AbortController = AbortController;
  w.confirm = () => true;
  return { w, d: w.document };
}

const QUEUE_ROWS = [
  { id: 'S-20260927-abc123', uid: 7, username: 'kiwi', status: 'open',
    needs_human: true, unread_operator: 3, last_message_at: '2026-09-27T10:05:00+00:00', last_seq: 1,
    last_text: '充值没到账', last_role: 'user' },
  { id: 'S-20260927-zzz999', uid: 9, username: 'other', status: 'closed',
    needs_human: false, unread_operator: 0, last_message_at: '2026-09-27T09:00:00+00:00',
    last_text: '', last_role: '' },
];

const CONV = { id: 'S-20260927-abc123', uid: 7, username: 'kiwi', status: 'open',
               needs_human: false, last_seq: 1, created_at: '2026-09-27T10:00:00+00:00',
               messages: [{ seq: 1, role: 'operator', text: '你好，有什么可以帮你',
                            created_at: '2026-09-27T10:00:00+00:00' }] };

async function openUserPage({ loggedIn = true, enabled = true, stream = [] } = {}) {
  const { html, js } = load('support');
  const { w, d } = open(html, js, 'https://omniapi.biz/brand/support.html');
  const calls = [];
  const sent = [];
  const config = Object.assign({}, CONFIG, { enabled });
  w.fetch = async (target, opts = {}) => {
    const href = String(target);
    const method = (opts.method || 'GET');
    calls.push(method + ' ' + href.split('?')[0]);
    const json = (status, payload) => ({ ok: status < 400, status,
                                         json: async () => payload });
    if (href.startsWith('/brand/api/support/config')) return json(200, { success: true, ...config });
    if (href.startsWith('/api/user/auth/refresh')) {
      if (!loggedIn) return json(401, { success: false });
      return json(200, { success: true, data: { access_token: 'TOKEN-7',
                                                 user: { id: 7, username: 'kiwi' } } });
    }
    if (href.startsWith('/brand/api/support/start')) return json(200, { success: true, conversation: CONV, config });
    if (href.startsWith('/brand/api/support/send')) {
      const body = JSON.parse(opts.body);
      sent.push({ body, auth: opts.headers.Authorization });
      return json(200, { success: true, needs_human: false,
                         message: { seq: 2, role: 'user', text: body.text,
                                    created_at: '2026-09-27T10:01:00+00:00' } });
    }
    if (href.startsWith('/brand/api/support/history')) return json(200, { success: true, messages: [] });
    if (href.startsWith('/brand/api/support/stream')) return streamResponse(stream);
    if (href.startsWith('/brand/api/support/close')) return json(200, { success: true });
    throw new Error('unexpected fetch: ' + href);
  };
  w.eval(js);
  await sleep(80);
  return { w, d, calls, sent };
}

(async () => {
  console.log('\n[1] 用户未登录时只能看到登录提示');
  {
    const { w, d, calls } = await openUserPage({ loggedIn: false });
    check('显示登录提示', d.getElementById('gate').hidden, false);
    check('不显示对话区', d.getElementById('chat-panel').hidden, true);
    check('没有调用 start', calls.includes('POST /brand/api/support/start'), false);
    w.close();
  }

  console.log('\n[2] 服务已关闭时说明原因，而不是留一个空框');
  {
    const { w, d } = await openUserPage({ enabled: false });
    check('显示关闭说明', d.getElementById('closed').hidden, false);
    check('文案来自配置', d.getElementById('closed-note').textContent.includes('不在服务时间'), true);
    check('不显示对话区', d.getElementById('chat-panel').hidden, true);
    w.close();
  }

  console.log('\n[3] 已登录：建立会话、渲染历史、显示会话编号');
  {
    const { w, d, calls } = await openUserPage();
    check('显示对话区', d.getElementById('chat-panel').hidden, false);
    check('重新获取了登录态', calls.includes('POST /api/user/auth/refresh'), true);
    check('显示了会话编号', d.getElementById('conv-id').textContent, 'S-20260927-abc123');
    check('渲染了历史消息', d.querySelectorAll('#log .bubble').length, 1);
    check('客服消息有身份标注', d.querySelector('#log .bubble .who').textContent, '客服');
    check('服务时间来自配置', d.getElementById('service-hours').textContent.includes('12:00'), true);
    check('开场白来自配置', d.getElementById('greeting').textContent.includes('OmniAPI 客服'), true);
    check('隐私说明来自配置', d.getElementById('privacy').textContent.includes('保存在我们'), true);
    w.close();
  }

  console.log('\n[4] 发送消息：带令牌、不带 uid、发完清空输入框');
  {
    const { w, d, sent } = await openUserPage();
    d.getElementById('text').value = '请问怎么接入？';
    d.getElementById('text').dispatchEvent(new w.Event('input'));
    d.getElementById('send').click();
    await sleep(60);
    check('带了访问令牌', sent[0].auth, 'Bearer TOKEN-7');
    check('请求体只有会话编号与正文',
      Object.keys(sent[0].body).sort().join(','), 'conversation_id,text');
    check('没有把 uid 发给服务端', 'uid' in sent[0].body, false);
    check('消息出现在对话里', d.getElementById('log').textContent.includes('请问怎么接入？'), true);
    check('自己的消息也标了署名',
      d.querySelector('#log .bubble.user .who').textContent, '我');
    check('输入框已清空', d.getElementById('text').value, '');
    w.close();
  }

  console.log('\n[5] Enter 直接发送，Shift+Enter 换行');
  {
    const { w, d, sent } = await openUserPage();
    const box = d.getElementById('text');
    box.value = '回车发送';
    box.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    await sleep(60);
    check('Enter 触发了发送', sent.length, 1);
    box.value = '换行';
    box.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, bubbles: true }));
    await sleep(30);
    check('Shift+Enter 不发送', sent.length, 1);
    w.close();
  }

  console.log('\n[6] SSE 推来的客服回复会出现在页面上');
  {
    const frames = [
      'event: ready\ndata: {"conversation_id":"S-20260927-abc123","since":1}\n\n',
      'event: message\ndata: {"seq":2,"role":"operator","text":"改一行 base_url 就行","created_at":"2026-09-27T10:02:00+00:00"}\n\n',
      'event: state\ndata: {"status":"open","needs_human":true,"ai_rounds":0}\n\n',
    ];
    const { w, d } = await openUserPage({ stream: frames });
    await sleep(120);
    const text = d.getElementById('log').textContent;
    check('推来的回复已渲染', text.includes('改一行 base_url 就行'), true);
    // 历史里已有一条客服消息，加上推来的这条共两条；用户自己的一条都没有。
    check('推来的算客服而不是自己', d.querySelectorAll('#log .bubble.operator').length, 2);
    check('没有把自己错标成用户', d.querySelectorAll('#log .bubble.user').length, 0);
    check('待人工状态可见', d.getElementById('conv-state').textContent.includes('需要人工处理'), true);
    w.close();
  }

  console.log('\n[7] 用户消息不会因为 SSE 回声被重复渲染');
  {
    // 服务端会把同一条消息既作为 POST 的响应、又通过 SSE 推一遍。
    const frames = ['event: message\ndata: {"seq":2,"role":"user","text":"重复测试","created_at":"2026-09-27T10:01:00+00:00"}\n\n'];
    const { w, d } = await openUserPage({ stream: frames });
    d.getElementById('text').value = '重复测试';
    d.getElementById('send').click();
    await sleep(140);
    const bubbles = Array.from(d.querySelectorAll('#log .bubble.user'))
      .filter(node => node.textContent.includes('重复测试'));
    check('同一条消息只出现一次', bubbles.length, 1);
    w.close();
  }

  console.log('\n[8] 经营者：用控制台身份进入并回复（带 CSRF）');
  {
    const { html, js } = load('support-admin');
    const { w, d } = open(html, js, 'https://omniapi.biz/brand/support-admin.html');
    const calls = [];
    const replies = [];
    let seeded = false;
    w.fetch = async (target, opts = {}) => {
      const href = String(target);
      const method = opts.method || 'GET';
      calls.push(method + ' ' + href.split('?')[0]);
      const json = (status, payload) => ({ ok: status < 400, status, json: async () => payload });
      if (href.startsWith('/brand/admin/api/support/session')) {
        if (method === 'POST') { seeded = true; return json(200, { success: true }); }
        // 补种之前未认证，补种之后认证 —— 与真实服务一致。
        return json(200, { success: true, authenticated: seeded,
                           csrf_token: seeded ? 'CSRF-1' : '' });
      }
      if (href.startsWith('/api/user/auth/refresh')) {
        return json(200, { success: true, data: { access_token: 'TOKEN-4',
                                                  user: { id: 4, username: 'kiki', role: 10 } } });
      }
      if (href.startsWith('/brand/admin/api/support/settings')) {
        return json(200, { success: true, csrf_token: 'CSRF-1', notify: { channel: 'wecom' },
                           ai: { enabled: false }, retention_days: 180, notify_credentials_present: true });
      }
      if (href.startsWith('/brand/admin/api/support/conversations')) {
        return json(200, { success: true,
                           counts: { conversations: 2, open: 2, needs_human: 1, unread_operator: 3 },
                           conversations: QUEUE_ROWS });
      }
      if (href.startsWith('/brand/admin/api/support/history')) {
        return json(200, { success: true, messages: [{ seq: 1, role: 'user', text: '充值没到账',
                                                       created_at: '2026-09-27T10:05:00+00:00' }] });
      }
      if (href.startsWith('/brand/admin/api/support/read')) return json(200, { success: true });
      if (href.startsWith('/brand/admin/api/support/reply')) {
        replies.push({ body: JSON.parse(opts.body), csrf: opts.headers['X-CSRF-Token'] });
        return json(200, { success: true, message: { seq: 2, role: 'operator', text: '我核对一下',
                                                      created_at: '2026-09-27T10:06:00+00:00' } });
      }
      if (href.startsWith('/brand/admin/api/support/stream')) {
        // 服务端推的是权威列表；假数据必须与 REST 一致，否则一连上就把列表清空了。
        return streamResponse(['event: conversations\ndata: ' + JSON.stringify({
          counts: { conversations: 2, open: 2, needs_human: 1, unread_operator: 3 },
          conversations: QUEUE_ROWS,
        }) + '\n\n']);
      }
      throw new Error('unexpected fetch: ' + href);
    };
    w.eval(js);
    await sleep(120);
    check('控制台身份换到了会话 cookie', calls.includes('POST /brand/admin/api/support/session'), true);
    check('进入了工作台', d.getElementById('board').hidden, false);
    check('列出了两个会话', d.querySelectorAll('#queue .item').length, 2);
    check('待人工的会话有醒目样式', d.querySelector('#queue .item.urgent') !== null, true);
    const badge = d.querySelector('#queue .item .pill');
    check('未读数可见', badge ? badge.textContent : '（没有徽标）', '3 条未读');
    check('设置是只读展示', d.getElementById('settings').textContent.includes('wecom'), true);

    d.querySelector('#queue .item').click();
    await sleep(80);
    check('打开了该会话的历史', d.getElementById('log').textContent.includes('充值没到账'), true);
    check('打开即标记已读', calls.includes('POST /brand/admin/api/support/read'), true);
    check('顾客消息标了是谁说的',
      d.querySelector('#log .bubble.user .who').textContent, '客户 · kiwi');
    check('标题带上顾客而不是只有编号',
      d.getElementById('thread-title').textContent, 'kiwi · S-20260927-abc123');
    check('列表直接给出最后一句是谁说的',
      d.querySelector('#queue .item .meta').textContent, '客户：充值没到账');
    check('列表仍能区分「还没有消息」的会话',
      d.querySelectorAll('#queue .item')[1].querySelector('.meta').textContent, '（还没有消息）');

    d.getElementById('text').value = '我核对一下';
    d.getElementById('send').click();
    await sleep(60);
    check('回复带了 CSRF 令牌', replies[0].csrf, 'CSRF-1');
    check('回复带上了会话编号', replies[0].body.conversation_id, 'S-20260927-abc123');
    check('回复出现在对话里', d.getElementById('log').textContent.includes('我核对一下'), true);
    const operatorLabels = Array.from(d.querySelectorAll('#log .bubble.operator .who'));
    check('自己的回复标了「我（客服）」',
      operatorLabels[operatorLabels.length - 1].textContent, '我（客服）');

    await sleep(80);
    check('SSE 推送更新了进行中数量', d.getElementById('open-count').textContent, '2');
    w.close();
  }

  console.log('\n[9] 控制台入口：人人有「在线客服」，管理员另有「客服工作台」');
  {
    // 经营者找不到「消息在哪看」，就是因为以前只有用户页入口。这个测试钉住：
    // 管理员一定拿得到工作台入口，普通用户一定拿不到。
    const js = fs.readFileSync(path.join(ROOT, 'nginx/brand/support-widget.js'), 'utf8');
    // 生产环境里 auth-enhance.js 由同一条 nginx 注入规则排在这个挂件**之前**，
    // 挂件复用它的 isAuthPage()。用例必须照同样的顺序加载，否则测的不是真实组合。
    const authJs = fs.readFileSync(path.join(ROOT, 'nginx/brand/auth-enhance.js'), 'utf8');
    const shell = '<!doctype html><html><body></body></html>';
    const entry = (d, id) => d.getElementById(id);

    async function widget({ enabled = true, role = 0, counts = null, queueStatus = 200,
                           seedRole = null, queueThrows = false,
                           url = 'https://omniapi.biz/console' } = {}) {
      const { w, d } = open(shell, js, url);
      if (seedRole !== null) {
        w.sessionStorage.setItem('omniapi.console_role',
          JSON.stringify({ role: seedRole, at: Date.now() }));
      }
      const calls = [];
      const stub = async (target) => {
        const href = String(target);
        calls.push(href.split('?')[0]);
        const json = (status, payload) => ({ ok: status < 400, status, json: async () => payload });
        if (href.startsWith('/brand/api/support/config')) {
          return json(200, { success: true, enabled });
        }
        if (href.startsWith('/api/user/auth/refresh')) {
          if (!role) return json(401, { success: false });
          return json(200, { success: true,
                             data: { access_token: 'TOKEN-4', user: { role } } });
        }
        if (href.startsWith('/brand/admin/api/support/conversations')) {
          if (queueThrows) throw new Error('network down');
          return json(queueStatus, queueStatus === 200
            ? { success: true, counts, conversations: [] }
            : { success: false, message: 'unauthorized' });
        }
        throw new Error('unexpected fetch: ' + href);
      };
      w.fetch = stub;
      w.eval(authJs);          // 增强脚本会包一层 fetch（用于登出联动）
      w.fetch = stub;          // 恢复桩，避免它被包住导致断言拿到包装后的调用
      w.eval(js);
      await sleep(60);
      return { w, d, calls };
    }

    {
      const { w, d } = await widget({ enabled: false });
      check('客服关闭时用户入口不出现', entry(d, 'omniapi-support-entry') !== null, false);
      check('客服关闭时工作台入口也不出现', entry(d, 'omniapi-support-desk-entry') !== null, false);
      w.close();
    }

    {
      const { w, d } = await widget({ role: 1 });
      check('普通用户有在线客服入口', entry(d, 'omniapi-support-entry') !== null, true);
      check('普通用户拿不到工作台入口', entry(d, 'omniapi-support-desk-entry') !== null, false);
      w.close();
    }

    {
      const counts = { conversations: 3, open: 2, needs_human: 2, unread_operator: 4 };
      const { w, d, calls } = await widget({ role: 100, counts });
      check('管理员有在线客服入口', entry(d, 'omniapi-support-entry') !== null, true);
      const desk = entry(d, 'omniapi-support-desk-entry');
      check('管理员有工作台入口', desk !== null, true);
      check('工作台入口指向工作台而不是用户页', desk ? desk.getAttribute('href') : '',
        '/brand/support-admin.html');
      check('有待人工时入口直接报数', desk ? desk.textContent : '', '客服工作台 · 2 条待人工');
      check('把角色记进了会话缓存',
        JSON.parse(w.sessionStorage.getItem('omniapi.console_role')).role, 100);
      check('这一次确实要问一次登录态',
        calls.filter(c => c === '/api/user/auth/refresh').length, 1);
      w.close();
    }

    {
      // refresh 与登录共用同一个受限流保护的桶，每次翻页都问会把人锁在门外。
      const counts = { conversations: 1, open: 1, needs_human: 0, unread_operator: 2 };
      const { w, d, calls } = await widget({ role: 100, counts, seedRole: 100 });
      check('缓存命中时仍然给出工作台入口',
        entry(d, 'omniapi-support-desk-entry') !== null, true);
      check('缓存命中时不再调用登录态接口（省一次限流额度）',
        calls.filter(c => c === '/api/user/auth/refresh').length, 0);
      w.close();
    }

    {
      const counts = { conversations: 3, open: 2, needs_human: 0, unread_operator: 5 };
      const { w, d } = await widget({ role: 100, counts });
      check('没有待人工时改为报未读数',
        entry(d, 'omniapi-support-desk-entry').textContent, '客服工作台 · 5 条未读');
      w.close();
    }

    for (const status of [401, 403]) {
      // 明确没权限（401/403）必须**不出入口**。以前这里无论成败都渲染，
      // 于是「退出登录后工作台气泡还挂着」——经营者看到就以为是安全事故。
      const { w, d } = await widget({ role: 100, queueStatus: status, seedRole: 100 });
      check('没权限时（' + status + '）不出现工作台入口',
        entry(d, 'omniapi-support-desk-entry') !== null, false);
      check('没权限时清掉缓存的角色，别让缓存把它救活',
        w.sessionStorage.getItem('omniapi.console_role'), null);
      w.close();
    }

    {
      // 但网络抖动/5xx 是另一回事：入口照给，只是不带数字 ——
      // 入口凭空消失比数字不准更让人以为功能没了。
      const { w, d } = await widget({ role: 100, queueThrows: true, seedRole: 100 });
      const desk = entry(d, 'omniapi-support-desk-entry');
      check('网络失败时入口仍然给出（只是不带计数）', desk !== null, true);
      check('不带计数时文字干净', desk ? desk.textContent : '', '客服工作台');
      w.close();
    }

    {
      // 登录/注册页上不该出现任何气泡：那里出现「客服工作台」会让人以为
      // 没登录也能进管理界面。线上经营者就是这么被吓到的。
      for (const page of ['https://omniapi.biz/sign-in', 'https://omniapi.biz/register']) {
        const { w, d } = await widget({ role: 100, counts: { needs_human: 2 },
                                        seedRole: 100, url: page });
        check(page.replace('https://omniapi.biz/', '') + ' 上不出现在线客服入口',
          entry(d, 'omniapi-support-entry') !== null, false);
        check(page.replace('https://omniapi.biz/', '') + ' 上不出现工作台入口',
          entry(d, 'omniapi-support-desk-entry') !== null, false);
        w.close();
      }
    }

    {
      // 登录页的判断必须**只有一份**：auth-enhance.js 暴露出来，挂件复用它。
      // 两个脚本各自写一份路径列表，迟早会走样（这次就是这么埋出来的）。
      const authJs = fs.readFileSync(path.join(ROOT, 'nginx/brand/auth-enhance.js'), 'utf8');
      const { w, d } = open(shell, js, 'https://omniapi.biz/sign-in');
      w.fetch = async () => ({ ok: true, status: 200, json: async () => ({ success: true }) });
      w.eval(authJs);
      check('auth-enhance 暴露了共用的登录页判断',
        typeof (w.OmniAuth && w.OmniAuth.isAuthPage), 'function');
      check('它把 /sign-in 认成登录页', w.OmniAuth.isAuthPage(), true);
      w.history.pushState({}, '', '/console');
      check('把 /console 认成非登录页', w.OmniAuth.isAuthPage(), false);
      w.history.pushState({}, '', '/sign-in');
      // 再加载挂件：走的是同一个判断，所以什么都不该渲染。
      w.fetch = async () => ({ ok: true, status: 200, json: async () => ({ success: true }) });
      w.eval(js);
      await sleep(60);
      check('挂件复用了这个判断（一个也不渲染）',
        d.querySelectorAll('a[id^="omniapi-support"]').length, 0);
      check('源码里没有再抄一份路径列表',
        /isAuthPage\(\)/.test(js) && !/sign-in|sign-up/.test(js), true);
      w.close();
    }
  }

  console.log('\n[10] 充值核验角标：登录页上同样不出现');
  {
    const js = fs.readFileSync(path.join(ROOT, 'nginx/brand/admin-badge.js'), 'utf8');
    const shell = '<!doctype html><html><body></body></html>';
    const badge = (d) => d.getElementById('omniapi-topup-notice');
    // 同样按生产顺序加载增强脚本（角标复用它的 isAuthPage）。
    const authJs = fs.readFileSync(path.join(ROOT, 'nginx/brand/auth-enhance.js'), 'utf8');
    async function probe(url) {
      const { w, d } = open(shell, js, url);
      const stub = async () => ({ ok: true, status: 200,
                                  json: async () => ({ success: true, authenticated: true,
                                                       counts: { pending: 2 } }) });
      w.fetch = stub;
      w.eval(authJs);
      w.fetch = stub;
      w.eval(js);
      await sleep(60);
      return { w, d };
    }
    {
      const { w, d } = await probe('https://omniapi.biz/console');
      check('控制台上有角标', badge(d) !== null, true);
      w.close();
    }
    {
      const { w, d } = await probe('https://omniapi.biz/sign-in');
      check('登录页上没有角标', badge(d) !== null, false);
      w.close();
    }
  }

  console.log(failures === 0 ? '\n全部通过' : `\n有 ${failures} 项失败`);
  process.exit(failures === 0 ? 0 : 1);
})();
