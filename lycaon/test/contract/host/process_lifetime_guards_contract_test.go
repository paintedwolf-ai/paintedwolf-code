package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSessionResourceDisposalStaysReachableFromProduction(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	session := filepath.Join(root, "lycaon", "internal", "session")

	chats := filepath.Join(session, "chats")
	if sites := receiverCallSites(t, chats, "m", "DisposeRuntime", ""); len(sites) == 0 {
		t.Fatal("chat deletion does not dispose its runtime resource scope")
	}
	if sites := receiverCallSites(t, chats, "m.resources", "Dispose", ""); len(sites) == 0 {
		t.Fatal("chat runtime disposal does not reach the lifecycle registry")
	}
	terminal := filepath.Join(session, "coordinatorcontrol")
	if sites := receiverCallSites(t, terminal, "m.Digests", "Forget", ""); len(sites) == 0 {
		t.Fatal("terminal worker transition does not release job-keyed wake digests")
	}
	app := filepath.Join(root, "lycaon", "internal", "app")
	if sites := receiverCallSites(t, app, "", "RegisterCleanup", ""); len(sites) == 0 {
		t.Fatal("session cleanup registration is missing from app wiring")
	}
}

// receiverCallSites lists non-test files under dir holding a call to name on
// receiver recv, skipping the file that declares it. An empty recv matches any
// receiver, for names that are unique across the tree.
func receiverCallSites(t *testing.T, dir, recv, name, declaredIn string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var out []string

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if declaredIn != "" && filepath.Base(path) == declaredIn {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != name {
				return true
			}
			if recv != "" {
				if hookReceiverPath(sel.X) != recv {
					return true
				}
			}
			out = append(out, path+":"+itoaLine(fset, call.Pos()))
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk "+dir, err)
	return out
}
