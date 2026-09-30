# OmniAPI 全栈代码安全审查与修复复验

审查及修复日期：2026-09-27  
当前结论：**CONDITIONAL PASS**

## 结论

白盒审查确认的两个 P1 资金问题已修复：充值现在先持久化 `prepared` 幂等记录，再调用钱包，成功后原子写入 `applied`；同一申请的幂等键固定为 `topup:<request_id>`，状态读取、加额和终态写入均在同一进程锁内。崩溃故障注入和并发确认回归均通过，客户端或管理员输入的交易参考号不能再改变钱包幂等键。

经营者明确要求继续使用东方财富。该项从阻断改为 `ACCEPTED_RISK`：生产响应明确标注“东方财富延迟行情”和 `operator-accepted`，保留逐币种报价时间、周末窗口、过期拒绝及备用汇率提示；适配器新增 HTTPS/允许主机、最终重定向、Content-Type、响应体积、字段数值与完整性校验。这个决定不改变第三方数据授权和可用性风险的归属。

14 项发现中，10 项已修复，1 项由经营者接受风险，1 项部分修复，1 项由 UFW 补偿控制，1 项镜像 SBOM/CVE 扫描仍待验证。详细机器可读记录见 `SECURITY_FINDINGS.json`。

## 已实施修复

| ID | 状态 | 结果 |
|---|---|---|
| OMNI-SEC-001 | FIXED | 钱包写入前持久化幂等状态；JSON 使用 fsync + 原子替换 |
| OMNI-SEC-002 | FIXED | 状态检查移入锁内；幂等键固定为申请 ID |
| OMNI-SEC-003 | ACCEPTED_RISK | 东方财富继续生产使用，增加来源披露、输入校验和失败关闭 |
| OMNI-SEC-004 | FIXED | 管理 POST 精确校验 Origin；写操作使用会话绑定 CSRF token |
| OMNI-SEC-005 | FIXED | Cookie TTL 改为 8 小时；持久化 generation 支持即时撤销 |
| OMNI-SEC-006 | FIXED | public-id 补丁保留 AffCode，并支持重复执行检测 |
| OMNI-SEC-007 | FIXED | runtime 2770；资金文件 0640、服务账号所有 |
| OMNI-SEC-008 | PARTIALLY_FIXED | auth-guard 非 root/只读根/无 capabilities；全部核心容器加资源限制和 no-new-privileges；数据库与 New API 身份仍待兼容验证 |
| OMNI-SEC-009 | FIXED | 上线 CSP、X-Frame-Options、nosniff、Referrer-Policy |
| OMNI-SEC-010 | FIXED | Pillow 完整解码、像素限制、PNG 重编码、拒绝 symlink、原子写入 |
| OMNI-SEC-011 | OPEN_COMPENSATING_CONTROL | 8787 仍监听全接口；UFW 仅放行 Docker 网段并拒绝其余来源，后续迁移 Unix socket |
| OMNI-SEC-012 | FIXED | 日志只保留 HTTP method 与 path，不写 query |
| OMNI-SEC-013 | FIXED | 动态支付文本使用 createElement/textContent |
| OMNI-SEC-014 | NEEDS_VERIFICATION | 固定摘要镜像的 SBOM/CVE 扫描尚未完成 |

完整备份流程也已补齐 `config/`、充值台账、请求/报价事件、支付设置、汇率快照、会话撤销状态和支付二维码资产。部署前创建了受限源码/运行态回滚包，并生成完整 PostgreSQL、New API 数据卷、证书和密钥备份：`/opt/ai-gateway/backups/2026-09-27_09-27-30`。

## 验证证据

- Python 回归：182 项全部通过，原先 6 个 `expectedFailure` 已转为正常安全回归并通过。
- 浏览器 DOM 回归：充值页与管理页两组全部通过；动态支付标题不再进入 `innerHTML`。
- Compose 解析通过；线上 PostgreSQL、Redis、New API、auth-guard、Nginx 全部健康。
- Nginx 配置校验通过，公开 `/healthz` 返回正常。
- 线上错误 Origin 的管理登录请求返回 `403 请求来源校验失败`。
- 首页返回 HSTS、CSP、`X-Frame-Options: DENY`、`X-Content-Type-Options: nosniff` 和 Referrer-Policy。
- 东方财富快照已在线刷新：USD 基准，八种货币齐全，CNY 汇率可用且 `stale=false`；响应包含行情来源和各币种时间。
- 线上文件权限：`runtime` 为 `2770 root:omniapi`，ledger/events/quotes 为 `0640 omniapi-watch:omniapi`。
- auth-guard 为 `65534:65534`、只读根、`cap_drop=ALL`、`no-new-privileges`；Nginx 已丢弃默认 capabilities 并加资源限制。
- 宿主机只公开 22、80、443；数据库、Redis、New API 未发布端口。8787 仍由 UFW 限制为 Docker 网段。
- TLS、Turnstile、真实模型调用和计费数据未因本次修复改动；发布后健康检查中 application/database/redis/containers 全部 PASS。

## 尚未关闭的工作

`OMNI-SEC-014` 需要在可用 Docker 扫描环境中对固定摘要运行 Syft/Trivy 或等价工具，并保存扫描器摘要、漏洞库时间、SBOM 和处置结论。`OMNI-SEC-011` 的最终修复是将 topup-watch 改为 Nginx 可访问的 Unix socket；当前 UFW 规则是有效补偿控制。数据库与 New API 的非 root 化应在独立恢复环境验证卷权限和迁移行为后上线。

本次没有发送真实付款、修改真实用户余额或执行压力测试。完整备份的独立恢复演练以及真实充值确认仍应作为下一次维护窗口的验收项。

