package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Declared evidence producers and indexer arms must stay in one-to-one correspondence.
func TestEvidenceFingerprintToolClosure(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	binding, err := evidence.LoadBinding()
	contractcheck.FailErr(t, "load evidence binding", err)

	// Native scan_* tools ground through BuildEvidenceRecord switch arms.
	declared := binding.DeclaredToolNames()

	indexed := fingerprintSwitchTools(t, root)

	contractcheck.FailSetEqual(t,
		"evidence-producing tools (evidence-kinds.yaml) vs fingerprint indexer arms (BuildEvidenceRecord)",
		declared, indexed,
	)
}

// fingerprintSwitchTools returns the tool-name literals handled by the
// `switch toolName` inside BuildEvidenceRecord, parsed from source so the test
// tracks the indexer rather than a hand-maintained copy of it.
func fingerprintSwitchTools(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "lycaon", "internal", "evidence", "fingerprint.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	contractcheck.FailErr(t, "parse fingerprint.go", err)

	var cases []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "buildEvidenceRecord" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != "toolName" {
				return true
			}
			for _, stmt := range sw.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range clause.List {
					if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						name, err := strconv.Unquote(lit.Value)
						contractcheck.FailErr(t, "unquote case literal", err)
						cases = append(cases, name)
					}
				}
			}
			return false
		})
	}
	if len(cases) == 0 {
		t.Fatal("found no `switch toolName` cases in buildEvidenceRecord; closure test cannot run")
	}
	return cases
}
