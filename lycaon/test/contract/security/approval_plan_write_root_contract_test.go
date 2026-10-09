package contract_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
)

// newApprovalPlanStore also returns a context bound to the store's host owner,
// the person whose answer settles a card.
func newApprovalPlanStore(t *testing.T, sessionID string) (*hitl.SQLStore, authzcontext.LedgerRecorder, context.Context) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSessionWithRoot(t, sqlDB, sessionID, testdbseed.DefaultProjectID, t.TempDir())
	return hitl.NewSQLStore(sqlDB), authzcontext.SQLRecorder(sqlDB), testdbseed.OwnerCaller(t, t.Context(), sqlDB)
}

type approvalPlanEvents struct{ events chan api.CheckpointEvent }

func (e approvalPlanEvents) PublishCheckpoint(_ context.Context, _, _ string, event api.CheckpointEvent) {
	if event.Status == api.CheckpointStatusPending {
		e.events <- event
	}
}

func (approvalPlanEvents) PublishAttention(context.Context) {}

type writeRootPlanInstaller struct {
	runtime *approvalstate.SandboxPathGrantRuntime
}

func (i writeRootPlanInstaller) InstallApprovalOption(_ context.Context, checkpointID string, option hitl.ApprovalOption) (func(), error) {
	var installed []string
	for _, delta := range option.Authority {
		if delta.Kind != hitl.AuthorityWriteRootChat {
			continue
		}
		for _, root := range delta.WriteRoots {
			if i.runtime.GrantChat(delta.ChatSession(), root, delta.Grant.ID, checkpointID, nil) {
				installed = append(installed, delta.Grant.ID)
			}
		}
	}
	return func() {
		for _, id := range installed {
			_, _ = i.runtime.RevokeByIDInstalledBy(id, checkpointID)
		}
	}, nil
}

// TestWriteRootCardAlwaysHasAnOption requires a task overlay when authority minting returns nothing.
func TestWriteRootCardAlwaysHasAnOption(t *testing.T) {
	events := make(chan api.CheckpointEvent, 1)
	store, rec, owner := newApprovalPlanStore(t, "session-floor")
	mgr := hitl.NewCheckpoints(store, approvalPlanEvents{events: events}, rec)
	mgr.SetCheckpointExpiry(func() time.Duration { return 0 })
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	mgr.Authority.SetApprovalAuthorityInstaller(writeRootPlanInstaller{runtime: runtime})
	broker := &session.WriteRootCheckpointBroker{
		Checkpoints: mgr, Runtime: runtime,
		Posture: func(string) gate.Posture { return gate.PostureStrict },
	}
	project, outside := t.TempDir(), "/Users/approval-plan-contract/floor-cache"
	resultCh := make(chan native.SandboxWriteRootResult, 1)
	go func() {
		result, _ := broker.Authorize(context.Background(), native.SandboxWriteRootAsk{
			SessionID: "session-floor", ProjectDir: project, ToolCallID: "floor-call", Command: "build",
			ProposedWriteRoot: outside,
		})
		resultCh <- result
	}()
	event := <-events
	if event.ToolApproval == nil || len(event.ToolApproval.Plan.Options) == 0 {
		t.Fatalf("floor card missing options: %+v", event.ToolApproval)
	}
	plan := event.ToolApproval.Plan
	if plan.Options[0].Rung != api.ApprovalOptionRung(hitl.ApprovalRungChat) {
		t.Fatalf("floor rung = %s, want task", plan.Options[0].Rung)
	}
	if plan.Options[0].Title != hitl.TitleAllowForThisChat {
		t.Fatalf("floor title = %q", plan.Options[0].Title)
	}
	if plan.RecommendedOptionID != plan.Options[0].ID {
		t.Fatalf("face = %q, want %q", plan.RecommendedOptionID, plan.Options[0].ID)
	}
	_, err := mgr.Authority.ResolveApprovalOption(owner, "session-floor", event.ID, plan.Options[0].ID)
	testutil.FailErr(t, "resolve floor option", err)
	<-resultCh
}

