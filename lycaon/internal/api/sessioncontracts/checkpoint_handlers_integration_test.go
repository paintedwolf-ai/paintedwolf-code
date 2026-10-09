package sessioncontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointHandlersListAndResolve(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "chk-api.db")

	reg := project.NewSQLRegistry(sqlDB)
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Create failed", err)

	store := store.NewSQL(sqlDB)
	sess, err := store.Create(t.Context(), wire.CreateSessionRequest{
		Posture:   wire.SessionPostureBuild,
		ProjectID: p.ID,
	}, p.ID)
	testutil.FailErr(t, "create session in store", err)

	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), pub, authzcontext.SQLRecorder(sqlDB))

	srv := api.NewServer(apitest.Dependencies(t, api.Dependencies{Core: api.CoreDependencies{
		Store: store, Projects: reg, Sessions: session.NewManager(store, nil, nil, settings.DefaultSessionLimits())}, Approvals: api.ApprovalsDependencies{Checkpoints: mgr}}), nil, api.TestAPIToken)

	dec, err := contractfixture.RequestExplicitAPIApprovalCheckpoint(t, mgr, hitl.CheckpointRequest{
		SessionID:      sess.ID,
		Kind:           wire.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "command"},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/sessions/"+sess.ID+"/checkpoints", nil)
	api.WithTestAuth(req)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", w.Code, w.Body.String())
	}
	var res wire.CheckpointListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	listed := res.Checkpoints
	if len(listed) != 1 || listed[0].ID != dec.CheckpointID {
		t.Fatalf("listed = %+v", listed)
	}
	if listed[0].ToolApproval == nil || listed[0].ToolApproval.Plan.Presentation.Tool != "command" {
		t.Fatalf("listed tool_approval = %+v", listed[0].ToolApproval)
	}

	contractfixture.AssertCheckpointReadSurfaces(t, srv, sess.ID, dec.CheckpointID)

	body := `{"kind":"tool_approval","action":"approve","option_id":"approve_current_action"}`
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/"+sess.ID+"/checkpoints/"+dec.CheckpointID, strings.NewReader(body))
	api.WithTestAuth(req)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve status = %d body = %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/"+sess.ID+"/checkpoints/unknown", strings.NewReader(body))
	api.WithTestAuth(req)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d", w.Code)
	}
}

func TestCheckpointHandlersRejectWithGuidance(t *testing.T) {
	srv, mgr, sess := contractfixture.NewCheckpointHandlerFixture(t)

	dec, err := contractfixture.RequestExplicitAPIApprovalCheckpoint(t, mgr, hitl.CheckpointRequest{
		SessionID:      sess.ID,
		Kind:           wire.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "write", Args: map[string]any{"path": "a.txt"}},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/"+sess.ID+"/checkpoints/"+dec.CheckpointID,
		strings.NewReader(`{"kind":"tool_approval","action":"reject","guidance":"Use the read tool instead."}`))
	api.WithTestAuth(req)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reject status = %d body = %s", w.Code, w.Body.String())
	}
	var rejected wire.CheckpointResponse
	if err := json.Unmarshal(w.Body.Bytes(), &rejected); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if rejected.Status != wire.CheckpointStatusRejected {
		t.Fatalf("status = %q want rejected", rejected.Status)
	}
	if rejected.Result["guidance"] != "Use the read tool instead." {
		t.Fatalf("guidance = %#v", rejected.Result["guidance"])
	}
}

