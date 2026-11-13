'use strict';
(() => {
  const status = document.getElementById('status'), retry = document.getElementById('retry');
  const state = new URLSearchParams(location.hash.slice(1)).get('state');
  history.replaceState(null, '', location.pathname);
  const destination = 'https://archive-admin.omniapi.biz';
  async function verify() {
    if (!window.opener || !/^[a-f0-9]{64}$/.test(state || '')) {
      status.textContent = '请从封存后台点击“使用控制台管理员身份验证”打开此窗口。';
      retry.disabled = true; return;
    }
    retry.disabled = true;
    status.textContent = '正在确认控制台管理员身份…';
    try {
      const response = await fetch('/api/user/auth/refresh', {method: 'POST', credentials: 'same-origin', cache: 'no-store'});
      const payload = await response.json();
      const data = payload.data || {};
      if (!response.ok || payload.success !== true || !data.access_token) throw Error('请先在控制台登录，再点击“已登录，重新确认身份”。');
      if (Number(data.user?.role || 0) < 10) throw Error('当前登录账号不是管理员，请使用管理员账号登录控制台。');
      window.opener.postMessage({type: 'omniapi-archive-identity', state, token: data.access_token}, destination);
      status.textContent = '已提交管理员身份，请回到封存后台进行二次解锁。';
    } catch (error) { status.textContent = error.message || '无法确认登录身份，请重试。'; }
    finally { retry.disabled = false; }
  }
  retry.addEventListener('click', verify);
  void verify();
})();
