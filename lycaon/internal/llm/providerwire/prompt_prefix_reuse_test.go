package providerwire

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/observability"
)

func capturedRequest(model, body string) modelcall.CompletionRequest {
	capture := &modelcall.RequestControlCapture{}
	capture.Record([]byte(body))
	return modelcall.CompletionRequest{Model: model, ControlCapture: capture}
}

func TestPrefixMeasurementLabelsItsUnits(t *testing.T) {
	const body = `{"max_tokens":10,"tools":[{"name":"read"}],"messages":[{"role":"user","content":"hello"}]}`
	prev := measurePrefixReuse(prefixBaseline{}, false, capturedRequest("unknown-model", body), promptCacheIdentity{}, observability.PromptCacheObservation{})
	t.Run("proxy without a tokenizer", func(t *testing.T) {
		got := measurePrefixReuse(prev.baseline, true, capturedRequest("unknown-model", body), promptCacheIdentity{}, observability.PromptCacheObservation{})
		if got.method != prefixProxyMethod || got.reusableTokens != 0 || got.reusableBytes != got.inputBytes {
			t.Fatalf("measurement %+v", got)
		}
	})
	t.Run("tokens where the catalog declares an encoding", func(t *testing.T) {
		got := measurePrefixReuse(prev.baseline, true, capturedRequest("gpt-4o", body), promptCacheIdentity{}, observability.PromptCacheObservation{})
		if !strings.HasPrefix(got.method, "tokens:") || got.reusableTokens == 0 {
			t.Fatalf("measurement %+v", got)
		}
	})
	t.Run("no serialized body means no claim", func(t *testing.T) {
		got := measurePrefixReuse(prev.baseline, true, modelcall.CompletionRequest{Model: "unknown-model"}, promptCacheIdentity{}, observability.PromptCacheObservation{})
		if got.method != "" || got.inputBytes != 0 {
			t.Fatalf("measurement %+v", got)
		}
	})
}

// Transport key order and moving cache markers are not model input.
func TestPrefixMeasurementIgnoresTransportShape(t *testing.T) {
	first := capturedRequest("unknown-model", `{"max_tokens":10,"system":[{"text":"s","type":"text"}],"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]}]}`)
	second := capturedRequest("unknown-model", `{"max_tokens":99,"messages":[{"content":[{"text":"a","type":"text"}],"role":"user"},{"role":"assistant","content":"b"}],"system":[{"type":"text","text":"s"}]}`)
	prev := measurePrefixReuse(prefixBaseline{}, false, first, promptCacheIdentity{}, observability.PromptCacheObservation{})
	got := measurePrefixReuse(prev.baseline, true, second, promptCacheIdentity{}, observability.PromptCacheObservation{})
	if got.firstDifference != prefixAppended || got.reusableBytes != prev.inputBytes {
		t.Fatalf("measurement %+v, previous input %d", got, prev.inputBytes)
	}
}
