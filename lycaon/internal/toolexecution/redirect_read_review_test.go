package toolexecution

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// redirectReadGate returns one fixed result and records the action it saw.
type redirectReadGate struct {
	decisionGate
	result *hitl.ApprovalResult
	seen   *hitl.ProposedAction
}

func (g redirectReadGate) Evaluate(_ context.Context, action hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	*g.seen = action
	return g.result, nil
}

func TestRedirectReadReviewInstallsOnlyApprovedAccess(t *testing.T) {
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract missing")
	}
	allow := func(context.Context, string, map[string]any, tools.ToolContext, string) (bool, bool, string, error) {
		return true, false, "", nil
	}
	decline := func(context.Context, string, map[string]any, tools.ToolContext, string) (bool, bool, string, error) {
		return false, true, "", nil
	}
	ask := &gate.Decision{Primary: api.GateOutsideRootsRead}
	cases := []struct {
		name      string
		noGate    bool
		result    *hitl.ApprovalResult
		preflight ReadPathPreflight
		wantCode  string
		wantCard  bool
	}{
		{name: "no approval gate", noGate: true, preflight: allow, wantCode: isolation.CodeApprovalUnavailable},
		{name: "enforcement block", result: &hitl.ApprovalResult{Denied: true, DenyCode: "SANDBOX_READ_DENIED"}, preflight: allow, wantCode: "SANDBOX_READ_DENIED"},
		{name: "declined card", result: &hitl.ApprovalResult{Decision: ask}, preflight: allow, wantCard: true, wantCode: "*"},
		{name: "protected read declined", result: &hitl.ApprovalResult{}, preflight: decline, wantCode: isolation.CodeReadPathDenied},
		{name: "allowed", result: &hitl.ApprovalResult{}, preflight: allow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := shortTempDir(t)
			path := filepath.Join(shortTempDir(t), "input.txt")
			access := []hitl.GrantedPathDelta{{Path: path}}
			if tc.result != nil {
				tc.result.FileAccess = access
			}
			var seen hitl.ProposedAction
			cards := &rejectHITL{}
			e := NewExecutor(nil, nil, "implement")
			e.Approvals.checkpointMgr = cards
			if !tc.noGate {
				e.Approvals.approvalGate = redirectReadGate{result: tc.result, seen: &seen}
			}
			e.Boundary.readPathPreflight = tc.preflight
			e.Boundary.sessionReadOverlay = func(context.Context, string, string) []string { return []string{"/leased"} }
			toolCtx := tools.ToolContext{
				Invocation: tools.Invocation{Contract: contract},
				Identity:   tools.InvocationIdentity{SessionID: "chat", ToolCallID: "call-1"},
				Source:     tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root"},
			}
			got, err := e.Boundary.ReviewRedirectRead(t.Context(), toolCtx, "<", path)
			if (cards.checkpointID != "") != tc.wantCard {
				t.Fatalf("card raised = %q, want card %v", cards.checkpointID, tc.wantCard)
			}
			if tc.wantCode != "" {
				var reject *toolrejection.ToolReject
				if err == nil || (tc.wantCode != "*" && (!errors.As(err, &reject) || reject.Code != tc.wantCode)) {
					t.Fatalf("review error = %v, want %s", err, tc.wantCode)
				}
				if slices.Contains(got.Files.SessionReadPaths, path) || len(got.Files.ApprovedFileAccess) != 0 {
					t.Fatalf("refused review installed access: %+v", got.Files)
				}
				return
			}
			if err != nil {
				t.Fatalf("review = %v", err)
			}
			if seen.Presentation.Command != "< "+path || !slices.Equal(seen.Invocation.Files, []string{path}) {
				t.Fatalf("reviewed action = %+v", seen.Invocation)
			}
			if !slices.Equal(got.Files.ApprovedFileAccess, access) || !slices.Equal(got.Files.SessionReadPaths, []string{"/leased", path}) {
				t.Fatalf("approved access = %+v", got.Files)
			}
		})
	}
}
