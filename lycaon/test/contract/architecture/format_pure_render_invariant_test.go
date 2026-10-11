package contract

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/testcorpus"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// purityClaim names a function assumed to perform no I/O.
type purityClaim struct {
	label    string
	pkgRel   string
	receiver string
	funcName string
}

// purityClaims lists functions covered by this contract.
var purityClaims = []purityClaim{
	{"(*StaticRejectFormatter).Format", "internal/guidance", "StaticRejectFormatter", "Format"},
	{"EnvelopeHintMessage", "internal/guidance", "", "EnvelopeHintMessage"},
	{"(*DefaultService).Validate", "internal/parse", "DefaultService", "Validate"},
	{"(*Runner).ValidateStages", "internal/hostcmd", "Runner", "ValidateStages"},
	{"ParseOutput", "internal/scan", "", "ParseOutput"},
	{"(*guidancedelivery.Service).Emit", "internal/session/guidancedelivery", "Service", "Emit"},
	{"(*guidancedelivery.Service).EmitEager", "internal/session/guidancedelivery", "Service", "EmitEager"},
}

// purityForbiddenImports contains unambiguous I/O boundaries.
var purityForbiddenImports = map[string]string{
	"net":          "TCP/UDP I/O",
	"net/http":     "HTTP I/O",
	"database/sql": "DB I/O",
	"os/exec":      "subprocess execution",
	"syscall":      "raw syscalls",
}

func TestPurityClaimsHoldNoIO(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, claim := range purityClaims {
		t.Run(claim.label, func(t *testing.T) {
			t.Parallel()
			scanPurityClaim(t, root, claim)
		})
	}
}

func scanPurityClaim(t *testing.T, root string, claim purityClaim) {
	t.Helper()
	dir := filepath.Join(root, "lycaon", claim.pkgRel)
	pkgFiles := parseGoDirForPurity(t, dir)

	funcs, methods, fileOf := indexPackageDecls(pkgFiles)
	target := lookupDecl(funcs, methods, claim.receiver, claim.funcName)
	if target == nil {
		t.Fatalf("could not find %s in %s", claim.label, claim.pkgRel)
	}

	reachable := map[*ast.File]string{}
	visited := map[*ast.FuncDecl]bool{}
	walkSamePackageCalls(target, funcs, methods, fileOf, visited, reachable)

	seenImports := collectImports(reachable)

	var violations []string
	for path, reason := range purityForbiddenImports {
		if loc, used := seenImports[path]; used {
			violations = append(violations, fmt.Sprintf("%s (%s) via %s", path, reason, loc))
		}
	}
	if len(violations) == 0 {
		return
	}
	sort.Strings(violations)
	t.Fatalf(
		"%s transitively reaches I/O packages:\n  - %s",
		claim.label, strings.Join(violations, "\n  - "),
	)
}

// parseGoDirForPurity returns non-test files with identifier bindings.
func parseGoDirForPurity(t *testing.T, dir string) []testcorpus.GoFile {
	t.Helper()
	corpus, err := contractcheck.LoadGoASTCorpusMode(dir, 0)
	contractcheck.FailErr(t, "load purity corpus", err)
	return testcorpus.RequireNonEmpty(t, "purity Go files", corpus.Production())
}

func indexPackageDecls(files []testcorpus.GoFile) (funcs map[string]*ast.FuncDecl, methods map[string]*ast.FuncDecl, fileOf map[*ast.FuncDecl]testcorpus.GoFile) {
	funcs = map[string]*ast.FuncDecl{}
	methods = map[string]*ast.FuncDecl{}
	fileOf = map[*ast.FuncDecl]testcorpus.GoFile{}
	for _, pf := range files {
		for _, decl := range pf.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			fileOf[fn] = pf
			if fn.Recv == nil || len(fn.Recv.List) == 0 {
				funcs[fn.Name.Name] = fn
				continue
			}
			recvType := fn.Recv.List[0].Type
			if star, ok := recvType.(*ast.StarExpr); ok {
				recvType = star.X
			}
			ident, ok := recvType.(*ast.Ident)
			if !ok {
				continue
			}
			methods[ident.Name+"."+fn.Name.Name] = fn
		}
	}
	return funcs, methods, fileOf
}

func lookupDecl(funcs, methods map[string]*ast.FuncDecl, receiver, name string) *ast.FuncDecl {
	if receiver == "" {
		return funcs[name]
	}
	return methods[receiver+"."+name]
}

// receiverInfo returns a method receiver's variable and type names.
func receiverInfo(fn *ast.FuncDecl) (varName, typeName string) {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return "", ""
	}
	field := fn.Recv.List[0]
	if len(field.Names) > 0 {
		varName = field.Names[0].Name
	}
	rt := field.Type
	if star, ok := rt.(*ast.StarExpr); ok {
		rt = star.X
	}
	if ident, ok := rt.(*ast.Ident); ok {
		typeName = ident.Name
	}
	return varName, typeName
}

// walkSamePackageCalls follows resolvable calls within one package.
// Calls on other receiver values remain unresolved.
func walkSamePackageCalls(
	fn *ast.FuncDecl,
	funcs, methods map[string]*ast.FuncDecl,
	fileOf map[*ast.FuncDecl]testcorpus.GoFile,
	visited map[*ast.FuncDecl]bool,
	reachable map[*ast.File]string,
) {
	if fn == nil || visited[fn] {
		return
	}
	visited[fn] = true
	if pf, ok := fileOf[fn]; ok {
		reachable[pf.AST] = filepath.Base(pf.Path)
	}
	if fn.Body == nil {
		return
	}
	recvName, recvType := receiverInfo(fn)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			walkSamePackageCalls(funcs[fun.Name], funcs, methods, fileOf, visited, reachable)
		case *ast.SelectorExpr:
			if isPackageQualifiedCall(fun) {
				return true
			}
			// Resolve calls through the current receiver.
			ident, ok := fun.X.(*ast.Ident)
			if !ok || recvName == "" || ident.Name != recvName {
				return true
			}
			walkSamePackageCalls(methods[recvType+"."+fun.Sel.Name], funcs, methods, fileOf, visited, reachable)
		}
		return true
	})
}

func collectImports(files map[*ast.File]string) map[string]string {
	out := map[string]string{}
	for f, basename := range files {
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if _, seen := out[path]; !seen {
				out[path] = basename
			}
		}
	}
	return out
}

// isPackageQualifiedCall recognizes selectors bound to imports.
func isPackageQualifiedCall(sel *ast.SelectorExpr) bool {
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Obj == nil
}
