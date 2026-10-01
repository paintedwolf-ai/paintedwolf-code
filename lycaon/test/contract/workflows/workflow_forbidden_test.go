package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// forbiddenConfigSamples must never appear in bundled lycaon/config/.
var forbiddenConfigSamples = []struct {
	id          string
	yamlSnippet string
}{
	{"stage_plan_complete", "complete_when: stage_plan_complete"},
	{"verify_evidence_passed", "verify_evidence_passed: true"},
	{"user_input_clarify", "complete_when: user_input_clarify"},
	{"request_user_input", "when:\n  request_user_input: true"},
	{"mode_is", "when:\n  mode_is: spec"},
}

func TestBundledConfigHasNoForbiddenPredicateIDs(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	configRoot := filepath.Join(root, "lycaon", "config")
	entries, err := os.ReadDir(configRoot)
	contractcheck.FailErr(t, "read directory entries", err)
	for _, e := range entries {
		if e.IsDir() {
			scanConfigDir(t, filepath.Join(configRoot, e.Name()))
			continue
		}
		if strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".yml") {
			checkNoForbiddenSubstrings(t, filepath.Join(configRoot, e.Name()), forbiddenConfigSamples)
		}
	}
}

func TestForbiddenIDsAreRejectedByRegistry(t *testing.T) {
	t.Parallel()
	for _, sample := range forbiddenConfigSamples {
		if !conditions.IsForbidden(sample.id) {
			t.Fatalf("sample id %q must be forbidden", sample.id)
		}
	}
}

func TestProjectOverlayFixturesHaveNoForbiddenIDs(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	overlayRoot := filepath.Join(root, "lycaon", "test", "contract", "testdata", "project-tier")
	if _, err := os.Stat(overlayRoot); os.IsNotExist(err) {
		t.Skip("no project-tier overlay fixtures")
	}
	scanConfigDir(t, overlayRoot)
}

func scanConfigDir(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Errorf("walk %s: %v", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			checkNoForbiddenSubstrings(t, path, forbiddenConfigSamples)
		}
		return nil
	})
}

func checkNoForbiddenSubstrings(t *testing.T, path string, samples []struct {
	id          string
	yamlSnippet string
}) {
	t.Helper()
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	text := string(data)
	for _, sample := range samples {
		if strings.Contains(text, sample.id) {
			for _, line := range strings.Split(text, "\n") {
				trim := strings.TrimSpace(line)
				if strings.Contains(trim, "#") && strings.Index(trim, "#") < strings.Index(trim, sample.id) {
					continue
				}
				if strings.Contains(line, sample.id) {
					t.Errorf("%s contains forbidden id %q in: %s", path, sample.id, strings.TrimSpace(line))
				}
			}
		}
	}
}
