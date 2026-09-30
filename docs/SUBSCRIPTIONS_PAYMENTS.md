# OmniAPI 套餐与支付准备

## 已确认

- 名称 OmniAPI，深色底、青绿色 O 形图标；原生后台与模型路由保留。
- 定价为采购成本加价 100%，即售价 = 成本 × 2；销售毛利率 50%，尚未扣支付手续费及运营成本。
- 用户暂提供一个兼容支付宝、微信的聚合收款码。当前按人工到账核验开通设计，不伪装自动支付回调。

## 套餐结构

| 类型 | 有效期 | 售价 | 额度 | 额度规则 |
|---|---|---|---|---|
| OmniAPI 体验包 | 30 天 | 19 元 | 2.815273 USD | 固定总额度、不重置 |
| OmniAPI 标准月包 | 30 天 | 39 元 | 5.778719 USD | 固定总额度、不重置 |
| OmniAPI 专业月包 | 30 天 | 99 元 | 14.669057 USD | 固定总额度、不重置 |

售价、有效期由经营者确认（原草案的“体验包 7 天”已被“每月”取代，三档统一为 30 天）。三档均使用 default 分组和相同模型单价，不承诺独占算力、额外并发或高级模型权限。不做无限额度或每日重置，以免实际使用成本超过售卖预算。手动续购，不自动续费。到期后的未使用额度处理须在售卖页面明确；原生订阅到期即不再可用。

不能将 0 额度写入原生套餐，因为原生 0 表示无限额度。三档套餐已在后台创建并保持未启用，启用时机见下文“上线顺序”。

## 定价数据

填写 config/commercial.example.json 的副本，提供 cny_per_usd 作为明确采用的业务结算换算率。每个启用模型填写：

```json
{"models":{"实际模型名":{"currency":"USD","input_per_million":0.5,"output_per_million":1.0}}}
```

这里数字仅展示格式，不是供应商实际报价。若采购采用充值折扣，应先换算成实际现金成本；缓存、工具、图片、搜索等附加项需单独核算。只有基础输入/输出报价不足以保证所有请求净利润。

执行 `python scripts/commercial_quote.py 本地配置路径` 输出草案，不修改线上价格。缺少汇率、成本或套餐售价时会拒绝输出可上线报价。普通文本售价为实际采购单价 × 2；套餐零售额度按实收售价等值配给，不另赠额度。不能同时将 Token 单价翻倍、又将等值套餐额度减半，否则成为四倍加价。

本版本原生套餐接口强制 currency=USD。人民币收款必须明确记录人民币实收额、固定换算率、对应 USD 套餐额及 quota，不能把人民币金额直接填入 USD 字段。不能使用内置默认汇率冒充实时汇率。

### 已填写的经营者数据

- `config/supplier-costs.json`：29 个已验证模型的采购单价，来源为经营者提供的报价截图；`missing_verified_models` 为空。
- `config/commercial.json`：加价倍率 2、结算币种 CNY、换算率 6.7489（2026-09-24/25 中间价）及三档套餐售价 19 / 39 / 99 元。
- `merchant_id` 仍为空，这是经营者明确决定：不写商户编号，客服只公示邮箱。因此套餐页不展示商户编号行，收款依据是经营者本人的聚合收款码。

### 上线顺序

```powershell
python scripts/apply_commercial_pricing.py --dry-run   # 写入快照，不改线上计费
python scripts/publish_plans.py config/commercial.json # 发布套餐目录
python scripts/create_plans.py                         # 只读：列出后台套餐状态
python scripts/apply_commercial_pricing.py             # 切换 29 个模型到商业单价
python scripts/create_plans.py --apply                 # 确保三档套餐存在且为未启用
```

`create_plans.py` 默认只读，`--apply` 才写入，且会把三档套餐**强制留在 `enabled=false`**，由管理员在后台启用。v0.13.2 没有套餐删除接口，因此回滚只能是禁用或修改，必须在启用前确认。切换单价后，`acceptance.py` 的预期扣额会自动跟随线上表达式，不再依赖内部测试价。

**`enabled=false` 必须用状态接口校准，不能只靠创建请求**：`SubscriptionPlan.Enabled` 的列默认值是 `true`，而 GORM 在插入时会省略 `false` 零值，所以创建时传 `enabled: false` 实际落库是 `true`。脚本因此在创建后调用 `PATCH /api/subscription/admin/plans/:id`（该接口用显式列更新 `enabled`）把套餐改回未启用，并在读完后台列表后校验，不一致就报 FAIL。

当前 29 个模型仍是内部测试价格，尚未应用商业定价；后台已有三档套餐但全部未启用。

## 人工收款与开通

