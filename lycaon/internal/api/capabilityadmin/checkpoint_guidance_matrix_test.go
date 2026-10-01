package capabilityadmin

import (
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// Guidance rides only the denying branch of each kind. The cases enumerate
// every kind and action, so one that lets guidance ride an approval fails here.
func TestGuidanceValidOnlyOnDenyingResolutions(t *testing.T) {
	const direction = "use bun install instead"
	cases := []struct {
		name    string
		req     wire.ResolveCheckpointRequest
		denying bool
	}{
		{"tool approve", wire.ResolveCheckpointRequest{Kind: wire.CheckpointKindToolApproval, Action: wire.ApprovalActionApprove}, false},
		{"tool reject", wire.ResolveCheckpointRequest{Kind: wire.CheckpointKindToolApproval, Action: wire.ApprovalActionReject}, true},
		{"content approve", wire.ResolveCheckpointRequest{Kind: wire.CheckpointKindContentApply, Decision: string(wire.ContentApplyApprove)}, false},
		{"content approve_partial", wire.ResolveCheckpointRequest{Kind: wire.CheckpointKindContentApply, Decision: string(wire.ContentApplyApprovePartial), ApprovedHunks: []string{"host-hunk"}}, false},
		{"content reject", wire.ResolveCheckpointRequest{Kind: wire.CheckpointKindContentApply, Decision: string(wire.ContentApplyReject)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name+" without guidance resolves", func(t *testing.T) {
			if _, _, err := resolveFromRequest(tc.req); err != nil {
				t.Fatalf("plain resolution errored: %+v", err)
			}
		})
		t.Run(tc.name+" with guidance", func(t *testing.T) {
			req := tc.req
			req.Guidance = direction
			toolResult, contentResult, err := resolveFromRequest(req)
			if !tc.denying {
				if err == nil || err.field != "guidance" {
					t.Fatalf("guidance rode a non-denying resolution: err=%+v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("denying resolution errored: %+v", err)
			}
			switch {
			case contentResult != nil:
				if contentResult.Guidance != direction {
					t.Fatalf("content guidance = %q", contentResult.Guidance)
				}
			case toolResult != nil:
				if toolResult.Comments != direction {
					t.Fatalf("decision guidance = %q", toolResult.Comments)
				}
				if toolResult.Approved {
					t.Fatal("denying resolution produced an approval")
				}
			default:
				t.Fatal("no result produced")
			}
		})
	}
}

func TestContentApplyResolveAcceptsOnlyHostHunkSelection(t *testing.T) {
	cases := []wire.ResolveCheckpointRequest{
		{Kind: wire.CheckpointKindContentApply, Decision: "approve_edited"},
		{Kind: wire.CheckpointKindContentApply, Decision: string(wire.ContentApplyApprovePartial)},
		{Kind: wire.CheckpointKindContentApply, Decision: string(wire.ContentApplyApprove), ApprovedHunks: []string{"host-hunk"}},
		{Kind: wire.CheckpointKindContentApply, Decision: string(wire.ContentApplyReject), ApprovedHunks: []string{"host-hunk"}},
	}
	for _, req := range cases {
		if _, _, err := resolveFromRequest(req); err == nil {
			t.Fatalf("malformed content_apply resolution was accepted: %+v", req)
		}
	}
}
