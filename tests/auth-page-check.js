// 登录页增强脚本的行为检查（jsdom）。
//
// 钉住的核心事实：**登录页上只能有 New API 自己那一个 Turnstile 组件**。
// 服务端是从 query 参数 `turnstile` 读令牌的（`middleware/turnstile-check.go` 的
// `c.Query("turnstile")`），New API 自己的前端也是这么发的。所以我们这边再渲染一个
// 既没有用，又会让页面上出现两个验证框——线上真出现过，经营者直接问「为什么有两个」。
//
// 运行：node tests/auth-page-check.js
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

// 复刻 New API 登录表单的关键结构：邮箱 + 密码 + 自己那个 cf-turnstile 容器 + 提交按钮。
const PAGE = `<!doctype html><html><body><div id="root"><form>
  <label>用户名或电子邮件</label>
  <input name="username" type="text" placeholder="用户名">
  <label>密码</label>
  <input type="password">
  <a href="/forgot-password">忘记密码？</a>
  <div class="cf-turnstile"></div>
  <button type="submit">登录</button>
</form></div></body></html>`;

function open(url = 'https://omniapi.biz/sign-in') {
  const dom = new JSDOM(PAGE, { url, runScripts: 'outside-only' });
  const w = dom.window;
  const state = { resets: 0, urls: [] };
  w.turnstile = { reset() { state.resets++; } };
  // 记录每一次真实的请求 URL，用来证明我们没有改写它。
  w.fetch = async (input, init) => {
    state.urls.push(typeof input === 'string' ? input : input.url);
    return { ok: true, status: 200, json: async () => ({ success: true }) };
  };
  w.XMLHttpRequest = w.XMLHttpRequest || function () {};
  w.eval(fs.readFileSync(path.join(ROOT, 'nginx/brand/auth-enhance.js'), 'utf8'));
  return { w, d: w.document, state };
}

