package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestComparisonPublishesBeforeScreeningAndRetainsPresentationScreens(t *testing.T) {
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	started, resume := make(chan struct{}, 1), make(chan struct{})
	var startOnce, resumeOnce sync.Once
	unblock := func() { resumeOnce.Do(func() { close(resume) }) }
	t.Cleanup(unblock)
	matcher.SetHarvestSource(func(ctx context.Context) []secretmatch.HarvestedValue {
		startOnce.Do(func() { started <- struct{}{} })
		select {
		case <-resume:
		case <-ctx.Done():
		}
		return nil
	})
	server := newTestServer(t, func(d *Dependencies) { d.SecretSpans = secretspan.New(matcher) })
	p, err := project.CreateWithRoot(t.Context(), server.projectRegistry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	before, after := "old\n", "awsAccessKeyId: AKIAQYJK5TXV4NZR7SGB\n"
	view := comparisonViewForTest(t, server, p.ID, wire.SourceComparisonSelector{Text: &wire.TextComparisonSource{
		Kind: "text", Path: "source.txt", Before: &before, After: &after,
	}}, "full")
	testutil.Receive(t, "screen started", started)
	if view.State != "ready" || view.Comparison.After.SecretScreen != nil {
		t.Fatalf("initial comparison waited for screening: %+v", view)
	}
	oldPresentation := presentationForTest(t, server, p.ID, view.ID, view.IntentRevision)
	fork := comparisonViewForTest(t, server, p.ID, wire.SourceComparisonSelector{Retained: &wire.RetainedComparisonSource{
		Kind: "retained", ViewID: view.ID, Comparison: "before",
	}}, "after")
	if fork.State != "ready" {
		t.Fatalf("retained comparison waited for screening: %+v", fork)
	}
	unblock()
	for _, initial := range []wire.SourceComparisonView{view, fork} {
		completed := awaitComparisonScreen(t, server, p.ID, initial.ID)
		if completed.ProjectionRevision == initial.ProjectionRevision || completed.IntentRevision != initial.IntentRevision {
			t.Fatal("screening did not publish an independent projection revision")
		}
		if completed.Comparison.After.SecretScreen.Spans != nil {
			t.Fatal("comparison metadata included whole-document spans")
		}
		frame := comparisonRowsForTest(t, server, p.ID, completed, 0)
		found := false
		for _, row := range frame.Rows {
			found = found || row.SecretScreen != nil && len(row.SecretScreen.Spans) > 0
		}
		if !found {
			t.Fatal("completed screening did not decorate the requested rows")
		}
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, newAuthedRequest(http.MethodGet, sourceapi.SourceViewURL(p.ID, view.ID)+"/presentations/"+oldPresentation.ID+"/rows?limit=200", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("old presentation unavailable: %d %s", response.Code, response.Body.String())
	}
	var oldFrame wire.SourceComparisonFrame
	testutil.FailErr(t, "decode retained rows", json.Unmarshal(response.Body.Bytes(), &oldFrame))
	for _, row := range oldFrame.Rows {
		if row.SecretScreen != nil {
			t.Fatal("screening mutated rows in a retained presentation")
		}
	}
	if oldPresentation.View.Comparison.Comparison.After.SecretScreen != nil {
		t.Fatal("screening mutated a retained metadata snapshot")
	}
}

func awaitComparisonScreen(t *testing.T, server *Server, projectID, viewID string) wire.SourceComparisonView {
	t.Helper()
	var view wire.SourceComparisonView
	testutil.WaitFor(t, 5*time.Second, func() bool {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, newAuthedRequest(http.MethodGet, sourceapi.SourceViewURL(projectID, viewID), nil))
		view = *readSourceViewResponse(t, response, http.StatusOK).Comparison
		return view.Comparison != nil &&
			(view.Comparison.After != nil && view.Comparison.After.SecretScreen != nil ||
				view.Comparison.Before != nil && view.Comparison.Before.SecretScreen != nil)
	})
	return view
}

func TestReleasedComparisonCancelsPendingScreening(t *testing.T) {
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	started, canceled := make(chan struct{}, 1), make(chan struct{}, 1)
	var startOnce, cancelOnce sync.Once
	matcher.SetHarvestSource(func(ctx context.Context) []secretmatch.HarvestedValue {
		startOnce.Do(func() { started <- struct{}{} })
		<-ctx.Done()
		cancelOnce.Do(func() { canceled <- struct{}{} })
		return nil
	})
	server := newTestServer(t, func(d *Dependencies) { d.SecretSpans = secretspan.New(matcher) })
	p, err := project.CreateWithRoot(t.Context(), server.projectRegistry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	text := "source\n"
	view := comparisonViewForTest(t, server, p.ID, wire.SourceComparisonSelector{Text: &wire.TextComparisonSource{
		Kind: "text", Path: "source.txt", Before: &text, After: &text,
	}}, "full")
	testutil.Receive(t, "screen started", started)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, newAuthedRequest(http.MethodDelete, sourceapi.SourceViewURL(p.ID, view.ID), nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("release comparison: %d %s", response.Code, response.Body.String())
	}
	testutil.Receive(t, "abandoned screen canceled", canceled)
}
