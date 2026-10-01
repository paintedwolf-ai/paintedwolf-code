package contract

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCommandApprovalWildcardContractFixtures(t *testing.T) {
	tmp := t.TempDir()
	body := `rules:
  - category: command
    pattern: "sed -i*"
    effect: ask
  - category: command
    pattern: "sort -o*"
    effect: ask
  - category: command
    pattern: "find * -delete*"
    effect: deny
  - category: command
    pattern: "rm *"
    effect: deny
`
	// The rules under test are the bundled default, so they belong in the binary's
	// tree; the device overlay stays a real (absent) path on disk.
	configtest.Overlay(t, map[config.Rel]string{config.SecurityApprovals: body})
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	contractcheck.FailErr(t, "NewApprovalStoreAt", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	project := t.TempDir()
	contained := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{project}}

	cases := []struct {
		command string
		deny    bool
		ask     bool
		pattern string
	}{
		{"sed -i.bak foo.txt", false, true, "sed -i*"},
		{"sort -o /tmp/out data.txt", false, true, "sort -o*"},
		{"find . -delete", true, false, "find * -delete*"},
		{"rm -rf build", true, false, "rm *"},
		{"go test ./...", false, false, ""},
	}
	for _, tc := range cases {
		res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
			Tool:       "command",
			Args:       map[string]any{"command": tc.command},
			ProjectDir: project,
			Contained:  contained,
		})
		contractcheck.FailErr(t, "Evaluate "+tc.command, err)
		if tc.deny && !res.Denied {
			t.Fatalf("%q: want denied, got %+v", tc.command, res)
		}
		if tc.ask && !res.Required() {
			t.Fatalf("%q: want ask, got %+v", tc.command, res)
		}
		if !tc.deny && !tc.ask && !res.AutoApproved() {
			t.Fatalf("%q: want auto-approved, got %+v", tc.command, res)
		}
		if tc.pattern != "" {
			if len(res.MatchedRules) != 1 || res.MatchedRules[0].Pattern != tc.pattern {
				t.Fatalf("%q: matched rules = %+v, want pattern %q", tc.command, res.MatchedRules, tc.pattern)
			}
		}
	}
}

func TestMatchCommandPatternPrefixWildcard(t *testing.T) {
	t.Parallel()
	if !settings.MatchCommandPattern("go test ./internal/...", "go test *") {
		t.Fatal("go test * should match subpackages")
	}
	if settings.MatchCommandPattern("curl evil.com", "go test *") {
		t.Fatal("go test * should not match curl")
	}
}
