package relay

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

// PrepareRequestBilling estimates and reserves one request's charge. Transports
// provide the current request body through BodyStorage or BillingRequestInput;
// channel retries retain the resulting billing session and pricing snapshot.
func PrepareRequestBilling(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	if info.RelayFormat == types.RelayFormatClaude {
		info.OmniNativeClaude = service.OmniNativeClaudeFor(info)
	}
	if status, reason, version := omniRequestPrice(c, info); status == "paused" {
		return types.NewError(errors.New(reason), types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusServiceUnavailable), types.ErrOptionWithSkipRetry())
	} else if status == "available" {
		info.OmniPriceVersion = version
		info.OmniPriceProtected = true
		info.ForcePreConsume = true
		service.OmniEnsureOutputLimit(info)
		if err := service.OmniValidatePricedRequest(info); err != nil {
			return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithStatusCode(http.StatusBadRequest), types.ErrOptionWithSkipRetry())
		}
	}
	omniActive, omniErr := model.OmniActive(info.UserId)
	if omniErr != nil {
		return types.NewError(omniErr, types.ErrorCodeQueryDataError)
	}
	if info.OmniPoolID != "" {
		omniActive = true
	}
	if omniActive {
		info.ForcePreConsume = true
		info.OmniBillingActive = true
		if err := service.OmniValidateOutputLimits(info); err != nil {
			return types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest))
		}
		service.OmniEnsureOutputLimit(info)
	}
	needSensitiveCheck := setting.ShouldCheckPromptSensitive()
	meta := &types.TokenCountMeta{TokenType: types.TokenTypeTokenizer}
	if info.Request != nil && (info.OmniPriceProtected || omniActive || needSensitiveCheck || constant.CountToken) {
		meta = info.Request.GetTokenCountMeta()
	} else {
		// Avoid building CombineText when only the pricing quantities are needed.
		switch request := info.Request.(type) {
		case *dto.GeneralOpenAIRequest:
			meta.MaxTokens = int(max(lo.FromPtr(request.MaxTokens), lo.FromPtr(request.MaxCompletionTokens)))
		case *dto.OpenAIResponsesRequest:
			meta.MaxTokens = int(lo.FromPtr(request.MaxOutputTokens))
		case *dto.ClaudeRequest:
			meta.MaxTokens = int(lo.FromPtr(request.MaxTokens))
		case *dto.ImageRequest:
			meta = request.GetTokenCountMeta()
		}
	}

	if needSensitiveCheck && meta != nil {
		if contains, words := service.CheckSensitiveText(meta.CombineText); contains {
			service.RequestPolicy(c).AddEvent(service.PolicyEvent{ErrorCode: string(types.ErrorCodeSensitiveWordsDetected), ErrorSource: "local", Decision: service.PolicyDecision{Action: "stop", Reason: "local_rejection", Source: "global"}, Health: "unchanged"})
			message := fmt.Sprintf("user sensitive words detected: %s", strings.Join(words, ", "))
			logger.LogWarn(c, message)
			return types.NewError(errors.New(message), types.ErrorCodeSensitiveWordsDetected)
		}
	}

	tokens, err := service.EstimateRequestToken(c, meta, info)
	if info.OmniPoolID != "" {
		tokens, err = service.CountRequestToken(c, meta, info)
	}
	if err != nil {
		return types.NewError(err, types.ErrorCodeCountTokenFailed)
	}
	if info.OmniNativeClaude {
		tokens += service.OmniNativeHistoryInputBound(info)
	}
	info.SetEstimatePromptTokens(tokens)
	changed, adjustErr := service.OmniAdjustDefaultOutputLimit(info)
	if adjustErr != nil {
		code, status := types.ErrorCode("context_length_exceeded"), http.StatusBadRequest
		var rejection *model.OmniBindingRejection
		if errors.As(adjustErr, &rejection) {
			code = types.ErrorCode(rejection.Reason)
			if rejection.Reason == "request_body_too_large" {
				status = http.StatusRequestEntityTooLarge
			}
		}
		return types.NewError(adjustErr, code, types.ErrOptionWithStatusCode(status), types.ErrOptionWithSkipRetry())
	}
	if changed {
		meta = info.Request.GetTokenCountMeta()
	}
	if info.OmniPoolID != "" {
		if err := service.OmniValidateSharedReservation(info); err != nil {
			var limitError *service.OmniSharedRequestLimitError
			if errors.As(err, &limitError) {
				return types.NewError(err, types.ErrorCode("shared_request_limit"), types.ErrOptionWithStatusCode(http.StatusBadRequest), types.ErrOptionWithSkipRetry())
			}
			return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithStatusCode(http.StatusForbidden), types.ErrOptionWithSkipRetry())
		}
		omniActive = true
	}

	priceData, err := helper.ModelPriceHelper(c, info, tokens, meta)
	if err != nil {
		return types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest))
	}
	if priceData.FreeModel && info.OmniPoolID != "" {
		return types.NewError(errors.New("共享套餐仅支持已核价且可预留的模型请求"), types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusForbidden), types.ErrOptionWithSkipRetry())
	}
	if priceData.FreeModel {
		logger.LogInfo(c, fmt.Sprintf("模型 %s 免费，跳过预扣费", info.OriginModelName))
		return nil
	}
	return service.PreConsumeBilling(c, priceData.QuotaToPreConsume, info)
}

// RefundFailedRequestBilling applies the common final-failure policy after all
// eligible attempts have ended. A settled BillingSession never refunds again.
func RefundFailedRequestBilling(c *gin.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError) *types.NewAPIError {
	if apiErr == nil {
		return nil
	}
	apiErr = service.NormalizeViolationFeeError(apiErr)
	if info.Billing != nil {
		info.Billing.Refund(c)
	}
	service.ChargeViolationFeeIfNeeded(c, info, apiErr)
	return apiErr
}