1. 公示套餐名称、有效期、实际人民币价格、可用额度及人工开通说明。二维码和收款人信息必须由站点经营者提供并核对。
2. 用户付款前记录本站用户 ID、套餐、应付金额；付款后提供交易单号和付款时间。不能要求用户提供登录密码、API Key 或完整支付账户信息。
3. 管理员登录自己的收款渠道核对实际到账、金额和交易单号，不能只凭截图或“已付款”按钮开通。
4. 以支付交易单号建立受限的人工对账记录，先检查是否处理过，再在原生后台为该用户绑定对应套餐。记录 user_id、plan_id、实收额、币种、到账时间、审核人、subscription_id，避免重复开通。
5. 用户在原生钱包/订阅页面核对套餐、额度、到期时间；订阅消费偏好使用原生选项。退款与取消订阅需同时更新对账记录，避免只退钱但额度继续可用。

扫码本身不创建已支付订单，不自动赠额，不开启虚假的在线支付方式。原生后台支持管理员绑定与取消套餐，无需自研业务表。

## 自动支付：易支付（Epay）

> 当前线上版本已升级为 v1.0.0-rc.40。本节保留了 v0.13.2 的历史调研记录；可执行的 rc.40 钱包支付流程以 [PAYMENT_IMPLEMENTATION.md](PAYMENT_IMPLEMENTATION.md) 为准。rc.40 已增加合规确认门槛。

v0.13.2 内置易支付客户端（`github.com/Calcium-Ion/go-epay`），接易支付兼容的聚合通道不需要改代码。

### 配置位置与字段（前端界面实测）

后台 → 系统设置 → 支付设置，分两处：

**通用设置**

| 界面字段 | 选项键 | 说明 |
|---|---|---|
| 服务器地址 | `ServerAddress` | 决定回调地址与首页展示地址。**易支付设置要求先填它**，否则保存被拒。 |
| 回调地址 | `CustomCallbackAddress` | 留空时用服务器地址；填写后覆盖默认值。 |
| 充值方式设置 | `PayMethods` | JSON 数组，`type` 必须是服务商支持的渠道。 |
| 自定义充值数量选项 | `payment_setting.amount_options` | JSON 数组，默认 `[10,20,50,100,200,500]`。 |
| 充值金额折扣配置 | `payment_setting.amount_discount` | JSON 对象。 |
| 充值分组倍率 | `TopupGroupRatio` | 分组级充值倍率。 |

**易支付设置**

| 界面字段 | 选项键 | 说明 |
|---|---|---|
| 支付地址 | `PayAddress` | 服务商网关**基础地址**，详见下方注意事项。 |
| 商户 ID | `EpayId` | 服务商后台的 PID。 |
| API 密钥 | `EpayKey` | 服务商后台的 KEY，只存服务端。 |
| 充值价格（x元/美金） | `Price` | 钱包充值换算率，当前 6.7489。 |
| 最低充值美元数量 | `MinTopUp` | 默认 1。 |

### 启用条件（v0.13.2 源码逐行核对）

```go
func isEpayTopUpEnabled() bool {
    return isEpayWebhookConfigured() && len(operation_setting.PayMethods) > 0
}
func isEpayWebhookConfigured() bool {
    return PayAddress != "" && EpayId != "" && EpayKey != ""
}
```

三个字段非空 + 支付方式非空即开启。**没有单独的开关，也没有合规确认门槛**——`isPaymentComplianceConfirmed` 是更新版本才引入的，v0.13.2 的 `controller/payment_webhook_availability.go` 里不存在这一段。

### 网关请求与回调地址

- 下单地址由 go-epay 拼成 `path.Join(PayAddress, "/submit.php")`，所以**`PayAddress` 只填基础地址，不要手工带 `/submit.php`**，否则会变成 `/submit.php/submit.php`。
- 签名算法：过滤掉 `sign`、`sign_type` 和空值 → 按 key 字典序 → 拼成 `k=v&k=v` → 末尾追加商户 KEY → MD5（小写十六进制）。
- 回调地址 = `CustomCallbackAddress`，为空则用 `ServerAddress`：

| 用途 | 完整路径 |
|---|---|
| 钱包充值 异步通知 | `{回调地址}/api/user/epay/notify` |
| 钱包充值 浏览器返回 | `{ServerAddress}/console/log`（注意这里用的是 ServerAddress，不跟随覆盖值） |
| 套餐 异步通知 | `{回调地址}/api/subscription/epay/notify` |
| 套餐 浏览器返回 | `{回调地址}/api/subscription/epay/return` |

浏览器返回页不能当作到账凭据，到账必须以服务端通知的签名校验结果为准。

