package rules_test

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadPathExcludesBundledDefault(t *testing.T) {
	cfg, err := rules.LoadPathExcludes()
	if err != nil {
		testutil.FailErr(t, "load bundled scan-excludes", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("version = %d, want 1", cfg.Version)
	}
	patterns := cfg.Patterns()
	if len(patterns) < 20 {
		t.Fatalf("floor collapsed to %d patterns: %v", len(patterns), patterns)
	}
	seen := map[string]int{}
	for _, p := range patterns {
		seen[p]++
	}
	// target, _build, and vendor are each claimed by more than one ecosystem;
	// the argv must carry one --exclude apiece.
	for p, n := range seen {
		if n > 1 {
			t.Fatalf("pattern %q flattened %d times", p, n)
		}
	}
	if got := len(cfg.GroupLeads()); got != len(cfg.Groups) {
		t.Fatalf("group leads = %d, want one per group (%d)", got, len(cfg.Groups))
	}
}

// Patterns come only from the staged scan-excludes.yaml.
func TestLoadPathExcludesReadsStagedCatalog(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{
		config.ScanExcludes: "version: 1\ngroups:\n  - id: only\n    languages: [go]\n    patterns: [my_deps]\n",
	})

	cfg, err := rules.LoadPathExcludes()
	if err != nil {
		testutil.FailErr(t, "load staged catalog", err)
	}
	if got := cfg.Patterns(); len(got) != 1 || got[0] != "my_deps" {
		t.Fatalf("patterns = %v, want [my_deps]", got)
	}
}

func TestLoadPathExcludesRejectsEmptyGroups(t *testing.T) {
	configtest.Only(t, map[config.Rel]string{config.ScanExcludes: "version: 1\n"})
	if _, err := rules.LoadPathExcludes(); err == nil {
		t.Fatal("expected error for a scan-excludes.yaml with no groups")
	}
}
