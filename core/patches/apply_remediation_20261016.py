"""Idempotent router extension for session-authenticated image playground."""
from pathlib import Path
import sys

def apply(root):
 path=Path(root)/'router/relay-router.go';text=path.read_text(encoding='utf8')
 marker='\tplaygroundRouter := router.Group("/pg")'
 addition='''
	// Browser image requests share the relay's accounting and admission checks.
	browserImages := router.Group("/api/omni/images")
	browserImages.Use(middleware.RouteTag("relay"), middleware.SystemPerformanceCheck())
	browserImages.Use(middleware.UserAuth(), middleware.UserCriticalRateLimit("image-generation"))
	browserImages.Use(middleware.OmniBrowserImageContext(), middleware.ModelRequestRateLimit(), middleware.Distribute())
	browserImages.POST("/generations", func(c *gin.Context) {
		controller.Relay(c, types.RelayFormatOpenAIImage)
	})

'''
 if 'browserImages :=' not in text:
  assert text.count(marker)==1
  path.write_text(text.replace(marker,addition+marker),encoding='utf8')
 path=Path(root)/'controller/relay.go';text=path.read_text(encoding='utf8')
 marker='\t\tdecision := service.DecideRelayRetry(c, newAPIError, common.RetryTimes-retryParam.GetRetry())'
 if 'image_submission_not_retried' not in text:
  assert text.count(marker)==1
  text=text.replace(marker,marker+'\n\t\tif relayFormat == types.RelayFormatOpenAIImage {\n\t\t\tdecision = service.PolicyDecision{Action:"stop", Reason:"image_submission_not_retried", Source:"system"}\n\t\t}')
  path.write_text(text,encoding='utf8')

 path=Path(root)/'relay/channel/openai/adaptor.go';text=path.read_text(encoding='utf8')
 marker='func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {'
 if 'a.omniGeminiImageRequest(c, info, requestBody)' not in text:
  assert text.count(marker)==1
  text=text.replace(marker,marker+'\n\tif response,handled,err := a.omniGeminiImageRequest(c, info, requestBody); handled {return response,err}')
  path.write_text(text,encoding='utf8')
 path=Path(root)/'relay/channel/openai/relay_image.go';text=path.read_text(encoding='utf8')
 marker='\tinfo.UpdateImageCount(openaiImageResponseCount(responseBody))'
 if 'omniValidateImageResponse(info, responseBody)' not in text:
  assert text.count(marker)>=1
  text=text.replace(marker,'\tif err := omniValidateImageResponse(info, responseBody); err != nil {\n\t\treturn nil, types.NewErrorWithStatusCode(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())\n\t}\n\n'+marker,1)
  path.write_text(text,encoding='utf8')

if __name__=='__main__':apply(sys.argv[1])
