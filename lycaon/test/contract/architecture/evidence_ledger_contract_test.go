package contract

import (
	"go/ast"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestNoBuildGroundingEvidenceFromMessagesInAuditPaths(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	sites := scanTranscriptLedgerProductionSites(t, root)
	if len(sites) != 0 {
		contractcheck.FailViolations(t, "production code must not call BuildLedgerFromTranscript(messages)", sites)
	}
}

func scanTranscriptLedgerProductionSites(t *testing.T, root string) []string {
	t.Helper()
	var sites []string
	lycaonRoot := filepath.Join(root, "lycaon")
	corpus, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	contractcheck.FailErr(t, "load lycaon Go corpus", err)
	for _, source := range corpus.Files() {
		if source.IsTest {
			continue
		}
		rel, err := filepath.Rel(root, source.Path)
		contractcheck.FailErr(t, "relativize corpus path", err)
		rel = filepath.ToSlash(rel)
		if fileCallsBuildLedgerFromTranscript(source.AST) {
			sites = append(sites, rel)
		}
	}
	return sites
}

func fileCallsBuildLedgerFromTranscript(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fun.Sel.Name != "BuildLedgerFromTranscript" {
				return true
			}
			if id, ok := fun.X.(*ast.Ident); ok && (id.Name == "guidance" || id.Name == "BuildLedgerFromTranscript") {
				found = true
				return false
			}
		case *ast.Ident:
			if fun.Name == "BuildLedgerFromTranscript" {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
