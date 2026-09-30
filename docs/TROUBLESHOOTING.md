# 故障排查

| 现象 | 检查与处理 |
|---|---|
| 找不到 docker | 安装 Docker Desktop，重开终端；脚本也检查系统/用户安装目录 |
| CLI 有版本但无法连接 | 启动 Docker Desktop，确认 Linux engine、WSL2；`docker info` 必须成功 |
| WSL/CIM 拒绝访问 | 当前 shell 可能受限；在本人有权限的终端检查，不能据此判定机器不支持虚拟化 |
| 固定镜像拉取失败 | 检查 Docker Hub 网络/鉴权/限流；保留固定版本，不回退 latest |
| 80 端口占用 | 查看占用进程，不自动杀其他服务；修改 `.env` 的 GATEWAY_PORT 并明确记录入口变化 |
| New API unhealthy | 查看数据库、Redis 健康；本机读取脱敏日志；检查密码、DSN、镜像架构 |
| 502 | 检查 New API 健康与容器重建后的 DNS；运行 restart 使 Nginx 重新解析服务地址 |
| 模型目录为空 | 检查 Token 分组、渠道状态、渠道模型列表；后台上游目录不等于用户可用目录 |
| 模型不存在/404 | 核对真实模型 ID、渠道映射和 Base URL；避免 `/v1/v1` |
| 流式最后一次出现 | 排除客户端缓冲；用验收脚本记录逐块时间，检查所有前置代理 buffering/cache/compression |
| 流式计费失败 | 检查最终 usage 是否包含输入输出；缺失不能宣称与上游精确一致 |
| 计费偏差 | 检查分组、ModelRatio、CompletionRatio、ModelPrice、缓存及特殊 token，不能直接修改余额掩盖问题 |
| 管理员覆盖额度后首次请求仍报余额不足 | v0.13.2 override 不立即清理用户缓存；初始化用原生启用用户操作刷新测试账户缓存；后续充值优先使用原生增加额度操作 |
| docker logs new-api 不可用 | 默认 logging driver none 防止 GORM 错误 SQL 泄露 Token；使用 Nginx、数据库用量日志及健康检查 |
| 重试不切换 | 检查 RetryTimes、状态码范围、同模型同分组、优先级、affinity、显式渠道及 skip-retry |
| 控制台报 `Request failed with status code 429` | 这是 New API 的**关键接口限流**（`CriticalRateLimit`，与登录**共用同一个按 IP 的桶**，固定窗口）。页面每加载一次就会调一次 `/api/user/auth/refresh`，刷新十几次就把桶打满；之后**连登录都会被拒**——**不是密码错、也不是服务坏了**。看响应头的 `Retry-After` 等窗口过去（固定窗口，重试不会延长它）；想根治就调宽 `docker-compose.yml` 里 new-api 的 `CRITICAL_RATE_LIMIT` / `CRITICAL_RATE_LIMIT_DURATION`（默认 20 次 / 20 分钟，本仓库已设为 **120 / 1200**，登录仍有 Cloudflare Turnstile 挡着） |
| 从控制台退出登录后，充值核验 / 客服工作台**仍然能进** | 控制台的登出只撤销控制台自己的会话；管钱的那张 cookie 有 8 小时 TTL。浏览器侧已在登出时联动注销（`auth-enhance.js` → `POST /brand/admin/api/logout`），两个服务共用一份代际计数文件，一次 bump 两边同时失效。若仍能进：跑 `python3 ops/support/probe_logout_e2e.py`（隔离实例，17 项判定），并检查有没有人把代际号缓存进内存 —— 它必须每次现读 |
| 控制台的气泡挡内容 / 拖不动 | 气泡位置存在浏览器本地（`omniapi.float.<键名>`）。控制台里执行 `OmniFloat.forget('<键名>')` 再刷新即可回默认角落；若所有气泡都拖不动，确认 `/brand/floating.js` 能加载（它必须在 `admin-badge.js` / `support-widget.js` **之前**注入，见 `nginx/https.conf` 的 `sub_filter`） |
| 恢复失败 | 校验 manifest、镜像摘要、数据库版本、独立端口；保留原卷和失败现场 |
| **用量/统计图表全是空的**（管理员端与用户端同时空，但钱包数字、日志列表都有数据） | 看 **`DataExportEnabled`**：它为 false 时 `quota_data` 永远为空，而 `/api/data`（管理员）与 `/api/data/self`（用户）都只从这张表取数。**不要去查 nginx 或前端**——接口是好的，只是返回空数组。 |

### 用量图表全是空的

看板的所有图表（管理员端 `/api/data`、`/api/data/flow`，用户端 `/api/data/self`、`/api/data/flow/self`）都读 `model/usedata.go` 里的 `quota_data` 聚合表，**不是**直接读 `logs`。写入链路是：`RecordConsumeLog` → 若 `common.DataExportEnabled` 才 `LogQuotaData()` 进内存缓存 → `UpdateQuotaData()` 每 `DataExportInterval` 分钟执行 `SaveQuotaDataCache()` 落库。开关一关，第一步就断了，库里 259 条消费日志在，图表照样全空。

只读诊断（不写任何东西）：

```bash
cd /opt/ai-gateway
docker exec ai-gateway-database-1 psql -U gateway -d gateway -c \
  "select key,value from options where key in ('DataExportEnabled','DataExportInterval','DataExportDefaultTime');"
docker exec ai-gateway-database-1 psql -U gateway -d gateway -c "select count(*) from quota_data;"
docker exec ai-gateway-database-1 psql -U gateway -d gateway -c "select type,count(*) from logs group by 1 order by 1;"
```

判断：`DataExportEnabled=false` + `quota_data` 为 0 行 + `logs` 有 `type=2` 的行 = 就是这个原因。`UpdateQuotaData()` 在 `main.go` 无条件启动，改开关**不需要重启容器**，最多 `DataExportInterval` 分钟后开始出数据。

处理：

1. 控制台 → **系统设置 → 数据仪表板 → 「启用数据仪表板」**（key `DataExportEnabled`）打开并保存。这个开关只能由经营者从浏览器改（`PUT /api/option/` 要管理员令牌，而登录受 Turnstile 保护，脚本进不去）。
2. 关闭期间的**历史用量不会自动出现在图表里**——`LogQuotaData()` 当时根本没被调用，那段时间的聚合值从未存在过。`logs` 里还有原始消费行，需要的话用 `scripts/backfill_quota_data.py`（默认只预演，要 `--apply` 才写）按 New API 自己的口径把 `logs` 回填进 `quota_data`。
3. 图表按小时分桶（`created_at` 向下取整到 3600 秒），刚打开开关后头几分钟看到的可能还是 0，等一个刷新间隔再看。

日志排查仅在本机进行。不要共享 `.env`、备份、Token 文件、完整 inspect/config 输出或未脱敏上游报错。
`healthcheck` 输出 `runtime/health.json`，真实与模拟验收分别输出 `runtime/real-acceptance.json` 和 `runtime/mock-acceptance.json`。
机器休眠、Docker Desktop 退出和 Windows 重启会中断服务；本机 MVP 不具备服务器可用性保证。
