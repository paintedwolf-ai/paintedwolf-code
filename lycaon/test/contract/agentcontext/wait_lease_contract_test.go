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

// These scans keep every park inside the machinery that moves the lease.
// The invariant itself — a session that will act again without new user input
// holds an open awaiting_wake lease for exactly that long — is proved in
// internal/coordinator/loopwake/wait_lease_invariant_test.go.

// sleepMoverArgAllowed lists the expressions that may name a sleep's mover.
// SleepMover's zero value reads as "not the host", so a defaulted field would
// publish no lease without failing to compile.
var sleepMoverArgAllowed = map[string]bool{
	"SleepMoverHost":          true,
	"SleepMoverUser":          true,
	"loopwake.SleepMoverHost": true,
	"loopwake.SleepMoverUser": true,
	// The one derivation: a re-arm carries forward the mover of the wait it continues.
	"l.activeSleepMover": true,
}

// sleepDeadlineWriters lists the functions that may write sessionSleep.until.
// Each one also moves the lease.
var sleepDeadlineWriters = map[string]bool{
	"enterSleep":          true,
	"breakSleep":          true,
	"DisarmTimerBackstop": true,
}

// Every armed sleep names who ends it.
func TestEnterSleepAlwaysNamesItsMover(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var findings []string

	err := filepath.WalkDir(filepath.Join(root, "lycaon"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipGoWalkDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || callSelectorName(call) != "EnterSleep" {
				return true
			}
			pos := fset.Position(call.Pos())
			if len(call.Args) != 7 {
				findings = append(findings, rel+":"+strconv.Itoa(pos.Line)+
					": EnterSleep takes a mover; this call passes "+strconv.Itoa(len(call.Args))+" arguments")
				return true
			}
			mover := sleepMoverExprText(call.Args[6])
			if !sleepMoverArgAllowed[mover] {
				findings = append(findings, rel+":"+strconv.Itoa(pos.Line)+
					": mover argument "+mover+" is not a SleepMover constant")
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk lycaon for EnterSleep calls", err)

	if len(findings) > 0 {
		t.Fatalf("Anti-drift: a sleep whose mover defaults publishes no lease and goes invisible:\n  %s",
			strings.Join(findings, "\n  "))
	}
}

// The sleep deadline moves only where the lease moves with it.
func TestSleepDeadlineWritesStayInTheLeaseFunnel(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "coordinator", "loopwake")
	var findings []string

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		rel, _ := filepath.Rel(root, path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || sleepDeadlineWriters[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range assign.Lhs {
					if exprText(lhs) != "st.until" {
						continue
					}
					pos := fset.Position(assign.Pos())
					findings = append(findings, rel+":"+strconv.Itoa(pos.Line)+" in "+fn.Name.Name)
				}
				return true
			})
		}
		return nil
	})
	contractcheck.FailErr(t, "walk loopwake for sleep deadline writes", err)

	if len(findings) > 0 {
		t.Fatalf("Anti-drift: a sleep armed outside the lease funnel is a wait no client can see:\n  %s",
			strings.Join(findings, "\n  "))
	}
}

// skipGoWalkDir names trees that hold files ending in .go which are not Go
// source, such as vendored scanner rule fixtures.
func skipGoWalkDir(name string) bool {
	switch name {
	case "vendor", "testdata", "node_modules", ".task", ".git":
		return true
	}
	return false
}

// sleepMoverExprText renders a mover argument, unwrapping the one call form the
// allowlist permits. The package's shared exprText stops at selectors.
func sleepMoverExprText(expr ast.Expr) string {
	if call, ok := expr.(*ast.CallExpr); ok {
		return exprText(call.Fun)
	}
	return exprText(expr)
}
