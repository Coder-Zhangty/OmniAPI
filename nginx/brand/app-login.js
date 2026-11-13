(() => {
  'use strict';
  const request = new URL(location.href).searchParams.get('request');
  const status = document.querySelector('#status'), approve = document.querySelector('#approve');
  const login = document.querySelector('#login'), retry = document.querySelector('#retry');
  const errors = {
    UPSTREAM_UNAVAILABLE: '官网登录验证服务暂时不可用，请稍后重试。',
    UPSTREAM_TOKEN_INVALID: '官网登录已失效，请重新登录官网后再确认。',
    UNAUTHORIZED: '请先登录官网后再确认。',
    REQUEST_NOT_PENDING: '该请求已处理或已过期，请返回客户端重新发起登录。',
    REQUEST_EXPIRED: '登录请求已过期，请返回客户端重新发起登录。',
    RATE_LIMITED: '操作过于频繁，请稍后重新发起登录。',
  };
  async function check() {
    if (!/^[A-Za-z0-9_-]{16,80}$/.test(request || '')) { status.textContent = '登录请求无效，请从客户端重新发起。'; return; }
    try { const user = await OmniCommerce.identity(); status.textContent = `将登录账号：${user?.display_name || user?.username || '当前账号'}`; approve.disabled = false; login.hidden = retry.hidden = true; }
    catch { status.textContent = '请先登录官网。支持 Google、GitHub 和已有登录方式。'; login.hidden = retry.hidden = false; }
  }
  approve.addEventListener('click', async () => {
    approve.disabled = true;
    try { await OmniCommerce.request('/api/app/login/approve', { request }); status.textContent = '已确认。请返回客户端，浏览器页面可以关闭。'; approve.hidden = true; }
    catch (error) {
      status.textContent = errors[error.code] || error.message;
      approve.disabled = false;
      if (['UPSTREAM_TOKEN_INVALID', 'UNAUTHORIZED'].includes(error.code)) login.hidden = retry.hidden = false;
    }
  });
  retry.addEventListener('click', check); check();
})();
