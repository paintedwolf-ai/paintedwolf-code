package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestNoKinOpenAPIInInternalPackages(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internalDir := filepath.Join(root, "lycaon", "internal")
	var hits []string
	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "getkin/kin-openapi") {
			hits = append(hits, filepath.Base(path))
		}
		return nil
	})
	contractcheck.FailErr(t, "operation failed", err)
	if len(hits) > 0 {
		t.Fatalf("kin-openapi must not appear under internal/: %v", hits)
	}
}
