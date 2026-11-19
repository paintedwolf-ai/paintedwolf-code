package sessionadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/sessionview"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSessionAccessPublishesProjectRecencyChanges(t *testing.T) {
	for _, access := range []string{"prepare", "materialize", "seen"} {
		t.Run(access, func(t *testing.T) {
			ctx := t.Context()
			projects := project.NewMemoryRegistry()
			older, err := projects.Create(ctx, project.CreateParams{})
			testutil.FailErr(t, "create older project", err)
			_, err = projects.Create(ctx, project.CreateParams{})
			testutil.FailErr(t, "create recent project", err)

			hub := events.NewMemoryHub()
			stream, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{
				Project: older.ID, Viewer: testutil.HostOwner(),
			})
			testutil.FailErr(t, "subscribe to project events", err)
			defer unsubscribe()

			sessions := store.NewMemory()
			var background taskgroup.Group
			t.Cleanup(background.Stop)
			lifecycle := &Lifecycle{
				Store: sessions, Projects: projects, Events: hub,
				SessionView: &sessionview.Projector{Projects: projects}, background: &background,
			}
			transcript := &Transcript{
				Store: sessions, Projects: projects, Events: hub, responses: &httpio.Responder{},
			}
			request := wire.CreateSessionRequest{ProjectID: older.ID, Posture: wire.SessionPostureSpec}
			for _, wantEvents := range []int{1, 0} {
				switch access {
				case "prepare":
					sess, err := sessions.CreateWithStatus(ctx, request, older.ID, wire.SessionStatusPreparing)
					testutil.FailErr(t, "create preparing session", err)
					lifecycle.prepareCreatedSession(ctx, request, sess)
					background.Wait(ctx)
					prepared, err := sessions.Get(ctx, sess.ID)
					testutil.FailErr(t, "read prepared session", err)
					if prepared.Status != wire.SessionStatusIdle {
						t.Fatalf("prepared session status = %s, want idle", prepared.Status)
					}
				case "materialize":
					_, err := lifecycle.MaterializeSession(ctx, request)
					testutil.FailErr(t, "materialize session", err)
				case "seen":
					sess, err := sessions.Create(ctx, request, older.ID)
					testutil.FailErr(t, "create session", err)
					req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/sessions/"+sess.ID+"/seen", nil)
					route := chi.NewRouteContext()
					route.URLParams.Add("id", sess.ID)
					rec := httptest.NewRecorder()
					transcript.HandleMarkSessionSeen(rec, req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route)))
					if rec.Code != http.StatusOK {
						t.Fatalf("mark seen = %d %s, want 200", rec.Code, rec.Body.String())
					}
				}
				ordered, err := projects.List(ctx)
				testutil.FailErr(t, "list recent projects", err)
				if len(ordered) != 2 || ordered[0].ID != older.ID {
					t.Fatalf("recent projects = %v, want %s first", ordered, older.ID)
				}
				assertProjectRecencyEvents(t, stream, older.ID, wantEvents)
			}
		})
	}
}

func assertProjectRecencyEvents(t *testing.T, stream <-chan wire.EventEnvelope, projectID string, want int) {
	t.Helper()
	count := 0
	for {
		select {
		case envelope := <-stream:
			if envelope.Topic != wire.EventTopicProject {
				continue
			}
			var event wire.ProjectEvent
			testutil.FailErr(t, "decode project update", json.Unmarshal(envelope.Data, &event))
			if event.Action != wire.ProjectEventUpdated || event.ID != projectID {
				t.Fatalf("project event = %v, want update for %s", event, projectID)
			}
			count++
		default:
			if count != want {
				t.Fatalf("project update count = %d, want %d", count, want)
			}
			return
		}
	}
}
