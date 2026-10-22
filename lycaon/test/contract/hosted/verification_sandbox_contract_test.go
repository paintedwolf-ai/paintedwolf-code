package contract

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestVerificationSandboxGrantsCoverEngineExecutables(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	profile := contractcheck.ReadRepoFile(t, root, ".github/actions/setup-verification/apparmor-verification-userns")
	for _, path := range []string{
		"/tmp/paintedwolf-test-runs-*/**/*.test",
		"/tmp/paintedwolf-test-runs-*/**/lycaon-smoke-bin-*/lycaon",
	} {
		if !strings.Contains(profile, path+" flags=(unconfined) {\n  userns,\n}") {
			t.Errorf("verification profile must grant user namespaces to %s", path)
		}
	}
	var action struct {
		Runs struct {
			Steps []struct{ Name, If, Run string }
		}
	}
	data := contractcheck.ReadRepoFile(t, root, ".github/actions/setup-verification/action.yml")
	contractcheck.FailErr(t, "decode verification setup", yaml.Unmarshal([]byte(data), &action))
	for _, step := range action.Runs.Steps {
		if step.Name != "Allow verification engines to create user namespaces" {
			continue
		}
		if step.If != "runner.os == 'Linux'" {
			t.Error("AppArmor setup must run only on Linux")
		}
		for _, required := range []string{
			"apparmor_restrict_unprivileged_userns", "apparmor-verification-userns",
			`python3 scripts/artifact_paths.py build "$GITHUB_WORKSPACE"`,
			`"%s/lycaon-dev" flags=(unconfined)`,
			"sudo apparmor_parser -r /etc/apparmor.d/paintedwolf-verification",
		} {
			if !strings.Contains(step.Run, required) {
				t.Errorf("AppArmor setup is missing %q", required)
			}
		}
		if strings.Contains(step.Run, "sysctl -w") {
			t.Error("verification must preserve the host-wide user namespace restriction")
		}
		return
	}
	t.Fatal("verification engine AppArmor setup is missing")
}
