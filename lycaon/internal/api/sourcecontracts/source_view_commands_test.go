package sourcecontracts

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceViewCreateReplayAndComparisonCommands(t *testing.T) {
	server := contractfixture.NewTestServer(t)
	t.Cleanup(server.StopBackground)
	p := contractfixture.CreateProjectForTest(t, server, t.TempDir())
	before, after := "before\n", "after\n"
	request := wire.SourceComparisonViewCreate{Kind: "comparison", ClientID: "window:main", OperationID: uuid.NewString(),
		Source: wire.SourceComparisonSelector{Text: &wire.TextComparisonSource{Kind: "text", Path: "a.txt", Before: &before, After: &after}},
		Intent: wire.SourceComparisonIntent{Mode: "full"}}
	created := contractfixture.ReadSourceViewResponse(t, contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleCreateSourceView, p.ID, "", request), http.StatusCreated)
	id := created.Comparison.ID
	replay := contractfixture.ReadSourceViewResponse(t, contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleCreateSourceView, p.ID, "", request), http.StatusCreated)
	if replay.Comparison.ID != id {
		t.Fatal("create retry made another view")
	}
	changed := request
	changed.Intent.Mode = "changes"
	conflict := contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleCreateSourceView, p.ID, "", changed)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("request body conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	var ready wire.SourceView
	testutil.WaitFor(t, 10*time.Second, func() bool {
		ready = contractfixture.ReadSourceViewResponse(t, contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleGetSourceView, p.ID, id, nil), http.StatusOK)
		return ready.Comparison.State != "preparing"
	})
	if ready.Comparison.State != "ready" {
		t.Fatalf("preparation failed: %+v", ready.Comparison.Failure)
	}
	update := wire.SourceComparisonViewUpdate{Kind: "comparison", OperationID: uuid.NewString(), ExpectedIntentRevision: ready.Comparison.IntentRevision, Intent: wire.SourceComparisonIntent{Mode: "before"}}
	updated := contractfixture.ReadSourceViewResponse(t, contractfixture.CallSourceViewHandler(t, server.Sources.Trees.HandleApplySourceViewIntent, p.ID, id, update), http.StatusOK)
	repeated := contractfixture.ReadSourceViewResponse(t, contractfixture.CallSourceViewHandler(t, server.Sources.Trees.HandleApplySourceViewIntent, p.ID, id, update), http.StatusOK)
	if repeated.Comparison.IntentRevision != updated.Comparison.IntentRevision || repeated.Comparison.Intent.Mode != "before" {
		t.Fatal("repeated command changed intent twice")
	}
	update.OperationID = uuid.NewString()
	stale := contractfixture.CallSourceViewHandler(t, server.Sources.Trees.HandleApplySourceViewIntent, p.ID, id, update)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale intent accepted: %d", stale.Code)
	}
	released := contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleReleaseSourceView, p.ID, id, nil)
	if released.Code != http.StatusNoContent {
		t.Fatalf("release=%d %s", released.Code, released.Body.String())
	}
	if again := contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleReleaseSourceView, p.ID, id, nil); again.Code != http.StatusNotFound {
		t.Fatalf("second release=%d %s", again.Code, again.Body.String())
	}
	expired := contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleGetSourceView, p.ID, id, nil)
	if expired.Code != http.StatusNotFound {
		t.Fatalf("released view=%d", expired.Code)
	}
}

func TestSourceViewInvalidationReleasesOnlyChangedProject(t *testing.T) {
	server := contractfixture.NewTestServer(t)
	t.Cleanup(server.StopBackground)
	first := contractfixture.CreateProjectForTest(t, server, t.TempDir())
	second := contractfixture.CreateProjectForTest(t, server, t.TempDir())
	Text := "source\n"
	request := wire.SourceComparisonViewCreate{Kind: "comparison", ClientID: "window:main", OperationID: uuid.NewString(), Source: wire.SourceComparisonSelector{Text: &wire.TextComparisonSource{Kind: "text", Path: "a.txt", After: &Text}}, Intent: wire.SourceComparisonIntent{Mode: "full"}}
	firstView := contractfixture.ReadSourceViewResponse(t, contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleCreateSourceView, first.ID, "", request), http.StatusCreated)
	secondView := contractfixture.ReadSourceViewResponse(t, contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleCreateSourceView, second.ID, "", request), http.StatusCreated)
	server.Sources.Views.InvalidateProjectSourceViews(first.ID)
	removed := contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleGetSourceView, first.ID, firstView.Comparison.ID, nil)
	if removed.Code != http.StatusNotFound {
		t.Fatalf("invalidated view=%d", removed.Code)
	}
	contractfixture.ReadSourceViewResponse(t, contractfixture.CallSourceViewHandler(t, server.Sources.Views.HandleGetSourceView, second.ID, secondView.Comparison.ID, nil), http.StatusOK)
}

func TestTextComparisonDistinguishesAbsentFromEmpty(t *testing.T) {
	empty := ""
	absent, present := sourceapi.TextComparisonSide("empty.txt", nil), sourceapi.TextComparisonSide("empty.txt", &empty)
	if absent.State != "absent" || absent.Availability != "absent" {
		t.Fatalf("absent side: %+v", absent)
	}
	if present.State == "absent" || present.Availability != "available" || present.Content != "" {
		t.Fatalf("empty side: %+v", present)
	}
}
