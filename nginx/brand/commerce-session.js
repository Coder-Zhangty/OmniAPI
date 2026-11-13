(() => {
  'use strict';
  if (window.OmniCommerce) return;
  let token = '', refresh;
  async function identity() {
    if (!refresh) refresh = fetch('/api/user/auth/refresh', { method: 'POST', credentials: 'same-origin', cache: 'no-store' }).then(async r => {
      const p = await r.json();
      if (!r.ok || !p.success || !p.data?.access_token) {
        const error = Error(r.status === 401 || r.ok && !p.success ? '请先登录后继续。' : '身份服务暂不可用，请重试。');
        error.status = r.status === 401 || r.ok && !p.success ? 401 : r.status;
        throw error;
      }
      token = p.data.access_token;
      return p.data.user;
    }).finally(() => { refresh = null; });
    return refresh;
  }
  async function request(path, body, retry = true, method) {
    if (!token) await identity();
    const r = await fetch(path, { method: method || (body === undefined ? 'GET' : 'POST'), credentials: 'same-origin', cache: 'no-store', headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    if (r.status === 401 && retry) { token = ''; await identity(); return request(path, body, false, method); }
    const p = await r.json();
    if (!r.ok || p.success === false) {
      const error = Error(p.message || '请求失败，请稍后重试。');
      error.code = p.code || '';
      error.status = r.status;
      throw error;
    }
    return p;
  }
  window.OmniCommerce = Object.freeze({ identity, request });
})();
