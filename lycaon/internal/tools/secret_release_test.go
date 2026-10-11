package tools_test

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestSecretScreenOffersTheReleaseLadder(t *testing.T) {
	mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, nil)
	fingerprints := []secretmatch.SecretFingerprint{"sf1_second", "sf1_first"}
	got, askErr := exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		SessionID: "worker", RootSessionID: "sess", ProjectID: "project-id", ProjectDir: "/project",
		Surface:       secretmatch.SurfaceModel,
		DestinationID: "provider", DestinationLabel: "Provider",
		RuleID: "rule", RuleTitle: "Credential",
		GenericShape: "a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2 (40 characters)",
		Occurrences:  2, SourceKind: secretmatch.SourceToolArgument,
		Fingerprints: fingerprints,
	})
	testutil.FailErr(t, "ask secret screen", askErr)
	if got.Decision != secretmatch.SendUnchanged {
		t.Fatalf("decision = %q", got.Decision)
	}
	// Standing redaction is one device-wide rung: widening it sends less.
	var release, redact []hitl.ApprovalGrantOffer
	for _, o := range mgr.req.GrantOffers {
		if o.Grant.Predicate.Category == hitl.ApprovalGrantCategorySecretRedact {
			redact = append(redact, o)
			continue
		}
		release = append(release, o)
	}
	if len(release) != 3 || len(redact) != 1 {
		t.Fatalf("secret ladders = %d release, %d redact: %+v", len(release), len(redact), mgr.req.GrantOffers)
	}
	if redact[0].Group != hitl.GroupRedaction || redact[0].Grant.ExpiresAt == nil || redact[0].Scope != hitl.ApprovalGrantScopeDevice {
		t.Errorf("redact rung = %+v, want an expiring device rung in the Redaction group", redact[0])
	}
	offer := release[0]
	if offer.Title != hitl.TitleSendUnchangedFor1Day || offer.TTLSeconds != hitl.DayRungTTLSeconds || offer.Scope != hitl.ApprovalGrantScopeProject {
		t.Fatalf("day offer = %+v", offer)
	}
	for _, o := range mgr.req.GrantOffers {
		if o.Grant.Title != o.Title {
			t.Errorf("offer %q and its grant disagree on the title (%q) — Saved approvals lists the grant", o.Title, o.Grant.Title)
		}
		if len(o.Grant.SecretFingerprints) != 2 {
			t.Errorf("rung %q lost the fingerprint set: %+v", o.Rung, o.Grant.SecretFingerprints)
		}
	}
	if offer.Grant.Predicate.Category != hitl.ApprovalGrantCategorySecret || offer.Grant.ProjectID != "project-id" || len(offer.Grant.SecretFingerprints) != 2 {
		t.Fatalf("secret authority = %+v", offer.Grant)
	}
	if !secretmatch.RecipientCovered(offer.Grant.SecretRecipients, "provider", string(secretmatch.SurfaceModel)) || !hitl.WitnessEqual(offer.Grant.Witness, hitl.SecretReleaseWitness(offer.Grant.SecretRecipients)) {
		t.Fatalf("release boundary = %+v", offer.Grant)
	}
	if release[1].Scope != hitl.ApprovalGrantScopeChat || release[1].Grant.ChatSessionID != "sess" {
		t.Fatalf("chat release is not bound to the originating chat: %+v", release[1])
	}
	plan, err := hitl.CompileCheckpointApprovalPlan(mgr.req)
	testutil.FailErr(t, "compile secret approval plan", err)
	// Model requests recommend protection and offer lease rungs but no quiet option.
	var leases, quiets int
	for _, opt := range plan.Options {
		switch opt.Kind {
		case hitl.ApprovalOptionLease:
			leases++
		case hitl.ApprovalOptionQuiet:
			quiets++
			if opt.Rung != hitl.ApprovalRungChat || opt.Group != hitl.GroupQuiet {
				t.Fatalf("quiet slot = %+v, want the chat row", opt)
			}
		case hitl.ApprovalOptionCurrentAction, hitl.ApprovalOptionRedacted, hitl.ApprovalOptionTracked:
		}
	}
	if plan.Options[0].Rung != hitl.ApprovalRungTracked || plan.RecommendedOptionID != plan.Options[0].ID {
		t.Fatalf("Protect must hold the face: %+v recommended=%q", plan.Options, plan.RecommendedOptionID)
	}
	if leases == 0 || quiets != 0 {
		t.Fatalf("secret ladder = %d leases, %d quiets: %+v", leases, quiets, plan.Options)
	}
}

