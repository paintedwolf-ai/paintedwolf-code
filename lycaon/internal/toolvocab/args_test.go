package toolvocab_test

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

// An argument is read only where text attributes it to a known tool, and is
// checked against that tool's schema alone: a parameter no schema has is
// caught, and a known tool's real parameter passes.
func TestUnacceptedArgsChecksArgumentsAttributedToATool(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)
	schemas := map[string]map[string]any{
		"source_history": {"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string"}}},
	}
	schema := func(tool string) (map[string]any, bool) {
		s, ok := schemas[tool]
		return s, ok
	}
	for name, tc := range map[string]struct {
		text string
		want []toolvocab.ArgClaim
	}{
		"call with an unknown argument": {
			text: "Call `source_history(path=\"a.go\", wait_ms=500)` first.",
			want: []toolvocab.ArgClaim{{Tool: "source_history", Arg: "wait_ms"}},
		},
		"colon form and dotted path": {
			text: "Pass `source_history(depth: 2)`; `source_history.since` is ignored.",
			want: []toolvocab.ArgClaim{{Tool: "source_history", Arg: "depth"}, {Tool: "source_history", Arg: "since"}},
		},
		"accepted arguments":             {text: "Use `source_history(path=\"a=b\", mode=diff)` or `source_history.path`."},
		"unknown tool is not attributed": {text: "Call `frobnicate(wait_ms=1)`."},
		"tool without a reported schema": {text: "Call `recall(wait_ms=1)`."},
		"prose parentheses":              {text: "The history (see source_history) has no arguments here."},
	} {
		t.Run(name, func(t *testing.T) {
			if got := toolvocab.UnacceptedArgs(catalog, tc.text, schema); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("UnacceptedArgs = %v, want %v", got, tc.want)
			}
		})
	}
}
