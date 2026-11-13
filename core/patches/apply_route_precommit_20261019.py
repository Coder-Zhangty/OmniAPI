"""Keep heartbeat-finalized errors on their original SSE protocol."""
from pathlib import Path
import sys
def apply(root):
 p=Path(root)/'controller/relay.go';s=p.read_text(encoding='utf-8')
 marker='service.OmniRespondPrecommitStreamError(c, relayFormat, newAPIError)'
 if marker in s:return
 before='\t\t\tnewAPIError.SetMessage(common.MessageWithRequestId(model.OmniUserMessage(newAPIError.Error()), requestId))\n'
 assert s.count(before)==1,'relay error response anchor drift'
 s=s.replace(before,before+'\t\t\tif '+marker+' { return }\n')
 p.write_text(s,encoding='utf-8',newline='\n')
if __name__=='__main__':apply(sys.argv[1])
