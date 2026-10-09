//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCreateSessionAttachesAmbientImplementRun(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))

	sqlDB := mustOpenWorkflowTestDB(t)
	store := store.NewSQL(sqlDB)
	wfReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	runStore := workflowpersistence.New(sqlDB)
	wfMgr := workflow.NewManager(runStore, store, wfReg, nil)

	dir := t.TempDir()
	projReg := project.NewSQLRegistry(sqlDB)
	p, err := project.CreateWithRoot(t.Context(), projReg, dir)
	testutil.FailErr(t, "reg.Create failed", err)
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: projReg,
		Workflows: wfMgr, WorkflowCatalog: workflowcatalog.Resolver{}, WorkflowRuns: runStore, ModuleRoot: root,
	}), nil, TestAPIToken)

	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", w.Code, w.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		testutil.FailErr(t, "decode session", err)
	}
	drainBackground(t, srv)
	run := waitAmbientActiveRun(t, wfMgr, sess.ID)
	if run.WorkflowID != "implement" || run.CurrentPhase != "boot" {
		t.Fatalf("run = %+v", run)
	}
	if run.StartMessageID != "" {
		t.Fatalf("ambient start_message_id = %q want empty until first visible user message", run.StartMessageID)
	}

	// attach_policy is derived at the HTTP boundary.
	getReq := newAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/workflow-runs/active", nil)
	getW := httptest.NewRecorder()
	srv.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("get active run status = %d body = %s", getW.Code, getW.Body.String())
	}
	var active wire.ActiveWorkflowRunResponse
	if err := json.Unmarshal(getW.Body.Bytes(), &active); err != nil {
		testutil.FailErr(t, "decode run", err)
	}
	if active.Run == nil {
		t.Fatalf("active run = null body = %s", getW.Body.String())
	}
	fetched := active.Run
	if fetched.AttachPolicy != string(workflowdef.AttachPolicySessionCreate) {
		t.Fatalf("HTTP run attach_policy = %q want %q (ambient implement must surface session_create so Den hides workflow chrome)",
			fetched.AttachPolicy, workflowdef.AttachPolicySessionCreate)
	}
}

func TestCreateSessionCatalogOmitsImplement(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := mustOpenWorkflowTestDB(t)
	store := store.NewSQL(sqlDB)
	wfReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	runStore := workflowpersistence.New(sqlDB)
	wfMgr := workflow.NewManager(runStore, store, wfReg, nil)

	dir := t.TempDir()
	projReg := project.NewSQLRegistry(sqlDB)
	p, err := project.CreateWithRoot(t.Context(), projReg, dir)
	testutil.FailErr(t, "reg.Create failed", err)
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: projReg,
		Workflows: wfMgr, WorkflowCatalog: workflowcatalog.Resolver{}, WorkflowRuns: runStore,
	}), nil, TestAPIToken)

	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", w.Code, w.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		testutil.FailErr(t, "decode session", err)
	}
	drainBackground(t, srv)

	listReq := newAuthedRequest(http.MethodGet, "/v1/workflows?session_id="+sess.ID, nil)
	listW := httptest.NewRecorder()
	srv.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list workflows status = %d body = %s", listW.Code, listW.Body.String())
	}
	var list wire.WorkflowListResponse
	if err := json.Unmarshal(listW.Body.Bytes(), &list); err != nil {
		testutil.FailErr(t, "decode summaries", err)
	}
	summaries := list.Workflows
	for _, s := range summaries {
		if s.ID == "implement" {
			t.Fatal("implement (attach.policy session_create) must not appear in product catalog API")
		}
	}
}

func mustOpenWorkflowTestDB(t *testing.T) db.ReadHandle {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "ambient-wf.db")
	return sqlDB
}

