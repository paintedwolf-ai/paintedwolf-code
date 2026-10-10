package toolexecution_test

import (
	"github.com/lycaon/lycaon/internal/toolapproval"

	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type countingApprovalGate struct {
	calls atomic.Int32
}

func (g *countingApprovalGate) Evaluate(context.Context, hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	g.calls.Add(1)
	return &hitl.ApprovalResult{
		Decision: &gate.Decision{
			Primary: api.GateAuthorityMisuse,
			Cited:   []gate.Fact{{Key: "detection.rule", Value: "test detection", Source: "detection_pack"}},
		},
		DetectionCitation: &hitl.DetectionMatch{
			PackID: "test-pack", RuleID: "test-rule", RuleTitle: "test detection", Level: "critical",
			External: true, Unrecoverable: true, Tagged: true,
		},
	}, nil
}

func (*countingApprovalGate) GrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}

func (*countingApprovalGate) AbsorbedGrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}
func (*countingApprovalGate) ApplyGrant(hitl.ApprovalGrant) (bool, error)         { return false, nil }
func (*countingApprovalGate) GrantCovers(hitl.ProposedAction) bool                { return false }
func (*countingApprovalGate) HostResourceLeaseCovers(hitl.ProposedAction) bool    { return false }
func (*countingApprovalGate) RevokeGrant(string) (bool, error)                    { return false, nil }
func (*countingApprovalGate) RevokeGrantInstalledBy(string, string) (bool, error) { return false, nil }
func (*countingApprovalGate) ListGrants(string) []hitl.ApprovalGrant              { return nil }
func (*countingApprovalGate) SecretFingerprintsCovered(string, string, string, string, []string) bool {
	return false
}

func (*countingApprovalGate) SecretRedactionStanding(string, []string) bool { return false }
func (*countingApprovalGate) PutAskQuiet(hitl.AskQuiet, int) (hitl.AskQuiet, bool) {
	return hitl.AskQuiet{}, false
}
func (*countingApprovalGate) AskQuietLive(string, string) (hitl.AskQuiet, bool) {
	return hitl.AskQuiet{}, false
}
func (*countingApprovalGate) NoteAskQuietSuppressed(string, string)         {}
func (*countingApprovalGate) ListAskQuiets(string) []hitl.AskQuiet          { return nil }
func (*countingApprovalGate) RevokeAskQuiet(string) bool                    { return false }
func (*countingApprovalGate) RevokeAskQuietInstalledBy(string, string) bool { return false }
func (*countingApprovalGate) ForgetSession(string)                          {}

func TestApprovalPolicyDoesNotTurnDeniedToolIntoApprovalAsk(t *testing.T) {
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{
		ID:    "implement",
		Tools: map[string]bool{"read": true},
	}})
	gate := &countingApprovalGate{}
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)

	decision, err := policy.Evaluate(context.Background(), platform.PolicyContext{
		ProfileID: "implement",
		ToolName:  "write",
	})
	testutil.FailErr(t, "evaluate denied tool", err)
	if decision == nil || !decision.Blocked || decision.RequiresApproval {
		t.Fatalf("denied tool must be blocked before the approval layer: %+v", decision)
	}
	if got := gate.calls.Load(); got != 0 {
		t.Fatalf("approval gate calls = %d, want 0 for profile-denied tool", got)
	}
}

type denyRuleGate struct{ countingApprovalGate }

func (g *denyRuleGate) Evaluate(context.Context, hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	g.calls.Add(1)
	return &hitl.ApprovalResult{
		Denied:   true,
		DenyCode: "APPROVAL_RULE_DENIED",
		MatchedRules: []hitl.ApprovalRuleMatch{{
			Category: "command", Pattern: "wget*", Effect: "deny",
			Command: "wget https://example.com",
			UnitID:  "approvals/rules/deny-wget", PackID: "foxden/watchdog", Scope: "device",
		}},
	}, nil
}

