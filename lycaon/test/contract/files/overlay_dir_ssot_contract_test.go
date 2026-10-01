package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Protected settings paths share one overlay directory definition.
func TestOverlayDirNameHasOneDefinition(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	literal := `"` + settingsoverlay.DirName() + `"`
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "node_modules", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == filepath.Join("internal", "settingsoverlay", "basenames.go") {
			return nil
		}
		// Vendored scanner fixtures contain independent path literals.
		if strings.HasPrefix(rel, filepath.Join("config", "runtime")) {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), literal) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	contractcheck.FailErr(t, "walk", err)

	if len(offenders) > 0 {
		t.Fatalf("overlay directory name is spelled out in %d file(s); use settingsoverlay.DirName():\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}
