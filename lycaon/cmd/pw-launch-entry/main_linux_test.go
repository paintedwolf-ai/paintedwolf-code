//go:build linux

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/launchguard"
)

func TestPWLaunchEntryRefusesInvalidAndUnsealedInvocations(t *testing.T) {
	if len(os.Environ()) != 0 {
		t.Fatalf("init did not scrub ambient environment: %v", os.Environ())
	}
	for name, args := range map[string][]string{
		"desktop without frame":      {"pw-launch-entry"},
		"broker agent without frame": {"pw-launch-entry", launchguard.BrokerAgentArgument},
		"intake without transition":  {"pw-launch-entry", launchguard.MountIntakeArgument},
		"view without transition":    {"pw-launch-entry", launchguard.MountViewArgument},
		"unknown argument":           {"pw-launch-entry", "--other"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr strings.Builder
			if code := run(args, &stderr); code != 1 || !strings.Contains(stderr.String(), "protected launch refused") {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
		})
	}
}
