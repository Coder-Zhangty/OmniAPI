# 在线客服（已上线）

方案与取舍见 [SUPPORT_CHAT_PLAN](SUPPORT_CHAT_PLAN.md)；本文只写**怎么用、怎么查、怎么改**。

## 它是什么

登录用户在 `/brand/support.html` 与客服实时对话；经营者在 `/brand/support-admin.html`
（手机浏览器也能用）接听。

**经营者从哪里进来**（三个都行，都要求同一个浏览器里已登录控制台管理员）：

1. 控制台左下角的**「客服工作台」按钮**——有待人工时它直接写「客服工作台 · N 条待人工」，
   没会话时写「N 条未读」。这是主入口。
2. 直接打开 `https://omniapi.biz/brand/support-admin.html`。若控制台登录态已失效，页面会显示
   「用控制台管理员身份进入」按钮，跟着点即可。
3. 手机：收到推送后点深链（形如 `…/support-admin.html#S-20260927-abc123`），直接落到那个会话。

**工作台怎么读**：左边是会话列表，每行三段——顾客名字（待人工会标出来）、
**最后一条是谁说的和说了什么**（例如「客户：充值没到账」，还没有消息时写「（还没有消息）」）、
会话编号与时间。点一行，右边打开完整对话；**每条消息顶部都写清是谁说的**
（`客户 · 名字` / `我（客服）` / `AI 助手（可能出错）`），不是只靠左右对齐分。

控制台左下角另有「在线客服」按钮，那是**用户视角**的对话页，经营者自己也会有一条会话。

### 退出登录会同时注销「充值核验」和「客服工作台」

**控制台自己的登出（`POST /api/user/auth/logout`）只撤销控制台那个会话**，它并不知道
管钱的那张 `omniapi_admin` cookie 存在 —— 那张 cookie 有 8 小时 TTL。结果是：从控制台
退出后，充值核验照旧能进、照旧能真的加额度。共用电脑上下一个人点一下就能给用户加额度。
（线上真发生过，经营者看到的是「我连管理员都没登上去我就可以看充值核验平台」。）

现在浏览器侧补上了联动：`auth-enhance.js` 拦到控制台的登出请求后，会拿当前会话去调
`POST /brand/admin/api/logout`，**两个服务读同一个代际计数文件，所以一次 bump 就把两边
一起断掉**，不需要等 TTL。

机制与不变量（改代码时别破坏）：

- 唯一的写入者是 `operator_session.bump_generation()`；两个服务都不许自己「读+1+写」。
- **代际号必须每次现读，不能缓存在内存里**。充值服务原先在启动时读进内存，于是从客服侧
  注销（或轮换口令）之后，充值侧照样认那张已注销的 cookie —— 注销只断了一边。
  `Service.generation()` 现在每次都读文件；测试 `tests/test_topup_requests.py` 的
  `SessionCounterIsReadFresh` 钉住这一点。
- `GET …/session` 的 `csrf_token` **只有在会话有效时才有值**。它是拿「你递过来的 cookie
  值」算出来的，对一张已作废的 cookie 也会算出结果；调用方（包括前端）必须以
  `authenticated` 为判据，不能以「有没有令牌」为判据。
- 注销是写操作：要正确的 Origin 和 CSRF 令牌，缺一个都是 403（防被第三方页面强制登出）。

验证：`python3 ops/support/probe_logout_e2e.py` —— 起一个充值临时实例和一个客服临时实例，
**共用一份临时代际文件**，然后证明「一张 cookie 开两边 → 在客服侧注销 → 两边同时失效、
钱那条路 401、缺 CSRF 被拒、线上代际与队列都没被动过」。17 项判定。

### 登录/注册页上不出现任何气泡

`auth-enhance.js` 判断「这是不是登录页」，并把结果暴露在 `window.OmniAuth.isAuthPage` 上；
`support-widget.js` 与 `admin-badge.js` 复用它，**不再各自抄一份路径列表**（抄第二份迟早
会走样）。判定为登录页时一个气泡都不渲染 —— 那里出现「客服工作台」会让人以为没登录也能进
管理界面。

### 四个浮动气泡都可以拖

控制台上共有四个气泡，都能按住拖到任意位置，**位置记在浏览器本地**（`localStorage`
键名前缀 `omniapi.float.`），刷新、切路由都保持：

| 气泡 | 默认位置 | 谁看得见 |
|---|---|---|
| 在线客服 | 左下 | 所有人（`both` 开关打开时） |
| 客服工作台 | 左下、在线客服上方 | 仅管理员 |
| 充值核验 · 暂无待处理 | 右下 | 仅管理员 |
| 显示币种（`/pricing`） | 右下、充值核验上方 | 所有人 |

几个刻意的规则，改代码时别破坏：

- **拖动之后那一次点击会被吞掉**，否则想挪位置的人会被直接带去客服页/充值核验页。
- 位移小于 6px 一律当成「点击」，手指按下的抖动不会把气泡挪走。
- 气泡里的**表单控件不参与拖动**（「显示币种」的下拉框就是）：在控件上按下时
  拖动模块完全不插手，否则原生下拉打不开。
