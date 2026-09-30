#!/usr/bin/env python3
"""Safely configure operator push notifications without exposing secrets.

The WeCom webhook is read with getpass, validated, tested, and only then written
to the root-owned secrets file. It never appears in argv or normal output.
"""
import argparse
import getpass
import json
import os
import re
import sys
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'ops' / 'support'))
import notifier  # noqa: E402


def validate_wecom(value):
    value = str(value or '').strip()
    parsed = urlsplit(value)
    query = parse_qs(parsed.query, keep_blank_values=True)
    if (parsed.scheme != 'https' or parsed.hostname != 'qyapi.weixin.qq.com' or
            parsed.path != '/cgi-bin/webhook/send' or set(query) != {'key'} or
            len(query['key']) != 1 or
            not re.fullmatch(r'[A-Za-z0-9_-]{16,128}', query['key'][0])):
        raise ValueError('Webhook 必须是企业微信 qyapi.weixin.qq.com 的群机器人地址')
    return value


def atomic_json(path, value, mode):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name('.' + path.name + '.tmp')
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as handle:
            json.dump(value, handle, ensure_ascii=False, indent=2)
            handle.write('\n')
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    finally:
        if temporary.exists():
            temporary.unlink()


def service_owns_secret(path):
    """Keep mode 0600 while allowing the unprivileged services to read it."""
    if os.name != 'posix' or os.geteuid() != 0:
        return
    try:
        import grp
        import pwd
        os.chown(path, pwd.getpwnam('omniapi-watch').pw_uid,
                 grp.getgrnam('omniapi').gr_gid)
    except (KeyError, PermissionError):
        raise SystemExit('BLOCKED: 找不到 omniapi-watch:omniapi 或无法设置密钥所有者')


def services_can_read_config(path):
    """The config is root-managed but group-readable by both notification services."""
    if os.name != 'posix' or os.geteuid() != 0:
        return
    try:
        import grp
        os.chown(path, 0, grp.getgrnam('omniapi').gr_gid)
        os.chmod(path, 0o640)
    except (KeyError, PermissionError):
        raise SystemExit('BLOCKED: 找不到 omniapi 属组或无法设置配置文件权限')


def main():
    parser = argparse.ArgumentParser(description='配置 OmniAPI 客服即时通知')
    parser.add_argument('--channel', choices=('wecom', 'none'), default='wecom')
    parser.add_argument('--config', type=Path, default=ROOT / 'config/support.json')
    parser.add_argument('--secrets', type=Path, default=ROOT / '.secrets/support-notify.json')
    parser.add_argument('--skip-test', action='store_true')
    args = parser.parse_args()

    config = json.loads(args.config.read_text(encoding='utf-8'))
    config.setdefault('notify', {})
    if args.channel == 'none':
        config['notify']['channel'] = 'none'
        atomic_json(args.config, config, 0o640)
        services_can_read_config(args.config)
        print('PASS: 客服即时通知已停用；已有密钥未删除。')
        return

    webhook = validate_wecom(getpass.getpass('企业微信机器人 Webhook（隐藏输入）: '))
    secrets = notifier.load_secrets(args.secrets)
    secrets['wecom'] = {'webhook': webhook}
    if not args.skip_test:
        result = notifier.publish('wecom', secrets, 'OmniAPI 客服通知测试',
                                  '测试成功。以后新工单会推送到这个群。',
                                  'https://omniapi.biz/brand/support-admin.html')
        if not result.get('ok'):
            raise SystemExit('BLOCKED: 企业微信测试失败：%s' % result.get('error'))
    atomic_json(args.secrets, secrets, 0o600)
    service_owns_secret(args.secrets)
    config['notify']['channel'] = 'wecom'
    config['notify'].setdefault('min_interval_seconds', 60)
    atomic_json(args.config, config, 0o640)
    services_can_read_config(args.config)
    print('PASS: 企业微信客服通知已启用，密钥文件权限为 0600。')


if __name__ == '__main__':
    main()
