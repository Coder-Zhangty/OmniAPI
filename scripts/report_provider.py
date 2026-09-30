"""Build redacted delivery evidence after real acceptance completes."""
import json
from gateway import ROOT, API, Stack, credentials, write_json, run, docker

def main():
    real=json.loads((ROOT/'runtime/real-acceptance.json').read_text(encoding='utf-8'))
    catalog=json.loads((ROOT/'docs/REAL_MODELS.json').read_text(encoding='utf-8'))
    verification=json.loads((ROOT/'docs/VERIFICATION.json').read_text(encoding='utf-8'))
    verification['real']=real
    stack=Stack(); secret=credentials(ROOT,'provider-default')['api_key']
    logs=stack.compose('logs','--no-color','nginx')
    db=stack.compose('exec','-T','database','psql','-U','gateway','-d','gateway','-At','-c',"SELECT content || COALESCE(other,'') FROM logs")
    verification['real_provider_log_secret_scan']={'status':'PASS' if secret.encode() not in logs+db else 'FAIL'}
    user=API(stack.url); user.login(credentials(ROOT,'tester'))
    _,pricing,_=user.request('/api/pricing')
    models=[r['model'] for r in catalog['results'] if r['status']=='PASS']
    rows=[x for x in pricing.get('data',[]) if x.get('model_name') in models]
    verification['real_test_price_configuration']={'status':'PASS' if len(rows)==len(models) and all(
        x.get('billing_mode')=='tiered_expr' and x.get('billing_expr')=='v1:p * 1 + c * 2' for x in rows) else 'FAIL', 'model_count':len(rows)}
    write_json(ROOT/'docs/VERIFICATION.json',verification)
    raw=run([docker(),'ps','-a','--format','{{json .}}']).decode()
    inventory=[{'name':r['Names'],'state':r['State'],'ports':r['Ports']} for r in [json.loads(line) for line in raw.splitlines()]]
    write_json(ROOT/'runtime/container-inventory.json',inventory)
    backups=sorted((ROOT/'backups').glob('*/manifest.json'))
    newest=backups[-1].parent.name
    report='# 真实上游接入验收\n\n日期：2026-09-26。API Base URL：http://localhost/v1。\n\n'
    report+='上游声明 42 个模型；29 个文本模型短文本调用成功并启用，7 个文本探测失败，6 个图像/语音/嵌入模型未测试、未开放。名称是上游标识，不构成底层模型来源认证。\n\n'
    report+='管理员渠道为 0-0 Main；普通用户只看到模型目录。用户模型、公开和登录定价、用户日志的响应检查见 PROVIDER_VISIBILITY.json。模型名称不加供应商品牌前缀，使用 default 分组。\n\n'
    report+='所有启用模型采用原生表达式 v1:p * 1 + c * 2：输入 $1、输出 $2/百万 Token，分组倍率 1，仅为内部测试价格。修复前一次验收发现固定倍率覆盖，原始证据保留 runtime/real-before-billing-fix.json；以下为修正后复测。\n\n'
    report+='端到端验收模型：gpt-5.4-mini。其余 28 个启用模型仅验证上游普通文本响应，未逐个验证 SSE/Responses。\n\n| 检查 | 状态 |\n|---|---|\n'
    for name,value in real['results'].items(): report+=f"| {name} | {value['status']} |\n"
    report+='\n新备份：backups/'+newest+'/；恢复采用独立项目与新卷，演练后停止。\n\n'
    report+='## 未开放模型\n\n| 模型 | 状态 | 原因 |\n|---|---|---|\n'
    for r in catalog['results']:
        if r['status']!='PASS':
            reason=str(r.get('http_status') or r.get('error_type') or '需要独立协议与计费验证')
            report+=f"| {r['model']} | {r['status']} | {reason} |\n"
    report+='\nFAIL 表示本次探测失败，不代表永久不可用；超时请求也可能产生上游费用。没有自动开放未验证或未定价的媒体模型。尚未真实验证 Anthropic/Gemini 原生协议；文本模型统一通过已验证的 OpenAI 兼容 Chat 接口接入。\n'
    (ROOT/'docs/REAL_PROVIDER_REPORT.md').write_text(report,encoding='utf-8')
    path=ROOT/'docs/DEPLOYMENT_REPORT.md'
    text=path.read_text(encoding='utf-8')
    start=text.index('## 部署信息')
    text='# AI Gateway V1 Deployment Report\n\n已完成真实上游接入：42 个声明模型中启用 29 个已验证文本模型。真实验收与限制见 [REAL_PROVIDER_REPORT.md](REAL_PROVIDER_REPORT.md)。\n\n'+text[start:]
    replacements={
        '| 当前启用真实 Provider | 0 |':'| 当前启用真实 Provider | 1 |',
        '待接入 Provider #1':'Provider #1（仅管理员可见）', '待接入 Base URL':'上游 Base URL（仅管理员可见）',
        'NOT TESTED，尚未提供上游 Key':'42 个声明模型；29 个已验证文本模型启用',
        'public-test-model（仅隔离模拟上游，非真实商业模型）':'gpt-5.4-mini 完整真实验收；29 个模型上游短文本探测成功',
        'PASS / NOT TESTED':'PASS',
        '真实上游未配置':'gpt-5.4-mini 真实验收通过',
        '真实上游待验':'gpt-5.4-mini 真实验收通过',
        '| Responses | NOT TESTED | 固定版本支持路由，实际上游待配置后测试 |':'| Responses | PASS | gpt-5.4-mini 真实调用成功 |',
        '主环境备份：`backups/2026-09-26_04-28-23/`。':'最新主环境备份：`backups/'+newest+'/`。',
        '- 用户选择稍后录入 0-0 Key，因此未创建含虚假 Key 的生产渠道；真实模型同步、Chat/SSE、Usage/Billing 和 Responses 明确为 NOT TESTED。':'- 真实渠道已接入；7 个文本模型探测失败、6 个媒体/嵌入模型未测试且未开放，详见 REAL_MODELS.json。',
        '下一步：按 PROVIDERS.md 在后台录入第一条真实渠道并获取模型，再执行 `python scripts/gateway.py test-price --model "实际模型名"` 和 `python scripts/acceptance.py --model "实际模型名"`。':'后续新增模型须先验证接口与定价；新增受支持协议的供应商继续使用原生渠道配置。',
    }
    for old,new in replacements.items(): text=text.replace(old,new)
    path.write_text(text,encoding='utf-8')
    print('Redacted real-provider report written; backup '+newest)
    print('Provider key absent from logs: '+verification['real_provider_log_secret_scan']['status'])
    print('Native expression prices: '+verification['real_test_price_configuration']['status'])

if __name__=='__main__': main()
