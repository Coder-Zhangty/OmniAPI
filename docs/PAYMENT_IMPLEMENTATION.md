# OmniAPI 充值系统运维说明

更新日期：2026-09-27

## 当前开放方式

- 人民币聚合收款码：CNY，人工核验。
- Payoneer：USD，最低 $20，人工核验实际净到账。
- USDT / USDC：代码和管理配置已交付；只有资产、网络、地址、二维码、最低金额全部配置后才会公开。

公开充值页为 `https://omniapi.biz/brand/topup.html`，管理页为 `https://omniapi.biz/brand/admin.html`。管理接口使用 New API 超级管理员身份校验，未登录请求返回 401。

## Xe 凭据

复制 `config/xe.example.json` 到服务器 `/opt/ai-gateway/.secrets/xe.json`，填写 Xe Account ID 与 API Key，并设置为仅 root 可读。systemd 使用 `LoadCredential` 向非特权服务提供临时只读副本：

```bash
chown root:root /opt/ai-gateway/.secrets/xe.json
chmod 600 /opt/ai-gateway/.secrets/xe.json
```

然后先探测行情并确认八种货币齐全，再将 `config/topup.json` 的 `fx.enabled` 改为 `true`，重启 `omniapi-topup-watch.service`。启用前必须确认 `/brand/api/fx/rates` 返回 `available=true`、`stale=false` 和供应商时间戳。若探测失败，保持关闭。

## 稳定币设置

管理员在支付方式设置中逐项填写资产、网络、钱包地址、二维码、最低金额、排序和公开说明。上传只接受 PNG、JPEG、WebP，最大 1 MiB。地址、网络或二维码任一缺失时，该方式不会出现在公开接口或充值页。每条链应单独配置，避免一个地址被错误标成多个网络。

## 核验入账

管理员打开待处理订单，核对付款参考号或链上交易哈希（用户可能没填），填写实际净到账金额，再执行确认。Payoneer 和稳定币的净到账单位为 USD；人民币输入实际净到账 CNY，服务端使用订单锁价折算。平台不补贴支付手续费、链上损耗或汇差。

**交易参考号是选填的**。强制填写只把成本推给用户（付款还没到账时手上往往没有参考号，填不出来就直接放弃提交），而每一笔额度都由经营者对着自己的收款流水人工确认，风险没有真正转移。代价是：没填参考号时，对账只能靠「金额 + 时间 + 账号」人工比对，所以**同一时间多笔同额订单的错认风险由经营者承担**。因此唯一金额尾数（每单分配互不相同的分位尾数）仍是应做的加固。

填了的参考号全局唯一（大小写不敏感），同一个外部流水不可能支撑两笔申请。每个订单只能进入一次终态，额度台账也做幂等检查。拒绝订单时不入账。

## EPay 与未来自动支付

`scripts/configure_epay.py` 保留给未来合规的支付宝／微信商户网关。当前没有商户网关、PID 和 KEY，因此 EPay 自动回调没有启用。收款码本身不能提供签名回调，也不能作为自动入账依据。

原生套餐 EPay 回调仍由 Nginx 返回 404，避免 rc.40 在 CNY 展示模式下按错误金额收款。取得商户主体和真实沙箱后，应先修复并验证金额绑定、签名、幂等和退款，再解除该拦截。

## 常用检查

```bash
cd /opt/ai-gateway
python3 scripts/verify_payment_upgrade.py
systemctl status omniapi-topup-watch.service
docker compose -f docker-compose.yml -f compose.https.yml ps
docker compose -f docker-compose.yml -f compose.https.yml exec -T nginx nginx -t
```

实现与验收状态见 [PAYMENT_FX_REPORT.md](PAYMENT_FX_REPORT.md)。
