package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Maintenance starts after structural recovery.
var expectedServeRunnerOrder = []string{
	"boot-recovery",
	"store-coupled-reconcile",
	"wal-checkpointer",
	"store-maintenance",
	"store-integrity-audit",
	"managed-secret-maintenance",
	"worker-poller",
	"scan-runner",
	"scan-cadence",
	"warm-runner",
	"decision-engine-warm",
	"source-blob-gc",
	"content-blob-gc",
	"prompt-attachment-maintenance",
	"content-density",
	"history-retention",
	"debug-retention",
	"worker-branch-retention",
}

func TestServeRunnerOrderSSOT(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	order := readStringSliceVar(t, root, "internal/app/runners.go", "serveRunnerOrder")
	if !reflect.DeepEqual(order, expectedServeRunnerOrder) {
		t.Fatalf("serveRunnerOrder = %v, want %v", order, expectedServeRunnerOrder)
	}
}

func TestServeBuildAndCloseShareResourceRegistry(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	build, err := os.ReadFile(filepath.Join(root, "lycaon", "internal/app/build.go"))
	contractcheck.FailErr(t, "read build", err)
	server, err := os.ReadFile(filepath.Join(root, "lycaon", "internal/app/build_server.go"))
	contractcheck.FailErr(t, "read build server", err)
	if !strings.Contains(string(build), "resources: newRuntimeResources()") ||
		!strings.Contains(string(build), "b.resources.capture(b)") ||
		!strings.Contains(string(build), "b.resources.Close(ctx)") {
		t.Fatal("build phases and failed-build cleanup must share runtimeResources")
	}
	if !strings.Contains(string(server), "resources:            b.resources") {
		t.Fatal("completed ServeApp must receive the builder's runtimeResources")
	}
}

func TestResourceRegistryClosesSessionResourcesBeforeDurableState(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal/app/runtime_resources.go")
	src, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	body := string(src)
	needles := []string{
		`r.track("preview", 30`,
		`r.track("browser-pages", 40`,
		`r.track("llm-service", 45`,
		`r.track("background-processes", 50`,
		`r.track("browser-pool", 60`,
		`r.track("repo-provider", 70`,
		`r.track("source-feeds", 80`,
		`r.track("source-snapshots", 82`,
		`r.track("source-watchers", 85`,
		`r.track("web-warmer", 90`,
		`r.track("web-index", 100`,
		`r.track("mcp", 110`,
		`r.track("event-outbox", 120`,
		`r.track("database", 130`,
		`r.track("instance-lock", 140`,
		`r.track("debug-captures", 150`,
	}
	for _, needle := range needles {
		if !strings.Contains(body, needle) {
			t.Fatalf("resource registry missing %q", needle)
		}
	}
}

// Recovered turns need redaction, secret screening, tools, and approval gates wired.
func TestBootRecoveryRunsAfterWiring(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	server, err := os.ReadFile(filepath.Join(root, "lycaon", "internal/app/build_server.go"))
	contractcheck.FailErr(t, "read build_server.go", err)
	for _, forbidden := range []string{"RecoverVerdictOperations", "RecoverPromptSubmissions"} {
		if strings.Contains(string(server), forbidden+"(b.ctx)") {
			t.Fatalf("build_server.go must not call %s during wiring — recovered turns would run before redaction, secret screen, MCP, and the approval-gate seal", forbidden)
		}
	}
	runners, err := os.ReadFile(filepath.Join(root, "lycaon", "internal/app/build_runners.go"))
	contractcheck.FailErr(t, "read build_runners.go", err)
	body := string(runners)
	if !strings.Contains(body, `byName["boot-recovery"]`) {
		t.Fatal("build_runners.go must register the boot-recovery runner")
	}
	// The runner executes the registry's serve phase. The registry selects entries.
	if !strings.Contains(body, "bootrecovery.PhaseServe") {
		t.Fatal("boot-recovery runner must run the registry's serve phase")
	}
}

func TestRegisterBackgroundRunnersUsesRunnerOrderSSOT(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal/app/build_runners.go")
	src, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	body := string(src)
	if !strings.Contains(body, "for _, name := range serveRunnerOrder") {
		t.Fatal("registerBackgroundRunners must iterate serveRunnerOrder")
	}
}

// A nil Live callback permits scheduled warming without an active app.
func TestWarmRunnerWiredWithPresenceGate(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	relFile := "internal/app/build_board.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, "lycaon", relFile), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", relFile, err)
	}
	var found, gated bool
	ast.Inspect(file, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WarmRunner" {
			return true
		}
		found = true
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Live" {
				gated = true
			}
		}
		return true
	})
	if !found {
		t.Fatalf("%s: no webresearch.WarmRunner literal found", relFile)
	}
	if !gated {
		t.Fatalf("%s: WarmRunner must set Live — scheduled warming runs only while the app is in use", relFile)
	}
}

func readStringSliceVar(t *testing.T, root, relFile, varName string) []string {
	t.Helper()
	path := filepath.Join(root, "lycaon", relFile)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", relFile, err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			valSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valSpec.Names) != 1 || valSpec.Names[0].Name != varName {
				continue
			}
			if len(valSpec.Values) != 1 {
				t.Fatalf("%s: expected single composite literal", varName)
			}
			cl, ok := valSpec.Values[0].(*ast.CompositeLit)
			if !ok {
				t.Fatalf("%s: expected composite literal", varName)
			}
			var out []string
			for _, elt := range cl.Elts {
				lit, ok := elt.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s: expected string elements only", varName)
				}
				out = append(out, strings.Trim(lit.Value, `"`))
			}
			return out
		}
	}
	t.Fatalf("%s not found in %s", varName, relFile)
	return nil
}
