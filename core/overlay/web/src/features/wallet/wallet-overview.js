import { createElement as h } from 'react'

export function WalletSummary({ user, loading, refreshFailed, formatQuota }) {
  if (loading) return h('p', { role: 'status', className: 'wallet-balance-note' }, '正在读取余额…')
  if (!user) return h('p', { role: 'status', className: 'wallet-balance-note' }, '暂时无法读取余额')

  const hasReservation = user.wallet_reserved_quota !== undefined
  return h('div', { className: 'wallet-summary' },
    h('div', { className: 'wallet-available' },
      h('span', { className: 'wallet-caption' }, '可用余额'),
      h('strong', { className: 'wallet-amount' }, formatQuota(user.quota ?? user.wallet_balance_quota ?? 0))),
    h('div', { className: 'wallet-summary-side' },
      h('dl', { className: 'wallet-usage' },
        h('div', null, h('dt', null, '累计消费'), h('dd', null, formatQuota(user.used_quota ?? 0))),
        h('div', null, h('dt', null, 'API 请求'), h('dd', null, (user.request_count ?? 0).toLocaleString()))),
      hasReservation && h('details', { className: 'wallet-balance-details' },
        h('summary', null, '余额明细'),
        h('dl', null,
          h('div', null, h('dt', null, '钱包余额'), h('dd', null, formatQuota(user.wallet_balance_quota ?? user.quota ?? 0))),
          h('div', null, h('dt', null, '临时预留'), h('dd', null, formatQuota(user.wallet_reserved_quota)))),
        h('p', null, '请求结束后释放预留，按实际用量结算。'))),
    refreshFailed && h('p', { role: 'status', className: 'wallet-balance-note' }, '余额刷新失败，正在显示上次读取的金额。'))
}

export function WalletReferral({ user, link, onTransfer, confirmed, directCredit, loading, formatQuota, Copy }) {
  const available = user?.aff_quota ?? 0
  const earned = user?.aff_history_quota ?? 0
  const canTransfer = !directCredit && available > 0
  return h('div', { className: 'wallet-referral' },
    h('h2', null, '推荐奖励'),
    loading ? h('p', { role: 'status', className: 'wallet-caption' }, '正在读取邀请资料…') : h('div', { className: 'wallet-reward-row' },
      h('div', { className: 'wallet-reward-value' },
        h('strong', null, formatQuota(canTransfer ? available : earned)),
        h('span', { className: 'wallet-caption' }, canTransfer ? '待转入' : '累计获得')),
      h('span', { className: 'wallet-caption' }, `${user?.aff_count ?? 0} 位受邀用户`),
      canTransfer && h('button', { type: 'button', className: 'omni-wallet-secondary', onClick: onTransfer, disabled: !confirmed }, '转入余额')),
    directCredit && h('p', { className: 'wallet-caption' }, '新邀请奖励直接计入钱包余额。'),
    canTransfer && !confirmed && h('p', { className: 'wallet-caption' }, '奖励转入暂未开放。'),
    link && h('div', { className: 'wallet-invite' },
      h('input', { readOnly: true, value: link, 'aria-label': '邀请链接', onFocus: event => event.currentTarget.select() }),
      h(Copy, { value: link, variant: 'outline', className: 'omni-wallet-secondary', tooltip: '复制邀请链接', 'aria-label': '复制邀请链接' })))
}