func TestActiveSecretReleaseSkipsCheckpoint(t *testing.T) {
	mgr := &secretScreenHITL{}
	gate := &secretReleaseGateStub{covered: true}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, gate)
	ledger := authzcontext.NewMemoryStore()
	exec.Approvals.SetAuthzRecorder(authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: ledger}})
	got, askErr := exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		SessionID: "worker", RootSessionID: "sess", ProjectID: "project-id", ProjectDir: "/project", Surface: secretmatch.SurfaceMCP,
		DestinationID: "mcp-server", DestinationLabel: "Configured service", ToolCallID: "call",
		RuleID: "rule", RuleTitle: "Credential", Fingerprints: []secretmatch.SecretFingerprint{"sf1_value"},
	})
	testutil.FailErr(t, "ask secret screen", askErr)
	if got.Decision != secretmatch.SendUnchanged || mgr.req.SecretScreen != nil {
		t.Fatalf("leased decision = %q checkpoint = %+v", got.Decision, mgr.req.SecretScreen)
	}
	if gate.chatSessionID != "sess" || gate.projectID != "project-id" || gate.destinationID != "mcp-server" || gate.surface != string(secretmatch.SurfaceMCP) || len(gate.fingerprints) != 1 {
		t.Fatalf("lease lookup = %+v", gate)
	}
	rows, err := ledger.ListEvents(t.Context(), "sess")
	testutil.FailErr(t, "list secret reuse audit", err)
	if len(rows) != 1 || rows[0].Action != authzcontext.EventActionSecretPermissionUsed || rows[0].ResolvedBy != authzcontext.ResolvedByHuman {
		t.Fatalf("secret reuse audit = %+v", rows)
	}
	var detail authzcontext.EventDetail
	testutil.FailErr(t, "decode secret reuse audit", json.Unmarshal([]byte(rows[0].DetailJSON), &detail))
	if detail.ToolCallID != "call" || detail.AuthorizationSource != authzledger.AuthorizationSourceLease || detail.ExternalAccess == nil {
		t.Fatalf("secret reuse attribution = %+v", detail)
	}
	access := detail.ExternalAccess
	if len(access.Endpoints) != 0 || len(access.DeclaredDestinations) != 1 || access.DeclaredDestinations[0] != "Configured service" {
		t.Fatalf("secret reuse invented an observed destination: %+v", access)
	}
}

func TestSecretScreenDoesNotOfferPathBoundRelease(t *testing.T) {
	mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, nil)
	got, askErr := exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		SessionID: "sess", ProjectDir: "/project", Surface: secretmatch.SurfaceMCP, DestinationID: "mcp-server",
		RuleID: "rule", RuleTitle: "Credential", GenericShape: "a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2 (40 characters)",
		Fingerprints: []secretmatch.SecretFingerprint{"sf1_value"},
	})
	testutil.FailErr(t, "ask secret screen", askErr)
	if got.Decision != secretmatch.SendUnchanged {
		t.Fatalf("decision = %q", got.Decision)
	}
	for _, offer := range mgr.req.GrantOffers {
		if offer.Grant.Predicate.Category == hitl.ApprovalGrantCategorySecret {
			t.Fatalf("release offered without canonical project identity: %+v", offer)
		}
	}
}

func TestSecretScreenOffersProjectBoundReleaseWithoutProjectFolder(t *testing.T) {
	mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, nil)
	_, _ = exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		SessionID: "sess", ProjectID: "project-id", Surface: secretmatch.SurfaceMCP, DestinationID: "mcp-server",
		RuleID: "rule", RuleTitle: "Credential", GenericShape: "a1b2c3a1b2c3a1b2c3a1 (20 characters)",
		Fingerprints: []secretmatch.SecretFingerprint{"sf1_value"},
	})
	if len(mgr.req.GrantOffers) != 4 {
		t.Fatalf("project identity without folder produced %d grant offers, want 4: %+v", len(mgr.req.GrantOffers), mgr.req.GrantOffers)
	}
	for _, offer := range mgr.req.GrantOffers {
		if offer.Grant.ProjectID != "project-id" || offer.Grant.ProjectDir != "" {
			t.Fatalf("offer was not bound to canonical project identity: %+v", offer)
		}
	}
	moved := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	exec.Approvals.SetCheckpointManager(t.Context(), moved, nil)
	_, _ = exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		SessionID: "sess", ProjectID: "project-id", ProjectDir: "/moved/project", Surface: secretmatch.SurfaceMCP, DestinationID: "mcp-server",
		RuleID: "rule", RuleTitle: "Credential", GenericShape: "a1b2c3a1b2c3a1b2c3a1 (20 characters)",
		Fingerprints: []secretmatch.SecretFingerprint{"sf1_value"},
	})
	if len(moved.req.GrantOffers) != len(mgr.req.GrantOffers) {
		t.Fatalf("moved project produced a different ladder: %+v", moved.req.GrantOffers)
	}
	for i := range mgr.req.GrantOffers {
		if moved.req.GrantOffers[i].ID != mgr.req.GrantOffers[i].ID {
			t.Fatalf("offer identity changed with folder relocation: %q != %q", moved.req.GrantOffers[i].ID, mgr.req.GrantOffers[i].ID)
		}
	}
}

