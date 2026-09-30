# OmniAPI 登录与第三方认证

公开登录和注册页只接受邮箱格式，邮箱直接作为 New API 的唯一用户名保存。当前阶段不发送邮箱验证码；启用 SMTP 后再开启邮箱验证。

Cloudflare Turnstile 使用 New API 原生服务端校验。Site Key 可以公开，Secret Key 只存放在服务器 `.secrets/turnstile.json`（0600）。密钥曾出现在截图中，因此上线验收后应在 Cloudflare 控制台轮换 Secret，再运行 `python3 scripts/configure_turnstile.py --file <root-only-file>`。

**对自动化的影响（务必记住）**：`turnstile_check=true` 之后，`POST /api/user/login` 需要人机验证，任何脚本都无法替代。三条后果：

1. 一切依赖 `API.login()` 的脚本（包括 `credit_topup.py` 的命令行发放、各类探针）不能再自行登录；错误信息会直接说明这一点（`gateway.py` 会把网关返回的 `Turnstile token 为空` 透传出来）。
2. 充值申请服务的入账**复用经营者浏览器换来的 access_token**，不自行登录。服务重启后、经营者打开管理页之前，入账会明确报错而不是静默失败。详见 `docs/DEPLOY.md` 的「身份模型」。
3. 需要管理员权限的一次性操作，改为在浏览器里完成后用控制台身份驱动，或请经营者配合。

## 登录页上只有一个 Turnstile 组件

服务端是从 **query 参数** `turnstile` 读令牌的（`middleware/turnstile-check.go` 的
`c.Query("turnstile")`），New API 自己的前端也是这么发的（`features/auth/api.ts`）。

因此 `nginx/brand/auth-enhance.js` **只做两件事**：改文案（邮箱、说明、快捷登录占位）与在
登录/注册请求结束后 `turnstile.reset()`（令牌单次有效）。它**不再自己渲染验证组件、也不改写请求 URL**
——以前那版为了「保证可见」额外渲染了一个，结果登录页出现**两个验证框**（线上真出过），
而且它塞进 URL 的令牌还会和 SPA 自己的打架。回归测试：`node tests/auth-page-check.js`。

第三方登录按钮只有在提供商配置完成后才可点击：

- GitHub：创建 OAuth App，回调地址 `https://omniapi.biz/api/oauth/github`，再填写 Client ID/Secret。
- Google：通过 New API 的 OIDC 配置接入，回调地址 `https://omniapi.biz/api/oauth/oidc`，发现地址使用 Google 官方 OpenID Configuration。
- Apple：需要 Services ID、私钥以及定期签发的 client secret JWT。当前 New API 只有一个通用 OIDC 槽位，和 Google 同时启用需要增加认证代理或维护定制实现。

页面在未配置 OAuth 凭据时显示“即将开放”，不会提供无效登录入口，也不会在仓库保存 Client Secret。