### ⚠️ 先别开：套餐实收金额只有页面标价的 1/6.75（实测）

`SubscriptionRequestEpay` 把 `plan.price_amount` 原值直接交给网关当收款额，**没有乘 `USDExchangeRate`**：

```go
Money: strconv.FormatFloat(plan.PriceAmount, 'f', 2, 64),   // 套餐，未换算
```

对照钱包充值 `RequestEpay`：它走 `getPayMoney()` = `amount × Price × 分组倍率 × 折扣`，**是有换算的**。也就是只有套餐这条路径少了一步。

而前端在 `quota_display_type = CNY` 时按 `price_amount × usd_exchange_rate` 展示人民币价格，两者叠加的结果（`scripts/configure_epay.py --preview` 在 2026-09-26 跑出的实测值）：

| 套餐 | 页面显示给买家 | 网关实际收取 |
|---|---|---|
| 体验包 | ¥19.00 | **¥2.82** |
| 标准月包 | ¥39.00 | **¥5.78** |
| 专业月包 | ¥99.00 | **¥14.67** |

**保持现价开启易支付，每单少收约 86%。** 已核对 `main` 分支同样如此，不是某个已修复的缺陷。

三个可选方向，都需要经营者决策：

1. **不开原生易支付**，继续用已交付的人工扫码核验流程（当前推荐，已端到端验证）。
2. **原生易支付只用于钱包充值**（充值金额计算正确），同时把三档套餐设为 `enabled=false`，避免用户点到金额错误的套餐支付。注意 `payment_setting.amount_options` 的语义是「美元额度数」而非人民币金额，需与充值页档位对齐。
3. **改价 + 调整显示币种**：把 `price_amount` 改成人民币实收额，并把 `quota_display_type` 换成倍率为 1 的币种，代价是全站额度显示口径随之改变。

### 其余前置条件

- **公网 HTTPS 域名**。当前 `ServerAddress = http://160.202.46.77` 是明文 IP，多数服务商只接受 HTTPS 域名回调。
- 服务商提供的 网关地址 / 商户 ID(PID) / 密钥(KEY)。
- `PayMethods` 里默认的 `custom1` 只是模板占位项，服务商不支持时点下去会下单失败，应替换成实际渠道。

### 配置工具

`python scripts/configure_epay.py`：默认预演，`--apply` 才写入（写前快照到 `runtime/epay-before-apply.json`）；`--preview` 只输出套餐金额对照。脚本会拒绝带 `submit.php` 的网关地址、拒绝回环/内网回调地址，并在写入后回读 `enable_online_topup` 验证。

### 当前实测状态

| 项 | 值 |
|---|---|
| `PayAddress` / `EpayId` / `EpayKey` | 空 / 空 / 未设置 |
| `CustomCallbackAddress` | 空（回调将回落到 `ServerAddress`） |
| `PayMethods` | 支付宝、微信、自定义1（非空） |
| `Price` / `USDExchangeRate` | 6.7489 / 6.7489 |
| `GET /api/user/topup/info` | `enable_online_topup: false` |
| `POST /api/user/pay` | 「当前管理员未配置支付信息」 |

`topup/info` 里的 `pay_methods` 只是按钮展示配置，**不代表通道可用**。原生订阅购买弹窗（`SubscriptionPurchaseModal`）在 `hasAnyPayment` 为假时显示「管理员未开启在线支付功能，请联系管理员配置。」

## 已交付：人工核验充值

在不动原生支付通道的前提下，用原生管理接口直接把额度加到用户钱包：

```text
用户 → /brand/topup.html 选择金额（预设档位，或自定义，最低 1 元）
     → 充值账号自动识别：钱包页跳转带 uid，或靠 refresh cookie，无需手填
     → 看收款码、扫码付款 → 点「我已付款，提交充值申请」
经营者 → 控制台右下角出现「N 笔待核验充值」角标
       → 打开 /brand/admin.html（自动认控制台管理员身份），核对金额与到账时间
       → 点「核实到账，加额度」→ 额度当场计入用户钱包
用户 → 刷新钱包页即可看到，**不需要再做任何操作**
```

用户点「我已付款」这一步只是**告知经营者去核对**，不产生任何额度；提交失败时页面自动退回「复制申请发给客服」，人工登记，不会卡死。用户侧仍不填写交易单号：单号由经营者从自己的收款记录中取得，作为台账的幂等键，申请记录里保留提交时间便于对账匹配。

**账号自动识别**：优先用 `?uid=` 参数；否则调 `POST /api/user/auth/refresh`，靠浏览器的 httpOnly refresh cookie 换取当前用户。

