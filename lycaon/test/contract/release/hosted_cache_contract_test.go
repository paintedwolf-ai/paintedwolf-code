package contract

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestHostedCacheWritesRequireTheTrustedMainRef(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, name := range []string{"cache-go", "cache-decide", "cache-notices-tools", "cache-release-build", "cache-analysis-tools", "setup-opengrep", "setup-verification"} {
		var action struct {
			Runs struct {
				Steps []struct {
					Uses string
					If   string
					With map[string]string
				}
			}
		}
		data := contractcheck.ReadRepoFile(t, root, ".github/actions/"+name+"/action.yml")
		contractcheck.FailErr(t, "decode cache action "+name, yaml.Unmarshal([]byte(data), &action))
		for _, step := range action.Runs.Steps {
			write := strings.HasPrefix(step.Uses, "actions/cache@") || strings.HasPrefix(step.Uses, "actions/cache/save@")
			guard := step.If
			if strings.HasPrefix(step.Uses, "Swatinem/rust-cache@") {
				write = true
				guard = step.With["save-if"]
			}
			if write && !strings.Contains(guard, "github.ref == 'refs/heads/main'") {
				t.Errorf("%s may save outside trusted main: %s (%s)", name, step.Uses, guard)
			}
		}
	}
}

func TestReleaseBuildReusesTheSharedGoCache(t *testing.T) {
	t.Parallel()
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/actions/cache-release-build/action.yml")
	if !strings.Contains(data, "uses: ./.github/actions/cache-go") {
		t.Fatal("release builds must restore the shared Go compiler cache")
	}
	if strings.Contains(data, "gocache") || strings.Contains(data, "gomodcache") {
		t.Fatal("release cache must not duplicate the shared Go payload")
	}
}
