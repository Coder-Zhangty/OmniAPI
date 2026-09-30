# AI Gateway

基于 New API 原生渠道的多供应商 API 聚合平台。本项目维护部署、运维和验收，不修改 Gateway 核心。

`CURRENT_NEW_API_VERSION=v1.0.0-rc.40`

生产实例（中国香港云主机）：站点 `https://omniapi.biz/`，用户 API Base URL：`https://omniapi.biz/v1`。
本机部署（默认）：入口 `http://localhost/`，Base URL `http://localhost/v1`，仅监听 `127.0.0.1`。

Base URL 一律填到 `/v1` 为止。部分客户端要求只填域名（由它自行拼接 `/v1/chat/completions`），填错的表现是 `404 Invalid URL`。
生产实例已启用 TLS 1.2/1.3、HSTS 和自动续期；HTTP 与 `www` 统一跳转主域名 HTTPS。

可用端点：`/v1/chat/completions`、`/v1/completions`、`/v1/messages`（Claude 格式）、`/v1/responses`、`/v1/embeddings`、`/v1/images/generations`、`/v1/audio/*`、`/v1/models`。认证用 `Authorization: Bearer <令牌>`。

完整运行结果见 [部署报告](docs/DEPLOYMENT_REPORT.md)，不要把文件交付等同于真实上游验收通过。

## 快速开始

先安装并启动 Docker Desktop（WSL2 / Linux containers），确认 `docker info` 成功。需要 Python 3.10+；无需 pip 依赖。

在本目录运行：

```powershell
python scripts/gateway.py init
python scripts/gateway.py lock-images
python scripts/gateway.py start
python scripts/gateway.py bootstrap
python scripts/gateway.py healthcheck
```

Windows 也可使用 `./scripts/gateway.ps1 start`；Linux/WSL 可使用 `bash scripts/start.sh`。
脚本从自身位置定位项目，可以在含空格、中文的路径下使用。

管理员凭据保存在 `.secrets/admin.json`，请本机查看，勿提交或发送。测试用户凭据在 `.secrets/tester.json`，有限额测试 Key 在 `.secrets/test-token.json`。

登录后台，按 [Provider 配置](docs/PROVIDERS.md) 创建 `0-0 Main` 并亲自填写 Key。获取实际模型后，只启用一个待验收的文本模型，再运行：

```powershell
python scripts/gateway.py test-price --model "真实模型名称"
python scripts/acceptance.py --model "真实模型名称"
```

验收会产生少量真实上游费用。测试用户初始额度为 500000（默认换算为 1 美元的内部测试额度），测试 Token 上限为 50000；这不等于上游费用上限。上游控制台应设置可接受的消费限额。

## 账户充值

当前使用**人工核验充值**：`/brand/topup.html` 提供人民币聚合收款码和 Payoneer USD（最低 $20）。USDT / USDC 管理配置已经交付，只有网络、钱包地址和二维码完整时才会公开。申请会进入宿主机常驻服务；经营者在 `/brand/admin.html` 核对实际净到账后入账。提交申请、二维码跳转或用户截图本身不产生额度。

钱包页会显示充值入口，由 `TopUpLink` 指向充值页。页面提示到账与人工核验可能延迟约 5 分钟。Xe 多币种换算组件已实现；凭据未配置前只显示 USD 并拒绝新换汇报价。

## 架构

```text
Client → Nginx → New API → Channel A / Channel B / ...
                   ├── PostgreSQL
                   └── Redis
```

Provider 配置保存在 New API 数据库，不使用唯一上游环境变量。新供应商使用后台 Add Channel；同一公共模型可配置到多个渠道，按渠道映射到不同上游名称。已支持的协议不需要改源码。

## 运维入口

