package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRuleApprovalGateTierReadAutoApproves(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	res, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "read",
		},
	})
	testutil.FailErr(t, "approvalGate.Evaluate failed", err)
	if !res.AutoApproved() || res.Required() {
		t.Fatalf("read should auto-approve with no rules: %+v", res)
	}
}

func TestRuleApprovalGateTierGitCommitBalancedAutoApproves(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovals(t, []settings.ApprovalRule{{
		Category: settings.ApprovalCategoryTool, Pattern: "command", Effect: settings.ApprovalEffectAsk,
	}})
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	res, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "git_commit",
		},
		Scope: hitl.ActionScope{
			ProjectDir: filepath.Join(tmp, "project"),
		},
	})
	testutil.FailErr(t, "approvalGate.Evaluate failed", err)
	if !res.AutoApproved() {
		t.Fatalf("git_commit should auto-approve at Balanced: %+v", res)
	}
}

// Strict posture requires consent for MCP calls.
func TestRuleApprovalGateMCPByPosture(t *testing.T) {
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "mcp_docs_search_docs",
		},
		Resources: hitl.ActionResources{
			ApprovalCategory: "mcp",
			ApprovalSubject:  "docs.search_docs",
		},
	}

	stageBundledApprovalsPosture(t, gate.PostureBalanced)
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt balanced failed", err)
	res, err := settings.NewRuleApprovalGate(store, settings.NoSources()).Evaluate(context.Background(), action)
	testutil.FailErr(t, "gate.Evaluate balanced failed", err)
	if !res.AutoApproved() || res.Required() {
		t.Fatalf("MCP tool should run silently at Balanced: %+v", res)
	}

	stageBundledApprovalsPosture(t, gate.PostureStrict)
	store, err = settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt strict failed", err)
	res, err = settings.NewRuleApprovalGate(store, settings.NoSources()).Evaluate(context.Background(), action)
	testutil.FailErr(t, "gate.Evaluate strict failed", err)
	if !res.Required() {
		t.Fatalf("MCP tool should ask at Strict: %+v", res)
	}
}

// An exact action-set lease covers only canonical actions in the originating task.
func TestRuleApprovalGateSessionGrantNeverClearsSubstrate(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovals(t, []settings.ApprovalRule{{
		Category: settings.ApprovalCategoryTool, Pattern: "command", Effect: settings.ApprovalEffectAsk,
	}})
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	proj := filepath.Join(tmp, "project")

	// This leased command has no confinement boundary.
	unconfined := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "true"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
			SessionID:  "chat-1",
		},
	}
	offer := hitl.ExactActionSetOffer(unconfined, []string{hitl.GrantKey(unconfined)})
	_, err = approvalGate.ApplyGrant(offer.Grant)
	testutil.FailErr(t, "approvalGate.ApplyGrant", err)
	res, err := approvalGate.Evaluate(context.Background(), unconfined)
	testutil.FailErr(t, "gate.Evaluate unconfined repeat", err)
	if res.Gate() != gateGateUnobservedChannel {
		t.Fatalf("an unconfined process must ask on every repeat despite a grant: %+v", res)
	}
}

func TestRuleApprovalGateSessionGrantCoversWorker(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovals(t, []settings.ApprovalRule{{
		Category: settings.ApprovalCategoryTool, Pattern: "command", Effect: settings.ApprovalEffectAsk,
	}})
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	proj := filepath.Join(tmp, "project")
	cmd := map[string]any{"command": "git push origin main"}

	contained := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj}}
	coordinator := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: cmd,
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
			SessionID:  "chat-1",
		},
		Execution: hitl.ActionExecution{
			Contained: contained,
		},
	}
	offer := hitl.ExactActionSetOffer(coordinator, []string{hitl.GrantKey(coordinator)})
	_, err = approvalGate.ApplyGrant(offer.Grant)
	testutil.FailErr(t, "approvalGate.ApplyGrant", err)

	worker := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: cmd,
		},
		Scope: hitl.ActionScope{
			ProjectDir:    proj,
			SessionID:     "worker-9",
			RootSessionID: "chat-1",
		},
		Execution: hitl.ActionExecution{
			Contained: contained,
		},
	}
	res, err := approvalGate.Evaluate(context.Background(), worker)
	testutil.FailErr(t, "gate.Evaluate worker repeat", err)
	if !res.AutoApproved() || res.Required() {
		t.Fatalf("worker result = %+v, want auto approved", res)
	}

	otherChatWorker := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: cmd,
		},
		Scope: hitl.ActionScope{
			ProjectDir:    proj,
			SessionID:     "worker-9",
			RootSessionID: "chat-other",
		},
		Execution: hitl.ActionExecution{
			Contained: contained,
		},
	}
	resOther, err := approvalGate.Evaluate(context.Background(), otherChatWorker)
	testutil.FailErr(t, "gate.Evaluate other chat worker", err)
	if !resOther.Required() || resOther.AutoApproved() {
		t.Fatalf("other chat worker result = %+v, want required", resOther)
	}
}

