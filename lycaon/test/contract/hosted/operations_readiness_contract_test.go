package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestGitHubActionsAreCommitPinnedAndMaintained(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	uses := regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*([^\s#]+)@([^\s#]+)`)
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	workflowDir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	contractcheck.FailErr(t, "read workflows", err)
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml")) {
			continue
		}
		body := contractcheck.ReadRepoFile(t, root, filepath.Join(".github", "workflows", entry.Name()))
		for _, match := range uses.FindAllStringSubmatch(body, -1) {
			if strings.HasPrefix(match[1], "./") {
				continue
			}
			if !sha.MatchString(match[2]) {
				t.Errorf("%s uses mutable action ref %s@%s", entry.Name(), match[1], match[2])
			}
		}
	}
	dependabot := contractcheck.ReadRepoFile(t, root, ".github/dependabot.yml")
	for _, needle := range []string{"package-ecosystem: github-actions", "interval: monthly"} {
		if !strings.Contains(dependabot, needle) {
			t.Fatalf("dependabot.yml missing %q", needle)
		}
	}
}
