package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSingleCompleteStreamImplementation(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	coordinatorDir := filepath.Join(root, "lycaon", "internal", "coordinator")
	sessionDir := filepath.Join(root, "lycaon", "internal", "session")
	var bodies []string
	for _, dir := range []string{coordinatorDir, sessionDir} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), "func (") && strings.Contains(string(data), " completeStream(") {
				rel, err := filepath.Rel(coordinatorDir, path)
				if err != nil {
					return err
				}
				bodies = append(bodies, rel)
			}
			return nil
		})
		contractcheck.FailErr(t, "operation failed", err)
	}
	if len(bodies) != 1 || bodies[0] != "promptloop/stream.go" {
		t.Fatalf("expected single completeStream in coordinator/promptloop/stream.go, found %v", bodies)
	}
}
