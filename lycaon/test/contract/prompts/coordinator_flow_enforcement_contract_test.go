package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var coordinatorSurfaceIDLiterals = []string{
	"implement_routing",
	"implement_dispatch",
	"implement_synthesis",
	"implement_investigate",
	"implement_overlay_promote",
	"implement_park",
	"workflow_compose",
	"plan_stub",
	"plan_research",
	"plan_approve",
	"await_user",
	"plan_execute",
}

var coordinatorBaseModeRefLiterals = []string{
	"implement-investigate",
	"implement-overlay-promote",
	"implement-synthesis",
	"implement-dispatch",
	"implement-routing",
	"implement-park",
	"compose",
}

func TestCoordinatorFlowSurfaceLiteralContainment(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	allow := surfaceLiteralAllowlist(lycaonRoot)
	var violations []string
	scanDir := filepath.Join(lycaonRoot, "internal", "coordinator")
	contractcheck.FailErr(t, "filepath.Walk coordinator", filepath.Walk(scanDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(lycaonRoot, path)
		contractcheck.FailErr(t, "filepath.Rel", err)
		if allow[rel] {
			return nil
		}
		violations = append(violations, scanGoSurfaceLiterals(path, rel)...)
		return nil
	}))
	if len(violations) > 0 {
		t.Fatalf("surface/mode literals outside allowlist:\n%s", strings.Join(violations, "\n"))
	}
}

func surfaceLiteralAllowlist(lycaonRoot string) map[string]bool {
	files := []string{
		"internal/coordinator/surface/profile.go",
		"internal/coordinator/surface/flow_eval.go",
		"internal/coordinator/surface/flow_loader.go",
		"internal/coordinator/surface/flow_parity.go",
		"internal/coordinator/surface/surface_profiles.go",
		"internal/coordinator/surface/envelope.go",
		"internal/coordinator/surface/execution_mode_state.go",
		"internal/coordinator/assembly/prompt_dump.go",
		"internal/coordinator/inject/execution_mode_prompt.go",
		"internal/coordinator/guard/host_turn_guard.go",
		"internal/coordinator/guard/closeout_grounding.go",
		"internal/coordinator/guard/prose_citation_grounding.go",
		"internal/coordinator/promptloop/batch.go",
		"internal/coordinator/promptloop/tool_batch.go",
		"internal/coordinator/promptloop/run_stages.go",
		"internal/session/worker_tool_coord.go",
		"internal/tools/coordinator_surface.go",
		"internal/tools/coordinator_guard.go",
		"internal/tools/native/coordinator_write_scope.go",
		"internal/tools/native/worker_mutation_hooks.go",
		"internal/tools/scope_reject.go",
		"internal/spawn/surfaces.go",
	}
	out := make(map[string]bool, len(files))
	for _, f := range files {
		out[f] = true
	}
	return out
}

func scanGoSurfaceLiterals(path, rel string) []string {
	src, err := os.ReadFile(path)
	if err != nil {
		return []string{rel + ": read: " + err.Error()}
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return []string{rel + ": parse: " + err.Error()}
	}
	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val := strings.Trim(lit.Value, "`\"")
		for _, id := range coordinatorSurfaceIDLiterals {
			if val == id {
				pos := fset.Position(lit.Pos())
				violations = append(violations, rel+":"+strconv.Itoa(pos.Line)+": surface id literal "+id)
			}
		}
		for _, id := range coordinatorBaseModeRefLiterals {
			if val == id {
				pos := fset.Position(lit.Pos())
				violations = append(violations, rel+":"+strconv.Itoa(pos.Line)+": mode ref literal "+id)
			}
		}
		return true
	})
	return violations
}
