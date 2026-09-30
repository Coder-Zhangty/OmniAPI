# 客服即时通知

OmniAPI 首选企业微信群机器人通知。用户创建工单、继续追问或提交充值申请后，机器人把摘要和对应工作台深链推送到经营者手机；回复和充值核验仍在 OmniAPI 后台完成。

## 启用企业微信

1. 在企业微信中创建内部群，添加群机器人并复制 Webhook。
2. 登录服务器后执行：

   ```sh
   cd /opt/ai-gateway
   sudo python3 scripts/configure_support_notify.py --channel wecom
   ```

3. 在隐藏输入提示中粘贴 Webhook。工具会先发送测试通知，成功后才启用。
4. 打开 `https://omniapi.biz/brand/support-admin.html`，在“设置”中确认“企业微信通知已启用”。

Webhook 只保存在 `.secrets/support-notify.json`，权限为 `0600`，不会进入浏览器、Git、命令参数或客服事件日志。连续短消息按工单合并，默认一分钟最多推送一次；高优先级关键词可立即推送。

充值通知只包含订单号、用户、付款方式和申报金额，不包含付款证明、完整交易参考号、钱包地址或任何密钥。通知只表示用户声明已经付款，不能触发自动入账；经营者必须在收款渠道确认实际净到账后再核验。

停用通知：

```sh
sudo python3 scripts/configure_support_notify.py --channel none
```

个人微信没有官方通用 Webhook。QQ 官方机器人需要 AppID、AppSecret、目标会话标识以及开放平台审核，适合在完成机器人审核后作为第二通道接入。
