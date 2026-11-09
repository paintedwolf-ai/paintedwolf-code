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
				if !strings.Contains(fix, "supply `syntax_override_reason`") {
					t.Fatalf("domain-tool leg must advise the form-tool override parameter: %s", fix)
				}
				if len(fix) > 1000 {
					t.Fatalf("recovery copy grew beyond bounded remedy: %d bytes", len(fix))
				}
			})
		}
	}
}

// TestMutationCandidateRecoveryCopyCommandLeg pins the command-leg branch of
// PC02-001: command edit, replace-lines, and redirects accept no override, so
// the copy must name `code-rewrite --syntax-override-reason` as the only way
// past the check instead of advising the form-tool parameter.
func TestMutationCandidateRecoveryCopyCommandLeg(t *testing.T) {
	ensureCatalog(t)
	loader, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	rules, err := loader.LoadDir(hintsDir(t))
	testutil.FailErr(t, "load", err)
	pipeline := NewGuardPipeline(rules, loader, NewCounterStore())
	pipeline.EnableAnchor(AnchorToolRejected)
	for _, operation := range []string{"write", "edit", "replace_lines", "code_rewrite"} {
		t.Run(operation, func(t *testing.T) {
			gc := NewGuardContext()
			gc.ObserveToolCall("command", map[string]any{"command": "fixture"})
			testutil.FailErr(t, "observe "+operation, gc.ObserveNativeOperation(bindNativeOperation(t, operation)))
			gc.ObservedRejectCode = "MUTATION_BROKE_PARSE"
			gc.PutRejectData("MUTATION_BROKE_PARSE", map[string]any{"path": "source", "parse_error": "line 2: missing token", "mutation_new_path": false})
			result, err := pipeline.EvaluateBlock(context.Background(), AnchorToolRejected, gc)
			testutil.FailErr(t, "evaluate", err)
			if !result.Enforced || result.Decision == nil || result.Decision.Code != "MUTATION_BROKE_PARSE" {
				t.Fatalf("missing mutation rejection: %#v", result)
			}
			fix := result.Decision.Copy["fix"]
			if !strings.Contains(fix, "code-rewrite --syntax-override-reason") {
				t.Fatalf("command leg must name the only override-capable form: %s", fix)
			}
			if strings.Contains(fix, "supply `syntax_override_reason`") {
				t.Fatalf("command leg must not advise the form-tool override parameter: %s", fix)
			}
			if len(fix) > 1000 {
				t.Fatalf("recovery copy grew beyond bounded remedy: %d bytes", len(fix))
			}
		})
	}
}
