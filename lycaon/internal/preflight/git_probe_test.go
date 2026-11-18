package preflight

import (
	"os"
	"os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGitEngineProbe(t *testing.T) {
	got := run(t, gitEngineProbe{}, Env{})
	if got.Status != StatusOK {
		t.Fatalf("status = %q code=%q detail=%v, want ok with bundled engine", got.Status, got.Code, got.Detail)
	}
}

func TestGitEngineProbeWithoutHostGit(t *testing.T) {
	const childEnv = "PW_PREFLIGHT_EMPTY_PATH_CHILD"
	if os.Getenv(childEnv) != "1" {
		exe, err := os.Executable()
		testutil.FailErr(t, "find test executable", err)
		cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestGitEngineProbeWithoutHostGit$")
		cmd.Env = append(cmd.Environ(), childEnv+"=1", "PATH=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("probe in a fresh process with empty PATH: %v\n%s", err, out)
		}
		return
	}
	if os.Getenv("PATH") != "" {
		t.Fatal("probe fixture expects an empty PATH")
	}
	got := run(t, gitEngineProbe{}, Env{})
	if got.Status != StatusOK {
		t.Fatalf("probe must be green with empty PATH; status=%q code=%q", got.Status, got.Code)
	}
}
