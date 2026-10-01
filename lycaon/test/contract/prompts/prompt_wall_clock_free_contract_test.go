package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestPromptRenderingHasNoWallClockCalls keeps time.Now() out of the prompt
// render path. The prompt assembly cache keys on rendered content, so an
// embedded timestamp turns every render into a miss. The allowlist is for
// renders that carry an explicit timestamp.
func TestPromptRenderingHasNoWallClockCalls(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	promptsDir := filepath.Join(root, "lycaon", "internal", "prompts")

	clock := regexp.MustCompile(`\btime\.(Now|Since|Until)\b`)
	var hits []string

	err := filepath.Walk(promptsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if clock.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "operation failed", err)
	if len(hits) > 0 {
		sort.Strings(hits)
		t.Fatalf("internal/prompts/ production source must not call time.Now/Since/Until "+
			"(would break prompt cache determinism). Found:\n  %s\n"+
			"If a render genuinely needs a timestamp, take it as an explicit parameter "+
			"so the caller controls it (and the prompt cache can key on it).",
			strings.Join(hits, "\n  "))
	}
}