func TestApprovalRuleDenyRendersTheMatchedRule(t *testing.T) {
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{
		ID:    "implement",
		Tools: map[string]bool{"command": true},
	}})
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), &denyRuleGate{})
	decision, err := policy.Evaluate(context.Background(), platform.PolicyContext{
		ProfileID: "implement",
		ToolName:  "command",
		ToolArgs:  map[string]any{"command": "wget https://example.com"},
	})
	testutil.FailErr(t, "evaluate denied command", err)
	if decision == nil || !decision.Blocked {
		t.Fatalf("denied command must be blocked: %+v", decision)
	}
	if decision.RejectCode != "APPROVAL_RULE_DENIED" {
		t.Fatalf("reject code = %q", decision.RejectCode)
	}
	if got, _ := decision.RejectData["rule_unit_id"].(string); got != "approvals/rules/deny-wget" {
		t.Fatalf("rule_unit_id = %v", decision.RejectData["rule_unit_id"])
	}
	if got, _ := decision.RejectData["rule_pack_id"].(string); got != "foxden/watchdog" {
		t.Fatalf("rule_pack_id = %v", decision.RejectData["rule_pack_id"])
	}

	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	rendered := toolrejection.RenderReject(&toolrejection.ToolReject{
		Code: decision.RejectCode, Data: decision.RejectData, FailureClass: api.FailureClassPolicyRejection,
	}, guidance.NewStaticRejectFormatter(hints))
	refusal, ok := guidance.RefusalFromError(rendered)
	if !ok {
		t.Fatalf("rendered denial is not a refusal: %#v", rendered)
	}
	if !strings.Contains(refusal.Body, "approvals/rules/deny-wget") {
		t.Fatalf("rendered body omitted unit id: %q", refusal.Body)
	}
	if !strings.Contains(refusal.Body, "foxden/watchdog") {
		t.Fatalf("rendered body omitted pack id: %q", refusal.Body)
	}
}

// The profile permits command; per-turn surfaces determine its availability.
func TestExecutorListCoordinatorCommandInProfile(t *testing.T) {
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	reg := tools.NewDefaultRegistry()
	for _, name := range []string{"command", "read"} {
		err := reg.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			return "", nil
		})
		testutil.FailErr(t, "register "+name, err)
	}
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), nil)
	exec := toolexecution.NewExecutor(policy, reg, "implement")

	listed, err := exec.Metadata.List(context.Background(), platform.ToolFilter{ProfileID: "coordinator"})
	testutil.FailErr(t, "List coordinator", err)
	hasCommand := false
	for _, m := range listed {
		if m.Name == "command" {
			hasCommand = true
		}
	}
	if !hasCommand {
		t.Fatal("coordinator profile must allow command as its ceiling")
	}
}

func TestExecutorAskRuleSuspendsNotHardError(t *testing.T) {
	tmp := t.TempDir()
	stageApprovals(t, settings.ApprovalEffectAsk)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())

	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))

	reg := tools.NewDefaultRegistry()
	if err := reg.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if err := reviewWriteFixture(ctx, args, tc); err != nil {
			return "", err
		}
		return "written:" + args["path"].(string), nil
	}); err != nil {
		testutil.FailErr(t, "register write", err)
	}

	mgr := &asyncHITL{requested: make(chan struct{}, 1)}
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, gate)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	type result struct {
		out string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		out, err := exec.Invoke(ctx, "write", map[string]any{"path": "a.txt", "content": "x"}, tools.ToolContext{
			Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmp, IsPrimary: true}},
				ActiveRootID: "r1"},
			Identity: tools.InvocationIdentity{SessionID: "sess-1",
				Agent: "implement"},
		})
		ch <- result{out, err}
	}()

	select {
	case <-mgr.requested:
		mgr.approve()
	case <-ctx.Done():
		t.Fatal("timed out waiting for approval request")
	}

	res := <-ch
	if res.err != nil {
		t.Fatalf("invoke err = %v want success after approve", res.err)
	}
	if res.out != "written:a.txt" {
		t.Fatalf("out = %q", res.out)
	}
}

