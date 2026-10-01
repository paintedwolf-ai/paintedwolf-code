package main

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSupervisedEvaluationStillRequiresPaidOptIn(t *testing.T) {
	for _, args := range [][]string{
		{"--wait-for-input"},
		{"--suite", "fixture.yaml", "--wait-for-input"},
		{"--allow-live", "--wait-for-input"},
	} {
		if _, err := parseEvalToolUsageFlags(args); err == nil {
			t.Fatalf("accepted supervised evaluation without paid suite: %v", args)
		}
	}
	flags, err := parseEvalToolUsageFlags([]string{"--suite", "fixture.yaml", "--allow-live", "--wait-for-input"})
	testutil.FailErr(t, "parse explicitly paid supervised suite", err)
	if !flags.waitForInput || !flags.allowLive {
		t.Fatal("lost explicit evaluation options")
	}
}
