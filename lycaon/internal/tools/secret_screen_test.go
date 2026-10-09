package tools_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type secretScreenHITL struct {
	status hitl.DecisionStatus
	result *hitl.DecisionResult
	req    hitl.CheckpointRequest
	err    error
}

func (m *secretScreenHITL) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.req = req
	if m.err != nil {
		return nil, m.err
	}
	return &hitl.CheckpointResponse{CheckpointID: "chk-secret", Status: hitl.DecisionStatusPending}, nil
}

func (m *secretScreenHITL) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: checkpointID, Status: m.status, Result: m.result}, nil
}

func TestAskSecretScreenMapsThreeWayDecision(t *testing.T) {
	finding := secretmatch.Alert{
		SessionID: "sess-1", ProjectID: "proj-1", Surface: secretmatch.SurfaceModel,
		DestinationID: "fireworks-main", DestinationLabel: "Fireworks",
		RuleID: "gitleaks:github-pat", RuleTitle: "GitHub Personal Access Token",
		GenericShape: "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
		Occurrences:  2, SourceKind: secretmatch.SourceToolResult, SourceTool: "read", SourcePath: ".env.local", SourceLine: 7,
		OriginKind: secretmatch.OriginFile, SourceToolCallID: "call_read_1",
	}
	cases := []struct {
		name         string
		mgr          *secretScreenHITL
		want         secretmatch.Decision
		wantGuidance string
	}{
		{name: "unchanged", mgr: &secretScreenHITL{status: hitl.DecisionStatusApproved}, want: secretmatch.SendUnchanged},
		{name: "redacted", mgr: &secretScreenHITL{status: hitl.DecisionStatusApproved, result: &hitl.DecisionResult{Approved: true, RedactSecrets: true}}, want: secretmatch.SendRedacted},
		// A declined send maps to Withhold.
		{name: "declined", mgr: &secretScreenHITL{status: hitl.DecisionStatusRejected}, want: secretmatch.Withhold},
		{
			name:         "declined_with_guidance",
			mgr:          &secretScreenHITL{status: hitl.DecisionStatusRejected, result: &hitl.DecisionResult{Comments: " use the staging key "}},
			want:         secretmatch.Withhold,
			wantGuidance: "use the staging key",
		},
		// A card that was raised and then expired is unanswered, not a No.
		{name: "expired", mgr: &secretScreenHITL{status: hitl.DecisionStatusExpired}, want: secretmatch.Unanswered},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
			exec.Approvals.SetCheckpointManager(tc.mgr, nil)
			got, askErr := exec.Secrets.AskSecretScreen(context.Background(), finding)
			testutil.FailErr(t, "ask secret screen", askErr)
			if got.Decision != tc.want {
				t.Fatalf("decision = %q want %q", got.Decision, tc.want)
			}
			if got.Guidance != tc.wantGuidance {
				t.Fatalf("guidance = %q want %q", got.Guidance, tc.wantGuidance)
			}
			if tc.mgr.req.SecretScreen == nil {
				t.Fatal("secret_screen payload missing")
			}
			if tc.mgr.req.SecretScreen.RuleTitle != finding.RuleTitle || tc.mgr.req.SecretScreen.SourceLine != 7 ||
				!tc.mgr.req.SecretScreen.CanRedact || !tc.mgr.req.SecretScreen.CanTrack {
				t.Fatalf("payload = %+v", tc.mgr.req.SecretScreen)
			}
			if tc.mgr.req.SecretScreen.OriginKind != secretmatch.OriginFile ||
				tc.mgr.req.SecretScreen.SourceToolCallID != "call_read_1" {
				t.Fatalf("origin = %+v", tc.mgr.req.SecretScreen)
			}
			if len(tc.mgr.req.GrantOffers) != 0 {
				t.Fatalf("model secret approval offered reusable grants: %+v", tc.mgr.req.GrantOffers)
			}
		})
	}
}

