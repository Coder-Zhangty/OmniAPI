# 模型、测试价格与额度

## 模型接入

已获取上游声明的 42 个模型，29 个文本模型通过短文本探测并启用。7 个探测失败、6 个非文本模型未开放；明细见 REAL_MODELS.json。
后台管理员获取上游目录使用原生渠道操作；用户的 `/v1/models` 是 Gateway 根据其可访问分组整理的目录，两者不是同一个权限视图。
首次 Public Model = Upstream Model。后续别名通过每个渠道的 model_mapping 实现，不将模型和供应商编码绑定。

## 测试价格

执行 `python scripts/gateway.py test-price --model "实际模型名"`。
此命令显式修改该模型的计费，保留其他模型配置，移除该模型的按次定价，分组 default 设为 1。

| 项目 | 配置 |
|---|---|
| 输入单价 | 1 USD / 1M tokens（仅测试） |
| 输出单价 | 2 USD / 1M tokens（仅测试） |
| QuotaPerUnit | 500000 / USD |
| ModelRatio | 0.5 |
| CompletionRatio | 2 |
| GroupRatio.default | 1 |

在 500000 quota/USD 且表达式为 `v1:p * 1 + c * 2` 时，普通无缓存文本请求期望扣额为 `round_half_up(input_tokens × 0.5 + output_tokens)`。

验收脚本不再写死这个式子：它读取线上 `billing_setting.billing_expr`，按 `round_half_up(tokens × 系数 / 1e6 × QuotaPerUnit)` 计算期望值。因此切换到商业单价后 `billing` / `stream_billing` 仍然有效；表达式缺失、或含缓存/工具等附加项而不再是纯输入输出形状时会直接判 FAIL 并给出原因，不会用一个近似值掩盖计价差异。
Usage→日志 quota→用户余额差→Token 余额差应相符。响应与日志 input/output token 应一致；流式请求要求最终 usage，缺失不能标记准确计费通过。
缓存、推理、音频、图片、工具及表达式计费可能引入额外计费项。验收先使用普通文本无工具请求；真实模型若含额外项，必须按该版本公式核算并记录，不能忽略差异。
测试价格不代表 0-0 套餐成本或可盈利售价。没有价格的模型不自动开放；不启用“未设置价格仍可调用”。

## Key 与用户

bootstrap 创建独立测试用户，管理员人工方式设置初始余额，用户拥有独立有限额 Token。后续可在原生 Token 页面创建、修改额度、禁用及删除。
额度与上游账单不是同一个余额。已开放普通用户账号密码注册，初始余额为 0，不配置支付系统。
上游 Key 只存于渠道后台数据库；`.secrets/test-token.json` 仅存用户 Gateway Key。

## 用量日志与隐私

保留 Request ID、User、Token ID、Model、Channel、Usage、Quota、Latency、Created At。Nginx 记录 HTTP 状态并关联 `X-Oneapi-Request-Id`。
Nginx 不记录 Authorization、查询参数、请求/响应正文；New API DEBUG、DIFY_DEBUG 保持关闭。
固定版本日志有 `Content`、`Other`，错误日志可能含上游错误文本；关闭调试并不能证明所有路径绝无正文。验收扫描测试 Prompt 和已知本地秘密，只输出是否命中；还需管理员本机核查渠道错误回显。
**`DataExportEnabled` 必须保持开启**。它只控制 `quota_data` 这张按小时聚合的统计表（字段仅 user_id / username / model_name / 小时 / 分组 / token_id / channel_id / node_name / token_used / count / quota），**不写任何请求或响应正文**，也不是通用 Prompt 日志总开关。曾经因为误以为它是正文开关而关掉，结果 259 条消费日志在库里、而管理员端和用户端的图表全空——排查过程见 `docs/TROUBLESHOOTING.md`「用量图表全是空的」。真正的正文风险来自 `logs.Content` / `logs.Other`（错误日志），与这个开关无关。

实测固定版本在 DEBUG=false 时，GORM 查询失败日志仍输出带 Token 条件的 SQL，未发现可用的 GORM 日志环境开关。因此默认将 **new-api 的 Docker logging driver 设为 none**，不持久化原始控制台输出。其他容器仍使用限额轮转日志，用量和错误审计仍保存在 PostgreSQL。排查使用健康检查、Nginx 状态及后台用量日志；临时启用控制台日志必须按含凭据数据保护。上游错误正文仍可能含用户内容，真实上游接入后需复核数据库日志。

## 真实接入后的计费修正

v0.13.2 对部分 GPT/Claude 名称锁定输出倍率，单独设置 CompletionRatio 不会覆盖。当前启用模型使用原生 billing_setting 的 tiered_expr，表达式为 v1:p * 1 + c * 2，系数单位为美元/百万 Token。保留原模型名，不修改核心源码。缓存等未单列类别归入输入/输出计费；测试价格不代表真实上游成本。
