# 固定版本源码核查

核查时间：2026-09-25。部署固定 v0.13.2，不把 main 分支新行为套到旧版本。

| 事项 | 官方依据与结论 |
|---|---|
| 发布 | https://github.com/QuantumNous/new-api/releases?page=5 ：v0.13.2 非 RC 基线；Latest 当前指向 v1.0.0-rc.40 |
| 镜像、数据库 | https://github.com/QuantumNous/new-api/blob/v0.13.2/docker-compose.yml ：calciumion/new-api，默认 PostgreSQL 15，可切 MySQL |
| 环境变量 | https://github.com/QuantumNous/new-api/blob/v0.13.2/common/init.go ：SESSION_SECRET、CRYPTO_SECRET、STREAMING_TIMEOUT、DEBUG、DIFY_DEBUG 等 |
| 初始化 | https://github.com/QuantumNous/new-api/blob/v0.13.2/controller/setup.go ：GET/POST /api/setup，password 与 confirmPassword |
| 管理认证 | https://github.com/QuantumNous/new-api/blob/v0.13.2/middleware/auth.go ：Session + New-Api-User；API Token 另行认证 |
| 渠道抽象 | https://github.com/QuantumNous/new-api/blob/v0.13.2/model/channel.go ：类型、Key、BaseURL、Models、Group、ModelMapping、Priority、Weight |
| 路由 | https://github.com/QuantumNous/new-api/blob/v0.13.2/model/ability.go ：group+model+channel 关系，priority 降序，数据库路径权重偏移 +10 |
| 重试 | https://github.com/QuantumNous/new-api/blob/v0.13.2/controller/relay.go ：RetryTimes、状态码及 skip-retry/affinity 条件 |
| 协议 | https://github.com/QuantumNous/new-api/blob/v0.13.2/router/relay-router.go ：chat、responses、messages、Gemini 原生路由 |
| Token | https://github.com/QuantumNous/new-api/blob/v0.13.2/controller/token.go ：有限额 Token；列表 Key 脱敏，显式 POST /:id/key 获取完整 Key |
| 额度 | https://github.com/QuantumNous/new-api/blob/v0.13.2/common/constants.go ：默认 500000 quota / USD |
| 用户额度 | https://github.com/QuantumNous/new-api/blob/v0.13.2/controller/user.go ：manage add_quota 的模式是 add/subtract/override，不是 set |
| 选项 | https://github.com/QuantumNous/new-api/blob/v0.13.2/model/option.go ：倍率、RetryTimes、DataExportEnabled 等持久化 |
| 日志 | https://github.com/QuantumNous/new-api/blob/v0.13.2/model/log.go ：UserId、TokenId、ChannelId、RequestId、PromptTokens、CompletionTokens、Quota、Content、Other |
| 请求关联 | https://github.com/QuantumNous/new-api/blob/v0.13.2/middleware/request-id.go ：应用生成响应请求 ID，不盲信客户端输入 |

## 与原始假设的差异

- 优先级越大越优先，不是 1 优先于 2。
- 同优先级权重不直接等价于填写数字的百分比；零权重不是禁用。
- latest 发布标签含 RC；生产版本不能依赖该标签。
- 已有 Responses/Claude/Gemini 路由，但不能由路由存在推导某个上游支持所有协议。
- DataExportEnabled 是用量导出开关，不是通用正文日志开关；错误消息仍要实测扫描。
- 当前本机选择 HTTP 回环；HTTPS 配置是可选迁移模板，不能在报告中标为已部署公网 HTTPS。
- 实测新建渠道立即调用出现 503，而模型目录已可见。`main.go` 在 Redis 启用时强制内存缓存，`BatchInsertChannels` 不立即刷新缓存。明确启用缓存并设置 SYNC_FREQUENCY=10，测试通过原生 `/api/channel/fix` 刷新；无需修改源码。
- 实测管理员 override 额度只更新 SQL，用户 Redis 余额可能仍为零。初始化随后通过原生 manage/enable 重新应用测试用户启用状态，触发官方缓存失效逻辑，避免首次请求误报余额不足。
- GORM 默认错误日志可能输出 Token 查询条件，即使 DEBUG=false。没有通过修改核心解决；采用 Docker logging driver none 防止原始控制台日志持久化，保留数据库业务日志和 Nginx 状态日志。
- 额度缓存更新是异步操作，即使 BATCH_UPDATE_ENABLED=false，HTTP 返回瞬间读取余额也可能尚未收敛。验收等待消费日志及最多 5 秒余额收敛后核对，不通过修改账本掩盖差异。
- `setting/operation_setting/status_code_ranges.go` 固定跳过 504/524；429、500、503 和连接中断可在原生重试流程进入备用渠道。
