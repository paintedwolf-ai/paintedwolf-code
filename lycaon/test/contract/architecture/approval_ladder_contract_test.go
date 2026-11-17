package contract

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRecommendedOptionOnlySetInPlanConstruction(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var faceWriters []string

	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal"))
	contractcheck.FailErr(t, "load internal Go corpus", err)
	for _, source := range corpus.Files() {
		if source.IsTest || source.Rel == "hitl/approval_plan.go" || source.Rel == "hitl/checkpoint_wire.go" {
			continue
		}
		rel, _ := filepath.Rel(root, source.Path)
		ast.Inspect(source.AST, func(n ast.Node) bool {
			if fn, ok := n.(*ast.FuncDecl); ok && source.Rel == "hitl/approval_offers.go" && fn.Name.Name == "refaceAfterFilter" && fn.Recv != nil && len(fn.Recv.List) == 1 {
				if ptr, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
					if owner, ok := ptr.X.(*ast.Ident); ok && owner.Name == "ApprovalPlan" {
						return false
					}
				}
			}
			switch node := n.(type) {
			case *ast.KeyValueExpr:
				if key, ok := node.Key.(*ast.Ident); ok && key.Name == "RecommendedOptionID" {
					faceWriters = append(faceWriters, rel+": RecommendedOptionID literal")
				}
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "RecommendedOptionID" {
						faceWriters = append(faceWriters, rel+": RecommendedOptionID assign")
					}
				}
			}
			return true
		})
	}

	if len(faceWriters) > 0 {
		t.Fatalf("face may only be computed by approval plan construction/refacing or checkpoint wire projection:\n  %s", strings.Join(faceWriters, "\n  "))
	}
}

func mustApprovalStore(t *testing.T) *settings.ApprovalStore {
	t.Helper()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	contractcheck.FailErr(t, "NewApprovalStoreAt", err)
	return store
}
