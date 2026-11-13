(() => {
  'use strict';
  const page = document.body.dataset.page;
  if (window.self === window.top && !new URLSearchParams(location.search).has('embedded')) {
    location.replace('/' + page + location.hash); return;
  }
  window.addEventListener('message', event => {
    if (event.origin !== location.origin || event.source !== window.parent || event.data?.type !== 'omni-commerce-theme') return;
    document.documentElement.dataset.theme = event.data.dark ? 'dark' : 'light';
  });
  document.addEventListener('click', event => {
    const link = event.target.closest('a[data-console-link]');
    if (!link || event.defaultPrevented || event.button || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault(); window.top.location.assign(link.href);
  });
})();
