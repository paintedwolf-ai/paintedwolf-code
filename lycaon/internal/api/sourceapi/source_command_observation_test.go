package sourceapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCommandObservationReadsTheAuthorizedChatWindowAfterSettlement(t *testing.T) {
	conn := testdbfixture.Open(t, "store.db")
	sessions := store.NewSQL(conn)
	server := newSourceHandlerFixture(t, func(deps *Deps) {
		deps.SessionStore, deps.ProjectRegistry = sessions, project.NewSQLRegistry(conn)
		deps.SourceLedger = sourceledger.New(conn, t.TempDir())
	})
	p := sourceViewFixtureProject(t, server)
	ctx := testdbseed.OwnerCaller(t, t.Context(), conn)
	makeChat := func(projectID string) string {
		chat, err := sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projectID)
		testutil.FailErr(t, "create chat", err)
		return chat.ID
	}
	chat, other := makeChat(p.ID), makeChat(p.ID)
	foreign := makeChat(sourceViewFixtureProject(t, server).ID)
	window := uuid.NewString()
	q := db.New(conn)
	testutil.FailErr(t, "insert command window", q.InsertSourceCommandWindow(ctx, db.InsertSourceCommandWindowParams{
		ID: window, ProjectID: p.ID, SessionID: chat, ToolName: "command", Ordinal: 1, StartedTs: "2026-11-18T10:00:00Z",
	}))
	read := func(session string, want int) wire.CommandObservationStatus {
		req := httptest.NewRequest(http.MethodGet, "/?session_id="+session, nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", p.ID)
		route.URLParams.Add("window_id", window)
		res := httptest.NewRecorder()
		server.History.HandleGetProjectSourceCommandObservation(res, req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route)))
		if res.Code != want {
			t.Fatalf("command observation status = %d, want %d: %s", res.Code, want, res.Body)
		}
		var state wire.CommandObservationStatus
		if want == http.StatusOK {
			testutil.FailErr(t, "decode observation", json.Unmarshal(res.Body.Bytes(), &state))
		} else if code := sourceViewErrorCode(t, res, want); code != wire.ApiErrorCodeNotFound {
			t.Fatalf("unavailable observation code = %q, want not_found", code)
		}
		return state
	}
	read(other, http.StatusNotFound)
	read(foreign, http.StatusNotFound)
	if before := read(chat, http.StatusOK); !before.Settling || len(before.Coverage) != 0 {
		t.Fatalf("running observation = %+v", before)
	}
	testutil.FailErr(t, "record gap", q.UpsertSourceCommandWindowCoverage(ctx, db.UpsertSourceCommandWindowCoverageParams{
		WindowID: window, RootID: p.Outside.ID, Path: "srv/shared", Coverage: "not_observed",
		Reason: "io_error", BoundariesJson: "[]",
	}))
	testutil.FailErr(t, "settle command", q.EndSourceCommandWindow(ctx, db.EndSourceCommandWindowParams{ID: window, EndedTs: "2026-11-18T10:00:01Z"}))
	after := read(chat, http.StatusOK)
	if after.Settling || len(after.Coverage) != 1 || after.Coverage[0].Folder != "/srv/shared" || after.Coverage[0].Reason != wire.SourceWindowGapIoError {
		t.Fatalf("settled observation = %+v", after)
	}
}