- 拖到屏幕外会自动夹回可视区内，窗口缩小时也重新夹一次。
- 想让它回到默认角落：控制台里执行 `OmniFloat.forget('topup-badge')`（键名见上表对应的
  `omniapi.float.` 后缀），刷新即可。

- 传输：**SSE**（`text/event-stream`）+ 普通 POST 发送。服务端每 20 秒发一次心跳；
  单条流最长 25 分钟后由服务端主动结束，客户端带 `since` 自动重连补齐，不丢消息。
- 存储：`runtime/support/YYYY-MM-DD.jsonl`，append-only 事件日志，状态由折叠事件得出。
- 会话编号形如 `S-20260927-abc123`，**对外只用编号**，不出现数字用户 ID。
- 服务：宿主机 systemd `omniapi-support`，`0.0.0.0:8790`，非特权账号 `omniapi-watch`，
  `MemoryMax=128M`。它**不在充值入账进程里**，也**不能改任何额度**。

## 一分钟体检

```bash
systemctl status omniapi-support --no-pager
curl -s http://127.0.0.1:8790/healthz            # streams / 会话计数
python3 /opt/ai-gateway/ops/support/support_service.py --check
python3 /opt/ai-gateway/ops/support/probe_support_e2e.py   # 公网只读验收 → runtime/support-e2e.json
python3 /opt/ai-gateway/ops/support/probe_workbench_read_e2e.py  # 经营者读队列/读消息的通路
```

`probe_workbench_read_e2e.py` 回答的是另一个问题：**有合法经营者会话时，队列和消息读得出来吗**。
脚本拿不到控制台登录态（Turnstile 拦着），所以它用充值服务的管理口令换一张会话 cookie——
两个服务共用同一份 cookie 密钥，所以客服服务也认。它**只做 GET**，最后还会验证「本探针没有
写权限」（缺 CSRF 时写接口必须 403），确保它不可能动线上数据。

## 部署改动

```bash
sudo sh ops/support/install.sh          # 预演
sudo sh ops/support/install.sh --apply  # 安装/更新（会顺带补 8790 的 ufw 规则）

# nginx 配置有改动时（它会随卷挂载进容器，需要 reload 才生效）
docker compose exec -T nginx nginx -t
docker compose exec -T nginx nginx -s reload
```

**8790 必须放通防火墙**：服务在宿主机、nginx 在容器里经 `host.docker.internal` 回连。
少了这条 ufw 规则的表现是**请求一直挂住直到超时**——看起来像服务挂了，其实是包被丢了。
`install.sh` 已包含这一步（与 8787 完全同款，只允许 `172.16.0.0/12`，公网照旧进不来）。

## 配置（改文件即生效，不用重启）

`config/support.json`：

| 字段 | 说明 |
|---|---|
| `enabled` | 关掉后：页面显示「客服暂时关闭」，注入的入口按钮也不出现 |
| `service_hours` / `greeting` / `offline_hint` | 页面文案 |
| `privacy_note` | **必须在页面展示**：客服消息会保存。不要把「不记录对话内容」写进任何文案 |
| `max_text` | 单条消息上限（内部还会被 2000 字硬顶住，取两者较小值） |
| `retention_days` | 保留天数，配合下面的 `--prune` |
| `handoff_keywords` | 命中即标记「待人工」并立即推送（不受合并间隔限制） |
| `notify.channel` | `none` / `wecom` / `bark` / `telegram` |
| `notify.min_interval_seconds` | 同一会话的推送合并窗口，默认 60 秒 |
| `ai.enabled` 等 | AI 先答的开关与闸门，见下 |

推送凭据在 `.secrets/support-notify.json`（0600，不进仓库）：

```json
{"wecom": {"webhook": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."},
 "bark": {"url": "https://api.day.app/你的key"},
 "telegram": {"token": "123:abc", "chat_id": "42"}}
```

**企业微信群机器人只能单向推**：在通知里回复不会回到本平台。所以「手机为主」的准确
含义是——推送即时到手机，点消息里的深链用手机浏览器回复。要在聊天窗里直接回，只有
Telegram Bot（可自行加 webhook）或企业微信自建应用回调。

推送**成功与失败都会写一条 `notify` 事件**留在日志里，"为什么没收到推送"是可以查的：
`grep '"type": "notify"' runtime/support/*.jsonl | tail`。

## 保留期清理

```bash
python3 ops/support/support_service.py --prune 180          # 预演：列出将删除的整天文件
python3 ops/support/support_service.py --prune 180 --apply  # 真的删
```

## 安全模型

- **身份只由服务端推导**：Bearer 令牌 → 网关 `/api/user/self`；请求体里的 `uid` 一律忽略。
- 经营者侧：cookie（与充值核验页**同一个**，登录一次管两件事）→ 读操作即可；
  **写操作额外要 Origin 绝对匹配 + `X-CSRF-Token`**。
- 限流：发送按账号、按 IP、全局各一条；建会话按 IP；身份校验单独一条（防止公开路由
  变成对网关的放大通道）。
