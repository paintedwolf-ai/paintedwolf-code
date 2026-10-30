//go:build linux

package launchguard

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Native counters must be convertible together with the managed parent's mode.
func requireManagedNativeCoverageMerge(t *testing.T, build, directory, overlay, linked string) {
	t.Helper()
	mode := testing.CoverMode()
	if mode == "" {
		mode = "atomic"
	}
	parent := filepath.Join(build, "pw-launch-parent-mode")
	goBuild(t, parent, "github.com/lycaon/lycaon/cmd/pw-launch", linked,
		"-cover", "-covermode="+mode,
		"-coverpkg=github.com/lycaon/lycaon/internal/launchguard,github.com/lycaon/lycaon/cmd/pw-launch",
		"-overlay="+overlay, launchCoverageTool(t, overlay))
	output, err := exec.Command(parent).CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 23 || len(output) != 0 {
		t.Fatalf("managed parent-mode executable did not retain the actual overlay: error=%v output=%q", err, output)
	}
	profile := filepath.Join(build, "managed-native.out")
	output, err = exec.Command("go", "tool", "covdata", "textfmt", "-i="+filepath.Join(directory, "coverage"), "-o="+profile).CombinedOutput()
	if err != nil {
		t.Fatalf("native profiles cannot join managed %s coverage: %v: %s", mode, err, output)
	}
	raw, err := os.ReadFile(profile)
	testutil.FailErr(t, "read converted native and managed counters", err)
	if !strings.Contains(string(raw), "github.com/lycaon/lycaon/internal/launchguard/") {
		t.Fatal("converted profiles lost actual launchguard source attribution")
	}
}
