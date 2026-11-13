"""Wire wallet discount endpoints and keep private rules out of generic options."""
from pathlib import Path
import sys
def apply(root):
 root=Path(root)
 p=root/'router/api-router.go';s=p.read_text(encoding='utf-8')
 marker='omni.GET("/admin/users/:id/discount"'
 if marker not in s:
  anchor='\t\tomni.GET("/admin/orders", middleware.AdminAuth(), controller.OmniOrders)'
  assert s.count(anchor)==1,'discount route anchor drift'
  s=s.replace(anchor,anchor+'\n\t\tomni.GET("/admin/users/:id/discount", middleware.RootAuth(), controller.OmniUserDiscountGet)\n\t\tomni.PUT("/admin/users/:id/discount", middleware.RootAuth(), controller.OmniUserDiscountPut)')
  p.write_text(s,encoding='utf-8',newline='\n')
 p=root/'controller/option.go';s=p.read_text(encoding='utf-8')
 if 'k == "OmniAPIUserDiscounts"' not in s:
  anchor='\tfor k, v := range common.OptionMap {\n'
  assert s.count(anchor)==1,'discount option read anchor drift'
  s=s.replace(anchor,anchor+'\t\tif k == "OmniAPIUserDiscounts" { continue }\n')
 if 'option.Key == "OmniAPIUserDiscounts"' not in s:
  anchor='\tswitch option.Value.(type) {'
  assert s.count(anchor)==1,'discount option write anchor drift'
  s=s.replace(anchor,'\tif option.Key == "OmniAPIUserDiscounts" { c.JSON(http.StatusForbidden, gin.H{"success":false,"message":"请使用用户专属优惠接口"}); return }\n'+anchor)
 p.write_text(s,encoding='utf-8',newline='\n')
if __name__=='__main__':apply(sys.argv[1])
