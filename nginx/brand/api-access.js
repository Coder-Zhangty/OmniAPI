(() => {
  'use strict';

  const BASE_URL = 'https://omniapi.biz/v1';
  const BANNER_ID = 'omniapi-api-access';

  function isKeysPage() {
    return /^\/keys\/?$/.test(location.pathname);
  }

  async function copyBaseUrl(button) {
    try {
      await navigator.clipboard.writeText(BASE_URL);
      button.textContent = '已复制';
    } catch (_) {
      const input = document.createElement('textarea');
      input.value = BASE_URL;
      input.setAttribute('readonly', '');
      input.style.position = 'fixed';
      input.style.opacity = '0';
      document.body.append(input);
      input.select();
      document.execCommand('copy');
      input.remove();
      button.textContent = '已复制';
    }
    window.setTimeout(() => { button.textContent = '复制地址'; }, 1600);
  }

  function createBanner() {
    const banner = document.createElement('section');
    banner.id = BANNER_ID;
    banner.setAttribute('aria-label', 'API 调用地址');
    const text = document.createElement('div');
    const eyebrow = document.createElement('strong');
    eyebrow.textContent = 'API Base URL';
    const hint = document.createElement('span');
    hint.textContent = '在 OpenAI 兼容客户端或代码中填写此地址';
    text.append(eyebrow, hint);
    const value = document.createElement('code');
    value.textContent = BASE_URL;
    const copy = document.createElement('button');
    copy.type = 'button';
    copy.textContent = '复制地址';
    copy.addEventListener('click', () => copyBaseUrl(copy));
    banner.append(text, value, copy);
    return banner;
  }

  function enhance() {
    const existing = document.getElementById(BANNER_ID);
    if (!isKeysPage()) {
      if (existing) existing.remove();
      return;
    }
    if (existing) return;
    const main = document.querySelector('main');
    if (main) main.prepend(createBanner());
  }

  enhance();
  new MutationObserver(enhance).observe(document.documentElement, { childList: true, subtree: true });
  window.addEventListener('popstate', enhance);
})();
