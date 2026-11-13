(() => {
  'use strict';
  const hosts = new Set(['omniapi.biz', 'backup.omniapi.biz']);
  function url(value) { const u = new URL(value); if (u.protocol !== 'https:' || !hosts.has(u.hostname) || u.username || u.password || !u.pathname.startsWith('/downloads/')) throw Error('下载地址校验失败'); return u.href; }
  async function load() {
    try {
      const response = await fetch('/brand/client-releases.json', { cache: 'no-store' });
      if (!response.ok) throw Error('版本信息暂不可用');
      const manifest = await response.json();
      if (manifest.v !== 1 || !Array.isArray(manifest.releases)) throw Error('版本信息格式异常');
      let count = 0;
      for (const release of manifest.releases) {
        if (!['windows-x64','android-arm64'].includes(release.platform) || release.accepted !== true) continue;
        if (!/^[a-f0-9]{64}$/.test(release.sha256) || !Number.isSafeInteger(release.bytes) || release.bytes <= 0) throw Error('文件信息校验失败');
        const row = document.querySelector(`[data-platform="${release.platform}"]`);
        const primary = row.querySelector('.primary'), mirror = row.querySelector('.mirror');
        primary.href = url(release.primary); primary.classList.remove('disabled'); primary.removeAttribute('aria-disabled');
        mirror.href = url(release.mirror); mirror.hidden = false;
        row.querySelector('.release-meta').textContent = `${release.version} · ${(release.bytes / 1024 / 1024).toFixed(1)} MB`;
        row.querySelector('.checksum').textContent = 'SHA-256：' + release.sha256;
        row.querySelector('.notes').textContent = release.notes || '';
        row.querySelector('details').hidden = false; count++;
      }
      document.querySelector('#release-status').textContent = count ? '请选择对应平台；主线路不可用时，可使用备用下载。' : '安装包通过验收后将在这里提供下载。';
    } catch (error) { document.querySelector('#release-status').textContent = error.message; }
  }
  load();
})();
