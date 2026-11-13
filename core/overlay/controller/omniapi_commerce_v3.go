package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"time"
)

func OmniCommerceCatalog() gin.H {
	cfg := model.OmniCommerceSettings()
	profile, err := model.OmniCommerceSaleProfile(model.DB)
	plans := []model.OmniCommercePlan{}
	if err == nil {
		plans, err = profile.Plans()
	}
	if len(plans) == 0 {
		for i, cents := range []int64{1900, 3900, 6900, 9900, 19900} {
			p := model.OmniCommercePlan{Key: fmt.Sprintf("v3_%d", cents/100), Title: fmt.Sprintf("%d元套餐", cents/100), MonthlyCents: cents, FiveEnabled: i < 2, Prices: map[int]int64{}}
			for _, m := range []int{1, 3, 6, 12} {
				p.Prices[m], _ = model.OmniCommercePrice(cents, m)
			}
			plans = append(plans, p)
		}
	}
	shared := []gin.H{}
	for _, cents := range []int64{9900, 19900, 39900} {
		var grant int64
		if err == nil {
			grant, _ = profile.GrantCents(cents)
		}
		shared = append(shared, gin.H{"cents": cents, "allowance_cents": grant})
	}
	var names []string
	sharedScope := ""
	if err == nil {
		var matrix model.OmniCapabilityMatrix
		_ = json.Unmarshal([]byte(profile.CapabilityMatrix), &matrix)
		sharedScope = matrix.SharedScope
		var routes map[string][]int
		_ = json.Unmarshal([]byte(profile.Models), &routes)
		for name := range routes {
			names = append(names, name)
		}
	}
	return gin.H{"enabled": cfg.Enabled, "sales_enabled": err == nil, "shared_scope": sharedScope, "profile_id": func() string {
		if err == nil {
			return profile.ID
		}
		return ""
	}(), "plans": plans, "shared": shared, "models": names, "price_version": func() string { v, _ := model.OmniCommercePrices(); return v.Version }(), "target_margin_percent": 20, "cost_verification_pending": err != nil}
}
func OmniCommercePurchase(c *gin.Context) {
	var b struct {
		Plan       string `json:"plan"`
		Months     int    `json:"months"`
		ProfileID  string `json:"profile_id"`
		RequestKey string `json:"request_key"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("请求无效"))
		return
	}
	omniReply(c, true, model.OmniCommercePurchase(c.GetInt("id"), b.Plan, b.Months, b.ProfileID, b.RequestKey, c.GetInt64("auth_version")))
}
func OmniCommerceResetQuote(c *gin.Context) {
	q, err := model.OmniCommerceResetQuote(c.GetInt("id"))
	omniReply(c, q, err)
}
func OmniCommerceReset(c *gin.Context) {
	var b struct {
		RequestKey string               `json:"request_key"`
		Quote      model.OmniResetQuote `json:"quote"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("请求无效"))
		return
	}
	omniReply(c, true, model.OmniCommerceReset(c.GetInt("id"), b.RequestKey, b.Quote, c.GetInt64("auth_version")))
}
func OmniCommerceConvert(c *gin.Context) {
	var b struct {
		ProfileID string `json:"profile_id"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("请求无效"))
		return
	}
	omniReply(c, true, model.OmniCommerceConvert(c.GetInt("id"), b.ProfileID, c.GetInt64("auth_version")))
}
func OmniSharedList(c *gin.Context) {
	type row struct {
		model.OmniSharedMember
		Name    string `json:"name"`
		OwnerID int    `json:"owner_id"`
	}
	rows := []row{}
	err := model.DB.Table("omni_shared_members AS m").Select("m.*, p.name, p.owner_id").Joins("JOIN omni_shared_pools AS p ON p.id = m.pool_id").Where("m.user_id = ? AND m.state IN ?", c.GetInt("id"), []string{"active", "invited"}).Scan(&rows).Error
	omniReply(c, rows, err)
}
func OmniSharedView(c *gin.Context) {
	v, err := model.OmniSharedView(c.GetInt("id"), c.Param("pool"), c.Query("period"))
	omniReply(c, v, err)
}
func OmniSharedMutate(c *gin.Context) {
	var b model.OmniSharedCommand
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("请求无效"))
		return
	}
	v, err := model.OmniSharedMutate(c.GetInt("id"), c.Param("pool"), b, c.GetInt64("auth_version"))
	omniReply(c, v, err)
}

func OmniEmailStatus(c *gin.Context) {
	var u model.User
	err := model.DB.Select("id", "email", "verified_email", "email_verified_at", "email_verification_required").First(&u, c.GetInt("id")).Error
	omniReply(c, gin.H{"email": u.Email, "verified": model.OmniEmailVerified(u), "required": u.EmailVerificationRequired, "legacy_optional": !u.EmailVerificationRequired}, err)
}
func OmniRiskEmailSend(c *gin.Context) {
	if !riskV2Origin(c) {
		return
	}
	uid, ok := riskV2Self(c)
	if !ok {
		return
	}
	var b struct {
		Email string `json:"email"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("请求无效"))
		return
	}
	email, err := service.ValidateAccountEmail(b.Email)
	if err != nil {
		omniReply(c, nil, err)
		return
	}
	var u model.User
	if err = model.DB.Select("id", "email").First(&u, uid).Error; err != nil {
		omniReply(c, nil, err)
		return
	}
	if u.Email != "" && model.NormalizeEmail(u.Email) != email {
		omniReply(c, nil, errors.New("已有邮箱需原邮箱验证或人工核验"))
		return
	}
	if err = model.OmniEmailAbuseAdmit(email, c.ClientIP(), uid, "risk_bind"); err != nil {
		omniReply(c, nil, err)
		return
	}
	code, err := model.OmniEmailNewCode(false)
	if err != nil {
		omniReply(c, nil, err)
		return
	}
	if err = model.OmniEmailIssue("risk_bind", email, uid, code); err != nil {
		omniReply(c, nil, err)
		return
	}
	err = common.SendEmailWithPurpose("OmniAPI 安全复核邮箱验证", email, "<p>验证码：<strong>"+code+"</strong></p><p>10分钟内有效。验证后仍需管理员审核。</p>", "risk_bind")
	omniReply(c, gin.H{"expires_in": 600, "resend_in": 60}, err)
}
func OmniRiskEmailVerify(c *gin.Context) {
	if !riskV2Origin(c) {
		return
	}
	uid, ok := riskV2Self(c)
	if !ok {
		return
	}
	var b struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("请求无效"))
		return
	}
	omniReply(c, true, model.OmniEmailRiskBind(uid, model.NormalizeEmail(b.Email), b.Code))
}

