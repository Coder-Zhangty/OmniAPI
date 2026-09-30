# 部署

## 本机依赖

Windows 11、已启用虚拟化的 WSL2、Docker Desktop Linux 引擎、Python 3.10+。Docker Compose 使用 V2。
官方安装说明：https://docs.docker.com/desktop/setup/install/windows-install/

先执行 `docker version`、`docker info`、`docker compose version`。Docker Desktop 未启动时不能把 CLI 安装成功当作引擎可用。
当前目录为空起步；安装 Docker 的系统修改发生在项目目录外。若安装请求重启，先保存其他工作并重启，再从 `lock-images` 继续。

## 初始化

按 README 运行 init → lock-images → start → bootstrap。

init 生成 256-bit 十六进制数据库、Redis、Session、Crypto 随机秘密，账户密码使用 20 字符、120-bit 随机值，符合固定版本用户接口的长度上限。十六进制避免 DSN 转义歧义。
Windows 用 ACL、Linux 用 0600/0700 限制读取。管理员从文件读取凭据，不在命令行传递密码。不要执行或分享未脱敏的 `docker compose config` / `docker inspect`，其中可能包含运行环境秘密。

bootstrap 使用固定版本官方 `/api/setup`、Session 登录、`New-Api-User` 头及后台管理接口。
遇到已存在但未完成初始化的管理员时停止，不覆盖其他人的凭据。重复执行已完成的 bootstrap 不重置额度、密码或选项。
首次初始化将测试用户余额设为 500000，创建额度 50000 的非无限 Token，开放普通用户账号密码注册，关闭默认赠额（`QuotaForNewUser`）。**`DataExportEnabled` 必须开启**：它是用量图表的数据源，关掉只会让管理员端与用户端的统计全空，且它不写任何请求正文（详解见 `MODELS.md`「用量日志与隐私」）。

## 服务与网络

| 服务 | 容器端口 | 默认宿主机端口 | 持久化 |
|---|---|---|---|
| nginx | 80 | 127.0.0.1:80 | 配置只读挂载 |
| new-api | 3000 | 无 | new-api-data |
| database | 5432 | 无 | database-data |
| redis | 6379 | 无 | redis-data |

桥接网络不设置 `internal: true`，因为应用需要访问公网 Provider。数据库和 Redis 的安全边界是不发布端口及强密码。
Nginx、数据库和 Redis 控制台日志最大 3×10 MiB/服务；New API 原始控制台日志不落盘，原因见 MODELS.md。MVP 禁用批量额度写入。该版本启用 Redis 时强制启用渠道内存缓存，配置明确设为 true，并将同步周期设为 10 秒；不能通过 MEMORY_CACHE_ENABLED=false 绕过。新建渠道后等待同步或使用原生“修复渠道能力”操作立即刷新。

## 公网部署

本机默认 `GATEWAY_HOST=127.0.0.1`，只有回环可访问。部署到公网服务器时：

1. 在服务器 `.env` 设 `GATEWAY_HOST=0.0.0.0`，让 Nginx 发布到服务器网卡；`new-api`、`database`、`redis` 仍然不发布端口，边界不变。
2. 云控制台安全组只放行 22、80、443，其余一律关闭。
3. 有域名后配置 HTTPS（见下），**不要长期以裸 HTTP 公网运行**：管理员密码与 API Key 在明文信道中可被截获。
4. 验收脚本的 `port_isolation` 会把 `exposure` 记为 `public`，这是预期结果，不是失败；它仍然强制「只有 Nginx 发布端口，其余三个不发布」。

## 当前实例

