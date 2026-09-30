(() => {
  'use strict';

  const TARGETS = new Set([
    '尚未启用在线充值。请使用兑换码或联系管理员。',
    '管理员未开启在线支付功能，请联系管理员配置。',
  ]);
  const LINK_ID = 'omniapi-manual-topup-link';

  function enhanceWallet() {
    if (location.pathname !== '/wallet' || document.getElementById(LINK_ID)) return;
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    let node;
    while ((node = walker.nextNode())) {
      if (!TARGETS.has((node.nodeValue || '').trim())) continue;
      const link = document.createElement('a');
      link.id = LINK_ID;
      link.href = '/brand/topup.html';
      link.textContent = '前往扫码充值 · 收款到账可能延迟约 5 分钟 →';
      link.style.cssText = [
        'color:#009f72',
        'font-weight:600',
        'text-decoration:none',
        'display:inline-block',
        'padding:2px 0',
      ].join(';');
      node.parentElement.replaceChildren(link);
      return;
    }
  }

  enhanceWallet();
  new MutationObserver(enhanceWallet).observe(document.documentElement, {
    childList: true,
    subtree: true,
  });
})();
