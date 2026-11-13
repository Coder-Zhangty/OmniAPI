package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const riskNoticeCookie = "omni_risk_notice"

func OmniRiskTrialSelf(c *gin.Context) {
	status, err := model.OmniTrialStatusFor(c.GetInt("id"))
	riskReply(c, status, err)
}

func writeRiskRestrictedLogin(c *gin.Context, b *service.AuthBundle) {
	c.Set("risk_auth_completed", true)
	c.Set("risk_auth_user_id", b.RiskUserID)
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(riskNoticeCookie, b.RiskNoticeToken, 900, "/api/omni/risk", "", true, true)
	c.Header("Cache-Control", "no-store")
	c.Header("X-Omni-Risk-Review", "/brand/risk-review.html")
	c.JSON(200, gin.H{"success": true, "message": "请查看本人安全复核通知", "data": gin.H{"restricted": true, "redirect_url": "/brand/risk-review.html"}})
}
func riskV2Identity(c *gin.Context) (int, error) {
	if raw := c.GetHeader("Authorization"); raw != "" {
		id, e := service.ParseAccessToken(strings.TrimPrefix(raw, "Bearer "))
		if e != nil {
			return 0, e
		}
		_, u, e := service.ValidateLoginSession(id)
		if e != nil {
			return 0, e
		}
		return u.Id, nil
	}
	if token, e := c.Cookie(riskNoticeCookie); e == nil && token != "" {
		if uid, e := service.ValidateOmniRiskNotice(token); e == nil {
			return uid, nil
		}
	}
	raw := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	id, e := service.ParseAccessToken(raw)
	if e != nil {
		return 0, e
	}
	_, u, e := service.ValidateLoginSession(id)
	if e != nil {
		return 0, e
	}
	return u.Id, nil
}
func riskV2Self(c *gin.Context) (int, bool) {
	uid, e := riskV2Identity(c)
	c.Header("Cache-Control", "no-store")
	if e != nil || uid <= 0 {
		c.AbortWithStatusJSON(401, gin.H{"success": false, "message": "请先完成身份验证后查看本人通知"})
		return 0, false
	}
	return uid, true
}
func OmniRiskV2Self(c *gin.Context) {
	uid, ok := riskV2Self(c)
	if !ok {
		return
	}
	r, e := model.OmniRiskReadControl(model.DB, uid)
	if e != nil {
		riskReply(c, nil, e)
		return
	}
	var notices []model.OmniRiskNoticeV2
	if e = model.DB.Where("user_id = ?", uid).Order("created_at DESC").Limit(20).Find(&notices).Error; e != nil {
		riskReply(c, nil, e)
		return
	}
	var account model.User
	if e = model.DB.Select("id", "status", "email", "verified_email", "email_verified_at").First(&account, uid).Error; e != nil {
		riskReply(c, nil, e)
		return
	}
	sourceLimited, sourceErr := riskV2SourceLimited(c, uid)
	pending := r.State == "hold" || r.State == "budget" || r.State == "guard" || r.State == "confirmed" || account.Status != 1 || sourceLimited || sourceErr != nil
	category := "账户安全复核"
	restriction := "当前没有待复核限制"
	switch r.State {
	case "hold", "confirmed":
		restriction = "已暂停调用及普通账户操作，等待人工复核"
	case "budget":
		restriction = "已暂停新调用，余额与赠额保留，等待人工复核"
	case "guard":
		restriction = "调用额度和并发受到保护，等待人工复核"
	case "restored":
		restriction = "本次保护已解除，请重新登录"
	}
	switch r.Reason {
	case "registration_sequence":
		category = "注册与账户操作异常"
	case "new_user_budget":
		category = "新账号消费保护"
	case "quote_unavailable":
		category = "请求预算待核实"
	case "manual_confirmed":
		category = "人工安全复核"
	default:
		if strings.Contains(r.Reason, "key_sources") || strings.Contains(r.Reason, "spend_deviation") {
			category = "调用与凭据安全"
		}
	}
	if r.UserID == 0 && account.Status != 1 {
		category = "历史人工处置"
		restriction = "账号已有人工使用限制，可提交说明申请复核"
	} else if r.UserID == 0 && sourceLimited {
		category = "网络来源保护"
		restriction = "当前网络来源的业务调用受限，可提交说明申请复核"
	}
	message := ""
	if account.Status != 1 && r.State == "restored" {
		message = "本次风险保护已解除，但账号仍有其他限制，请提交说明由管理员核对。"
	}
	if sourceErr != nil && r.UserID == 0 {
		category = "来源校验暂不可用"
		restriction = "可查看本人通知和提交说明，请稍后再试其他操作"
	}
	public := gin.H{"state": r.State, "case_id": r.CaseID, "started_at": r.StartedAt}
	riskReply(c, gin.H{"control": public, "notices": notices, "review_pending": pending, "message": message, "reason_category": category, "current_restriction": restriction, "email_verification_required": model.OmniEmailSettings().Enabled && pending && !model.OmniEmailVerified(account), "email_verified": model.OmniEmailVerified(account)}, nil)
}
func riskV2Origin(c *gin.Context) bool {
	origin := c.GetHeader("Origin")
	if origin != "https://omniapi.biz" && origin != "https://www.omniapi.biz" && origin != "https://omniapi.hyeroomy.com" {
		c.AbortWithStatus(403)
		return false
	}
	if !strings.HasPrefix(c.ContentType(), "application/json") {
		c.AbortWithStatus(415)
		return false
	}
	return true
}
func OmniRiskV2Ack(c *gin.Context) {
	if !riskV2Origin(c) {
		return
	}
	uid, ok := riskV2Self(c)
	if !ok {
		return
	}
	var b struct {
		Displayed    bool `json:"displayed"`
		Acknowledged bool `json:"acknowledged"`
	}
	if c.ShouldBindJSON(&b) != nil {
		c.AbortWithStatus(400)
		return
	}
	updates := map[string]any{}
	if b.Displayed {
		updates["displayed_at"] = gorm.Expr("CASE WHEN displayed_at > 0 THEN displayed_at ELSE ? END", time.Now().Unix())
	}
	if b.Acknowledged {
		updates["acknowledged_at"] = gorm.Expr("CASE WHEN acknowledged_at > 0 THEN acknowledged_at ELSE ? END", time.Now().Unix())
	}
	if len(updates) == 0 {
		c.AbortWithStatus(400)
		return
	}
	q := model.DB.Model(&model.OmniRiskNoticeV2{}).Where("id = ? AND user_id = ?", c.Param("id"), uid).Updates(updates)
	if q.Error != nil || q.RowsAffected != 1 {
		c.AbortWithStatus(404)
		return
	}
	riskReply(c, gin.H{"saved": true}, nil)
}
func OmniRiskV2Appeal(c *gin.Context) {
	if !riskV2Origin(c) {
		return
	}
	uid, ok := riskV2Self(c)
	if !ok {
		return
	}
	var b struct {
		Body string `json:"body"`
	}
	if c.Request.ContentLength > 10000 || c.ShouldBindJSON(&b) != nil || len([]rune(b.Body)) < 5 || len([]rune(b.Body)) > 2000 {
		c.AbortWithStatusJSON(400, gin.H{"success": false, "message": "请填写5至2000字说明，不要提交密码或Key"})
		return
	}
	var count int64
	model.DB.Model(&model.OmniRiskAppeal{}).Where("user_id = ? AND created_at >= ?", uid, time.Now().Unix()-3600).Count(&count)
	if count >= 3 {
		c.AbortWithStatus(429)
		return
	}
	r, e := model.OmniRiskReadControl(model.DB, uid)
	if e != nil {
		riskReply(c, nil, e)
		return
	}
	if r.CaseID == "" {
		var notice model.OmniRiskNoticeV2
		if err := model.DB.Where("user_id = ?", uid).Order("created_at DESC").Limit(1).Find(&notice).Error; err != nil {
			riskReply(c, nil, err)
			return
		}
		r.CaseID = notice.CaseID
		if r.CaseID == "" {
			limited, sourceErr := riskV2SourceLimited(c, uid)
			if !limited && sourceErr == nil {
				c.AbortWithStatusJSON(400, gin.H{"success": false, "message": "当前没有可提交说明的复核记录"})
				return
			}
			r.CaseID = "SOURCE-" + strconv.Itoa(uid)
		}
	}
	id := uuid.NewString()
	e = model.DB.Transaction(func(tx *gorm.DB) error {
		var owner model.User
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&owner, uid).Error; e != nil {
			return e
		}
		if e := tx.Model(&model.OmniRiskAppeal{}).Where("user_id = ? AND created_at >= ?", uid, time.Now().Unix()-3600).Count(&count).Error; e != nil {
			return e
		}
		if count >= 3 {
			return errors.New("复核说明提交频率过高，请稍后再试")
		}
		if e := tx.Create(&model.OmniRiskAppeal{ID: id, UserID: uid, CaseID: r.CaseID, Body: b.Body, CreatedAt: time.Now().Unix()}).Error; e != nil {
			return e
		}
		return model.OmniRiskEnqueue(tx, "appeal:"+id, r.CaseID, "case", "OmniAPI 收到本人复核说明\n账号 #"+strconv.Itoa(uid)+"\n请登录风控中心查看；说明正文不发送外部渠道。\nhttps://omniapi.biz/risk-control")
	})
	riskReply(c, gin.H{"submitted": e == nil}, e)
}
func OmniRiskV2Admin(c *gin.Context) {
	p, e := model.OmniRiskV2Policy()
	if e != nil {
		riskReply(c, nil, e)
		return
	}
	var retries []model.OmniRiskRetryV2
	var controls []model.OmniRiskControl
	var ips []model.OmniRiskIP
	var appeals []model.OmniRiskAppeal
	var actions []model.OmniRiskActionV2
	var notices []model.OmniRiskNoticeV2
	for _, query := range []struct {
		Table any
		Order string
		Limit int
	}{{&retries, "next_at", 100}, {&controls, "started_at DESC", 100}, {&ips, "created_at DESC", 100}, {&appeals, "created_at DESC", 100}, {&actions, "created_at DESC", 100}, {&notices, "created_at DESC", 100}} {
		if e = model.DB.Order(query.Order).Limit(query.Limit).Find(query.Table).Error; e != nil {
			riskReply(c, nil, e)
			return
		}
	}
	var exposure []struct {
		State    string
		N        int64
		Reserved int64
		Actual   int64
	}
	model.DB.Model(&model.OmniRiskExposure{}).Select("state,COUNT(*) AS n,SUM(reserved) AS reserved,SUM(actual) AS actual").Group("state").Find(&exposure)
	publicNotices := []gin.H{}
	for _, n := range notices {
		publicNotices = append(publicNotices, gin.H{"user_id": n.UserID, "case_id": n.CaseID, "title": n.Title, "displayed_at": n.DisplayedAt, "acknowledged_at": n.AcknowledgedAt})
	}
	riskReply(c, gin.H{"policy": p, "controls": controls, "ips": ips, "appeals": appeals, "actions": actions, "notices": publicNotices, "exposure": exposure, "detector_retries": retries}, nil)
}
func OmniRiskV2PolicyPut(c *gin.Context) {
	if !riskV2Origin(c) {
		return
	}
	var b model.OmniRiskPolicyV2
	if c.ShouldBindJSON(&b) != nil || b.Version < 1 || b.NewBudgetMicros != 1000000 {
		c.AbortWithStatus(400)
		return
	}
	e := model.DB.Transaction(func(tx *gorm.DB) error {
		var current model.OmniRiskPolicyV2
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, 1).Error; e != nil {
			return e
		}
		if current.Version != b.Version {
			return errors.New("risk_version_conflict")
		}
		enabling := !b.Shadow && ((b.AutoHold && (current.Shadow || !current.AutoHold)) || (b.NewUserProtection && (current.Shadow || !current.NewUserProtection)) || (b.TemporaryIP && (current.Shadow || !current.TemporaryIP)))
		if enabling {
			if e := model.OmniRiskActivationReady(); e != nil {
				return e
			}
		}

		q := tx.Model(&model.OmniRiskPolicyV2{}).Where("id = 1 AND version = ?", b.Version).Updates(map[string]any{"version": b.Version + 1, "auto_hold": b.AutoHold, "new_user_protection": b.NewUserProtection, "temporary_ip": b.TemporaryIP, "shadow": b.Shadow, "updated_at": time.Now().Unix()})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return errors.New("risk_version_conflict")
		}
		snapshot, _ := json.Marshal(b)
		return tx.Create(&model.OmniRiskActionV2{ID: uuid.NewString(), ActorID: c.GetInt("id"), Action: "policy", Note: string(snapshot), CreatedAt: time.Now().Unix()}).Error
	})
	riskV2AdminReply(c, gin.H{"saved": e == nil}, e)
}
func OmniRiskV2Action(c *gin.Context) {
	if !riskV2Origin(c) {
		return
	}
	var b struct {
		UserID  int    `json:"user_id"`
		Version int64  `json:"version"`
		Action  string `json:"action"`
		Note    string `json:"note"`
	}
	if c.ShouldBindJSON(&b) != nil || b.UserID <= 0 || len([]rune(b.Note)) < 3 || len(b.Note) > 2000 {
		c.AbortWithStatus(400)
		return
	}
	var e error
	switch b.Action {
	case "restore":
		e = model.OmniRiskRestoreV2(b.UserID, c.GetInt("id"), b.Version, b.Note, false)
	case "hold":
		var r model.OmniRiskControl
		r, e = model.OmniRiskReadControl(model.DB, b.UserID)
		if e == nil && r.Version != b.Version {
			e = errors.New("risk_version_conflict")
		}
		if e == nil {
			e = model.OmniRiskApplyV2(b.UserID, uuid.NewString(), "manual_confirmed", true, c.GetInt("id"), "", b.Version)
		}
	case "confirm":
		var r model.OmniRiskControl
		r, e = model.OmniRiskReadControl(model.DB, b.UserID)
		if e == nil && r.Version != b.Version {
			e = errors.New("risk_version_conflict")
		}
		if e == nil && r.State != "hold" && r.State != "confirmed" {
			e = model.OmniRiskApplyV2(b.UserID, uuid.NewString(), "manual_confirmed", true, c.GetInt("id"), "", b.Version)
			if e == nil {
				r, e = model.OmniRiskReadControl(model.DB, b.UserID)
			}
		}
		if e == nil {
			e = model.OmniRiskRestoreV2(b.UserID, c.GetInt("id"), r.Version, b.Note, true)
		}
	case "observe":
		e = model.DB.Transaction(func(tx *gorm.DB) error {
			q := tx.Model(&model.OmniRiskControl{}).Where("user_id = ? AND version = ?", b.UserID, b.Version).Updates(map[string]any{"note": b.Note, "version": b.Version + 1})
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return errors.New("risk_version_conflict")
			}
			if e := tx.Model(&model.OmniRiskAppeal{}).Where("user_id = ? AND reviewed_at = 0", b.UserID).Updates(map[string]any{"reviewed_by": c.GetInt("id"), "reviewed_at": time.Now().Unix(), "decision": "continue_protection"}).Error; e != nil {
				return e
			}
			return tx.Create(&model.OmniRiskActionV2{ID: uuid.NewString(), UserID: b.UserID, ActorID: c.GetInt("id"), Action: "observe", Note: b.Note, CreatedAt: time.Now().Unix()}).Error
		})
	default:
		e = errors.New("invalid risk action")
	}
	riskV2AdminReply(c, gin.H{"saved": e == nil}, e)
}
func OmniRiskV2IPAction(c *gin.Context) {
	if !riskV2Origin(c) {
		return
	}
	var b struct {
		Version int64  `json:"version"`
		IP      string `json:"ip"`
		Tag     string `json:"tag"`
		Revoke  bool   `json:"revoke"`
		Minutes int    `json:"minutes"`
		Note    string `json:"note"`
	}
	if c.ShouldBindJSON(&b) != nil || len(b.Note) < 3 || (b.Minutes != 0 && b.Minutes != 15) {
		c.AbortWithStatus(400)
		return
	}
	tag := b.Tag
	if b.IP != "" {
		tag = model.OmniRiskNetworkTag(b.IP)
	}
	if decoded, e := hex.DecodeString(tag); e != nil || len(decoded) != 12 {
		c.AbortWithStatus(400)
		return
	}
	now := time.Now().Unix()
	e := model.DB.Transaction(func(tx *gorm.DB) error {

		var old model.OmniRiskIP
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tag = ?", tag).Find(&old).Error; e != nil {
			return e
		}
		if old.Version != b.Version {
			return errors.New("risk_version_conflict")
		}
		if b.Revoke {
			q := tx.Model(&model.OmniRiskIP{}).Where("tag = ? AND version = ?", tag, b.Version).Updates(map[string]any{"revoked_at": now, "version": b.Version + 1})
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return errors.New("risk_version_conflict")
			}
		} else {
			expires := int64(0)
			if b.Minutes > 0 {
				expires = now + int64(b.Minutes*60)
			}
			if old.Tag == "" {
				if e := tx.Create(&model.OmniRiskIP{Tag: tag, Version: 1, Source: "manual", ExpiresAt: expires, CreatedAt: now}).Error; e != nil {
					return e
				}
			} else {
				q := tx.Model(&model.OmniRiskIP{}).Where("tag = ? AND version = ?", tag, b.Version).Updates(map[string]any{"version": b.Version + 1, "source": "manual", "expires_at": expires, "created_at": now, "revoked_at": 0})
				if q.Error != nil {
					return q.Error
				}
				if q.RowsAffected != 1 {
					return errors.New("risk_version_conflict")
				}
			}
		}

		sum := sha256.Sum256([]byte(tag))
		return tx.Create(&model.OmniRiskActionV2{ID: uuid.NewString(), ActorID: c.GetInt("id"), Action: "ip", Note: hex.EncodeToString(sum[:8]) + ":" + b.Note, CreatedAt: now}).Error
	})
	riskV2AdminReply(c, gin.H{"saved": e == nil}, e)
}