func TestReleaseQuietSendsUnchanged(t *testing.T) {
	mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	gate := &secretReleaseGateStub{quiet: true}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, gate)
	got, askErr := exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		SessionID: "sess", ProjectID: "project-id", ProjectDir: "/project",
		Surface:       secretmatch.SurfaceModel,
		DestinationID: "provider", DestinationLabel: "Provider",
		RuleID: "rule", RuleTitle: "Credential",
		Fingerprints: []secretmatch.SecretFingerprint{"sf1_value"},
	})
	testutil.FailErr(t, "ask secret screen", askErr)
	if got.Decision != secretmatch.SendUnchanged {
		t.Fatalf("release quiet decision = %q, want %q", got.Decision, secretmatch.SendUnchanged)
	}
	if mgr.req.SecretScreen != nil {
		t.Fatalf("quiet raised a card: %+v", mgr.req.SecretScreen)
	}
}

func TestQuietedUnredactableSecretScreenSendsUnchanged(t *testing.T) {
	mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	gate := &secretReleaseGateStub{quiet: true}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, gate)
	got, askErr := exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		SessionID: "sess", ProjectID: "project-id", ProjectDir: "/project",
		Surface:       secretmatch.SurfaceCommand,
		DestinationID: "provider", DestinationLabel: "Provider",
		RuleID: "rule", RuleTitle: "Credential",
		Fingerprints: []secretmatch.SecretFingerprint{"sf1_value"},
	})
	testutil.FailErr(t, "ask secret screen", askErr)
	if got.Decision != secretmatch.SendUnchanged {
		t.Fatalf("quieted unredactable decision = %q", got.Decision)
	}
}

type secretReleaseGateStub struct {
	covered        bool
	quiet          bool
	standingRedact bool
	chatSessionID  string
	projectID      string
	destinationID  string
	surface        string
	fingerprints   []string
}

func (g *secretReleaseGateStub) SecretFingerprintsCovered(chatSessionID, projectID, destinationID, surface string, fingerprints []string) bool {
	g.chatSessionID = chatSessionID
	g.projectID = projectID
	g.destinationID = destinationID
	g.surface = surface
	g.fingerprints = append([]string(nil), fingerprints...)
	return g.covered
}

func (g *secretReleaseGateStub) SecretRedactionStanding(string, []string) bool {
	return g.standingRedact
}

func (*secretReleaseGateStub) Evaluate(context.Context, hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	return &hitl.ApprovalResult{}, nil
}
func (*secretReleaseGateStub) GrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}
func (*secretReleaseGateStub) AbsorbedGrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}
func (*secretReleaseGateStub) ApplyGrant(hitl.ApprovalGrant) (bool, error) { return false, nil }
func (*secretReleaseGateStub) GrantCovers(hitl.ProposedAction) bool        { return false }
func (*secretReleaseGateStub) HostResourceLeaseCovers(hitl.ProposedAction) bool {
	return false
}
func (*secretReleaseGateStub) RevokeGrant(string) (bool, error)                    { return false, nil }
func (*secretReleaseGateStub) RevokeGrantInstalledBy(string, string) (bool, error) { return false, nil }
func (*secretReleaseGateStub) ListGrants(string) []hitl.ApprovalGrant              { return nil }
func (*secretReleaseGateStub) PutAskQuiet(hitl.AskQuiet, int) (hitl.AskQuiet, bool) {
	return hitl.AskQuiet{}, false
}
func (g *secretReleaseGateStub) AskQuietLive(_, key string) (hitl.AskQuiet, bool) {
	if !g.quiet {
		return hitl.AskQuiet{}, false
	}
	return hitl.AskQuiet{ID: "quiet_stub", Key: key}, true
}
func (*secretReleaseGateStub) NoteAskQuietSuppressed(string, string)         {}
func (*secretReleaseGateStub) ListAskQuiets(string) []hitl.AskQuiet          { return nil }
func (*secretReleaseGateStub) RevokeAskQuiet(string) bool                    { return false }
func (*secretReleaseGateStub) RevokeAskQuietInstalledBy(string, string) bool { return false }
func (*secretReleaseGateStub) ForgetSession(string)                          {}

