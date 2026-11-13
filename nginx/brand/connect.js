(() => {
  'use strict';
  const client = document.getElementById('client'), model = document.getElementById('connect-model');
  const defaults = {claude:'claude-opus-5-5','cc-switch':'claude-opus-5-5',codex:'gpt-5.6-sol',gemini:'gemini-3.8-flash',curl:'gpt-5.6-sol',mcp:'gpt-image-2'};
  const quote = value => JSON.stringify(value);
  function render() {
    const name = model.value.trim();
    if (!/^[A-Za-z0-9_.-]{1,128}$/.test(name)) { document.getElementById('configuration').textContent = '请输入模型广场中的完整型号。'; return; }
    let text = '', hint = '';
    switch (client.value) {
      case 'claude':
        hint = '合并到 Claude Code 的用户 settings.json。Anthropic 基址填写域名，客户端自行拼接 /v1/messages。';
        text = JSON.stringify({env:{ANTHROPIC_BASE_URL:'https://omniapi.biz',ANTHROPIC_AUTH_TOKEN:'YOUR_OMNIAPI_TOKEN',ANTHROPIC_MODEL:name}},null,2); break;
      case 'cc-switch':
        hint = '在 CC Switch 新增 Claude 提供商，按下面的三个字段填写；不要重复追加 /v1。';
        text = '名称：OmniAPI\nAPI 地址：https://omniapi.biz\nAPI Key：YOUR_OMNIAPI_TOKEN\n模型：' + name; break;
      case 'codex':
        hint = '合并到用户目录 .codex/config.toml，并在启动 Codex 的环境中设置 OMNI_API_KEY 为自己的令牌。';
        text = 'model_provider = "omniapi"\nmodel = ' + quote(name) + '\n\n[model_providers.omniapi]\nname = "OmniAPI"\nbase_url = "https://omniapi.biz/v1"\nwire_api = "responses"\nenv_key = "OMNI_API_KEY"\nsupports_websockets = false'; break;
      case 'gemini':
        hint = '在启动 Gemini CLI 的终端环境中设置以下变量；使用 API Key 方式登录。';
        text = 'GOOGLE_GEMINI_BASE_URL=https://omniapi.biz\nGEMINI_API_KEY=YOUR_OMNIAPI_TOKEN\nGEMINI_MODEL=' + name; break;
      case 'curl':
        hint = '这是 Bash / WSL 示例。先设置环境变量 OMNI_API_KEY，再发送最小 Responses 请求。';
        text = 'curl https://omniapi.biz/v1/responses \\\n  -H "Authorization: Bearer $OMNI_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d ' + "'" + JSON.stringify({model:name,input:'Reply with OK.',max_output_tokens:128}) + "'"; break;
      case 'mcp':
        hint = '下载并解压 MCP 到 C:/OmniAPI-Images，需要 Python 3.10+。将下面对象加入 CC Switch 的 MCP 配置；不改变主对话模型。';
        text = JSON.stringify({mcpServers:{'omniapi-images':{command:'py',args:['-3','C:/OmniAPI-Images/omni_images_mcp.py'],env:{OMNI_API_KEY:'YOUR_OMNIAPI_TOKEN',OMNI_BASE_URL:'https://omniapi.biz/v1',OMNI_IMAGE_OUTPUT_DIR:'C:/OmniAPI-Images/output'}}}},null,2); break;
    }
    document.getElementById('instruction').textContent = hint;
    document.getElementById('configuration').textContent = text;
    document.getElementById('copy-status').textContent = '';
  }
  client.addEventListener('change', () => { model.value = defaults[client.value]; model.disabled = client.value === 'mcp'; render(); });
  model.addEventListener('input', render);
  document.getElementById('copy').addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(document.getElementById('configuration').textContent); document.getElementById('copy-status').textContent = '已复制，记得替换令牌占位符。'; }
    catch (_) { document.getElementById('copy-status').textContent = '请选中上面的配置手动复制。'; }
  });
  render();
})();
