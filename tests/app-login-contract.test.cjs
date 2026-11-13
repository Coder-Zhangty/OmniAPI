const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const brand = path.join(__dirname, '..', 'nginx', 'brand');

test('commerce preserves the account-service error code without exposing credentials', async () => {
  const context = { window: {}, fetch: async (url) => ({
    ok: url.includes('refresh'), status: url.includes('refresh') ? 200 : 503,
    json: async () => url.includes('refresh')
      ? { success: true, data: { access_token: 'synthetic-token', user: { id: 7 } } }
      : { success: false, code: 'UPSTREAM_UNAVAILABLE' },
  }) };
  vm.runInNewContext(fs.readFileSync(path.join(brand, 'commerce-session.js'), 'utf8'), context);
  await assert.rejects(context.window.OmniCommerce.request('/api/app/login/approve', { request: 'synthetic-request' }),
    error => error.code === 'UPSTREAM_UNAVAILABLE' && error.status === 503 && !error.message.includes('synthetic-token'));
});

for (const [code, expected] of [
  ['UPSTREAM_UNAVAILABLE', '官网登录验证服务暂时不可用'],
  ['UPSTREAM_TOKEN_INVALID', '官网登录已失效'],
  ['REQUEST_NOT_PENDING', '该请求已处理或已过期'],
]) {
  test('authorization page explains ' + code, async () => {
    const elements = Object.fromEntries(['status', 'approve', 'login', 'retry'].map(key => [key, {
      hidden: true, disabled: true, textContent: '',
      addEventListener(type, fn) { this[type] = fn; },
    }]));
    const context = {
      URL, location: { href: 'https://omniapi.biz/brand/app-login.html?request=synthetic-request-123456' },
      document: { querySelector: selector => elements[selector.slice(1)] },
      OmniCommerce: {
        identity: async () => ({ username: 'synthetic-user' }),
        request: async () => { const error = Error('请求失败，请稍后重试。'); error.code = code; throw error; },
      },
    };
    vm.runInNewContext(fs.readFileSync(path.join(brand, 'app-login.js'), 'utf8'), context);
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(elements.approve.disabled, false);
    await elements.approve.click();
    assert.ok(elements.status.textContent.includes(expected));
    assert.equal(elements.approve.disabled, false);
    if (code === 'UPSTREAM_TOKEN_INVALID') assert.equal(elements.login.hidden, false);
  });
}
