package oaichat_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/internal/claude_messages"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/internal/oai_chat"
	"github.com/stretchr/testify/require"
)

func TestOmniClaudeCacheControlsSurviveChatConversion(t *testing.T) {
	for _, ttl := range []string{"5m", "1h"} {
		t.Run(ttl, func(t *testing.T) {
			control := json.RawMessage(`{"type":"ephemeral","ttl":"` + ttl + `"}`)
			output := uint(64000)
			body := `{"model":"claude-sonnet-4-6","max_tokens":64000,"system":[{"type":"text","text":"Synthetic instructions","cache_control":` +
				string(control) + `}],"messages":[{"role":"user","content":[{"type":"text","text":"Synthetic content","cache_control":` +
				string(control) + `}]}]}`
			var original dto.ClaudeRequest
			require.NoError(t, json.Unmarshal([]byte(body), &original))
			chat, err := claudemessages.ClaudeMessagesRequestToOpenAIChat(context.Background(), original, nil)
			require.NoError(t, err)
			request := *chat
			// The coordinator serializes its normalized body before invoking
			// the target adapter. Reproduce that map-based content representation.
			wire, err := json.Marshal(request)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(wire, &request))
			converted, err := oaichat.OpenAIChatRequestToClaudeMessages(context.Background(), nil, request)
			require.NoError(t, err)
			encoded, err := json.Marshal(converted)
			require.NoError(t, err)
			var actual struct {
				System []struct{ CacheControl json.RawMessage `json:"cache_control"` } `json:"system"`
				Messages []struct{ Content []struct{ CacheControl json.RawMessage `json:"cache_control"` } `json:"content"` } `json:"messages"`
			}
			require.NoError(t, json.Unmarshal(encoded, &actual))
			require.Len(t, actual.System, 1)
			require.Len(t, actual.Messages, 1)
			require.Len(t, actual.Messages[0].Content, 1)
			require.JSONEq(t, string(control), string(actual.System[0].CacheControl))
			require.JSONEq(t, string(control), string(actual.Messages[0].Content[0].CacheControl))
			require.Equal(t, output, *converted.MaxTokens)
		})
	}
}
