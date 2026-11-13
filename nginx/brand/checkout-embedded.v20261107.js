(() => {
  'use strict';
  if (parent === window || new URLSearchParams(location.search).get('embedded') !== 'wallet') return;
  document.documentElement.classList.add('omni-checkout-embedded');
  function size() { parent.postMessage({ type: 'omni-checkout-size', height: Math.ceil(document.body.getBoundingClientRect().height) + 2 }, location.origin); }
  const observer = new ResizeObserver(size); observer.observe(document.body);
  addEventListener('load', size);
  addEventListener('message', event => {
    if (event.origin !== location.origin || event.source !== parent || event.data?.type !== 'omni-checkout-theme') return;
    document.documentElement.classList.toggle('dark', event.data.dark === true);
  });
  document.addEventListener('click', event => {
    const link = event.target.closest('a[href]');
    if (!link || event.defaultPrevented || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    const url = new URL(link.href, location.href);
    if (url.origin !== location.origin) return;
    if (url.pathname === '/wallet') { event.preventDefault(); parent.postMessage({ type: 'omni-checkout-history' }, location.origin); }
    else if (['/brand/topup.html', '/brand/checkout.html'].includes(url.pathname)) { url.searchParams.set('embedded', 'wallet'); link.href = url.href; }
    else link.target = '_top';
  });
  size();
})();
