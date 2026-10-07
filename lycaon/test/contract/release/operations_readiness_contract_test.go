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
	for _, needle := range []string{"package-ecosystem: github-actions", "interval: weekly"} {
		if !strings.Contains(dependabot, needle) {
			t.Fatalf("dependabot.yml missing %q", needle)
		}
	}
}

func TestReleaseSafetyPrimitivesStayWired(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	release := contractcheck.ReadRepoFile(t, root, ".github/workflows/release.yml")
	releaseDrivers := release + contractcheck.ReadRepoFile(t, root, "scripts/release-stage-platform-artifacts.sh")
	for _, needle := range []string{
		"needs.classify.outputs.publish == 'true'",
		"scripts/verify-updater-signature.sh",
		"scripts/release-r2-immutable-put.sh",
		"scripts/release_distribution.py casks",
		"activate-updater:",
		"scripts/release-r2-publish-pointer.sh",
	} {
		if !strings.Contains(releaseDrivers, needle) {
			t.Fatalf("release workflow missing safety primitive %q", needle)
		}
	}
	immutable := contractcheck.ReadRepoFile(t, root, "scripts/release-r2-immutable-put.sh")
	for _, needle := range []string{
		"cmp -s",
		"already exists with different bytes",
		"max-age=31536000, immutable",
		"r2_rest_object_stat",
	} {
		if !strings.Contains(immutable, needle) {
			t.Fatalf("immutable R2 helper missing %q", needle)
		}
	}
	pointer := contractcheck.ReadRepoFile(t, root, "scripts/release-r2-publish-pointer.sh")
	for _, needle := range []string{"no-cache, no-store, must-revalidate", "cmp -s", "release_pointer_headers.py", `"${DOWNLOAD_BASE_URL%/}/${KEY}"`} {
		if !strings.Contains(pointer, needle) {
			t.Fatalf("updater pointer helper missing %q", needle)
		}
	}
}
