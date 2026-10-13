package loopguard

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReadPrerequisiteRecoveryRequiresMatchingFrozenDocument(t *testing.T) {
	for _, tc := range []struct {
		name, code, document string
		reads                map[string]int64
		allowed              bool
	}{
		{"matching read", "EDITOR_DOCUMENT_READ_REQUIRED", "doc", map[string]int64{"doc": 3}, true},
		{"unrelated read", "EDITOR_DOCUMENT_READ_REQUIRED", "doc", map[string]int64{"other": 3}, false},
		{"no frozen read", "EDITOR_DOCUMENT_READ_REQUIRED", "doc", nil, false},
		{"missing identity", "EDITOR_DOCUMENT_READ_REQUIRED", "", map[string]int64{"doc": 3}, false},
		{"invalid revision", "EDITOR_DOCUMENT_READ_REQUIRED", "doc", map[string]int64{"doc": 0}, false},
		{"unrelated refusal", "WRITE_SCOPE_DENIED", "doc", map[string]int64{"doc": 3}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewMemoryDoomLoopGuard()
			args := map[string]any{"path": "a.txt"}
			for response := range DoomLoopMaxSameCodeRejects {
				failure := &api.InvocationFailure{Code: tc.code, Details: map[string]any{"document_id": tc.document}}
				testutil.FailErr(t, "record prerequisite", g.RecordAttempt(t.Context(), "session", fmt.Sprint(response), "edit", args, failure, false))
			}
			allowed, count, _, err := g.Check(t.Context(), "session", "next", "edit", args, true, tools.NewAgentReadBases(tc.reads))
			testutil.FailErr(t, "check recovered invocation", err)
			if allowed != tc.allowed {
				t.Fatalf("allowed = %v, want %v", allowed, tc.allowed)
			}
			if allowed && count != 0 {
				t.Fatalf("recovered count = %d", count)
			}
			if got := g.CodeRejectResponses("session", "edit", tc.code); got != DoomLoopMaxSameCodeRejects {
				t.Fatalf("lost rejection history: %d", got)
			}
		})
	}
}

func TestCurrentResponseReadCannotResolveFrozenPrerequisite(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	args := map[string]any{"path": "a.txt"}
	completedReads := map[string]int64{}
	frozen := tools.NewAgentReadBases(completedReads)
	for response := range DoomLoopMaxSameCodeRejects {
		testutil.FailErr(t, "record prerequisite", g.RecordAttempt(t.Context(), "session", fmt.Sprint(response), "edit", args, &api.InvocationFailure{Code: "EDITOR_DOCUMENT_READ_REQUIRED", Details: map[string]any{"document_id": "doc"}}, false))
	}
	completedReads["doc"] = 3
	allowed, _, _, err := g.Check(t.Context(), "session", "current", "edit", args, true, frozen)
	testutil.FailErr(t, "check current response", err)
	if allowed {
		t.Fatal("current-response read escaped the frozen edit basis")
	}
	allowed, _, _, err = g.Check(t.Context(), "session", "next", "edit", args, true, tools.NewAgentReadBases(completedReads))
	testutil.FailErr(t, "check next response", err)
	if !allowed {
		t.Fatal("next-response read did not resolve its prerequisite")
	}
}