⚠️ **rc.40 不再把用户信息写入 localStorage**——旧版的 `user` / `uid` 键只剩「待清理」用途（见前端包里那行 `new Set([..., "user","uid","aff",...])`），没有任何前端可读的持久化副本。这一点在线上实测确认过：带 Bearer 调 `/api/user/self` 得 200，仅靠 refresh cookie 调 `/api/user/self` 得 401，而 `POST /api/user/auth/refresh` 用一个 cookie 就能返回 `data.user`。**不要沿用 v0.13.2 时期「读 localStorage.user」的做法。**

两处都取不到（未登录，refresh 返回 401）时提示先登录并禁用付款按钮，**不会退回让用户手填用户 ID**。

**金额**：预设档位 10/20/50/100/200/500，另可自定义任意金额，单笔最低 1 元、上限 100000 元。换算与 `scripts/commercial_quote.py` 同源（往下取 6 位小数，再乘额度单价向下取整），已用 `tests/topup-page-check.js` 逐值核对。

| 组件 | 位置 | 作用 |
|---|---|---|
| 充值配置 | `config/topup.json` | 金额档位、最低金额、客服、收款码、结算汇率、收款开关 |
| 目录发布 | `scripts/publish_topup.py` | 校验并生成 `nginx/brand/topup.json`；缺项输出 BLOCKED 且不写文件 |
| 充值页 | `nginx/brand/topup.html` + `topup.js` | 选金额、展示收款码、提交申请（复用 `plans.css`） |
| 提交服务 | `ops/topup_watch/topup_watch.py` | 宿主机 systemd 常驻，接收申请并提供管理端接口 |
| 安装脚本 | `ops/topup_watch/install.sh` | 默认预演，`--apply` 才执行：服务账号、权限、口令、单元、防火墙 |
| systemd 单元 | `ops/systemd/omniapi-topup-watch.service` | 以非特权账号 `omniapi-watch` 运行，`ProtectSystem=strict` |
| 申请台账 | `runtime/topup-requests.jsonl` | append-only 事件日志，状态由折叠事件得出，不会覆写历史 |
| 管理页 | `nginx/brand/admin.html` + `admin.js` | 自动认控制台管理员身份 → 待核验列表 → 一键加到用户钱包 |
| 控制台角标 | `nginx/brand/admin-badge.js` | 注入 SPA，只有管理员浏览器才显示待核验数量 |
| 队列 CLI | `scripts/topup_requests.py` | `--pending` / `--all` / `--dismiss <ID> --apply` |
| 入账 | `scripts/credit_topup.py` | 幂等台账 + 直接把额度加到用户钱包；默认 dry-run，`--apply` 才入账 |
| 端到端探针 | `probe_fx_quote_e2e`（报价→提交→待核验）、`probe_admin_selfheal_e2e`（管理端路由/会话自愈/CSRF 与 Origin 守卫）、`probe_credit_e2e`（经营者侧入账）、`probe_balance_e2e`（余额核对） | 按层拆开验证，各自用临时文件，不碰线上队列 |
| 已退役 | `probe_e2e.py`、`probe_console_identity.py` | 都依赖密码登录网关（Turnstile 之后脚本做不到）和被删除的无报价提交路径，**跑起来只会误报失败**；文件保留为明确退役的存根并指向替代探针 |
| 台账 | `runtime/topup-ledger.json` | 交易单号 → 用户、金额、额度、入账前后余额、是否核对通过 |
| 入口 | 选项 `TopUpLink = /brand/topup.html` | 钱包页显示「前往扫码充值」入口 |

### 入账账号由服务端推导（2026-09-27 改造）

**改之前**：`POST /brand/api/topup/request` 是公开匿名接口，直接采信请求体里的 `uid`，
任何人 POST 一个数字就能指定额度记到哪个账号；同一个 `uid` 还有第二条「不报价」的
旧路径也读它。

**改之后**：

- 页面提交时带 `Authorization: Bearer <access_token>`（来自同源 refresh cookie 换取的
  短时令牌，页面本来就用它渲染账号标签）。
- 服务端拿令牌问网关 `/api/user/self`，**用网关返回的 id 作为入账目标**；
  `payload['uid']` 被完全忽略。无令牌或令牌无效 → **401**（而不是 400/429，
  客户端不应把它误解成参数错误或限流）。
- `?uid=` 降级为**纯展示**，不再参与身份判定——否则一条钓鱼链接就能把钱引到别人账号。
- **不报价的旧提交路径已删除**：报价是金额与额度的唯一来源，留着它会让唯一金额尾数
  （P2）被绕过。
- 身份校验有**独立限流** `20 次 / 10 分钟 / 来源`：未认证请求若每次都触发一次网关查询，
  公开路由就成了对网关的放大通道，也会烧掉经营者自己的提交额度。
