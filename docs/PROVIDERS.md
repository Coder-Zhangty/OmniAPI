# Provider / Channel

## 第一条真实渠道

登录后台 → 渠道 → 添加渠道：名称 `0-0 Main`，类型 OpenAI，Base URL `https://api.0-0.pro`，分组 `default`，优先级 30，权重 10。
Key 由管理员直接填写。不要给 Base URL 重复追加 `/v1`；路径由原生适配器构造。
用后台获取模型功能查询 Key 对应的 `/v1/models`，保留目录结果，再选择一个文本模型保存并测试。目录不保证每个模型都能实际调用。
不要创建虚构的 Provider B 作为生产备用，不自动启用全部模型，不启用自动价格同步覆盖测试价格。保存新渠道后等待最多 10 秒缓存同步，再执行调用；后台原生修复渠道能力也可立即刷新。

## 增加第二个 Provider

后台 Add Channel，选择受支持的协议适配器，填写其独立地址和凭据，加入 `default` 分组，填入公共模型名。
如果 Provider B 的上游名不同，设置渠道模型映射：

```json
{"model-x": "provider/model-x"}
```

Public Model 为 `model-x`。渠道 A、B 可以同时提供它；用户不需更换 Gateway Key 或 Base URL。
已有原生适配器支持的供应商不需改源码；全新私有协议可能需要官方新增适配器，这不是现有多渠道架构需要重建。

## 选择与故障处理

- 优先级越大越优先，例如 A=30、B=20、C=10。同优先级按权重选择。
- `weight=0` 不代表禁用；禁用使用渠道状态。固定版本数据库选择路径使用 weight+10，不能把 UI 数值直接视为严格流量比例。
- 当前 `RetryTimes=0`；拥有两个或更多真实渠道后在系统设置将重试次数设为 2，并按实际需求设置 `AutomaticRetryStatusCodes`（例如 `429,500-599`）。
- 原生实现还检查 affinity、显式渠道指定、skip-retry 标记及错误类型；并非所有失败都进入下一渠道。
- 固定版本明确跳过 HTTP 504、524 及 bad_response_body 错误，即使状态码范围包含这些值也不会自动重试。不要把 5xx 配置理解为所有 5xx 均能切换。
- 启用自动禁用/恢复前单独验证阈值及恢复行为。MVP 不自动禁用唯一渠道，避免误触后完全失去服务。
- 不对已输出部分内容的 SSE 承诺透明切换；客户端中断、上游超时和重复生成可能造成上游成本，网关失败退款也不代表上游没有收费。

## 协议能力

| 下游接口 | 固定版本实现 | V1 验收 |
|---|---|---|
| GET /v1/models | Token 认证及模型目录 | 必测 |
| POST /v1/chat/completions | OpenAI relay | 普通与 SSE 必测 |
| POST /v1/responses | Responses relay | 实际上游支持时测试；失败如实报告 |
| POST /v1/messages | Claude relay | 记录接入方案 |
| POST /v1beta/models/* | Gemini relay | 记录接入方案 |

路由存在不等于任意渠道能执行所有跨协议转换。Anthropic/Gemini 采用对应原生渠道类型和其真实凭据；本阶段不修改适配器。

## 扩展验收

`python scripts/acceptance.py --mock --recovery` 创建独立项目，两个 Python 模拟上游只在 Docker 网络可访问，Gateway 绑定 127.0.0.1:18081。
测试公共模型到 upstream-a/upstream-b 的映射、优先级、429/500/503/断连重试，结束后停止容器并保留卷用于排查。
此结果必须标注 synthetic，不能宣称真实 Provider B 验收通过。模拟程序不是 Gateway 实现，也不会部署到正式四服务环境。
