package controller

import (
	"bytes"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	hostdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"slices"
	"strings"
)

// Only these stateless transports enter the OpenAI routing coordinator. Native
// provider adaptors are selected later, separately for each verified attempt.
func omniCoordinatorRequest(c *gin.Context, info *relaycommon.RelayInfo) bool {
	if c.Request == nil || c.Request.URL == nil {
		return false
	}
	switch c.Request.URL.Path {
	case "/v1/chat/completions", "/v1/messages":
		return true
	case "/v1/responses":
		request, ok := info.Request.(*dto.OpenAIResponsesRequest)
		if !ok || request.PreviousResponseID != "" {
			return false
		}
		conversation := bytes.TrimSpace(request.Conversation)
		return len(conversation) == 0 || bytes.Equal(conversation, []byte("null"))
	}
	return false
}

func omniPrimary(c *gin.Context, info *relaycommon.RelayInfo) (*model.Channel, bool, *types.NewAPIError) {
	info.OmniNativeClaude = false
	p := service.OmniRoutingFor(info)
	if !p.Enabled || !service.OmniRoutingUserAllowed(p, info.UserId) {
		return nil, false, nil
	}
	constraints := service.GetChannelConstraints(c)
	pin, pinned, _ := constraints.ResolvedPin()
	ids := p.Models[info.OriginModelName]
	if len(ids) == 0 {
		return nil, false, nil
	}
	if _, enabled := p.WaitBudgetFor(info.OriginModelName); omniStatefulResponse(c, info) && (enabled || service.OmniVerifiedPathsOnly(info, ids[0])) {
		// Middleware has already selected this request's supplier. A new
		// primary order must not move an opaque response/conversation ID, and
		// an outer retry must not replay it to another supplier either.
		id := c.GetInt("channel_id")
		if info.ChannelMeta != nil && info.ChannelId > 0 {
			if id > 0 && id != info.ChannelId {
				return nil, true, types.NewError(errors.New("stateful response channel binding conflict"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
			}
			id = info.ChannelId
		}
		if id <= 0 || pinned && pin.ChannelId != id {
			return nil, true, types.NewError(errors.New("stateful response channel binding unavailable"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		selected, err := model.CacheGetChannel(id)
		if err != nil || selected == nil || selected.Status != common.ChannelStatusEnabled {
			return nil, true, types.NewError(errors.New("stateful response channel binding unavailable"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		if !pinned || pin.RetryMode != hostdto.PinRetrySingleAttempt {
			constraints.AddPin(hostdto.ChannelPin{ChannelId: id, Source: hostdto.PinSourceOriginTask, Rank: hostdto.PinRankOriginTask, RetryMode: hostdto.PinRetrySingleAttempt})
		}
		// getChannel's legacy metadata path can select another random channel;
		// return the already selected channel without mutating its context/meta.
		return selected, true, nil
	}
	if pinned {
		return nil, false, nil
	}
	coordinated := omniCoordinatorRequest(c, info)
	info.OmniNativeClaude = coordinated && info.RelayFormat == types.RelayFormatClaude && service.OmniNativeClaudeFor(info)
	var envelopeFailure *model.OmniBindingRejection
	ch, err := model.CacheGetChannel(ids[0])
	if image, ok := info.Request.(*dto.ImageRequest); ok {
		ch = nil
		for _, id := range ids {
			candidate, e := model.CacheGetChannel(id)
			if e == nil && candidate != nil && candidate.Status == common.ChannelStatusEnabled && candidate.Type == constant.ChannelTypeOpenAI && strings.Contains(","+candidate.Group+",", ","+info.UsingGroup+",") && omniImageBindingAllows(info, image, id) {
				ch, err = candidate, nil
				break
			}
		}
	}
	if coordinated {
		// This is only the request-local conversion bootstrap. The coordinator
		// still attempts the original policy order, including circuit checks and
		// disabled/missing-route audit evidence, before sending to a provider.
		ch = nil
		for _, id := range ids {
			if !service.OmniSharedChannelAllowed(info, id) {
				continue
			}
			candidate, candidateErr := model.CacheGetChannel(id)
			if candidateErr == nil && candidate != nil && candidate.Status == common.ChannelStatusEnabled && (candidate.Type == constant.ChannelTypeOpenAI || candidate.Type == constant.ChannelTypeAnthropic) && strings.Contains(","+candidate.Group+",", ","+info.UsingGroup+",") {
				if info.OmniNativeClaude && !model.OmniChannelNativeClaude(info.OmniRegistryVersion, info.OriginModelName, id, candidate.Type) {
					continue
				}
				if failure := service.OmniDeclaredRequestReject(info, id); failure != nil {
					envelopeFailure = failure
					continue
				}
				if request, ok := info.Request.(*dto.ClaudeRequest); ok {
					if !model.OmniNativeToolChoiceAllows(info.OmniRegistryVersion, info.OriginModelName, id, request.ToolChoice) {
						continue
					}
					required := model.OmniNativeToolCapabilities(request.Tools)
					if len(required) > 0 && service.OmniBindingRequestReject(info, id, required) != nil {
						continue
					}
				}
				ch, err = candidate, nil
				break
			}
		}
	}
	if err != nil || ch == nil || ch.Status != common.ChannelStatusEnabled || (ch.Type != constant.ChannelTypeOpenAI && ch.Type != constant.ChannelTypeAnthropic) || !strings.Contains(","+ch.Group+",", ","+info.UsingGroup+",") {
		if ch == nil && envelopeFailure != nil {
			status := 400
			if envelopeFailure.Reason == "request_body_too_large" {
				status = 413
			}
			return nil, true, types.NewError(envelopeFailure, types.ErrorCode(envelopeFailure.Reason), types.ErrOptionWithStatusCode(status), types.ErrOptionWithSkipRetry())
		}
		return nil, true, types.NewError(errors.New("verified primary route unavailable"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	if ch.Type == constant.ChannelTypeAnthropic && !coordinated {
		// Stateful Responses and other transports retain their original binding.
		return nil, false, nil
	}
	selected := ch
	if coordinated {
		coordinator := *ch
		coordinator.Type = constant.ChannelTypeOpenAI
		baseURL := ch.GetBaseURL()
		coordinator.BaseURL = &baseURL
		selected = &coordinator
	}
	if apiErr := middleware.SetupContextForSelectedChannel(c, selected, info.OriginModelName); apiErr != nil {
		return nil, true, apiErr
	}
	if coordinated {
		// Native ingress may infer Anthropic from the binding on this
		// bootstrap channel. The coordinator selects each native adaptor
		// itself, so keep the bootstrap on the OpenAI adaptor.
		common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	}
	common.SetContextKey(c, "omni_primary_native_type", ch.Type)
	info.ChannelMeta = nil
	info.InitChannelMeta(c)
	info.DisablePing = true
	return ch, true, nil
}

func omniStatefulResponse(c *gin.Context, info *relaycommon.RelayInfo) bool {
	if c.Request == nil || c.Request.URL == nil || c.Request.URL.Path != "/v1/responses" {
		return false
	}
	request, ok := info.Request.(*dto.OpenAIResponsesRequest)
	if !ok {
		return false
	}
	conversation := bytes.TrimSpace(request.Conversation)
	return request.PreviousResponseID != "" || len(conversation) > 0 && !bytes.Equal(conversation, []byte("null"))
}

func omniImageBindingAllows(info *relaycommon.RelayInfo, image *dto.ImageRequest, id int) bool {
	registry := model.OmniRegistryAt(info.OmniRegistryVersion)
	if !registry.Enabled {
		return true
	}
	required := "image_spec:" + image.Size + ":" + image.Quality
	for _, binding := range registry.Models[registry.Canonical(info.OriginModelName)].Bindings {
		if binding.ChannelID != id || binding.Status != "verified" {
			continue
		}
		if strings.HasSuffix(strings.Split(info.RequestURLPath, "?")[0], "/images/edits") {
			return slices.Contains(binding.Supports, "image_edit_spec:"+image.Size+":"+image.Quality)
		}
		tagged := false
		for _, capability := range binding.Supports {
			if strings.HasPrefix(capability, "image_spec:") {
				tagged = true
			}
		}
		if tagged {
			return slices.Contains(binding.Supports, required)
		}
		// Compatibility for the previously accepted single-spec image provider.
		return image.Size == "1024x1024" && slices.Contains([]string{"low", "medium", "high"}, image.Quality)
	}
	return false
}
