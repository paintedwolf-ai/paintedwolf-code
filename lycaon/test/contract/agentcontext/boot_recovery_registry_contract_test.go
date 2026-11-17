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
)

const recoveryComposeRoot = "../../../internal/app"

// recoveryCallExempt lists Recover calls outside subsystem startup recovery.
var recoveryCallExempt = map[string]string{
	"runBuildRecovery": "registry driver",
	"runServeRecovery": "registry driver",
}

func TestEverySubsystemOwnerRecoveryRunsThroughTheRegistry(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	var offenders []string

	walkComposeRoot(t, fset, func(path string, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := recoveryCallName(call)
			if name == "" {
				return true
			}
			if _, exempt := recoveryCallExempt[name]; exempt {
				return true
			}
			if withinRecoveryEntry(file, call) {
				return true
			}
			offenders = append(offenders, filepath.Base(path)+": "+name+
				" at line "+lineOf(fset, call.Pos()))
			return true
		})
	})

	if len(offenders) > 0 {
		t.Fatalf("startup recovery must register a bootrecovery.Entry instead of "+
			"calling Recover* inline — ordering and failure policy belong to the "+
			"registry:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// Omitted entry fields prevent startup registration.
func TestRecoveryEntriesDeclareKindAndPhase(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	var offenders []string

	walkComposeRoot(t, fset, func(path string, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isRecoveryEntryLit(lit) {
				return true
			}
			fields := map[string]bool{}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok {
					fields[key.Name] = true
				}
			}
			for _, required := range []string{"Name", "Kind", "Phase", "Run"} {
				if !fields[required] {
					offenders = append(offenders, filepath.Base(path)+
						": entry at line "+lineOf(fset, lit.Pos())+" omits "+required)
				}
			}
			return true
		})
	})

	if len(offenders) > 0 {
		t.Fatalf("every bootrecovery.Entry declares Name, Kind, Phase, and Run:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

func walkComposeRoot(t *testing.T, fset *token.FileSet, visit func(string, *ast.File)) {
	t.Helper()
	err := filepath.WalkDir(recoveryComposeRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		visit(path, file)
		return nil
	})
	testutil.FailErr(t, "walk app composition owners", err)
}

func recoveryCallName(call *ast.CallExpr) string {
	var name string
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		name = fn.Sel.Name
	case *ast.Ident:
		name = fn.Name
	default:
		return ""
	}
	if !strings.HasPrefix(name, "Recover") && !strings.HasPrefix(name, "runRecover") {
		return ""
	}
	return name
}

func isRecoveryEntryLit(lit *ast.CompositeLit) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "bootrecovery" && sel.Sel.Name == "Entry"
}

// A recovery call may sit inside the entry's Run closure.
func withinRecoveryEntry(file *ast.File, target *ast.CallExpr) bool {
	found := false
	var stack []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		if n == target {
			for _, ancestor := range stack {
				if lit, ok := ancestor.(*ast.CompositeLit); ok && isRecoveryEntryLit(lit) {
					found = true
					return false
				}
			}
		}
		stack = append(stack, n)
		return true
	})
	return found
}

func lineOf(fset *token.FileSet, pos token.Pos) string {
	return strings.TrimPrefix(fset.Position(pos).String(), fset.Position(pos).Filename+":")
}
