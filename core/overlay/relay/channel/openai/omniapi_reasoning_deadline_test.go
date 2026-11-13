package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
)

// A reasoning frame is not yet visible content: the route wrapper still owns
// the response. It must not replace a longer initial deadline with a short
// post-commit idle timeout while the client has received nothing.
func TestOmniReasoningDoesNotShortenInitialDeadline(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Thinking\"}}]}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(1400 * time.Millisecond)
		omniSend(w, "reasoned", "accepted", true)
	})
	p := service.GetOmniRouting()
	p.FirstSecondsByModel = map[string]int{"test-model": 3}
	encoded, _ := json.Marshal(p)
	common.OptionMap["OmniAPIRouting"] = string(encoded)
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"synthetic"}]}`))
	if err != nil {
		t.Fatal("reasoning shortened the initial content deadline:", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "accepted") {
		t.Fatal(string(data), err)
	}
}
