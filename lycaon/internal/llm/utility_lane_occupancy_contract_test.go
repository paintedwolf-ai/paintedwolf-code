package llm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestUtilityLaneOccupancyKeepsCompileOnTheAdmittedTurn: stamp and envelope
// paths do not curate; occupancy is marked on the admitted turn.
func TestUtilityLaneOccupancyKeepsCompileOnTheAdmittedTurn(t *testing.T) {
	root := testutil.CheckoutRoot(t)
	assertOccupancyFuncAvoids(t, root, filepath.Join("lycaon", "internal", "workflow"),
		[]string{"StampFanoutExecuteOutput", "stampReviewLoopEvidence"},
		[]string{"MaybeCurateSynthesisEvidence", "Curate", "Complete"})
	assertOccupancyFuncAvoids(t, root, filepath.Join("lycaon", "internal", "session"),
		[]string{"CoordinatorEnvelopeForWorkerCycleTerminal"},
		[]string{"MaybeCurateSynthesisEvidence", "appendSynthesisEnvelope", "Curate"})
	assertOccupancyFuncCalls(t, root, filepath.Join("lycaon", "internal", "session"),
		"runTurnLocked", []string{"WithSession", "WithLane"})
	assertOccupancyFuncCalls(t, root, filepath.Join("lycaon", "internal", "llm"),
		"completeUtility", []string{"beginLaneCall", "endLaneCall"})
	assertOccupancyFuncCalls(t, root, filepath.Join("lycaon", "internal", "llm"),
		"streamLite", []string{"beginLaneCall", "endLaneCall"})
	assertOccupancyIdentAbsent(t, root, filepath.Join("lycaon", "internal", "workflow"),
		[]string{"synthesisCurator", "SetSynthesisCurator"})
}

func TestBindSummarizerForwardsServiceLifecycle(t *testing.T) {
	life := &recordingLifecycle{}
	svc := &Service{Lifecycle: life}
	sum := svc.BindSummarizer(&RegistrySummarizer{})
	if sum.Lifecycle == nil {
		t.Fatal("BindSummarizer left Lifecycle nil")
	}
	sum.Lifecycle.PublishCall(occupiedSessionCtx(), api.LLMCallEvent{Status: api.LLMCallStatusActive})
	if len(life.evs) != 1 || life.evs[0].Status != api.LLMCallStatusActive {
		t.Fatalf("forwarded events = %+v", life.evs)
	}
}

func assertOccupancyFuncAvoids(t *testing.T, root, relDir string, funcs, forbidden []string) {
	t.Helper()
	want := map[string]bool{}
	for _, name := range funcs {
		want[name] = true
	}
	forbid := map[string]bool{}
	for _, name := range forbidden {
		forbid[name] = true
	}
	for _, item := range parseOccupancyFuncs(t, root, relDir) {
		fn := item.fn
		if !want[fn.Name.Name] {
			continue
		}
		var hits []string
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if forbid[sel.Sel.Name] {
				hits = append(hits, sel.Sel.Name)
			}
			return true
		})
		if len(hits) > 0 {
			rel, _ := filepath.Rel(root, item.path)
			t.Fatalf("%s %s must not curate or Complete: %s", rel, fn.Name.Name, strings.Join(hits, ", "))
		}
	}
}

func assertOccupancyFuncCalls(t *testing.T, root, relDir, funcName string, required []string) {
	t.Helper()
	found := false
	for _, item := range parseOccupancyFuncs(t, root, relDir) {
		fn := item.fn
		if fn.Name.Name != funcName {
			continue
		}
		found = true
		seen := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Ident:
				seen[node.Name] = true
			case *ast.SelectorExpr:
				seen[node.Sel.Name] = true
			}
			return true
		})
		var missing []string
		for _, name := range required {
			if !seen[name] {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			rel, _ := filepath.Rel(root, item.path)
			t.Fatalf("%s %s missing occupancy marks: %s", rel, funcName, strings.Join(missing, ", "))
		}
	}
	if !found {
		t.Fatalf("%s: function %s not found", relDir, funcName)
	}
}

func assertOccupancyIdentAbsent(t *testing.T, root, relDir string, names []string) {
	t.Helper()
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
	}
	fset := token.NewFileSet()
	var hits []string
	for _, file := range parseOccupancyTree(t, fset, filepath.Join(root, relDir)) {
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || !want[id.Name] {
				return true
			}
			rel, _ := filepath.Rel(root, fset.File(id.Pos()).Name())
			hits = append(hits, rel+": "+id.Name)
			return true
		})
	}
	if len(hits) > 0 {
		t.Fatalf("workflow must not hold a synthesis curator:\n  %s", strings.Join(hits, "\n  "))
	}
}

type occupancyPkgFunc struct {
	path string
	fn   *ast.FuncDecl
}

func parseOccupancyFuncs(t *testing.T, root, relDir string) []occupancyPkgFunc {
	t.Helper()
	fset := token.NewFileSet()
	var out []occupancyPkgFunc
	for _, file := range parseOccupancyTree(t, fset, filepath.Join(root, relDir)) {
		path := fset.File(file.Pos()).Name()
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			out = append(out, occupancyPkgFunc{path: path, fn: fn})
		}
	}
	return out
}

// parseOccupancyTree parses production files under dir, subpackages included.
func parseOccupancyTree(t *testing.T, fset *token.FileSet, dir string) []*ast.File {
	t.Helper()
	var files []*ast.File
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			if d != nil && d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return walkErr
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		files = append(files, file)
		return nil
	})
	testutil.FailErr(t, "parse "+dir, err)
	return files
}
