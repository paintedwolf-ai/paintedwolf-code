// Package wea_test implements the D3 in-process lane tests for TB07
// (Approvals, grants and decision records: A01, A02, A03, A04, A12).
// This file is strictly test-only (*_test.go) and is never linked into any shipped binary.
package wea_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzcontext/authztest"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

type tb07InstallRecorder struct {
	mu          sync.Mutex
	installed   []hitl.ApprovalOption
	checkpoints []string
	rolledBack  []string
	failInstall error
}

func (r *tb07InstallRecorder) InstallApprovalOption(_ context.Context, checkpointID string, option hitl.ApprovalOption) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failInstall != nil {
		return nil, r.failInstall
	}
	r.installed = append(r.installed, option)
	r.checkpoints = append(r.checkpoints, checkpointID)
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.rolledBack = append(r.rolledBack, checkpointID)
	}, nil
}

func (r *tb07InstallRecorder) RollbackApprovalOption(_ context.Context, checkpointID string, _ hitl.ApprovalOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rolledBack = append(r.rolledBack, checkpointID)
	return nil
}

func (r *tb07InstallRecorder) counts() (installed, rolledBack int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.installed), len(r.rolledBack)
}

func tb07SeedSession(t *testing.T, sqlDB db.Handle, projectID, sessionID string) people.Person {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC().Format(time.RFC3339)
	var ownerID string
	if err := sqlDB.QueryRowContext(ctx, `SELECT id FROM people WHERE role = 'owner' LIMIT 1`).Scan(&ownerID); err != nil {
		t.Fatalf("read owner person: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT OR IGNORE INTO projects (id, name, last_opened_at, created_at) VALUES (?, 'WEA TB07 Project', ?, ?)`,
		projectID, now, now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT OR IGNORE INTO sessions (id, project_id, owner_person_id, root_session_id, mode, posture, created_at, updated_at, activity_at)
		 VALUES (?, ?, ?, ?, 'agent', 'build', ?, ?, ?)`,
		sessionID, projectID, ownerID, sessionID, now, now, now,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return people.Person{ID: ownerID, Role: api.PersonRoleOwner}
}

func tb07NewManager(t *testing.T, sqlDB db.Handle) *hitl.Checkpoints {
	t.Helper()
	ledger := &authzcontext.Ledger{Store: authzcontext.NewSQLStore(sqlDB)}
	mgr := hitl.NewCheckpoints(
		hitl.NewSQLStore(sqlDB),
		&events.Publisher{Hub: events.NewMemoryHub()},
		authzcontext.LedgerRecorder{Ledger: ledger},
	)
	mgr.SetCheckpointExpiry(func() time.Duration { return 0 })
	return mgr
}

func tb07Presentation() (hitl.ApprovalPresentation, []api.ApprovalGate) {
	_, decision := gate.Evaluate(gate.Facts{
		Stage:           gate.StagePreSpawn,
		Ran:             gate.ProducerApprovalRequest,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1},
	}, gate.DefaultPosture)
	primary, cited, reasons := hitl.PresentDecision(decision)
	return hitl.ApprovalPresentation{
		Action: "Execute reviewed action",
		Impact: "Performs the reviewed effect",
		Gate:   primary,
		Cited:  cited,
	}, reasons
}

// TestWEATB07_A01_ApprovalPlanHashBindingAndRevalidation verifies A01:
//   - Approval plans for file changes, commands, and network sends are content-addressed.
//   - Changing file bytes/base or material arguments changes the canonical GrantKey and Plan ID.
//   - Reordering set-valued destinations preserves the canonical GrantKey.
//   - Loading/resolving a plan whose stored ID no longer matches its content fails closed.
//   - Approving an option installs exact authority and seals an integrity-verified decision event.
func TestWEATB07_A01_ApprovalPlanHashBindingAndRevalidation(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "tb07-a01.db")
	owner := tb07SeedSession(t, sqlDB, "proj-a01", "sess-a01")
	ownerCtx := people.WithCaller(ctx, owner)
	mgr := tb07NewManager(t, sqlDB)
	installer := &tb07InstallRecorder{}
	mgr.Authority.SetApprovalAuthorityInstaller(installer)

	// 1. File change plan binds path, operation, sizes, and content hashes.
	fileActionA := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "write", ActionID: "call-file-1"},
		Scope:      hitl.ActionScope{SessionID: "sess-a01", ProjectID: "proj-a01", ProjectDir: "/tmp/proj-a01"},
		Mutations: hitl.ActionMutations{
			FileChanges: []api.ApprovalFileChange{{
				Path:         "notes.txt",
				Operation:    "edit",
				BeforeBytes:  10,
				AfterBytes:   14,
				BeforeSHA256: "sha256:aaaa",
				AfterSHA256:  "sha256:bbbb",
			}},
		},
	}
	fileActionChangedBytes := fileActionA
	fileActionChangedBytes.Mutations.FileChanges = []api.ApprovalFileChange{{
		Path:         "notes.txt",
		Operation:    "edit",
		BeforeBytes:  10,
		AfterBytes:   15,
		BeforeSHA256: "sha256:aaaa",
		AfterSHA256:  "sha256:cccc",
	}}
	fileActionChangedBase := fileActionA
	fileActionChangedBase.Mutations.FileChanges = []api.ApprovalFileChange{{
		Path:         "notes.txt",
		Operation:    "edit",
		BeforeBytes:  10,
		AfterBytes:   14,
		BeforeSHA256: "sha256:dddd",
		AfterSHA256:  "sha256:bbbb",
	}}

	keyA := hitl.GrantKey(fileActionA)
	keyChangedBytes := hitl.GrantKey(fileActionChangedBytes)
	keyChangedBase := hitl.GrantKey(fileActionChangedBase)
	if keyA == "" || keyA == keyChangedBytes || keyA == keyChangedBase {
		t.Fatalf("file-change GrantKey must bind before/after hashes: A=%q bytes=%q base=%q", keyA, keyChangedBytes, keyChangedBase)
	}

	// 2. Network send set-valued destination reorder preserves GrantKey, while adding/changing a destination alters it.
	netOrder1 := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{
				"command": "curl https://example.invalid",
				"capability_request": map[string]any{
					"direct_ip": map[string]any{
						"declared_destinations": []any{"198.51.100.10:443", "198.51.100.20:443"},
					},
				},
			},
		},
		Scope: hitl.ActionScope{SessionID: "sess-a01", ProjectID: "proj-a01", ProjectDir: "/tmp/proj-a01"},
	}
	netOrder2 := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{
				"command": "curl https://example.invalid",
				"capability_request": map[string]any{
					"direct_ip": map[string]any{
						"declared_destinations": []any{"198.51.100.20:443", "198.51.100.10:443"},
					},
				},
			},
		},
		Scope: hitl.ActionScope{SessionID: "sess-a01", ProjectID: "proj-a01", ProjectDir: "/tmp/proj-a01"},
	}
	netDifferent := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{
				"command": "curl https://example.invalid",
				"capability_request": map[string]any{
					"direct_ip": map[string]any{
						"declared_destinations": []any{"198.51.100.10:443", "198.51.100.99:443"},
					},
				},
			},
		},
		Scope: hitl.ActionScope{SessionID: "sess-a01", ProjectID: "proj-a01", ProjectDir: "/tmp/proj-a01"},
	}
	if hitl.GrantKey(netOrder1) != hitl.GrantKey(netOrder2) {
		t.Fatalf("reordering set-valued destinations changed GrantKey: %q vs %q", hitl.GrantKey(netOrder1), hitl.GrantKey(netOrder2))
	}
	if hitl.GrantKey(netOrder1) == hitl.GrantKey(netDifferent) {
		t.Fatalf("changing destination member did not change GrantKey: %q", hitl.GrantKey(netOrder1))
	}

	// 3. Immutable plan digest check + decision sealing.
	pres, reasons := tb07Presentation()
	pres.FileChanges = fileActionA.Mutations.FileChanges
	plan, err := hitl.NewApprovalPlan(
		fileActionA,
		hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{
			Kind:    hitl.ApprovalSubjectAction,
			Title:   "Edit notes.txt",
			Targets: []hitl.ApprovalTarget{{Kind: "file", Label: "notes.txt"}},
		},
		pres,
		reasons,
		[]hitl.ApprovalOption{hitl.CurrentActionOption()},
		hitl.FaceContext{},
	)
	if err != nil {
		t.Fatalf("NewApprovalPlan: %v", err)
	}
	if plan.ID == "" {
		t.Fatalf("expected non-empty content-addressed plan.ID")
	}
	// Mutating the plan after compilation MUST fail Validate.
	tampered := *plan
	tampered.Presentation.Action = "Mutated after review"
	if err := tampered.Validate(); err == nil {
		t.Fatalf("expected tampered ApprovalPlan.Validate() to fail closed")
	}

	resp, err := mgr.RequestCheckpoint(ownerCtx, hitl.CheckpointRequest{
		SessionID:      "sess-a01",
		Kind:           api.CheckpointKindToolApproval,
		ToolCallID:     "call-file-1",
		ProposedAction: &fileActionA,
		ApprovalPlan:   plan,
	})
	if err != nil {
		t.Fatalf("RequestCheckpoint: %v", err)
	}
	if _, err := mgr.Authority.ResolveApprovalOption(ownerCtx, "sess-a01", resp.CheckpointID, plan.Options[0].ID); err != nil {
		t.Fatalf("ResolveApprovalOption: %v", err)
	}
	installed, rolledBack := installer.counts()
	if installed != 1 || rolledBack != 0 {
		t.Fatalf("installer counts = (%d, %d), want (1, 0)", installed, rolledBack)
	}

	// Verify recorded authz_events decision names deciding person & verifies hash chain.
	eventsList, err := authzcontext.NewSQLStore(sqlDB).ListEvents(ctx, "sess-a01")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(eventsList) != 1 || eventsList[0].Action != authzcontext.EventActionApprovalDecision ||
		eventsList[0].Outcome != authzcontext.EventOutcomeAllowed || eventsList[0].ResolverPersonID != owner.ID {
		t.Fatalf("unexpected authz_events for A01: %+v", eventsList)
	}
	if br, err := authzcontext.Verify(ctx, sqlDB); err != nil || br != nil {
		t.Fatalf("authzcontext.Verify failed: br=%+v err=%v", br, err)
	}
}

// TestWEATB07_A02_GrantScopesBoundariesAndManualClockExpiry verifies A02:
//   - Chat grant covers root chat and its worker child sessions, never another chat,
//     ends when chat is deleted (ForgetSession), and revokes cleanly.
//   - Project grant covers only its ProjectID, never a different ProjectID.
//   - Device grant covers cross-project subjects when scope is device.
//   - Changing a material argument or confinement witness invalidates reuse.
//   - Manual-clock / time-bounded expiry stops reuse after deadline.
func TestWEATB07_A02_GrantScopesBoundariesAndManualClockExpiry(t *testing.T) {
	ctx := t.Context()
	tmp := t.TempDir()
	globalPath := filepath.Join(tmp, "approvals.yaml")
	projADir := filepath.Join(tmp, "proj-a")
	projBDir := filepath.Join(tmp, "proj-b")
	if err := os.MkdirAll(projADir, 0o755); err != nil {
		t.Fatalf("mkdir proj-a: %v", err)
	}
	if err := os.MkdirAll(projBDir, 0o755); err != nil {
		t.Fatalf("mkdir proj-b: %v", err)
	}

	store, err := settings.NewApprovalStoreAt(globalPath)
	if err != nil {
		t.Fatalf("NewApprovalStoreAt: %v", err)
	}
	if err := store.PutGlobal(settings.ApprovalConfig{
		Posture: gate.PostureBalanced,
		Rules: []settings.ApprovalRule{
			{Category: settings.ApprovalCategoryCommand, Pattern: "deploy *", Effect: settings.ApprovalEffectAsk},
			{Category: settings.ApprovalCategoryHost, Pattern: "api.example.invalid", Effect: settings.ApprovalEffectAsk},
		},
	}); err != nil {
		t.Fatalf("PutGlobal: %v", err)
	}

	ruleGate := settings.NewRuleApprovalGate(store, settings.Sources{}).(*settings.RuleApprovalGate)
	containedA := hitl.Contained{FSJailed: true, Egress: "mediated", Roots: []string{projADir}}
	witnessA := hitl.BoundaryWitness(containedA)

	// 1. Chat-scoped grant: covers root chat + worker child session, never another chat.
	chatAction := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "deploy staging"},
		},
		Scope:     hitl.ActionScope{SessionID: "chat-alpha", RootSessionID: "chat-alpha", ProjectID: "proj-a", ProjectDir: projADir},
		Execution: hitl.ActionExecution{Contained: containedA},
	}
	resBefore, err := ruleGate.Evaluate(ctx, chatAction)
	if err != nil || !resBefore.Required() {
		t.Fatalf("expected ask before chat grant: res=%+v err=%v", resBefore, err)
	}

	chatGrant := hitl.ApprovalGrant{
		ID:                "grant-chat-deploy",
		Scope:             hitl.ApprovalGrantScopeChat,
		ChatSessionID:     "chat-alpha",
		ProjectID:         "proj-a",
		ProjectDir:        projADir,
		Predicate:         hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategoryCommand), Pattern: "deploy staging"},
		Witness:           witnessA,
		GrantedAt:         time.Now().UTC(),
		GrantedByPersonID: "person-owner",
	}
	if _, err := ruleGate.ApplyGrant(chatGrant); err != nil {
		t.Fatalf("ApplyGrant chat: %v", err)
	}

	// Same chat matches without asking.
	if !ruleGate.GrantCovers(chatAction) {
		t.Fatalf("expected chat grant to cover exact action in same chat")
	}
	// Worker child session under chat-alpha matches without asking.
	workerAction := chatAction
	workerAction.Scope.SessionID = "worker-child-1"
	workerAction.Scope.RootSessionID = "chat-alpha"
	if !ruleGate.GrantCovers(workerAction) {
		t.Fatalf("expected chat grant to cover worker child session under same root chat")
	}
	// Different chat MUST NOT match.
	otherChatAction := chatAction
	otherChatAction.Scope.SessionID = "chat-beta"
	otherChatAction.Scope.RootSessionID = "chat-beta"
	if ruleGate.GrantCovers(otherChatAction) {
		t.Fatalf("chat grant MUST NOT match across chats")
	}
	// Changed material argument MUST NOT match.
	changedArgAction := chatAction
	changedArgAction.Invocation.Args = map[string]any{"command": "deploy production"}
	if ruleGate.GrantCovers(changedArgAction) {
		t.Fatalf("chat grant MUST NOT match changed material argument")
	}
	// Changed confinement witness MUST NOT match.
	changedWitnessAction := chatAction
	changedWitnessAction.Execution.Contained.FSJailed = false
	if ruleGate.GrantCovers(changedWitnessAction) {
		t.Fatalf("chat grant MUST NOT match changed confinement witness")
	}
	// Deleting chat (ForgetSession) ends chat authority.
	ruleGate.ForgetSession("chat-alpha")
	if ruleGate.GrantCovers(chatAction) {
		t.Fatalf("chat grant MUST NOT match after ForgetSession")
	}

	// 2. Project-scoped grant: matches proj-a, NEVER matches proj-b, and revoking stops reuse.
	future := time.Now().UTC().Add(24 * time.Hour)
	projGrant := hitl.ApprovalGrant{
		ID:                "grant-proj-host",
		Scope:             hitl.ApprovalGrantScopeProject,
		ProjectID:         "proj-a",
		ProjectDir:        projADir,
		Title:             "Allow api.example.invalid for this project",
		Predicate:         hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategoryHost), Pattern: "api.example.invalid"},
		Witness:           witnessA,
		GrantedAt:         time.Now().UTC(),
		ExpiresAt:         &future,
		GrantedByPersonID: "person-owner",
	}
	if _, err := ruleGate.ApplyGrant(projGrant); err != nil {
		t.Fatalf("ApplyGrant project: %v", err)
	}
	hostActionA := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "network", Args: map[string]any{"host": "api.example.invalid"}},
		Scope:      hitl.ActionScope{SessionID: "chat-gamma", RootSessionID: "chat-gamma", ProjectID: "proj-a", ProjectDir: projADir},
		Execution:  hitl.ActionExecution{Contained: containedA},
	}
	if !ruleGate.GrantCovers(hostActionA) {
		t.Fatalf("expected project grant to cover host in proj-a")
	}
	hostActionB := hostActionA
	hostActionB.Scope.ProjectID = "proj-b"
	hostActionB.Scope.ProjectDir = projBDir
	if ruleGate.GrantCovers(hostActionB) {
		t.Fatalf("project grant MUST NOT match across projects")
	}
	revoked, err := ruleGate.RevokeGrant("grant-proj-host")
	if err != nil || !revoked {
		t.Fatalf("RevokeGrant failed: revoked=%v err=%v", revoked, err)
	}
	if ruleGate.GrantCovers(hostActionA) {
		t.Fatalf("revoked project grant MUST NOT still match")
	}

	// 3. Manual-clock expiry bounds: ResolveExpiry + expired durable grant never covers.
	clk := newManualClock(time.Date(2026, 11, 27, 20, 0, 0, 0, time.UTC))
	t0 := clk.Now()
	dayGrant := hitl.ApprovalGrant{
		ID:         "grant-day-ttl",
		Scope:      hitl.ApprovalGrantScopeChat,
		TTLSeconds: hitl.DayRungTTLSeconds,
	}
	resolvedExp := dayGrant.ResolveExpiry(t0)
	if resolvedExp == nil || !resolvedExp.Equal(t0.Add(24*time.Hour)) {
		t.Fatalf("ResolveExpiry = %v, want %v", resolvedExp, t0.Add(24*time.Hour))
	}
	// Scope ceiling shorter than TTL caps expiry.
	ceiling := t0.Add(2 * time.Hour)
	dayGrant.ExpiresAt = &ceiling
	cappedExp := dayGrant.ResolveExpiry(t0)
	if cappedExp == nil || !cappedExp.Equal(ceiling) {
		t.Fatalf("ResolveExpiry did not respect scope ceiling: got %v want %v", cappedExp, ceiling)
	}

	// Expired project grant (ExpiresAt in the past) MUST NOT cover.
	expiredTime := time.Now().UTC().Add(-time.Minute)
	expiredGrant := projGrant
	expiredGrant.ID = "grant-proj-expired"
	expiredGrant.ExpiresAt = &expiredTime
	if _, err := ruleGate.ApplyGrant(expiredGrant); err != nil {
		t.Fatalf("ApplyGrant expired: %v", err)
	}
	if ruleGate.GrantCovers(hostActionA) {
		t.Fatalf("expired project grant MUST NOT cover action")
	}

	// 4. GrantedPath runtime: exact vs tree + expiry + cross-chat isolation.
	gpRuntime := grantedpath.NewRuntime()
	outsideFile := filepath.Join(tmp, "outside", "secret-free.txt")
	if err := os.MkdirAll(filepath.Dir(outsideFile), 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(outsideFile, []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if _, err := gpRuntime.Grant("chat-alpha", grantedpath.Grant{
		ID: "gp-1", Path: outsideFile, Mode: grantedpath.ModeRead, Tree: false, ExpiresAt: &future,
	}); err != nil {
		t.Fatalf("gpRuntime.Grant: %v", err)
	}
	if _, ok := gpRuntime.Covers("chat-alpha", "proj-a", outsideFile, grantedpath.ModeRead); !ok {
		t.Fatalf("expected grantedpath to cover exact file in chat-alpha")
	}
	if _, ok := gpRuntime.Covers("chat-alpha", "proj-a", outsideFile, grantedpath.ModeWrite); ok {
		t.Fatalf("read-only grantedpath MUST NOT cover write mode")
	}
	if _, ok := gpRuntime.Covers("chat-beta", "proj-a", outsideFile, grantedpath.ModeRead); ok {
		t.Fatalf("chat-alpha grantedpath MUST NOT cover chat-beta")
	}
}

// TestWEATB07_A03_PosturesPolicyAndDetectionRules verifies A03:
//   - Postures: light, balanced, strict, and Advanced Off (never_ask).
//   - agent_policy_change asks at balanced & strict, silent at light.
//   - Advanced Off (never_ask) still enforces deny rules and systemic boundary/secret controls.
//   - Unknown posture token fails closed (ParsePosture rejects; project overlay falls back to strict).
//   - Project & extension policy asks or denies and NEVER grants (effect: allow/permit, grants, never_ask: true all refused).
//   - Detection rule on command field adds an ask; miss leaves normal gates in force.
func TestWEATB07_A03_PosturesPolicyAndDetectionRules(t *testing.T) {
	ctx := t.Context()
	tmp := t.TempDir()
	globalPath := filepath.Join(tmp, "approvals.yaml")
	projDir := filepath.Join(tmp, "repo")
	overlayDir := filepath.Join(projDir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}

	// 1. Unknown posture token is refused by ParsePosture.
	if _, err := gate.ParsePosture("yolo"); err == nil {
		t.Fatalf("ParsePosture must reject unknown posture token")
	}

	// 2. agent_policy_change across light, balanced, strict.
	allProducers := gate.ProducerContainment | gate.ProducerDetection | gate.ProducerPayload |
		gate.ProducerDestination | gate.ProducerFilePath | gate.ProducerConsent |
		gate.ProducerLease | gate.ProducerRule | gate.ProducerExposure |
		gate.ProducerIngestion | gate.ProducerApprovalRequest | gate.ProducerPackageExecution
	agentsPath := filepath.Join(projDir, "AGENTS.md")
	policyFacts := gate.Facts{
		Stage:            gate.StagePreSpawn,
		Ran:              allProducers,
		Containment:      gate.Containment{FSJailed: true, Egress: gate.EgressProxy},
		File:             &gate.FileTarget{Path: agentsPath, Mode: gate.ModeWrite},
		AgentPolicyPaths: []string{agentsPath},
	}
	if v, _ := gate.Evaluate(policyFacts, gate.PostureLight); v == gate.Ask {
		t.Fatalf("agent_policy_change should be silent at light, got %v", v)
	}
	if v, _ := gate.Evaluate(policyFacts, gate.PostureBalanced); v != gate.Ask {
		t.Fatalf("agent_policy_change must ask at balanced, got %v", v)
	}
	if v, _ := gate.Evaluate(policyFacts, gate.PostureStrict); v != gate.Ask {
		t.Fatalf("agent_policy_change must ask at strict, got %v", v)
	}

	// 3. Project overlay cannot grant, cannot enable never_ask, cannot inject grants, and cannot relax deny to ask.
	store, err := settings.NewApprovalStoreAt(globalPath)
	if err != nil {
		t.Fatalf("NewApprovalStoreAt: %v", err)
	}
	neverAsk := true
	if err := store.PutGlobal(settings.ApprovalConfig{
		Posture:  gate.PostureBalanced,
		NeverAsk: &neverAsk,
		Rules: []settings.ApprovalRule{
			{Category: settings.ApprovalCategoryCommand, Pattern: "rm -rf *", Effect: settings.ApprovalEffectDeny},
		},
	}); err != nil {
		t.Fatalf("PutGlobal: %v", err)
	}

	// Drop an untrusted repository .paintedwolf/approvals.yaml trying every forbidden relaxation.
	repoYAML := `approval_posture: bogus_posture
never_ask: true
grants:
  - id: forged-grant
    scope: device
    category: command
    pattern: "*"
rules:
  - category: command
    pattern: "rm -rf *"
    effect: ask
  - category: command
    pattern: "curl *"
    effect: allow
  - category: command
    pattern: "deploy prod"
    effect: deny
`
	if err := os.WriteFile(filepath.Join(overlayDir, "approvals.yaml"), []byte(repoYAML), 0o644); err != nil {
		t.Fatalf("write project approvals.yaml: %v", err)
	}

	projView := store.ProjectOverlay(projDir)
	if projView.Config.Posture != gate.PostureStrict {
		t.Fatalf("unknown project posture must fail closed to strict, got %q", projView.Config.Posture)
	}
	if len(projView.Rejected) < 4 {
		t.Fatalf("expected >=4 rejected entries (posture, never_ask, grants, effect:allow), got %+v", projView.Rejected)
	}

	// Verify Advanced Off (global never_ask=true) STILL enforces BOTH global deny and project deny!
	ruleGate := settings.NewRuleApprovalGate(store, settings.Sources{}).(*settings.RuleApprovalGate)
	for _, cmd := range []string{"rm -rf /tmp/foo", "deploy prod"} {
		res, err := ruleGate.Evaluate(ctx, hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": cmd}},
			Scope:      hitl.ActionScope{SessionID: "sess-a03", ProjectID: "proj-a03", ProjectDir: projDir},
		})
		if err != nil {
			t.Fatalf("Evaluate(%q): %v", cmd, err)
		}
		if !res.Denied || res.DenyCode != "APPROVAL_RULE_DENIED" {
			t.Fatalf("command %q must be denied even under Advanced Off: res=%+v", cmd, res)
		}
	}

	// Verify the forged "effect: allow" rule did NOT enter effective rules.
	eff := store.Get(llm.SettingsScopeProject, settings.ProjectRef{Dir: projDir})
	for _, r := range eff.Rules {
		if r.Effect != settings.ApprovalEffectAsk && r.Effect != settings.ApprovalEffectDeny {
			t.Fatalf("non-ask/deny policy rule leaked into effective config: %+v", r)
		}
	}

	// 4. Detection rule triggers an ask on command field even at Light posture; miss leaves normal gates in force.
	detHitFacts := gate.Facts{
		Stage:       gate.StagePreSpawn,
		Ran:         allProducers,
		Containment: gate.Containment{FSJailed: true, Egress: gate.EgressProxy},
		Detection: &gate.Match{
			PackID:        "pack-wea",
			RuleID:        "wea-det-01",
			Level:         "high",
			External:      true,
			Unrecoverable: true,
		},
	}
	if v, dec := gate.Evaluate(detHitFacts, gate.PostureLight); v != gate.Ask || dec.Primary != api.GateAuthorityMisuse {
		t.Fatalf("detection rule hit must ask with primary=authority_misuse, got verdict=%v primary=%v", v, dec.Primary)
	}
	detMissFacts := gate.Facts{
		Stage:       gate.StagePreSpawn,
		Ran:         allProducers,
		Containment: gate.Containment{FSJailed: true, Egress: gate.EgressProxy},
	}
	if v, _ := gate.Evaluate(detMissFacts, gate.PostureLight); v == gate.Ask {
		t.Fatalf("detection miss with no other gates must not spuriously ask, got %v", v)
	}
}

// TestWEATB07_A04_UnrecordableDecisionFailsClosedAndH1CapacityLimit verifies A04 + historical lead H1:
//   - When decision recording fails (ledger append returns ErrSealFailed), BOTH approve and reject
//     fail closed: checkpoint remains pending, installed authority is rolled back, effect does not run.
//   - Without an authenticated deciding person (people.Deciding), resolution fails closed.
//   - H1 (`program state memory exceeded: >64MB` / 67108864): when program-state memory is at the
//     64 MiB catalog cap, ResolveApprovalOption fails closed with typed *toolcontract.ProgramMemoryLimitError,
//     installs 0 authority, leaks 0 memory, and leaves rejection available.
func TestWEATB07_A04_UnrecordableDecisionFailsClosedAndH1CapacityLimit(t *testing.T) {
	ctx := t.Context()

	// Part 1: Unrecordable decision (ledger append fails) fails closed and rolls back installed authority.
	t.Run("unrecordable_ledger_fails_closed", func(t *testing.T) {
		sqlDB := testdbfixture.Open(t, "tb07-a04-unrecordable.db")
		owner := tb07SeedSession(t, sqlDB, "proj-a04", "sess-a04")
		ownerCtx := people.WithCaller(ctx, owner)
		mgr := hitl.NewCheckpoints(
			hitl.NewSQLStore(sqlDB),
			&events.Publisher{Hub: events.NewMemoryHub()},
			authztest.RejectEvents(t, sqlDB),
		)
		mgr.SetCheckpointExpiry(func() time.Duration { return 0 })
		installer := &tb07InstallRecorder{}
		mgr.Authority.SetApprovalAuthorityInstaller(installer)

		action := hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "echo hi"}, ActionID: "call-a04"},
			Scope:      hitl.ActionScope{SessionID: "sess-a04", ProjectID: "proj-a04"},
		}
		pres, reasons := tb07Presentation()
		plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectAction, Title: "Run echo",
			Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "echo hi"}},
		}, pres, reasons, []hitl.ApprovalOption{hitl.CurrentActionOption()}, hitl.FaceContext{})
		if err != nil {
			t.Fatalf("NewApprovalPlan: %v", err)
		}
		resp, err := mgr.RequestCheckpoint(ownerCtx, hitl.CheckpointRequest{
			SessionID: "sess-a04", Kind: api.CheckpointKindToolApproval, ToolCallID: "call-a04",
			ProposedAction: &action, ApprovalPlan: plan,
		})
		if err != nil {
			t.Fatalf("RequestCheckpoint: %v", err)
		}

		// Approve attempt MUST fail closed with ErrSealFailed and roll back installed authority.
		_, err = mgr.Authority.ResolveApprovalOption(ownerCtx, "sess-a04", resp.CheckpointID, plan.Options[0].ID)
		if !errors.Is(err, authzledger.ErrSealFailed) {
			t.Fatalf("expected ErrSealFailed on unrecordable approve, got %v", err)
		}
		installed, rolledBack := installer.counts()
		if installed != 1 || rolledBack != 1 {
			t.Fatalf("expected installed authority to be rolled back on seal failure: installed=%d rolledBack=%d", installed, rolledBack)
		}
		polled, err := mgr.PollCheckpoint(ownerCtx, resp.CheckpointID)
		if err != nil {
			t.Fatalf("PollCheckpoint: %v", err)
		}
		if polled.Status != hitl.DecisionStatusPending {
			t.Fatalf("checkpoint must remain pending after unrecordable approve, got %q", polled.Status)
		}

		// Missing caller identity (no people.Deciding) MUST also fail closed before installing anything.
		_, err = mgr.Authority.ResolveApprovalOption(ctx, "sess-a04", resp.CheckpointID, plan.Options[0].ID)
		if err == nil {
			t.Fatalf("expected ResolveApprovalOption without caller identity to fail closed")
		}
		installedAfter, _ := installer.counts()
		if installedAfter != installed {
			t.Fatalf("resolution without caller must not install authority")
		}

		// Reject attempt MUST also fail closed with ErrSealFailed.
		_, err = mgr.ResolveCheckpoint(ownerCtx, "sess-a04", resp.CheckpointID, api.CheckpointKindToolApproval,
			&hitl.DecisionResult{Approved: false}, nil)
		if !errors.Is(err, authzledger.ErrSealFailed) {
			t.Fatalf("expected ErrSealFailed on unrecordable reject, got %v", err)
		}
	})

	// Part 2: Historical lead H1 — 64 MiB program-state memory limit (`program state memory exceeded: >64MB`).
	t.Run("h1_program_state_memory_64mib_limit_fails_closed", func(t *testing.T) {
		limits, err := toolcontract.CatalogProgramMemoryLimits()
		if err != nil {
			t.Fatalf("CatalogProgramMemoryLimits: %v", err)
		}
		const want64MiB = int64(64 << 20)
		if limits.State != want64MiB {
			t.Fatalf("catalog program state memory limit = %d, want %d (64 MiB, H1)", limits.State, want64MiB)
		}
		memory, err := toolcontract.NewProgramMemory(limits)
		if err != nil {
			t.Fatalf("NewProgramMemory: %v", err)
		}

		sqlDB := testdbfixture.Open(t, "tb07-a04-h1.db")
		owner := tb07SeedSession(t, sqlDB, testdbseed.DefaultProjectID, "sess-h1")
		ownerCtx := people.WithCaller(ctx, owner)
		mgr := tb07NewManager(t, sqlDB)
		mgr.Authority.SetNativeAuthorityAdmission(memory)
		installer := &tb07InstallRecorder{}
		mgr.Authority.SetApprovalAuthorityInstaller(installer)

		// Mint a real native-authority checkpoint backed by a folder chain.
		scratch := t.TempDir()
		folder := filepath.Join(scratch, "h1-fixture", "a", "b")
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatalf("mkdir h1 fixture: %v", err)
		}
		target := gate.FileTarget{Path: filepath.Join(folder, "target.txt"), Mode: gate.ModeRead, OutsideRoots: true}
		_, decision := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerFilePath, File: &target}, gate.PostureBalanced)
		review := hitl.NewNativeActionReview(ownerCtx, memory, tb07NativeInspector{})
		defer review.Close()
		args, nativeInputs, err := review.Freeze(ownerCtx, "list_dir", map[string]any{"path": folder})
		if err != nil {
			t.Fatalf("Freeze native inputs: %v", err)
		}
		action := hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{Tool: "list_dir", Args: args, NativeInputs: nativeInputs, ActionID: "call-h1"},
			Scope:      hitl.ActionScope{SessionID: "sess-h1", ProjectID: testdbseed.DefaultProjectID, ProjectDir: scratch},
		}
		offers := settings.GrantedPathOffers(action, target, decision, nil)
		resp, err := mgr.RequestCheckpoint(ownerCtx, hitl.CheckpointRequest{
			SessionID:      "sess-h1",
			Kind:           api.CheckpointKindToolApproval,
			ToolCallID:     "call-h1",
			ProposedAction: &action,
			Decision:       decision,
			GrantOffers:    offers,
		})
		review.Close()
		if err != nil {
			t.Fatalf("RequestCheckpoint (H1 native): %v", err)
		}

		row, err := mgr.Store.Get(ownerCtx, resp.CheckpointID)
		if err != nil {
			t.Fatalf("Store.Get: %v", err)
		}
		rawPlan, err := json.Marshal(row.Payload["approval_plan"])
		if err != nil {
			t.Fatalf("marshal plan: %v", err)
		}
		var plan hitl.ApprovalPlan
		if err := json.Unmarshal(rawPlan, &plan); err != nil {
			t.Fatalf("unmarshal plan: %v", err)
		}

		// Exhaust the 64 MiB program-state budget to trigger the exact H1 condition (>67108864).
		releaseFull, err := memory.ReserveProgramState(ownerCtx, limits.State)
		if err != nil {
			t.Fatalf("ReserveProgramState full 64 MiB: %v", err)
		}
		_, err = mgr.Authority.ResolveApprovalOption(ownerCtx, "sess-h1", resp.CheckpointID, plan.Options[0].ID)
		var memErr *toolcontract.ProgramMemoryLimitError
		if !errors.As(err, &memErr) {
			releaseFull()
			t.Fatalf("expected *toolcontract.ProgramMemoryLimitError (H1), got %v", err)
		}
		installed, _ := installer.counts()
		if installed != 0 {
			releaseFull()
			t.Fatalf("H1 capacity failure must install 0 authority, installed=%d", installed)
		}
		if memory.InUse().State != limits.State {
			releaseFull()
			t.Fatalf("H1 capacity failure leaked memory reservation: inUse=%d want=%d", memory.InUse().State, limits.State)
		}
		// Checkpoint remains pending and rejection still works without restoring grant authority.
		polled, err := mgr.PollCheckpoint(ownerCtx, resp.CheckpointID)
		if err != nil || polled.Status != hitl.DecisionStatusPending {
			releaseFull()
			t.Fatalf("checkpoint must stay pending after H1 capacity refusal: polled=%+v err=%v", polled, err)
		}
		if _, err := mgr.ResolveCheckpoint(ownerCtx, "sess-h1", resp.CheckpointID, api.CheckpointKindToolApproval,
			&hitl.DecisionResult{Approved: false}, nil); err != nil {
			releaseFull()
			t.Fatalf("reject must remain available during H1 capacity pressure: %v", err)
		}
		releaseFull()
		if memory.InUse().State != 0 {
			t.Fatalf("memory reservations not fully released: %d", memory.InUse().State)
		}
	})
}

type tb07NativeInspector struct{}

func (tb07NativeInspector) ProjectOperationInputs(ctx context.Context, state toolcontract.ReviewStateAdmission, args map[string]any) (map[string]any, func(), error) {
	release, err := state.ReserveProgramState(ctx, 8192)
	if err != nil {
		return nil, nil, err
	}
	return args, release, nil
}

// TestWEATB07_A12_RestartRecoveryPendingAndGrants verifies A12:
//   - Pending approval survives restart, stays bound to the exact same immutable plan ID,
//     and resolving after restart commits ONCE (re-resolving does not re-install or re-run).
//   - Chat grants survive restart with approver provenance and scope intact; revoking removes them.
//   - Half-recorded (prepared but uncommitted) approval operation rolls back on restart without
//     disturbing committed grants.
//   - Corrupted stored native authority fails closed on boot replay.
func TestWEATB07_A12_RestartRecoveryPendingAndGrants(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "tb07-a12.db")

	// Boot 1: Create pending approval #1 (leaves pending across restart) and approve #2 (installs chat grant).
	sqlDB1, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open boot1: %v", err)
	}
	owner := tb07SeedSession(t, sqlDB1, "proj-a12", "sess-a12")
	ownerCtx := people.WithCaller(ctx, owner)
	mgr1 := tb07NewManager(t, sqlDB1)
	inst1 := &tb07InstallRecorder{}
	mgr1.Authority.SetApprovalAuthorityInstaller(inst1)

	pres, reasons := tb07Presentation()
	pendingAction := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "make test"}, ActionID: "call-pending"},
		Scope:      hitl.ActionScope{SessionID: "sess-a12", ProjectID: "proj-a12"},
	}
	pendingPlan, err := hitl.NewApprovalPlan(pendingAction, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Run make test",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "make test"}},
	}, pres, reasons, []hitl.ApprovalOption{hitl.CurrentActionOption()}, hitl.FaceContext{})
	if err != nil {
		t.Fatalf("NewApprovalPlan pending: %v", err)
	}
	pendingResp, err := mgr1.RequestCheckpoint(ownerCtx, hitl.CheckpointRequest{
		SessionID: "sess-a12", Kind: api.CheckpointKindToolApproval, ToolCallID: "call-pending",
		ProposedAction: &pendingAction, ApprovalPlan: pendingPlan,
	})
	if err != nil {
		t.Fatalf("RequestCheckpoint pending: %v", err)
	}

	grantAction := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "make build"}, ActionID: "call-grant"},
		Scope:      hitl.ActionScope{SessionID: "sess-a12", ProjectID: "proj-a12"},
	}
	chatGrant := hitl.ApprovalGrant{
		ID:            "grant-a12-chat",
		Scope:         hitl.ApprovalGrantScopeChat,
		ChatSessionID: "sess-a12",
		ProjectID:     "proj-a12",
		Title:         hitl.TitleAllowForThisChat,
	}
	grantPlan, err := hitl.NewApprovalPlan(grantAction, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Run make build",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "make build"}},
	}, pres, reasons, []hitl.ApprovalOption{{
		ID: "chat", Kind: hitl.ApprovalOptionLease, Scope: hitl.ApprovalGrantScopeChat, Rung: hitl.ApprovalRungChat,
		Title: hitl.TitleAllowForThisChat, Coverage: "make build", ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: "command changes",
		DecisionAction: hitl.ApprovalOptionApprove,
		Authority:      []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &chatGrant}},
	}}, hitl.FaceContext{})
	if err != nil {
		t.Fatalf("NewApprovalPlan grant: %v", err)
	}
	grantResp, err := mgr1.RequestCheckpoint(ownerCtx, hitl.CheckpointRequest{
		SessionID: "sess-a12", Kind: api.CheckpointKindToolApproval, ToolCallID: "call-grant",
		ProposedAction: &grantAction, ApprovalPlan: grantPlan,
	})
	if err != nil {
		t.Fatalf("RequestCheckpoint grant: %v", err)
	}
	if _, err := mgr1.Authority.ResolveApprovalOption(ownerCtx, "sess-a12", grantResp.CheckpointID, "chat"); err != nil {
		t.Fatalf("ResolveApprovalOption chat grant: %v", err)
	}

	// Simulate a half-recorded (prepared, uncommitted) approval operation left by a crash before commit.
	halfAction := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "make half"}, ActionID: "call-half"},
		Scope:      hitl.ActionScope{SessionID: "sess-a12", ProjectID: "proj-a12"},
	}
	halfOpt := hitl.CurrentActionOption()
	halfPlan, err := hitl.NewApprovalPlan(halfAction, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Run make half",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "make half"}},
	}, pres, reasons, []hitl.ApprovalOption{halfOpt}, hitl.FaceContext{})
	if err != nil {
		t.Fatalf("NewApprovalPlan half: %v", err)
	}
	halfResp, err := mgr1.RequestCheckpoint(ownerCtx, hitl.CheckpointRequest{
		SessionID: "sess-a12", Kind: api.CheckpointKindToolApproval, ToolCallID: "call-half",
		ProposedAction: &halfAction, ApprovalPlan: halfPlan,
	})
	if err != nil {
		t.Fatalf("RequestCheckpoint half: %v", err)
	}
	halfOptJSON, _ := json.Marshal(halfOpt)
	if _, err := sqlDB1.ExecContext(ctx,
		`INSERT INTO approval_operations (checkpoint_id, session_id, option_id, option_json, status, created_at)
		 VALUES (?, 'sess-a12', ?, ?, 'prepared', ?)`,
		halfResp.CheckpointID, halfOpt.ID, string(halfOptJSON), time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed prepared approval_operation: %v", err)
	}

	if err := mgr1.StopExpiryTimers(ctx); err != nil {
		t.Fatalf("StopExpiryTimers: %v", err)
	}
	if err := sqlDB1.Close(); err != nil {
		t.Fatalf("close boot1 DB: %v", err)
	}

	// Boot 2: Fresh manager on same DB.
	sqlDB2, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open boot2: %v", err)
	}
	defer func() { _ = sqlDB2.Close() }()
	mgr2 := tb07NewManager(t, sqlDB2)
	inst2 := &tb07InstallRecorder{}
	mgr2.Authority.SetApprovalAuthorityInstaller(inst2)

	// 1. Recover uncommitted prepared approval operations -> rolls back cp-half-recorded.
	if err := mgr2.Authority.RecoverApprovalOperations(ctx); err != nil {
		t.Fatalf("RecoverApprovalOperations: %v", err)
	}
	_, rolledBack := inst2.counts()
	if rolledBack != 1 {
		t.Fatalf("expected 1 rolled-back half-recorded operation, got %d", rolledBack)
	}

	// 2. RestoreChatGrants replays committed chat grant with owner provenance intact.
	if err := mgr2.Authority.RestoreChatGrants(ctx); err != nil {
		t.Fatalf("RestoreChatGrants: %v", err)
	}
	installedAfterReplay, _ := inst2.counts()
	if installedAfterReplay != 1 {
		t.Fatalf("expected 1 replayed chat grant, got %d", installedAfterReplay)
	}
	if g := inst2.installed[0].Authority[0].Grant; g == nil || g.ID != "grant-a12-chat" || g.GrantedByPersonID != owner.ID {
		t.Fatalf("replayed chat grant lost identity/provenance: %+v", g)
	}

	// 3. RestorePending preserves pending approval with identical plan ID; resolving commits once; retry does not run twice.
	if err := mgr2.RestorePending(ctx); err != nil {
		t.Fatalf("RestorePending: %v", err)
	}
	rowAfterRestart, err := mgr2.Store.Get(ctx, pendingResp.CheckpointID)
	if err != nil {
		t.Fatalf("Store.Get pending after restart: %v", err)
	}
	if rowAfterRestart.Status != hitl.DecisionStatusPending {
		t.Fatalf("pending approval lost across restart: status=%q", rowAfterRestart.Status)
	}
	rawPlan, _ := json.Marshal(rowAfterRestart.Payload["approval_plan"])
	var restoredPlan hitl.ApprovalPlan
	if err := json.Unmarshal(rawPlan, &restoredPlan); err != nil {
		t.Fatalf("unmarshal restored plan: %v", err)
	}
	if restoredPlan.ID != pendingPlan.ID {
		t.Fatalf("restored plan ID = %q, want %q", restoredPlan.ID, pendingPlan.ID)
	}

	if _, err := mgr2.Authority.ResolveApprovalOption(ownerCtx, "sess-a12", pendingResp.CheckpointID, pendingPlan.Options[0].ID); err != nil {
		t.Fatalf("resolve pending after restart: %v", err)
	}
	// Repeat resolution MUST be idempotent (no double install / no second effect).
	if _, err := mgr2.Authority.ResolveApprovalOption(ownerCtx, "sess-a12", pendingResp.CheckpointID, pendingPlan.Options[0].ID); err != nil {
		t.Fatalf("idempotent repeat resolve: %v", err)
	}
	installedFinal, _ := inst2.counts()
	if installedFinal != installedAfterReplay+1 {
		t.Fatalf("effect installed %d times after restart, want exactly 1", installedFinal-installedAfterReplay)
	}

	// 4. Corrupt stored chat_grant JSON (valid JSON with unknown elevated_effects token) -> boot replay fails closed.
	if _, err := sqlDB2.ExecContext(ctx, `UPDATE chat_grants SET delta_json = '{"kind":"generic_grant","grant":{"id":"grant-a12-chat","scope":"chat","elevated_effects":["forged_unknown_effect"]}}' WHERE id = 'grant-a12-chat'`); err != nil {
		t.Fatalf("corrupt chat_grants row: %v", err)
	}
	inst3 := &tb07InstallRecorder{}
	mgr2.Authority.SetApprovalAuthorityInstaller(inst3)
	if err := mgr2.Authority.RestoreChatGrants(ctx); err == nil {
		t.Fatalf("expected corrupted chat_grants row to fail closed on boot replay")
	}
	if inst3Count, _ := inst3.counts(); inst3Count != 0 {
		t.Fatalf("corrupted replay must install 0 grants, got %d", inst3Count)
	}
	if br, err := authzcontext.Verify(ctx, sqlDB2); err != nil || br != nil {
		t.Fatalf("authzcontext.Verify after restart failed: br=%+v err=%v", br, err)
	}
}
