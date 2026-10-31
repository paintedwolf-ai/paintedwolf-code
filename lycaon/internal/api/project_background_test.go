package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type projectBackgroundTestEnv struct {
	srv       *Server
	bgReg     *bgprocess.Registry
	hub       *events.MemoryHub
	store     session.Store
	projects  project.Registry
	sessions  *session.Host
	setPorts  func(handle string, addrs ...netip.AddrPort)
}

func newProjectBackgroundTestEnv(t *testing.T) *projectBackgroundTestEnv {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")
	st := store.NewSQL(sqlDB)
	projReg := project.NewSQLRegistry(sqlDB)
	hub := events.NewMemoryHub()
	publisher := &events.Publisher{Hub: hub}

	reg := tools.NewStubRegistry()
	mock := llm.NewMockProvider(testMockConfig(t))
	mgr := session.NewHost(st, session.Models{
		Client:   mock,
		Provider: nil,
		Limits:   settings.DefaultSessionLimits(),
		Cost:     nil,
	}, reg)
	mgr.SetDataDir(t.TempDir())
	mgr.SetProjectRegistry(projReg)
	mgr.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: reg})
	wireTestBindingRegistry(t)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())

	var (
		portMu    sync.Mutex
		mockPorts = make(map[string][]netip.AddrPort)
	)
	bgReg := bgprocess.NewRegistry(bgprocess.Config{
		MaxBackground:        8,
		MaxRecent:            4,
		SweepInterval:        time.Hour,
		OutputCoalesceWindow: 20 * time.Millisecond,
		ListenerObserver: func(_ context.Context) ([]bgprocess.ObservedListener, error) {
			portMu.Lock()
			defer portMu.Unlock()
			var out []bgprocess.ObservedListener
			for handle, addrs := range mockPorts {
				for _, addr := range addrs {
					out = append(out, bgprocess.ObservedListener{
						Addr:   addr,
						Handle: handle,
					})
				}
			}
			return out, nil
		},
	}, bgprocess.Hooks{
		Complete: func(ctx context.Context, c bgprocess.Completion) {
			mgr.Processes.HandleCommandCompletion(ctx, c)
		},
		Publish:        publisher.PublishProcess,
		PublishProject: publisher.PublishProjectBackground,
	})
	t.Cleanup(func() { _ = bgReg.Lifecycle.Close(context.Background()) })
	mgr.SetBackgroundRegistry(bgReg)

	srv := newServerForTest(t, Dependencies{
		Core: CoreDependencies{
			Database:      sqlDB,
			Store:         st,
			Projects:      projReg,
			Sessions:      mgr,
			PersonActions: personactions.New(sqlDB),
		},
		Host: HostDependencies{
			Events:         hub,
			EventPublisher: publisher,
		},
	})

	return &projectBackgroundTestEnv{
		srv:      srv,
		bgReg:    bgReg,
		hub:      hub,
		store:    st,
		projects: projReg,
		sessions: mgr,
		setPorts: func(handle string, addrs ...netip.AddrPort) {
			portMu.Lock()
			mockPorts[handle] = addrs
			portMu.Unlock()
		},
	}
}

