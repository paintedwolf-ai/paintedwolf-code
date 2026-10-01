package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDeclaredOperationsRecordWhoInvokedThem(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	s := &Server{personActions: personactions.New(sqlDB)}
	recorded := generatedOperation{ID: "revokeThing", Method: "POST", Path: "/v1/things/{id}/revoke", Params: []string{"id"}, RecordsPersonAction: true}
	unrecorded := generatedOperation{ID: "previewThing", Method: "POST", Path: "/v1/things/{id}/preview", Params: []string{"id"}}

	r := chi.NewRouter()
	r.Method(recorded.Method, "/things/{id}/revoke", s.recordPersonAction(recorded, func(w http.ResponseWriter, r *http.Request) {
		personactions.Note(r.Context(), "revoked_grant_id", "grant_b", "grant_a", "grant_b")
		w.WriteHeader(http.StatusCreated)
	}))
	r.Method(unrecorded.Method, "/things/{id}/preview", s.recordPersonAction(unrecorded, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	ctx := testdbseed.OwnerCaller(t, t.Context(), sqlDB)
	for _, path := range []string{"/things/thing-1/revoke", "/things/thing-1/preview"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil).WithContext(ctx))
	}

	actions, err := s.personActions.Recent(t.Context(), 10)
	testutil.FailErr(t, "list person actions", err)
	if len(actions) != 1 {
		t.Fatalf("person actions = %+v, want only the declared operation", actions)
	}
	got := actions[0]
	caller, _ := people.Caller(ctx)
	if got.PersonID != caller.ID || got.OperationID != "revokeThing" || got.Status != http.StatusCreated ||
		got.PathParams["id"] != "thing-1" {
		t.Fatalf("person action = %+v", got)
	}
	if ids := got.Subject["revoked_grant_id"]; len(ids) != 2 || ids[0] != "grant_a" || ids[1] != "grant_b" {
		t.Fatalf("subject = %+v, want sorted deduplicated grant ids", got.Subject)
	}
}

func TestPersonActionRowsAreImmutable(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	store := personactions.New(sqlDB)
	testutil.FailErr(t, "record", store.Record(t.Context(), personactions.Action{
		PersonID: testdbseed.OwnerID(t, sqlDB), OperationID: "deleteSession", Status: http.StatusNoContent,
	}))
	if _, err := sqlDB.ExecContext(t.Context(), `UPDATE person_actions SET status = 200 WHERE operation_id = 'deleteSession'`); err == nil {
		t.Fatal("a person action was rewritten")
	}
}