func TestExecutorCarriesSingleApprovalEvaluationToCheckpoint(t *testing.T) {
	tmp := t.TempDir()
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	gate := &countingApprovalGate{}
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "ok", nil
	}); err != nil {
		testutil.FailErr(t, "register write", err)
	}
	mgr := &asyncHITL{requested: make(chan struct{}, 1)}
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, gate)

	done := make(chan error, 1)
	go func() {
		_, err := exec.Invoke(context.Background(), "read", map[string]any{"path": "a.txt"}, tools.ToolContext{
			Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmp, IsPrimary: true}},
				ActiveRootID: "r1"},
			Identity: tools.InvocationIdentity{SessionID: "sess-1",
				Agent: "implement"},
		})
		done <- err
	}()
	<-mgr.requested
	mgr.approve()
	testutil.FailErr(t, "Invoke", <-done)
	if got := gate.calls.Load(); got != 1 {
		t.Fatalf("approval gate calls = %d, want exactly 1", got)
	}
	// The detection citation rides the checkpoint request; hitl persists it and
	// seals detection_resolved with the decision (see hitl manager tests).
	if mgr.request.Detection == nil || mgr.request.Detection.RuleID != "test-rule" {
		t.Fatalf("checkpoint request detection = %+v", mgr.request.Detection)
	}
}

func TestExecutorCommandStopApprovalShowsRecordedCommand(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("command_stop", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		if args["handle"] != "process-123" {
			t.Fatalf("handle = %#v want process-123", args["handle"])
		}
		return "stopped", nil
	}); err != nil {
		testutil.FailErr(t, "register command_stop", err)
	}

	mgr := &asyncHITL{requested: make(chan struct{}, 1)}
	executor := toolexecution.NewExecutor(stubPolicy{decision: &platform.PolicyDecision{
		Allowed:          true,
		RequiresApproval: true,
	}}, reg, "implement")
	executor.Approvals.SetCheckpointManager(t.Context(), mgr, nil)
	executor.Approvals.SetBackgroundCommandResolver(func(sessionID, handle string) string {
		if sessionID != "session-123" || handle != "process-123" {
			t.Fatalf("resolver received session=%q handle=%q", sessionID, handle)
		}
		return "npm run dev"
	})
	executor.Approvals.SetApprovalExplainer(approvalExplainerFunc(func(action hitl.ProposedAction) toolapproval.ApprovalExplanation {
		return toolapproval.ApprovalExplanation{What: "Stop the background command: " + action.Presentation.Command + "."}
	}))

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := executor.Invoke(context.Background(), "command_stop", map[string]any{"handle": "process-123"}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "session-123",
				Agent: "implement"},
		})
		done <- result{out: out, err: err}
	}()

	select {
	case <-mgr.requested:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for command_stop approval")
	}
	if mgr.request.ProposedAction == nil || mgr.request.ProposedAction.Presentation.Command != "npm run dev" {
		t.Fatalf("approval action = %+v", mgr.request.ProposedAction)
	}
	if mgr.request.Explanation == nil || mgr.request.Explanation.What != "Stop the background command: npm run dev." {
		t.Fatalf("approval explanation = %+v", mgr.request.Explanation)
	}
	mgr.approve()
	got := <-done
	testutil.FailErr(t, "invoke command_stop", got.err)
	if got.out != "stopped" {
		t.Fatalf("output = %q want stopped", got.out)
	}
}

func TestExecutorRejectReturnsApprovalDenied(t *testing.T) {
	tmp := t.TempDir()
	stageApprovals(t, settings.ApprovalEffectAsk)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())

	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))

	reg := tools.NewDefaultRegistry()
	ran := false
	if err := reg.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if err := reviewWriteFixture(ctx, args, tc); err != nil {
			return "", err
		}
		ran = true
		return "ok", nil
	}); err != nil {
		testutil.FailErr(t, "register write", err)
	}

	mgr := &rejectHITL{guidance: "Use the read tool and summarize the current file instead."}
	outcomes := bundledOutcomes(t)
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, gate)
	exec.Approvals.SetApprovalOutcomeRenderer(outcomes)

	_, err = exec.Invoke(context.Background(), "write", map[string]any{"path": "a.txt", "content": "x"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmp, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: "sess-1",
			Agent: "implement"},
	})
	if err == nil {
		t.Fatal("expected error after reject")
	}
	refusal, ok := guidance.RefusalFromError(err)
	if !ok || refusal.Facts.Outcome != api.ToolResultOutcomeRejected || refusal.Code() != approvaloutcome.CodeApprovalDenied {
		t.Fatalf("approval refusal lost its structured outcome: %v", err)
	}
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.FailureClass != api.FailureClassPolicyRejection {
		t.Fatalf("approval failure metadata: %+v", reject)
	}
	wantDenied := outcomes.ApprovalOutcome(approvaloutcome.CodeApprovalDenied, map[string]any{
		"guidance": mgr.guidance,
	})
	if !strings.Contains(err.Error(), wantDenied) {
		t.Fatalf("err = %v want %q", err, wantDenied)
	}
	if ran {
		t.Fatal("tool must not run after reject")
	}
}

