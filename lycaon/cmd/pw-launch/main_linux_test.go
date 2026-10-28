//go:build linux

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/launchguard"
)

func TestPWLaunchRefusesUnprivilegedBootstrapAndStage(t *testing.T) {
	if len(os.Environ()) != 0 {
		t.Fatalf("init did not scrub ambient environment: %v", os.Environ())
	}
	var stderr strings.Builder
	if code := run([]string{"pw-launch"}, &stderr); code != 1 || !strings.Contains(stderr.String(), "privileged bootstrap transition is unavailable") {
		t.Fatalf("bootstrap exit=%d stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	if code := run([]string{"pw-launch", launchguard.StageArgument}, &stderr); code != 1 || !strings.Contains(stderr.String(), "administrator identity") {
		t.Fatalf("stage exit=%d stderr=%q", code, stderr.String())
	}
}