func TestWriteRootBrokerHomeChildFromUnquotedMkdir(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)
	store, rec, _ := newApprovalPlanStore(t, "session-unquoted-mkdir")
	mgr := hitl.NewCheckpoints(store, approvalPlanEvents{events: make(chan api.CheckpointEvent, 1)}, rec)
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	broker := &session.WriteRootCheckpointBroker{
		Checkpoints: mgr, Runtime: runtime, ApprovalsDisabled: func(string) bool { return true },
	}
	result, err := broker.Authorize(t.Context(), native.SandboxWriteRootAsk{
		SessionID: "session-unquoted-mkdir", ProjectDir: t.TempDir(), ToolCallID: "mkdir-cache",
		Command: "build", ProposedWriteRoot: filepath.Join(home, "cache"),
	})
	testutil.FailErr(t, "await unquoted mkdir write root", err)
	if result.Raised || !result.Authorized {
		t.Fatalf("result = %+v", result)
	}
	want := filepath.Join(home, "cache")
	if result.ProposedWriteRoot != want {
		t.Fatalf("proposed = %q want %q", result.ProposedWriteRoot, want)
	}
}

func TestWriteRootBrokerHomeChildFromBSDMkdir(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)
	store, rec, _ := newApprovalPlanStore(t, "session-bsd-mkdir")
	mgr := hitl.NewCheckpoints(store, approvalPlanEvents{events: make(chan api.CheckpointEvent, 1)}, rec)
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	broker := &session.WriteRootCheckpointBroker{
		Checkpoints: mgr, Runtime: runtime, ApprovalsDisabled: func(string) bool { return true },
	}
	blocked := filepath.Join(home, ".tool")
	result, err := broker.Authorize(t.Context(), native.SandboxWriteRootAsk{
		SessionID: "session-bsd-mkdir", ProjectDir: t.TempDir(), ToolCallID: "mkdir-tool",
		Command: "mkdir -p " + blocked, ProposedWriteRoot: blocked,
	})
	testutil.FailErr(t, "await bsd mkdir write root", err)
	if result.Raised || !result.Authorized {
		t.Fatalf("result = %+v", result)
	}
	if result.ProposedWriteRoot != blocked {
		t.Fatalf("proposed = %q want %q", result.ProposedWriteRoot, blocked)
	}
}

func TestWriteRootBrokerRaisesBalancedCardOutsideRoots(t *testing.T) {
	events := make(chan api.CheckpointEvent, 1)
	store, rec, owner := newApprovalPlanStore(t, "session-balanced-outside-roots")
	mgr := hitl.NewCheckpoints(store, approvalPlanEvents{events: events}, rec)
	mgr.SetCheckpointExpiry(func() time.Duration { return 0 })
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	mgr.Authority.SetApprovalAuthorityInstaller(writeRootPlanInstaller{runtime: runtime})
	broker := &session.WriteRootCheckpointBroker{
		Checkpoints: mgr, Runtime: runtime,
		Posture: func(string) gate.Posture { return gate.PostureBalanced },
	}
	blocked := "/Users/approval-plan-contract/outside-root"
	resultCh := make(chan native.SandboxWriteRootResult, 1)
	go func() {
		result, _ := broker.Authorize(context.Background(), native.SandboxWriteRootAsk{
			SessionID: "session-balanced-outside-roots", ProjectDir: t.TempDir(),
			ToolCallID: "mkdir-tool", Command: "mkdir -p " + blocked, ProposedWriteRoot: blocked,
		})
		resultCh <- result
	}()
	var event api.CheckpointEvent
	select {
	case event = <-events:
	case result := <-resultCh:
		t.Fatalf("outside-root write returned without a card: %+v", result)
	}
	if event.ToolApproval == nil || event.ToolApproval.Plan.Subject.Kind != api.ApprovalSubjectKind(hitl.ApprovalSubjectWriteRootSet) {
		t.Fatalf("outside-root write must raise a card: %+v", event.ToolApproval)
	}
	if event.ToolApproval.Plan.Subject.Targets[0].Label != blocked {
		t.Fatalf("card target = %q want %q", event.ToolApproval.Plan.Subject.Targets[0].Label, blocked)
	}
	_, err := mgr.Authority.ResolveApprovalOption(owner, "session-balanced-outside-roots", event.ID, event.ToolApproval.Plan.Options[0].ID)
	testutil.FailErr(t, "resolve outside-root option", err)
	result := <-resultCh
	if !result.Raised || !result.Authorized || result.ProposedWriteRoot != blocked {
		t.Fatalf("result = %+v", result)
	}
}

