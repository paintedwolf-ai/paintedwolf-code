package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestNoCompositionDiscouragementInCatalog(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scanRoots := []string{
		filepath.Join(root, "lycaon", "config", "packs"),
		filepath.Join(root, "lycaon", "internal", "commandsurface"),
	}
	forbidden := []string{
		"separate command calls",
		"separate `command` calls",
		"pipes never execute",
		"No pipes/compounds",
	}
	for _, scanRoot := range scanRoots {
		err := filepath.WalkDir(scanRoot, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".md") && !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			lower := strings.ToLower(string(data))
			for _, phrase := range forbidden {
				if strings.Contains(lower, strings.ToLower(phrase)) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s contains discouraged phrase %q", rel, phrase)
				}
			}
			return nil
		})
		contractcheck.FailErr(t, "walk "+scanRoot, err)
	}
}
