package controller

import (
	"errors"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func OmniMigrationAdmin(c *gin.Context) {
	var b struct {
		Action       string `json:"action"`
		ID           string `json:"id"`
		ProfileID    string `json:"profile_id"`
		PreviewHash  string `json:"preview_hash"`
		AcceptanceID string `json:"acceptance_id"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("请求无效"))
		return
	}
	switch b.Action {
	case "close_legacy":
		omniReply(c, true, model.OmniMigrationCloseLegacy(c.GetInt("id"), b.ProfileID, b.AcceptanceID))
	case "preview":
		v, e := model.OmniMigrationPrepare(c.GetInt("id"), b.ID, b.ProfileID)
		omniReply(c, v, e)
	case "apply":
		omniReply(c, true, model.OmniMigrationApply(c.GetInt("id"), b.ID, b.PreviewHash))
	case "open_sales":
		omniReply(c, true, model.OmniMigrationOpenSales(c.GetInt("id"), b.ID, b.AcceptanceID))
	default:
		omniReply(c, nil, errors.New("未知迁移操作"))
	}
}
func OmniMigrationResult(c *gin.Context) {
	var batch model.OmniMigrationBatch
	if err := model.DB.First(&batch, "id = ?", c.Param("batch")).Error; err != nil {
		omniReply(c, nil, err)
		return
	}
	var items []model.OmniMigrationItem
	err := model.DB.Where("batch_id = ?", batch.ID).Order("user_id").Find(&items).Error
	omniReply(c, gin.H{"batch": batch, "preview": batch.PreviewJSON, "items": items}, err)
}
func OmniMigrationSelf(c *gin.Context) {
	uid := c.GetInt("id")
	var item model.OmniMigrationItem
	var exit []model.OmniMigrationExit
	err := model.DB.Where("user_id = ?", uid).Find(&item).Error
	if err == nil {
		err = model.DB.Where("user_id = ?", uid).Order("requested_at DESC").Limit(30).Find(&exit).Error
	}
	omniReply(c, gin.H{"migration": item, "applications": exit}, err)
}
func OmniMigrationExitRequest(c *gin.Context) {
	var b struct {
		RequestKey string `json:"request_key"`
		Reason     string `json:"reason"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("请求无效"))
		return
	}
	v, e := model.OmniMigrationExitRequest(c.GetInt("id"), b.RequestKey, b.Reason, c.GetInt64("auth_version"))
	omniReply(c, v, e)
}
func OmniMigrationExitWithdraw(c *gin.Context) {
	omniReply(c, true, model.OmniMigrationExitResolve(c.GetInt("id"), c.GetInt("id"), c.Param("exit"), "withdraw", "用户撤回"))
}
func OmniMigrationExitAdminList(c *gin.Context) {
	var rows []model.OmniMigrationExit
	err := model.DB.Order("requested_at DESC").Limit(200).Find(&rows).Error
	omniReply(c, rows, err)
}
func OmniMigrationExitAdmin(c *gin.Context) {
	var b struct {
		UserID int    `json:"user_id"`
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if c.ShouldBindJSON(&b) != nil || b.Action != "approve" && b.Action != "reject" {
		omniReply(c, nil, errors.New("审批操作无效"))
		return
	}
	omniReply(c, true, model.OmniMigrationExitResolve(c.GetInt("id"), b.UserID, c.Param("exit"), b.Action, b.Note))
}

// The old purchase endpoints remain readable for historical receipts, while
// new purchase attempts and late old settlements require manual verification.
func OmniLegacySubscriptionSaleGuard(c *gin.Context) {
	closed, err := model.OmniLegacySaleClosed(model.DB)
	if err != nil || closed {
		if err == nil {
			err = errors.New("旧套餐已停售，请进入个人套餐；历史付款请人工核验")
		}
		omniReply(c, nil, err)
		c.Abort()
		return
	}
	c.Next()
}
