package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type capabilityRecorder struct {
	records []authzledger.CapabilityRecord
}

func (*capabilityRecorder) AppendToolDenied(context.Context, authzledger.ToolDeniedRecord) {}

func (r *capabilityRecorder) AppendCapabilityRecord(_ context.Context, rec authzledger.CapabilityRecord) error {
	r.records = append(r.records, rec)
	return nil
}

func (*capabilityRecorder) AppendDirectIPLifecycle(context.Context, authzledger.DirectIPLifecycleRecord) {
}

func (*capabilityRecorder) AppendMediatedEndpoint(context.Context, authzledger.MediatedEndpointRecord) {
}

func trustedModelAlert() secretmatch.Alert {
	return secretmatch.Alert{
		SessionID: "sess-1", RootSessionID: "root-1", ProjectID: "proj-1",
		Surface:            secretmatch.SurfaceModel,
		DestinationID:      "ollama-1@abcd",
		DestinationLabel:   "Ollama",
		DestinationTrusted: true,
		RuleID:             "gitleaks:github-pat", RuleTitle: "GitHub token", GenericShape: "ghp_…",
		Fingerprints: []secretmatch.SecretFingerprint{"fp-1"},
	}
}

// A trusted destination sends unchanged with no card, and the ledger still
// counts the send under the trust that authorized it.
func TestAskSecretScreenTrustedDestinationSendsUnchangedAndRecordsTheSend(t *testing.T) {
	recorder := &capabilityRecorder{}
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetAuthzRecorder(recorder)

	resolution, err := exec.Secrets.AskSecretScreen(context.Background(), trustedModelAlert())
	testutil.FailErr(t, "ask for a trusted destination", err)
	if resolution.Decision != secretmatch.SendUnchanged {
		t.Fatalf("decision = %q, want send_unchanged", resolution.Decision)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("ledger rows = %d, want one trusted-destination row", len(recorder.records))
	}
	row := recorder.records[0]
	if row.Action != authzledger.ActionSecretDestinationTrusted ||
		row.AuthorizationSource != authzledger.AuthorizationSourceTrustedDestination ||
		row.Outcome != authzledger.OutcomeAllowed || row.ResolvedBy != authzledger.ResolvedByHuman ||
		row.SessionID != "root-1" || row.Tool != string(secretmatch.SurfaceModel) {
		t.Fatalf("ledger row = %+v", row)
	}
}

// Without trust the same alert reaches the card path, which faults with no
// checkpoint manager.
func TestAskSecretScreenUntrustedDestinationReachesTheCard(t *testing.T) {
	recorder := &capabilityRecorder{}
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetAuthzRecorder(recorder)
	alert := trustedModelAlert()
	alert.DestinationTrusted = false

	_, err := exec.Secrets.AskSecretScreen(context.Background(), alert)
	fault, ok := secretmatch.Faulted(err)
	if !ok || fault.Stage != secretmatch.FaultStageCheckpointsUnwired {
		t.Fatalf("err = %v, want the card path", err)
	}
	if len(recorder.records) != 0 {
		t.Fatalf("ledger rows = %+v, want none before a card exists", recorder.records)
	}
}

// The secret card carries a release ladder (day, chat, project) and a separate
// standing device redaction rung, each with its own grant identity.
func TestSecretCardCarriesBothLadders(t *testing.T) {
	t.Parallel()
	recipients := []secretmatch.Recipient{secretmatch.LocalRecipient}
	fingerprints := []secretmatch.SecretFingerprint{"fp-one"}
	release := secretReleaseLadder("chat-1", "proj-1", "/workspace", recipients, []string{"token"}, false, fingerprints)
	if got := offerRungs(release); !slices.Equal(got, []hitl.ApprovalOptionRung{hitl.ApprovalRungDay, hitl.ApprovalRungChat, hitl.ApprovalRungProject}) {
		t.Fatalf("release rungs = %v, want day, chat, project", got)
	}
	if release[1].Scope != hitl.ApprovalGrantScopeChat || release[1].Grant.ChatSessionID != "chat-1" {
		t.Fatalf("chat rung = scope %s chat %q", release[1].Scope, release[1].Grant.ChatSessionID)
	}
	for _, offer := range release {
		if offer.Grant.Predicate.Category != hitl.ApprovalGrantCategorySecret {
			t.Errorf("release rung %s grants category %q", offer.Rung, offer.Grant.Predicate.Category)
		}
	}
	managed := secretReleaseLadder("chat-1", "proj-1", "/workspace", recipients, []string{"token"}, true, fingerprints)
	if managed[0].Title != hitl.TitleSendFor1Day || release[0].Title != hitl.TitleSendUnchangedFor1Day {
		t.Fatalf("day titles = managed %q, unmanaged %q", managed[0].Title, release[0].Title)
	}
	if got := offerRungs(secretReleaseLadder("", "proj-1", "/workspace", recipients, nil, false, fingerprints)); !slices.Equal(got, []hitl.ApprovalOptionRung{hitl.ApprovalRungDay, hitl.ApprovalRungProject}) {
		t.Fatalf("release rungs without a chat = %v, want day, project", got)
	}

	redact := secretRedactLadder("proj-1", "/workspace", fingerprints)
	if len(redact) != 1 || redact[0].Rung != hitl.ApprovalRungDevice || redact[0].Group != hitl.GroupRedaction ||
		redact[0].Grant.Predicate.Category != hitl.ApprovalGrantCategorySecretRedact {
		t.Fatalf("redact ladder = %+v, want one device redaction rung", redact)
	}

	ids := map[string]bool{}
	for _, offer := range append(release, redact...) {
		if offer.ID == "" || offer.ID != offer.Grant.ID || ids[offer.ID] {
			t.Fatalf("offer %s id %q (grant %q) is empty, split or shared", offer.Rung, offer.ID, offer.Grant.ID)
		}
		ids[offer.ID] = true
	}
	other := secretRedactLadder("proj-1", "/workspace", []secretmatch.SecretFingerprint{"fp-two"})
	if len(other) != 1 || other[0].ID == redact[0].ID {
		t.Fatal("a different secret shares the redaction grant identity")
	}
	if secretRedactLadder("", "/workspace", fingerprints) != nil || secretReleaseLadder("chat-1", "", "/workspace", recipients, nil, false, fingerprints) != nil {
		t.Fatal("a ladder without a project minted offers")
	}
}

