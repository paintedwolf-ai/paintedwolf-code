package tools_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type validatingBranchWorkspace struct {
	err error
}

func (b validatingBranchWorkspace) ValidateMeta(context.Context) error        { return b.err }
func (validatingBranchWorkspace) EnsureParents(context.Context, string) error { return nil }

func TestValidateWorkerBranchChecksMetadata(t *testing.T) {
	want := errors.New("invalid branch metadata")
	err := tools.ValidateWorkerBranch(context.Background(), tools.ToolContext{
		Source: tools.InvocationSource{WorkerBranchRoot: t.TempDir(),
			BranchWorkspace: validatingBranchWorkspace{err: want}},
	})
	if !errors.Is(err, want) {
		t.Fatalf("ValidateWorkerBranch error = %v, want %v", err, want)
	}
}

func TestValidateWorkerBranchAllowsCanonicalWorkspace(t *testing.T) {
	testutil.FailErr(t, "ValidateWorkerBranch", tools.ValidateWorkerBranch(context.Background(), tools.ToolContext{}))
}

func TestAgentLaunchAuthorityRechecksBranchAndLiveAction(t *testing.T) {
	ctx := context.Background()
	identity := confine.EgressCommand{SessionID: "session", ToolCallID: "call", ToolName: "terminal_open", CommandLine: "vim notes.txt"}
	branchErr := errors.New("invalid branch metadata")
	branch := tools.ToolContext{Source: tools.InvocationSource{WorkerBranchRoot: t.TempDir(),
		BranchWorkspace: validatingBranchWorkspace{err: branchErr}}}
	bind := func(t *testing.T) *confine.ActionLease {
		lease, err := confine.BindAction(nil, identity)
		testutil.FailErr(t, "BindAction", err)
		return lease
	}
	changed := identity
	changed.CommandLine = "vim other.txt"
	for _, tc := range []struct {
		name     string
		tctx     tools.ToolContext
		lease    func(*testing.T) *confine.ActionLease
		identity confine.EgressCommand
		want     error
		refused  bool
	}{
		{name: "live action", lease: bind, identity: identity},
		{name: "missing action", lease: func(*testing.T) *confine.ActionLease { return nil }, identity: identity, refused: true},
		{name: "closed action", lease: func(t *testing.T) *confine.ActionLease {
			lease := bind(t)
			lease.Close(ctx)
			return lease
		}, identity: identity, refused: true},
		{name: "changed identity", lease: bind, identity: changed, refused: true},
		{name: "invalid branch", tctx: branch, lease: bind, identity: identity, want: branchErr, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tools.AgentLaunchAuthority(tc.tctx, nil, tc.lease(t), tc.identity)(ctx)
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("authority error = %v, want %v", err, tc.want)
			}
			if (err != nil) != tc.refused {
				t.Fatalf("authority error = %v, refused want %v", err, tc.refused)
			}
		})
	}
}
