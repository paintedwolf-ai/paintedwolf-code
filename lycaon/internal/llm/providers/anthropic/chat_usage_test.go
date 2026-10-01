package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAnthropicUsagePreservesWriteLifetime(t *testing.T) {
	var wire Usage
	testutil.FailErr(t, "decode native cache usage", json.Unmarshal([]byte(`{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":400,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":100}}`), &wire))
	u := NormalizeUsage(&wire)
	if u.PromptTokens != 800 || u.CacheCreationInputTokens != 300 || u.CacheCreation1HInputTokens != 100 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestNativeUsageDistinguishesMissingCountersFromZero(t *testing.T) {
	for _, tc := range []struct {
		raw                  string
		reported, incomplete bool
	}{
		{`{}`, false, true},
		{`{"input_tokens":0}`, true, true},
		{`{"output_tokens":12}`, true, true},
		{`{"input_tokens":0,"output_tokens":0}`, true, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var wire Usage
			testutil.FailErr(t, "decode native usage presence", json.Unmarshal([]byte(tc.raw), &wire))
			completion, err := mapAnthropicResponse(&Response{Usage: &wire})
			testutil.FailErr(t, "map native usage", err)
			got := completion.Usage
			if got.Reported() != tc.reported || got.Incomplete != tc.incomplete {
				t.Fatalf("usage = %+v, want reported=%v incomplete=%v", got, tc.reported, tc.incomplete)
			}
		})
	}
}
