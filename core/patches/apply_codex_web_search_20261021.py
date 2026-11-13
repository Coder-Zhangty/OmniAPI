from pathlib import Path
import sys
def apply(source):
 path=Path(source)/'service/text_quota.go';text=path.read_text(encoding='utf-8')
 old='for name, tool := range relayInfo.ResponsesUsageInfo.BuiltInTools {'
 new=old+'\n\t\t\tif OmniTokenPricedWebSearch(relayInfo, name) { continue }'
 if 'if OmniTokenPricedWebSearch(relayInfo, name)' not in text:
  assert text.count(old)==1,'search billing seam changed';text=text.replace(old,new,1);path.write_text(text,encoding='utf-8',newline='\n')
if __name__=='__main__':apply(sys.argv[1])
