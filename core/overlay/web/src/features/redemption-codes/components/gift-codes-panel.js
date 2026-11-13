import * as React from 'react'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { getCurrencyDisplay, getCurrencyLabel } from '@/lib/currency'
import { formatQuota, getEditableQuotaStep, parseQuotaFromDollars } from '@/lib/format'

// Shared by the native redemption page and its narrowly scoped browser release.
export function GiftCodesPanel() {
  const h = React.createElement
  const [items, setItems] = React.useState([])
  const [next, setNext] = React.useState('')
  const [message, setMessage] = React.useState('')
  const [busy, setBusy] = React.useState(false)
  const [creating, setCreating] = React.useState(false)
  const [claims, setClaims] = React.useState(null)
  const lock = React.useRef(false)
  const mounted = React.useRef(true)
  const draft = React.useRef(null)
  const currency = getCurrencyLabel()
  const currencyLabel = currency === 'CNY' ? '人民币 / 元' : currency === 'USD' ? '美元' : currency
  const quotaPerUnit = getCurrencyDisplay().config.quotaPerUnit
  const amountLabel = record => formatQuota(record.quota ?? Math.round(Number(record.amount_usd) * quotaPerUnit))
  const stamp = value => value ? new Date(value * 1000).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' }) : '不限时'
  async function request(path, body, method = 'POST') {
    const response = body === undefined ? await api.get('/api/omni/admin/gifts' + path) : await api.request({ url: '/api/omni/admin/gifts' + path, method, data: body })
    if (!response.data?.success) throw Error(response.data?.message || '操作失败，请重试。')
    return response.data.data
  }
  async function load(cursor = '') {
    const result = await request(cursor ? '?before=' + encodeURIComponent(cursor) : '')
    if (!mounted.current) return
    setItems(previous => cursor ? [...previous, ...result.items] : result.items)
    setNext(result.next || '')
  }
  async function run(fn) {
    if (lock.current) return
    lock.current = true; setBusy(true); setMessage('')
    try { await fn() } catch (error) { if (mounted.current) setMessage(error.response?.data?.message || error.message || '请求失败，请重试。') }
    finally { lock.current = false; if (mounted.current) setBusy(false) }
  }
  React.useEffect(() => { mounted.current = true; run(() => load()); return () => { mounted.current = false } }, [])
  const button = (label, onClick, variant = 'outline') => h(Button, { type: 'button', size: 'sm', variant, disabled: busy, onClick }, label)
  async function submit(event) {
    event.preventDefault()
    const form = event.currentTarget
    const data = new FormData(form)
    const expiry = data.get('expires')
    const entered = String(data.get('amount')).trim()
    const quota = parseQuotaFromDollars(Number(entered))
    if (!/^[0-9]+(\.[0-9]{1,6})?$/.test(entered) || !Number.isSafeInteger(quota) || quota <= 0 || !Number.isSafeInteger(quotaPerUnit) || quotaPerUnit <= 0) { setMessage('请输入有效的正数额度，最多六位小数。'); return }
    // The existing backend accepts exact USD decimals; convert native quota units
    // with integer arithmetic rather than relabeling the entered currency.
    const scaled = BigInt(quota) * 1000000n
    if (scaled % BigInt(quotaPerUnit) !== 0n) { setMessage('额度无法精确换算，请调整末位小数。'); return }
    const micros = scaled / BigInt(quotaPerUnit)
    const usd = (micros / 1000000n).toString() + '.' + (micros % 1000000n).toString().padStart(6, '0')
    if (micros > 10000000000n) { setMessage('额度超出可发放上限，请调低每人额度。'); return }
    const payload = { title: data.get('title').trim(), amount_usd: usd, max_claims: Number(data.get('limit')), expires_at: expiry ? Math.floor(new Date(expiry + ':00+08:00').getTime() / 1000) : 0 }
    if (expiry && (!Number.isFinite(payload.expires_at) || payload.expires_at <= Date.now() / 1000)) { setMessage('请选择未来的截止时间。'); return }
    const signature = JSON.stringify(payload)
    let saved = draft.current
    if (!saved) { try { saved = JSON.parse(sessionStorage.getItem('omni-gift-draft') || 'null') } catch {} }
    if (!saved || saved.signature !== signature) saved = { signature, request_id: crypto.randomUUID() }
    draft.current = saved
    try { sessionStorage.setItem('omni-gift-draft', JSON.stringify(saved)) } catch {}
    await run(async () => {
      const result = await request('', { ...payload, request_id: saved.request_id })
      draft.current = null
      try { sessionStorage.removeItem('omni-gift-draft') } catch {}
      setCreating(false); setMessage('已创建：' + result.code + '。用户可直接在钱包的兑换码输入框领取。')
      await load()
    })
  }
  const inputStyle = { width: '100%', minWidth: 0, border: '1px solid var(--border)', borderRadius: 6, padding: '7px 10px', background: 'var(--background)', color: 'inherit' }
  const field = (label, props) => h('label', { style: { display: 'grid', gap: 6, fontSize: 14 } }, label, h('input', { ...props, style: inputStyle, disabled: busy }))
  const cell = (text, key) => h('td', { key, style: { padding: '12px 14px', borderBottom: '1px solid var(--border)', verticalAlign: 'top' } }, text)
  const table = (headers, rows) => h('div', { style: { overflowX: 'auto', border: '1px solid var(--border)', borderRadius: 8 } }, h('table', { style: { width: '100%', fontSize: 14, textAlign: 'left', borderCollapse: 'collapse' } },
    h('thead', null, h('tr', { style: { background: 'var(--muted)' } }, headers.map(text => h('th', { key: text, scope: 'col', style: { padding: '10px 14px', whiteSpace: 'nowrap' } }, text)))), h('tbody', null, rows)))
  async function showClaims(campaign, after = 0) {
    const result = await request('/' + campaign.id + '/claims?after_user=' + after)
    setClaims(previous => ({ campaign, items: after ? [...previous.items, ...result.items] : result.items, next: result.next_user || 0 }))
  }
  return h('div', { style: { height: '100%', overflow: 'auto', paddingBottom: 16 } },
    h('div', { className: 'flex flex-wrap items-center justify-between gap-2', style: { marginBottom: 16 } },
      h('p', { className: 'text-muted-foreground text-sm' }, '同一码多人领取，每账号一次；额度属于赠额，不计实付或会员累计。'),
      h('div', { className: 'flex gap-2' }, button('刷新', () => run(() => load())), button(creating ? '取消创建' : '创建多人福利码', () => setCreating(!creating), 'default'))),
    message && h('p', { role: 'status', style: { overflowWrap: 'anywhere', marginBottom: 16 } }, message),
    creating && h('form', { method: 'post', onSubmit: submit, style: { border: '1px solid var(--border)', borderRadius: 8, padding: 16, marginBottom: 16 } },
      h('div', { style: { display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(min(100%,200px),1fr))', gap: 16 } },
        field('名称', { name: 'title', maxLength: 60, required: true, placeholder: '例如：周末福利' }),
        field('每人额度（' + currencyLabel + '）', { name: 'amount', required: true, type: 'number', inputMode: 'decimal', min: getEditableQuotaStep(), step: getEditableQuotaStep(), placeholder: '1.00' }),
        field('限领人数', { name: 'limit', type: 'number', required: true, min: 1, max: 100000, step: 1, placeholder: '100' }),
        field('截止时间（北京时间，选填）', { name: 'expires', type: 'datetime-local' })),
      h('p', { className: 'text-muted-foreground text-sm', style: { margin: '12px 0' } }, '创建后金额和人数固定，可停用。用户继续在原钱包页面兑换。'),
      h(Button, { type: 'submit', size: 'sm', disabled: busy }, busy ? '正在保存…' : '创建福利码')),
    table(['名称 / 兑换码', '每人额度', '已领 / 限领人数', '状态', '截止时间', '操作'], items.map(c => {
      const state = !c.enabled ? '已停用' : c.expires_at && c.expires_at <= Date.now() / 1000 ? '已过期' : c.claimed >= c.max_claims ? '已领完' : '领取中'
      return h('tr', { key: c.id },
        cell(h('div', null, h('div', { style: { fontWeight: 500 } }, c.title), h('code', { style: { fontSize: 12, userSelect: 'all', whiteSpace: 'nowrap' } }, c.code)), 'name'),
        cell(amountLabel(c), 'amount'), cell(c.claimed + ' / ' + c.max_claims, 'count'), cell(state, 'status'), cell(stamp(c.expires_at), 'expires'),
        cell(h('div', { className: 'flex flex-wrap gap-2' }, button('复制', () => run(async () => { try { await navigator.clipboard.writeText(c.code); setMessage('兑换码已复制。') } catch { setMessage('请选中兑换码后复制：' + c.code) } })), button('领取记录', () => run(() => showClaims(c))), button(c.enabled ? '停用' : '启用', () => run(async () => { await request('/' + c.id, { version: c.version, enabled: !c.enabled }, 'PUT'); await load() }))), 'actions'))
    })),
    !items.length && h('p', { className: 'text-muted-foreground text-sm', style: { padding: 24, textAlign: 'center' } }, busy ? '正在加载…' : '暂无多人福利码，点击“创建多人福利码”添加。'),
    next && h('div', { style: { marginTop: 12 } }, button('加载更多', () => run(() => load(next)))),
    claims && h('section', { style: { marginTop: 24 } }, h('div', { className: 'flex items-center justify-between', style: { marginBottom: 12 } }, h('h3', { className: 'font-semibold' }, claims.campaign.title + ' · 领取记录'), button('收起', () => setClaims(null))),
      table(['账号', '到账额度', '领取时间'], claims.items.map(c => h('tr', { key: c.user_id }, cell('#' + c.user_id, 'user'), cell(amountLabel(c), 'amount'), cell(stamp(c.created_at), 'date')))),
      !claims.items.length && h('p', { style: { padding: 16 } }, '暂时无人领取。'), claims.next > 0 && button('加载更多记录', () => run(() => showClaims(claims.campaign, claims.next)))))
}

export function IntegratedRedemptions({ Provider, Layout, PrimaryButtons, Table, Dialogs, title }) {
  const h = React.createElement
  const [mode, setMode] = React.useState('single')
  const [root, setRoot] = React.useState(false)
  React.useEffect(() => { let active = true; api.get('/api/user/self', { skipErrorHandler: true }).then(r => { if (active) { const allowed = r.data?.success && r.data?.data?.role === 100; setRoot(allowed); if (allowed && window.location.hash === '#gifts') setMode('gift') } }).catch(() => {}); return () => { active = false } }, [])
  return h(Provider, null,
    h(Layout, { fixedContent: true },
      h(Layout.Title, null, title),
      h(Layout.Actions, null,
        root && h('div', { role: 'group', 'aria-label': '兑换码类型', className: 'flex gap-2' },
          h(Button, { size: 'sm', variant: mode === 'single' ? 'default' : 'outline', 'aria-pressed': mode === 'single', onClick: () => setMode('single') }, '单人兑换码'),
          h(Button, { size: 'sm', variant: mode === 'gift' ? 'default' : 'outline', 'aria-pressed': mode === 'gift', onClick: () => setMode('gift') }, '多人福利码')),
        mode === 'single' && h(PrimaryButtons)),
      h(Layout.Content, null, mode === 'single' ? h(Table) : h(GiftCodesPanel))),
    h(Dialogs))
}
