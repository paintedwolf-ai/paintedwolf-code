package pagedview

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestViewportPagesCoverBothDirections(t *testing.T) {
	for _, direction := range []int{-1, 0, 1} {
		pages := ViewportPages(40010, 40060, 100000, direction)
		if len(pages) != 7 || pages[0] != 40000 {
			t.Fatalf("pages=%v", pages)
		}
		for _, offset := range []int64{39400, 39600, 39800, 40200, 40400, 40600} {
			if !slices.Contains(pages, offset) {
				t.Fatalf("missing nearby page %d in %v", offset, pages)
			}
		}
	}
	if pages := ViewportPages(99990, 100100, 100000, -1); len(pages) != 4 || pages[0] != 99800 {
		t.Fatalf("tail pages=%v", pages)
	}
}

func TestViewportInterestSupersedesWorkAndRejectsReordering(t *testing.T) {
	var interests ViewportInterests
	defer interests.Close()
	started, stopped := make(chan struct{}), make(chan struct{})
	testutil.FailErr(t, "start old viewport", interests.Replace(t.Context(), "consumer", 1, func(ctx context.Context) { close(started); <-ctx.Done(); close(stopped) }, func() {}))
	<-started
	latest := make(chan struct{})
	testutil.FailErr(t, "replace viewport", interests.Replace(t.Context(), "consumer", 3, func(context.Context) { close(latest) }, func() {}))
	<-stopped
	<-latest
	called := false
	testutil.FailErr(t, "ignore reordered viewport", interests.Replace(t.Context(), "consumer", 2, func(context.Context) { t.Error("obsolete demand ran") }, func() { called = true }))
	if !called {
		t.Fatal("obsolete demand retained its presentation")
	}
}
