package oar

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMutationCandidateRecoveryCopy(t *testing.T) {
	ensureCatalog(t)
	loader, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	rules, err := loader.LoadDir(hintsDir(t))
	testutil.FailErr(t, "load", err)
	pipeline := NewGuardPipeline(rules, loader, NewCounterStore())
	pipeline.EnableAnchor(AnchorToolRejected)
	for _, tool := range []string{"write", "edit", "replace_lines", "code_rewrite"} {
		for _, state := range []string{"new", "existing", "unknown"} {
			t.Run(tool+"/"+state, func(t *testing.T) {
				gc := NewGuardContext()
				gc.Invocation.Tool = tool
				gc.ObservedRejectCode = "MUTATION_BROKE_PARSE"
				data := map[string]any{"path": "source", "parse_error": "line 2: missing token"}
				if state != "unknown" {
					data["mutation_new_path"] = state == "new"
				}
				gc.PutRejectData("MUTATION_BROKE_PARSE", data)
				result, err := pipeline.EvaluateBlock(context.Background(), AnchorToolRejected, gc)
				testutil.FailErr(t, "evaluate", err)
				if !result.Enforced || result.Decision == nil || result.Decision.Code != "MUTATION_BROKE_PARSE" {
					t.Fatalf("missing mutation rejection: %#v", result)
				}
				fix := result.Decision.Copy["fix"]
				if !strings.Contains(fix, "rejected candidate") || !strings.Contains(fix, "syntax_override_reason") {
					t.Fatalf("missing candidate location or override semantics: %s", fix)
				}
				if strings.Contains(fix, "was absent") != (state == "new") {
					t.Fatalf("absence claim does not match observation: %s", fix)
				}
				if state == "new" && !strings.Contains(fix, "retry a complete `write`") {
					t.Fatalf("new candidate lacks creation remedy: %s", fix)
				}
				if len(fix) > 1000 {
					t.Fatalf("recovery copy grew beyond bounded remedy: %d bytes", len(fix))
				}
			})
		}
	}
}
