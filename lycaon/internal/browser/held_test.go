package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNormalizeViewportDefaults(t *testing.T) {
	w, h, err := NormalizeViewport(0, 0)
	testutil.FailErr(t, "NormalizeViewport failed", err)
	if w != DefaultViewportWidth || h != DefaultViewportHeight {
		t.Fatalf("defaults: got %dx%d", w, h)
	}
}

func TestHeldPagePersistsAcrossOperationDeadlines(t *testing.T) {
	if testing.Short() {
		t.Skip("browser lifetime")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	openCtx, cancelOpen := context.WithCancel(t.Context())
	held, err := OpenHeld(openCtx, pool, OpenOpts{
		ProjectDir: fixtureDir(t, "multi-step-form"), Width: 400, Height: 300,
	})
	testutil.FailErr(t, "open held page", err)
	defer func() { _ = held.Close(t.Context()) }()
	cancelOpen()
	if deadline, ok := held.Page.GetContext().Deadline(); ok {
		t.Fatalf("held page inherited a deadline: %v", deadline)
	}
	first, cancelFirst := pageOperationContext(t.Context(), held.Page, time.Millisecond)
	defer cancelFirst()
	<-first.Done()
	if held.Page.GetContext().Err() != nil {
		t.Fatal("one expired operation canceled the held page")
	}
	// Hold the page beyond the opening deadline.
	timer := time.NewTimer(RasterizeTimeout + 50*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal("test canceled before the retained-page wait completed")
	}
	_, err = held.Act(t.Context(), []CaptureAction{
		{Type: "fill", ActionLocator: ActionLocator{Selector: "#email"}, Value: "still@live.test"},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#next"}},
		{Type: "fill", ActionLocator: ActionLocator{Selector: "#code"}, Value: "42"},
		{Type: "click", ActionLocator: ActionLocator{Selector: "#finish"}},
	}, nil)
	testutil.FailErr(t, "act after opening deadline", err)
	out, err := held.Snapshot(t.Context(), SnapshotOpts{})
	testutil.FailErr(t, "snapshot after opening deadline", err)
	if !strings.Contains(string(out.Snapshot)+string(out.State), "done:still@live.test:42") {
		t.Fatalf("snapshot lost retained page state: %s %s", out.Snapshot, out.State)
	}
	closedPage := held.Page
	testutil.FailErr(t, "close retained page", held.Close(t.Context()))
	checkCtx, cancelCheck := context.WithTimeout(t.Context(), RasterizeTimeout)
	defer cancelCheck()
	if _, err := closedPage.Browser().Context(checkCtx).PageFromTarget(closedPage.TargetID); err == nil {
		t.Fatal("closed target or its cached Rod page is still available")
	}
}

func TestCanceledPageOperationPreservesPoolAndOtherPages(t *testing.T) {
	if testing.Short() {
		t.Skip("browser cancellation")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	held, err := OpenHeld(t.Context(), pool, OpenOpts{ProjectDir: fixtureDir(t, "multi-step-form")})
	testutil.FailErr(t, "open survivor page", err)
	defer func() { _ = held.Close(t.Context()) }()
	before, err := pool.Browser(t.Context())
	testutil.FailErr(t, "browser before cancellation", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := pool.NewPage(ctx, 400, 300); err == nil {
		t.Fatal("canceled page creation succeeded")
	}
	if _, err := held.Act(ctx, []CaptureAction{{Type: "click", ActionLocator: ActionLocator{Selector: "#next"}}}, nil); err == nil {
		t.Fatal("canceled action succeeded")
	}
	after, err := pool.Browser(t.Context())
	testutil.FailErr(t, "browser after cancellation", err)
	if before != after {
		t.Fatal("canceled operation replaced the pooled browser")
	}
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelWait()
	started := time.Now()
	_, err = held.Act(waitCtx, []CaptureAction{{Type: "wait", Wait: "duration", TimeoutMS: 1000}}, nil)
	if err == nil || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("delay action ignored cancellation: elapsed=%s err=%v", time.Since(started), err)
	}
	_, err = held.Act(t.Context(), []CaptureAction{{Type: "fill", ActionLocator: ActionLocator{Selector: "#email"}, Value: "survived"}}, nil)
	testutil.FailErr(t, "action on survivor page", err)
	_, err = held.Snapshot(t.Context(), SnapshotOpts{})
	testutil.FailErr(t, "snapshot on survivor page", err)
}

func TestPoolReplacesOnlyDisconnectedBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("browser connection recovery")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	before, err := pool.Browser(t.Context())
	testutil.FailErr(t, "open pooled browser", err)
	ctx, cancel := context.WithTimeout(t.Context(), RasterizeTimeout)
	defer cancel()
	testutil.FailErr(t, "close test-owned browser connection", before.Context(ctx).Close())
	select {
	case <-pool.disconnected:
	case <-ctx.Done():
		t.Fatal("pool did not observe browser disconnect")
	}
	page, err := pool.NewPage(t.Context(), 400, 300)
	testutil.FailErr(t, "create page on replacement browser", err)
	defer func() { _ = closePage(t.Context(), page) }()
	after, err := pool.Browser(t.Context())
	testutil.FailErr(t, "replacement browser", err)
	if before == after {
		t.Fatal("pool retained the disconnected browser")
	}
}

func TestNormalizeViewportRejectsOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"wider than the frame edge", MaxViewportDim + 1, 720},
		{"taller than the frame edge", 1280, MaxViewportDim + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := NormalizeViewport(tc.w, tc.h)
			rej := &browserengine.RejectError{}
			if !errors.As(err, &rej) || rej.Code != "CAPTURE_VIEWPORT_BOUNDS" {
				t.Fatalf("got %#v want CAPTURE_VIEWPORT_BOUNDS", err)
			}
		})
	}
}

func TestCaptureIsOneShotSugarOverHeldPage(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()

	cap := &CapturePool{Pool: pool}
	out, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "multi-step-form"),
		Actions: []CaptureAction{
			{Type: "fill", ActionLocator: ActionLocator{Selector: "#email"}, Value: "a@b.c"},
			{Type: "click", ActionLocator: ActionLocator{Selector: "#next"}},
			{Type: "fill", ActionLocator: ActionLocator{Selector: "#code"}, Value: "9"},
			{Type: "click", ActionLocator: ActionLocator{Selector: "#finish"}},
		},
		Width: 400, Height: 300,
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !strings.Contains(string(out.Snapshot), "done:a@b.c:9") && !strings.Contains(string(out.State), "done") {
		raw := string(out.Snapshot) + string(out.State)
		if !strings.Contains(raw, "done:a@b.c:9") {
			t.Fatalf("sugar path missed final state: %s", raw)
		}
	}
}
