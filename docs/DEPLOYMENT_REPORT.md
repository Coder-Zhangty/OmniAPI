# OmniAPI 线上部署报告

更新时间：2026-09-28

## 当前部署

| 项目 | 实际值 |
|---|---|
| 公网地址 | `https://omniapi.biz` |
| New API | `v1.0.0-rc.40`，镜像摘要 `sha256:37c0f99a76ed0a5d5b376b8486173415efd1ec416042b392bd564eb0ae6a55ef` |
| PostgreSQL | 15，摘要已锁定于 `images.lock.json` |
| Redis / Nginx | 摘要已锁定于 `images.lock.json` |
| 模型 | 29 个已启用并定价的真实上游文本模型 |
| 上游 | `0-0 Main`，渠道身份只在管理员后台可见 |
| HTTPS | Let's Encrypt，SAN 为 `omniapi.biz`、`www.omniapi.biz`，到期 2026-12-26 |
| 续期 | `omniapi-cert-renew.timer` 已启用且 active |
| 回滚点 | `/opt/ai-gateway/backups/2026-09-27_04-19-49` |

## 服务与端口

| 服务 | 状态 | 对外端口 |
|---|---|---|
| Nginx | healthy | 80、443 |
| New API | healthy | 无 |
| PostgreSQL | healthy | 无 |
| Redis | healthy | 无 |
| auth-guard | healthy | 无 |
| topup-watch | active | 8787 仅 Docker 网桥可访问，UFW 拒绝公网 |
| SSH | active | 22 |

公网探测 `160.202.46.77:8787` 超时。UFW 默认拒绝入站，只允许 22、80、443；8787 对 Docker 网桥 `172.16.0.0/12` 放行后立即拒绝其他来源。

## 验收

| 检查 | 状态 | 说明 |
|---|---|---|
| HTTP 与 `www` 跳转 | PASS | 均 308 到 `https://omniapi.biz`。 |
| TLS 证书与 HSTS | PASS | 双域名 SAN，TLS 1.2/1.3，HSTS 一年。 |
| Turnstile 登录/注册 | PASS | 公开 Site Key、服务端校验，登录仅接受邮箱与密码。 |
| 真实模型调用与 Streaming | PASS | 既有真实上游验收保持通过。 |
| 原生模型广场 | PASS | 29 个模型、供应商 Logo、真实计费表达式和多币种展示组件。 |
| 计费两位小数 | PASS | 页面、配置与实际 `billing_expr` 使用相同价格。 |
| 人民币与 Payoneer | PASS | 公开方式、最低金额、报价和人工核验流程通过。 |
| Xe 实时汇率 | BLOCKED | 没有 Xe Account ID / API Key，安全关闭。 |
| USDT / USDC 收款 | BLOCKED | 没有网络、钱包地址与二维码，不公开。 |
| 真实 Payoneer/链上付款 | NOT TESTED | 未进行真实资金交易。 |
| 备份 | PASS | 数据库、数据卷、配置、密钥与校验和已保存。 |
| 本次恢复演练 | NOT TESTED | 本次发布使用既有已验证备份流程，没有覆盖线上数据做恢复。 |

本地测试 `122/122 PASS`。线上 `scripts/verify_payment_upgrade.py` 的原生 pricing、29 行价格、两位小数、供应商 Logo、支付方式、FX 安全关闭、Payoneer 最低金额与链接共 10 项全部 PASS。Nginx 配置检查 PASS。

## 已知限制

- 多币种价格组件已经交付，但未配置 Xe 前只显示 USD；不会用静态或过期汇率伪装实时行情。
- CNY 收款码与 Payoneer 都是人工核验。到账、用户截图或二维码返回不会自动增加余额。
- USDT / USDC 在配置完整前隐藏。转错网络无法自动找回。
- 自动支付宝／微信支付仍需要合规商户接口、签名回调和真实沙箱验证。

充值与汇率的详细证据见 [PAYMENT_FX_REPORT.md](PAYMENT_FX_REPORT.md)，真实上游验收见 [REAL_PROVIDER_REPORT.md](REAL_PROVIDER_REPORT.md)。

## 2026-09-28 邀请奖励与首次通知修复

| 检查 | 状态 | 说明 |
|---|---|---|
| 注册邀请码位置 | PASS | 邀请码已移到邮箱之后、密码之前；线上 1440×1100 截图确认首屏可见。 |
| ¥18 转移边界 | PASS | 根因是 ¥18 反算为 1,333,551 个整数额度单位，而旧配置只有 1,333,550。未来奖励已改为 1,333,551；历史邀请按每次 1 个单位补到主余额并同步历史总额。 |
| 邀请人通知 | PASS | 登录后读取本人 `aff_count`；发现新增邀请时弹出“邀请奖励到账”，展示新增人数和 ¥18.00/人，并提供钱包入口。 |
| 公告首次弹窗 | PASS | `Notice` 与未读公告在用户进入系统后逐条弹窗；点击“我知道了”才持久化已读，之后不重复弹出。 |
| 弹窗 XSS 防护 | PASS | 公告内容先转纯文本，DOM 使用 `textContent`，不执行后台公告中的 HTML。 |
| 隔离 UI 验收 | PASS | 使用模拟用户和 1 次邀请渲染弹窗；未调用线上加额接口。证据位于 `runtime/security-review/notice-dialog.png`。 |
| 线上真实新邀请 | NOT TESTED | 未为验收创建垃圾账号；现有 1 次历史邀请已完成数据修正。 |
| 发布前备份 | PASS | `/opt/ai-gateway/backups/referral-notices-20260928`，数据库和变更前配置均有 SHA-256 清单，目录仅 root 可读。 |

本次迁移具备幂等保护：仅当 `QuotaForInviter=1333550` 时执行。迁移后线上值为 `1333551`，17 个有效用户均有唯一邀请码，New API、Nginx、PostgreSQL、Redis 与 auth-guard 全部健康。
