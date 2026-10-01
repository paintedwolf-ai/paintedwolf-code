package contract

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Pack ids, unit ids, and contribution command ids contain "/" and ":", so they
// travel as one percent-encoded path segment. chi returns the raw segment, so a
// handler reading one with chi.URLParam gets a still-encoded string that fails
// to parse. api.encodedPathID is the one reader that decodes.
func TestCompositeIDPathParamsAreReadDecoded(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "api")
	fset, files := contractcheck.ParseNonTestGoTree(t, dir)

	composite := map[string]bool{}
	for _, name := range compositeIDPathParamNames() {
		composite[name] = true
	}

	for _, file := range files {
		path := fset.File(file.Pos()).Name()
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isChiURLParam(call) || len(call.Args) != 2 {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(lit.Value)
			if err != nil || !composite[name] {
				return true
			}
			t.Errorf("%s:%d: chi.URLParam(r, %q) returns the raw path segment; "+
				"read composite ids with encodedPathID so %q arrives decoded",
				filepath.Base(path), fset.Position(call.Pos()).Line, name, name)
			return true
		})
	}
}

// isChiURLParam matches chi.URLParam(...) calls.
func isChiURLParam(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "URLParam" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "chi"
}

// compositeIDPathParamNames is the route-parameter list this gate covers.
func compositeIDPathParamNames() []string {
	return []string{"pack_id", "unit_id", "command_id", "meta_pack_id"}
}