func TestProjectBackgroundListLiveHydrationAndIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh/sleep fixture is unix-oriented")
	}
	env := newProjectBackgroundTestEnv(t)
	ctx := t.Context()

	// Configure secret screening on background process command lines.
	matcher := secretmatch.NewInertMatcher()
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Secret:         "super-secret-token-xyz",
			RuleID:         secretmatch.ManagedRuleID,
			Title:          secretmatch.ManagedRuleTitle,
			Source:         secretmatch.SourceRememberedMatch,
			NonDisclosable: true,
		}}
	})
	env.bgReg.Output.SetCaptureProjector(captureprojection.New(matcher, nil))

	rootADir := resolveTestPath(t, t.TempDir())
	subDirA := filepath.Join(rootADir, "services", "web")
	testutil.FailErr(t, "mkdir subDirA", os.MkdirAll(subDirA, 0o755))
	rootBDir := resolveTestPath(t, t.TempDir())
	otherDir := resolveTestPath(t, t.TempDir())

	proj1, err := env.projects.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{
			{Path: rootADir, IsPrimary: ptrBoolForTest(true)},
			{Path: rootBDir},
		},
	})
	testutil.FailErr(t, "create proj1", err)
	rootA := proj1.Roots[0]
	rootB := proj1.Roots[1]

	proj2, err := env.projects.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{
			{Path: otherDir, IsPrimary: ptrBoolForTest(true)},
		},
	})
	testutil.FailErr(t, "create proj2", err)

	sess1, err := env.store.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj1.ID)
	testutil.FailErr(t, "create sess1", err)
	testutil.FailErr(t, "name sess1", env.store.UpdateSession(ctx, sess1.ID, func(s *wire.Session) {
		s.Title = "Initial Chat Title"
	}))

	sess2, err := env.store.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj1.ID)
	testutil.FailErr(t, "create sess2", err)
	archivedAt := time.Now().UTC()
	testutil.FailErr(t, "archive sess2", env.store.UpdateSession(ctx, sess2.ID, func(s *wire.Session) {
		s.Title = "Archived Chat"
		s.ArchivedAt = &archivedAt
	}))

	sessOther, err := env.store.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj2.ID)
	testutil.FailErr(t, "create sessOther", err)

	runner := hostcmd.NewRunner()

	// 1. Running background job in sess1 under rootA/services/web with a secret in command.
	h1, err := env.bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sess1.ID, ProjectID: proj1.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("serve --token super-secret-token-xyz"),
			ProjectDir: subDirA,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start h1", err)
	env.setPorts(h1, netip.MustParseAddrPort("127.0.0.1:5173"))
	env.bgReg.RefreshListeners(ctx)

	// 2. Running background job in archived sess2 under rootB.
	h2, err := env.bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sess2.ID, ProjectID: proj1.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("worker-daemon"),
			ProjectDir: rootBDir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start h2", err)

	// 3. Unpromoted JobModeAwaited job in sess1 (must NOT appear in project background list).
	_, err = env.bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sess1.ID, ProjectID: proj1.ID, Runner: runner, Mode: bgprocess.JobModeAwaited,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("inlineawaited"),
			ProjectDir: rootADir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start inline awaited", err)

	// 4. Short-lived background job in sess1 that exits cleanly (appears as recently ended).
	hEnded, err := env.bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sess1.ID, ProjectID: proj1.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("one-shot-migration"),
			ProjectDir: rootADir,
			Stages:     []exec.Stage{{Name: "sh", Args: []string{"-c", "exit 0"}}},
		},
	})
	testutil.FailErr(t, "start hEnded", err)
	done, err := env.bgReg.Lifecycle.Await(ctx, sess1.ID, hEnded, 5*time.Second)
	testutil.FailErr(t, "await hEnded", err)
	if !done {
		t.Fatal("expected hEnded to finish")
	}

	// 5. Running background job in proj2 (must NOT appear in proj1's list).
	hOther, err := env.bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sessOther.ID, ProjectID: proj2.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("other-project-server"),
			ProjectDir: otherDir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start hOther", err)

	// Call GET /v1/projects/{proj1.ID}/background
	listRec := httptest.NewRecorder()
	env.srv.ServeHTTP(listRec, newAuthedRequest(http.MethodGet, "/v1/projects/"+proj1.ID+"/background", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	var listResp wire.ProjectBackgroundProcessListResponse
	testutil.FailErr(t, "decode list response", json.Unmarshal(listRec.Body.Bytes(), &listResp))
	if listResp.Revision <= 0 {
		t.Fatalf("expected positive revision, got %d", listResp.Revision)
	}
	if len(listResp.Processes) != 3 {
		t.Fatalf("expected 3 processes (h1, h2, hEnded), got %d: %+v", len(listResp.Processes), listResp.Processes)
	}

	byID := make(map[string]wire.ProjectBackgroundProcess, len(listResp.Processes))
	for _, p := range listResp.Processes {
		if p.ProjectID != proj1.ID {
			t.Fatalf("unexpected project_id %q in proj1 list", p.ProjectID)
		}
		if p.ProcessID == hOther {
			t.Fatal("proj1 list leaked proj2 background process")
		}
		byID[p.ProcessID] = p
	}

	got1, ok := byID[h1]
	if !ok {
		t.Fatalf("missing h1 from list: %+v", byID)
	}
	if !got1.Running || got1.SessionID != sess1.ID || got1.SessionTitle != "Initial Chat Title" || got1.SessionArchived {
		t.Fatalf("h1 metadata mismatch: %+v", got1)
	}
	if got1.RootID != rootA.ID || got1.WorkingDir != filepath.Join("services", "web") {
		t.Fatalf("h1 root/working_dir = (%q, %q), want (%q, %q)", got1.RootID, got1.WorkingDir, rootA.ID, filepath.Join("services", "web"))
	}
	if strings.Contains(got1.Command, "super-secret-token-xyz") {
		t.Fatalf("h1 command was not secret-screened: %q", got1.Command)
	}
	if len(got1.Ports) != 1 || got1.Ports[0] != "127.0.0.1:5173" {
		t.Fatalf("h1 ports = %v, want [127.0.0.1:5173]", got1.Ports)
	}

	got2, ok := byID[h2]
	if !ok {
		t.Fatalf("missing h2 from list: %+v", byID)
	}
	if !got2.Running || got2.SessionID != sess2.ID || got2.SessionTitle != "Archived Chat" || !got2.SessionArchived {
		t.Fatalf("h2 metadata mismatch: %+v", got2)
	}
	if got2.RootID != rootB.ID || got2.WorkingDir != "." {
		t.Fatalf("h2 root/working_dir = (%q, %q), want (%q, \".\")", got2.RootID, got2.WorkingDir, rootB.ID)
	}

	gotEnded, ok := byID[hEnded]
	if !ok {
		t.Fatalf("missing hEnded from list: %+v", byID)
	}
	if gotEnded.Running || gotEnded.EndedAt == nil || gotEnded.ExitCode == nil || *gotEnded.ExitCode != 0 || gotEnded.TerminationReason != wire.BackgroundProcessTerminationReasonExited {
		t.Fatalf("hEnded state mismatch: %+v", gotEnded)
	}

	// Live hydration check: rename sess1 and archive it, then re-query GET /v1/projects/{proj1.ID}/background.
	now := time.Now().UTC()
	testutil.FailErr(t, "rename and archive sess1", env.store.UpdateSession(ctx, sess1.ID, func(s *wire.Session) {
		s.Title = "Renamed Live Title"
		s.ArchivedAt = &now
	}))

	listRec2 := httptest.NewRecorder()
	env.srv.ServeHTTP(listRec2, newAuthedRequest(http.MethodGet, "/v1/projects/"+proj1.ID+"/background", nil))
	if listRec2.Code != http.StatusOK {
		t.Fatalf("list2 status = %d", listRec2.Code)
	}
	var listResp2 wire.ProjectBackgroundProcessListResponse
	testutil.FailErr(t, "decode list2 response", json.Unmarshal(listRec2.Body.Bytes(), &listResp2))
	for _, p := range listResp2.Processes {
		if p.ProcessID == h1 {
			if p.SessionTitle != "Renamed Live Title" || !p.SessionArchived {
				t.Fatalf("live hydration did not reflect updated session title/archived state: %+v", p)
			}
		}
	}

	// Verify SessionSummary.BackgroundRunning on GET /v1/projects/{proj1.ID}/sessions?archived=true.
	sessListRec := httptest.NewRecorder()
	env.srv.ServeHTTP(sessListRec, newAuthedRequest(http.MethodGet, "/v1/projects/"+proj1.ID+"/sessions?archived=true", nil))
	if sessListRec.Code != http.StatusOK {
		t.Fatalf("sessions list status = %d body=%s", sessListRec.Code, sessListRec.Body.String())
	}
	var sessListResp wire.SessionListPage
	testutil.FailErr(t, "decode sessions list", json.Unmarshal(sessListRec.Body.Bytes(), &sessListResp))
	if len(sessListResp.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessListResp.Sessions))
	}
	for _, summary := range sessListResp.Sessions {
		if summary.BackgroundRunning != 1 {
			t.Fatalf("session %s background_running = %d, want 1", summary.ID, summary.BackgroundRunning)
		}
	}

	// Verify GET /v1/projects/{proj1.ID}/removal-assessment populates background_processes.
	assessRec := httptest.NewRecorder()
	env.srv.ServeHTTP(assessRec, newAuthedRequest(http.MethodGet, "/v1/projects/"+proj1.ID+"/removal-assessment", nil))
	if assessRec.Code != http.StatusOK {
		t.Fatalf("removal-assessment status = %d body=%s", assessRec.Code, assessRec.Body.String())
	}
	var assessment wire.ProjectRemovalAssessment
	testutil.FailErr(t, "decode removal-assessment", json.Unmarshal(assessRec.Body.Bytes(), &assessment))
	if len(assessment.BackgroundProcesses) != 2 {
		t.Fatalf("removal-assessment background_processes = %+v, want 2 live processes", assessment.BackgroundProcesses)
	}

	// Verify cross-project active background command query returns all 3 running jobs across proj1 and proj2.
	allRunning, err := env.sessions.ProjectControl.AllRunningBackgroundProcesses(ctx)
	testutil.FailErr(t, "AllRunningBackgroundProcesses", err)
	if len(allRunning) != 3 {
		t.Fatalf("AllRunningBackgroundProcesses = %d, want 3 (h1, h2, hOther)", len(allRunning))
	}
}

