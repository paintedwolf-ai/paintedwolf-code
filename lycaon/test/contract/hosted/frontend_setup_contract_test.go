package contract

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestFrontendSetupSkipsGoCachesAndProvisionsNativeInputs(t *testing.T) {
	t.Parallel()
	var action struct {
		Runs struct {
			Steps []struct{ Name, If, Run, Uses string }
		}
	}
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/actions/setup-verification/action.yml")
	contractcheck.FailErr(t, "decode verification setup", yaml.Unmarshal([]byte(data), &action))
	skipped := map[string]bool{
		"Fetch pinned git engine": false, "Fetch pinned text recognition engine": false,
		"Provision pinned Go modules": false, "Set up maintained OpenGrep": false,
	}
	cacheSkipped, nativeInputs := false, false
	for _, step := range action.Runs.Steps {
		if _, ok := skipped[step.Name]; ok {
			skipped[step.Name] = strings.Contains(step.If, "inputs.setup != 'frontend'")
		}
		if step.Uses == "./.github/actions/cache-go" {
			cacheSkipped = step.If == "inputs.setup != 'frontend'"
		}
		if step.If == "inputs.lookup-only != 'true' && (inputs.setup == 'frontend')" &&
			strings.Contains(step.Run, "./task --version") &&
			strings.Contains(step.Run, "cargo fetch --locked --manifest-path lycaon/internal/documentcore/native/Cargo.toml") {
			nativeInputs = true
		}
	}
	for name, guarded := range skipped {
		if !guarded {
			t.Errorf("frontend setup must skip %s", name)
		}
	}
	if !cacheSkipped || !nativeInputs {
		t.Errorf("frontend setup must skip the Go payload and provision document core: cache skipped %v, native inputs %v", cacheSkipped, nativeInputs)
	}
}