// An impossible redact decision returns a structured fault.
func TestAskSecretScreenRedactDecisionOnUnrewritableSurfaceFaults(t *testing.T) {
	mgr := &secretScreenHITL{
		status: hitl.DecisionStatusApproved,
		result: &hitl.DecisionResult{Approved: true, RedactSecrets: true},
	}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(mgr, nil)
	got, askErr := exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		SessionID: "sess-1", ProjectID: "proj-1", Surface: secretmatch.SurfaceCommand, DestinationID: "process",
		RuleID: "gitleaks:github-pat", RuleTitle: "GitHub Personal Access Token",
		GenericShape: "a1b2c3a1b2c3a1b2c3a1 (20 characters)",
		SourceKind:   secretmatch.SourceToolArgument,
	})
	fault, ok := secretmatch.Faulted(askErr)
	if !ok || fault.Stage != secretmatch.FaultStageRedactUnsupported {
		t.Fatalf("err = %v, want a redact_unsupported fault", askErr)
	}
	if got.Decision != "" {
		t.Fatalf("fault carried a decision %q; nobody chose it", got.Decision)
	}
	if mgr.req.SecretScreen == nil || mgr.req.SecretScreen.CanTrack {
		t.Fatalf("command screen offered tracking outside the model boundary: %+v", mgr.req.SecretScreen)
	}
}

// Pre-card exits are faults, not decisions.
func TestAskSecretScreenPreCardExitsFaultRatherThanBlockSilently(t *testing.T) {
	finding := secretmatch.Alert{
		SessionID: "sess-1", ProjectID: "proj-1", Surface: secretmatch.SurfaceModel, DestinationID: "provider",
		RuleID: "gitleaks:github-pat", RuleTitle: "GitHub Personal Access Token",
		GenericShape: "a1b2c3a1b2c3a1b2c3a1 (20 characters)",
		SourceKind:   secretmatch.SourceToolResult, SourceTool: "read",
	}
	raiseErr := errors.New("secret approval plan target has no generic shape")
	cases := []struct {
		name      string
		mgr       hitl.CheckpointManager
		alert     secretmatch.Alert
		wantStage string
		wantCause error
	}{
		{name: "no_checkpoint_manager", mgr: nil, alert: finding, wantStage: secretmatch.FaultStageCheckpointsUnwired},
		{
			name:      "no_session",
			mgr:       &secretScreenHITL{status: hitl.DecisionStatusApproved},
			alert:     func() secretmatch.Alert { a := finding; a.SessionID = ""; return a }(),
			wantStage: secretmatch.FaultStageNoSession,
		},
		{
			name:      "raise_failed",
			mgr:       &secretScreenHITL{err: raiseErr},
			alert:     finding,
			wantStage: secretmatch.FaultStageRaise,
			wantCause: raiseErr,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
			if tc.mgr != nil {
				exec.Approvals.SetCheckpointManager(tc.mgr, nil)
			}
			got, askErr := exec.Secrets.AskSecretScreen(context.Background(), tc.alert)
			fault, ok := secretmatch.Faulted(askErr)
			if !ok {
				t.Fatalf("err = %v, want an AskFault", askErr)
			}
			if fault.Stage != tc.wantStage {
				t.Fatalf("stage = %q, want %q", fault.Stage, tc.wantStage)
			}
			if got.Decision != "" {
				t.Fatalf("fault carried decision %q; no card was raised", got.Decision)
			}
			if tc.wantCause != nil && !errors.Is(askErr, tc.wantCause) {
				t.Fatalf("fault dropped its cause: %v", askErr)
			}
		})
	}
}

func TestAskSecretScreenPublicInboundIsSilentWithoutCheckpoint(t *testing.T) {
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	got, askErr := exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		Surface: secretmatch.SurfaceModel,
		RuleID:  "gitleaks:github-pat", RuleTitle: "GitHub Personal Access Token",
		SourceKind: secretmatch.SourceToolResult, SourceTool: "fetch_url",
		Source: gate.SecretSourcePublicInbound,
	})
	testutil.FailErr(t, "ask secret screen", askErr)
	if got.Decision != secretmatch.SendUnchanged {
		t.Fatalf("decision = %q want %q", got.Decision, secretmatch.SendUnchanged)
	}
}