func TestExecutorExpiryReturnsTimeoutNotDenial(t *testing.T) {
	tmp := t.TempDir()
	stageApprovals(t, settings.ApprovalEffectAsk)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())

	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))

	reg := tools.NewDefaultRegistry()
	ran := false
	if err := reg.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if err := reviewWriteFixture(ctx, args, tc); err != nil {
			return "", err
		}
		ran = true
		return "ok", nil
	}); err != nil {
		testutil.FailErr(t, "register write", err)
	}

	mgr := &expireHITL{}
	outcomes := bundledOutcomes(t)
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, gate)
	exec.Approvals.SetApprovalOutcomeRenderer(outcomes)

	_, err = exec.Invoke(context.Background(), "write", map[string]any{"path": "a.txt", "content": "x"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmp, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: "sess-1",
			Agent: "implement"},
	})
	if err == nil {
		t.Fatal("expected error after expiry")
	}
	wantExpired := outcomes.ApprovalOutcome(approvaloutcome.CodeApprovalExpired, nil)
	if !strings.Contains(err.Error(), wantExpired) {
		t.Fatalf("err = %v want %q", err, wantExpired)
	}
	if strings.Contains(err.Error(), outcomes.ApprovalOutcome(approvaloutcome.CodeApprovalDenied, nil)) {
		t.Fatalf("expiry must not surface as a denial: %v", err)
	}
	if ran {
		t.Fatal("tool must not run after expiry")
	}
}

func TestListIncludesToolsRequiringApprovalAsk(t *testing.T) {
	tmp := t.TempDir()
	stageApprovals(t, settings.ApprovalEffectAsk)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())

	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	reg := tools.NewDefaultRegistry()
	for _, name := range []string{"read", "write", "edit", "replace_lines"} {
		if err := reg.Register(name, func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
			return "ok", nil
		}); err != nil {
			testutil.FailErr(t, "register "+name, err)
		}
	}

	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")

	listed, err := exec.Metadata.List(context.Background(), platform.ToolFilter{ProfileID: "implement"})
	testutil.FailErr(t, "exec.List failed", err)
	names := make(map[string]bool)
	for _, meta := range listed {
		names[meta.Name] = true
	}
	for _, want := range []string{"read", "write", "edit", "replace_lines"} {
		if !names[want] {
			t.Fatalf("list missing %q (ask tools must appear on wire): %v", want, names)
		}
	}
}

func TestExecutorCheckpointManagerNotConfigured(t *testing.T) {
	tmp := t.TempDir()
	stageApprovals(t, settings.ApprovalEffectAsk)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())

	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))

	reg := tools.NewDefaultRegistry()
	if err := reg.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if err := reviewWriteFixture(ctx, args, tc); err != nil {
			return "", err
		}
		t.Fatal("tool must not run without checkpoint manager")
		return "", nil
	}); err != nil {
		testutil.FailErr(t, "register write", err)
	}

	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")

	_, err = exec.Invoke(context.Background(), "write", map[string]any{"path": "a.txt"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmp, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: "sess-1",
			Agent: "implement"},
	})
	if err == nil || !strings.Contains(err.Error(), "checkpoints not configured") {
		t.Fatalf("err = %v want checkpoints not configured", err)
	}
}

type rejectHITL struct {
	checkpointID string
	guidance     string
}

