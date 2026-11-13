(() => {
  'use strict';
  if (parent === window || new URLSearchParams(location.search).get('embedded') !== 'wallet') return;
  document.documentElement.classList.add('omni-checkout-embedded');
  if (document.body.dataset.page === 'select') {
    const form = document.getElementById('topup-form');
    const order = document.querySelector('.order');
    const methods = form.querySelector('.methods');
    const methodGroup = document.createElement('div');
    methodGroup.className = 'wallet-payment-methods';
    const title = document.getElementById('payment-title');
    title.textContent = '支付方式';
    methodGroup.append(title, methods);
    for (const radio of methods.querySelectorAll('input')) radio.setAttribute('form', form.id);
    order.append(methodGroup);

    const summary = document.getElementById('summary-breakdown');
    const payButton = document.getElementById('pay-button');
    form.prepend(summary);
    const quote = summary.querySelector('.quote-details');
    const quotaLine = quote.querySelector('.receipt-row');
    summary.insertBefore(quotaLine, quote);
    quotaLine.classList.add('wallet-quota-estimate');
    const fee = document.getElementById('summary-fee').closest('.receipt-row');
    const fees = document.createElement('dl');
    fees.append(fee);
    quote.append(fees);
    const account = document.querySelector('.pay-content .account');
    form.insertBefore(account, payButton);
    const custom = document.getElementById('custom-amount');
    const amount = document.getElementById('amount');
    amount.setAttribute('aria-label', '自定义充值金额');
    const amountField = document.getElementById('amount-field');
    const presets = document.getElementById('presets');
    presets.after(amountField);
    const syncAmount = () => { amountField.hidden = custom.getAttribute('aria-pressed') !== 'true'; };
    new MutationObserver(syncAmount).observe(custom, { attributes: true, attributeFilter: ['aria-pressed'] });
    custom.addEventListener('click', () => { amountField.hidden = false; amount.focus(); amount.select(); });
    syncAmount();
    const warning = document.getElementById('switch-warning');
    new MutationObserver(() => { methods.inert = !warning.hidden; }).observe(warning, { attributes: true, attributeFilter: ['hidden'] });
  }
  function size() { parent.postMessage({ type: 'omni-checkout-size', height: Math.ceil(document.body.getBoundingClientRect().height) + 2 }, location.origin); }
  const observer = new ResizeObserver(size); observer.observe(document.body);
  addEventListener('load', size);
  addEventListener('message', event => {
    if (event.origin !== location.origin || event.source !== parent || event.data?.type !== 'omni-checkout-theme') return;
    document.documentElement.classList.toggle('dark', event.data.dark === true);
    size();
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
