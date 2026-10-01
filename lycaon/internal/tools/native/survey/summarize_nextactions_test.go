package survey

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestNextActionsDropUnresolvable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkg/real.go", "package main\n")
	kept := validateNextActions(context.Background(), nativefixture.Boundary(t), nativefixture.Context(dir), []summarize.NextAction{
		{Tool: "read", Path: "pkg/real.go", Why: "ok"},
		{Tool: "read", Path: "ghost/missing.go", Why: "bad"},
		{Tool: "grep", Pattern: "func", Why: "ok"},
		{Tool: "grep", Pattern: "[", Why: "bad"},
	})
	if len(kept) != 2 {
		t.Fatalf("next_actions = %+v, want read+grep kept", kept)
	}
	var sawRead, sawGrep bool
	for _, a := range kept {
		if a.Tool == "read" && a.Path == "pkg/real.go" {
			sawRead = true
		}
		if a.Tool == "grep" && a.Pattern == "func" {
			sawGrep = true
		}
	}
	if !sawRead || !sawGrep {
		t.Fatalf("unexpected kept actions: %+v", kept)
	}
}

func TestValidateNextActionsWhyOnly(t *testing.T) {
	dir := t.TempDir()
	kept := validateNextActions(context.Background(), nativefixture.Boundary(t), nativefixture.Context(dir), []summarize.NextAction{
		{Why: "follow up manually"},
	})
	if len(kept) != 0 {
		t.Fatalf("non-executable action retained: %+v", kept)
	}
}
