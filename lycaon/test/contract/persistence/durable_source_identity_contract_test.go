package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// durableWorkspaceIDExemptions records physical workspace observations.
var durableWorkspaceIDExemptions = map[string]string{
	"delegation_legs": "records the workspace a leg ran in; nothing is looked up by it",
}

func TestDurableTablesDoNotKeyOnPhysicalWorkspace(t *testing.T) {
	t.Parallel()
	store := testdbfixture.Open(t, "store.db")
	rows, err := store.QueryContext(t.Context(), `
		SELECT tl.name
		FROM pragma_table_list AS tl
		JOIN pragma_table_info(tl.name) AS col ON col.name = 'workspace_id'
		WHERE tl.schema = 'main' AND tl.type = 'table'
		ORDER BY tl.name
	`)
	contractcheck.FailErr(t, "list tables with workspace_id", err)
	defer func() { _ = rows.Close() }()
	var offenders []string
	for rows.Next() {
		var name string
		contractcheck.FailErr(t, "scan table", rows.Scan(&name))
		if _, exempt := durableWorkspaceIDExemptions[name]; !exempt {
			offenders = append(offenders, name)
		}
	}
	contractcheck.FailErr(t, "iterate tables", rows.Err())
	if len(offenders) > 0 {
		t.Fatalf("durable tables key on the physical workspace id: %s\n"+
			"That hash changes with the root set. Key durable source rows on project_id "+
			"plus branch_id, or record the reason in durableWorkspaceIDExemptions.",
			strings.Join(offenders, ", "))
	}
}

// sourceIdentityOwners address files by project, branch, root, and path.
var sourceIdentityOwners = []string{
	filepath.Join("lycaon", "internal", "sourceledger"),
	filepath.Join("lycaon", "internal", "editordoc"),
}

func TestSourceIdentityOwnersDoNotDerivePhysicalWorkspaces(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var offenders []string
	for _, dir := range sourceIdentityOwners {
		walkErr := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "sourceworkspace" {
					return true
				}
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+": sourceworkspace."+sel.Sel.Name)
				return true
			})
			return nil
		})
		testutil.FailErr(t, "walk "+dir, walkErr)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("source identity owners derive a physical workspace:\n  %s\n"+
			"That identity changes with the root set. Take a sourcebranch.ID instead.",
			strings.Join(offenders, "\n  "))
	}
}
