package contract

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// coordinatorOnlyTools is the set of tools whose handler bodies guard
// access with isCoordinatorAgent. The set must match the set of call
// sites in non-test source — TestCoordinatorOnlyGuardInventory keeps
// the table honest.
var coordinatorOnlyTools = []string{
	"workflow_advance",
	"workflow_transition",
	"fanout_plan",
	"submit_verdict",
	"workflow_catalog_summaries",
	"workflow_compose",
	"workflow_compose_from_template",
	"workflow_persist",
	"workflow_user_feedback",
	"ask_user",
}

// TestCoordinatorOnlyToolsRejectNonCoordinatorAgents drives every tool that
// claims a coordinator-only role with Agent="implementer" and asserts each
// rejects with a coordinator-role error. Adding a new coordinator-only tool
// without enforcing the guard will land here as a missing rejection.
func TestCoordinatorOnlyToolsRejectNonCoordinatorAgents(t *testing.T) {
	reg := toolfixture.RegisterCatalogToolsForContract(t)

	// Some tools validate arguments before the role guard, so they get args
	// that pass validation.
	knownArgs := map[string]map[string]any{
		"workflow_compose":               {"manifest": map[string]any{}},
		"workflow_compose_from_template": {"template_id": "x"},
		// The rest take no arguments.
	}

	for _, tool := range coordinatorOnlyTools {
		t.Run(tool, func(t *testing.T) {
			args := map[string]any{}
			if a, ok := knownArgs[tool]; ok {
				args = a
			}
			_, err := reg.Run(context.Background(), tool, args, tools.ToolContext{
				Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
					ActiveRootID: "r1"},
				Identity: tools.InvocationIdentity{Agent: "implementer",
					SessionID: "sess-1"},
			})
			if err == nil {
				t.Fatalf("tool %q accepted non-coordinator agent — guard missing", tool)
			}
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "coordinator") {
				t.Fatalf("tool %q rejected non-coordinator but error %q does not mention 'coordinator'", tool, err.Error())
			}
		})
	}
}

// TestCoordinatorOnlyGuardInventory matches guarded tool names to the table.
func TestCoordinatorOnlyGuardInventory(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	workflowDir := filepath.Join(root, "lycaon", "internal", "workflow")
	entries, err := os.ReadDir(workflowDir)
	contractcheck.FailErr(t, "read directory entries", err)

	// A tool is guarded when the guard call appears between its Register("X",
	// and the next Register( call.
	registerRE := regexp.MustCompile(`reg\.Register\("([a-z_][a-z0-9_]*)",`)
	const guard = "isCoordinatorAgent(tctx.Agent)"

	guarded := map[string]bool{}
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workflowDir, name))
		contractcheck.FailErr(t, "read file", err)
		text := string(data)
		matches := registerRE.FindAllStringSubmatchIndex(text, -1)
		for i, m := range matches {
			toolName := text[m[2]:m[3]]
			end := len(text)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			if strings.Contains(text[m[0]:end], guard) {
				guarded[toolName] = true
			}
		}
	}

	expected := map[string]bool{}
	for _, name := range coordinatorOnlyTools {
		expected[name] = true
	}

	var missing []string
	for name := range expected {
		if !guarded[name] {
			missing = append(missing, name)
		}
	}
	var extra []string
	for name := range guarded {
		if !expected[name] {
			extra = append(extra, name)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		sort.Strings(missing)
		sort.Strings(extra)
		t.Fatalf("coordinatorOnlyTools out of sync with isCoordinatorAgent guards:\n  table-but-not-guarded: %v\n  guarded-but-not-in-table: %v",
			missing, extra)
	}
}
