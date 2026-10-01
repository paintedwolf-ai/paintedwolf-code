package nativemanifest_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBundledFamiliesValid(t *testing.T) {
	cfg, err := nativemanifest.Load()
	testutil.FailErr(t, "nativemanifest.Load", err)
	if err := cfg.ValidateFamilies(); err != nil {
		t.Fatalf("ValidateFamilies: %v", err)
	}
	for _, want := range []string{"command", "terminal", "page"} {
		if len(cfg.FamilyMembers(want)) == 0 {
			t.Fatalf("missing family %s", want)
		}
	}
	implied := cfg.ResourceImpliedTools(nativemanifest.ResourceFactCommandJobs)
	if len(implied) == 0 {
		t.Fatal("command_jobs has no implied tools")
	}
	for _, name := range implied {
		if name == "command" {
			t.Fatal("command_jobs must not imply command")
		}
	}
}

func TestValidateFamiliesRejectsOverlapAndUnknown(t *testing.T) {
	cfg := nativemanifest.Config{
		ApprovalReversibility: map[string]string{"command": nativemanifest.TierRecoverable},
		Families: map[string][]string{
			"command": {"command", "missing"},
			"other":   {"command"},
		},
		Companions:      map[string][]string{"command": {"command", "absent"}, "ghost": {"command"}},
		ResourceImplied: map[string][]string{"not_a_fact": {"command"}},
	}
	err := cfg.ValidateFamilies()
	if err == nil {
		t.Fatal("expected invalid families")
	}
	got := err.Error()
	for _, want := range []string{"missing", "command in families", "unknown fact", "command names itself", "unknown companion absent", "unknown tool ghost"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q missing %q", got, want)
		}
	}
}
