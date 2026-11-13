# OmniAPI 图片 MCP 1.2.1

不想安装工具，可直接登录 [网页生图台](https://omniapi.biz/brand/image-studio.html)。

本工具使用自己的 OmniAPI 令牌，把真实 PNG 保存到本地。主对话模型保持原配置。

1. 安装 Python 3.10+，解压到 `C:/OmniAPI-Images`。
2. 在 CC Switch 的 MCP 中添加下方配置，替换令牌占位符，启用到目标客户端。
3. 重启客户端，先调用 `omni_list_image_models` 查看当前型号、尺寸、品质和价格，再调用 `omni_generate_image`。

```json
{
  "mcpServers": {
    "omniapi-images": {
      "command": "py",
      "args": ["-3", "C:/OmniAPI-Images/omni_images_mcp.py"],
      "env": {
        "OMNI_API_KEY": "YOUR_OMNIAPI_TOKEN",
        "OMNI_BASE_URL": "https://omniapi.biz/v1",
        "OMNI_IMAGE_OUTPUT_DIR": "C:/OmniAPI-Images/output"
      }
    }
  }
}
```

仅有 `python` 命令时，command 改为 `python` 并删除 args 中的 `-3`。macOS/Linux 可使用 `python3`，同时改为自己的实际脚本与输出目录。

Codex 用户也可以合并以下内容到用户目录的 `.codex/config.toml`：

```toml
[mcp_servers.omniapi-images]
command = "py"
args = ["-3", "C:/OmniAPI-Images/omni_images_mcp.py"]
startup_timeout_sec = 20
tool_timeout_sec = 720

[mcp_servers.omniapi-images.env]
OMNI_API_KEY = "YOUR_OMNIAPI_TOKEN"
OMNI_BASE_URL = "https://omniapi.biz/v1"
OMNI_IMAGE_OUTPUT_DIR = "C:/OmniAPI-Images/output"
```

配置字段参照 [OpenAI 官方 MCP 文档](https://developers.openai.com/codex/mcp)。其他客户端的工具等待时间建议设为至少 660 秒。

## 调用示例

> 先调用 omni_list_image_models 查看可用型号和规格，然后用 omni_generate_image 生成一张蓝色陶瓷碗的图片，文件名 bowl.png。使用我选择的型号与规格，不要自动重复生成。

工具参数：`prompt` 必填；`model`、`size`、`quality`、`filename` 可选。`service_tier`、`speed` 仅允许模型目录明确开放的值。

旧配置不需要修改：未指定型号时仍使用 `gpt-image-2`，未填写尺寸或品质时使用实时目录默认值。目前该型号已统一到 API 快连，已验收尺寸为 **1254×1254**，默认 low；显式指定 1024×1024 的请求会被拒绝，不会改尺寸或切回旧线路。`default` 品质表示供应商默认；它与 low/medium/high 的含义不同。

每次生成一张图片，不覆盖同名文件。返回值包含实际像素、所选型号、品质、本地路径与请求编号。工具在保存前核验 PNG 完整性和尺寸。

## 错误处理

- **401**：核对自己的 OmniAPI 令牌、有效期和权限。
- **规格未开放**：重新调用型号目录，使用列出的精确组合。
- **超时或连接断开**：工具不会自动重试。先查看官网调用记录，携带请求编号联系站内客服；不要连续提交。
- **尺寸不符或图片不完整**：不会保存为成功图片，请保留请求编号。
- **找不到工具**：核对 Python 命令、脚本绝对路径、客户端是否已启用 MCP，并完全重启客户端。

公开教程和配置中只使用占位符；不要填写供应商密钥。原生内置生图工具与此独立 MCP 的支持条件不同。
