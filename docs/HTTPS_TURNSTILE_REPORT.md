# OmniAPI HTTPS、Turnstile 与安全升级报告

执行日期：2026-09-26（Asia/Shanghai）

| 项目 | 状态 | 证据 |
|---|---|---|
| 升级前备份 | PASS | `backups/2026-09-26_10-06-32`，23 个文件校验通过 |
| 隔离升级 | PASS | 独立项目恢复 v0.13.2 数据并迁移到 v1.0.0-rc.40 |
| New API 镜像 | PASS | `calciumion/new-api@sha256:37c0f99a76ed0a5d5b376b8486173415efd1ec416042b392bd564eb0ae6a55ef` |
| 主域名 HTTPS | PASS | `https://omniapi.biz`，Let’s Encrypt，证书到期 2026-12-25 |
| HTTP 与裸 IP 跳转 | PASS | 80 端口返回 308，目标为 `https://omniapi.biz` |
| HSTS | PASS | `Strict-Transport-Security: max-age=31536000` |
| 认证缓存保护 | PASS | 登录接口返回 `Cache-Control: no-store` |
| ServerAddress | PASS | `https://omniapi.biz` |
| Passkey RP/Origin | PASS | RP ID `omniapi.biz`，Origin `https://omniapi.biz` |
| 自动续期 | PASS | Certbot dry-run 成功；`omniapi-cert-renew.timer` active |
| 线上真实调用 | PASS | Chat、Streaming、Responses、用量、渠道和普通/流式扣费通过 |
| 鉴权 | PASS | 错误、禁用、删除 Token 均被拒绝 |
| 端口隔离 | PASS | 仅 22、80、443 对外；PostgreSQL、Redis、New API 未发布 |
| 升级后备份 | PASS | `backups/2026-09-26_10-34-05`，包含证书、配置、密钥和数据 |
| 升级后恢复演练 | PASS | 新项目、新卷、127.0.0.1:18082；管理员、用户、渠道、Token、调用、计费和日志通过 |
| 服务器 root 密码轮换 | PASS | **Linux 的 root**（不是控制台管理员）。新密码未输出，保存于服务器 `.secrets/root-console-password.txt`（0600）；SSH 密码登录关闭，所以它只在本机/控制台 `su` 时有用 |
| `www.omniapi.biz` | BLOCKED | 1.1.1.1 与权威公开 DNS 尚无 A/AAAA 记录，无法加入证书 |
| Cloudflare Turnstile | PASS | 登录/注册页显示 Managed Widget；公开状态仅返回 Site Key；无令牌请求在账户校验前被拒绝 |
| 邮箱登录 | PASS | 页面与容器内 `auth-guard` 均只接受邮箱；旧用户名直接调用 API 也会被拒绝；管理员账号已迁移为邮箱格式 |
| OAuth 快捷登录 | BLOCKED | 页面已预留 Google/GitHub/Apple 入口；缺少各平台 OAuth 应用的 Client ID/Secret，保持禁用避免伪入口 |
| 模型价格页 | PASS | `/pricing` 展示 29 个可用模型，输入/输出 USD/M Tokens 与实际计费表达式一致，固定两位小数 |

**两个「root」是两回事，别混：**

| | 是谁 | 凭据在哪 | 什么时候用 |
|---|---|---|---|
| **控制台超级管理员** | New API 账号 `ttyy.zhang@gmail.com`（`display_name = Root User`，`role = 100`），登录用**邮箱** | 服务器与本机 `.secrets/admin.json` 的 `username` / `password` | 登录 `https://omniapi.biz/console`，管模型、渠道、用户、系统设置 |
| **服务器 Linux root** | 操作系统的 root | 服务器 `.secrets/root-console-password.txt` | 只在**本机/控制台** `su` 时有意义；SSH 密码登录已关闭，远端只能靠密钥 |

两者的口令**完全不同**，用错会以为「密码被改了」。核对办法（只读、不打印口令）：
控制台口令可用 `bcrypt.checkpw(候选, users.password)` 验；Linux root 口令可用
`perl -e 'print crypt($ENV{CAND}, $ENV{HASH})'` 与 `/etc/shadow` 对照。

线上验收证据位于服务器：

- `runtime/real-acceptance.json`
- `runtime/upgrade-rc40-acceptance.json`
- `runtime/restore-rc40-result.json`
- `runtime/restore-rc40-acceptance.json`

Turnstile 状态：`turnstile_check=true`；公开状态不含 Secret；缺失令牌已实测拒绝。页面在认证请求结束后重置组件，避免复用单次令牌。有效、过期和重复令牌的完整浏览器验收仍需人工完成一次真实挑战，自动化不能代替用户完成人机验证。
