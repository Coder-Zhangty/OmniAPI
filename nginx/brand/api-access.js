(() => {
  'use strict';

  const BASE_URL = 'https://omniapi.biz/v1';
  const BACKUP_URL = 'https://omniapi.hyeroomy.com/v1';
  const BANNER_ID = 'omniapi-api-access';

  function isKeysPage() {
    return /^\/keys\/?$/.test(location.pathname);
  }

  async function copyBaseUrl(button, url) {
    try {
      await navigator.clipboard.writeText(url);
      button.textContent = '已复制';
    } catch (_) {
      const input = document.createElement('textarea');
      input.value = url;
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
    for (const [label, url, description] of [
      ['主线路', BASE_URL, '默认使用此地址'],
      ['备用线路', BACKUP_URL, '主线路访问异常时切换']
    ]) {
      const row = document.createElement('div');
      row.className = 'omniapi-endpoint-row';
      const text = document.createElement('div');
      const title = document.createElement('strong');
      title.textContent = label;
      const hint = document.createElement('span');
      hint.textContent = description;
      text.append(title, hint);
      const value = document.createElement('code');
      value.textContent = url;
      const copy = document.createElement('button');
      copy.type = 'button';
      copy.textContent = '复制地址';
      copy.setAttribute('aria-label', '复制' + label + ' API 地址');
      copy.addEventListener('click', () => copyBaseUrl(copy, url));
      row.append(text, value, copy);
      banner.append(row);
    }
    const note = document.createElement('span');
    note.textContent = 'OpenAI 兼容客户端填写完整 /v1 地址。';
    banner.append(note);
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

/* omni-image-navigation-loader */
(() => {
  if (document.getElementById("omni-image-navigation-script")) return;
  const script = document.createElement("script");
  script.id = "omni-image-navigation-script";
  script.src = "/brand/image-navigation.js?v=20261016-1";
  script.defer = true;
  document.head.append(script);
})();

/* omni-risk-navigation-loader */
(() => {
  if (document.getElementById('omni-risk-navigation-script')) return;
  const script = document.createElement('script');
  script.id = 'omni-risk-navigation-script';
  script.src = '/brand/risk-navigation.js?v=20261016-1';
  script.defer = true;
  document.head.append(script);
})();