var _ = http.StatusOK

func riskV2AdminReply(c *gin.Context, data any, err error) {
	if err != nil {
		for _, item := range []struct{ Code, Message string }{
			{"risk_version_conflict", "记录已被更新，请刷新后重新复核。"},
			{"risk_activation_not_verified", "发布验收尚未完成或已经过期，请先完成受控发布验证。"},
			{"risk_token_restore_conflict", "Key 已有其他变更，未执行恢复，请转人工核对。"},
			{"risk_restore_conflict", "账号已有其他限制或版本变更，未执行恢复，请转人工核对。"},
			{"risk_existing_restriction_conflict", "账号已存在其他限制，请转人工核对。"},
		} {
			if strings.Contains(err.Error(), item.Code) {
				c.Header("Cache-Control", "no-store")
				c.JSON(409, gin.H{"success": false, "code": item.Code, "message": item.Message})
				return
			}
		}
	}
	riskReply(c, data, err)
}

func riskV2SourceLimited(c *gin.Context, uid int) (bool, error) {
	tag := model.OmniRiskNetworkTag(c.ClientIP())
	if tag == "" {
		return false, nil
	}
	if tag == "risk_source_unavailable" {
		return false, errors.New("risk_source_unavailable")
	}
	var rules []model.OmniRiskIP
	if e := model.DB.Where("tag = ? AND revoked_at = 0 AND (expires_at = 0 OR expires_at > ?)", tag, time.Now().Unix()).Find(&rules).Error; e != nil {
		return false, e
	}
	if len(rules) == 0 {
		return false, nil
	}
	for _, r := range rules {
		if r.Source == "manual" {
			return true, nil
		}
	}
	paid, e := model.OmniRiskPaid(model.DB, uid)
	return !paid, e
}