- 事件里记 `uid_source='verified-session'`，便于事后区分新旧路径。

**一个副作用的正确性**：未登录用户不能提交。页面本来就要求登录才启用提交按钮，
所以正常用户无感。

**顺带清理**：`scripts/topup_requests.py` 里只做「校验客户端 uid」的 `submission()`
助手已删除——它存在的唯一目的就是信任客户端。

### 同一轮修掉的两个旧缺陷

1. `recent_duplicate()` 比较的是 `row['cny']`，但报价路径写入的事件只有
   `quoted_amount`，所以「同人同额重复提交」的去重在报价路径上**一直失效**。
   已改为 `row.get('quoted_amount') or row.get('cny')`。
2. `quote()` 用 `.quantize(Decimal('0.000001'))`（默认**四舍五入**），而页面档位、
   `build_request` 与台账都用 `usd_from_cny()`（**向下取整**）——同一天同一金额，
   页面承诺的额度会比实际到账多 1。已统一走 `usd_from_cny()`。

### 充值申请的告知与核验

管理页地址：`https://omniapi.biz/brand/admin.html`。**经营者本来就登录着控制台，所以不需要第二遍密码**：页面（以及控制台右下角的角标）会用控制台的 refresh cookie 换一个短时 access_token，交给充值服务向网关核实管理员身份（`role >= 10`），通过后直接发会话 cookie，有效期 30 天。

口令入口保留为兜底（未登录控制台时使用）。口令在安装时生成一次，盐与哈希存在 `.secrets/topup-admin.json`（属组可读，非 root 可读），口令副本在 `.secrets/topup-admin-password.txt`（仅 root）。**存进密码管理器后应删除口令副本。**

会话 cookie 里带 scope，记录身份是「口令」还是「控制台账号」，发码与忽略操作会把它写进事件日志的 `by` 字段，便于事后追责。

接口与安全边界：

- `POST /brand/api/topup/request` **需要已登录身份**（`Authorization: Bearer <access_token>`，无令牌返回 401）；`/brand/admin/api/*` 需要会话 cookie。
- 换会话的两条路径（`POST /brand/admin/api/session` 用控制台令牌、`POST /brand/admin/api/login` 用口令）都走同一条登录限流。
- 角色判定由网关自己做（服务用调用方令牌请求 `/api/user/self`），服务不解析也不缓存令牌；`role < 10`、账号被停用、令牌伪造一律 403。
- 金额与额度在**服务端重算**（与页面、台账同一组 `commercial_quote` 函数），页面传来的数字只用于展示，改包造不出额度。
- 只接受普通十进制金额，明确拒绝科学计数法（`1e9` 会掩盖数量级，读队列时看不出来）。
- 限流：单来源 10 次 / 10 分钟，全局 60 次 / 10 分钟（真正的兜底），管理端登录/换会话 8 次 / 15 分钟。
- 同账号同金额在 10 分钟窗口内视为同一笔，重复点击不会重复入队。
- 端口 8787 只对 docker 网段放行（`ufw allow from 172.16.0.0/12`），其余来源拒绝。**规则顺序不能颠倒**：容器回连宿主机走 INPUT 链，一条笼统的 `ufw deny 8787` 会把容器一起挡住，表现为页面 502。
- 发码复用同一个管理员会话：`POST /api/user/login` 在 20 次 / 20 分钟的关键限流内，逐次登录会在批量发码时打满自己的额度。

控制台角标在有待核验时显示琥珀色计数；无待处理时显示浅色「充值核验 · 暂无待处理」，这样「没提示」不会再被误读成功能坏了；会话过期时角标直接收起，不会显示误导性的「0」。

实测（证据：`runtime/topup-notify-e2e.json`、`runtime/topup-console-identity.json`）：

- 完整链路：提交 → 管理端列表可见（含用户名）→ 一键入账 → 额度 498211 → 1239072，**delta 740861 与期望完全一致**；重复入账被拒（该申请已处理）。
- 身份通道：站长账号仅凭控制台令牌换到会话（200），读到待核验列表；普通账号换会话 403、读列表 401；伪造令牌 403；口令入口仍可用。

设计要点：

- **额度换算与套餐完全同源**：`usd = 人民币 / 6.7489`（向下取 6 位），`quota = usd × 500000`（向下取整）。充值页、套餐页、后台套餐三处不可能漂移。
- **不伪装自动到账**：扫码不产生订单、不加额度；只有经营者核验后才加额度。
- **幂等**：同一交易单号第二次执行会被台账直接拒绝。
- **为什么直接用管理接口加额度，而不是兑换码，也不是改数据库**：
  - 兑换码要多一步「用户自己去钱包页兑换」，用户不兑就等于没到账（2026-09-26 真实发生过一笔）；
  - 直接改数据库有实际风险：网关把用户记录缓存在内存（`MEMORY_CACHE_ENABLED=true`，`SYNC_FREQUENCY=10`），SQL 写入可能被缓存覆盖或读不到；
  - `POST /api/user/manage {action: add_quota, mode: add}` 是官方管理接口，服务端**原子增量**，并发两笔也不会互相覆盖，并且走的是维护缓存的同一层。