// Approval imports a cost worksheet; it never opens sales. The separate release
// switch requires the combined acceptance report and live email readiness.
func OmniCommerceApprove(c *gin.Context) {
	var p model.OmniCommerceProfile
	if c.ShouldBindJSON(&p) != nil {
		omniReply(c, nil, errors.New("核价资料无效"))
		return
	}
	p.ApprovedBy = c.GetInt("id")
	p.ApprovedAt = time.Now().Unix()
	err := model.OmniCommerceApprove(p)
	omniReply(c, true, err)
}

func OmniSharedRecoveryList(c *gin.Context) {
	var rows []model.OmniSharedHold
	err := model.DB.Where("state = ? AND dispatch_state = ?", "pending", "unknown").Order("created_at").Limit(200).Find(&rows).Error
	omniReply(c, rows, err)
}
func OmniSharedRecovery(c *gin.Context) {
	var b struct {
		RequestID  string `json:"request_id"`
		PoolID     string `json:"pool_id"`
		Actual     int64  `json:"actual"`
		Evidence   string `json:"evidence"`
		RequestKey string `json:"request_key"`
		Unfreeze   bool   `json:"unfreeze"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("请求无效"))
		return
	}
	var err error
	if b.Unfreeze {
		err = model.OmniSharedUnfreeze(c.GetInt("id"), b.PoolID, b.Evidence, b.RequestKey)
	} else {
		err = model.OmniSharedReconcile(c.GetInt("id"), b.RequestID, b.Actual, b.Evidence, b.RequestKey)
	}
	omniReply(c, true, err)
}
