package contract

import (
	"go/ast"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestNoDietToolNameMaps forbids Go tool-name maps for diet routing under internal/llm (C95/C102).
func TestNoDietToolNameMaps(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "llm")
	fset, files := contractcheck.ParseNonTestGoTree(t, dir)
	forbidden := []string{
		"summarizeChunkTools",
		"isSummarizeChunkTool",
		"overlayPromoteChunkTools",
		"isOverlayPromoteChunkTool",
	}
	for _, file := range files {
		path := fset.File(file.Pos()).Name()
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, name := range forbidden {
				if id.Name == name {
					t.Errorf("%s: diet tool-name map/helper %q must not exist", filepath.Base(path), name)
				}
			}
			return true
		})
	}
}