- **`add_quota` 的实测语义**（用于防止误操作）：必须带 `mode`，否则返回「Invalid parameters」；`mode=add` 是增量且不接受负数；`mode=override` 是**覆盖**（会把余额设成给定值，谨慎使用）。探针见 `runtime/quota-semantics.json`。
- **入账后必须核对**：脚本读入账前/后两次余额，只有差值等于应发额度才记 `balance_verified: true`；若接口返回成功但余额一直读不到期望值（缓存滞后），仍写入台账并标注，**拒绝重试**以免重复入账，同时在管理页明确提示去后台核对。
- **兑换码方式保留为兜底**：`--via code`，用于管理接口行为变化时的应急。兑换码名称限 1–20 字符（脚本已截断）。

## 支付金额换算口径（已复核）

`Price`（钱包充值换算）与 `USDExchangeRate`（显示换算）原先分别是 7.3 和 6.8，都与业务结算率 6.7489 不一致：套餐 19 元在支付弹窗里显示成 **¥19.14**（截图在案）。两者现已统一为 6.7489，原值保存在 `runtime/topup-before-settings.json`。

2026-09-26 复核时发现 `Price` 实际停留在 `6.75`（迁移重放那一步写成了两位小数），与 `USDExchangeRate = 6.7489` 相差 0.016%；已写回 6.7489。复核结果：

| 请求 | 返回 |
|---|---|
| `POST /api/user/amount {amount:10}` | `67.49`（统一前为 `73.00`） |
| `POST /api/user/amount {amount:19}` | `128.23` |

注意这里的 `amount` 是**美元额度数**，不是人民币金额：填 19 表示「19 美元额度」，需付 ¥128.23。这与 `/brand/topup.html` 上「19 元」档位的含义不同，两个入口的数字不能混读。

## 人民币汇率：行情自动折算（2026-09-27 起）

经营者选择**全自动跟随东方财富行情**，每次下单取最新报价。相关文件与字段：

| 位置 | 作用 |
|---|---|
| `config/topup.json` → `fx.enabled` | 是否使用行情（当前 `true`） |
| `config/topup.json` → `fx.provider` | `eastmoney`（适配器在 `scripts/fx_rates.py`） |
| `config/topup.json` → `fx.refresh_seconds` | 距上次抓取超过该秒数就重新抓（300） |
| `config/topup.json` → `fx.max_age_seconds` | **行情报价自身**的可用时限（345600 = 96 小时） |
| `config/topup.json` → `fx.fallback_max_age_seconds` | **备用汇率**的可用时限（2592000 = 30 天） |
| `config/topup.json` → `fx.source_decision` | 经营者决定与已知限制的留痕（日期、选择、说明） |
| `config/topup.json` → `cny_per_usd` / `fx_as_of` | 备用汇率及其采用日期，最后一层兜底 |
| `runtime/fx-rates.json` | 行情快照（含每种货币的报价时间、来源、许可状态） |
| `ops/systemd/omniapi-fx-refresh.{service,timer}` | 每 5 分钟预热快照 |

### 两个时钟，不要混用

- `fetched_at`（本次抓取时间）决定**要不要重新抓**。用报价时间判断会出现「闭市期间每次访问都去抓」。
- `as_of` / `rate_as_of`（报价公布时间）决定**这个汇率还能不能定价**。用抓取时间判断会把一份合法的周末报价误判为过期。

### 兜底阶梯（fail-visible，不是 fail-closed）

行情不可用时不会直接中断收款，而是按顺序降级，并且**每一层都在页面上标明实际来源与报价时间**：

1. 报价在 `max_age_seconds` 内 → 按行情折算，来源写成行情名称（报价时间早于刷新间隔时追加「最新已公布报价」）。
2. 报价超出行情时限但在 `fallback_max_age_seconds` 内 → **沿用最近一次真实行情报价**，来源写成「（最近一次报价）」，并在页面显示「本单沿用最近一次行情报价折算」。
3. 快照完全不可用 → 用经营者设定的 `cny_per_usd`，来源写成「备用汇率（经营者设定，YYYY-MM-DD 起沿用）」，并显示提示。
4. 连备用汇率的 `fx_as_of` 也超出窗口 → **明确拒绝报价**（错误信息包含原因与期限），不静默用过期汇率。

