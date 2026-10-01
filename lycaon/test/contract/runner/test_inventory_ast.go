package contract

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func walkTestFiles(t *testing.T, root string, visit func(pkg string, fset *token.FileSet, file *ast.File, src []byte)) {
	t.Helper()
	lycaonRoot := filepath.Join(root, "lycaon")
	corpus, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	contractcheck.FailErr(t, "load lycaon Go corpus", err)
	for _, file := range corpus.Files() {
		if !file.IsTest {
			continue
		}
		pkg := "./" + filepath.ToSlash(filepath.Dir(file.Rel))
		visit(pkg, corpus.Fset, file.AST, file.Bytes())
	}
}

func testFuncBodiesByPackage(t *testing.T, root string) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	walkTestFiles(t, root, func(pkg string, fset *token.FileSet, file *ast.File, src []byte) {
		if out[pkg] == nil {
			out[pkg] = map[string]string{}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			start := fset.Position(fn.Body.Pos()).Offset
			end := fset.Position(fn.Body.End()).Offset
			out[pkg][fn.Name.Name] = string(src[start:end])
		}
	})
	if len(out) == 0 {
		t.Fatal("parsed no test functions under lycaon/ — the inventory scan is broken")
	}
	return out
}

func allGoTestNames(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	walkTestFiles(t, root, func(_ string, _ *token.FileSet, file *ast.File, _ []byte) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				out[fn.Name.Name] = struct{}{}
			}
		}
	})
	if len(out) == 0 {
		t.Fatal("found no Test functions under lycaon/ — the name scan is broken")
	}
	return out
}

func packagesCallingHelper(t *testing.T, root, helper string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for pkg, funcs := range testFuncBodiesByPackage(t, root) {
		reached := map[string]bool{}
		for name, body := range funcs {
			if callsIdentifier(body, helper) {
				reached[name] = true
			}
		}
		for grew := true; grew; {
			grew = false
			for name, body := range funcs {
				if reached[name] {
					continue
				}
				for target := range reached {
					if callsIdentifier(body, target) {
						reached[name] = true
						grew = true
						break
					}
				}
			}
		}
		for name := range reached {
			if strings.HasPrefix(name, "Test") {
				out[pkg] = append(out[pkg], name)
			}
		}
		if len(out[pkg]) == 0 {
			delete(out, pkg)
		}
	}
	return out
}

// Source text avoids type-checking the full test tree.
func callsIdentifier(body, name string) bool {
	for offset := 0; ; {
		idx := strings.Index(body[offset:], name)
		if idx < 0 {
			return false
		}
		start := offset + idx
		offset = start + len(name)
		if start > 0 && isGoIdentByte(body[start-1]) {
			continue // Part of a longer identifier.
		}
		if strings.HasPrefix(strings.TrimLeft(body[offset:], " \t\n"), "(") {
			return true
		}
	}
}

func isGoIdentByte(b byte) bool {
	return b == '_' || b == '.' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
