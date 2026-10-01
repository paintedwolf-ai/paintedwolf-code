package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestApprovalWitnessPinsAttachedJailOnly(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	assertWitnessStructIsJailOnly(t, root)
	assertFuncOmitsOverlayFields(t, root, "lycaon/internal/hitl/witness.go", "BoundaryWitness")
	assertFuncOmitsOverlayFields(t, root, "lycaon/internal/hitl/grant_key.go", "GrantKey")
}

func assertWitnessStructIsJailOnly(t *testing.T, root string) {
	t.Helper()
	srcPath := filepath.Join(root, "lycaon", "internal", "hitl", "approval_grant.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, nil, 0)
	contractcheck.FailErr(t, "parse approval_grant.go", err)
	want := map[string]bool{"FSJailed": true, "Egress": true, "RootsDigest": true}
	var fields []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "ApprovalGrantWitness" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return false
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				fields = append(fields, name.Name)
			}
		}
		return false
	})
	if len(fields) == 0 {
		t.Fatal("ApprovalGrantWitness not found")
	}
	for _, name := range fields {
		if !want[name] {
			t.Fatalf("ApprovalGrantWitness carries overlay field %s", name)
		}
		delete(want, name)
	}
	for name := range want {
		t.Fatalf("ApprovalGrantWitness missing jail field %s", name)
	}
}

func assertFuncOmitsOverlayFields(t *testing.T, root, rel, name string) {
	t.Helper()
	src := contractcheck.ReadRepoFile(t, root, rel)
	fn := mustFindFunc(t, src, filepath.Base(rel), name)
	body := src[fn.Body.Pos()-1 : fn.Body.End()]
	for _, field := range []string{"SocketPathsDigest", "SocketCount", "BoundaryPermitsDigest", "BoundaryPermitCount"} {
		if strings.Contains(body, field) {
			t.Fatalf("%s must not pin overlay field %s", name, field)
		}
	}
}