| 动作 | 命令 |
|---|---|
| 停止并保留卷 | `python scripts/gateway.py stop` |
| 重启 | `python scripts/gateway.py restart` |
| 健康检查 | `python scripts/gateway.py healthcheck` |
| 备份 | `python scripts/gateway.py backup` |
| 隔离恢复 | `python scripts/gateway.py restore --backup backups/时间戳 --port 18080` |
| 双模拟上游完整验收 | `python scripts/acceptance.py --mock --recovery` |
| 发布充值目录 | `python scripts/publish_topup.py` |
| 安装/更新充值申请服务 | `sudo sh ops/topup_watch/install.sh --apply`（不带 `--apply` 为预演） |
| 查看待核验充值申请 | `python scripts/topup_requests.py --pending` |
| 忽略一笔申请（不发额度） | `python scripts/topup_requests.py --dismiss <申请编号> --apply` |
| 核验到账并直接加额度（命令行方式） | `python scripts/credit_topup.py --user-id N --amount-cny 20 --transaction 单号 --apply` |
| 退回兑换码方式（应急） | `python scripts/credit_topup.py --user-id N --amount-cny 20 --transaction 单号 --via code --apply` |
| 查看充值台账与兑换状态 | `python scripts/credit_topup.py --list` |
| 充值报价与受理链路 | `python3 ops/topup_watch/probe_fx_quote_e2e.py`（在服务器上跑，不碰线上队列） |
| 管理端入账路由与会话自愈 | `python3 ops/topup_watch/probe_admin_selfheal_e2e.py`（起临时实例，不发放任何额度） |
| 运营商侧入账 | `python3 ops/topup_watch/probe_credit_e2e.py`（需经营者浏览器先建立会话） |
| 本地静态/脚本测试 | `python -m unittest discover -s tests -p "test_*.py" -v` |
| 充值页前端逻辑 | `node tests/topup-page-check.js`（需一次性 `npm install jsdom`）——在 jsdom 里真实执行 `topup.js`，核对自动识别账号、自定义金额边界、额度换算与提交行为 |
| 客服页与客服台逻辑 | `node tests/support-page-check.js`——在 jsdom 里真实执行 `support.js` 与 `support-admin.js`，核对身份、渲染、署名、列表预览、SSE 推送去重与 CSRF |
| 客服页布局（输入框能不能打字） | `python -m unittest tests.test_support_layout`——**用无头浏览器量真实像素**；jsdom 不做布局，这个洞只能这样测（本机无 Chrome/Edge 时跳过） |
| 登录页增强与验证组件 | `node tests/auth-page-check.js`——钉住「页面上只有一个 Turnstile 组件」、不改写请求 URL、令牌用后重置 |
| 浮动气泡的拖动行为 | `node tests/floating-check.js`——阈值、拖后吞掉点击、位置记忆、夹在可视区、表单控件不参与拖动 |
| 管理页前端逻辑 | `node tests/admin-page-check.js`——在 jsdom 里真实执行 `admin.js`，核对「网关管理员会话丢失后页面自己补种」与「入账途中会话失效则补种并重试一次」 |
| 新版支付/价格验收 | `python scripts/verify_payment_upgrade.py` |
| 在线客服服务自检 | `python3 ops/support/support_service.py --check` |
| 在线客服公网验收 | `python3 ops/support/probe_support_e2e.py`（只读，产出 `runtime/support-e2e.json`） |
| 注销是否真的断掉两侧管理会话 | `python3 ops/support/probe_logout_e2e.py`（两个隔离临时实例共用一个**临时**代际文件；证明「一侧注销 → 两侧同时失效」，不碰线上会话与队列） |
| 经营者能不能读到队列与消息 | `python3 ops/support/probe_workbench_read_e2e.py`（只读；用充值口令换共用会话 cookie，末尾自证无写权限） |
| 安装/更新在线客服 | `sudo sh ops/support/install.sh --apply`（不带 `--apply` 为预演） |
| 清理过期客服记录 | `python3 ops/support/support_service.py --prune 180 --apply`（默认预演） |
| 用量图表全空时回填历史聚合 | `python3 scripts/backfill_quota_data.py`（默认只预演；`--apply` 才写库）。原因与判断见 [故障排查](docs/TROUBLESHOOTING.md)「用量图表全是空的」 |

镜像不可拉取时停止，禁止自动替换为 latest。`images.lock.json` 记录摘要，`.env` 使用不可变摘要引用。`lock-images` 重复运行保留已有锁；更新流程见 [UPGRADE](docs/UPGRADE.md)。

## 文档

- [部署](docs/DEPLOY.md)、[Provider](docs/PROVIDERS.md)、[模型与价格](docs/MODELS.md)
- [备份恢复](docs/BACKUP.md)、[升级回滚](docs/UPGRADE.md)、[故障排查](docs/TROUBLESHOOTING.md)
- [官方源码核查](docs/UPSTREAM_RESEARCH.md)、[验收报告](docs/DEPLOYMENT_REPORT.md)、[充值与汇率报告](docs/PAYMENT_FX_REPORT.md)
- [在线客服](docs/SUPPORT.md)（运维与排障）、[客服实施方案](docs/SUPPORT_CHAT_PLAN.md)

`.env`、`.secrets/`、`backups/`、证书私钥、研究下载和运行输出均被 Git 忽略。数据库及备份包含渠道凭据，不能视为非敏感文件。普通用户使用邮箱和密码登录/注册，并由 Cloudflare Turnstile 保护；新注册账号赠送人民币 5 元试用额度（按固定赠送汇率 ¥6.7489/US$ 与 500,000 额度/US$ 折算为 370,430 额度单位）。

每位用户在钱包中拥有唯一邀请链接和邀请码。注册页可选填邀请码；成功邀请一名新用户后，邀请人获得人民币 18 元等值邀请额度（固定折算为 1,333,550 额度单位），可在钱包转入主余额。被邀请人仍领取人民币 5 元新用户试用额度，不额外叠加邀请奖励。
