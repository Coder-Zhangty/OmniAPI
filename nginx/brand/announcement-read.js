(() => {
  'use strict';

  const STORAGE_KEY = 'notification-storage';
  const OWN_READ_PREFIX = 'omniapi.notice.read.';
  const AFF_SEEN_PREFIX = 'omniapi.notice.aff-count.';
  let announcements = null;
  let noticeContent = '';
  let loading = false;
  let entryCheckStarted = false;
  let entryCheckRunning = false;
  const dialogQueue = [];
  let dialogOpen = false;

  function readState() {
    try {
      const value = JSON.parse(localStorage.getItem(STORAGE_KEY) || '{}');
      value.state = value.state && typeof value.state === 'object' ? value.state : {};
      value.state.readAnnouncementKeys = Array.isArray(value.state.readAnnouncementKeys)
        ? value.state.readAnnouncementKeys : [];
      if (typeof value.version !== 'number') value.version = 0;
      return value;
    } catch (_) {
      return { state: { lastReadNotice: '', readAnnouncementKeys: [], closedUntilDate: null }, version: 0 };
    }
  }

  function isRead(key) {
    return readState().state.readAnnouncementKeys.includes(key) ||
      localStorage.getItem(OWN_READ_PREFIX + key) === '1';
  }

  function markRead(key) {
    const value = readState();
    value.state.readAnnouncementKeys = Array.from(new Set([
      ...value.state.readAnnouncementKeys,
      key,
    ]));
    localStorage.setItem(STORAGE_KEY, JSON.stringify(value));
    localStorage.setItem(OWN_READ_PREFIX + key, '1');
  }

  function normalizeText(value) {
    if (typeof value !== 'string') return '';
    // 公告由管理员维护；弹窗只显示纯文本，避免把公告内容变成 HTML 注入点。
    return value.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim();
  }

  function enqueueDialog(item) {
    if (!item || !item.key || dialogQueue.some((queued) => queued.key === item.key)) return;
    dialogQueue.push(item);
    showNextDialog();
  }

  function showNextDialog() {
    if (dialogOpen || !dialogQueue.length) return;
    dialogOpen = true;
    const item = dialogQueue.shift();
    const overlay = document.createElement('div');
    overlay.className = 'omni-notice-overlay';
    overlay.setAttribute('role', 'presentation');
    const dialog = document.createElement('section');
    dialog.className = 'omni-notice-dialog';
    dialog.setAttribute('role', 'dialog');
    dialog.setAttribute('aria-modal', 'true');
    dialog.setAttribute('aria-labelledby', 'omni-notice-title');
    const kicker = document.createElement('div');
    kicker.className = 'omni-notice-kicker';
    kicker.textContent = item.kicker || 'OmniAPI 通知';
    const title = document.createElement('h2');
    title.id = 'omni-notice-title';
    title.textContent = item.title || '通知';
    const body = document.createElement('p');
    body.textContent = item.body || '';
    const footer = document.createElement('div');
    footer.className = 'omni-notice-actions';
    if (item.href) {
      const link = document.createElement('a');
      link.href = item.href;
      link.textContent = item.action || '查看详情';
      footer.append(link);
    }
    const acknowledge = document.createElement('button');
    acknowledge.type = 'button';
    acknowledge.textContent = '我知道了';
    footer.append(acknowledge);
    dialog.append(kicker, title, body, footer);
    overlay.append(dialog);
    document.body.append(overlay);
    const close = () => {
      try { item.onAcknowledge && item.onAcknowledge(); } catch (_) {}
      overlay.remove();
      dialogOpen = false;
      showNextDialog();
    };
    acknowledge.addEventListener('click', close, { once: true });
    acknowledge.focus();
  }

  async function accessToken() {
    const response = await fetch('/api/user/auth/refresh', {
      method: 'POST', cache: 'no-store', credentials: 'same-origin',
    });
    if (!response.ok) return '';
    const payload = await response.json().catch(() => ({}));
    return payload?.data?.access_token || '';
  }

  async function loadCurrentUser() {
    const token = await accessToken();
    if (!token) return null;
    const response = await fetch('/api/user/self', {
      cache: 'no-store', credentials: 'same-origin',
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!response.ok) return null;
    const payload = await response.json().catch(() => ({}));
    return payload?.data ? { user: payload.data, token } : null;
  }

  async function queueServerNotices(identity) {
    if (!identity?.token) return;
    const response = await fetch('/brand/api/user-notices', {
      cache: 'no-store', credentials: 'same-origin',
      headers: { Authorization: `Bearer ${identity.token}` },
    });
    if (!response.ok) return;
    const payload = await response.json().catch(() => ({}));
    (Array.isArray(payload.notices) ? payload.notices : []).forEach((notice) => {
      if (!notice?.id) return;
      enqueueDialog({
        key: `server-notice:${notice.id}`,
        kicker: notice.kind === 'topup-overdue' ? '充值核验提醒' : '账户通知',
        title: normalizeText(notice.title) || 'OmniAPI 通知',
        body: normalizeText(notice.body),
        href: typeof notice.href === 'string' && notice.href.startsWith('/') ? notice.href : '',
        action: normalizeText(notice.action) || '查看详情',
        onAcknowledge: () => fetch('/brand/api/user-notices/read', {
          method: 'POST', credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${identity.token}` },
          body: JSON.stringify({ id: notice.id }),
        }).catch(() => {}),
      });
    });
  }

  function queueUnreadAnnouncements() {
    (announcements || []).forEach((item) => {
      const key = item.id !== undefined && item.id !== null ? `id:${item.id}` : '';
      if (!key || isRead(key)) return;
      enqueueDialog({
        key: `announcement:${key}`,
        kicker: '平台公告',
        title: normalizeText(item.title) || '平台通知',
        body: normalizeText(item.content || item.description || item.title),
        onAcknowledge: () => { markRead(key); render(); },
      });
    });
  }

  function queueReferralNotice(user) {
    const identity = String(user.public_id || user.id || user.email || 'current');
    const count = Math.max(0, Number(user.aff_count) || 0);
    const key = AFF_SEEN_PREFIX + identity;
    const rawSeen = localStorage.getItem(key);
    const seen = rawSeen === null ? 0 : Math.max(0, Number(rawSeen) || 0);
    if (count <= seen) {
      if (rawSeen === null) localStorage.setItem(key, String(count));
      return;
    }
    const delta = count - seen;
    enqueueDialog({
      key: `referral:${identity}:${count}`,
      kicker: '邀请奖励到账',
      title: '有人通过您的邀请码完成注册',
      body: `新增 ${delta} 位受邀用户，已为您发放 ¥${(delta * 18).toFixed(2)} 邀请奖励。奖励暂存在“推荐计划”，可前往钱包转入主余额。`,
      href: '/wallet', action: '前往钱包',
      onAcknowledge: () => localStorage.setItem(key, String(count)),
    });
  }

  async function runEntryChecks() {
    if (entryCheckStarted || entryCheckRunning || window.OmniAuth?.isAuthPage?.()) return;
    entryCheckRunning = true;
    const identity = await loadCurrentUser().catch(() => null);
    entryCheckRunning = false;
    if (!identity) return;
    entryCheckStarted = true;
    try {
      const response = await fetch('/api/notice', { cache: 'no-cache', credentials: 'same-origin' });
      const payload = response.ok ? await response.json() : {};
      noticeContent = normalizeText(payload?.data || '');
    } catch (_) { noticeContent = ''; }
    const state = readState();
    if (noticeContent && state.state.lastReadNotice !== noticeContent) {
      enqueueDialog({
        key: `notice:${noticeContent.slice(0, 80)}`,
        kicker: '平台通知', title: '请查看最新通知', body: noticeContent,
        onAcknowledge: () => {
          const value = readState();
          value.state.lastReadNotice = noticeContent;
          localStorage.setItem(STORAGE_KEY, JSON.stringify(value));
        },
      });
    }
    await queueServerNotices(identity).catch(() => {});
    queueUnreadAnnouncements();
    queueReferralNotice(identity.user);
  }

  function announcementPanel() {
    const title = Array.from(document.querySelectorAll('span,div')).find((node) => {
      const text = (node.textContent || '').trim();
      return (text === '公告' || text === 'Announcements') && node.children.length <= 2;
    });
    return title ? title.closest('.rounded-2xl') : null;
  }

  function render() {
    if (!announcements) return;
    const panel = announcementPanel();
    if (!panel) return;
    const rows = Array.from(panel.querySelectorAll('button')).filter((button) =>
      button.querySelector('time') && !button.dataset.omniAnnouncementRead
    );
    rows.forEach((row, index) => {
      const item = announcements[index];
      if (!item) return;
      const key = item.id !== undefined && item.id !== null ? `id:${item.id}` : '';
      if (!key) return;
      row.dataset.omniAnnouncementRead = key;
      row.classList.add('omni-announcement-row');
      const badge = document.createElement('span');
      badge.className = 'omni-announcement-read';
      const update = () => {
        const read = isRead(key);
        badge.textContent = read ? '已读' : '未读';
        badge.classList.toggle('is-read', read);
      };
      row.addEventListener('click', () => {
        markRead(key);
        update();
      });
      update();
      row.append(badge);
    });
  }

  function load() {
    if (announcements || loading) {
      render();
      runEntryChecks();
      return;
    }
    loading = true;
    fetch('/api/status', { cache: 'no-store', credentials: 'same-origin' })
      .then((response) => response.ok ? response.json() : Promise.reject(new Error('status unavailable')))
      .then((payload) => {
        const data = payload.data || {};
        announcements = Array.isArray(data.announcements) ? data.announcements.slice(0, 20) : [];
        render();
        runEntryChecks();
      })
      .catch(() => { announcements = []; })
      .finally(() => { loading = false; });
  }

  load();
  new MutationObserver(load).observe(document.documentElement, { childList: true, subtree: true });
})();
