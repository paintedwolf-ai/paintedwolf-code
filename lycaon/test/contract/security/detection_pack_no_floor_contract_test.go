package contract

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestDetectionPackSettingsDoesNotImportDetectionpack guards import inversion.
func TestDetectionPackSettingsDoesNotImportDetectionpack(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "settings")
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			pathLit, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if pathLit == "github.com/lycaon/lycaon/internal/detectionpack" ||
				strings.HasSuffix(pathLit, "/internal/detectionpack") {
				rel, _ := filepath.Rel(contractcheck.RepoRoot(t), path)
				t.Errorf("%s imports internal/detectionpack", rel)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk settings imports", err)
}
