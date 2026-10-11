package sourcecontracts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceViewTreeReviewAndScopeReplacement(t *testing.T) {
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	server := contractfixture.NewTestServer(t, withLedger)
	p := contractfixture.CreateProjectForTest(t, server, t.TempDir())
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
	physical, err := server.Sources.Workspace.ProjectRegistry.Get(t.Context(), p.ID)
	testutil.FailErr(t, "resolve physical project", err)
	testutil.FailErr(t, "record deleted review path", ledger.Record(t.Context(), sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: p.Roots[0].ID, Path: "gone/deleted.go"},
		ProjectID:      p.ID, Op: wire.SourceChangeOpDelete,
		Origin: wire.SourceChangeOriginAgent, SessionID: "chat", Before: []byte("source\n")}))
	request := wire.SourceTreeViewCreate{Kind: "tree", ClientID: "window:main", OperationID: uuid.NewString(), WorkspaceID: physical.WorkspaceID(), Intent: wire.SourceTreeIntent{Review: &wire.SourceTreeReviewScope{Baseline: "session:chat"}}}
	created := contractfixture.ReadSourceViewResponse(t, contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleCreateSourceView, p.ID, "", request), http.StatusCreated)
	id := created.Tree.ID
	waitReady := func() *wire.SourceTreeView {
		t.Helper()
		var ready *wire.SourceTreeView
		testutil.WaitFor(t, 10*time.Second, func() bool {
			ready = contractfixture.ReadSourceViewResponse(t, contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleGetSourceView, p.ID, id, nil), http.StatusOK).Tree
			return ready.State != "preparing"
		})
		if ready.State != "ready" {
			t.Fatalf("review preparation=%+v", ready.Failure)
		}
		return ready
	}
	ready := waitReady()
	if ready.Extent.Rows != 3 {
		t.Fatalf("review extent=%+v", ready.Extent)
	}
	// Read the same publication through the handlers, under the caller that created the view.
	made := contractfixture.CallSourceViewHandler(t, server.Sources.Presentation.HandleCreateSourcePresentation, p.ID, id, wire.SourcePresentationCreate{OperationID: uuid.NewString(), IntentRevision: ready.IntentRevision})
	if made.Code != http.StatusCreated {
		t.Fatalf("create presentation: %d %s", made.Code, made.Body.String())
	}
	var presentation wire.SourcePresentation
	testutil.FailErr(t, "decode presentation", json.Unmarshal(made.Body.Bytes(), &presentation))
	callPresentation := func(handler http.HandlerFunc, target string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", p.ID)
		route.URLParams.Add("view_id", id)
		route.URLParams.Add("presentation_id", presentation.ID)
		response := httptest.NewRecorder()
		handler(response, request.WithContext(people.WithCaller(context.WithValue(request.Context(), chi.RouteCtxKey, route), testutil.HostOwner())))
		return response
	}
	defer callPresentation(server.Sources.Presentation.HandleReleaseSourcePresentation, "/")
	response := callPresentation(server.Sources.Presentation.HandleGetSourceViewRows, "/?limit=200")
	if response.Code != http.StatusOK {
		t.Fatalf("review rows: status=%d body=%s", response.Code, response.Body.String())
	}
	var frame wire.SourceTreeFrame
	testutil.FailErr(t, "decode review rows", json.Unmarshal(response.Body.Bytes(), &frame))
	if len(frame.Rows) != 3 || frame.Rows[2].Address.Path != "gone/deleted.go" || !frame.Rows[2].Deleted {
		t.Fatalf("review rows=%+v", frame.Rows)
	}
	update := wire.SourceTreeViewUpdate{Kind: "tree", OperationID: uuid.NewString(), ExpectedIntentRevision: ready.IntentRevision, Command: wire.SourceTreeCommand{Review: &wire.SourceTreeReview{Kind: "review"}}}
	contractfixture.ReadSourceViewResponse(t, contractfixture.CallSourceViewHandler(t, server.Sources.Trees.HandleApplySourceViewIntent, p.ID, id, update), http.StatusOK)
	ready = waitReady()
	if ready.Intent.Review != nil || ready.Extent.Rows != 2 {
		t.Fatalf("cleared review=%+v", ready)
	}
}
