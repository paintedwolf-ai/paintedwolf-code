package sessioncontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPatchSessionTitle(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	srv := contractfixture.NewTestServer(t)

	sess := contractfixture.CreateSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)

	patch := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/sessions/"+sess.ID, strings.NewReader(
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

	empty := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/sessions/"+sess.ID, strings.NewReader(`{"title":"   "}`))
	emptyRec := httptest.NewRecorder()
	srv.ServeHTTP(emptyRec, empty)
	contractfixture.AssertErrorResponse(t, emptyRec, http.StatusBadRequest, "invalid_request")

	get := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID, nil)
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
	srv := contractfixture.NewTestServer(t)

	sess := contractfixture.CreateSessionAtPathOnServer(t, srv, t.TempDir(), wire.SessionPostureBuild)

	compound := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/sessions/"+sess.ID, strings.NewReader(
		`{"title":"Must not land","archived":true}`,
	))
	compoundRec := httptest.NewRecorder()
	srv.ServeHTTP(compoundRec, compound)
	contractfixture.AssertErrorResponse(t, compoundRec, http.StatusBadRequest, "invalid_request")

	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID, nil))
	var after wire.Session
	if err := json.Unmarshal(getRec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode session after rejected patch: %v", err)
	}
	if after.Title == "Must not land" || after.ArchivedAt != nil {
		t.Fatalf("rejected compound command mutated session: title=%q archived_at=%v", after.Title, after.ArchivedAt)
	}
}
