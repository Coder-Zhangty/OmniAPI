package controller

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed omniapi_risk_page.html
var omniRiskPage []byte

func OmniRiskPage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(200, "text/html; charset=utf-8", omniRiskPage)
}

func riskReply(c *gin.Context, data any, err error) {
	c.Header("Cache-Control", "no-store")
	if err != nil {
		c.JSON(503, gin.H{"success": false, "message": "风控数据暂不可用，请查看运行状态。"})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": data})
}
func OmniRiskSummary(c *gin.Context) {
	settings, err := model.GetOmniRiskSettings()
	if err != nil {
		riskReply(c, nil, err)
		return
	}
	day := model.OmniRiskDay{Day: model.OmniRiskDayKey(time.Now())}
	model.DB.Where("day = ?", day.Day).First(&day)
	counts := map[string]int64{}
	for _, state := range []string{"pending", "inflight", "unknown", "failed", "reviewed", "historically_handled"} {
		var n int64
		if err := model.DB.Model(&model.OmniRiskSubject{}).Where("state = ?", state).Count(&n).Error; err != nil {
			riskReply(c, nil, err)
			return
		}
		counts[state] = n
	}
	var runs []model.OmniRiskRun
	err = model.DB.Order("started_at DESC").Limit(24).Find(&runs).Error
	var incidents []model.OmniRiskIncident
	model.DB.Where("active = ?", true).Find(&incidents)
	var notifications []model.OmniRiskOutbox
	model.DB.Order("created_at DESC").Limit(30).Find(&notifications)
	var balance struct{ Quota int64 }
	model.DB.Table("users").Select("quota").Where("id = ?", settings.ServiceUserID).Scan(&balance)
	p, policyErr := model.OmniRiskV2Policy()
	if err == nil {
		err = policyErr
	}
	riskReply(c, gin.H{"settings": settings, "day": day, "daily_limit_micros": model.OmniRiskDailyMicros, "model": model.OmniRiskModel, "policy_version": service.OmniRiskPolicyVersion, "prompt_sha": model.OmniRiskDigest([]byte(service.OmniRiskPrompt)), "queue": counts, "runs": runs, "incidents": incidents, "notifications": notifications, "service_quota": balance.Quota, "retention_days": 90, "lookback_days": 30, "enforcement_enabled": !p.Shadow && (p.AutoHold || p.NewUserProtection || p.TemporaryIP)}, err)
}
func OmniRiskPolicy(c *gin.Context) {
	riskReply(c, gin.H{"version": service.OmniRiskPolicyVersion, "prompt": service.OmniRiskPrompt, "actions": service.OmniRiskActionLabels, "daily_limit_micros": model.OmniRiskDailyMicros, "model": model.OmniRiskModel, "same_ip_is_not_identity": true, "false_positive_exemption": false}, nil)
}
func OmniRiskCases(c *gin.Context) {
	if id := c.Param("id"); id != "" {
		var row model.OmniRiskCase
		if model.DB.First(&row, "id = ?", id).Error != nil {
			c.JSON(404, gin.H{"success": false, "message": "案件不存在或已超过保留期。"})
			return
		}
		var report any
		var evidence any
		json.Unmarshal([]byte(row.Report), &report)
		json.Unmarshal([]byte(row.Evidence), &evidence)
		var actions []model.OmniRiskAction
		model.DB.Where("case_id = ?", id).Order("created_at,id").Find(&actions)
		riskReply(c, gin.H{"case": row, "report": report, "evidence": evidence, "actions": actions}, nil)
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 || page > 200 {
		page = 1
	}
	q := model.DB.Model(&model.OmniRiskCase{})
	status := c.Query("status")
	if status == "open" || status == "observing" || status == "closed" {
		q = q.Where("status = ?", status)
	}
	var total int64
	err := q.Count(&total).Error
	var rows []model.OmniRiskCase
	if err == nil {
		err = q.Order("updated_at DESC,id").Offset((page - 1) * 30).Limit(30).Find(&rows).Error
	}
	riskReply(c, gin.H{"items": rows, "total": total, "page": page}, err)
}
func OmniRiskQueue(c *gin.Context) {
	var rows []model.OmniRiskSubject
	err := model.DB.Where("state <> ?", "reviewed").Order("priority DESC,changed_at,user_id").Limit(100).Find(&rows).Error
	riskReply(c, rows, err)
}

func riskWriteAllowed(c *gin.Context) bool {
	origin := c.GetHeader("Origin")
	if origin != "" && origin != "https://omniapi.biz" && origin != "https://www.omniapi.biz" {
		c.AbortWithStatus(http.StatusForbidden)
		return false
	}
	if !strings.HasPrefix(c.ContentType(), "application/json") {
		c.AbortWithStatus(415)
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	return true
}
func OmniRiskControl(c *gin.Context) {
	if !riskWriteAllowed(c) {
		return
	}
	var body struct {
		Enabled         *bool `json:"enabled"`
		ExpectedVersion int64 `json:"expected_version"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Enabled == nil {
		c.JSON(400, gin.H{"success": false, "message": "参数错误。"})
		return
	}
	if *body.Enabled {
		var n int64
		model.DB.Model(&model.OmniRiskRun{}).Where("kind = ? AND state = ?", "acceptance", "completed").Count(&n)
		if n == 0 {
			c.JSON(409, gin.H{"success": false, "message": "须先完成首次真实模型验收，计入当天1元额度。"})
			return
		}
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&model.OmniRiskSettings{}).Where("id = 1 AND version = ?", body.ExpectedVersion).Updates(map[string]any{"enabled": *body.Enabled, "notifications": true, "version": gorm.Expr("version + 1")})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return fmt.Errorf("risk_configuration_changed")
		}
		return tx.Create(&model.OmniRiskAction{CreatedAt: time.Now().Unix(), ActorID: c.GetInt("id"), Action: "control", Note: fmt.Sprintf("enabled=%v", *body.Enabled)}).Error
	})
	if err != nil {
		c.JSON(409, gin.H{"success": false, "message": "配置已变化，请刷新后重试。"})
		return
	}
	riskReply(c, gin.H{"enabled": *body.Enabled}, nil)
}
func OmniRiskRunNow(c *gin.Context) {
	if !riskWriteAllowed(c) {
		return
	}
	var body struct {
		Key        string `json:"key"`
		Acceptance bool   `json:"acceptance"`
	}
	if c.ShouldBindJSON(&body) != nil || len(body.Key) < 8 || len(body.Key) > 64 || strings.ContainsAny(body.Key, " /\\\r\n") {
		c.JSON(400, gin.H{"success": false, "message": "需要有效的幂等请求编号。"})
		return
	}
	s, err := model.GetOmniRiskSettings()
	if err != nil {
		riskReply(c, nil, err)
		return
	}
	if !s.Enabled && !body.Acceptance {
		c.JSON(409, gin.H{"success": false, "message": "审查已暂停，请先恢复。"})
		return
	}
	run := model.OmniRiskRun{ID: "manual:" + body.Key, Kind: "manual", State: "queued", StartedAt: time.Now().Unix(), PolicyVersion: service.OmniRiskPolicyVersion, PromptSHA: model.OmniRiskDigest([]byte(service.OmniRiskPrompt))}
	if body.Acceptance {
		run.Kind = "acceptance"
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		// Config lock serializes manual submissions; the hourly worker lease still
		// serializes execution with automatic jobs and the global daily budget.
		var locked model.OmniRiskSettings
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, 1).Error; err != nil {
			return err
		}
		var active model.OmniRiskRun
		if tx.Where("state IN ?", []string{"queued", "running"}).Order("started_at").First(&active).Error == nil {
			run = active
			return nil
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&run).Error; err != nil {
			return err
		}
		return tx.Create(&model.OmniRiskAction{CreatedAt: time.Now().Unix(), ActorID: c.GetInt("id"), Action: "run", Note: run.ID}).Error
	})
	riskReply(c, run, err)
}
func OmniRiskCaseDecision(c *gin.Context) {
	if !riskWriteAllowed(c) {
		return
	}
	var body struct {
		Verdict          string `json:"verdict"`
		Note             string `json:"note"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if c.ShouldBindJSON(&body) != nil || len([]rune(body.Note)) > 1000 || (body.Verdict != "confirmed" && body.Verdict != "false_positive" && body.Verdict != "observe") {
		c.JSON(400, gin.H{"success": false, "message": "需要选择确认风险、误报或继续观察，并填写不超过1000字的备注。"})
		return
	}
	id := c.Param("id")
	status := "closed"
	closed := time.Now().Unix()
	if body.Verdict == "observe" {
		status = "observing"
		closed = 0
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&model.OmniRiskCase{}).Where("id = ? AND revision = ?", id, body.ExpectedRevision).Updates(map[string]any{"status": status, "verdict": body.Verdict, "note": body.Note, "reviewed_by": c.GetInt("id"), "closed_at": closed, "updated_at": time.Now().Unix(), "revision": gorm.Expr("revision + 1")})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return fmt.Errorf("risk_case_changed")
		}
		if status == "closed" {
			if err := tx.Model(&model.OmniRiskOutbox{}).Where("case_id = ? AND state = ?", id, "pending").Updates(map[string]any{"state": "cancelled", "error_code": "case_closed_before_delivery"}).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.OmniRiskAction{CreatedAt: time.Now().Unix(), ActorID: c.GetInt("id"), Action: body.Verdict, CaseID: id, Note: body.Note}).Error
	})
	if err != nil {
		c.JSON(409, gin.H{"success": false, "message": "案件已变化，请刷新再处理。"})
		return
	}
	riskReply(c, gin.H{"status": status, "account_or_balance_changed": false, "exemption_created": false}, nil)
}