| 项目 | 值 |
|---|---|
| 主机 | 灵梦云 香港 CN2 特惠云主机，1 核 2G / 系统盘 30G / 10M 峰值 |
| 地址 | `https://omniapi.biz/`（HTTP 与裸 IP 入口 308 跳转主域名）；SSH 22 |
| 项目路径 | `/opt/ai-gateway`（`/root/migrate.sh`、`/root/post_migrate.sh` 为本次迁移脚本） |
| 运行状态 | 四容器 running/healthy；nginx 监听 `0.0.0.0:80/443`；New API v1.0.0-rc.40 |
| 主机防线 | ufw 仅放行 22/80/443；docker 与 ssh 开机自启；unattended-upgrades 已启用，并加了 `CPUQuota=40%` / `Nice=19` / `IOSchedulingClass=idle`（见下方故障记录） |
| 验收 | `python3 scripts/acceptance.py --model gpt-5.4-mini` → 19 PASS / 0 FAIL / 2 NOT TESTED（persistence、restore 需 `--recovery`） |
| 计费 | 29 个模型已切换到商业单价（成本 × 2）；三档套餐已启用，用户端可见。回滚快照：`runtime/commercial-before-apply.json` |
| SSH | 已关闭密码登录（`/etc/ssh/sshd_config.d/50-pubkey.conf`），仅接受密钥。部署密钥在经营者本机 `~/.ssh/wb_gateway_deploy`；密钥丢失时用云厂商面板控制台进入 |
| 备份 | `python3 scripts/gateway.py backup`，最新还原点见 `backups/` |

> 在服务器上直接跑运维脚本时注意：脚本连 `127.0.0.1`，与浏览器**共用同一个限流桶**（`CriticalRateLimit` = 20 次 / 20 分钟 / IP）。连续执行多个脚本会触发 429，验收会以 `runner: FAIL / RuntimeError` 失败。等待约 20 分钟即可，不是环境故障。

迁移方式是「恢复 `backups/2026-09-26_10-31-32/` 快照 + 重放之后的三项增量」（三档套餐、`TopUpLink`/`USDExchangeRate`/`Price`、两个品牌目录）。`.env` 与 `.secrets/` 必须沿用原值，尤其 **`CRYPTO_SECRET` 不能更换**，否则渠道 API Key 无法解密。

## 故障记录：relay 全部 503（性能保护误触发，2026-09-26 已修）

**现象**：`GET /v1/models` 正常（无令牌 401、有令牌 200），但 `POST /v1/chat/completions` 恒返回 503；换成有效令牌也一样。

**定位过程**

1. nginx 访问日志给出关键一行：`"path":"/v1/chat/completions","status":503,"upstream_status":503,"duration":0.003` —— 3 毫秒、上游同为 503，说明是应用层立刻拒绝，不是网络或上游问题。
2. 不带令牌也能复现 503，说明拒绝发生在鉴权**之前**。
3. 对照 `router/relay-router.go`：`/v1/models` 只挂 `RouteTag` + `TokenAuth`；`/v1/chat/completions` 还多挂了 `SystemPerformanceCheck()`，且位于 `TokenAuth()` 之前。这正好解释了「列表正常、调用失败」。
4. 响应体给出确切读数：`system cpu overloaded (current: 99.4%, threshold: 90%)`。
5. `ps` 显示 `unattended-upgrade`（53.9% CPU）与 `dpkg`（正在安装 vim）占满这台 **1 核**机器。

**根因**：部署时启用的自动安全更新在 1 核机器上跑 apt，把 CPU 顶到约 99%，越过 `performance_setting.monitor_cpu_threshold = 90`，于是所有 relay 请求被拒。这是间歇性的——只在自动更新运行时出现，所以刚迁移后的验收（当时负载低）能通过。

**修复**

- `performance_setting.monitor_cpu_threshold`：90 → **100**（内存 90 / 磁盘 95 的保护保留）。1 核共享 VPS 的 CPU 读数受后台任务与宿主机噪声影响很大，不适合作为拒绝依据；而网关只做转发，几乎不吃 CPU。
- 给 `apt-daily.service` 与 `unattended-upgrades.service` 加 drop-in（`/etc/systemd/system/*.d/cpu-limit.conf`）：`CPUQuota=40%`、`Nice=19`、`IOSchedulingClass=idle`。
- 快照：`runtime/performance-before-fix.json`。

**验证**：无令牌 POST 恢复 401，有效令牌返回 200 `AI_GATEWAY_OK`；`acceptance.py` **19/19 PASS**（含 `billing`、`stream_billing`）。

**遗留**：这台机器只有 1 核，自动更新期间仍会短暂吃满 CPU。长远建议换 2 核，或把自动更新改成手动执行。**排查同类故障的入口**：直接 `curl -X POST http://127.0.0.1/v1/chat/completions -d '{}'`，响应体会写明是 CPU、内存还是磁盘超阈值。

