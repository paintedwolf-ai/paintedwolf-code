package sessioncontracts

import (
	"context"
	"encoding/json"
	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	wire "github.com/lycaon/lycaon/pkg/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCreateSessionAttachesAmbientImplementRun(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))

	sqlDB := contractfixture.MustOpenWorkflowTestDB(t)
	store := store.NewSQL(sqlDB)
	wfReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	runStore := workflowpersistence.New(sqlDB)
	wfMgr := workflow.NewManager(runStore, store, wfReg, nil)

	dir := t.TempDir()
	projReg := project.NewSQLRegistry(sqlDB)
	p, err := project.CreateWithRoot(t.Context(), projReg, dir)
	testutil.FailErr(t, "reg.Create failed", err)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: projReg}, Workflow: hostapi.WorkflowDependencies{
		Workflows: wfMgr, WorkflowCatalog: workflowcatalog.Resolver{}, WorkflowRuns: runStore}, Storage: hostapi.StorageDependencies{ModuleRoot: root}}), nil, hostapi.TestAPIToken)

	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
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
	contractfixture.DrainBackground(t, srv)
	run := contractfixture.WaitAmbientActiveRun(t, wfMgr, sess.ID)
	if run.WorkflowID != "implement" || run.CurrentPhase != "boot" {
		t.Fatalf("run = %+v", run)
	}
	if run.StartMessageID != "" {
		t.Fatalf("ambient start_message_id = %q want empty until first visible user message", run.StartMessageID)
	}

	// attach_policy is derived at the HTTP boundary.
	getReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/workflow-runs/active", nil)
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
	sqlDB := contractfixture.MustOpenWorkflowTestDB(t)
	store := store.NewSQL(sqlDB)
	wfReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	runStore := workflowpersistence.New(sqlDB)
	wfMgr := workflow.NewManager(runStore, store, wfReg, nil)

	dir := t.TempDir()
	projReg := project.NewSQLRegistry(sqlDB)
	p, err := project.CreateWithRoot(t.Context(), projReg, dir)
	testutil.FailErr(t, "reg.Create failed", err)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: projReg}, Workflow: hostapi.WorkflowDependencies{
		Workflows: wfMgr, WorkflowCatalog: workflowcatalog.Resolver{}, WorkflowRuns: runStore}}), nil, hostapi.TestAPIToken)

	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
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
	contractfixture.DrainBackground(t, srv)

	listReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/workflows?session_id="+sess.ID, nil)
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

func TestCreateSessionWithInvalidProjectWorkflowAttachesAmbientImplement(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))

	sqlDB := contractfixture.MustOpenWorkflowTestDB(t)
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
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: projReg}, Workflow: hostapi.WorkflowDependencies{
		Workflows: wfMgr, WorkflowCatalog: resolver, WorkflowRuns: runStore}, Storage: hostapi.StorageDependencies{ModuleRoot: root}}), nil, hostapi.TestAPIToken)

	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
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
	contractfixture.DrainBackground(t, srv)
	run := contractfixture.WaitAmbientActiveRun(t, wfMgr, sess.ID)
	if run.WorkflowID != "implement" || run.CurrentPhase != "boot" {
		t.Fatalf("run = %+v, want implement in boot phase", run)
	}
}

func TestAbortSessionWithInvalidProjectWorkflowSucceeds(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := contractfixture.MustOpenWorkflowTestDB(t)
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
	mgr.SetSessionWorkflowStop(wfMgr.Controls)

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store, Projects: projReg, Sessions: mgr}, Workflow: hostapi.WorkflowDependencies{
		Workflows: wfMgr, WorkflowCatalog: resolver, WorkflowRuns: runStore}}), nil, hostapi.TestAPIToken)

	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
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
	contractfixture.DrainBackground(t, srv)

	abortReq := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/abort", strings.NewReader(`{"reason":"user stopped"}`))
	abortReq.Header.Set("Content-Type", "application/json")
	abortW := httptest.NewRecorder()
	srv.ServeHTTP(abortW, abortReq)
	if abortW.Code != http.StatusOK {
		t.Fatalf("abort session status = %d body = %s", abortW.Code, abortW.Body.String())
	}
}
