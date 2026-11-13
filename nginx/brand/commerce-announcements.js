(() => {
  'use strict';
  async function show() {
    try {
      await window.OmniCommerce.identity();
      const p = await window.OmniCommerce.request('/api/omni/announcements');
      const rows = Array.isArray(p.data) ? p.data : [];
      for (const notice of rows) {
        while (document.querySelector('.omni-notice-overlay')) await new Promise(resolve => setTimeout(resolve, 500));
        await new Promise(resolve => {
          const overlay = document.createElement('div'); overlay.className = 'omni-notice-overlay';
          const dialog = document.createElement('section'); dialog.className = 'omni-notice-dialog'; dialog.setAttribute('role', 'dialog'); dialog.setAttribute('aria-modal', 'true'); dialog.setAttribute('aria-labelledby', 'commerce-notice-title');
          const title = document.createElement('h2'); title.id = 'commerce-notice-title'; title.textContent = notice.title;
          const body = document.createElement('p'); body.textContent = notice.body;
          const button = document.createElement('button'); button.type = 'button'; button.textContent = '知道了';
          const status = document.createElement('p'); status.setAttribute('role', 'status');
          const previous = document.activeElement;
          button.onclick = async () => { button.disabled = true; try { await window.OmniCommerce.request('/api/omni/notices/read', { key: notice.key }); overlay.remove(); previous?.focus(); resolve(); } catch { status.textContent = '暂时无法保存已读状态，请重试。'; button.disabled = false; } };
          dialog.onkeydown = event => { if (event.key === 'Tab') { event.preventDefault(); button.focus(); } if (event.key === 'Escape') button.click(); };
          dialog.append(title, body, status, button); overlay.append(dialog); document.body.append(overlay); button.focus();
        });
      }
    } catch { /* Anonymous users and unavailable rollout do not show popups. */ }
  }
  show();
})();
