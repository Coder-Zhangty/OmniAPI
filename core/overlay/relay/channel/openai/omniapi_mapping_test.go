package openai

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOmniMappedModel(t *testing.T) {
	original := "test-model"
	if got, err := omniMappedModel(nil, original); err != nil || got != original {
		t.Fatal(got, err)
	}
	for _, item := range []struct {
		value, want string
		invalid     bool
	}{
		{"", original, false}, {" \t\n", original, false}, {"null", original, false}, {"{}", original, false},
		{`{"test-model":"upstream-model"}`, "upstream-model", false},
		{`{"other":"different"}`, original, false}, {`{"test-model":""}`, original, false},
		{`{"test-model":123}`, "", true}, {`["wrong"]`, "", true}, {`{"private-config":`, "", true},
	} {
		t.Run(item.value, func(t *testing.T) {
			got, err := omniMappedModel(&item.value, original)
			if (err != nil) != item.invalid || got != item.want {
				t.Fatalf("model=%q error=%v", got, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-config") {
				t.Fatal("configuration leaked")
			}
		})
	}
}

func TestOmniEmptyMappingBackupActuallyRequested(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "json"}[stream], func(t *testing.T) {
			received := false
			c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 500) }, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "test-model" {
					t.Error("original model not preserved", err)
				}
				received = true
				if stream {
					omniSend(w, "backup", "recovered", true)
				} else {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"id":"backup","choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2}}`)
				}
			})
			info.IsStream = stream
			ids := service.GetOmniRouting().Models[info.OriginModelName]
			if err := model.DB.Model(&model.Channel{}).Where("id = ?", ids[1]).Update("model_mapping", "").Error; err != nil {
				t.Fatal(err)
			}
			body := `{"model":"test-model","stream":false,"messages":[{"role":"user","content":"hello"}]}`
			if stream {
				body = strings.Replace(body, "false", "true", 1)
			}
			resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || !received || !strings.Contains(string(data), "recovered") {
				t.Fatal("backup not recovered", err)
			}
			attempts := model.OmniRequestAttempts(info.RequestId)
			if len(attempts) != 2 || attempts[1].Outcome != "completed" {
				t.Fatalf("attempts=%+v", attempts)
			}
			other := model.NewLogOther()
			c.Set("use_channel", []string{"outer-primary"})
			service.AppendRelayLogAdminInfo(c, info, other)
			var logged map[string]any
			json.Unmarshal([]byte(other.JSONString()), &logged)
			admin := logged["admin_info"].(map[string]any)
			chain := admin["use_channel"].([]any)
			if len(chain) != 2 || admin["channel_attempts"] == nil {
				t.Fatal("actual failover not visible to administrator", admin)
			}
			if logged["channel_attempts"] != nil || logged["use_channel"] != nil {
				t.Fatal("private routes exposed")
			}
		})
	}
}

func TestOmniFailedRouteLogIncludesAllCandidates(t *testing.T) {
	bad := func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 500) }
	c, info := omniHTTPFixture(t, bad, bad)
	_, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err == nil {
		t.Fatal("all failed request accepted")
	}
	other := model.NewLogOther()
	service.AppendRelayLogAdminInfo(c, info, other)
	var logged map[string]any
	json.Unmarshal([]byte(other.JSONString()), &logged)
	admin := logged["admin_info"].(map[string]any)
	if len(admin["use_channel"].([]any)) != 2 || len(admin["channel_attempts"].([]any)) != 2 {
		t.Fatal("failed log omitted backup")
	}
}