func TestCheckpointHandlersValidationErrors(t *testing.T) {
	srv, mgr, sess := contractfixture.NewCheckpointHandlerFixture(t)

	dec, err := contractfixture.RequestExplicitAPIApprovalCheckpoint(t, mgr, hitl.CheckpointRequest{
		SessionID:      sess.ID,
		Kind:           wire.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "command"},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"invalid action", `{"kind":"tool_approval","action":"maybe"}`, http.StatusBadRequest},
		{"approve without option", `{"kind":"tool_approval","action":"approve"}`, http.StatusBadRequest},
		{"guidance with approve", `{"kind":"tool_approval","action":"approve","guidance":"do something else"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/"+sess.ID+"/checkpoints/"+dec.CheckpointID, strings.NewReader(tc.body))
			api.WithTestAuth(req)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
			}
			pending, err := mgr.ListPending(t.Context(), sess.ID, nil)
			if err != nil || len(pending) != 1 || pending[0].ID != dec.CheckpointID {
				t.Fatalf("checkpoint should remain pending after validation error: pending=%v err=%v", pending, err)
			}
		})
	}
}

func TestCheckpointHandlersResolveExactReplay(t *testing.T) {
	srv, mgr, sess := contractfixture.NewCheckpointHandlerFixture(t)

	dec, err := contractfixture.RequestExplicitAPIApprovalCheckpoint(t, mgr, hitl.CheckpointRequest{
		SessionID:      sess.ID,
		Kind:           wire.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "command"},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)
	body := `{"kind":"tool_approval","action":"approve","option_id":"approve_current_action"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/"+sess.ID+"/checkpoints/"+dec.CheckpointID, strings.NewReader(body))
	api.WithTestAuth(req)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first resolve status = %d", w.Code)
	}

	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/"+sess.ID+"/checkpoints/"+dec.CheckpointID, strings.NewReader(body))
	api.WithTestAuth(req)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("replay resolve status = %d want 200", w.Code)
	}
}