func TestAdvancedOffGrantsWriteRootWithoutCard(t *testing.T) {
	t.Parallel()
	store, rec, _ := newApprovalPlanStore(t, "session")
	mgr := hitl.NewCheckpoints(store, approvalPlanEvents{events: make(chan api.CheckpointEvent, 1)}, rec)
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	broker := &session.WriteRootCheckpointBroker{
		Checkpoints: mgr, Runtime: runtime, ApprovalsDisabled: func(string) bool { return true },
	}
	project, outside := t.TempDir(), "/Users/approval-plan-contract/cache"
	result, err := broker.Authorize(t.Context(), native.SandboxWriteRootAsk{
		SessionID: "session", ProjectDir: project, ToolCallID: "call", Command: "build",
		ProposedWriteRoot: outside,
	})
	testutil.FailErr(t, "await write root with approvals disabled", err)
	if result.Raised || !result.Authorized {
		t.Fatalf("result = %+v", result)
	}
	pending, err := mgr.ListPending(t.Context(), "session", nil)
	testutil.FailErr(t, "list pending", err)
	if len(pending) != 0 {
		t.Fatalf("pending = %d", len(pending))
	}
}

func TestWriteRootUsesToolApprovalPlanAndInstallsBeforeRelease(t *testing.T) {
	events := make(chan api.CheckpointEvent, 1)
	store, rec, owner := newApprovalPlanStore(t, "session")
	mgr := hitl.NewCheckpoints(store, approvalPlanEvents{events: events}, rec)
	mgr.SetCheckpointExpiry(func() time.Duration { return 0 })
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	mgr.Authority.SetApprovalAuthorityInstaller(writeRootPlanInstaller{runtime: runtime})
	broker := &session.WriteRootCheckpointBroker{
		Checkpoints: mgr, Runtime: runtime, Posture: func(string) gate.Posture { return gate.PostureStrict },
	}
	project, outside := t.TempDir(), "/Users/approval-plan-contract/cache"

	resultCh := make(chan native.SandboxWriteRootResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := broker.Authorize(context.Background(), native.SandboxWriteRootAsk{
			SessionID: "session", ProjectDir: project, ToolCallID: "call",
			ToolName: "verify", Command: "build",
			ProposedWriteRoot: outside,
		})
		resultCh <- result
		errCh <- err
	}()

	event := <-events
	if event.Kind != api.CheckpointKindToolApproval || event.ToolApproval == nil {
		t.Fatalf("event = %+v", event)
	}
	plan := event.ToolApproval.Plan
	if plan.Presentation.Command != "build" {
		t.Fatalf("presentation.command = %q want build", plan.Presentation.Command)
	}
	if plan.Presentation.Tool != "verify" {
		t.Fatalf("presentation.tool = %q want verify", plan.Presentation.Tool)
	}
	if _, ok := plan.Subject.Targets[0].Details["command"]; ok {
		t.Fatalf("write-root target details must not duplicate presentation.command: %+v", plan.Subject.Targets[0].Details)
	}
	// Write-root has one affirmative rung, the chat lease, so no quiet rung is minted.
	var affirmative []api.ApprovalOption
	var quiet int
	for _, opt := range plan.Options {
		if opt.Kind == api.ApprovalOptionKind(hitl.ApprovalOptionQuiet) {
			quiet++
			continue
		}
		affirmative = append(affirmative, opt)
	}
	if plan.Subject.Kind != api.ApprovalSubjectKind(hitl.ApprovalSubjectWriteRootSet) || len(affirmative) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if quiet != 0 {
		t.Fatalf("write-root card with task lease unexpectedly carried quiet rung: %+v", plan.Options)
	}
	_, err := mgr.Authority.ResolveApprovalOption(owner, "session", event.ID, affirmative[0].ID)
	testutil.FailErr(t, "resolve write-root option", err)
	approvedRoot := plan.Subject.Targets[0].Label
	if roots := runtime.SessionWriteRoots("session"); len(roots) != 1 || roots[0] != approvedRoot {
		t.Fatalf("write-root authority was not installed before release")
	}
	result := <-resultCh
	testutil.FailErr(t, "await write-root result", <-errCh)
	if !result.Raised || !result.Authorized || result.Denied {
		t.Fatalf("result = %+v", result)
	}
}

