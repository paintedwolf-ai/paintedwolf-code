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
	for _, want := range []string{"processes", "held_call", "terminal", "page"} {
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
		ResourceImplied: map[string][]string{"not_a_fact": {"command"}},
	}
	err := cfg.ValidateFamilies()
	if err == nil {
		t.Fatal("expected invalid families")
	}
	got := err.Error()
	for _, want := range []string{"missing", "command in families", "unknown fact"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q missing %q", got, want)
		}
	}
}

func TestValidateFamiliesRejectsLauncherInControlFamily(t *testing.T) {
	tools := []string{"terminal_open", "terminal_send", "terminal_close", "wait", "command_output"}
	tiers := map[string]string{}
	for _, name := range tools {
		tiers[name] = nativemanifest.TierReversible
	}
	valid := nativemanifest.Config{
		ApprovalReversibility: tiers,
		Families: map[string][]string{
			"terminal":    {"terminal_send", "terminal_close"},
			"command_job": {"command_output"},
		},
		ResourceImplied: map[string][]string{
			nativemanifest.ResourceFactTerminals:   {"terminal_send", "terminal_close", "wait"},
			nativemanifest.ResourceFactCommandJobs: {"command_output", "wait"},
		},
	}
	testutil.FailErr(t, "controls in one family", valid.ValidateFamilies())

	withLauncher := valid
	withLauncher.Families = map[string][]string{
		"terminal":    {"terminal_open", "terminal_send", "terminal_close"},
		"command_job": {"command_output"},
	}
	split := valid
	split.Families = map[string][]string{
		"terminal":    {"terminal_send"},
		"command_job": {"command_output"},
	}
	for name, cfg := range map[string]nativemanifest.Config{"launcher in family": withLauncher, "controls split": split} {
		err := cfg.ValidateFamilies()
		if err == nil || !strings.Contains(err.Error(), nativemanifest.ResourceFactTerminals) {
			t.Fatalf("%s: err = %v, want a fault naming %s", name, err, nativemanifest.ResourceFactTerminals)
		}
	}
}
