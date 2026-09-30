# OmniAPI 轻量客服工单台

## 架构

客服服务继续由 `omniapi-support.service` 独立运行。每天的
`runtime/support/YYYY-MM-DD.jsonl` 是唯一事实来源；每个新事件都有随机
`event_id`，写入时先 append、flush、fsync，再更新
`runtime/support/support-index.sqlite3`。SQLite 使用 WAL，只承担搜索、筛选和分页，
删除或损坏后会从 JSONL 幂等重建。

图片存放在 Web 根目录之外的 `runtime/support-attachments/`。浏览器只能通过鉴权接口
读取；服务端检查 Base64、文件大小、图像解码结果和格式，用 Pillow 重新编码并删除元数据。
只允许 PNG、JPEG 和 WebP；每条消息最多 3 张，每张不超过 5 MiB。

## 状态

- 用户发送消息：`waiting_operator`
- 客服回复：`waiting_user`
- 任一方确认解决：`resolved`
- 解决满 7 天：`closed`
- 关闭 7 天内：用户可重新打开

优先级只能由服务端初判或经营者调整。用户不能把自己的工单标记为紧急。默认队列顺序为
超时（等待客服超过 30 分钟）、高优先级、等待客服时间最长。

## 接口

用户接口前缀为 `/brand/api/support/v2/`，用 New API access token 的
`Authorization: Bearer` 头鉴权。经营者接口前缀为
`/brand/admin/api/support/v2/`，读取要求签名 Cookie，写入还要求同源 Origin 和 CSRF。
令牌不写入 URL、localStorage 或服务日志。

旧 `start/send/history/close` 接口仍可用。v2 包含工单列表、创建、详情、消息、图片上传、
已读、解决和重新打开；经营者另有搜索筛选、回复、内部备注、状态、优先级和运行时设置。
内部备注只存在于管理员详情响应。

## 运行与恢复

Pillow 是附件处理的运行依赖。部署前执行：

```bash
python3 ops/support/support_service.py --check
python3 -m unittest tests.test_support_store tests.test_support_service \
  tests.test_support_notifier tests.test_support_layout tests.test_support_v2
```

隔离重放可复制 `runtime/support/*.jsonl` 到临时目录，然后构造
`ConversationStore`。启动服务会自动补齐缺失的 SQLite 事件。若要强制重建，停止客服服务，
移走 `support-index.sqlite3*`，再启动服务。不得删除 JSONL。

保留期清理默认只预演：

```bash
python3 ops/support/support_service.py --events runtime/support --prune 180
```

核对预演清单后加 `--apply`。应用模式同时删除所属附件并重建索引。

## AI 辅助

当前生产配置保持关闭，页面不会显示 AI 按钮。未来启用时只能生成摘要、分类建议和回复草稿，
不能写入消息或自动发送；附件原图、API Key、验证码和完整付款参考号不得进入模型请求。

