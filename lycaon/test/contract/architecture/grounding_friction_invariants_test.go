package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func countSourceDefs(t *testing.T, anchor string) map[string]int {
	t.Helper()
	return countSourceDefsIn(t, "", anchor)
}

func countSourceDefsIn(t *testing.T, domain, anchor string) map[string]int {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	base := filepath.Join(root, "lycaon", "internal", domain)
	hits := map[string]int{}
	err := contractcheck.WalkFiles(base, map[string]struct{}{".go": {}}, true, func(path string, data []byte) error {
		n := strings.Count(string(data), anchor)
		if n > 0 {
			hits[relRepoPath(root, path)] = n
		}
		return nil
	})
	testutil.FailErr(t, "walk internal for "+anchor, err)
	return hits
}

func totalDefs(hits map[string]int) int {
	total := 0
	for _, n := range hits {
		total += n
	}
	return total
}

func TestOneCloseoutLifecycle(t *testing.T) {
	t.Parallel()
	if hits := countSourceDefsIn(t, "session/closeouts", "type Service struct"); totalDefs(hits) != 1 {
		t.Fatalf("closeouts.Service defined %d times, want exactly 1: %v", totalDefs(hits), hits)
	}
	if hits := countSourceDefs(t, "type closeoutLifecycle struct"); totalDefs(hits) != 0 {
		t.Fatalf("parallel legacy closeout lifecycle remains: %v", hits)
	}
	if hits := countSourceDefs(t, "func (m *Service) RecordGroundingFriction("); totalDefs(hits) != 1 {
		t.Fatalf("RecordGroundingFriction defined %d times, want exactly 1 shared seam: %v", totalDefs(hits), hits)
	}
}

func TestOneLedgerCloseoutAssembler(t *testing.T) {
	t.Parallel()
	if hits := countSourceDefsIn(t, "session/closeoutassembly", "func (m *Service) Assemble("); totalDefs(hits) != 1 {
		t.Fatalf("closeoutassembly.Service.Assemble defined %d times, want exactly 1 surface-aware assembler: %v", totalDefs(hits), hits)
	}
	if hits := countSourceDefs(t, "func (m *Manager) assembleLedgerCloseout("); totalDefs(hits) != 0 {
		t.Fatalf("parallel legacy ledger closeout assembler remains: %v", hits)
	}
	if hits := countSourceDefs(t, "func (l *turnCloseout) emitAssembledCloseout("); totalDefs(hits) != 1 {
		t.Fatalf("emitAssembledCloseout defined %d times, want exactly 1: %v", totalDefs(hits), hits)
	}
}

func TestOneObservedAutobindBinder(t *testing.T) {
	t.Parallel()
	if hits := countSourceDefs(t, "func BindObservedSample("); totalDefs(hits) != 1 {
		t.Fatalf("BindObservedSample defined %d times, want exactly 1 shared binder: %v", totalDefs(hits), hits)
	}
	if hits := countSourceDefs(t, "func SelectObservedPathSample("); totalDefs(hits) != 1 {
		t.Fatalf("SelectObservedPathSample defined %d times, want exactly 1: %v", totalDefs(hits), hits)
	}
}

func TestExhaustionRoutesToAssembledCloseout(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	stages := filepath.Join(root, "lycaon", "internal", "coordinator", "promptloop", "run_stages.go")
	raw, err := os.ReadFile(stages)
	testutil.FailErr(t, "read run_stages.go", err)
	data := string(raw)
	if !strings.Contains(data, "commitOut.exhausted") {
		t.Fatal("run_stages.go: missing commitOut.exhausted → assemble branch")
	}
	if !strings.Contains(data, "emitAssembledCloseout") {
		t.Fatal("run_stages.go: missing emitAssembledCloseout")
	}

	closeout := filepath.Join(root, "lycaon", "internal", "coordinator", "promptloop", "turn_closeout.go")
	raw, err = os.ReadFile(closeout)
	testutil.FailErr(t, "read turn_closeout.go", err)
	data = string(raw)
	if !strings.Contains(data, "returnAssembledEarlyCloseout") {
		t.Fatal("turn_closeout.go: missing returnAssembledEarlyCloseout for non-committed forced finish")
	}
	if !strings.Contains(data, "emitAssembledCloseout") {
		t.Fatal("turn_closeout.go: missing emitAssembledCloseout")
	}
}

func TestEarlyCloseoutFinishBlockRoutesToAssembledCloseout(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "coordinator", "promptloop", "turn_closeout.go")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read turn_closeout.go", err)
	data := string(raw)
	if !strings.Contains(data, "emitEarlyCloseoutAfterFinishBlock") {
		t.Fatal("turn_closeout.go: missing emitEarlyCloseoutAfterFinishBlock")
	}
	if !strings.Contains(data, "returnAssembledEarlyCloseout") {
		t.Fatal("turn_closeout.go: missing returnAssembledEarlyCloseout")
	}
}

func TestObservedAutobindDoesNotInventFromProse(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()

	path := filepath.Join(root, "lycaon", "internal", "guidance", "observed_autobind.go")
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	testutil.FailErr(t, "parse observed_autobind.go", err)

	bannedSelectors := map[string]struct{}{"Synthesis": {}, "Brief": {}, "Narrative": {}}
	seen := false
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "BuildObservedAutobindGrounding" || fn.Body == nil {
			return true
		}
		seen = true
		ast.Inspect(fn.Body, func(bn ast.Node) bool {
			sel, ok := bn.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, bad := bannedSelectors[sel.Sel.Name]; bad {
				t.Fatalf("BuildObservedAutobindGrounding reads prose field .%s — wire projection must stay report/ledger only", sel.Sel.Name)
			}
			return true
		})
		return true
	})
	if !seen {
		t.Fatal("BuildObservedAutobindGrounding not found")
	}

	src := filepath.Join(root, "lycaon", "internal", "guidance", "observed_sample.go")
	raw, err := os.ReadFile(src)
	testutil.FailErr(t, "read observed_sample.go", err)
	body := string(raw)
	for _, need := range []string{
		"ObservedPathsSorted",
		"HandleForPath",
		"IsOpenablePath",
		"ObservedCitations",
		"narrativeContainsObserved",
	} {
		if !strings.Contains(body, need) {
			t.Fatalf("SelectObservedPathSample must gate eligibility on ledger helpers; missing %s", need)
		}
	}
	if !strings.Contains(body, "preferredObservedURLs") {
		t.Fatal("URL preference must share ObservedCitations membership, not a separate Contains scan")
	}
}
