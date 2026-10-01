package main

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEvalToolUsageTimeout(t *testing.T) {
	flags, err := parseEvalToolUsageFlags([]string{"--timeout", "5m"})
	testutil.FailErr(t, "parse eval timeout", err)
	if flags.timeout != 5*time.Minute {
		t.Fatalf("timeout = %s", flags.timeout)
	}
	unbounded, err := parseEvalToolUsageFlags([]string{"--timeout", "0s"})
	testutil.FailErr(t, "parse unbounded task", err)
	if unbounded.timeout != 0 {
		t.Fatal("unbounded task acquired a deadline")
	}
	for _, args := range [][]string{{"--timeout"}, {"--timeout", "-1s"}, {"--timeout", "bad"}} {
		if _, err := parseEvalToolUsageFlags(args); err == nil {
			t.Errorf("accepted invalid timeout: %v", args)
		}
	}
}

func TestEvalToolUsageFlags(t *testing.T) {
	flags, err := parseEvalToolUsageFlags([]string{"--from", "capture", "--corpus", "tasks.yaml", "--project", "project", "--runs", "3", "--json", "--out", "profile.json"})
	testutil.FailErr(t, "parse eval options", err)
	if flags.from != "capture" || flags.corpus != "tasks.yaml" || flags.project != "project" || flags.runs != 3 || !flags.json || flags.out != "profile.json" {
		t.Fatalf("unexpected eval options: %+v", flags)
	}
	for _, args := range [][]string{{"--runs", "0"}, {"--runs", "bad"}, {"--corpus"}, {"--unknown"}, {"positional"}} {
		if _, err := parseEvalToolUsageFlags(args); err == nil {
			t.Errorf("accepted invalid arguments: %v", args)
		}
	}
}

func TestEvalSuiteModesAreExplicit(t *testing.T) {
	for _, args := range [][]string{
		{"--suite", "suite.yaml", "--from", "capture"},
		{"--suite", "suite.yaml", "--project", "shared"},
		{"--compare", "before.json"},
		{"--compare", "before.json", "--from", "after.json", "--allow-live"},
		{"--refresh", "report.json", "--allow-live"},
		{"--refresh", "report.json", "--out", "another.json"},
	} {
		if _, err := parseEvalToolUsageFlags(args); err == nil {
			t.Errorf("accepted ambiguous or unsafe mode: %v", args)
		}
	}
	flags, err := parseEvalToolUsageFlags([]string{"--suite", "suite.yaml", "--expect-model", "kimi", "--label", "candidate"})
	testutil.FailErr(t, "parse suite without spending authorization", err)
	if flags.allowLive {
		t.Fatal("suite selection silently authorized paid calls")
	}
	if err := runEvalToolUsage(nil); err == nil {
		t.Fatal("default command started live work without opt-in")
	}
}