func TestStandingRedactionStripsWithoutACard(t *testing.T) {
	mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, &secretReleaseGateStub{standingRedact: true, covered: true})
	got, askErr := exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		SessionID: "sess", ProjectID: "project-id", ProjectDir: "/project",
		Surface:       secretmatch.SurfaceModel,
		DestinationID: "provider", DestinationLabel: "Provider",
		RuleID: "rule", RuleTitle: "Credential",
		Fingerprints: []secretmatch.SecretFingerprint{"sf1_value"},
	})
	testutil.FailErr(t, "ask secret screen", askErr)
	if got.Decision != secretmatch.SendRedacted {
		t.Fatalf("standing redaction decision = %q, want %q", got.Decision, secretmatch.SendRedacted)
	}
	if mgr.req.SecretScreen != nil {
		t.Fatalf("standing redaction raised a card: %+v", mgr.req.SecretScreen)
	}
}

func TestStandingRedactionOnUnrewritableSurfaceStillAsks(t *testing.T) {
	mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, &secretReleaseGateStub{standingRedact: true})
	got, askErr := exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		SessionID: "sess", ProjectID: "project-id", ProjectDir: "/project",
		Surface:       secretmatch.SurfaceCommand,
		DestinationID: "proxy", DestinationLabel: "any host this command dials",
		RuleID: "rule", RuleTitle: "Credential",
		Fingerprints: []secretmatch.SecretFingerprint{"sf1_value"},
	})
	testutil.FailErr(t, "ask secret screen", askErr)
	if mgr.req.SecretScreen == nil {
		t.Fatal("unrewritable surface skipped the card on a standing redaction")
	}
	if !mgr.req.SecretScreen.StandingRedactionHeld || mgr.req.SecretScreen.CanRedact {
		t.Fatalf("secret screen = %+v, want held standing redaction on an unredactable surface", mgr.req.SecretScreen)
	}
	if got.Decision != secretmatch.SendUnchanged {
		t.Fatalf("decision = %q, want the approved answer %q", got.Decision, secretmatch.SendUnchanged)
	}
}

func TestRedactionContestRequiresReviewDespiteEarlierPermission(t *testing.T) {
	mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, &secretReleaseGateStub{standingRedact: true, covered: true, quiet: true})
	finding := secretmatch.Alert{
		SessionID: "chat", ProjectID: "project", Surface: secretmatch.SurfaceModel,
		DestinationID: "provider", DestinationLabel: "Provider", RuleID: "rule", RuleTitle: "Credential",
		Fingerprints: []secretmatch.SecretFingerprint{"value"},
	}
	permission := &secretcap.Resolution{}
	permission.ApproveRelease(secretcap.Release{Fingerprints: finding.Fingerprints, Recipients: []secretmatch.Recipient{{ID: "provider", Label: "Provider", Surface: secretmatch.SurfaceModel, Kind: secretmatch.DestinationModelProvider}}})
	ctx := secretcap.WithResolution(t.Context(), permission)
	redacted, err := exec.Secrets.AskSecretScreen(ctx, finding)
	testutil.FailErr(t, "apply standing redaction", err)
	if redacted.ReceiptToken == "" {
		t.Fatal("redaction did not return a contest receipt")
	}
	finding.ContestToken = redacted.ReceiptToken
	_, err = exec.Secrets.AskSecretScreen(ctx, finding)
	testutil.FailErr(t, "review redaction contest", err)
	if mgr.req.SecretScreen == nil || !mgr.req.SecretScreen.Contested {
		t.Fatal("earlier permission silently answered the contest")
	}
}

func TestServiceRecipientsRequireTheirOwnPermissionDespiteProcessQuiet(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
		exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
		exec.Approvals.SetCheckpointManager(t.Context(), mgr, &secretReleaseGateStub{quiet: quiet})
		_, err := exec.Secrets.AskSecretScreen(t.Context(), secretmatch.Alert{
			SessionID: "chat", ProjectID: "project", Surface: secretmatch.SurfaceCommand,
			DestinationID: "proxy", DestinationLabel: "Processes in this chat", RuleID: secretmatch.ManagedRuleID,
			RuleTitle: secretmatch.ManagedRuleTitle, GenericShape: "Protected value", OriginKind: secretmatch.OriginField, SourcePath: "arguments",
			Fingerprints: []secretmatch.SecretFingerprint{"value"}, SecretNames: []string{"Service password"}, ConnectPorts: []uint16{8080},
			Recipients: []secretmatch.Recipient{{ID: "service", Label: "http://localhost:8080", Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService}},
		})
		testutil.FailErr(t, "review declared recipients", err)
		if mgr.req.SecretScreen == nil {
			t.Fatalf("process quiet=%v suppressed the added service recipient", quiet)
		}
		plan, err := hitl.CompileCheckpointApprovalPlan(mgr.req)
		testutil.FailErr(t, "compile service review", err)
		for _, option := range plan.Options {
			if option.Kind == hitl.ApprovalOptionQuiet && !option.Disabled {
				t.Fatal("service review offered a quiet without recipient authority")
			}
		}
	}
}