func TestProjectBackgroundStopRecordsPersonActionAndNotifiesOwningChat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	env := newProjectBackgroundTestEnv(t)
	ctx := t.Context()

	dir1 := resolveTestPath(t, t.TempDir())
	dir2 := resolveTestPath(t, t.TempDir())
	proj1 := createProjectForTest(t, env.srv, dir1)
	proj2 := createProjectForTest(t, env.srv, dir2)

	sess1, err := env.store.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj1.ID)
	testutil.FailErr(t, "create sess1", err)
	sess2, err := env.store.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj2.ID)
	testutil.FailErr(t, "create sess2", err)

	runner := hostcmd.NewRunner()
	h1, err := env.bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sess1.ID, ProjectID: proj1.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		OriginTool: "command",
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("proj1-server"),
			ProjectDir: dir1,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start h1", err)

	h2, err := env.bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sess2.ID, ProjectID: proj2.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		OriginTool: "command",
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("proj2-server"),
			ProjectDir: dir2,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start h2", err)

	// Cross-project stop attempt: proj1 trying to stop h2 (which belongs to proj2) must 404.
	crossRec := httptest.NewRecorder()
	env.srv.ServeHTTP(crossRec, newAuthedRequest(http.MethodPost, "/v1/projects/"+proj1.ID+"/background/"+h2+"/stop", nil))
	assertErrorResponse(t, crossRec, http.StatusNotFound, "background_process_not_found")
	if !env.bgReg.HasRunning(sess2.ID) {
		t.Fatal("cross-project stop terminated proj2's process")
	}

	// Non-existent process ID in proj1 must 404.
	missingProcRec := httptest.NewRecorder()
	env.srv.ServeHTTP(missingProcRec, newAuthedRequest(http.MethodPost, "/v1/projects/"+proj1.ID+"/background/bg_missing/stop", nil))
	assertErrorResponse(t, missingProcRec, http.StatusNotFound, "background_process_not_found")

	// Non-existent project ID must 404 on both GET and POST.
	missingProjGet := httptest.NewRecorder()
	env.srv.ServeHTTP(missingProjGet, newAuthedRequest(http.MethodGet, "/v1/projects/00000000-0000-4000-8000-000000009999/background", nil))
	assertErrorResponse(t, missingProjGet, http.StatusNotFound, "project_not_found")

	missingProjStop := httptest.NewRecorder()
	env.srv.ServeHTTP(missingProjStop, newAuthedRequest(http.MethodPost, "/v1/projects/00000000-0000-4000-8000-000000009999/background/"+h1+"/stop", nil))
	assertErrorResponse(t, missingProjStop, http.StatusNotFound, "project_not_found")

	// Valid stop on proj1/h1:
	stopRec := httptest.NewRecorder()
	env.srv.ServeHTTP(stopRec, newAuthedRequest(http.MethodPost, "/v1/projects/"+proj1.ID+"/background/"+h1+"/stop", nil))
	if stopRec.Code != http.StatusOK {
		t.Fatalf("stop status = %d body=%s", stopRec.Code, stopRec.Body.String())
	}
	var stopResp wire.BackgroundProcessStopResult
	testutil.FailErr(t, "decode stop result", json.Unmarshal(stopRec.Body.Bytes(), &stopResp))
	if stopResp.ProcessID != h1 || !stopResp.StopRequested {
		t.Fatalf("stopResp = %+v", stopResp)
	}

	// Verify person action was recorded for stopProjectBackgroundProcess.
	actions, err := env.srv.personActions.Recent(ctx, 10)
	testutil.FailErr(t, "list person actions", err)
	foundAction := false
	for _, act := range actions {
		if act.OperationID == "stopProjectBackgroundProcess" &&
			act.PathParams["id"] == proj1.ID &&
			act.PathParams["process_id"] == h1 &&
			act.Status == http.StatusOK {
			foundAction = true
			break
		}
	}
	if !foundAction {
		t.Fatalf("expected stopProjectBackgroundProcess person action, got %+v", actions)
	}

	// Wait for h1 to settle and verify termination_reason == stopped_by_user and owning chat kick.
	done, err := env.bgReg.Lifecycle.Await(ctx, sess1.ID, h1, 5*time.Second)
	testutil.FailErr(t, "await h1", err)
	if !done {
		t.Fatal("h1 did not exit after stop")
	}

	listRec := httptest.NewRecorder()
	env.srv.ServeHTTP(listRec, newAuthedRequest(http.MethodGet, "/v1/projects/"+proj1.ID+"/background", nil))
	var listResp wire.ProjectBackgroundProcessListResponse
	testutil.FailErr(t, "decode list after stop", json.Unmarshal(listRec.Body.Bytes(), &listResp))
	if len(listResp.Processes) != 1 || listResp.Processes[0].Running || listResp.Processes[0].TerminationReason != wire.BackgroundProcessTerminationReasonStoppedByUser {
		t.Fatalf("processes after stop = %+v, want running=false reason=stopped_by_user", listResp.Processes)
	}

	// Owning chat must have coordinator-process-finished queued for its next turn.
	if kickID, ok := env.sessions.Runner.Coordinator.Kicks().PeekPendingKickID(sess1.ID); !ok || kickID != "coordinator-process-finished" {
		t.Fatalf("pending kick for owning chat = (%q, %v), want coordinator-process-finished", kickID, ok)
	}

	// SessionSummary.BackgroundRunning must now be 0.
	sessListRec := httptest.NewRecorder()
	env.srv.ServeHTTP(sessListRec, newAuthedRequest(http.MethodGet, "/v1/projects/"+proj1.ID+"/sessions", nil))
	var sessListResp wire.SessionListPage
	testutil.FailErr(t, "decode sessions after stop", json.Unmarshal(sessListRec.Body.Bytes(), &sessListResp))
	if len(sessListResp.Sessions) != 1 || sessListResp.Sessions[0].BackgroundRunning != 0 {
		t.Fatalf("session summary after stop = %+v, want background_running=0", sessListResp.Sessions)
	}
}

