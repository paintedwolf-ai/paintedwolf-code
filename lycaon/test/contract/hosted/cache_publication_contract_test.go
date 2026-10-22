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
			if !step.Continue || step.If != "runner.os == 'Linux'" || step.Run != "./task lint:full" {
				t.Error("lint diagnostics must remain optional and Linux-only")
			}
			for _, name := range []string{"cache-go", "cache-decide", "cache-notices-tools", "cache-analysis-tools", "setup-webkit-atspi-images"} {
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
