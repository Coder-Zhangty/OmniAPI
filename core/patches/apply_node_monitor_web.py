import json,pathlib,re,shutil,sys
ROOT=pathlib.Path(__file__).resolve().parents[2]
def apply(web):
 overlay=ROOT/'core/overlay/web'
 header=(web/'src/routes/_authenticated/system-info/index.tsx').read_text(encoding='utf-8').split('*/',1)[0]+'*/\n'
 keys=set()
 for source in overlay.rglob('*'):
  if not source.is_file():continue
  dest=web/source.relative_to(overlay);dest.parent.mkdir(parents=True,exist_ok=True)
  text=source.read_text(encoding='utf-8')
  text=re.sub(r'/\*\s*Copyright \(C\) 2023-2026 QuantumNous[\s\S]*?\*/\s*','',text)
  text=header+text
  dest.write_text(text,encoding='utf-8')
  keys.update(re.findall(r"'([^'\n]{2,160})'",text))
 sidebar=web/'src/hooks/use-sidebar-data.ts';text=sidebar.read_text(encoding='utf-8')
 if "url: '/node-monitor'" not in text:
  anchor="title: t('System Info'),"
  text=text.replace(anchor,"title: t('Node Monitor'),\n            url: '/node-monitor',\n            icon: ServerCog,\n            requiredRole: ROLE.SUPER_ADMIN,\n          },\n          {\n            "+anchor,1)
  sidebar.write_text(text,encoding='utf-8')
 translations=json.loads((ROOT/'core/patches/node_monitor_zh.json').read_text(encoding='utf-8'))
 for lang in ['en','zh']:
  path=web/f'src/i18n/locales/{lang}.json';document=json.loads(path.read_text(encoding='utf-8'));target=document.get('translation',document)
  for key,value in translations.items():target[key]=value if lang=='zh' else key
  path.write_text(json.dumps(document,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
if __name__=='__main__':apply(pathlib.Path(sys.argv[1]))