func (m *rejectHITL) RequestCheckpoint(_ context.Context, _ hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.checkpointID = "chk-reject"
	return &hitl.CheckpointResponse{CheckpointID: m.checkpointID, Status: hitl.DecisionStatusPending}, nil
}

func (m *rejectHITL) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{
		CheckpointID: checkpointID,
		Status:       hitl.DecisionStatusRejected,
		Result:       &hitl.DecisionResult{Approved: false, Comments: m.guidance},
	}, nil
}

func (m *rejectHITL) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (m *rejectHITL) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (m *rejectHITL) SessionApprovalDenied(context.Context, string) (bool, error) {
	return true, nil
}
func (m *rejectHITL) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (m *rejectHITL) PatchPendingToolApprovalAIRationale(context.Context, string, string) error {
	return nil
}
func (m *rejectHITL) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}
func (m *rejectHITL) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

// outcomeCatalog loads the bundled approval-outcome copy and adapts it to the
// executor's renderer interface, so tests assert on real YAML-authored prose.
type outcomeCatalog struct {
	cat *approvaloutcome.Catalog
}

func (o outcomeCatalog) ApprovalOutcome(code string, ctx map[string]any) string {
	return o.cat.Message(code, ctx)
}

func bundledOutcomes(t *testing.T) outcomeCatalog {
	t.Helper()
	cfg, err := approvaloutcome.Load()
	testutil.FailErr(t, "load approval-outcome catalog", err)
	return outcomeCatalog{cat: approvaloutcome.NewCatalog(cfg)}
}

type expireHITL struct {
	checkpointID string
}

func (m *expireHITL) RequestCheckpoint(_ context.Context, _ hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.checkpointID = "chk-expire"
	return &hitl.CheckpointResponse{CheckpointID: m.checkpointID, Status: hitl.DecisionStatusPending}, nil
}

func (m *expireHITL) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: checkpointID, Status: hitl.DecisionStatusExpired}, nil
}

func (m *expireHITL) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (m *expireHITL) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (m *expireHITL) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (m *expireHITL) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (m *expireHITL) PatchPendingToolApprovalAIRationale(context.Context, string, string) error {
	return nil
}
func (m *expireHITL) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}
func (m *expireHITL) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

type asyncHITL struct {
	checkpointID string
	approved     atomic.Bool
	requested    chan struct{}
	request      hitl.CheckpointRequest
}

func (m *asyncHITL) RequestCheckpoint(_ context.Context, request hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.checkpointID = "chk-1"
	m.request = request
	select {
	case m.requested <- struct{}{}:
	default:
	}
	return &hitl.CheckpointResponse{CheckpointID: m.checkpointID, Status: hitl.DecisionStatusPending}, nil
}

func (m *asyncHITL) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	if m.approved.Load() {
		return &hitl.CheckpointResponse{
			CheckpointID: checkpointID,
			Status:       hitl.DecisionStatusApproved,
			Result:       &hitl.DecisionResult{Approved: true},
		}, nil
	}
	return &hitl.CheckpointResponse{CheckpointID: checkpointID, Status: hitl.DecisionStatusPending}, nil
}

func (m *asyncHITL) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (m *asyncHITL) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (m *asyncHITL) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (m *asyncHITL) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (m *asyncHITL) PatchPendingToolApprovalAIRationale(context.Context, string, string) error {
	return nil
}
func (m *asyncHITL) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}
func (m *asyncHITL) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

func (m *asyncHITL) approve() { m.approved.Store(true) }

type approvalExplainerFunc func(hitl.ProposedAction) toolapproval.ApprovalExplanation

func (f approvalExplainerFunc) ExplainApproval(action hitl.ProposedAction) toolapproval.ApprovalExplanation {
	return f(action)
}

// stageApprovals replaces the shipped approval rules for one test.
func stageApprovals(t *testing.T, effect settings.ApprovalEffect) {
	t.Helper()
	configtest.Overlay(t, map[config.Rel]string{
		config.SecurityApprovals: "rules:\n  - category: tool\n    pattern: write\n    effect: " + string(effect) + "\n",
	})
}

func (m *rejectHITL) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

func (m *expireHITL) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

func (m *asyncHITL) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