// Light relaxes discretionary prompts but keeps the always-on substrate: contained command
// runs silently, while structured path escapes and uncontained execution still ask.
func TestRuleApprovalGateOffSubstrateOnly(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovalsPosture(t, gate.PostureLight)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	project := filepath.Join(tmp, "project")

	push, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "git push origin main"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: project,
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{project}},
		},
	})
	testutil.FailErr(t, "gate.Evaluate push failed", err)
	if !push.AutoApproved() {
		t.Fatalf("git push should auto-approve at Light: %+v", push)
	}

	sudo, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "sudo rm /etc/hosts"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: project,
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{project}},
		},
	})
	testutil.FailErr(t, "gate.Evaluate sudo failed", err)
	if !sudo.AutoApproved() {
		t.Fatalf("sudo should auto-approve at Light (Seatbelt contains it): %+v", sudo)
	}

	// Light still asks on an unconfined process: the channel gate is not a risk
	// preference, it is the report that no boundary applied.
	unconfined, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "true"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: project,
		},
	})
	testutil.FailErr(t, "gate.Evaluate unconfined", err)
	if unconfined.Gate() != gateGateUnobservedChannel {
		t.Fatalf("an unconfined process must ask even at Light: %+v", unconfined)
	}
}

func TestLightHonorsExplicitAskAndDenyRules(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovalsPosture(t, gate.PostureLight)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	err = store.PutGlobal(settings.ApprovalConfig{
		Posture: gate.PostureLight,
		Rules: []settings.ApprovalRule{
			{Category: settings.ApprovalCategoryTool, Pattern: "write", Effect: settings.ApprovalEffectAsk},
			{Category: settings.ApprovalCategoryTool, Pattern: "edit", Effect: settings.ApprovalEffectDeny},
		},
	})
	testutil.FailErr(t, "store.PutGlobal failed", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	project := filepath.Join(tmp, "project")

	asked, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "write",
			Files: []string{filepath.Join(project, "asked.txt")},
		},
		Scope: hitl.ActionScope{
			ProjectDir: project,
		},
	})
	testutil.FailErr(t, "gate.Evaluate ask", err)
	if !asked.Required() || asked.Denied {
		t.Fatalf("Light explicit ask result = %+v", asked)
	}

	denied, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "edit",
			Files: []string{filepath.Join(project, "denied.txt")},
		},
		Scope: hitl.ActionScope{
			ProjectDir: project,
		},
	})
	testutil.FailErr(t, "gate.Evaluate deny", err)
	if !denied.Denied {
		t.Fatalf("Light explicit deny result = %+v", denied)
	}
}

// Ownership changes are recoverable within confined write paths.
func TestRuleApprovalGateTierChownRecoverableInProjectEscapeAsks(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	proj := filepath.Join(tmp, "project")

	inProject, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "chown",
			Files: []string{filepath.Join(proj, "scripts", "run.sh")},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
		},
	})
	testutil.FailErr(t, "gate.Evaluate in-project chown", err)
	if !inProject.AutoApproved() || inProject.Required() {
		t.Fatalf("in-project chown should auto-approve (recoverable): %+v", inProject)
	}

	// Outside the attached folders, outside_roots asks at Balanced so a grant can
	// unlock the native resolve path (same as read/write escapes).
	escaping, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "chown",
			Files: []string{"/etc/passwd"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
		},
	})
	testutil.FailErr(t, "gate.Evaluate escaping chown", err)
	if !escaping.Required() || escaping.Gate() != api.GateOutsideRootsWrite {
		t.Fatalf("escaping chown must ask outside_roots_write at Balanced: %+v", escaping)
	}
}

func TestRuleApprovalGateExplicitLeaseShortCircuitsTier(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovals(t, []settings.ApprovalRule{{
		Category: settings.ApprovalCategoryTool, Pattern: "chown", Effect: settings.ApprovalEffectAsk,
	}})
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "chown",
		},
		Scope: hitl.ActionScope{
			SessionID: "chat-1",
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{tmp}},
		},
	}
	first, err := approvalGate.Evaluate(context.Background(), action)
	testutil.FailErr(t, "gate.Evaluate first", err)
	offers := approvalGate.GrantOffers(action, first)
	if len(offers) == 0 {
		t.Fatal("host did not offer a bounded task lease")
	}
	_, err = approvalGate.ApplyGrant(offers[0].Grant)
	testutil.FailErr(t, "approvalGate.ApplyGrant", err)
	res, err := approvalGate.Evaluate(context.Background(), action)
	testutil.FailErr(t, "approvalGate.Evaluate failed", err)
	if !res.AutoApproved() {
		t.Fatalf("explicit lease should satisfy tier ask: %+v", res)
	}
}
