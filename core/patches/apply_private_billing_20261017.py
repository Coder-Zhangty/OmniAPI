"""Keep the targeted billing hooks reproducible in future overlay builds."""
from pathlib import Path
import argparse,re

def apply(source,check=False):
 source=Path(source);staged={}
 def transform(name,before,after,marker):
  p=source/name;s=staged.get(p,p.read_text(encoding='utf-8'))
  if marker in s:return
  assert s.count(before)==1,('private billing anchor drift',name)
  staged[p]=s.replace(before,after)
 p=source/'relay/common/relay_info.go';s=p.read_text(encoding='utf-8')
 if 'OmniPrivateCostMultiplier' not in s:
  s,n=re.subn(r'(\bOmniPriceExpression\s+string)',r'OmniPrivateCostMultiplier float64\n\t\1',s,count=1);assert n==1;staged[p]=s
 transform('relay/helper/price.go','\tbillingModelName := info.GetBillingModelName()','\tif err := service.OmniPreparePrivateBilling(info); err != nil { return hosttypes.PriceData{}, err }\n\tbillingModelName := info.GetBillingModelName()','service.OmniPreparePrivateBilling(info)')
 transform('relay/helper/price.go','\tgroupRatioInfo := HandleGroupRatio(c, info)','\tgroupRatioInfo := HandleGroupRatio(c, info)\n\tif info.OmniPrivateCostMultiplier > 0 { groupRatioInfo.GroupRatio = 1 }','if info.OmniPrivateCostMultiplier > 0')
 transform('service/tiered_settle.go','\ttr, err := billingexpr.ComputeTieredQuotaWithRequest(snap, params, requestInput)','\tsnap = omniPrivateSettlementSnapshot(relayInfo, snap)\n\ttr, err := billingexpr.ComputeTieredQuotaWithRequest(snap, params, requestInput)','snap = omniPrivateSettlementSnapshot')
 anchor='func PrepareTieredBillingForSelectedGroup(c *gin.Context, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {'
 transform('service/tiered_settle.go',anchor,anchor+'\n\tif relayInfo.OmniPrivateCostMultiplier > 0 { if _, err := omniPrivateSelectedExpression(relayInfo); err != nil { return types.NewErrorWithStatusCode(errors.New("当前型号计费配置待核验，请联系客服"), types.ErrorCodeModelPriceError, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry()) } }','omniPrivateSelectedExpression(relayInfo)')
 p=source/'service/tiered_settle.go';s=staged.get(p,p.read_text(encoding='utf-8'))
 if '"errors"' not in s:staged[p]=s.replace('import (','import (\n "errors"',1)
 transform('service/log_info_generate.go','\tother.SetPublic("billing_mode", "tiered_expr")','\tother.SetPublic("billing_mode", "tiered_expr")\n\tif relayInfo.OmniPrivateCostMultiplier > 0 { expression, _ := omniPrivateSelectedExpression(relayInfo); other.SetAdmin("private_billing", map[string]any{"basis":"request-frozen selected-route procurement tariff", "multiplier":relayInfo.OmniPrivateCostMultiplier, "expression":expression}); return }','other.SetAdmin("private_billing"')
 if not check:
  for p,s in staged.items():p.write_text(s,encoding='utf-8',newline='\n')
 print(('CHECK' if check else 'APPLIED'),'private billing hooks',len(staged))
if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--source',required=True);p.add_argument('--check',action='store_true');a=p.parse_args();apply(a.source,a.check)
