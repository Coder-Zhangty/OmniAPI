#!/usr/bin/env python3
"""「这个令牌是谁」——充值服务和客服服务共用的唯一一份实现。

两个服务都要问同一个问题：调用者这个 Bearer 令牌属于哪个账号、是不是管理员。
把它写两遍的代价不是重复劳动，而是**「谁算管理员」这个判断会漂移**，而那恰好就是
安全边界本身。所以这里只留一份，网关永远是唯一的裁决者。

这里只做校验，不保存任何会话状态：拿到结果之后要不要记住这个令牌，由调用方决定
（充值服务会把它当作经营者会话缓存在内存里，客服服务则每次请求重新校验）。
"""
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'scripts'))

from gateway import API  # noqa: E402

MAX_TOKEN_LENGTH = 4096
# New API 的角色等级：1 = 普通用户，10 = 管理员，100 = 站长。
ADMIN_ROLE = 10


def _fetch(gateway_url, token):
    """Ask the gateway who this token belongs to. Any failure means "nobody"."""
    token = str(token or '').strip()
    if not token or len(token) > MAX_TOKEN_LENGTH:
        return None
    try:
        status, data, _ = API(gateway_url).request('/api/user/self', token=token)
    except Exception:
        return None
    if status != 200 or not isinstance(data, dict) or data.get('success') is False:
        return None
    user = data.get('data') if isinstance(data.get('data'), dict) else data
    return user if isinstance(user, dict) else None


def user_for(gateway_url, token):
    """An enabled account behind a console access token, or None.

    Returns the shape the recharge service already used, so a caller cannot tell
    which module resolved it.
    """
    user = _fetch(gateway_url, token)
    if user is None:
        return None
    try:
        user_id = int(user.get('id') or 0)
        state = int(user.get('status') or 0)
        role = int(user.get('role') or 1)
    except (TypeError, ValueError):
        return None
    if user_id <= 0 or state != 1:
        return None
    return {'id': user_id,
            'username': str(user.get('username') or ''),
            'display_name': str(user.get('display_name') or ''),
            'public_id': str(user.get('public_id') or ''),
            'role': role}


def admin_for(gateway_url, token, admin_role=ADMIN_ROLE):
    """An enabled administrator behind a console access token, or None.

    Same lookup, different bar: the role has to reach `admin_role`. The service on
    the other end of this session can then act with the operator's own identity
    rather than us trusting a number the browser sent.
    """
    user = _fetch(gateway_url, token)
    if user is None:
        return None
    try:
        role = int(user.get('role') or 0)
        state = int(user.get('status') or 0)
    except (TypeError, ValueError):
        return None
    if role < admin_role or state != 1:
        return None
    return {'id': user.get('id'), 'username': str(user.get('username') or ''), 'role': role}