func TestWriteRootRulesEnforceDenyAndRouteAskThroughGate(t *testing.T) {
	store, rec, owner := newApprovalPlanStore(t, "session")
	events := make(chan api.CheckpointEvent, 1)
	mgr := hitl.NewCheckpoints(store, approvalPlanEvents{events: events}, rec)
	mgr.SetCheckpointExpiry(func() time.Duration { return 0 })
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	mgr.Authority.SetApprovalAuthorityInstaller(writeRootPlanInstaller{runtime: runtime})
	project, outside := t.TempDir(), "/Users/approval-plan-contract/rule-cache"

	denyBroker := &session.WriteRootCheckpointBroker{
		Checkpoints: mgr, Runtime: runtime,
		Rule: func(context.Context, string, string, string) (settings.ApprovalRule, bool) {
			return settings.ApprovalRule{
				Category: settings.ApprovalCategoryWriteRoot,
				Effect:   settings.ApprovalEffectDeny,
			}, true
		},
	}
	denied, err := denyBroker.Authorize(t.Context(), native.SandboxWriteRootAsk{
		SessionID: "session", ProjectDir: project, ToolCallID: "deny-call", Command: "build",
		ProposedWriteRoot: outside,
	})
	testutil.FailErr(t, "deny write-root rule", err)
	if !denied.Denied || denied.Raised || denied.Authorized {
		t.Fatalf("deny result = %+v", denied)
	}

	askBroker := &session.WriteRootCheckpointBroker{
		Checkpoints: mgr, Runtime: runtime,
		Rule: func(context.Context, string, string, string) (settings.ApprovalRule, bool) {
			return settings.ApprovalRule{
				Category: settings.ApprovalCategoryWriteRoot,
				Pattern:  outside,
				Effect:   settings.ApprovalEffectAsk,
			}, true
		},
		Posture: func(string) gate.Posture { return gate.PostureLight },
	}
	resultCh := make(chan native.SandboxWriteRootResult, 1)
	go func() {
		result, _ := askBroker.Authorize(context.Background(), native.SandboxWriteRootAsk{
			SessionID: "session", ProjectDir: project, ToolCallID: "ask-call", Command: "build",
			ProposedWriteRoot: outside,
		})
		resultCh <- result
	}()
	event := <-events
	if event.ToolApproval == nil || event.ToolApproval.Plan.Presentation.Gate != api.GateUserRule {
		t.Fatalf("ask-rule event = %+v", event)
	}
	_, err = mgr.Authority.ResolveApprovalOption(owner, "session", event.ID, event.ToolApproval.Plan.Options[0].ID)
	testutil.FailErr(t, "resolve ask-rule checkpoint", err)
	if result := <-resultCh; !result.Raised || !result.Authorized {
		t.Fatalf("ask result = %+v", result)
	}
}
