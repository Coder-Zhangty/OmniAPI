import * as omniReact from 'react'

const h = (...args) => omniReact.createElement(...args)
const STYLE = '/brand/account-workspace.v20261107b.css'

function AccountStyles() {
  return h('link', { rel: 'stylesheet', href: STYLE })
}

export function AccountThreshold({ quota, toDisplay, toQuota, step, symbol, onChange, Input }) {
  const [editing, setEditing] = omniReact.useState(null)
  return h('div', { className: 'omni-threshold-input' },
    symbol && h('span', { 'aria-hidden': true }, symbol),
    h(Input, { id: 'threshold', type: 'number', inputMode: 'decimal', min: 0, step,
      'aria-describedby': 'account-threshold-hint',
      value: editing ?? String(toDisplay(quota)),
      onChange: event => {
        const { value, valueAsNumber } = event.target
        setEditing(value)
        if (valueAsNumber >= 0 && Number.isSafeInteger(toQuota(valueAsNumber))) onChange(toQuota(valueAsNumber))
      },
      onBlur: () => setEditing(null),
    }))
}

export function AccountWeek({ records, today, month }) {
  const date = new Date(today + 'T12:00:00')
  const monday = new Date(date)
  monday.setDate(date.getDate() - (date.getDay() + 6) % 7)
  return h('div', { className: 'omni-week', 'aria-label': '本周签到' }, ['一', '二', '三', '四', '五', '六', '日'].map((label, index) => {
    const day = new Date(monday); day.setDate(monday.getDate() + index)
    const key = `${day.getFullYear()}-${String(day.getMonth() + 1).padStart(2, '0')}-${String(day.getDate()).padStart(2, '0')}`
    const known = key.startsWith(month)
    const checked = known && records[key] !== undefined
    const current = key === today
    return h('div', { key, className: 'omni-week-day' + (checked ? ' is-checked' : '') + (current ? ' is-today' : ''), title: key + (checked ? ' 已签到' : known ? '' : ' 请展开记录查看'), 'aria-current': current ? 'date' : undefined },
      h('span', null, label), h('strong', null, checked ? '✓' : current ? '今' : day.getDate()))
  }))
}

export function AccountRecharge({ affiliate, ...props }) {
  const frame = omniReact.useRef(null)
  const [height, setHeight] = omniReact.useState(260)
  const [ready, setReady] = omniReact.useState(false)
  omniReact.useEffect(() => {
    function receive(event) {
      if (event.origin !== location.origin || event.source !== frame.current?.contentWindow) return
      if (event.data?.type === 'omni-checkout-size' && Number.isFinite(event.data.height)) {
        setHeight(Math.max(120, Math.ceil(event.data.height)))
        setReady(true)
      }
      if (event.data?.type === 'omni-checkout-history') props.onOpenBilling?.()
    }
    function theme() { frame.current?.contentWindow?.postMessage({ type: 'omni-checkout-theme', dark: document.documentElement.classList.contains('dark') }, location.origin) }
    const observer = new MutationObserver(theme)
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    frame.current?.addEventListener('load', theme)
    addEventListener('message', receive)
    return () => { removeEventListener('message', receive); observer.disconnect(); frame.current?.removeEventListener('load', theme) }
  }, [props.onOpenBilling])
  return h('div', { className: 'omni-recharge' },
    h('section', { className: 'omni-inline-payment', 'aria-label': '账户充值' },
      h('div', { className: 'omni-section-heading' }, h('div', null, h('h2', null, '充值'), h('p', null, '选择金额与支付方式，确认报价后付款'))),
      !ready && h('div', { className: 'omni-frame-loading', role: 'status' }, '正在读取可用支付渠道…', h('a', { href: '/brand/topup.html', className: 'omni-text-link' }, '打开收银台')),
      h('iframe', { ref: frame, title: '账户充值', src: '/brand/topup.html?embedded=wallet&view=compact', style: { height, visibility: ready ? 'visible' : 'hidden' }, tabIndex: ready ? 0 : -1 })),
    h('div', { className: 'omni-wallet-utilities' },
    h('section', { className: 'omni-affiliate', 'aria-label': '推荐奖励' }, affiliate),
    h('section', { className: 'omni-redemption', 'aria-label': '兑换码' },
      h('h2', null, '兑换码'),
      props.loading ? h('p', { role: 'status' }, '正在读取兑换设置…') : props.topupInfo?.enable_redemption === false
        ? h('p', null, '兑换码暂未开放，请联系客服。')
        : h('form', { onSubmit: event => { event.preventDefault(); if (!props.redeeming && props.redemptionCode.trim()) props.onRedeem() } },
          h('label', { htmlFor: 'redemption-code', className: 'omni-sr-only' }, '兑换码'),
          h('input', { id: 'redemption-code', value: props.redemptionCode, placeholder: '输入兑换码', autoComplete: 'off', disabled: props.redeeming, onChange: event => props.onRedemptionCodeChange(event.target.value) }),
          h('button', { type: 'submit', className: 'omni-wallet-secondary', disabled: props.redeeming || !props.redemptionCode.trim() }, props.redeeming ? '兑换中…' : '兑换')),
      props.topupLink && h('a', { href: props.topupLink, target: '_blank', rel: 'noopener noreferrer', className: 'omni-text-link' }, '获取兑换码 ↗'))))
}

