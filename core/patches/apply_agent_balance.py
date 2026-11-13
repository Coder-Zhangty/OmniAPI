"""Apply after the v5.8 commercial/auth overlays; anchor drift fails closed."""
import argparse
from pathlib import Path

def apply(source,check=False):
 source=Path(source);updates={}
 path=source/'service/billing_session.go';text=path.read_text(encoding='utf-8')
 before='ok && f.reserved > 0';after='ok && f.record != nil && f.record.State == "pending"'
 if before in text:text=text.replace(before,after,1)
 else:assert after in text,'refund anchor drift'
 before='f.flexibleAllowance && f.record != nil && f.record.Source == "omni_subscription"'
 after='f.flexibleAllowance && f.record != nil'
 if before in text:text=text.replace(before,after,1)
 else:assert after in text,'reserve anchor drift'
 updates[path]=text
 path=source/'controller/user.go';text=path.read_text(encoding='utf-8')
 if 'omniAppendWalletBalance(user, data)' not in text:
  start=text.index('func buildSelfUserData(user *model.User) map[string]any {')
  end=text.index('\n}',start)+2
  block=text[start:end];assert block.count('return map[string]any{')==1
  block=block.replace('return map[string]any{','data := map[string]any{',1)
  assert block.endswith('\n\t}\n}')
  block=block[:-2]+'\n\tomniAppendWalletBalance(user, data)\n\treturn data\n}'
  text=text[:start]+block+text[end:]
 updates[path]=text
 if not check:
  for path,text in updates.items():path.write_text(text,encoding='utf-8',newline='\n')
 print(('CHECK' if check else 'APPLIED')+' actual-wallet and self DTO integration')

if __name__=='__main__':
 parser=argparse.ArgumentParser();parser.add_argument('--source',required=True);parser.add_argument('--check',action='store_true');a=parser.parse_args();apply(a.source,a.check)
