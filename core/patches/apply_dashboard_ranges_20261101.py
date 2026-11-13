"""Keep the dashboard static delta in subsequent gateway source builds."""
import argparse,json,pathlib

def apply(root):
    web=root/'web/src'
    edits={}
    p=web/'features/dashboard/constants.ts';s=p.read_text(encoding='utf8')
    old="  { label: '29 Days', days: 29 },"
    new=old+''.join(f"\n  {{ label: '{n} Days', days: {n} }}," for n in [30,90,180,365])
    if new not in s:
        assert s.count(old)==1,'dashboard preset anchor drift'
        edits[p]=s.replace(old,new)
    p=web/'features/dashboard/components/models/models-filter-dialog.tsx';s=p.read_text(encoding='utf8')
    old='grid grid-cols-2 gap-2 sm:flex';new='grid grid-cols-2 gap-2 sm:grid-cols-4'
    if old in s:
        assert s.count(old)==2,'dashboard layout anchor drift'
        edits[p]=s.replace(old,new)
    for p in (web/'i18n/locales').glob('*.json'):
        s=p.read_text(encoding='utf8');data=json.loads(s)['translation'];label=data.get('29 Days')
        if label is None:continue
        old='    "29 Days": '+json.dumps(label,ensure_ascii=False)+','
        additions=''.join('\n    '+json.dumps(f'{n} Days')+': '+json.dumps(label.replace('29',str(n)),ensure_ascii=False)+',' for n in [90,180,365] if f'{n} Days' not in data)
        if additions:
            assert s.count(old)==1,'locale anchor drift: '+p.name
            edits[p]=s.replace(old,old+additions)
    for p,s in edits.items():p.write_text(s,encoding='utf8')
    print('Dashboard source files updated:',len(edits))

if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--source',type=pathlib.Path,required=True)
    apply(parser.parse_args().source)
