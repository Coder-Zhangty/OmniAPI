#!/usr/bin/env python3
"""RETIRED -- 这个探针在设计上已经跑不通，文件保留只为让旧手册不再误导人。

它靠 `token_for()` → `gateway.API.login()` 拿控制台令牌，也就是**用密码登录网关**。
Cloudflare Turnstile（`turnstile_check=true`）之后，`POST /api/user/login` 需要人机
验证，脚本再也拿不到令牌，所以「站长能进、普通用户不能进」这两条从一开始就测不了。
它另外还读 `row['cny']`，而报价订单记的是 `quoted_amount`，会直接 KeyError。

相同覆盖面已由下面这些替代：

    ops/topup_watch/probe_admin_selfheal_e2e.py  管理员会话、伪令牌 403、CSRF / Origin 守卫
    ops/topup_watch/probe_credit_e2e.py          经营者侧口令登录 → 入账
    tests/admin-page-check.js                    管理页在浏览器里的行为（jsdom）

「控制台管理员身份换会话」这一步现在只能由真实浏览器证明，这正是设计意图：
服务不代替人机验证。

原实现不做删除：改造前的完整版本保留在服务器
`/root/gateway-crlf-renorm-*.tar.gz`（内含 `ai-gateway/ops`），随时可取回。
"""
import sys

NOTICE = """\
probe_console_identity.py 已退役：它用密码登录网关来拿控制台令牌，而 Turnstile 之后
脚本做不到，因此它永远无法证明「站长能换到会话」；它还会因读 row['cny'] 直接 KeyError。
请改用 probe_admin_selfheal_e2e.py / probe_credit_e2e.py / tests/admin-page-check.js。
"""

if __name__ == '__main__':
    print(NOTICE, file=sys.stderr)
    raise SystemExit(2)