func (m *secretScreenHITL) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (m *secretScreenHITL) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (m *secretScreenHITL) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (m *secretScreenHITL) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (m *secretScreenHITL) PatchPendingToolApprovalAIRationale(context.Context, string, string) error {
	return nil
}
func (m *secretScreenHITL) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}
func (m *secretScreenHITL) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

func TestAskSecretScreenUsesAttributionAndNeverStoresValue(t *testing.T) {
	const planted = "AKIAQYJK5TXV4NZR7SGB"
	match := secretmatch.Match{
		RuleID:       "gitleaks:aws-access-token",
		Title:        "AWS access key ID",
		Severity:     "critical",
		GenericShape: "a1b2c3a1b2c3a1b2c3a1 (20 characters)",
	}
	ctx := secretmatch.WithAskAttribution(context.Background(), secretmatch.AskAttribution{
		SessionID:  "sess-1",
		ProjectID:  "proj-1",
		ToolCallID: "tc-1",
	})

	mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(mgr, nil)
	decision, askErr := exec.Secrets.AskSecretScreen(ctx, secretmatch.Alert{
		Surface:       secretmatch.SurfaceWebSearch,
		DestinationID: "web_search", DestinationLabel: "web search providers",
		RuleID: match.RuleID, RuleTitle: match.Title, GenericShape: match.GenericShape,
		Occurrences: 1, SourceKind: secretmatch.SourceToolArgument, SourceTool: "web_search", SourcePath: "query",
		OriginKind: secretmatch.OriginField,
	})
	testutil.FailErr(t, "ask secret screen", askErr)
	if decision.Decision != secretmatch.SendUnchanged {
		t.Fatalf("decision = %q", decision.Decision)
	}
	if mgr.req.ProposedAction == nil {
		t.Fatal("missing proposed action")
	}
	args := mgr.req.ProposedAction.Invocation.Args
	blob := strings.Join([]string{
		mgr.req.Title,
		stringifyAny(args["surface"]),
		stringifyAny(args["rule_id"]),
		stringifyAny(args["destination_id"]),
		stringifyAny(args["shape"]),
	}, " ")
	if strings.Contains(blob, planted) {
		t.Fatalf("value in checkpoint payload: %s", blob)
	}
	if shape := stringifyAny(args["shape"]); shape != match.GenericShape {
		t.Fatalf("checkpoint shape = %q, want %q", shape, match.GenericShape)
	}
	wantArgs := []string{
		"surface", "surface_label", "can_redact", "destination_id", "destination_label",
		"destination_kind", "rule_id", "rule_title", "shape", "occurrences", "source_kind",
		"source_tool", "source_path", "source_line",
	}
	if len(args) != len(wantArgs) {
		t.Fatalf("checkpoint args keys = %v", args)
	}
	for _, key := range wantArgs {
		if _, ok := args[key]; !ok {
			t.Errorf("checkpoint args missing %q", key)
		}
	}
	// The title uses the display label.
	if !strings.Contains(mgr.req.Title, "web search") {
		t.Fatalf("title=%q", mgr.req.Title)
	}
	if args["destination_label"] != "web search providers" {
		t.Fatalf("destination label = %q", args["destination_label"])
	}
	if mgr.req.SecretScreen == nil || mgr.req.SecretScreen.SourcePath != "query" ||
		mgr.req.SecretScreen.OriginKind != secretmatch.OriginField ||
		mgr.req.SecretScreen.ToolCallID != "tc-1" {
		t.Fatalf("secret payload = %+v", mgr.req.SecretScreen)
	}
	if mgr.req.Explanation == nil || mgr.req.Explanation.What == "" {
		t.Fatalf("explanation = %+v", mgr.req.Explanation)
	}
}

func stringifyAny(v any) string {
	s, _ := v.(string)
	return s
}

func (m *secretScreenHITL) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
