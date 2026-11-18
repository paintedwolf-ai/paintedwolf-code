package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/tools/toolstest"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// errStoreUnreadable is a store failure that is not an absent session.
var errStoreUnreadable = errors.New("session store unreadable")

// lookupStore answers every read of a session row with err.
type lookupStore struct {
	*store.SQL
	err error
}

func (s lookupStore) Get(context.Context, string) (*wire.Session, error) { return nil, s.err }

func (s lookupStore) UpdateSession(context.Context, string, func(*wire.Session)) error { return s.err }

func (s lookupStore) GetTranscriptPage(context.Context, string, wire.TranscriptPageQuery) (wire.SessionTranscriptPage, error) {
	return wire.SessionTranscriptPage{}, s.err
}

// A store failure is not an absent session: a 404 tells Den the chat was
// deleted. Every session route that reports session_not_found for the store's
// own sentinel answers 500 when the store fails instead.
func TestSessionRoutesNeverReportStoreFailureAsNotFound(t *testing.T) {
	sentinel := sessionRouteCodes(t, store.ErrSessionNotFound)
	failure := sessionRouteCodes(t, errStoreUnreadable)
	classifying := 0
	for route, code := range failure {
		if code.code == wire.ApiErrorCodeSessionNotFound {
			t.Errorf("%s reports session_not_found for a store failure", route)
		}
		if sentinel[route].code != wire.ApiErrorCodeSessionNotFound {
			continue
		}
		classifying++
		if code.status != http.StatusInternalServerError {
			t.Errorf("%s answers %d (%s) for a store failure, want 500", route, code.status, code.code)
		}
	}
	if classifying == 0 {
		t.Fatal("no session route reports session_not_found for an absent session")
	}
}

type routeAnswer struct {
	status int
	code   wire.ApiErrorCode
}

// sessionRouteCodes calls every /v1/sessions/{id} operation against a server
// whose store answers lookups with err.
func sessionRouteCodes(t *testing.T, err error) map[string]routeAnswer {
	t.Helper()
	failing := lookupStore{SQL: storetest.New(t), err: err}
	reg := toolstest.NewRegistry()
	host := session.NewHost(failing, session.Models{Client: llm.NewMockProvider(testMockConfig(t)), Limits: settings.DefaultSessionLimits()}, reg)
	host.SetDataDir(t.TempDir())
	wireTestBindingRegistry(t)
	srv := newServerForTest(t, Dependencies{Core: CoreDependencies{Store: failing, Projects: project.NewMemoryRegistry(), Sessions: host}})
	out := map[string]routeAnswer{}
	for _, op := range allGeneratedOperations {
		if !strings.HasPrefix(op.Path, "/v1/sessions/{id}") {
			continue
		}
		path := op.Path
		for _, param := range op.Params {
			path = strings.ReplaceAll(path, "{"+param+"}", uuid.NewString())
		}
		var body *strings.Reader
		if op.Method != http.MethodGet && op.Method != http.MethodDelete {
			body = strings.NewReader("{}")
		}
		var req *http.Request
		if body == nil {
			req = newAuthedRequest(op.Method, path, nil)
		} else {
			req = newAuthedRequest(op.Method, path, body)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		// A success or stream body carries no error code; only the code matters.
		var resp wire.ErrorResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		out[op.Method+" "+op.Path] = routeAnswer{status: w.Code, code: resp.Code}
	}
	if len(out) == 0 {
		t.Fatal("no /v1/sessions/{id} operations are generated")
	}
	return out
}