优先用「最近一次真实行情」而不是手填数字，是因为前者更接近市场且自带报价时间；手填数字只在完全没有快照时使用。

### 已知限制（经营者已知情并接受，记录在 `fx.source_decision`）

- 从香港服务器访问该端点会被 302 到 **push2delay（延迟行情）**，不是交易所实时流。
- **人民币中间价本身就是每个交易日公布一次**，因此「实时中间汇率」对 CNY 并不存在；同一交易日内价格稳定，跨日才会变。
- 该端点是**公开网页行情，不是持牌数据产品**。其服务条款限制未经书面许可用行情数据开发衍生服务。若要长期合规运营在线收款，建议改用持牌源（Xe 适配器已写好，只缺凭据）或回到固定汇率。

### 实测（2026-09-27，香港服务器）

只读探测：HTTP 200，0.81 秒返回；USDCNYC 报价时间为 **2026-09-24**，其余 6 个币种为 09-25 收盘。按 96 小时窗口，CNY 报价处于可用期内；若报价继续不更新，会在第 5 天转入第 2 层兜底并在页面标注。

## 支付方式的收款信息在配置里，不在代码里

`config/payment-methods.json` 由经营者维护（收款链接、钱包地址、二维码、最低金额、公开说明）。服务**每次请求都会重新读取该文件**，因此改链接只需编辑一个 JSON，不需要改代码或重新部署：

```sh
# 在服务器上改完即生效，无需重启服务
nano /opt/ai-gateway/config/payment-methods.json
```

- 文件里的 `id` 必须是内置轨道之一（`cny-qr` 由 `config/topup.json` 派生，其余为 `payoneer` 与 4 个稳定币）；出现未知 `id` 会直接报错，而不是被静默忽略。
- 只接受白名单字段，其它键一律忽略。
- 管理页（`/brand/admin.html`）**只负责启用开关与二维码上传**，收款链接、钱包地址、最低金额与说明在该页只读显示。这样即使管理页会话被利用，也无法改写收款去向。上传的二维码只接受指向服务自己管理的资源目录的路径。
- `runtime/payment-settings.json` 现在只保存开关与排序（`version: 2`）；旧版把整行支付方式写在该文件里，读取时只取其开关，避免旧文件里的收款链接被恢复。
- 未配置完整的收款方式不会出现在用户侧：Payoneer 缺链接、稳定币缺地址或二维码时，即使被勾选为启用也不会展示。

## 入账会话：为什么必须先由经营者在管理页确认身份

网关的登录接口受 Cloudflare Turnstile 保护（`turnstile_check=true`），脚本无法代替人机验证。所以服务**不自行登录**，而是复用经营者浏览器用 `/api/user/auth/refresh` 换来的 access_token 作为网关会话，用它执行 `POST /api/user/manage {action: add_quota, mode: add}`。

| 情形 | 行为 |
|---|---|
| 经营者打开着管理页 / 控制台角标 | 会话已就绪，点「核实到账，加额度」直接成功 |
| 服务刚重启、经营者还没打开发过管理页 | 页面从 `summary.gateway_session` 读到会话已丢，**自己补种一次**，经营者无需动作。补种不成功才显示提示与「重新确认身份」按钮；**不会**去撞被保护的登录接口，也不会静默失败 |
| 只用管理口令进入过（没登录控制台） | 口令会话只能查看队列。入账仍需要控制台管理员登录态，页面会明确说明并给出补种按钮 |
| access_token 过期 | 服务丢弃会话并重试一次（此时页面通常已带来新令牌）；台账仍保证同一笔不重复入账 |
| 入账途中会话失效 | 服务端返回 `409 + code=gateway_session_missing`，页面补种后重试一次，经营者无感 |

**为什么 409 而不是 400**：`gateway_session_missing` 是「需要重新确认身份」，不是「你填错了」。混在 400 里会让经营者以为净到账金额有问题，而错误提示里那句「刷新管理页」在旧实现下根本修不好问题——刷新走的还是「cookie 有效就直接进面板」那条短路逻辑。

实测（`runtime/topup-credit-e2e.json`、`runtime/topup-fx-quote-e2e.json`）：报价 → 提交 → 管理页看到该笔（含报价汇率与来源）→ 入账 → 额度核对，其中入账这一步的失败路径已被验证会给出上述可操作提示。

## 关键接口限流（上线前必须知道）

New API 的 `CriticalRateLimit` 默认 **20 次 / 20 分钟 / 客户端 IP**，且 `POST /api/user/login` 与 `POST /api/user/topup`（兑换码充值）都在这条限流内。

