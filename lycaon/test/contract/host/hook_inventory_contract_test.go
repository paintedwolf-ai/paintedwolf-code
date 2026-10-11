package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// TestHookInventoryCoversWireBindings checks callback coverage and lock notes.
func TestHookInventoryCoversWireBindings(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	inventoryPath := filepath.Join(root, "lycaon", "config", "fixtures", "hook_inventory.yaml")

	var bindings []wireHookBinding
	for _, buildPath := range appProductionFilePaths(t, root) {
		bindings = append(bindings, parseWireHookBindings(t, buildPath)...)
	}
	declared := loadHookInventory(t, inventoryPath)

	var uncovered []string
	for _, b := range bindings {
		key := b.manager + "." + b.field
		if _, ok := declared[key]; !ok {
			uncovered = append(uncovered, key+"  ("+b.file+":"+strconv.Itoa(b.line)+")")
		}
	}
	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Fatalf(`hook bindings missing from hook_inventory.yaml:
%s`,
			"  - "+strings.Join(uncovered, "\n  - "))
	}

	var stale []string
	for key := range declared {
		found := false
		for _, b := range bindings {
			if b.manager+"."+b.field == key {
				found = true
				break
			}
		}
		if !found {
			stale = append(stale, key)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Fatalf(`stale hook_inventory.yaml entries:
%s`,
			"  - "+strings.Join(stale, "\n  - "))
	}

	var missingNotes []string
	for key, h := range declared {
		if h.reentrant && strings.TrimSpace(h.notes) == "" {
			missingNotes = append(missingNotes, key)
		}
	}
	if len(missingNotes) > 0 {
		sort.Strings(missingNotes)
		t.Fatalf(`reentrant hook entries missing lock notes:
%s`,
			"  - "+strings.Join(missingNotes, "\n  - "))
	}
}

type wireHookBinding struct {
	manager string
	field   string
	file    string
	line    int
}

type inventoryHook struct {
	reentrant bool
	notes     string
}

// parseWireHookBindings finds manager hook assignments in build files.
func parseWireHookBindings(t *testing.T, path string) []wireHookBinding {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	testutil.FailErr(t, "parse "+filepath.Base(path), err)

	// Map build variables to their inventory type names.
	managerTypeForVar := map[string]string{
		"workflowMgr.Requests":    "workflow/inputs.Requests",
		"workflowMgr.Phases":      "workflow/phases.Service",
		"workflowMgr.Publication": "workflow/publication.Runs",
		"workflowMgr.Controls":    "workflow/lifecycle.Commands",
		"workflowMgr.Children":    "workflow.Children",
		"workflowMgr.Approvals":   "workflow.Approvals",
		"workflowMgr.Feedback":    "workflow/inputs.Feedback",
		"workflowMgr.Asks":        "workflow/inputs.Asks",
		"workflowMgr.Verdicts":    "workflow/review.Verdicts",
		"delegationMgr":           "delegation.Manager",
	}

	isHookField := func(name string) bool {
		return strings.HasPrefix(name, "On") || strings.HasSuffix(name, "Hook")
	}

	var out []wireHookBinding
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		managerVar := strings.TrimPrefix(hookReceiverPath(sel.X), "b.")
		managerVar = strings.ReplaceAll(managerVar, "deps.Workflows.Manager", "workflowMgr")
		if strings.Contains(filepath.ToSlash(path), "/app/workflows/") {
			managerVar = strings.ReplaceAll(managerVar, "r.Manager", "workflowMgr")
		} else if strings.Contains(filepath.ToSlash(path), "/app/delegations/") {
			managerVar = strings.ReplaceAll(managerVar, "r.Manager", "delegationMgr")
		}
		if !isHookField(sel.Sel.Name) {
			return true
		}
		managerType, ok := managerTypeForVar[managerVar]
		if !ok {
			if managerVar == "workflowMgr" || strings.HasPrefix(managerVar, "workflowMgr.") {
				t.Fatalf("unclassified workflow callback resource %s at %s", managerVar, fset.Position(assign.Pos()))
			}
			return true
		}
		out = append(out, wireHookBinding{
			manager: managerType,
			field:   sel.Sel.Name,
			file:    filepath.Base(path),
			line:    fset.Position(assign.Pos()).Line,
		})
		return true
	})
	return out
}

func loadHookInventory(t *testing.T, path string) map[string]inventoryHook {
	t.Helper()
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read hook_inventory.yaml", err)
	var raw struct {
		Hooks []struct {
			Manager   string `yaml:"manager"`
			Field     string `yaml:"field"`
			FiresOn   string `yaml:"fires_on"`
			BindsTo   string `yaml:"binds_to"`
			Posture   string `yaml:"posture"`
			Reentrant bool   `yaml:"reentrant"`
			Notes     string `yaml:"notes"`
		} `yaml:"hooks"`
	}
	testutil.FailErr(t, "parse hook_inventory.yaml", yaml.Unmarshal(data, &raw))
	out := map[string]inventoryHook{}
	for i, h := range raw.Hooks {
		manager := strings.TrimSpace(h.Manager)
		field := strings.TrimSpace(h.Field)
		firesOn := strings.TrimSpace(h.FiresOn)
		bindsTo := strings.TrimSpace(h.BindsTo)
		posture := strings.ToLower(strings.TrimSpace(h.Posture))
		if manager == "" || field == "" || firesOn == "" || bindsTo == "" {
			t.Fatalf("hook inventory entry %d has empty required fields", i)
		}
		if posture != "async" && posture != "synchronous" {
			t.Fatalf("hook inventory entry %d has invalid posture %q", i, h.Posture)
		}
		key := manager + "." + field
		if _, exists := out[key]; exists {
			t.Fatalf("duplicate hook inventory entry %q", key)
		}
		out[key] = inventoryHook{
			reentrant: h.Reentrant,
			notes:     h.Notes,
		}
	}
	return out
}

func hookReceiverPath(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		prefix := hookReceiverPath(value.X)
		if prefix != "" {
			return prefix + "." + value.Sel.Name
		}
	}
	return ""
}

func appProductionFilePaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(filepath.Join(root, "lycaon", "internal", "app"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			paths = append(paths, path)
		}
		return nil
	})
	contractcheck.FailErr(t, "walk app composition sources", err)
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("no app composition sources")
	}
	return paths
}
