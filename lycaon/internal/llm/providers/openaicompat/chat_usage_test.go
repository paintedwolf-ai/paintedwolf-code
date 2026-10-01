package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestChatUsagePreservesWritesAndExplicitZero(t *testing.T) {
	for _, raw := range []string{
		`{"prompt_tokens":1000,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":400,"cache_write_tokens":200}}`,
		`{"prompt_tokens":1000,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":400},"cache_creation_input_tokens":200}`,
	} {
		var wire Usage
		testutil.FailErr(t, "decode chat cache usage", json.Unmarshal([]byte(raw), &wire))
		u := NormalizeUsage(&wire)
		if u.PromptTokens != 1000 || u.CacheReadInputTokens != 400 || u.CacheCreationInputTokens != 200 {
			t.Fatalf("usage = %+v", u)
		}
	}
	if NormalizeUsage(nil).Reported() {
		t.Fatal("missing usage treated as reported")
	}
	if !NormalizeUsage(&Usage{PromptTokens: new(0), CompletionTokens: new(0)}).Reported() {
		t.Fatal("explicit zero usage treated as missing")
	}
}

func TestChatUsageDistinguishesMissingCountersFromZero(t *testing.T) {
	for _, tc := range []struct {
		raw                  string
		reported, incomplete bool
	}{
		{`{}`, false, true},
		{`{"prompt_tokens":0}`, true, true},
		{`{"prompt_tokens":0,"completion_tokens":0}`, true, false},
	} {
		var wire Usage
		testutil.FailErr(t, "decode usage presence", json.Unmarshal([]byte(tc.raw), &wire))
		got := NormalizeUsage(&wire)
		if got.Reported() != tc.reported || got.Incomplete != tc.incomplete {
			t.Fatalf("%s: usage=%+v", tc.raw, got)
		}
	}
}
