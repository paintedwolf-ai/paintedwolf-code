package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A command's prepared output to a credential file reaches review as a
// protected write, like the same change from the write tool, even when the
// command itself is contained and runs silently.
func TestCommandRedirectReviewAsksForProtectedTargets(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "load approval store", err)
	locations, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "load sensitive locations", err)
	sources := settings.NoSources()
	sources.Locations = locations
	gate := settings.NewRuleApprovalGate(store, sources)
	proj := t.TempDir()
	contained := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj}}

	for _, tc := range []struct{ tool, line, target string }{
		{"command", "echo x > .env", ".env"},
		{"verify", "echo x >> .env.production", ".env.production"},
		{"command", "echo x 2> deploy/id_rsa", "deploy/id_rsa"},
		{"command", "printf x | sort > .npmrc", ".npmrc"},
	} {
		args := map[string]any{"command": tc.line}
		preSpawn, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: tc.tool,
Args: args,
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
Execution: hitl.ActionExecution{
Contained: contained,
},
})
		testutil.FailErr(t, "evaluate "+tc.line, err)
		if preSpawn.Required() {
			t.Fatalf("%s: the contained command itself must stay silent: %+v", tc.line, preSpawn.Decision)
		}

		abs := filepath.Join(proj, filepath.FromSlash(tc.target))
		review, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: tc.tool,
Args: args,
Files: []string{abs},
ResolvedFiles: []string{abs},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
Execution: hitl.ActionExecution{
Contained: contained,
},
Mutations: hitl.ActionMutations{
FileChanges: []api.ApprovalFileChange{{Path: abs, Operation: "write", After: "x\n"}},
},
})
		testutil.FailErr(t, "evaluate review "+tc.line, err)
		if !review.Required() || review.Gate() != api.GateSensitiveLocation {
			t.Fatalf("%s: writing %s must ask as a protected subject like the write tool, got %+v",
				tc.line, tc.target, review.Decision)
		}
		native, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Args: map[string]any{"path": tc.target},
Files: []string{abs},
ResolvedFiles: []string{abs},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
Execution: hitl.ActionExecution{
Contained: contained,
},
Mutations: hitl.ActionMutations{
FileChanges: []api.ApprovalFileChange{{Path: abs, Operation: "write", After: "x\n"}},
},
})
		testutil.FailErr(t, "evaluate native "+tc.target, err)
		if !native.Required() || native.Gate() != review.Gate() {
			t.Fatalf("%s: command review gate %q differs from the write tool's %q", tc.target, review.Gate(), native.Gate())
		}
	}
}