- 渲染一律 `textContent`，服务端不做 HTML 拼接。
- 并发 SSE 流上限 64，超出返回 503 并让前端降级为 5 秒轮询（界面会写明）。
- 客服服务**不碰额度**。经营者在这里看不到「加额度」按钮——那件事只有充值核验页能做。

## 排障

| 现象 | 检查 |
|---|---|
| 页面 502 / 请求超时 | `systemctl is-active omniapi-support`；`ss -ltnp \| grep 8790`；**ufw 是否有 8790 规则** |
| 请求回落到充值服务的 404 | nginx 的 `/brand/api/support/` location 是否排在 `/brand/api/` **之前** |
| 消息发出去了但对方不实时看到 | `/healthz` 的 `streams` 是否为 0；浏览器是否被代理缓冲（nginx 必须 `proxy_buffering off`） |
| 经营者点回复报 CSRF 失败 | 会话 cookie 与实际 Origin 是否一致（`SUPPORT_PUBLIC_ORIGIN`）；刷新页面重新取令牌 |
| 从控制台退出后，充值核验/客服工作台还能进 | 会话代际没有同步。先跑 `probe_logout_e2e.py` 定位；再检查是否有人把代际号缓存进了内存（必须每次 `Service.generation()` 现读），或让某个服务指向了别的 `admin-session-state.json` |
| 「没登录也能看到管理入口」 | 先确认是**残留会话**还是真的没鉴权：干净浏览器（无 cookie）打开登录页应当只有「在线客服」一个都没有，接口应 401。气泡本身在登录页已被禁止渲染 |
| 经营者说「看不到消息 / 不知道去哪看」 | 先确认同一浏览器里**已登录控制台管理员**；控制台左下角应有「客服工作台」按钮（没有就是控制台登录态失效）。再跑 `probe_workbench_read_e2e.py` 确认服务端读通路正常 |
| 用户说「发不出去 / 打不了字」 | **先量输入框宽度**：`python -m unittest tests.test_support_layout`。`plans.css` 的全局 `button{width:100%}` 会把它挤成 22px，见下节 |
| 收不到手机推送 | `config/support.json` 的 `notify.channel` 是否还是 `none`；`.secrets/support-notify.json` 是否存在；看 `notify` 事件里的 `status` 和 `error` |
| 提示「客服会话已达上限」 | 内存里会话数到了 `MAX_CONVERSATIONS`（300）。结束旧会话，或调大上限 |
| 用户说「打不了字」、输入框看不见 | **十有八九是 `plans.css` 的全局 `button{width:100%}` 又赢了**。见下节。跑 `python -m unittest tests.test_support_layout` 一测就知道 |
| 刷新几次页面就提示「发起会话过于频繁」 | `start` 是幂等的（已有进行中的会话就返回那一个），不该按「开新会话」限量。核对 `START_IP_LIMIT`（宽松，60/10 分钟）与 `START_UID_LIMIT`（严格，只管真的新建） |

## 页面布局的两个硬约束（踩过的坑）

**1. `.composer` 里的按钮必须复位 `width`。**

`plans.css` 里有一条给套餐页竖排表单写的全局规则：

```css
button{width:100%;…}     /* 还有 textarea{width:100%} */
```

客服页的 `.composer` 是 `display:flex` 的一行。不复位的话，「发送」的 flex 基准尺寸等于
容器宽度，会把输入框挤成 **22px 宽的一条缝**——页面看着完全正常，但一个字都打不进去。
「转人工」「结束会话」也会各自占满一整行。

所以两个客服页面都显式写了：

```css
.composer button{width:auto;flex:0 0 auto;…}
.row-actions button{width:auto;…}
.quick button{width:auto;…}
```

**2. 这类问题 jsdom 检查不出来，必须用真实浏览器量。**

`tests/support-page-check.js` 在 jsdom 里跑 JS，逻辑再对也看不出「输入框只有 22px」，
因为 jsdom 不做布局。所以另有 `tests/test_support_layout.py`：

- 第 1 层（任何机器都能跑）：静态检查每个 flex 行有没有 `… button { width: … }` 复位规则；
- 第 2 层（本机有 Chrome/Edge 时）：把页面放进无头浏览器，**读实际像素**——
  输入框 ≥ 300px、发送按钮 ≤ 200px、操作按钮不得单独占行。

服务器上没有浏览器，第 2 层会跳过并说明（`OK (skipped=1)` 是预期的，不是失败）。

## 还没做完

- **AI 先答**：`ai.enabled` 目前是 `false`。需要先建一个受限令牌
  （新建 `support_bot` 用户 → 令牌设 `RemainQuota` 上限 + 打开 `ModelLimitsEnabled`
  只允许一两个便宜模型 → 写入 `.secrets/support-ai.json`），再打开开关。
  分级规则（关键词直通、每会话轮数上限、`[[需人工]]`）已在 `support_service.py` 留好位置。
- **匿名咨询**：目前必须登录才能发起会话。公开页面（定价/充值页）的匿名入口涉及人机验证，
  单独评估后再做。