## HTTPS（已上线）

`omniapi.biz` 已启用 HTTPS，证书由 certbot 签发（Let's Encrypt），自动续签挂在 systemd timer 上。

| 项 | 值 |
|---|---|
| 证书 | `/etc/letsencrypt/live/omniapi.biz/`（挂载进 nginx 容器） |
| 80 端口 | 308 跳转到 `https://omniapi.biz`，保留 `/.well-known/acme-challenge/` 供续签 |
| www 子域 | 308 跳转到主域 |
| 其他 | HSTS `max-age=31536000`；TLSv1.2/1.3；`server_tokens off` |
| 续签 | `systemctl list-timers omniapi-cert-renew.timer`（当前 90 天有效期，到期 2026-12-25） |

`compose.https.yml` 与 `certs/` 模板保留，用于换机或换域名时重建。

**注意**：`https.conf` 已把有缺陷的套餐易支付回调用 nginx 拦成 404（`/api/subscription/epay/notify` 与 `/return`）。原因是原生套餐实收金额只按 `price_amount` 收取、未乘汇率，在 CNY 显示模式下会少收约 86%（详见 `docs/SUBSCRIPTIONS_PAYMENTS.md`）。钱包充值回调 `/api/user/epay/notify` 不受影响。**解除这个拦截前必须先解决金额不一致问题。**

## 充值申请服务（宿主机 systemd）

用户点「我已付款」的告知链路不放进容器，而是一个宿主机上的非特权常驻进程：它要发放兑换码，需要读管理员会话，但不需要 Docker 权限，也不该和业务容器共享生命周期。

| 项 | 值 |
|---|---|
| 单元 | `/etc/systemd/system/omniapi-topup-watch.service`（源在 `ops/systemd/`） |
| 安装 | `sh ops/topup_watch/install.sh --apply`（不带参数只预演） |
| 运行账号 | `omniapi-watch`（systemd 系统账号，无登录 shell） |
| 监听 | `0.0.0.0:8787`，仅容器网段可达 |
| 反向代理 | nginx → `host.docker.internal:8787`，依赖 compose 里的 `extra_hosts: host-gateway` |
| 可写 | 仅 `/opt/ai-gateway/runtime`（`ProtectSystem=strict` + `ReadWritePaths`） |
| 可读 | `scripts/`、`config/`、`.secrets/` |
| 日志 | `journalctl -u omniapi-topup-watch -n 50` |
| 自检 | `python3 ops/topup_watch/topup_watch.py --check` |

**汇率快照预热定时器**：另有一个 oneshot 单元 `omniapi-fx-refresh.service` + `.timer`（源在 `ops/systemd/`，由同一个 `install.sh` 安装），每 5 分钟运行 `topup_watch.py --refresh-fx`，与 `config/topup.json` 的 `fx.refresh_seconds` 对齐。没有它的话快照只在有人访问页面时才刷新：首个访客要等上游响应，并发下单还可能把第三方端点打爆。单元带 `CPUQuota=30%`、`Nice=10`，避免在 1 vCPU 机器上与结账请求争抢。

```sh
systemctl list-timers omniapi-fx-refresh.timer   # 下次触发时间
systemctl status omniapi-fx-refresh.service      # 上次抓取结果
python3 ops/topup_watch/topup_watch.py --refresh-fx   # 手动抓一次
```

抓取失败不会让定时器停下，也不会让结账直接报错：服务继续使用上一份快照，并在 `/brand/api/fx/rates` 的 `refresh_error` 里报告原因。

**权限**：服务要读 `.secrets/admin.json`（发码）并写 `runtime/`，所以安装脚本把 `.secrets` 目录设为 `750` 且属组 `omniapi`、`admin.json` 设为 `640`、`runtime/` 设为 `2775`（setgid，保证经营者脚本与服务写出的文件同组可读写）。root 私有的 `.env` **没有**放开——服务从单元里的 `TOPUP_WATCH_GATEWAY_URL` 直接拿到公网地址，全程不解析 `.env`。

