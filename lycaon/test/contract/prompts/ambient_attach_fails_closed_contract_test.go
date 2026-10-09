package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// ambientAttachFixture is a server wired for session creation whose workflow
// run store either admits or refuses the ambient start.
type ambientAttachFixture struct {
	srv       *api.Server
	runs      *workflow.SQLStore
	projectID string
}

// refusedStarts is a run store that refuses every workflow start.
type refusedStarts struct{ *workflow.SQLStore }

func (refusedStarts) ReplayStart(context.Context, string, string, string) (*wire.WorkflowRun, bool, error) {
	return nil, false, errors.New("run store refused the start")
}

func newAmbientAttachFixture(t *testing.T, startsAdmitted bool) ambientAttachFixture {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	sessions := store.NewSQL(sqlDB)
	projects := project.NewSQLRegistry(sqlDB)
	p, err := project.CreateWithRoot(t.Context(), projects, t.TempDir())
	contractcheck.FailErr(t, "create project", err)

	registry, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "workflow.RegistryFromDirs", err)
	runs := workflow.NewSQLStore(sqlDB)
	var store workflow.RunStore = runs
	if !startsAdmitted {
		store = refusedStarts{runs}
	}
	mgr := workflow.NewManager(store, sessions, registry, nil)
	mgr.Resolver = workflow.ManifestResolver{}
	deps := apitest.Dependencies(t, api.Dependencies{Core:api.CoreDependencies{
		Store: sessions, Projects: projects,},Storage:api.StorageDependencies{ ModuleRoot: filepath.Join(contractcheck.RepoRoot(t), "lycaon"),},Workflow:api.WorkflowDependencies{
		Workflows: mgr, WorkflowRuns: store,},})
	return ambientAttachFixture{srv: api.NewServer(deps, nil, api.TestAPIToken), projectID: p.ID, runs: runs}
}

// createSession posts a session and returns the accepted body.
func (f ambientAttachFixture) createSession(t *testing.T, posture wire.SessionPosture) wire.Session {
	t.Helper()
	body, err := json.Marshal(wire.CreateSessionRequest{ProjectID: f.projectID, Posture: posture})
	contractcheck.FailErr(t, "marshal create session", err)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions", bytes.NewReader(body))
	req.Header.Set("Authorization", api.TestAuthHeader())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /v1/sessions status = %d body = %s", w.Code, w.Body.String())
	}
	var sess wire.Session
	contractcheck.FailErr(t, "decode created session", json.Unmarshal(w.Body.Bytes(), &sess))
	return sess
}

// awaitPrepared polls until the session leaves the preparing status.
func (f ambientAttachFixture) awaitPrepared(t *testing.T, sessionID string) wire.Session {
	t.Helper()
	var last wire.Session
	testutil.WaitFor(t, 10*time.Second, func() bool {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/sessions/"+sessionID, nil)
		req.Header.Set("Authorization", api.TestAuthHeader())
		w := httptest.NewRecorder()
		f.srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			return false
		}
		if err := json.Unmarshal(w.Body.Bytes(), &last); err != nil {
			return false
		}
		return last.Status != wire.SessionStatusPreparing
	})
	return last
}

// TestBuildChatSessionAlwaysHasLeafRun pins the ambient invariant: a prepared
// build session has an active root run, which phase, gates, agent roster, and
// parallel-task caps all read.
func TestBuildChatSessionAlwaysHasLeafRun(t *testing.T) {
	fixture := newAmbientAttachFixture(t, true)
	sess := fixture.createSession(t, wire.SessionPostureBuild)
	prepared := fixture.awaitPrepared(t, sess.ID)
	if prepared.Status != wire.SessionStatusIdle {
		t.Fatalf("prepared build session status = %q want %q", prepared.Status, wire.SessionStatusIdle)
	}
	run, err := fixture.runs.ActiveBySession(context.Background(), sess.ID)
	contractcheck.FailErr(t, "active run by session", err)
	if run == nil {
		t.Fatal("prepared build chat session has no active workflow run — ambient attach must fail closed, not skip")
	}
	if run.ParentRunID != nil {
		t.Fatalf("ambient attach produced a child run (parent %q), want a root", *run.ParentRunID)
	}
}

// TestBuildChatSessionFailsClosedWithoutAmbientAttach is the other half: a build
// session whose ambient run cannot start must not present as idle and healthy.
func TestBuildChatSessionFailsClosedWithoutAmbientAttach(t *testing.T) {
	fixture := newAmbientAttachFixture(t, false)
	sess := fixture.createSession(t, wire.SessionPostureBuild)
	prepared := fixture.awaitPrepared(t, sess.ID)
	if prepared.Status != wire.SessionStatusError {
		t.Fatalf("build session status = %q want %q when ambient attach cannot run",
			prepared.Status, wire.SessionStatusError)
	}
}

// TestNonBuildPosturesPrepareWithoutRun scopes the rule: ambient attach is
// build-only, so other postures reach idle with no active run.
func TestNonBuildPosturesPrepareWithoutRun(t *testing.T) {
	fixture := newAmbientAttachFixture(t, true)
	for _, posture := range []wire.SessionPosture{
		wire.SessionPostureSpec,
		wire.SessionPostureOrchestrate,
		wire.SessionPostureVet,
	} {
		t.Run(string(posture), func(t *testing.T) {
			sess := fixture.createSession(t, posture)
			prepared := fixture.awaitPrepared(t, sess.ID)
			if prepared.Status != wire.SessionStatusIdle {
				t.Fatalf("prepared %s session status = %q want %q", posture, prepared.Status, wire.SessionStatusIdle)
			}
			run, err := fixture.runs.ActiveBySession(context.Background(), sess.ID)
			contractcheck.FailErr(t, "active run by session", err)
			if run != nil {
				t.Fatalf("%s session attached workflow run %q; ambient attach is build-only", posture, run.ID)
			}
		})
	}
}
