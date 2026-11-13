(() => {
  'use strict';
  if (location.pathname !== '/pricing') return;
  fetch('/api/omni/pricing', { cache: 'no-store' }).then(response => response.json()).then(result => {
    if (!result.success || !result.data?.version) return;
    const data = result.data;
    const available = Object.keys(data.models || {}).length;
    const pending = Object.keys(data.waiting || {}).length;
    const apply = () => {
      const title = document.querySelector('h1');
      if (!title || !title.nextElementSibling) return false;
      const chinese = title.textContent.includes('模型');
      title.nextElementSibling.textContent = chinese
        ? `${available} 个模型已核验可调用 · ${pending} 个模型暂不可用`
        : `${available} verified models available · ${pending} models temporarily unavailable`;
      if (!document.getElementById('omniapi-price-verification')) {
        const note = document.createElement('p');
        note.id = 'omniapi-price-verification';
        note.style.cssText = 'margin-top:12px;color:#667b80;font-size:12px;line-height:1.7';
        const date = new Date(data.verified_at).toLocaleString(chinese ? 'zh-CN' : 'en-US', { hour12: false });
        note.textContent = chinese
          ? `价格更新后按本站统一售价计费。价格更新：${date}。人民币换算：1 USD = ${data.cny_per_usd} CNY。`
          : `Current rates apply consistently across the site. Updated: ${date}. 1 USD = ${data.cny_per_usd} CNY.`;
        title.parentElement.append(note);
      }
      if (!document.getElementById('omniapi-vision-models')) {
        const names = Object.entries(data.models || {}).filter(([, model]) => (model.restrictions || []).includes('input_images_token_priced')).map(([name]) => name).sort();
        if (names.length) {
          const details = document.createElement('details');
          details.id = 'omniapi-vision-models';
          details.style.cssText = 'margin-top:12px;font-size:13px;line-height:1.8';
          const summary = document.createElement('summary');
          summary.style.cssText = 'cursor:pointer;font-weight:600;color:var(--primary,#16787c)';
          summary.textContent = chinese ? `支持读图（${names.length} 个型号） · PNG / JPEG · 图片按输入 Token 计费` : `Image input supported (${names.length} models) · PNG / JPEG · Billed as input tokens`;
          const list = document.createElement('div');
          list.style.cssText = 'display:flex;flex-wrap:wrap;gap:6px 14px;margin-top:8px';
          for (const name of names) { const item = document.createElement('code'); item.textContent = name; item.style.overflowWrap = 'anywhere'; list.append(item); }
          details.append(summary, list);
          title.parentElement.append(details);
        }
      }
      return true;
    };
    if (apply()) return;
    const observer = new MutationObserver(() => { if (apply()) observer.disconnect(); });
    observer.observe(document.body, { childList: true, subtree: true });
    setTimeout(() => observer.disconnect(), 120000);
  }).catch(() => {});
})();
