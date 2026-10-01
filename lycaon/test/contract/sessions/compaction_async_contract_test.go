package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCompactionSingleWriterContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	allowed := map[string]bool{
		"internal/session/compaction.go": true,
	}
	callers, err := scanCompactionViewWriterCallers(lycaonRoot)
	contractcheck.FailErr(t, "scan writeCompactionView callers", err)
	for fn, sites := range callers {
		if allowed[fn] {
			continue
		}
		t.Fatalf("writeCompactionView caller %s not allowed: %v", fn, sites)
	}
	if len(callers["internal/session/compaction.go"]) < 2 {
		t.Fatalf("expected runBackgroundCompaction and ForceCompact in compaction.go, got %v", callers)
	}
}

func scanCompactionViewWriterCallers(lycaonRoot string) (map[string][]string, error) {
	fset := token.NewFileSet()
	out := make(map[string][]string)
	err := filepath.WalkDir(lycaonRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(lycaonRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "writeCompactionView" {
				return true
			}
			pos := fset.Position(call.Pos())
			out[rel] = append(out[rel], pos.String())
			return true
		})
		return nil
	})
	return out, err
}

func TestCompactionNoGlobalLLMSemaphoreContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cmd := exec.CommandContext(t.Context(), "git", "grep", "-E",
		`globalLLM|llmInFlight|LLMInFlight|llmSemaphore`,
		"--", "lycaon/internal/llm/")
	cmd.Dir = root
	cmd.Env = lyexec.LocalGitEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unexpected global LLM semaphore tokens in internal/llm:\n%s", string(out))
	}
}

func TestCompactionSyncPathAvoidsSummarizerContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, path := range []string{
		"lycaon/internal/session/prompt_assembly.go",
		"lycaon/internal/coordinator/promptloop/stream.go",
	} {
		data, err := os.ReadFile(filepath.Join(root, path))
		contractcheck.FailErr(t, "read "+path, err)
		body := string(data)
		for _, forbidden := range []string{".Compact(", ".Summarize(", "CompactChunk("} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s must not call compaction LLM APIs on the sync path; found %q", path, forbidden)
			}
		}
	}
	forbidden, err := compactionSyncPathForbiddenCalls(filepath.Join(root, "lycaon/internal/session/compaction.go"))
	contractcheck.FailErr(t, "scan compaction.go sync paths", err)
	if len(forbidden) > 0 {
		t.Fatalf("compaction.go sync paths must not call compaction LLM APIs: %v", forbidden)
	}
}

func compactionSyncPathForbiddenCalls(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	syncFuncs := map[string]bool{"maybeCompact": true}
	var hits []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || !syncFuncs[fn.Name.Name] || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			switch sel.Sel.Name {
			case "Compact", "Summarize", "CompactChunk":
				hits = append(hits, fn.Name.Name+":"+sel.Sel.Name)
			}
			return true
		})
	}
	return hits, nil
}

func TestCompactionRunnerUsesWithoutCancelContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon/internal/session/compaction_runner.go"))
	contractcheck.FailErr(t, "read compaction_runner.go", err)
	if !strings.Contains(string(data), "context.WithoutCancel") {
		t.Fatal("compaction_runner.go must preserve WithoutCancel background context")
	}
}

func TestCompactionThresholdBandContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon/internal/llm/compaction/compactor_config.go")
	consts := parseIntConsts(t, path)
	// The async compaction band is [budget_trigger, hard_ceiling] with a
	// non-empty target below it; these are the derive-only fallbacks when
	// compaction.yaml omits the percentage.
	for _, want := range []struct {
		name  string
		value int
	}{
		{"defaultBudgetTriggerPct", 60},
		{"defaultHardCeilingPct", 85},
		{"defaultTargetTokensPct", 35},
	} {
		got, ok := consts[want.name]
		if !ok {
			t.Fatalf("%s: const %s not found", path, want.name)
		}
		if got != want.value {
			t.Fatalf("%s: const %s = %d, want %d", path, want.name, got, want.value)
		}
	}
	if consts["defaultTargetTokensPct"] >= consts["defaultBudgetTriggerPct"] ||
		consts["defaultBudgetTriggerPct"] >= consts["defaultHardCeilingPct"] {
		t.Fatalf("%s: expected target < trigger < ceiling, got %d < %d < %d",
			path, consts["defaultTargetTokensPct"], consts["defaultBudgetTriggerPct"], consts["defaultHardCeilingPct"])
	}
}

// parseIntConsts returns the integer constant values declared in a Go source
// file, keyed by identifier name, so a contract can bind to the named constant
// rather than grepping its literal value out of the file body.
func parseIntConsts(t *testing.T, path string) map[string]int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	contractcheck.FailErr(t, "parse "+filepath.Base(path), err)
	out := map[string]int{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.INT {
					continue
				}
				v, err := strconv.Atoi(lit.Value)
				if err != nil {
					continue
				}
				out[name.Name] = v
			}
		}
	}
	return out
}

func TestCompactionTokenObservationWiredContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cmd := exec.CommandContext(t.Context(), "git", "grep", "-n",
		"RecordCompactionTokenObservation",
		"--",
		"lycaon/internal/coordinator/promptloop/stream.go",
		"lycaon/internal/session/coordinator_wire.go",
	)
	cmd.Dir = root
	cmd.Env = lyexec.LocalGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("RecordCompactionTokenObservation must be wired through prompt loop:\n%s", string(out))
	}
	if !strings.Contains(string(out), "stream.go") || !strings.Contains(string(out), "coordinator_wire.go") {
		t.Fatalf("unexpected wiring sites:\n%s", string(out))
	}
}