func offerRungs(offers []hitl.ApprovalGrantOffer) []hitl.ApprovalOptionRung {
	out := make([]hitl.ApprovalOptionRung, 0, len(offers))
	for _, offer := range offers {
		out = append(out, offer.Rung)
	}
	return out
}

// guardrailsHITL records each raised card in the shared event order and
// rejects it.
type guardrailsHITL struct {
	mu     sync.Mutex
	events *[]string
	reqs   []hitl.CheckpointRequest
}

func (m *guardrailsHITL) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.mu.Lock()
	*m.events = append(*m.events, "card")
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()
	return &hitl.CheckpointResponse{CheckpointID: "chk-secret", Status: hitl.DecisionStatusPending}, nil
}

func (m *guardrailsHITL) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{
		CheckpointID: checkpointID,
		Status:       hitl.DecisionStatusRejected,
		Result:       &hitl.DecisionResult{Approved: false},
	}, nil
}

func (m *guardrailsHITL) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}

func (m *guardrailsHITL) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

func (m *guardrailsHITL) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}

func (m *guardrailsHITL) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}

func (m *guardrailsHITL) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}

func (m *guardrailsHITL) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}

func (m *guardrailsHITL) PatchPendingToolApprovalAIRationale(context.Context, string, string, string) (bool, error) {
	return false, nil
}

func (m *guardrailsHITL) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}

func (m *guardrailsHITL) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

func guardrailsExecutor(t *testing.T) (*Executor, *guardrailsHITL, *[]string) {
	t.Helper()
	var events []string
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register command", reg.Register("command", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		events = append(events, "run")
		return "ok", nil
	}))
	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "BuildMatcher", err)
	mgr := &guardrailsHITL{events: &events}
	exec := NewExecutor(nil, reg, "implement")
	exec.Secrets.SetSecretMatcher(m)
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, nil)
	return exec, mgr, &events
}

// A credential in command argv is screened before the tool runs: the gate
// asks through the outbound-secret gate, and a declined card never runs the
// tool nor carries the value. Clean argv runs without a card.
func TestOutboundSecretArgvAsksBeforeRun(t *testing.T) {
	tc := tools.ToolContext{
		Invocation: tools.Invocation{Contract: catalogContract(t, "command")},
		Identity:   tools.InvocationIdentity{SessionID: "s1", ToolCallID: "tc1", ProjectID: "project-id", Agent: "implement"},
	}

	clean, _, cleanEvents := guardrailsExecutor(t)
	_, err := clean.Invoke(t.Context(), "command", map[string]any{"command": "curl https://example.test"}, tc)
	testutil.FailErr(t, "clean command", err)
	if strings.Join(*cleanEvents, ",") != "run" {
		t.Fatalf("clean argv events = %v, want run only", *cleanEvents)
	}

	exec, mgr, events := guardrailsExecutor(t)
	_, err = exec.Invoke(t.Context(), "command", argvWithCredential(), tc)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != toolrejection.OutboundSecretDeniedCode {
		t.Fatalf("declined secret send err = %v, want %s", err, toolrejection.OutboundSecretDeniedCode)
	}
	if strings.Join(*events, ",") != "card" {
		t.Fatalf("secret argv events = %v, want one card and no run", *events)
	}
	req := mgr.reqs[0]
	if req.Decision == nil || req.Decision.Primary != api.GateSecretOutbound {
		t.Fatalf("card decision = %+v, want primary %s", req.Decision, api.GateSecretOutbound)
	}
	raw, err := json.Marshal(req)
	testutil.FailErr(t, "marshal card", err)
	if strings.Contains(string(raw)+err2s(reject), plantAWS) {
		t.Fatal("card or refusal carries the matched value")
	}
}

func err2s(reject *toolrejection.ToolReject) string {
	raw, _ := json.Marshal(reject.Data)
	return reject.Observation + string(raw)
}
