package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestNoHeuristicsInTranscriptOrderingPath scans Den transcript modules for
// banned ordering heuristics (Date.parse on message clocks).
func TestNoHeuristicsInTranscriptOrderingPath(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon-den", "src", "chat", "transcript")
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
			return nil
		}
		if strings.Contains(path, ".test.") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		if strings.Contains(text, "Date.parse") {
			t.Errorf("%s: Date.parse forbidden in transcript ordering path", path)
		}
		return nil
	})
	testutil.FailErr(t, "walk transcript", err)
}
