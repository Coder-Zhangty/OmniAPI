"""Add the bounded inline-to-multipart supplier adapter; safe to reapply."""
from pathlib import Path
def apply(root):
 p=Path(root)/'relay/channel/openai/adaptor.go';s=p.read_text(encoding='utf8');marker='if response,handled,err := a.omniGeminiImageRequest(c, info, requestBody); handled {return response,err}'
 # Baseline may already be gofmt formatted.
 if 'a.omniInlineImageEdit(c, info, requestBody)' not in s:
  if marker in s:s=s.replace(marker,marker+'\n\tif response,handled,err := a.omniInlineImageEdit(c, info, requestBody); handled {return response,err}')
  else:
   anchor='func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {'
   assert s.count(anchor)==1
   # Gemini must get its native JSON first, before the generic edit adapter.
   end=s.index('\n\tif info.RelayMode',s.index(anchor));s=s[:end]+'\n\tif response,handled,err := a.omniInlineImageEdit(c, info, requestBody); handled {return response,err}'+s[end:]
  p.write_text(s,encoding='utf8')
