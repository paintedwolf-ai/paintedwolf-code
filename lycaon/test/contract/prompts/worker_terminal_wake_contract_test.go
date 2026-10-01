package contract

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkerResultStatusReactableClosesOverSummaryEnum(t *testing.T) {
	t.Parallel()
	for _, status := range api.AllWorkerSummaryStatuses() {
		got := api.WorkerResultStatusReactable(string(status))
		switch status {
		case api.WorkerSummaryStatusCanceled, api.WorkerSummaryStatusHeld:
			if got {
				t.Fatalf("%q must not wake the coordinator", status)
			}
		default:
			if !got {
				t.Fatalf("%q is a parent-visible terminal and must wake the coordinator", status)
			}
		}
	}
}

func TestOutcomeProjectionNudgesOnEveryMethod(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "worker")
	_, files := contractcheck.ParseNonTestGoTree(t, dir)
	recorderMethods := interfaceMethodNames(files, "OutcomeProjection")
	if len(recorderMethods) == 0 {
		t.Fatal("OutcomeProjection interface not found")
	}
	wakeNames := discoverWakeCallNames(files)
	recorders := structsWithFieldType(files, "WorkerSessionOutcomes")
	if len(recorders) == 0 {
		t.Fatal("no struct holds WorkerSessionOutcomes")
	}
	for _, recorder := range recorders {
		for _, method := range recorderMethods {
			fn := typeMethod(files, recorder, method)
			if fn == nil {
				t.Fatalf("%s must implement OutcomeProjection method %s", recorder, method)
			}
			if !funcCallsAny(fn, wakeNames) {
				t.Fatalf("%s.%s must nudge the parent coordinator (Nudge* wake, not only NotifyWorkerCycleTerminal)", recorder, method)
			}
		}
	}
}

// Every terminal queue mutation must publish its committed outcome.
func TestWorkerPollerTerminalQueueOpsNotifyOutcomes(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "worker")
	_, files := contractcheck.ParseNonTestGoTree(t, dir)
	terminalQueue := map[string]bool{
		"Fail": true, "Complete": true, "Cancel": true, "FinishCanceled": true,
	}
	publish := map[string]bool{"publishCommittedOutcome": true}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !recvTypeIs(fn, "LocalWorkerPoller") {
				continue
			}
			if !funcCallsAny(fn, terminalQueue) {
				continue
			}
			if !funcCallsAny(fn, publish) {
				t.Fatalf("LocalWorkerPoller.%s finalizes a queue job without publishing its committed outcome", fn.Name.Name)
			}
		}
	}
}

func interfaceMethodNames(files []*ast.File, name string) []string {
	var out []string
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name == nil || ts.Name.Name != name {
				return true
			}
			iface, ok := ts.Type.(*ast.InterfaceType)
			if !ok || iface.Methods == nil {
				return true
			}
			for _, field := range iface.Methods.List {
				if len(field.Names) == 0 {
					continue
				}
				if _, ok := field.Type.(*ast.FuncType); ok {
					out = append(out, field.Names[0].Name)
				}
			}
			return true
		})
	}
	return out
}

func structsWithFieldType(files []*ast.File, fieldType string) []string {
	var out []string
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name == nil {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			for _, field := range st.Fields.List {
				if ident, ok := field.Type.(*ast.Ident); ok && ident.Name == fieldType {
					out = append(out, ts.Name.Name)
				}
			}
			return true
		})
	}
	return out
}

func typeMethod(files []*ast.File, recv, name string) *ast.FuncDecl {
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Name.Name != name || fn.Body == nil {
				continue
			}
			if recvTypeIs(fn, recv) {
				return fn
			}
		}
	}
	return nil
}

func recvTypeIs(fn *ast.FuncDecl, name string) bool {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return false
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

func discoverWakeCallNames(files []*ast.File) map[string]bool {
	direct := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			base := callBaseName(contractcheck.CallName(call))
			if isCoordinatorWakeName(base) {
				direct[base] = true
			}
			return true
		})
	}
	// One hop: same-package helpers that call a Nudge* wake.
	helpers := map[string]bool{}
	for k, v := range direct {
		helpers[k] = v
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Body == nil {
				continue
			}
			if funcCallsAny(fn, direct) {
				helpers[fn.Name.Name] = true
			}
		}
	}
	return helpers
}

func isCoordinatorWakeName(name string) bool {
	return strings.Contains(name, "Nudge") && !strings.Contains(name, "Should")
}

func callBaseName(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

func funcCallsAny(fn *ast.FuncDecl, names map[string]bool) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if names[callBaseName(contractcheck.CallName(call))] {
			found = true
		}
		return true
	})
	return found
}