**防火墙顺序（踩过的坑）**：容器回连宿主机走的是 INPUT 链，所以一条笼统的 `ufw deny 8787/tcp` 会把 nginx 容器一起挡住，表现为页面 502。正确顺序是先放行 docker 网段、再拒绝其余来源：

```sh
ufw allow from 172.16.0.0/12 to any port 8787 proto tcp comment "docker bridges only"
ufw deny 8787/tcp comment "topup-watch stays private"
```

`install.sh` 已按此顺序写入，重装不会退回错误状态。

**管理员口令**：安装时生成一次，只有 PBKDF2 哈希写进 `.secrets/topup-admin.json`（`640`，属组 `omniapi`），另存一份 root 私有副本 `.secrets/topup-admin-password.txt` 供经营者取用，取走后应删除。哈希不可反推，忘记只能重设（备份并删除 secrets 文件后重跑安装）。

**身份模型**：经营者平时不需要这个口令。管理页与控制台角标都在控制台同源下运行，它们用 `/api/user/auth/refresh` 拿到短时 access_token，再调 `POST /brand/admin/api/session`；服务拿这个令牌去请求网关的 `/api/user/self`，`role >= 10` 且账号启用才发 30 天会话 cookie。也就是说**角色判定由网关自己做，服务不解析、不缓存令牌**。口令登录是未登录控制台时的兜底。会话 cookie 里带 scope（`pw` 或 `na:<用户ID>`），发码/忽略会把它记进事件日志的 `by` 字段。

**入账会话来自经营者，不是密码登录（Turnstile 之后的重要变化）**：网关的 `POST /api/user/login` 受 Cloudflare Turnstile 保护（`turnstile_check=true`），**脚本无法代替人机验证**。因此服务的 `api()` 不再尝试密码登录，而是复用经营者浏览器换来的那个 access_token（`newapi_admin`）作为网关会话，并用它执行 `POST /api/user/manage`。

含义是：

- 判定一个请求是不是管理员，仍然由网关负责，服务只是借用了经营者已经证明过的身份。
- 服务进程重启会丢掉内存里的网关会话，但经营者那张 30 天 cookie 仍然有效：`GET /brand/admin/api/session` 照样返回 `authenticated: true`，面板照常打开。**只靠 cookie 判断「能不能入账」是错的**，所以同一个接口另外返回 `gateway_session`（取自 `Service.has_api()`），`summary` 也带上它，让它搭在页面已有的 30 秒轮询上。
- 页面看到 `gateway_session: false` 会**自己补种一次**（`ensureGatewaySession()`），不需要经营者做任何动作；补种失败才显示可见提示与「重新确认身份」按钮。补种走的是登录限流桶（8 次 / 15 分钟 / IP），所以自动补种失败后不再重试，避免把配额打满。
- 入账时若会话在途中失效（服务重启或 access_token 过期），服务端返回 **409 + `code: gateway_session_missing`**，页面补种后**重试一次**，经营者无感。这个状态刻意与「令牌被拒」区分开：没有会话时重试是纯浪费，`forget_api()` 更是有害的——根本没有东西可忘。

**Token 过期与降级**：access_token 是短时的；过期后网关返回未授权，服务会 `forget_api()` 并**重试一次**（届时页面已带来新令牌）。台账仍然保证同一笔不会重复入账。`SessionMissing` 不走这条路：它直接抛出，不重试、不 `forget_api()`。

## 静态资源缓存策略

`/brand/` 下由 `config/*.json` 生成的内容，改动后必须立刻对访客生效——否则访客会看到过期的客服联系方式，在支付页面上直接导致联系错人。因此分两类：

| 路径 | 缓存 | 理由 |
|---|---|---|
| `/brand/*.html`、`*.json`、`*.css`、`*.js` | `no-cache` | 配置驱动的内容。`no-cache` 仍走 ETag/Last-Modified 校验，未改动时返回 304，不重传内容 |
| `/brand/` 其余（收款码图片、图标） | `public, max-age=86400` | 体积大、改动少 |

改这些文件后**不需要** reload nginx，静态文件每次请求都从磁盘读取。

Docker Desktop 对某些商业用途有订阅要求，采用 Docker 官方条款。
