#!/usr/bin/env python3
"""RETIRED -- 这个探针在设计上已经跑不通，文件保留只为让旧手册不再误导人。

它写于两次改动之前，而那两次改动让它赖以成立的前提全部失效：

1. Cloudflare Turnstile（`turnstile_check=true`）保护 `POST /api/user/login`，
   脚本再也无法登录网关账号——而这个探针要先登进去才谈得上提交。
2. 充值提交改为**由服务端从调用者令牌推导入账账号**，并把报价单变成唯一提交路径。
   本文件里的 `{'uid': ..., 'cny': ...}` 请求体正是被删掉的那条旧路径。

所以运行它只会报出一串与线上无关的失败，反而让人以为服务坏了。相同覆盖面已由：

    ops/topup_watch/probe_fx_quote_e2e.py        报价 → 提交 → 待核验列表（不碰线上队列）
    ops/topup_watch/probe_admin_selfheal_e2e.py  管理端入账路由、会话自愈、CSRF / Origin 守卫
    ops/topup_watch/probe_credit_e2e.py          经营者侧登录 → 入账
    ops/topup_watch/probe_balance_e2e.py         余额核对

脚本无法驱动的「用户侧两半」改在 jsdom 里验证：`tests/topup-page-check.js`、
`tests/admin-page-check.js`。

原实现不做删除：改造前的完整版本保留在服务器
`/root/gateway-crlf-renorm-*.tar.gz`（内含 `ai-gateway/ops`），随时可取回。
"""
import sys

NOTICE = """\
probe_e2e.py 已退役：它依赖「脚本登录网关」（Turnstile 之后做不到）和已被删除的
无报价提交路径（{'uid', 'cny'} 请求体），跑起来只会产出与线上无关的失败。
请改用 probe_fx_quote_e2e.py / probe_admin_selfheal_e2e.py / probe_credit_e2e.py /
probe_balance_e2e.py；用户侧交互改用 tests/topup-page-check.js、
tests/admin-page-check.js。
"""

if __name__ == '__main__':
    print(NOTICE, file=sys.stderr)
    raise SystemExit(2)
