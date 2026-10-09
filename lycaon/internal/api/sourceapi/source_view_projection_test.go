package sourceapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestMountedComparisonsShareBoundedContentAndSmallPresentations(t *testing.T) {
	budget := pagedview.NewBudget(128 << 20)
	cache := sourcecomparison.NewCache(budget)
	t.Cleanup(cache.Close)
	service := &sourceViewService{presentations: pagedview.NewLeaseRegistry[*sourcePresentation](pagedview.NewBudget(8<<20), 8, sourceViewLifetime), registry: pagedview.NewRegistry[*sourceView](budget, sourceViewCapacity, sourceViewLifetime)}
	t.Cleanup(service.close)
	scope := pagedview.Scope{Person: "person", Project: "project"}
	for i := range 300 {
		before := wire.SourceComparisonSide{Content: fmt.Sprintf("old %d\n", i), Availability: "available"}
		after := wire.SourceComparisonSide{Content: fmt.Sprintf("new %d\n", i), Availability: "available"}
		for fork := range 2 {
			document, releaseDocument, err := cache.Prepare(t.Context(), scope.Person, scope.Project, before, after, nil)
			testutil.FailErr(t, "prepare shared content", err)
			key := sourceViewCreateKey{scope: scope, client: "window", operation: fmt.Sprintf("%d/%d", i, fork)}
			view, release, _, err := service.create(t.Context(), key, []byte("intent"), 0, func() *sourceView {
				ctx, cancel := context.WithCancel(t.Context())
				return &sourceView{ctx: ctx, cancel: cancel, state: "ready", commands: pagedview.NewCommands[string](&service.receipts), comparisonData: sourceViewComparisonData{comparison: document,
					comparisonBudget: budget, comparisonRelease: releaseDocument}}
			})
			testutil.FailErr(t, "retain mounted presentation", err)
			view.comparisonData.projection, err = view.comparisonProjection(t.Context(), document, wire.SourceComparisonIntent{Mode: "changes"}, nil, nil)
			testutil.FailErr(t, "project shared content", err)
			t.Cleanup(release)
		}
	}
	if used := budget.Used(); used > 32<<20 {
		t.Fatalf("600 small presentations retain %d bytes", used)
	}
}

func TestComparisonReadRetainsProjectionWithoutBlockingIntent(t *testing.T) {
	budget := pagedview.NewBudget(8 << 20)
	cache := sourcecomparison.NewCache(budget)
	t.Cleanup(cache.Close)
	document, release, err := cache.Prepare(t.Context(), "person", "project",
		wire.SourceComparisonSide{Content: "old\n"}, wire.SourceComparisonSide{Content: "new\n"}, nil)
	testutil.FailErr(t, "prepare comparison", err)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	view := &sourceView{ctx: ctx, cancel: cancel, state: "ready", commands: pagedview.NewCommands[string](nil), comparisonData: sourceViewComparisonData{comparison: document, comparisonBudget: budget}}
	defer view.close()
	view.comparisonData.projection, err = view.comparisonProjection(t.Context(), document, wire.SourceComparisonIntent{Mode: "full"}, nil, nil)
	testutil.FailErr(t, "prepare original projection", err)
	read, releaseRead := view.read()
	defer releaseRead()
	before := budget.Used()
	done := make(chan error, 1)
	go func() {
		view.intentMu.Lock()
		defer view.intentMu.Unlock()
		done <- view.applyComparisonIntent(httptest.NewRequest("PATCH", "/", nil), wire.SourceComparisonIntent{Mode: "after"})
	}()
	select {
	case err := <-done:
		testutil.FailErr(t, "update during frame read", err)
	case <-time.After(time.Second):
		t.Fatal("frame read held the intent lock")
	}
	if budget.Used() <= before {
		t.Fatal("replaced projection lost its charge during an active read")
	}
	rows, _, err := read.comparisonData.projection.Frame(t.Context(), 0, 10)
	testutil.FailErr(t, "read retained projection", err)
	if len(rows) != 2 {
		t.Fatalf("old projection has %d rows", len(rows))
	}
}
