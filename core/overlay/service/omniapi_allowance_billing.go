package service

import (
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

// Reserve funding first so the token and session reserve the actual allowance,
// not a rejected theoretical maximum. A failed token check restores funding.
func (s *BillingSession) preConsumeOmniAllowance(c *gin.Context, estimate int) *types.NewAPIError {
	f := s.funding.(*OmniFunding)
	deadline := time.NewTimer(120 * time.Second)
	defer deadline.Stop()
	var fundingErr error
	for {
		if err := c.Request.Context().Err(); err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeUpdateDataError, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
		}
		fundingErr = f.PreConsume(estimate)
		if !errors.Is(fundingErr, model.ErrOmniAllowancePending) {
			break
		}
		retry := time.NewTimer(250 * time.Millisecond)
		select {
		case <-c.Request.Context().Done():
			retry.Stop()
			fundingErr = c.Request.Context().Err()
		case <-deadline.C:
			retry.Stop()
			fundingErr = errors.New("已有请求正在结算订阅额度，请稍后重试；本次未扣费")
		case <-retry.C:
			continue
		}
		return types.NewErrorWithStatusCode(fundingErr, types.ErrorCodeUpdateDataError, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	if err := fundingErr; err != nil {
		if f.poolID != "" && (errors.Is(err, model.ErrOmniSharedUnavailable) || errors.Is(err, model.ErrOmniSharedConcurrent)) {
			status := http.StatusForbidden
			if errors.Is(err, model.ErrOmniSharedConcurrent) {
				status = http.StatusTooManyRequests
			}
			return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, status, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		if apiErr, handled := omniFundingError(err); handled {
			return apiErr
		}
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	held := int(f.reserved)
	if f.record != nil && f.record.BillingPolicy == model.OmniCustomerBillingV1 {
		s.relayInfo.OmniCustomerActual = true
		tokenID := s.relayInfo.TokenId
		if s.relayInfo.IsPlayground {
			tokenID = 0
		}
		if err := model.OmniBindCustomerBilling(f.requestID, tokenID, s.relayInfo.OmniPriceVersion, s.relayInfo.OmniPriceQuote); err != nil {
			if refundErr := f.Refund(); refundErr != nil {
				return types.NewError(refundErr, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
			}
			return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry())
		}
		s.preConsumedQuota = held
		s.tokenConsumed = 0
		s.syncRelayInfo()
		return nil
	}
	if err := PreConsumeTokenQuota(s.relayInfo, held); err != nil {
		if refundErr := f.Refund(); refundErr != nil {
			// Keep the durable reservation attached for the relay's failure cleanup.
			s.preConsumedQuota = held
			s.relayInfo.Billing = s
			s.syncRelayInfo()
			return types.NewError(refundErr, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
		return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	s.preConsumedQuota = held
	s.tokenConsumed = held
	s.syncRelayInfo()
	logger.LogInfo(c, fmt.Sprintf("用户 %d 预留额度 %s，估算上限 %s，按实际用量结算 (funding=%s)", s.relayInfo.UserId, logger.FormatQuota(held), logger.FormatQuota(estimate), f.Source()))
	return nil
}
