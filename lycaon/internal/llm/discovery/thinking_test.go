package discovery

import (
	"encoding/json"
	"testing"
)

func TestOpenRouterThinkingMetadata(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		state   string
		count   int
		disable bool
	}{
		{`{"supported_efforts":["low","high","max"],"mandatory":true,"default_effort":"max"}`, "supported", 3, false},
		{`{"supported_efforts":null}`, "supported", 6, true},
		{`{}`, "supported", 0, true},
		{`{"supported_efforts":["none","low"]}`, "supported", 1, true},
		{`{"supported_efforts":"high"}`, "unknown", 0, false},
		{`{"supported_efforts":["high","high"]}`, "unknown", 0, false},
	} {
		var metadata openRouterThinking
		if err := json.Unmarshal([]byte(tc.raw), &metadata); err != nil {
			t.Fatalf("decode fixture: %v", err)
		}
		got := metadata.capabilities("custom")
		if got.State != tc.state || len(got.Efforts) != tc.count || got.CanDisable != tc.disable {
			t.Fatalf("%s: %+v", tc.raw, got)
		}
	}
}