func TestCheckpointGrantOfferCreatesProjectLease(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	tmp := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, tmp)
	svc, err := settings.NewService()
	testutil.FailErr(t, "settings.NewService", err)

	sqlDB := testdbfixture.OpenPath(t, filepath.Join(tmp, "chk-grant.db"))

	reg := project.NewSQLRegistry(sqlDB)
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "CreateWithRoot", err)
	store := store.NewSQL(sqlDB)
	sess, err := store.Create(t.Context(), wire.CreateSessionRequest{
		Posture:   wire.SessionPostureBuild,
		ProjectID: p.ID,
	}, p.ID)
	testutil.FailErr(t, "create session", err)

	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	chkMgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), pub, authzcontext.SQLRecorder(sqlDB))

	approvals := settings.NewRuleApprovalGate(svc.Approvals, settings.NoSources())
	srv := api.NewServer(apitest.Dependencies(t, api.Dependencies{Core: api.CoreDependencies{
		Store: store, Projects: reg, Sessions: session.NewManager(store, nil, nil, settings.DefaultSessionLimits()),
		Settings: svc}, Host: api.HostDependencies{Events: hub}, Approvals: api.ApprovalsDependencies{Checkpoints: chkMgr, ApprovalGate: approvals}}), nil, api.TestAPIToken)

	action := hitl.ProposedAction{
		Tool: "network", Args: map[string]any{"host": "api.example.test"},
		ProjectID: p.ID, ProjectDir: dir, SessionID: sess.ID, Contained: hitl.ContainedForRequest(confine.Request{Roots: []string{dir}}),
	}
	decision := &gate.Decision{Primary: wire.GateUserRule, Cited: []gate.Fact{{
		Gate: wire.GateUserRule, Key: "rule.pattern", Value: "api.example.test", Source: "test_fixture",
	}}}
	offers := approvals.GrantOffers(action, &hitl.ApprovalResult{Decision: decision})
	var selected hitl.ApprovalGrantOffer
	for _, offer := range offers {
		if offer.Scope == hitl.ApprovalGrantScopeProject {
			selected = offer
		}
	}
	if selected.ID == "" {
		t.Fatalf("missing project grant offer: %+v", offers)
	}
	dec, err := chkMgr.RequestCheckpoint(t.Context(), hitl.CheckpointRequest{
		SessionID:      sess.ID,
		Kind:           wire.CheckpointKindToolApproval,
		ToolCallID:     "call-grant",
		ProposedAction: &action,
		GrantOffers:    offers,
		Decision:       decision,
	})
	testutil.FailErr(t, "RequestCheckpoint", err)

	body := `{"kind":"tool_approval","action":"approve","option_id":"` + selected.ID + `"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/"+sess.ID+"/checkpoints/"+dec.CheckpointID, strings.NewReader(body))
	api.WithTestAuth(req)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve status = %d body = %s", w.Code, w.Body.String())
	}

	listReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/approval-grants?session_id="+sess.ID, nil)
	api.WithTestAuth(listReq)
	listW := httptest.NewRecorder()
	srv.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list grants status = %d body = %s", listW.Code, listW.Body.String())
	}
	var grants wire.ApprovalGrantsResponse
	if err := json.Unmarshal(listW.Body.Bytes(), &grants); err != nil {
		testutil.FailErr(t, "decode grants", err)
	}
	found := false
	for _, g := range grants.Grants {
		if g.ID == selected.ID && g.Pattern == selected.Grant.Predicate.Pattern && g.Scope == wire.ApprovalGrantScopeProject {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected grant %q in %+v", selected.ID, grants.Grants)
	}
}

func TestWriteRootPlanAtomicallyCreatesTaskAndDeviceAuthority(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	tmp := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, tmp)
	svc, err := settings.NewService()
	testutil.FailErr(t, "settings.NewService", err)

	sqlDB := testdbfixture.OpenPath(t, filepath.Join(tmp, "chk-wr.db"))

	reg := project.NewSQLRegistry(sqlDB)
	projectDir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, projectDir)
	testutil.FailErr(t, "CreateWithRoot", err)
	store := store.NewSQL(sqlDB)
	sess, err := store.Create(t.Context(), wire.CreateSessionRequest{
		Posture:   wire.SessionPostureBuild,
		ProjectID: p.ID,
	}, p.ID)
	testutil.FailErr(t, "create session", err)

	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	chkMgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), pub, authzcontext.SQLRecorder(sqlDB))

	approvals := settings.NewRuleApprovalGate(svc.Approvals, settings.NoSources())
	writeRootRT := approvalstate.NewSandboxPathGrantRuntime()
	srv := api.NewServer(apitest.Dependencies(t, api.Dependencies{Core: api.CoreDependencies{
		Store: store, Projects: reg, Sessions: session.NewManager(store, nil, nil, settings.DefaultSessionLimits()),
		Settings: svc}, Host: api.HostDependencies{Events: hub}, Approvals: api.ApprovalsDependencies{Checkpoints: chkMgr, ApprovalGate: approvals,
		Authority: capabilityadmin.Authority{WriteRoots: writeRootRT}}}), nil, api.TestAPIToken)

	proposed := filepath.Join(tmp, "shared-cache")
	testutil.FailErr(t, "create proposed write root", os.MkdirAll(proposed, 0o755))
	action := hitl.ProposedAction{
		Tool: "write_root", Args: map[string]any{"proposed_write_root": proposed},
		ProjectID: p.ID, ProjectDir: projectDir, SessionID: sess.ID, Contained: hitl.ContainedForRequest(confine.Request{Roots: []string{projectDir}}),
	}
	_, writeDecision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn,
		Ran: gate.ProducerContainment | gate.ProducerDetection | gate.ProducerConsent |
			gate.ProducerFilePath | gate.ProducerLease | gate.ProducerRule,
		UserRule: &gate.UserRule{Category: "write_root", Pattern: proposed, Subject: proposed},
	}, gate.PostureLight)
	offers := approvals.GrantOffers(action, &hitl.ApprovalResult{Decision: writeDecision})
	var selected hitl.ApprovalGrantOffer
	for _, offer := range offers {
		if offer.Scope == hitl.ApprovalGrantScopeDevice {
			selected = offer
		}
	}
	if selected.ID == "" {
		t.Fatalf("missing device write-root offer: %+v", offers)
	}
	chatGrant := hitl.ApprovalGrant{
		ID: "grant_task_write_root", Scope: hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryWriteRoot, Pattern: proposed},
		ChatSessionID: sess.ID, Title: hitl.TitleAllowForThisChat,
	}
	primaryGate, cited, reasons := hitl.PresentDecision(writeDecision)
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectWriteRootSet, Title: "Allow write access",
		Targets: []hitl.ApprovalTarget{{Kind: "write_root", Label: proposed}},
	}, hitl.ApprovalPresentation{
		Action: "Sandbox write access", Impact: "Allow writes under this root.",
		Gate: primaryGate, Cited: cited,
	}, reasons, []hitl.ApprovalOption{{
		ID: selected.ID, Kind: hitl.ApprovalOptionLease, Scope: selected.Scope,
		Title: selected.Title, Coverage: selected.Coverage, ExpiresWhen: selected.ExpiresWhen,
		ReaskWhen: selected.ReaskWhen, Rung: selected.Rung, DecisionAction: hitl.ApprovalOptionApprove,
		Authority: []hitl.ApprovalAuthorityDelta{
			{Kind: hitl.AuthorityWriteRootChat, Grant: &chatGrant, ChatSessionID: sess.ID, WriteRoots: []string{proposed}},
			{Kind: hitl.AuthorityGenericGrant, Grant: &selected.Grant},
		},
	}}, hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	dec, err := chkMgr.RequestCheckpoint(t.Context(), hitl.CheckpointRequest{
		SessionID: sess.ID, Kind: wire.CheckpointKindToolApproval, Type: hitl.DecisionTypeApprove,
		ToolCallID: "call-wr", ProposedAction: &action, ApprovalPlan: plan,
	})
	testutil.FailErr(t, "RequestCheckpoint", err)

	body := `{"kind":"tool_approval","action":"approve","option_id":"` + selected.ID + `"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/"+sess.ID+"/checkpoints/"+dec.CheckpointID, strings.NewReader(body))
	api.WithTestAuth(req)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve status = %d body = %s", w.Code, w.Body.String())
	}

	if roots := writeRootRT.SessionWriteRoots(sess.ID); len(roots) != 1 || roots[0] != proposed {
		t.Fatalf("task write roots = %v want [%s]", roots, proposed)
	}

	if roots := svc.Approvals.WriteRootsForProject(p.ID); len(roots) != 1 || roots[0] != proposed {
		t.Fatalf("WriteRootsForProject = %v want [%s]", roots, proposed)
	}

	listReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/approval-grants", nil)
	api.WithTestAuth(listReq)
	listW := httptest.NewRecorder()
	srv.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list grants status = %d body = %s", listW.Code, listW.Body.String())
	}
	var grants wire.ApprovalGrantsResponse
	if err := json.Unmarshal(listW.Body.Bytes(), &grants); err != nil {
		testutil.FailErr(t, "decode grants", err)
	}
	found, foundTask := false, false
	for _, g := range grants.Grants {
		if g.ID == selected.ID && g.Pattern == proposed && g.Category == wire.ApprovalGrantCategoryWriteRoot {
			found = true
		}
		if g.ID == chatGrant.ID && g.Pattern == proposed && g.Scope == wire.ApprovalGrantScopeChat {
			foundTask = true
		}
	}
	if !found || !foundTask {
		t.Fatalf("expected durable %q and task %q write_root grants in %+v", selected.ID, chatGrant.ID, grants.Grants)
	}

	revokeBody := `{"ids":["` + chatGrant.ID + `","` + selected.ID + `"]}`
	revokeReq := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/approval-grants/revoke", strings.NewReader(revokeBody))
	api.WithTestAuth(revokeReq)
	revokeReq.Header.Set("Content-Type", "application/json")
	revokeW := httptest.NewRecorder()
	srv.ServeHTTP(revokeW, revokeReq)
	if revokeW.Code != http.StatusOK {
		t.Fatalf("revoke status = %d body = %s", revokeW.Code, revokeW.Body.String())
	}
	if roots := writeRootRT.SessionWriteRoots(sess.ID); len(roots) != 0 {
		t.Fatalf("task write roots after revoke = %v", roots)
	}
	if roots := svc.Approvals.WriteRootsForProject(p.ID); len(roots) != 0 {
		t.Fatalf("durable write roots after revoke = %v", roots)
	}
}

func TestCheckpointRejectsClientAuthoredGrantPredicate(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	srv, chkMgr, sess := contractfixture.NewCheckpointHandlerFixture(t)
	dec, err := contractfixture.RequestExplicitAPIApprovalCheckpoint(t, chkMgr, hitl.CheckpointRequest{
		SessionID:      sess.ID,
		Kind:           wire.CheckpointKindToolApproval,
		Type:           hitl.DecisionTypeApprove,
		ToolCallID:     "call-client-predicate",
		ProposedAction: &hitl.ProposedAction{Tool: "write"},
	})
	testutil.FailErr(t, "RequestCheckpoint", err)

	body := `{"kind":"tool_approval","action":"approve","grant":{"predicate":{"category":"tool","pattern":"*"}}}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/"+sess.ID+"/checkpoints/"+dec.CheckpointID, strings.NewReader(body))
	api.WithTestAuth(req)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("client-authored authority status = %d want 400 body = %s", w.Code, w.Body.String())
	}
}
