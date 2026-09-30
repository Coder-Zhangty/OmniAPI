# OmniAPI 原生模型广场、多币种与多渠道充值报告

更新时间：2026-09-27

## 发布结果

| 项目 | 状态 | 线上证据 |
|---|---|---|
| New API 原生 `/pricing` | PASS | 取消静态页覆盖；原生搜索、筛选、状态、详情和响应式布局继续由 rc.40 提供。 |
| 29 个模型元数据 | PASS | 29/29 行具有供应商名称、`vendor_id` 与原生彩色图标。 |
| 输入/输出计费说明 | PASS | 增强脚本把表达式显示为“输入 / 输出单价（每百万 Tokens）”。 |
| 商业价格两位小数 | PASS | `billing_setting` 和 29 个 `billing_expr` 已在事务中同步更新；验收脚本确认无超过两位小数的价格。 |
| 成本两倍与向上取整 | PASS | 商业报价使用 `ceil(cost * 2 * 100) / 100`，单元测试覆盖低价模型。 |
| 多币种展示组件 | PASS | USD、CNY、EUR、GBP、JPY、HKD、CAD、AUD 已实现；JPY 显示整数，其余显示两位小数。 |
| Xe 实时行情 | BLOCKED | 未提供 Xe API 凭据，`fx.enabled=false`；公开接口返回不可用状态，页面只显示 USD，新换汇订单不会创建。 |
| 人民币扫码 | PASS | 原聚合收款码继续开放，订单提交后人工核验。 |
| Payoneer | PASS | 固定 USD 链接和二维码已上线，最低金额 $20；低于 $20 的报价返回 400。 |
| USDT / USDC | BLOCKED | 后台配置能力已交付；没有网络、地址和二维码的资产不会公开。 |
| 人工核验与净到账入账 | PASS（自动化测试） | 核验必须填写 `net_received`，服务端计算 `credited_usd`；重复参考号、交易哈希和重复核验均被拒绝。尚未进行真实 Payoneer 或链上付款。 |
| 旧人民币订单兼容 | PASS | 保留旧提交格式和 append-only 事件读取。 |
| 管理接口隔离 | PASS | 未登录访问 `/brand/admin/api/settings` 返回 401。 |
| 充值服务端口隔离 | PASS | 8787 仅允许 Docker 网桥；从公网连接实测超时。公网只开放 22、80、443。 |
| HTTPS 主域名与 `www` | PASS | 证书 SAN 包含 `omniapi.biz`、`www.omniapi.biz`，到期日 2026-12-26；HTTP 与 `www` 均 308 到主站 HTTPS。 |

## 定价与供应商映射

| 模型前缀 | 供应商 | 原生图标 |
|---|---|---|
| `claude-` | Anthropic | `Claude.Color` |
| `gemini-` | Google | `Gemini.Color` |
| `gpt-`、`codex-` | OpenAI | `OpenAI` |
| `deepseek-` | DeepSeek | `DeepSeek.Color` |
| `glm-` | 智谱 | `Zhipu.Color` |
| `grok-` | xAI | `XAI` |

更新前的模型元数据和 `billing_setting` 已保存到备份和运行时快照。线上 SQL 通过单事务更新两项计费配置和 29 行模型元数据，执行结果为 `UPDATE 1 / UPDATE 1 / UPDATE 29 / COMMIT`。页面显示和实际扣费读取同一份 `billing_expr`，避免维护第二套价格。

## 充值流程

1. 前端读取 `/brand/api/payment-methods`，只显示完整且启用的方式。
2. 前端向 `/brand/api/topup/quote` 请求 15 分钟报价。服务端校验方式、币种、最低金额与行情新鲜度。
3. 用户付款后以 `quote_id` 提交 `/brand/api/topup/request`（交易参考号是**选填**的）。客户端金额和汇率不作为可信数据。
4. 管理员核验实际净到账金额。Payoneer、USDT、USDC 按净到账 USD 1:1 发放；人民币按报价快照汇率折算。
5. 服务端追加终态事件并写入独立额度台账。**填了**的参考号全局唯一（大小写不敏感）、终态订单不能再次入账；没填则靠金额 + 时间 + 账号人工比对。

页面明确显示“付款到账及人工核验可能延迟约 5 分钟”和“最终额度按实际净到账核算”。稳定币页面还会显示资产、网络、文字地址、复制按钮、二维码与转错网络提示；配置不完整时整项隐藏。

## 汇率失效策略

- Xe 凭据只从 `/opt/ai-gateway/.secrets/xe.json` 读取，不进入容器环境、浏览器、日志或 Git。
- 默认每 5 分钟尝试刷新，缓存记录供应商时间与抓取时间。
- 行情超过 15 分钟标记为过期；模型广场回退 USD，新换汇报价失败。
- 有效期内已经提交的订单继续使用不可变的报价快照。
- 当前未配置 Xe，所以人民币旧流程和 USD Payoneer可用，多币种换汇订单保持关闭。

## 测试证据

- 本地自动化：`122/122 PASS`。
- JavaScript 语法：`topup.js`、`admin.js`、`pricing-enhance.js` 全部 PASS。
- Python 编译：汇率、报价、订单、入账、服务与验收脚本全部 PASS。
- 线上验收：`scripts/verify_payment_upgrade.py` 返回 `status=PASS`，10 项检查全部为 `true`。
- Nginx：`nginx -t` PASS。
- 容器：PostgreSQL、Redis、New API、Nginx、auth-guard 全部 healthy。
- 备份回滚点：`/opt/ai-gateway/backups/2026-09-27_04-19-49`。

完整自动支付仍需支付服务商回调。当前收款码、Payoneer 和稳定币属于人工核验方式，二维码跳转、用户截图或前端声明不会自动增加余额。