export function AccountWallet({ affiliate, stats, recharge }) {
  return h('div', { className: 'omni-account omni-wallet' },
    h(AccountStyles),
    h('link', { rel: 'stylesheet', href: '/brand/wallet-overview.v20261107.css' }),
    h('header', { className: 'omni-page-heading' }, h('h1', null, '钱包'), h('button', { type: 'button', className: 'omni-wallet-secondary', onClick: recharge.props.onOpenBilling }, '充值记录')),
    h('section', { className: 'omni-wallet-stats', 'aria-label': '账户余额与用量' }, stats),
    h('div', { id: 'wallet-add-funds' }, h(AccountRecharge, { ...recharge.props, affiliate })))
}

export function AccountProfile({ identity, profile, settings, language, sidebar, checkin }) {
  const [tab, setTab] = omniReact.useState('notifications')
  const tabs = [['notifications', '通知设置'], ['account', '账户资料'], ['preferences', '使用偏好'], ['interface', '界面设置']]
  const tabRefs = omniReact.useRef([])
  const panelId = key => ({ account: 'account-details', interface: 'account-interface' })[key] || 'account-settings'
  function moveTab(event, index) {
    let next
    if (event.key === 'ArrowRight') next = (index + 1) % tabs.length
    if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length
    if (event.key === 'Home') next = 0
    if (event.key === 'End') next = tabs.length - 1
    if (next === undefined) return
    event.preventDefault(); setTab(tabs[next][0]); tabRefs.current[next]?.focus()
  }
  return h('div', { className: 'omni-account omni-profile' },
    h(AccountStyles),
    h('header', { className: 'omni-page-heading' }, h('div', null, h('h1', null, '个人设置'), h('p', null, '管理账户资料与使用偏好'))),
    h('section', { className: 'omni-profile-identity', 'aria-label': '账户摘要' }, identity,
      profile && h('button', { type: 'button', className: 'omni-outline', onClick: () => { setTab('account'); tabRefs.current[1]?.focus() } }, '查看资料')),
    h('div', { className: 'omni-tabs', role: 'tablist', 'aria-label': '设置分类' }, tabs.map(([key, label], index) => h('button', {
      key, id: 'account-tab-' + key, role: 'tab', type: 'button', 'aria-selected': tab === key,
      'aria-controls': panelId(key), tabIndex: tab === key ? 0 : -1,
      ref: element => { tabRefs.current[index] = element }, onKeyDown: event => moveTab(event, index), onClick: () => setTab(key),
    }, label))),
    h('div', { className: 'omni-profile-grid' },
      h('div', { className: 'omni-settings-column' },
        h('section', { id: 'account-settings', role: 'tabpanel', 'aria-labelledby': 'account-tab-' + (tab === 'preferences' ? 'preferences' : 'notifications'), hidden: !['notifications', 'preferences'].includes(tab), className: 'omni-settings-panel mode-' + tab },
          h('h2', null, tab === 'preferences' ? '使用偏好' : '通知设置'), settings),
        h('section', { id: 'account-details', role: 'tabpanel', 'aria-labelledby': 'account-tab-account', hidden: tab !== 'account', className: 'omni-settings-panel' },
          h('h2', null, '账户资料'), profile && h('dl', { className: 'omni-account-details' },
            [['显示名称', profile.display_name || profile.username], ['账户名', profile.username], ['账户邮箱', profile.email || '未绑定'], ['用户 ID', profile.id], ['分组', profile.group]].map(([label, value]) =>
              h('div', { key: label }, h('dt', null, label), h('dd', null, value || '—'))))),
        h('section', { id: 'account-interface', role: 'tabpanel', 'aria-labelledby': 'account-tab-interface', hidden: tab !== 'interface', className: 'omni-interface-panel' }, language, sidebar)),
      checkin && h('aside', { className: 'omni-checkin', 'aria-label': '每日签到' }, checkin)))
}