**服务器端脚本和浏览器走的是不同的桶**，但各自都会被打满：

- 在服务器上跑的运维/验收脚本连 `127.0.0.1:80`，共享 `CT127.0.0.1` 这一个桶。密集执行多个脚本（每次都要登录）会把它打满，随后**服务器本地的脚本登录全部 429**，验收脚本会以 `runner: FAIL / RuntimeError` 的形式失败——这是限流，不是环境坏了。等待约 20 分钟即可恢复。
- 用户和经营者从公网 IP 访问走各自的桶，互不影响。
- 真正的风险是**同一出口 IP 的多个用户**（例如共用 NAT）：多人同时兑换兑换码会互相挤占。批量操作前留出冷却时间。

出现 429 时，`acceptance.py` 的报错信息只有 `Acceptance failed: RuntimeError`，容易误判成部署故障。排查时先确认是不是限流。

## 套餐页面发布

品牌套餐页 `nginx/brand/plans.html` 从 `/brand/plans.json` 读取目录。该文件由脚本生成，不手工编辑：

```powershell
python scripts/publish_plans.py config/commercial.json --dry-run
python scripts/publish_plans.py config/commercial.json
```

脚本复用 `commercial_quote.py` 的报价逻辑，字段与原生订阅结构一致（`price_amount`、`currency`、`duration_unit`、`duration_value`、`total_amount`、`quota_reset_period`），可直接用于后台创建套餐。约束：

- 缺换算率、缺任一已验证模型成本、缺套餐售价时输出 `BLOCKED` 并列出具体原因，不写文件。
- 只要求 29 个 `PASS` 模型有成本；`FAIL` 与 `NOT TESTED` 模型不纳入，避免为未开放模型定价。
- `merchant_id` 可选：经营者已明确表示不填写商户编号，客服只公示邮箱。`contact` 必须由经营者提供；声明 `accepting_payments` 却缺 `contact` 时直接报错，脚本不代填。
- 未声明收款时目录以非收款状态发布，页面不展示付款表单，不会引导用户付款。
- 生成的套餐 `enabled` 始终为 `false` 字段值；实际落库状态由 `create_plans.py` 强制校准（见下），脚本不改线上计费。

`nginx/brand` 以只读方式挂载进容器，重新生成目录后无需重启即可生效；页面使用 `cache: no-store` 读取。

## 当前完成状态

| 项目 | 状态 |
|---|---|
| 品牌名称与图标 | PASS |
| 套餐结构及报价工具 | PASS：加价倍率、换算率、三档售价与 29 个模型采购成本均已填写 |
| 套餐页面与目录发布脚本 | 已交付，测试覆盖阻塞与防代填逻辑 |
| 商业单价 | **已切换**：29 个模型全部改为 `v1:p * 成本入*2 + c * 成本出*2`，描述改为中性文案，0 处不一致。快照 `runtime/commercial-before-apply.json` 可回滚 |
| 后台原生套餐 | **已启用**：id 1/2/3，售价 2.815273 / 5.778719 / 14.669057 USD，折合 ¥19.00 / ¥39.00 / ¥99.00，30 天不重置，用户端可见 3 档 |
| 收款码展示 | 图片已就位；商户编号按经营者要求不公示，客服走邮箱 |
| 人工收款开通流程 | 已记录，待实际到账演练 |
| 人民币汇率 | **已切换为行情自动折算**（东方财富，每单注明来源与报价时间）；兜底阶梯与已知限制见上 |
| 支付方式收款信息 | **已移出代码**：`config/payment-methods.json` 维护，改文件即时生效 |
| 原生在线支付 | **NOT TESTED / 不可用**：`PayAddress`、`EpayId` 为空，且回调必须是公网 HTTPS |

### 启用顺序的教训：先切单价，再启用套餐

套餐启用后用户就能看到 19/39/99 三档。**但如果在模型仍是内部测试价时开放售卖，高价模型会亏钱。**实算：`claude-fable-5` 采购成本 $1.50/$7.50 每百万 Token，用户花 19 元得到 1407636 额度，按测试价能换约 0.94 组「100 万输入 + 100 万输出」，而这些量向上游要付约 **$8.46 ≈ ¥57**——一单亏约 38 元。所以顺序必须是先 `apply_commercial_pricing.py` 再启用套餐。

### 启用套餐不等于原生购买能走通

套餐已启用，原生订阅页能看到三档，但**点「购买」仍会提示「管理员未开启在线支付功能」**（`hasEpay = enable_online_topup && epayMethods.length > 0` 为假）。这不是缺陷：原生购买必须有真实支付通道。**用户应走 `/brand/plans.html` 的扫码 + 人工核验流程**。
