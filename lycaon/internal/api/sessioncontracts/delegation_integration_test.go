package sessioncontracts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestDelegationCreateDispatchAbortHTTP(t *testing.T) {
	srv, reg := contractfixture.NewDelegationTestServer(t)
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "project.CreateWithRoot failed", err)

	createBody := fmt.Sprintf(`{"operation_id":%q,"project_id":%q,"task":"implement","strategy":"file-based"}`, uuid.NewString(), p.ID)
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/delegations", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", w.Code, w.Body.String())
	}
	var r wire.Delegation
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if len(r.Legs) != 1 {
		t.Fatalf("legs = %d", len(r.Legs))
	}
	if r.CoordinatorSessionID == "" {
		t.Fatal("expected coordinator_session_id on delegation create")
	}

	dispatchReq := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/delegations/"+r.ID+"/dispatch",
		strings.NewReader(fmt.Sprintf(`{"leg_id":%q}`, r.Legs[0].ID)))
	dispatchReq.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, dispatchReq)
	if w.Code != http.StatusOK {
		t.Fatalf("dispatch status = %d body = %s", w.Code, w.Body.String())
	}
	var leg wire.Leg
	if err := json.Unmarshal(w.Body.Bytes(), &leg); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if leg.Status != wire.LegStatusDispatched {
		t.Fatalf("leg status = %q", leg.Status)
	}

	abortReq := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/delegations/"+r.ID+"/abort", strings.NewReader(`{"reason":"stop"}`))
	abortReq.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, abortReq)
	if w.Code != http.StatusOK {
		t.Fatalf("abort status = %d", w.Code)
	}
}

func TestCreateDelegationReplaysItsOperation(t *testing.T) {
	srv, reg := contractfixture.NewDelegationTestServer(t)
	p, err := project.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "project.CreateWithRoot failed", err)
	operationID := uuid.NewString()
	create := func(body string) *httptest.ResponseRecorder {
		req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/delegations", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}

	first := create(fmt.Sprintf(`{"operation_id":%q,"project_id":%q,"task":"implement"}`, operationID, p.ID))
	if first.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", first.Code, first.Body.String())
	}
	var created wire.Delegation
	testutil.FailErr(t, "decode created delegation", json.Unmarshal(first.Body.Bytes(), &created))
	if created.Strategy != wire.HuntStrategyFileBased {
		t.Fatalf("strategy = %q, want the file-based default", created.Strategy)
	}

	replay := create(fmt.Sprintf(`{"operation_id":%q,"project_id":%q,"task":"implement","strategy":"file-based"}`, operationID, p.ID))
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay status = %d body = %s, want the first create's 201", replay.Code, replay.Body.String())
	}
	var replayed wire.Delegation
	testutil.FailErr(t, "decode replayed delegation", json.Unmarshal(replay.Body.Bytes(), &replayed))
	if replayed.ID != created.ID {
		t.Fatalf("replayed id = %s, want %s", replayed.ID, created.ID)
	}

	conflict := create(fmt.Sprintf(`{"operation_id":%q,"project_id":%q,"task":"something else"}`, operationID, p.ID))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d body = %s", conflict.Code, conflict.Body.String())
	}
	var resp wire.ErrorResponse
	testutil.FailErr(t, "decode conflict", json.Unmarshal(conflict.Body.Bytes(), &resp))
	if resp.Code != wire.ApiErrorCodeIdempotencyConflict {
		t.Fatalf("conflict code = %q", resp.Code)
	}
}

func TestDispatchUnknownDelegationOrLeg(t *testing.T) {
	srv, reg := contractfixture.NewDelegationTestServer(t)
	p, err := project.CreateWithRoot(t.Context(), reg, t.TempDir())
	testutil.FailErr(t, "project.CreateWithRoot failed", err)
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/delegations", strings.NewReader(
		fmt.Sprintf(`{"operation_id":%q,"project_id":%q,"task":"implement"}`, uuid.NewString(), p.ID)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var created wire.Delegation
	testutil.FailErr(t, "decode created delegation", json.Unmarshal(w.Body.Bytes(), &created))

	for _, tc := range []struct {
		name, path, body string
		want             wire.ApiErrorCode
	}{
		{"unknown delegation", "/v1/delegations/" + uuid.NewString() + "/dispatch", `{}`, wire.ApiErrorCodeDelegationNotFound},
		{"unknown delegation with leg", "/v1/delegations/" + uuid.NewString() + "/dispatch", fmt.Sprintf(`{"leg_id":%q}`, uuid.NewString()), wire.ApiErrorCodeDelegationNotFound},
		{"unknown leg", "/v1/delegations/" + created.ID + "/dispatch", fmt.Sprintf(`{"leg_id":%q}`, uuid.NewString()), wire.ApiErrorCodeDelegationLegNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := contractfixture.NewAuthedRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
			}
			var resp wire.ErrorResponse
			testutil.FailErr(t, "decode error", json.Unmarshal(w.Body.Bytes(), &resp))
			if resp.Code != tc.want {
				t.Fatalf("code = %q, want %q", resp.Code, tc.want)
			}
		})
	}
}

func TestDelegationEndToEndComplete(t *testing.T) {
	srv, reg := contractfixture.NewDelegationTestServerRunningWorkers(t)
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open failed", err)

	createBody := fmt.Sprintf(`{"operation_id":%q,"project_id":%q,"task":"hello delegation","strategy":"file-based"}`, uuid.NewString(), p.ID)
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/delegations", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d", w.Code)
	}
	var r wire.Delegation
	json.Unmarshal(w.Body.Bytes(), &r)

	dispatchReq := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/delegations/"+r.ID+"/dispatch",
		strings.NewReader(fmt.Sprintf(`{"leg_id":%q}`, r.Legs[0].ID)))
	dispatchReq.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(httptest.NewRecorder(), dispatchReq)

	readDelegation := func() wire.Delegation {
		getReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/delegations/"+r.ID, nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, getReq)
		var st wire.Delegation
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			testutil.FailErr(t, "unmarshal delegation", err)
		}
		return st
	}
	testutil.WaitFor(t, 5*time.Second, func() bool {
		return readDelegation().Phase == wire.DelegationPhaseDone
	})

	// Done is also reached when the worker dies before starting. A preamble
	// failure leaves Result.Status empty; a worker that ran reports a status.
	// LegStatusComplete needs a real implementer artifact, which a mock model
	// and a stub tool registry cannot produce.
	final := readDelegation()
	if len(final.Legs) != 1 {
		t.Fatalf("legs = %d, want 1", len(final.Legs))
	}
	leg := final.Legs[0]
	if leg.StartedAt == nil || leg.CompletedAt == nil {
		t.Fatalf("leg did not run to a terminal state: started=%v completed=%v",
			leg.StartedAt != nil, leg.CompletedAt != nil)
	}
	if leg.Result == nil {
		t.Fatal("leg carries no worker result")
	}
	if strings.TrimSpace(leg.Result.Status) == "" {
		t.Fatalf("worker never reported an outcome — it failed before running: %q", leg.Result.Summary)
	}

	boardReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/board?session_id="+r.CoordinatorSessionID, nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, boardReq)
	if w.Code != http.StatusOK {
		t.Fatalf("board status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestBoardSnapshotHTTP(t *testing.T) {
	srv, reg := contractfixture.NewDelegationTestServer(t)
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Open failed", err)
	coord, err := srv.Sources.Workspace.SessionStore.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID, Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create coordinator session", err)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/board?session_id="+coord.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
}
