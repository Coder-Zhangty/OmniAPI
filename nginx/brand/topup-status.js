(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const labels = { refunded:'已退款（本金及赠额已撤回）', pending: '等待付款 / 核验', fulfilled: '已核验到账', credited: '已自动到账', expired: '已过期', unknown: '支付结果待确认', creating: '正在创建支付单', review: '待经营者核对', dismissed: '已关闭', awaiting_payment: '等待付款' };
  function render(target, rows, subscription) {
    target.replaceChildren();
    if (!rows.length) { target.textContent = '暂无记录。'; return; }
    rows.forEach(row => {
      const item = document.createElement('article'); item.className = 'payment-setting';
      const name = document.createElement('strong'); name.textContent = row.id;
      const amount = document.createElement('span'); amount.textContent = subscription ? '¥' + (row.cents / 100).toFixed(2) : `${row.quoted_amount ?? row.amount ?? '—'} ${row.currency || 'CNY'}`;
      const status = document.createElement('span'); status.textContent = row.payment_method === 'wallet' ? '钱包已扣款 · 订阅已购买' : labels[row.state || row.status] || '处理中';
      const at = document.createElement('time'); const stamp = row.created_at || row.at; at.textContent = stamp ? new Date(typeof stamp === 'number' ? stamp * 1000 : stamp).toLocaleString('zh-CN') : '';
      item.append(name, amount, status, at);
      if(!subscription&&row.quota){const details=document.createElement('p');const units=q=>'US$'+(Number(q||0)/500000).toFixed(6);details.textContent='本金 '+units(row.quota)+' · 赠额 '+units(row.bonus)+' · 总到账 '+units(Number(row.quota)+Number(row.bonus||0))+(row.promotion_state==='pending_payment_time'?' · 赠额待核验付款时间':'');item.append(details);}
      target.append(item);
    });
  }
  let busy = false;
  async function load() {
    if (busy) return; busy = true; $('reload').disabled = true;
    try {
      await window.OmniCommerce.identity();
      const outcomes = await Promise.allSettled([window.OmniCommerce.request('/brand/api/topup/history'), window.OmniCommerce.request('/api/omni/orders'), window.OmniCommerce.request('/api/omni/topups'), window.OmniCommerce.request('/api/omni/self')]);
      const manual = outcomes[0].status === 'fulfilled' ? outcomes[0].value.requests || [] : [];
      const automatic = outcomes[2].status === 'fulfilled' ? (outcomes[2].value.data || []).map(r => ({ ...r, quoted_amount: (r.cents / 100).toFixed(2), currency: 'CNY' })) : [];
      const receipts=outcomes[3].status==='fulfilled'?(outcomes[3].value.data?.receipts||[]):[];
      const mergeReceipt=row=>{const receipt=receipts.find(r=>r.kind==='topup'&&(r.key==='topup:'+row.id||r.key.endsWith(':'+row.id)));return receipt?{...row,quota:receipt.quota,bonus:receipt.bonus,paid_at:receipt.paid_at,promotion_state:receipt.promotion_state,...(receipt.refunded?{state:'refunded'}:{})}:row;};
      for(const collection of [manual,automatic])for(let i=0;i<collection.length;i++)collection[i]=mergeReceipt(collection[i]);
      if (outcomes[0].status === 'fulfilled' || outcomes[2].status === 'fulfilled') render($('topups'), [...manual, ...automatic].sort((a, b) => new Date(typeof (b.created_at || b.at) === 'number' ? b.created_at * 1000 : b.created_at || b.at) - new Date(typeof (a.created_at || a.at) === 'number' ? a.created_at * 1000 : a.created_at || a.at)), false); else $('topups').textContent = outcomes[0].reason.message;
      if (outcomes[1].status === 'fulfilled') render($('subscriptions'), outcomes[1].value.data || [], true); else $('subscriptions').textContent = outcomes[1].reason.message;
      $('status').textContent = outcomes.every(r => r.status === 'fulfilled') ? '记录已更新。' : '部分记录暂时无法读取，可重试。';
    } catch (e) { $('status').textContent = e.message; }
    finally { busy = false; $('reload').disabled = false; }
  }
  $('reload').addEventListener('click', load); load();
})();
