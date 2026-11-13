"""Apply HA fixes to an attested registry-v5.1 production source tree."""
import argparse
from pathlib import Path

def apply(root):
 file=root/'model/option.go'
 text=file.read_text(encoding='utf-8')
 if 'invalidateBillingPricing := false' not in text:
  anchor='\tcommon.OptionMapRWMutex.Lock()\n\tdefer common.OptionMapRWMutex.Unlock()\n\tcommon.OptionMap[key] = value'
  replacement='''\tcommon.OptionMapRWMutex.Lock()
\tinvalidateBillingPricing := false
\tdefer func() {
\t\tcommon.OptionMapRWMutex.Unlock()
\t\t// Pricing refresh reads Omni options while holding its own cache lock.
\t\t// Never wait for that cache lock while holding the option write lock.
\t\tif invalidateBillingPricing {
\t\t\tInvalidatePricingCache()
\t\t\tratio_setting.InvalidateExposedDataCache()
\t\t}
\t}()
\tcommon.OptionMap[key] = value'''
  assert text.count(anchor)==1,'option lock patch anchor differs from attested source'
  text=text.replace(anchor,replacement)
  text=text.replace('if handleConfigUpdate(key, value) {\n','if handleConfigUpdate(key, value) {\n\t\tinvalidateBillingPricing = strings.HasPrefix(key, "billing_setting.")\n',1)
  old='\t} else if configName == "billing_setting" {\n\t\tInvalidatePricingCache()\n\t\tratio_setting.InvalidateExposedDataCache()\n'
  assert text.count(old)==1,'billing invalidation patch anchor differs'
  text=text.replace(old,'')
  file.write_text(text,encoding='utf-8')
 file=root/'model/omniapi_maintenance.go';text=file.read_text(encoding='utf-8')
 if 'if !common.IsMasterNode' not in text:
  assert text.count('func StartOmniMaintenance() {')==1
  file.write_text(text.replace('func StartOmniMaintenance() {','func StartOmniMaintenance() {\n if !common.IsMasterNode { return }'),encoding='utf-8')

if __name__=='__main__':
 parser=argparse.ArgumentParser();parser.add_argument('source',type=Path);args=parser.parse_args();apply(args.source)
