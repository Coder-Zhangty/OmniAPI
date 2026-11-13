package service

import (
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"net/http"
	"time"
)

func omniFundingError(err error) (*types.NewAPIError, bool) {
	if errors.Is(err, model.ErrOmniAccountDebt) {
		return types.NewErrorWithStatusCode(err, types.ErrorCode("account_in_debt"), http.StatusPaymentRequired, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog()), true
	}
	var subscription *model.OmniSubscriptionQuotaError
	if errors.As(err, &subscription) {
		message := "本次请求的模型或请求类型不在当前订阅可用范围内；钱包余额续用已关闭，请使用订阅支持的模型和请求类型"
		if subscription.ModelIncluded && subscription.FiveDisabled {
			zone := time.FixedZone("Asia/Shanghai", 8*3600)
			message = fmt.Sprintf("当前订阅周额度不足或已暂停，周额度将在 %s（北京时间）恢复。本档不设五小时限制；本次未扣费、未转扣钱包。", time.Unix(subscription.WeekResetAt, 0).In(zone).Format("01-02 15:04"))
			return types.NewErrorWithStatusCode(errors.New(message), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog()), true
		}
		if subscription.ModelIncluded {
			if subscription.ActualAllowance {
				zone := time.FixedZone("Asia/Shanghai", 8*3600)
				message = fmt.Sprintf("当前订阅额度已用完或已暂停，等待对应窗口重置后自动恢复（五小时 %s，七天 %s，北京时间）。已完成请求的超额部分由平台承担；本次请求未扣费，未转扣钱包。", time.Unix(subscription.FiveResetAt, 0).In(zone).Format("01-02 15:04"), time.Unix(subscription.WeekResetAt, 0).In(zone).Format("01-02 15:04"))
				if model.OmniCustomerBillingEnabled() {
					message = fmt.Sprintf("当前订阅额度已用完或已暂停（五小时 %s，七天 %s，北京时间恢复），钱包续用已关闭；本次未接收、未扣费。此前已接收请求按实际用量结算，套餐外差额扣钱包。", time.Unix(subscription.FiveResetAt, 0).In(zone).Format("01-02 15:04"), time.Unix(subscription.WeekResetAt, 0).In(zone).Format("01-02 15:04"))
				}
				return types.NewErrorWithStatusCode(errors.New(message), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog()), true
			}
			message = fmt.Sprintf("订阅额度不足以预扣本次请求：需预扣 %s，五小时剩余 %s（上限 %s），七天剩余 %s（上限 %s）。预扣按输入及允许的最大输出计算，成功后按实际用量结算；钱包余额续用已关闭，本次未扣费。", logger.FormatQuota(int(subscription.Required)), logger.FormatQuota(int(subscription.FiveAvailable)), logger.FormatQuota(int(subscription.FiveLimit)), logger.FormatQuota(int(subscription.WeekAvailable)), logger.FormatQuota(int(subscription.WeekLimit)))
			if subscription.Required > subscription.FiveLimit || subscription.Required > subscription.WeekLimit {
				message += "本次预扣超过订阅单个窗口的额度上限，等待重置仍不足；请减少上下文或最大输出后重试"
			} else {
				zone := time.FixedZone("Asia/Shanghai", 8*3600)
				message += fmt.Sprintf("请减少上下文或最大输出，或等待额度重置后重试（五小时 %s，七天 %s，北京时间）", time.Unix(subscription.FiveResetAt, 0).In(zone).Format("01-02 15:04"), time.Unix(subscription.WeekResetAt, 0).In(zone).Format("01-02 15:04"))
			}
		}
		return types.NewErrorWithStatusCode(errors.New(message), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog()), true
	}
	var wallet *model.OmniWalletQuotaError
	if errors.As(err, &wallet) {
		message := fmt.Errorf("钱包余额已耗尽（当前 %s），本次请求未扣费", logger.FormatQuota(int(wallet.Available)))
		return types.NewErrorWithStatusCode(message, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog()), true
	}
	if errors.Is(err, model.ErrOmniInsufficient) {
		message := errors.New("本次请求无法使用订阅额度，且已关闭钱包余额续用；可在钱包开启余额续用后重试")
		return types.NewErrorWithStatusCode(message, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog()), true
	}
	return nil, false
}
