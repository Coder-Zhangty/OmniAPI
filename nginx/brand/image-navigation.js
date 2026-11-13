/* Public image workspace navigation; no authentication or billing changes. */
(() => {
  'use strict';
  if (window.__omniImageNavigation) return;
  window.__omniImageNavigation = true;
  const studio = '/brand/image-studio.html';
  const guide = '/brand/connect.html';
  const style = document.createElement('style');
  style.textContent = `
    .omni-image-nav{padding:8px;flex-shrink:0}
    .omni-image-nav a{display:flex;align-items:center;gap:10px;min-height:40px;padding:10px;border-radius:6px;font-size:14px;font-weight:600;line-height:20px;color:var(--sidebar-foreground,inherit);text-decoration:none;white-space:nowrap;overflow:hidden}
    .omni-image-nav a:hover{background:var(--sidebar-accent,#edf2e9)}
    .omni-image-nav a:focus-visible{outline:2px solid #318163;outline-offset:-2px}
    .omni-image-nav svg{width:18px;height:18px;flex-shrink:0;color:#318163}
    [data-collapsible="icon"] .omni-image-nav a{width:32px;min-height:32px;padding:7px}
    [data-collapsible="icon"] .omni-image-nav span{display:none}
    #omni-image-recommendation{display:flex;align-items:center;justify-content:space-between;gap:24px;margin:18px 24px 8px;padding:20px 22px;border-left:3px solid #318163;border-radius:0 8px 8px 0;background:var(--sidebar-accent,#edf2e9);color:inherit}
    #omni-image-recommendation strong{font-size:19px;line-height:1.5;font-weight:650}
    #omni-image-recommendation p{font-size:13px;line-height:1.7;opacity:.75;margin:5px 0 0}
    #omni-image-recommendation .omni-image-actions{display:flex;align-items:center;gap:20px;flex-shrink:0}
    #omni-image-recommendation a{font-size:14px;font-weight:600;text-decoration:none}
    #omni-image-recommendation .omni-image-primary{padding:12px 18px;border-radius:6px;background:#245e49;color:white}
    #omni-image-recommendation a:hover{text-decoration:underline;text-underline-offset:4px}
    #omni-image-recommendation a:focus-visible{outline:2px solid #318163;outline-offset:4px}
    @media(max-width:760px){#omni-image-recommendation{display:block;margin:12px;padding:18px}#omni-image-recommendation .omni-image-actions{margin-top:16px;gap:16px;flex-wrap:wrap}}
  `;
  document.head.append(style);
  function enhance() {
    for (const content of document.querySelectorAll('[data-slot="sidebar-content"]')) {
      if (content.querySelector('.omni-image-nav')) continue;
      if (content.querySelector('a[href="' + studio + '"]')) continue;
      const nav = document.createElement('nav');
      nav.className = 'omni-image-nav';
      nav.setAttribute('aria-label', '图片工具');
      const link = document.createElement('a');
      link.href = studio; link.title = '网页生图';
      link.setAttribute('aria-label', '网页生图');
      link.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="18" height="18" rx="3"/><circle cx="16" cy="8" r="2"/><path d="m3 17 6-6 4 4 3-3 5 5"/></svg><span>网页生图</span>';
      nav.append(link);
      // A separate sibling leaves React's own animated menu tree intact.
      content.prepend(nav);
    }
    const existing = document.getElementById('omni-image-recommendation');
    if (!/^\/(keys|playground)\/?$/.test(location.pathname)) {
      existing?.remove(); return;
    }
    const main = document.querySelector('main');
    if (!main || existing) return;
    const notice = document.createElement('section');
    notice.id = 'omni-image-recommendation';
    notice.setAttribute('aria-label', '推荐网页生图');
    notice.innerHTML = '<div><strong>生成图片，推荐直接使用网页生图台</strong><p>无需配置客户端，选择模型后即可生成、预览和下载。</p></div><div class="omni-image-actions"><a class="omni-image-primary" href="' + studio + '">网页生成图片 ↗</a><a href="' + guide + '">客户端接入指南 →</a></div>';
    main.prepend(notice);
  }
  let queued = false;
  function schedule() {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => { queued = false; enhance(); });
  }
  enhance();
  new MutationObserver(schedule).observe(document.documentElement, {childList:true,subtree:true});
  window.addEventListener('popstate', schedule);
})();
