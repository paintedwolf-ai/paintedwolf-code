package toolvocabtest_test

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
)

// An argument is read only where text attributes it to a known tool, and is
// checked against that tool's schema alone: a parameter the schema dropped or
// never had is caught, and the tool's own parameter passes.
func TestUnacceptedArgsChecksArgumentsAttributedToATool(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, argProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)
	// source_history's schema as it stands once a timeout_ms parameter is
	// dropped; recall reports none.
	schemas := map[string]map[string]any{
		"source_history": {"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string"}}},
	}
	schema := func(tool string) (map[string]any, bool) {
		s, ok := schemas[tool]
		return s, ok
	}
	for name, tc := range map[string]struct {
		text string
		want []toolvocabtest.ArgClaim
	}{
		"call with an unknown argument": {
			text: "Call `source_history(path=\"a.go\", wait_ms=500)` first.",
			want: []toolvocabtest.ArgClaim{{Tool: "source_history", Arg: "wait_ms"}},
		},
		"colon form and dotted path": {
			text: "Pass `source_history(depth: 2)`; `source_history.since` is ignored.",
			want: []toolvocabtest.ArgClaim{{Tool: "source_history", Arg: "depth"}, {Tool: "source_history", Arg: "since"}},
		},
		"accepted arguments":             {text: "Use `source_history(path=\"a=b\", mode=diff)` or `source_history.path`."},
		"unknown tool is not attributed": {text: "Call `frobnicate(wait_ms=1)`."},
		"dropped argument": {
			text: "Call `source_history(path=\"a.go\", timeout_ms=500)`; raise `source_history.timeout_ms` when slow.",
			want: []toolvocabtest.ArgClaim{{Tool: "source_history", Arg: "timeout_ms"}},
		},
		"own argument":                   {text: "Call `source_history(mode=diff)`."},
		"tool without a reported schema": {text: "Call `recall(wait_ms=1)`."},
		"prose parentheses":              {text: "The history (see source_history) has no arguments here."},
	} {
		t.Run(name, func(t *testing.T) {
			if got := toolvocabtest.UnacceptedArgs(catalog, tc.text, schema); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("UnacceptedArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

func argProfiles() []sandbox.ToolProfile {
	return []sandbox.ToolProfile{
		{ID: "explore_readonly", Tools: map[string]bool{"recall": true, "source_history": true}},
		{ID: "acme_narrow", Tools: map[string]bool{"recall": true}},
	}
}
