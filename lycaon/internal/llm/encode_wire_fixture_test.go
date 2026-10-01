package llm

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
)

func encodeWire(t *testing.T, p *openaicompat.Provider, req modelcall.CompletionRequest) map[string]any {
	t.Helper()
	body, err := p.Prepare(req, true)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return wire
}