(async () => {
  console.log('\n[1] 登录页上不再多出第二个验证组件');
  {
    const { w, d } = open();
    await sleep(60);
    check('没有渲染自己的验证组件容器', d.querySelectorAll('.omni-turnstile-visible').length, 0);
    check('页面上仍然只有 SPA 那一个容器', d.querySelectorAll('.cf-turnstile').length, 1);
    // 旧版本是 1400ms 后才去渲染兜底组件，所以这里必须越过那个时间点才算证明。
    await sleep(1600);
    check('等过旧兜底组件的时间点仍然没有多出来', d.querySelectorAll('.omni-turnstile-visible').length, 0);
    w.close();
  }

  console.log('\n[2] 其它增强照旧生效');
  {
    const { w, d } = open();
    await sleep(60);
    check('邮箱输入框被改成 email 类型', d.querySelector('input[name=username]').type, 'email');
    check('注入了说明文案',
      (d.querySelector('.omni-auth-note') || {}).textContent,
      '仅支持邮箱和密码登录 · 人机验证令牌单次有效');
    check('注入了快捷登录占位',
      (d.querySelector('.omni-oauth-title') || {}).textContent, '快捷登录即将开放');
    check('「用户名」字样被改成「邮箱」',
      d.querySelector('label').textContent, '邮箱');
    w.close();
  }

  console.log('\n[3] 不改写请求 URL，也不往上面塞自己的令牌');
  {
    const { w, state } = open();
    await sleep(60);
    await w.fetch('/api/user/login?turnstile=TOKEN-FROM-SPA', { method: 'POST' });
    await sleep(120);
    check('登录请求的 URL 原样不动', state.urls[0], '/api/user/login?turnstile=TOKEN-FROM-SPA');
    check('没有多加 turnstile 参数', (state.urls[0].match(/turnstile=/g) || []).length, 1);
    w.close();
  }

  console.log('\n[4] 登录请求结束后重置人机验证（令牌单次有效）');
  {
    const { w, state } = open();
    await sleep(60);
    await w.fetch('/api/user/login?turnstile=TOKEN', { method: 'POST' });
    await sleep(150);
    check('重置了组件', state.resets, 1);
    // 其它接口不该触发重置，否则会把用户刚点好的验证清掉。
    await w.fetch('/api/user/self');
    await sleep(150);
    check('非登录请求不重置', state.resets, 1);
    w.close();
  }

  console.log('\n[5] 离开登录页时清理干净');
  {
    const { w, d } = open();
    await sleep(60);
    check('进页面时在卡片上打了标记', d.querySelectorAll('.omni-auth-card').length, 1);
    // 模拟 SPA 跳到别的路由
    w.history.pushState({}, '', '/console');
    await sleep(120);
    check('标记被清掉', d.querySelectorAll('.omni-auth-card').length, 0);
    check('说明文案被清掉', d.querySelectorAll('.omni-auth-note').length, 0);
    check('body 上的登录页标记也被去掉', d.body.className.includes('omni-auth'), false);
    w.close();
  }

  console.log('\n[6] 登录页/控制台页的判断只有一份（供其它注入脚本复用）');
  {
    const { w } = open('https://omniapi.biz/sign-in');
    check('暴露了 window.OmniAuth.isAuthPage', typeof (w.OmniAuth && w.OmniAuth.isAuthPage), 'function');
    check('/sign-in 是登录页', w.OmniAuth.isAuthPage(), true);
    w.history.pushState({}, '', '/register');
    check('/register 是登录页', w.OmniAuth.isAuthPage(), true);
    w.history.pushState({}, '', '/console');
    check('/console 不是登录页', w.OmniAuth.isAuthPage(), false);
    w.close();
  }

  console.log('\n[7] 从控制台退出登录，必须把充值核验/客服工作台的管理会话一起断掉');
  {
    // 线上真实发生过：经营者从控制台退出后，充值核验照旧能进 —— 控制台的登出只撤销
    // 它自己的会话，而那张管理 cookie 有 8 小时 TTL。共用电脑上下一个人就能加额度。
    const js = fs.readFileSync(path.join(ROOT, 'nginx/brand/auth-enhance.js'), 'utf8');
    const shell = '<!doctype html><html><body></body></html>';

    async function scenario({ authenticated = true, csrf = 'CSRF-9' } = {}) {
      const dom = new JSDOM(shell, { url: 'https://omniapi.biz/console', runScripts: 'outside-only' });
      const w = dom.window;
      const calls = [];
      // 桩必须在 eval 之前装好：脚本会包一层 fetch，之后覆盖就把它绕过去了。
      w.fetch = async (input, init) => {
        const href = typeof input === 'string' ? input : input.url;
        const headers = (init && init.headers) || {};
        calls.push({ href, method: (init && init.method) || 'GET', csrf: headers['X-CSRF-Token'] });
        const json = (payload) => ({ ok: true, status: 200, json: async () => payload });
        if (href === '/brand/admin/api/session') {
          return json({ success: true, authenticated, csrf_token: authenticated ? csrf : '',
                        gateway_session: true });
        }
        return json({ success: true });
      };
      w.sessionStorage.setItem('omniapi.console_role', JSON.stringify({ role: 100, at: Date.now() }));
      w.eval(js);
      await sleep(30);
      calls.length = 0;
      return { w, calls };
    }

    {
      const { w, calls } = await scenario();
      // SPA 用的是 axios(XHR)，所以走 XHR 这条路。
      const xhr = new w.XMLHttpRequest();
      xhr.open('POST', '/api/user/auth/logout');
      xhr.dispatchEvent(new w.Event('loadend'));
      await sleep(60);
      const logout = calls.filter(c => c.href === '/brand/admin/api/logout');
      check('登出后确实调用了注销', logout.length, 1);
      check('注销请求带上了 CSRF 令牌', logout[0] && logout[0].csrf, 'CSRF-9');
      check('注销请求用的是 POST', logout[0] && logout[0].method, 'POST');
      check('顺便清掉缓存的角色',
        w.sessionStorage.getItem('omniapi.console_role'), null);
      w.close();
    }

    {
      // fetch 也拦着：万一 SPA 换实现或者有别的调用方。
      const { w, calls } = await scenario();
      await w.fetch('/api/user/auth/logout', { method: 'POST' });
      await sleep(60);
      check('fetch 路径同样触发注销',
        calls.filter(c => c.href === '/brand/admin/api/logout').length, 1);
      w.close();
    }

    {
      // 没有管理会话时不该徒劳地打注销接口。
      const { w, calls } = await scenario({ authenticated: false });
      const xhr = new w.XMLHttpRequest();
      xhr.open('POST', '/api/user/auth/logout');
      xhr.dispatchEvent(new w.Event('loadend'));
      await sleep(60);
      check('本来就没会话时不打注销接口',
        calls.filter(c => c.href === '/brand/admin/api/logout').length, 0);
      w.close();
    }

    {
      // 登录/注册本身不是登出，绝不能被误当成登出而把会话注销掉。
      const { w, calls } = await scenario();
      const xhr = new w.XMLHttpRequest();
      xhr.open('POST', '/api/user/login');
      xhr.dispatchEvent(new w.Event('loadend'));
      await sleep(60);
      check('登录请求不会误触发注销',
        calls.filter(c => c.href === '/brand/admin/api/logout').length, 0);
      w.close();
    }
  }

  console.log(failures === 0 ? '\n全部通过' : `\n有 ${failures} 项失败`);
  process.exit(failures === 0 ? 0 : 1);
})();
