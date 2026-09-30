# 真实上游接入验收

日期：2026-09-26。API Base URL：http://localhost/v1。

上游声明 42 个模型；29 个文本模型短文本调用成功并启用，7 个文本探测失败，6 个图像/语音/嵌入模型未测试、未开放。名称是上游标识，不构成底层模型来源认证。

管理员渠道为 0-0 Main；普通用户只看到模型目录。用户模型、公开和登录定价、用户日志的响应检查见 PROVIDER_VISIBILITY.json。模型名称不加供应商品牌前缀，使用 default 分组。

所有启用模型采用原生表达式 v1:p * 1 + c * 2：输入 $1、输出 $2/百万 Token，分组倍率 1，仅为内部测试价格。修复前一次验收发现固定倍率覆盖，原始证据保留 runtime/real-before-billing-fix.json；以下为修正后复测。

端到端验收模型：gpt-5.4-mini。其余 28 个启用模型仅验证上游普通文本响应，未逐个验证 SSE/Responses。

| 检查 | 状态 |
|---|---|
| web_ui | PASS |
| admin_login | PASS |
| model_catalog | PASS |
| invalid_token | PASS |
| chat_completions | PASS |
| usage_and_channel | PASS |
| billing | PASS |
| streaming | PASS |
| stream_billing | PASS |
| disabled_token | PASS |
| deleted_token | PASS |
| responses | PASS |
| log_secret_scan | PASS |
| prompt_body_scan | PASS |
| database_log_secret_scan | PASS |
| database_prompt_scan | PASS |
| user_cannot_read_channels | PASS |
| raw_app_logs_not_persisted | PASS |
| port_isolation | PASS |
| persistence | PASS |
| restore | PASS |
| restored_user_login | PASS |

新备份：backups/2026-09-26_10-12-49/；恢复采用独立项目与新卷，演练后停止。

## 未开放模型

| 模型 | 状态 | 原因 |
|---|---|---|
| claude-fable-5-1 | FAIL | 403 |
| codex-auto-review | FAIL | TimeoutError |
| gemini-2.5-flash-tts | NOT TESTED | 需要独立协议与计费验证 |
| gemini-3-pro-image | NOT TESTED | 需要独立协议与计费验证 |
| gemini-3.1-flash-image | NOT TESTED | 需要独立协议与计费验证 |
| gemini-3.1-pro | FAIL | TimeoutError |
| gemini-3.8-flash | FAIL | TimeoutError |
| gemini-embedding-001 | NOT TESTED | 需要独立协议与计费验证 |
| gpt-image-2 | NOT TESTED | 需要独立协议与计费验证 |
| gpt-image-2.5 | NOT TESTED | 需要独立协议与计费验证 |
| jev-1.13.0 | FAIL | 404 |
| jev-latest | FAIL | 404 |
| jev-preview | FAIL | 404 |

FAIL 表示本次探测失败，不代表永久不可用；超时请求也可能产生上游费用。没有自动开放未验证或未定价的媒体模型。尚未真实验证 Anthropic/Gemini 原生协议；文本模型统一通过已验证的 OpenAI 兼容 Chat 接口接入。
