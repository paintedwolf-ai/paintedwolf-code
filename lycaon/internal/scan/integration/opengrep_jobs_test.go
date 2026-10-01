package integration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOpenGrepArgsIncludeJobs(t *testing.T) {
	args, err := (opengrep.Invocation{Analysis: opengrep.Analysis{Mode: opengrep.Intraprocedural}, Output: "/tmp/out.json", Rules: []string{"/rules/a.yaml"}, Targets: []string{"/repo"}, Jobs: 2}).Args()
	if err != nil {
		testutil.FailErr(t, "construct invocation", err)
	}
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--jobs" && args[i+1] == "2" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("args missing --jobs 2: %v", args)
	}
}

// Explicit excludes preserve the bundled floor with project ignore files.
func TestOpenGrepArgsCarryCatalogExcludes(t *testing.T) {
	cfg, err := rules.LoadPathExcludes()
	if err != nil {
		testutil.FailErr(t, "load bundled scan-excludes", err)
	}
	patterns := cfg.Patterns()
	args, err := (opengrep.Invocation{Analysis: opengrep.Analysis{Mode: opengrep.Intraprocedural}, Output: "/tmp/out.json", Rules: []string{"/rules/a.yaml"}, Targets: []string{"/repo"}, Jobs: 2, Excludes: patterns}).Args()
	if err != nil {
		testutil.FailErr(t, "construct invocation", err)
	}

	got := map[string]bool{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--exclude" {
			got[args[i+1]] = true
		}
	}
	if len(got) != len(patterns) {
		t.Fatalf("--exclude count = %d, want %d: %v", len(got), len(patterns), args)
	}
	for _, want := range []string{".git", settingsoverlay.DirName(), "node_modules", "vendor"} {
		if !got[want] {
			t.Fatalf("argv missing --exclude %s: %v", want, args)
		}
	}
	if args[len(args)-1] != "/repo" {
		t.Fatalf("target must stay last: %v", args)
	}
}
