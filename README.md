# OmniAPI · AI Gateway

[简体中文](#简体中文) · [English](#english)

## 简体中文

OmniAPI 是一个可自行部署的模型 API 网关。它以 New API 为基础，在统一入口处理模型路由、账号、额度和用量，并提供模型目录、控制台和客服界面。

- **网关服务**：Docker Compose 编排 New API、PostgreSQL、Redis、Nginx 和登录保护服务。
- **网页界面**：`nginx/brand/` 提供控制台扩展、图像工作台、账号和客服页面。
- **扩展源码**：`core/overlay/` 和 `core/patches/` 包含针对 New API 的扩展和补丁。客户端辅助工具位于 `clients/`。

### 本地启动

需要 Python 3.10+ 和运行中的 Docker Compose。请在项目根目录执行：

```bash
python scripts/gateway.py init
python scripts/gateway.py lock-images
python scripts/gateway.py start
python scripts/gateway.py bootstrap
python scripts/gateway.py healthcheck
```

`init` 会生成本地环境配置和管理员凭据。默认通过 `http://127.0.0.1` 访问。首次登录的凭据保存在 `.secrets/admin.json`。使用管理控制台配置模型供应商，测试时可使用 `python scripts/acceptance.py --mock`。

该流程启动本地基础网关。扩展的支付、客服、同步与归档功能依赖对应服务和部署配置。部署前按需配置自己的域名、供应商和商户信息。

## English

OmniAPI is a self-hosted model API gateway built on New API. It gives clients a single endpoint for model routing and provides an account console, quota and usage tracking, a model directory and support pages.

- **Gateway services** — Docker Compose runs New API, PostgreSQL, Redis, Nginx and a login protection service.
- **Web interface** — `nginx/brand/` contains the console extensions, image studio, account and support pages.
- **Extensions** — `core/overlay/` and `core/patches/` contain additions and patches for New API. Client utilities are under `clients/`.

### Start locally

Use Python 3.10+ and a running Docker Compose installation. Run the commands above from the repository root. The initializer generates local configuration and administrator credentials in `.secrets/admin.json`. Open `http://127.0.0.1`, add providers through the admin console and use `python scripts/acceptance.py --mock` for mock-provider testing.

This starts the base gateway. Payments, support, sync and archiving require their corresponding services and deployment settings. Configure your own domain, providers and merchant details before deploying those integrations.

## 上游与许可 / Upstream and license

网关基于 [QuantumNous/New API](https://github.com/QuantumNous/new-api/tree/v0.13.2)。其源码和派生修改使用 [AGPL-3.0](core/LICENSE.new-api)。

The gateway builds on [QuantumNous/New API](https://github.com/QuantumNous/new-api/tree/v0.13.2). Its source and derivative modifications are distributed under the [AGPL-3.0](core/LICENSE.new-api).
