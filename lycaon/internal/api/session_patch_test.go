package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPatchSessionTitle(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	srv := newTestServer(t)

	createProj := newAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(`{"roots":[]}`))
	projRec := httptest.NewRecorder()
	srv.ServeHTTP(projRec, createProj)
	if projRec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", projRec.Code, projRec.Body.String())
	}
	var proj wire.Project
	if err := json.Unmarshal(projRec.Body.Bytes(), &proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	createSess := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(
		`{"project_id":"`+proj.ID+`","posture":"build"}`,
	))
	sessRec := httptest.NewRecorder()
	srv.ServeHTTP(sessRec, createSess)
	if sessRec.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", sessRec.Code, sessRec.Body.String())
	}
	var sess wire.Session
	if err := json.Unmarshal(sessRec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	drainBackground(t, srv)

	patch := newAuthedRequest(http.MethodPatch, "/v1/sessions/"+sess.ID, strings.NewReader(
		`{"title":"  Ship readiness checklist  "}`,
	))
	patchRec := httptest.NewRecorder()
	srv.ServeHTTP(patchRec, patch)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d body = %s", patchRec.Code, patchRec.Body.String())
	}
	var updated wire.Session
	if err := json.Unmarshal(patchRec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode patched: %v", err)
	}
	if updated.Title != "Ship readiness checklist" {
		t.Fatalf("title = %q", updated.Title)
	}

	empty := newAuthedRequest(http.MethodPatch, "/v1/sessions/"+sess.ID, strings.NewReader(`{"title":"   "}`))
	emptyRec := httptest.NewRecorder()
	srv.ServeHTTP(emptyRec, empty)
	assertErrorResponse(t, emptyRec, http.StatusBadRequest, "invalid_request")

	get := newAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID, nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, get)
	var after wire.Session
	if err := json.Unmarshal(getRec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if after.Title != "Ship readiness checklist" {
		t.Fatalf("title cleared to %q", after.Title)
	}
}

func TestPatchSessionRejectsCompoundCommandWithoutMutation(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	srv := newTestServer(t)

	createProj := newAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(`{"roots":[]}`))
	projRec := httptest.NewRecorder()
	srv.ServeHTTP(projRec, createProj)
	var proj wire.Project
	if err := json.Unmarshal(projRec.Body.Bytes(), &proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	createSess := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(
		`{"project_id":"`+proj.ID+`","posture":"build"}`,
	))
	sessRec := httptest.NewRecorder()
	srv.ServeHTTP(sessRec, createSess)
	var sess wire.Session
	if err := json.Unmarshal(sessRec.Body.Bytes(), &sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	drainBackground(t, srv)

	compound := newAuthedRequest(http.MethodPatch, "/v1/sessions/"+sess.ID, strings.NewReader(
		`{"title":"Must not land","archived":true}`,
	))
	compoundRec := httptest.NewRecorder()
	srv.ServeHTTP(compoundRec, compound)
	assertErrorResponse(t, compoundRec, http.StatusBadRequest, "invalid_request")

	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, newAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID, nil))
	var after wire.Session
	if err := json.Unmarshal(getRec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode session after rejected patch: %v", err)
	}
	if after.Title == "Must not land" || after.ArchivedAt != nil {
		t.Fatalf("rejected compound command mutated session: title=%q archived_at=%v", after.Title, after.ArchivedAt)
	}
}
