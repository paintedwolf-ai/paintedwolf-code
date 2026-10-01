package sourcetree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSupersededReviewDoesNotExpireRetainedView(t *testing.T) {
	view, root := viewFixture(t)
	<-view.Prepare()
	builder := view.ReviewBuilder(t.Context())
	defer builder.Close()
	testutil.FailErr(t, "record deleted path", builder.Add(Address{Root: root.ID, Path: "gone.txt"}))
	review, err := builder.Finish()
	testutil.FailErr(t, "finish review", err)
	testutil.FailErr(t, "install review", view.SetReview(review))
	// A frame captured the old review before a background refresh replaced it.
	release := review.retain()
	defer release()
	testutil.FailErr(t, "replace review", view.SetReview(nil))
	_, _, err = review.projection(view.ctx, reviewSource{root: root.ID}, "previous", nil)
	if !errors.Is(err, pagedview.ErrRevision) {
		t.Fatalf("superseded review read = %v, want revision changed", err)
	}
	_, _, err = view.Revision(t.Context())
	testutil.FailErr(t, "read retained view", err)
}

func TestEmptyReviewRefreshPreservesProjection(t *testing.T) {
	view, _ := viewFixture(t)
	<-view.Prepare()
	before, _, err := view.Revision(t.Context())
	testutil.FailErr(t, "read initial revision", err)
	builder := view.ReviewBuilder(t.Context())
	defer builder.Close()
	review, err := builder.Finish()
	testutil.FailErr(t, "finish empty review", err)
	testutil.FailErr(t, "publish empty review", view.SetReview(review))
	after, _, err := view.Revision(t.Context())
	testutil.FailErr(t, "read refreshed revision", err)
	if after != before {
		t.Fatalf("empty review changed revision: %v -> %v", before, after)
	}
}

func TestReviewPreparationDoesNotBlockConcurrentReads(t *testing.T) {
	view, root := viewFixture(t)
	<-view.Prepare()
	builder := view.ReviewBuilder(t.Context())
	t.Cleanup(builder.Close)
	testutil.FailErr(t, "record deleted path", builder.Add(Address{Root: root.ID, Path: "gone/file.txt"}))
	review, err := builder.Finish()
	testutil.FailErr(t, "publish review facts", err)
	t.Cleanup(review.Close)
	navigation, err := view.catalog.OpenNavigation(t.Context(), view.scope.Project, root)
	testutil.FailErr(t, "read review basis", err)
	base := Projection{Root: root.ID, Rules: &Rules{}, Navigation: navigation}
	revision, err := base.revision(t.Context())
	testutil.FailErr(t, "read review dependencies", err)
	testutil.FailErr(t, "release review basis", navigation.Close())
	entered, unblock := make(chan struct{}), make(chan struct{})
	var once sync.Once
	source := reviewSource{root: root.ID, rules: &Rules{}, revision: revision, open: func(ctx context.Context) (*sourcecatalog.Navigation, error) {
		once.Do(func() { close(entered) })
		select {
		case <-unblock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return view.catalog.OpenNavigation(ctx, view.scope.Project, root)
	}}
	_, _, err = review.projection(view.ctx, source, "intent", nil)
	if !errors.Is(err, pagedview.ErrPreparing) {
		t.Fatalf("initial read=%v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("review builder did not start")
	}
	_, _, err = review.projection(view.ctx, source, "intent", nil)
	if !errors.Is(err, pagedview.ErrPreparing) {
		t.Fatalf("concurrent read=%v", err)
	}
	close(unblock)
	var projection *ReviewProjection
	var release func()
	testutil.WaitFor(t, 5*time.Second, func() bool {
		projection, release, err = review.projection(view.ctx, source, "intent", nil)
		return !errors.Is(err, pagedview.ErrPreparing)
	})
	testutil.FailErr(t, "read published review", err)
	defer release()
	if projection == nil {
		t.Fatal("ready review has no projection")
	}
}

func TestIdenticalReviewFactsPreserveRevision(t *testing.T) {
	view, root := viewFixture(t)
	<-view.Prepare()
	build := func() *ReviewSet {
		builder := view.ReviewBuilder(t.Context())
		defer builder.Close()
		testutil.FailErr(t, "record review fact", builder.Add(Address{Root: root.ID, Path: "gone.txt"}))
		review, err := builder.Finish()
		testutil.FailErr(t, "finish review facts", err)
		return review
	}
	testutil.FailErr(t, "install review", view.SetReview(build()))
	view.mu.Lock()
	before := view.revision
	original := view.review
	view.mu.Unlock()
	testutil.FailErr(t, "refresh identical review", view.SetReview(build()))
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.revision != before || view.review != original {
		t.Fatal("identical facts replaced the retained review")
	}
}

func TestReviewProjectionSurvivesUnrelatedDiscovery(t *testing.T) {
	view, root := viewFixture(t)
	testutil.FailErr(t, "create closed directory", os.Mkdir(filepath.Join(root.Path, "closed"), 0700))
	testutil.FailErr(t, "create hidden child", os.WriteFile(filepath.Join(root.Path, "closed", "file.txt"), []byte("source"), 0600))
	<-view.Prepare()
	builder := view.ReviewBuilder(t.Context())
	defer builder.Close()
	testutil.FailErr(t, "record deleted fact", builder.Add(Address{Root: root.ID, Path: "gone.txt"}))
	review, err := builder.Finish()
	testutil.FailErr(t, "finish review", err)
	testutil.FailErr(t, "install review", view.SetReview(review))
	var frame Frame
	testutil.WaitFor(t, 5*time.Second, func() bool {
		frame, err = frameForTest(t, view, t.Context(), FrameRequest{Limit: 10, Anchor: &Address{Root: root.ID, Path: "."}})
		return !errors.Is(err, pagedview.ErrPreparing)
	})
	testutil.FailErr(t, "read prepared review", err)
	review.mu.Lock()
	previous := review.current[root.ID]
	review.mu.Unlock()
	_, err = view.catalog.ObserveDirectory(t.Context(), view.scope.Project, root, "closed", sourcecatalog.DirectoryRead{Priority: backgroundwork.PriorityProactive})
	testutil.FailErr(t, "discover unrelated children", err)
	after, err := frameForTest(t, view, t.Context(), FrameRequest{Limit: 10})
	testutil.FailErr(t, "reuse prepared review frame", err)
	review.mu.Lock()
	retained := review.current[root.ID]
	review.mu.Unlock()
	if previous != retained || frame.Revision != after.Revision {
		t.Fatal("unrelated discovery rebuilt the review")
	}
}
