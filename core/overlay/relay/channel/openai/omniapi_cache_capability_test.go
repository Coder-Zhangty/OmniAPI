package openai

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestOmniExplicitCacheRequiresQuotedSupplierDimension(t *testing.T) {
	for _,tc:=range []struct{body,required string}{
		{`{"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`,"cache_write_1h"},
		{`{"system":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}`,"cache_write"},
		{`{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"parameters":{"properties":{"cache_control":{"type":"object"}}}}}]}`,""},
	}{
		var raw map[string]any;_ = json.Unmarshal([]byte(tc.body),&raw)
		got:=omniRequiredCapabilities(raw)
		if tc.required!=""&&!slices.Contains(got,tc.required){t.Fatalf("missing priced cache capability: %v",got)}
		if tc.required==""&&(slices.Contains(got,"cache_write")||slices.Contains(got,"cache_write_1h")){t.Fatal("tool schema interpreted as cache purchase")}
	}
}