func waitAmbientActiveRun(t *testing.T, wfMgr *workflow.RunManager, sessionID string) *wire.WorkflowRun {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := wfMgr.Store.Runs.ActiveBySession(context.Background(), sessionID)
		testutil.FailErr(t, "GetActive", err)
		if run != nil {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected active implement workflow run after session create")
	return nil
}

func TestCreateSessionWithInvalidProjectWorkflowAttachesAmbientImplement(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))

	sqlDB := mustOpenWorkflowTestDB(t)
	store := store.NewSQL(sqlDB)
	wfReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	runStore := workflowpersistence.New(sqlDB)
	resolver := workflowcatalog.Resolver{
		ProjectTierApplies: func(context.Context, string) bool { return true },
	}
	wfMgr := workflow.NewManager(runStore, store, wfReg, nil)
	wfMgr.Resolver.ProjectTierApplies = resolver.ProjectTierApplies

	dir := t.TempDir()
	wfDir := filepath.Join(dir, settingsoverlay.DirName(), "workflows", "implement-dispatch")
	testutil.FailErr(t, "mkdir", os.MkdirAll(wfDir, 0o755))
	invalidYAML := `id: implement-dispatch
version: 1.0.0
extends: implement@1.0.0
controls:
  default_execution_mode: orchestrate
attach:
  policy: session_create
`
	testutil.FailErr(t, "write invalid workflow", os.WriteFile(filepath.Join(wfDir, "workflow.yaml"), []byte(invalidYAML), 0o644))

	projReg := project.NewSQLRegistry(sqlDB)
	p, err := project.CreateWithRoot(t.Context(), projReg, dir)
	testutil.FailErr(t, "reg.Create failed", err)
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: projReg,
		Workflows: wfMgr, WorkflowCatalog: resolver, WorkflowRuns: runStore, ModuleRoot: root,
	}), nil, TestAPIToken)

	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", w.Code, w.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		testutil.FailErr(t, "decode session", err)
	}
	drainBackground(t, srv)
	run := waitAmbientActiveRun(t, wfMgr, sess.ID)
	if run.WorkflowID != "implement" || run.CurrentPhase != "boot" {
		t.Fatalf("run = %+v, want implement in boot phase", run)
	}
}

func TestAbortSessionWithInvalidProjectWorkflowSucceeds(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := mustOpenWorkflowTestDB(t)
	store := store.NewSQL(sqlDB)
	wfReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	runStore := workflowpersistence.New(sqlDB)
	resolver := workflowcatalog.Resolver{
		ProjectTierApplies: func(context.Context, string) bool { return true },
	}
	wfMgr := workflow.NewManager(runStore, store, wfReg, nil)
	wfMgr.Resolver.ProjectTierApplies = resolver.ProjectTierApplies

	dir := t.TempDir()
	wfDir := filepath.Join(dir, settingsoverlay.DirName(), "workflows", "implement-dispatch")
	testutil.FailErr(t, "mkdir", os.MkdirAll(wfDir, 0o755))
	invalidYAML := `id: implement-dispatch
version: 1.0.0
extends: implement@1.0.0
controls:
  default_execution_mode: orchestrate
attach:
  policy: session_create
`
	testutil.FailErr(t, "write invalid workflow", os.WriteFile(filepath.Join(wfDir, "workflow.yaml"), []byte(invalidYAML), 0o644))

	projReg := project.NewSQLRegistry(sqlDB)
	p, err := project.CreateWithRoot(t.Context(), projReg, dir)
	testutil.FailErr(t, "reg.Create failed", err)

	mgr := session.NewManager(store, nil, nil, settings.DefaultSessionLimits())
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})
	mgr.SetSessionWorkflowStop(wfMgr)

	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store, Projects: projReg, Sessions: mgr,
		Workflows: wfMgr, WorkflowCatalog: resolver, WorkflowRuns: runStore,
	}), nil, TestAPIToken)

	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", w.Code, w.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		testutil.FailErr(t, "decode session", err)
	}
	drainBackground(t, srv)

	abortReq := newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/abort", strings.NewReader(`{"reason":"user stopped"}`))
	abortReq.Header.Set("Content-Type", "application/json")
	abortW := httptest.NewRecorder()
	srv.ServeHTTP(abortW, abortReq)
	if abortW.Code != http.StatusOK {
		t.Fatalf("abort session status = %d body = %s", abortW.Code, abortW.Body.String())
	}
}
