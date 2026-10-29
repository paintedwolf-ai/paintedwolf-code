package contract

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestPreparedCachesPublishBeforeLintDiagnostics(t *testing.T) {
	t.Parallel()
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name, Uses, Run, If string
				Continue            bool `yaml:"continue-on-error"`
				With                map[string]string
			}
		}
	}
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/build-caches.yml")
	contractcheck.FailErr(t, "decode cache warming", yaml.Unmarshal([]byte(data), &workflow))
	steps := workflow.Jobs["verification"].Steps
	prepared, recorded := false, false
	published := map[string]bool{}
	for _, step := range steps {
		if step.Name == "Warm desktop workspace builds" {
			prepared = true
		}
		if step.Name == "Record lint diagnostics" {
			recorded = true
			if !step.Continue || !strings.Contains(step.If, "steps.state.outputs.maintenance == 'true'") || !strings.Contains(step.If, "runner.os == 'Linux'") || step.Run != "./task lint:full" {
				t.Error("lint diagnostics must remain optional and Linux-only")
			}
			for _, name := range []string{"cache-go", "cache-decide", "cache-document-core", "cache-notices-tools", "cache-analysis-tools", "setup-webkit-atspi-images"} {
				if !published["./.github/actions/"+name] {
					t.Errorf("%s is not published before lint diagnostics", name)
				}
			}
		}
		if step.With["save"] != "true" {
			continue
		}
		if step.Uses == "./.github/actions/cache-lint-diagnostics" {
			if !recorded {
				t.Error("lint diagnostics publish before they are recorded")
			}
			continue
		}
		if strings.HasPrefix(step.Uses, "./.github/actions/cache-") || step.Uses == "./.github/actions/setup-webkit-atspi-images" {
			if !prepared || recorded {
				t.Errorf("%s must publish between compilation and lint diagnostics", step.Uses)
			}
			if !strings.Contains(step.If, "github.ref == 'refs/heads/main'") || strings.Contains(step.If, "always()") {
				t.Errorf("%s must require successful preparation on main", step.Uses)
			}
			published[step.Uses] = true
		}
	}
	if !prepared || !recorded {
		t.Error("cache warming requires preparation and diagnostics phases")
	}
}

func TestReleaseCompilerPreparationPrecedesSharedCachePublication(t *testing.T) {
	t.Parallel()
	jobs := hostedJobs(t, "build-caches")
	if _, separate := jobs["release"]; separate {
		t.Fatal("release compilation must retain the platform warmer's compiler inputs")
	}
	prepared := false
	for _, step := range jobs["verification"].Steps {
		if step.Run == "./task setup-dev -- --release-cache" {
			prepared = true
			if !strings.Contains(step.If, "runner.os == 'macOS'") || !strings.Contains(step.If, "steps.setup.outputs.go-cache-hit") {
				t.Fatal("release warming must cover missing Go inputs on macOS")
			}
		}
		if step.Uses == "./.github/actions/cache-go" && step.With["save"] == "true" && !prepared {
			t.Fatal("Go cache publication precedes release compilation")
		}
	}
	if !prepared {
		t.Fatal("platform warmer does not prepare release compilation")
	}
}