func TestProjectBackgroundAuthorizationRejectsUnauthenticatedAndForbiddenCallers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	env := newProjectBackgroundTestEnv(t)
	ctx := t.Context()

	dir := resolveTestPath(t, t.TempDir())
	proj := createProjectForTest(t, env.srv, dir)
	sess, err := env.store.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj.ID)
	testutil.FailErr(t, "create sess", err)
	testutil.FailErr(t, "title sess", env.store.UpdateSession(ctx, sess.ID, func(s *wire.Session) {
		s.Title = "Confidential Chat Title"
	}))

	handle, err := env.bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sess.ID, ProjectID: proj.ID, Runner: hostcmd.NewRunner(), Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("confidential-cmd-flag"),
			ProjectDir: dir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start handle", err)

	// 1. Unauthenticated requests (missing or wrong token) -> 401 Unauthorized.
	for _, tc := range []struct {
		method string
		path   string
		token  string
	}{
		{http.MethodGet, "/v1/projects/" + proj.ID + "/background", ""},
		{http.MethodGet, "/v1/projects/" + proj.ID + "/background", "wrong-token"},
		{http.MethodPost, "/v1/projects/" + proj.ID + "/background/" + handle + "/stop", ""},
		{http.MethodPost, "/v1/projects/" + proj.ID + "/background/" + handle + "/stop", "wrong-token"},
	} {
		req := httptest.NewRequestWithContext(ctx, tc.method, tc.path, nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		assertErrorResponse(t, rec, http.StatusUnauthorized, "unauthorized")
		if strings.Contains(rec.Body.String(), "confidential-cmd-flag") || strings.Contains(rec.Body.String(), "Confidential Chat Title") {
			t.Fatalf("unauthenticated response leaked metadata: %s", rec.Body.String())
		}
	}

	// 2. Non-owner caller -> 403 Forbidden via authorizeOperation.
	nonOwnerCtx := people.WithCaller(ctx, people.Person{ID: "person-guest", Role: wire.PersonRole("guest")})
	listHandler := env.srv.authorizeOperation(operationListProjectBackgroundProcesses, env.srv.Admin.Project.Projects.HandleListProjectBackgroundProcesses)
	stopHandler := env.srv.authorizeOperation(operationStopProjectBackgroundProcess, env.srv.Admin.Project.Projects.HandleStopProjectBackgroundProcess)

	recList := httptest.NewRecorder()
	listHandler(recList, httptest.NewRequestWithContext(nonOwnerCtx, http.MethodGet, "/v1/projects/"+proj.ID+"/background", nil))
	assertErrorResponse(t, recList, http.StatusForbidden, "forbidden")
	if strings.Contains(recList.Body.String(), "confidential-cmd-flag") || strings.Contains(recList.Body.String(), "Confidential Chat Title") {
		t.Fatalf("forbidden list response leaked metadata: %s", recList.Body.String())
	}

	recStop := httptest.NewRecorder()
	stopHandler(recStop, httptest.NewRequestWithContext(nonOwnerCtx, http.MethodPost, "/v1/projects/"+proj.ID+"/background/"+handle+"/stop", nil))
	assertErrorResponse(t, recStop, http.StatusForbidden, "forbidden")

	if !env.bgReg.HasRunning(sess.ID) {
		t.Fatal("unauthorized/forbidden requests must not stop the running process")
	}
}

