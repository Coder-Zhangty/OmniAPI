package controller

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"time"
)

func OmniRoutingAdmin(c *gin.Context) {
	var rows []struct {
		Model              string  `json:"model"`
		ChannelID          int     `json:"channel_id"`
		Outcome            string  `json:"outcome"`
		Count              int64   `json:"count"`
		AverageFirstMillis float64 `json:"average_first_millis"`
		CostUSD            float64 `json:"estimated_cost_usd"`
	}
	err := model.DB.Model(&model.OmniAttempt{}).Select("model, channel_id, outcome, COUNT(*) AS count, AVG(first_millis) AS average_first_millis, SUM(cost_usd) AS cost_usd").Where("created_at > ?", time.Now().Unix()-86400).Group("model,channel_id,outcome").Scan(&rows).Error
	omniReply(c, gin.H{"policy": service.GetOmniRouting(), "last_24_hours": rows, "pricing_version": model.GetOmniPrices().Version}, err)
}
func OmniRoutingUpdate(c *gin.Context) {
	var next service.OmniRoutePolicy
	if c.ShouldBindJSON(&next) != nil {
		omniReply(c, nil, errors.New("invalid routing policy"))
		return
	}
	if next.FirstSeconds < 1 || next.FirstSeconds > 120 || next.IdleSeconds < 1 || next.IdleSeconds > 180 || next.FailureThreshold < 1 || next.FailureThreshold > 10 || next.CooldownSeconds < 1 || next.CooldownSeconds > 600 {
		omniReply(c, nil, errors.New("routing thresholds outside supported limits"))
		return
	}
	approved := model.GetOmniPrices()
	if next.Enabled {
		for name := range approved.Models {
			if len(next.Models[name]) == 0 {
				omniReply(c, nil, errors.New("enabled policy must retain every published model's primary route"))
				return
			}
		}
	}
	for name, ids := range next.Models {
		var verified struct {
			Routes []int `json:"routes"`
		}
		if json.Unmarshal(approved.Models[name], &verified) != nil || len(ids) == 0 || len(verified.Routes) == 0 || ids[0] != verified.Routes[0] {
			omniReply(c, nil, errors.New("model must retain its verified primary"))
			return
		}
		seen := map[int]bool{}
		for _, id := range ids {
			allowed := false
			for _, v := range verified.Routes {
				if v == id {
					allowed = true
				}
			}
			if !allowed || seen[id] {
				omniReply(c, nil, errors.New("only unique accepted routes may be configured"))
				return
			}
			seen[id] = true
		}
	}
	for _, uid := range next.RolloutUserIDs {
		if uid <= 0 {
			omniReply(c, nil, errors.New("invalid rollout user"))
			return
		}
	}
	current := service.GetOmniRouting()
	next.Costs = current.Costs
	// Older admin clients do not send model-specific deadlines. Keep the
	// accepted overrides when saving weights and circuit thresholds.
	next.FirstSecondsByModel = current.FirstSecondsByModel
	next.WaitBudgets = current.WaitBudgets
	next.FallbackOnlyChannels = current.FallbackOnlyChannels
	next.PrimaryExceptions = current.PrimaryExceptions
	if err := service.ValidateOmniSupplierOrder(next); err != nil {
		omniReply(c, nil, err)
		return
	}
	data, err := json.Marshal(next)
	if err == nil {
		err = model.UpdateOption("OmniAPIRouting", string(data))
	}
	omniReply(c, next, err)
}
