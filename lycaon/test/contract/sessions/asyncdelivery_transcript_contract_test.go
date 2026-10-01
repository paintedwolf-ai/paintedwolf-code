package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestDenHidesInternalVisibilityForTranscript(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	paths := []string{
		filepath.Join(root, "lycaon-den", "src", "chat", "transcript", "projection", "message-transcript.ts"),
		filepath.Join(root, "lycaon-den", "src", "chat", "transcript", "projection", "transcript-items.ts"),
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(raw)
		if !strings.Contains(src, `visibility === "internal"`) {
			t.Fatalf("%s must filter visibility=internal from Den transcript", filepath.Base(path))
		}
	}
}
