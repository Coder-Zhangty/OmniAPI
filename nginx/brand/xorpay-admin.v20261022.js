(() => {
 'use strict';
 const section = document.createElement('section'), title = document.createElement('h2'), status = document.createElement('p'), list = document.createElement('div'), refresh = document.createElement('button');
 title.textContent = 'XorPay 自动充值订单'; status.setAttribute('role', 'status'); refresh.type = 'button'; refresh.textContent = '刷新自动支付订单';
 section.append(title, status, refresh, list); document.querySelector('main footer').before(section);
 async function load() {
  refresh.disabled = true;
  try {
   await window.OmniCommerce.identity(); const p = await window.OmniCommerce.request('/api/omni/admin/topups'); list.replaceChildren();
   for (const row of p.data || []) {
    const item = document.createElement('p'); item.textContent = `${row.id} · 用户 ${row.user_id} · ¥${(row.cents / 100).toFixed(2)} · ${row.method} · ${row.state}` + (row.error_code ? ' · 异常：' + row.error_code : ''); list.append(item);
   }
   status.textContent = '最近 ' + (p.data || []).length + ' 笔。异常订单请在 XorPay 商户后台核对；此列表不执行人工发额度。';
  } catch (e) { status.textContent = e.message; } finally { refresh.disabled = false; }
 }
 refresh.addEventListener('click', load); load();
})();
