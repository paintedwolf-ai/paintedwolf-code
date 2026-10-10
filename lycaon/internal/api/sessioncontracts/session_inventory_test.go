package sessioncontracts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCreateSessionReturnsBeforeInventoryAndDetachesCancellation(t *testing.T) {
	// The project is created on the registry, not through the server: project
	// creation schedules its own inventory, and the fake must see only the
	// session's.
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	registry := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), registry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	fake := &contractfixture.BlockingInventoryService{
		Started: make(chan struct{}), Release: make(chan struct{}), CtxErr: make(chan error, 1),
	}
	srv := contractfixture.NewTestServer(t, func(d *hostapi.Dependencies) {
		d.Core.Projects = registry
		d.Source.SourceInventory = fake
	})

	ctx, cancel := context.WithCancel(t.Context())
	body := `{"project_id":"` + p.ID + `","posture":"build"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("session creation waited for background source inventory")
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sess.ID == "" || sess.Status != wire.SessionStatusPreparing {
		t.Fatalf("session = %+v, want durable preparing row", sess)
	}

	select {
	case <-fake.Started:
	case <-time.After(time.Second):
		t.Fatal("source inventory was not scheduled")
	}
	cancel()
	close(fake.Release)
	select {
	case err := <-fake.CtxErr:
		if err != nil {
			t.Fatalf("inventory inherited request cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("source inventory did not finish")
	}
	contractfixture.DrainBackground(t, srv)
}

func TestFixtureStoreOverrideKeepsHostAndAPIAligned(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		sql, workflows, supplied bool
	}{
		{name: "default"}, {name: "sql_store", sql: true},
		{name: "supplied_host", sql: true, supplied: true},
		{name: "workflow_default", workflows: true},
		{name: "workflow_sql_store", sql: true, workflows: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LYCAON_LLM_MOCK", "1")
			optionCalls := 0
			opts := []contractfixture.TestDeps{contractfixture.WithPassiveSourceInventory, func(d *hostapi.Dependencies) { optionCalls++ }}
			var supplied *session.Host
			if tc.sql {
				database := testdbfixture.Open(t, "fixture.db")
				sessions := store.NewSQL(database)
				opts = append(opts, func(d *hostapi.Dependencies) {
					d.Core.Store, d.Core.Database = sessions, database
					d.Core.Projects = project.NewSQLRegistry(database)
				})
				if tc.supplied {
					supplied = session.NewHost(sessions, session.Models{Client: llm.NewMockProvider(nil), Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
					opts = append(opts, func(d *hostapi.Dependencies) { d.Core.Sessions = supplied })
				}
			}
			var srv *hostapi.Server
			if tc.workflows {
				srv = contractfixture.NewTestServerWithWorkflows(t, opts...)
			} else {
				srv = contractfixture.NewTestServer(t, opts...)
			}
			if optionCalls != 1 {
				t.Fatalf("dependency option applied %d times", optionCalls)
			}
			host := srv.Admin.SessionAdmin.Lifecycle.Sessions
			if supplied != nil && host != supplied {
				t.Fatal("fixture replaced the supplied Host")
			}
			registry := srv.Sources.Workspace.ProjectRegistry
			p, err := project.CreateWithRoot(t.Context(), registry, t.TempDir())
			testutil.FailErr(t, "create fixture project", err)
			created := contractfixture.CreateSessionForProject(t, srv, p.ID)
			read, err := host.Chats.Get(t.Context(), created.ID)
			testutil.FailErr(t, "Host reads API-created session", err)
			if read.ID != created.ID || read.ProjectID != p.ID {
				t.Fatalf("Host read another session: %+v", read)
			}
			_, err = host.Chats.SetPinned(t.Context(), created.ID, true)
			testutil.FailErr(t, "Host pins API-created session", err)
			page := contractfixture.ListSessionsPage(t, srv, p.ID, "")
			if len(page.Sessions) != 1 || page.Sessions[0].ID != created.ID || page.Sessions[0].PinRank == nil {
				t.Fatalf("API did not expose Host pin mutation: %+v", page)
			}
			if tc.workflows {
				response := httptest.NewRecorder()
				srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/workflow-runs/active", nil))
				if response.Code != http.StatusOK {
					t.Fatalf("active workflow status = %d body=%s", response.Code, response.Body.String())
				}
				var active wire.ActiveWorkflowRunResponse
				testutil.FailErr(t, "decode ambient workflow", json.Unmarshal(response.Body.Bytes(), &active))
				if active.Run == nil || active.Run.SessionID != created.ID || active.Run.ProjectID != p.ID || active.Run.WorkflowID != "implement" {
					t.Fatalf("ambient workflow did not attach to API session: %+v", active.Run)
				}
			}
		})
	}
}
