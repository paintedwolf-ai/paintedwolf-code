package invocation_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestReceiptDocumentRoutes(t *testing.T) {
	var database db.Handle
	srv := contractfixture.NewTestServerWithWorkflows(t, func(d *api.Dependencies) {
		database = d.Core.Database
		d.Core.Invocations = invocation.NewSQLRecorder(d.Core.Database)
	})
	const session = "11111111-2222-4333-8444-555555555555"
	testdbseed.InsertSession(t, database, session, testdbseed.DefaultProjectID)
	f := programReceiptFixtureOn(t, database, session, "actual-model-call", map[string]any{"command": "${op} arg"})
	f.start(t, f.child(t))
	get := func(path string, query url.Values) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+session+path+"?"+query.Encode(), nil))
		return rec
	}
	errorCode := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		var body struct {
			Code string `json:"code"`
		}
		testutil.FailErr(t, "decode error body", json.Unmarshal(rec.Body.Bytes(), &body))
		return body.Code
	}

	rec := get("/program-receipt-document", url.Values{"scopes": {f.program}})
	if rec.Code != http.StatusOK {
		t.Fatalf("read status = %d body = %s", rec.Code, rec.Body.String())
	}
	var page wire.CommandReceiptDocumentPage
	testutil.FailErr(t, "decode page", json.Unmarshal(rec.Body.Bytes(), &page))
	if page.TotalRows != 4 || len(page.Rows) != 4 || page.Settled {
		t.Fatalf("page = %+v", page)
	}

	rec = get("/program-receipt-document/search", url.Values{"scopes": {f.program}, "revision": {page.Revision}, "q": {`"type"`}, "limit": {"2"}})
	var found wire.CommandReceiptSearchPage
	testutil.FailErr(t, "decode search", json.Unmarshal(rec.Body.Bytes(), &found))
	if rec.Code != http.StatusOK || len(found.Matches) != 2 || found.NextCursor == "" {
		t.Fatalf("search status = %d body = %s", rec.Code, rec.Body.String())
	}

	for name, tc := range map[string]struct {
		path   string
		query  url.Values
		status int
		code   wire.ApiErrorCode
	}{
		"no scopes":          {"/program-receipt-document", url.Values{}, http.StatusBadRequest, wire.ApiErrorCodeInvalidQuery},
		"repeated scope":     {"/program-receipt-document", url.Values{"scopes": {f.program, f.program}}, http.StatusBadRequest, wire.ApiErrorCodeInvalidQuery},
		"limit over bound":   {"/program-receipt-document", url.Values{"scopes": {f.program}, "limit": {"501"}}, http.StatusBadRequest, wire.ApiErrorCodeInvalidQuery},
		"offset past end":    {"/program-receipt-document", url.Values{"scopes": {f.program}, "offset": {"5"}}, http.StatusBadRequest, wire.ApiErrorCodeInvalidQuery},
		"malformed revision": {"/program-receipt-document", url.Values{"scopes": {f.program}, "revision": {"abc"}}, http.StatusBadRequest, wire.ApiErrorCodeInvalidQuery},
		"unknown program":    {"/program-receipt-document", url.Values{"scopes": {uuid.NewString()}}, http.StatusNotFound, wire.ApiErrorCodeCommandProgramNotFound},
		"search without q":   {"/program-receipt-document/search", url.Values{"scopes": {f.program}, "revision": {page.Revision}}, http.StatusBadRequest, wire.ApiErrorCodeInvalidQuery},
		"tampered cursor":    {"/program-receipt-document/search", url.Values{"scopes": {f.program}, "revision": {page.Revision}, "q": {"x"}, "cursor": {"bogus"}}, http.StatusBadRequest, wire.ApiErrorCodeInvalidPageCursor},
	} {
		rec := get(tc.path, tc.query)
		if rec.Code != tc.status || errorCode(rec) != string(tc.code) {
			t.Fatalf("%s: status = %d body = %s", name, rec.Code, rec.Body.String())
		}
	}

	f.start(t, f.child(t))
	for _, path := range []string{"/program-receipt-document", "/program-receipt-document/search"} {
		rec := get(path, url.Values{"scopes": {f.program}, "revision": {page.Revision}, "q": {"x"}})
		if rec.Code != http.StatusConflict || errorCode(rec) != string(wire.ApiErrorCodeChatContentChanged) {
			t.Fatalf("%s stale revision: status = %d body = %s", path, rec.Code, rec.Body.String())
		}
	}
}
