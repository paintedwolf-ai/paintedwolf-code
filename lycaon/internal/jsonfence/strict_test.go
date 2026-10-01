package jsonfence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/jsonfence"
)

func TestParseStrict(t *testing.T) {
	t.Parallel()

	const envelope = `{"k":1}`
	decode := func(candidate string) (string, bool) {
		if candidate == envelope {
			return candidate, true
		}
		return "", false
	}

	if got, ok := jsonfence.ParseStrict("```json\n"+envelope+"\n```", decode); !ok || got != envelope {
		t.Fatalf("ParseStrict = %q, ok=%v", got, ok)
	}
	if _, ok := jsonfence.ParseStrict("intro\n"+envelope, decode); ok {
		t.Fatal("hybrid content must not parse strict")
	}
}