func TestProjectBackgroundEventsAndReconnectReplay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	env := newProjectBackgroundTestEnv(t)
	ctx := t.Context()

	baseURL := startTestHTTPServer(t, env.srv)
	dir := resolveTestPath(t, t.TempDir())
	proj := createProjectForTest(t, env.srv, dir)
	sess, err := env.store.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj.ID)
	testutil.FailErr(t, "create sess", err)

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, baseURL+"/v1/events?project_id="+proj.ID, nil)
	testutil.FailErr(t, "build SSE request", err)
	WithTestAuth(req)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "open SSE stream", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	readNextProjectBackgroundEvent := func(r *bufio.Reader) (string, wire.ProjectBackgroundEvent) {
		t.Helper()
		var lastID string
		for {
			line, readErr := r.ReadString('\n')
			testutil.FailErr(t, "read SSE line", readErr)
			line = strings.TrimRight(line, "\r\n")
			if after, ok := strings.CutPrefix(line, "id:"); ok {
				lastID = strings.TrimSpace(after)
				continue
			}
			data, ok := strings.CutPrefix(line, "data:")
			if !ok {
				continue
			}
			var envEvent struct {
				Topic   wire.EventTopic            `json:"topic"`
				Payload wire.ProjectBackgroundEvent `json:"payload"`
			}
			testutil.FailErr(t, "decode SSE envelope", json.Unmarshal([]byte(strings.TrimSpace(data)), &envEvent))
			if envEvent.Topic == wire.EventTopicProjectBackground {
				return lastID, envEvent.Payload
			}
		}
	}

	// 1. Start a background process -> "started" event.
	handle, err := env.bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sess.ID, ProjectID: proj.ID, Runner: hostcmd.NewRunner(), Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("sse-server"),
			ProjectDir: dir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start handle", err)

	_, startEv := readNextProjectBackgroundEvent(reader)
	if startEv.Action != "started" || startEv.ProcessID != handle || startEv.ProjectID != proj.ID || startEv.SessionID != sess.ID || !startEv.Running || startEv.Revision != 1 {
		t.Fatalf("started event = %+v", startEv)
	}

	// 2. Listener port change -> "ports_changed" event.
	env.setPorts(handle, netip.MustParseAddrPort("127.0.0.1:4321"))
	env.bgReg.RefreshListeners(ctx)

	cursorAfterPorts, portsEv := readNextProjectBackgroundEvent(reader)
	if portsEv.Action != "ports_changed" || portsEv.ProcessID != handle || portsEv.Revision <= startEv.Revision || len(portsEv.Ports) != 1 || portsEv.Ports[0] != "127.0.0.1:4321" {
		t.Fatalf("ports_changed event = %+v", portsEv)
	}
	if cursorAfterPorts == "" {
		t.Fatal("expected SSE id cursor on ports_changed event")
	}

	// 3. Disconnect SSE stream, stop the process while disconnected, then reconnect with ?after=cursorAfterPorts.
	cancelStream()
	_ = resp.Body.Close()

	stopRec := httptest.NewRecorder()
	env.srv.ServeHTTP(stopRec, newAuthedRequest(http.MethodPost, "/v1/projects/"+proj.ID+"/background/"+handle+"/stop", nil))
	if stopRec.Code != http.StatusOK {
		t.Fatalf("stop status = %d", stopRec.Code)
	}
	_, err = env.bgReg.Lifecycle.Await(ctx, sess.ID, handle, 5*time.Second)
	testutil.FailErr(t, "await handle after stop", err)

	replayCtx, cancelReplay := context.WithCancel(ctx)
	defer cancelReplay()
	replayReq, err := http.NewRequestWithContext(replayCtx, http.MethodGet, baseURL+"/v1/events?project_id="+proj.ID+"&after="+cursorAfterPorts, nil)
	testutil.FailErr(t, "build replay SSE request", err)
	WithTestAuth(replayReq)
	replayResp, err := http.DefaultClient.Do(replayReq)
	testutil.FailErr(t, "open replay SSE stream", err)
	defer replayResp.Body.Close()
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("replay SSE status = %d", replayResp.StatusCode)
	}

	replayReader := bufio.NewReader(replayResp.Body)
	_, endedEv := readNextProjectBackgroundEvent(replayReader)
	if endedEv.Action != "ended" || endedEv.ProcessID != handle || endedEv.Running || endedEv.TerminationReason != wire.BackgroundProcessTerminationReasonStoppedByUser || endedEv.Revision <= portsEv.Revision {
		t.Fatalf("replayed ended event = %+v (portsEv.Revision=%d)", endedEv, portsEv.Revision)
	}
}

func ptrBoolForTest(v bool) *bool { return &v }
