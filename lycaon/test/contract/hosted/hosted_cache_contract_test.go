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
	for _, name := range []string{"cache-go", "cache-decide", "cache-document-core", "cache-rust", "cache-notices-tools", "cache-release-build", "cache-analysis-tools", "cache-lint-diagnostics", "setup-opengrep", "setup-verification", "setup-linux-native", "setup-webkit-atspi-images"} {
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
	restored := map[string]bool{}
	for _, step := range hostedJobs(t, "release")["build-release"].Steps {
		restored[step.Uses] = true
	}
	for _, name := range []string{"cache-go", "cache-document-core", "cache-notices-tools", "cache-release-build"} {
		if !restored["./.github/actions/"+name] {
			t.Errorf("release builds must restore %s", name)
		}
	}
	if strings.Contains(data, "gocache") || strings.Contains(data, "gomodcache") {
		t.Fatal("release cache must not duplicate the shared Go payload")
	}
}

func TestMainCacheWarmingCompletesBeforeTheNextPush(t *testing.T) {
	t.Parallel()
	var workflow struct {
		Concurrency struct {
			Group  string
			Cancel *bool `yaml:"cancel-in-progress"`
		}
	}
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/build-caches.yml")
	contractcheck.FailErr(t, "decode cache warmer concurrency", yaml.Unmarshal([]byte(data), &workflow))
	if workflow.Concurrency.Group == "" || workflow.Concurrency.Cancel == nil || *workflow.Concurrency.Cancel {
		t.Fatal("main pushes must serialize cache warming without cancelling cold preparation")
	}
}

func TestNestedCachePublicationRunsAfterPreparation(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, name := range []string{"cache-go", "cache-decide", "cache-document-core", "cache-rust", "cache-notices-tools", "cache-analysis-tools", "cache-lint-diagnostics", "setup-webkit-atspi-images"} {
		data := contractcheck.ReadRepoFile(t, root, ".github/actions/"+name+"/action.yml")
		if strings.Contains(data, "uses: actions/cache@") || !strings.Contains(data, "uses: actions/cache/save@") {
			t.Errorf("%s must publish in its current action scope, not a nested post-job hook", name)
		}
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string
				Run  string
				With map[string]string
			}
		}
	}
	data := contractcheck.ReadRepoFile(t, root, ".github/workflows/build-caches.yml")
	contractcheck.FailErr(t, "decode cache publication stages", yaml.Unmarshal([]byte(data), &workflow))
	prepared := false
	published := map[string]bool{}
	for _, step := range workflow.Jobs["verification"].Steps {
		if step.Run == "./task setup-dev -- --workspace-cache" {
			prepared = true
		}
		if step.With["save"] == "true" {
			if !prepared {
				t.Fatalf("%s publishes before preparation", step.Uses)
			}
			published[step.Uses] = true
		}
	}
	for _, name := range []string{"cache-go", "cache-decide", "cache-document-core", "cache-rust", "cache-notices-tools", "cache-analysis-tools", "cache-lint-diagnostics", "setup-webkit-atspi-images"} {
		if !published["./.github/actions/"+name] {
			t.Errorf("prepared %s has no publication stage", name)
		}
	}
}

func TestNativeCacheActionsProbeExactKeysAndNeverDeleteEntries(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, name := range []string{"cache-go", "cache-decide", "cache-document-core", "cache-rust", "cache-release-build", "cache-notices-tools", "cache-analysis-tools", "cache-lint-diagnostics", "setup-linux-native", "setup-opengrep", "setup-webkit-atspi-images"} {
		data := contractcheck.ReadRepoFile(t, root, ".github/actions/"+name+"/action.yml")
		if strings.Contains(data, "CARGO_PROFILE_") {
			t.Errorf("%s must preserve caller-owned compiler profile settings", name)
		}
		if strings.Contains(data, "gh cache delete") || strings.Contains(data, "actions/caches/") {
			t.Errorf("%s must leave cache retirement to the platform", name)
		}
		if name != "cache-lint-diagnostics" && name != "setup-linux-native" && (!strings.Contains(data, "lookup-only:") || !strings.Contains(data, "cache-hit")) {
			t.Errorf("%s must expose the version-aware native lookup", name)
		}
	}
}
