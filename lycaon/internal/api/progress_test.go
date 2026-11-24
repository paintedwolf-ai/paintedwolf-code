package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type stubProgressSessionRepo struct {
	store.SessionRepository
	sessions map[string]*wire.Session
}

func (s stubProgressSessionRepo) Get(_ context.Context, id string) (*wire.Session, error) {
	sess, ok := s.sessions[id]
	if !ok {
		return nil, store.ErrSessionNotFound
	}
	return sess, nil
}

type stubAPIProgressStore struct {
	progress.Store
	snap progress.Snapshot
	err  error
}

func (s stubAPIProgressStore) Snapshot(context.Context, string) (progress.Snapshot, error) {
	return s.snap, s.err
}

func TestHandleSessionProgressReturnsDigestAndSurfacesStoreErrors(t *testing.T) {
	sessions := stubProgressSessionRepo{
		sessions: map[string]*wire.Session{
			"root-1":  {ID: "root-1"},
			"child-1": {ID: "child-1", ParentSessionID: "root-1"},
		},
	}

	callRoute := func(conv *Conversation, id string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Get("/v1/sessions/{id}/progress", conv.handleSessionProgress)
		req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+id+"/progress", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	seeded := progress.NewSnapshot("run-1",
		progress.Row{ID: "p1", State: progress.ProgressStateDone, Label: "Seed schema"},
		progress.Row{ID: "p2", State: progress.ProgressStatePending, Label: "Run verification"},
	)
	rec := callRoute(&Conversation{
		sessionStore:  sessions,
		progressStore: stubAPIProgressStore{snap: seeded},
		responses:     &httpio.Responder{Logger: slog.Default()},
	}, "child-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var digest wire.ProgressDigest
	if err := json.Unmarshal(rec.Body.Bytes(), &digest); err != nil {
		t.Fatalf("decode digest: %v", err)
	}
	if len(digest.Steps) != 2 || digest.Steps[0].Label != "Seed schema" || digest.Steps[1].State != "pending" {
		t.Fatalf("unexpected digest steps: %+v", digest.Steps)
	}

	nilStoreRec := callRoute(&Conversation{
		sessionStore: sessions,
		responses:    &httpio.Responder{Logger: slog.Default()},
	}, "root-1")
	if nilStoreRec.Code != http.StatusOK {
		t.Fatalf("nil progressStore status = %d, want 200", nilStoreRec.Code)
	}

	failRec := callRoute(&Conversation{
		sessionStore:  sessions,
		progressStore: stubAPIProgressStore{err: errors.New("progress db locked")},
		responses:     &httpio.Responder{Logger: slog.Default()},
	}, "root-1")
	if failRec.Code != http.StatusInternalServerError {
		t.Fatalf("snapshot error status = %d, want 500", failRec.Code)
	}
}
