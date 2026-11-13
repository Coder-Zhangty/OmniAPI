package service

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// Persist the reservation identity before acknowledging an accepted task.
// Pending tasks retain their reservation until a confirmed terminal result.
func OmniBindTask(info *relaycommon.RelayInfo, task *model.Task) {
	if session, ok := info.Billing.(*BillingSession); ok {
		if funding, ok := session.funding.(*OmniFunding); ok {
			task.PrivateData.OmniRequestID = info.RequestId
			funding.asyncTask = task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure
			if funding.shared && funding.asyncTask {
				_ = model.DB.Model(&model.OmniSharedHold{}).Where("request_id = ? AND state = ?", info.RequestId, "pending").Update("dispatch_state", "task").Error
			}
		}
	}
}

func OmniFinishTask(ctx context.Context, task *model.Task, actual int, reason string) bool {
	if actual < 0 {
		return false
	}
	record, err := model.OmniRequestRecord(task.PrivateData.OmniRequestID)
	if err != nil {
		logger.LogError(ctx, "task reservation lookup failed")
		return false
	}
	// The frozen source cannot be silently replaced or exceed its reserved cap.
	if record.BillingPolicy != model.OmniCustomerBillingV1 && int64(actual) > record.Reserved && record.Source != "omni_shared" {
		logger.LogWarn(ctx, "task cost exceeded reserved maximum; excess retained by platform")
		actual = int(record.Reserved)
	}
	changed, err := model.OmniAdjustOnce(record.RequestID, int64(actual), true)
	if err != nil {
		logger.LogError(ctx, "task reservation settlement failed")
		return false
	}
	if !changed {
		return true
	}
	delta := actual - int(record.Reserved)
	if record.BillingPolicy == model.OmniCustomerBillingV1 {
		delta = actual - task.Quota
	}
	if record.BillingPolicy != model.OmniCustomerBillingV1 {
		taskAdjustTokenQuota(ctx, task, delta)
	}
	model.UpdateUserUsedQuota(task.UserId, delta)
	model.UpdateChannelUsedQuota(task.ChannelId, delta)
	task.Quota = actual
	if err = task.UpdateQuota(); err != nil {
		logger.LogError(ctx, "task quota projection update failed")
	}
	if delta != 0 {
		kind := model.LogTypeConsume
		amount := delta
		if delta < 0 {
			kind = model.LogTypeRefund
			amount = -delta
		}
		other := taskBillingOther(task)
		other.SetPublic("billing_source", record.Source)
		model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{UserId: task.UserId, LogType: kind, ChannelId: task.ChannelId, ModelName: taskModelName(task), Quota: amount, TokenId: task.PrivateData.TokenId, Group: task.Group, Content: reason, Other: other})
	}
	common.SysLog("OmniAPI task reservation finalized")
	return true
}
