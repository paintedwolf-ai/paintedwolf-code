package preview

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

func configurePreviewProjection(c *Controller) {
	c.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	c.projectFrame = func(
		_ context.Context, _ *captureprojection.Projector, _ captureprojection.Scope,
		_ browser.PageRegions, _ string, raw []byte,
	) ([]byte, error) {
		return append([]byte(nil), raw...), nil
	}
}

func TestActionOverlayFromStructuredAction(t *testing.T) {
	got := actionOverlay(browser.CaptureAction{
		Type: "click", ActionLocator: browser.ActionLocator{Selector: "#next"},
	}, nil)
	if got == nil || got.Label != "click · #next" || got.Target != "#next" {
		t.Fatalf("overlay=%+v", got)
	}
}

func TestResultRectFromTheBoxTheActionReported(t *testing.T) {
	got := resultRect(json.RawMessage(`{"ok":true,"point":{"x":60,"y":30},"rect":{"x":10,"y":20,"width":100,"height":20}}`))
	if got == nil || *got != (rect{X: 10, Y: 20, W: 100, H: 20}) {
		t.Fatalf("rect=%+v", got)
	}
	for _, raw := range []string{`{"ok":true}`, `{"rect":{"x":1,"y":1,"width":0,"height":5}}`, `not json`} {
		if got := resultRect(json.RawMessage(raw)); got != nil {
			t.Fatalf("resultRect(%s)=%+v, want nil", raw, got)
		}
	}
}

func TestSetWatchingWithoutPagesIsNoop(t *testing.T) {
	var mu sync.Mutex
	var events []api.PreviewEvent
	c := NewController(DefaultConfig(), func(_ context.Context, _, _ string, ev api.PreviewEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	out := c.SetWatching(context.Background(), "sess-1", true, "page-1")
	if !out.Watching {
		t.Fatal("expected watching true")
	}
	mu.Lock()
	n := len(events)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("unexpected events: %d", n)
	}
}

func TestSetWatchingRequiresPageID(t *testing.T) {
	c := NewController(DefaultConfig(), nil)
	out := c.SetWatching(context.Background(), "session-1", true, "")
	if out.Watching || len(c.watching) != 0 {
		t.Fatalf("watch result=%+v state=%v want inactive", out, c.watching)
	}
}

func TestPageWatchesRemainIndependent(t *testing.T) {
	c := NewController(DefaultConfig(), nil)
	c.SetWatching(context.Background(), "session-1", true, "page-1")
	c.SetWatching(context.Background(), "session-1", true, "page-2")
	c.SetWatching(context.Background(), "session-1", false, "page-1")

	if c.watching[streamKey("session-1", "page-1")] {
		t.Fatal("page-1 watch remained active")
	}
	if !c.watching[streamKey("session-1", "page-2")] {
		t.Fatal("page-2 watch was removed")
	}
}

func TestFPSCoalesceDropsIntermediateFrames(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxFPS = 2 // 500ms
	var mu sync.Mutex
	var frames int
	c := NewController(cfg, func(_ context.Context, _, _ string, ev api.PreviewEvent) {
		if ev.Op == api.PreviewEventOpFrame {
			mu.Lock()
			frames++
			mu.Unlock()
		}
	})
	configurePreviewProjection(c)
	h := &castHandle{cancel: func() {}, done: make(chan struct{})}
	st := &stream{
		projectID:   "p",
		sessionID:   "s",
		pageID:      "page-1",
		cast:        h,
		minInterval: time.Second / time.Duration(cfg.MaxFPS),
		held:        &browser.HeldPage{Width: 100, Height: 80},
	}
	c.mu.Lock()
	c.pages[streamKey("s", "page-1")] = st
	c.mu.Unlock()

	jpeg := []byte{0xff, 0xd8, 0xff, 0xd9} // minimal jpeg markers
	c.offerFrame(context.Background(), st, h, jpeg, "", "", browser.PageRegions{})
	c.offerFrame(context.Background(), st, h, jpeg, "", "", browser.PageRegions{})
	c.offerFrame(context.Background(), st, h, jpeg, "", "", browser.PageRegions{})
	mu.Lock()
	first := frames
	mu.Unlock()
	if first != 1 {
		t.Fatalf("first burst frames=%d want 1", first)
	}
	time.Sleep(st.minInterval + 20*time.Millisecond)
	c.flushPending(context.Background(), st, h)
	mu.Lock()
	second := frames
	mu.Unlock()
	if second != 2 {
		t.Fatalf("after interval frames=%d want 2", second)
	}
}

func TestPublishActionWithoutStreamIsNoop(t *testing.T) {
	c := NewController(DefaultConfig(), nil)
	c.PublishAction(context.Background(), "s", "p", browser.CaptureAction{Type: "click"}, json.RawMessage(`{"ok":true}`))
}

func TestClaimEmitsAndSnapshotsInvocationAssociation(t *testing.T) {
	var events []api.PreviewEvent
	c := NewController(DefaultConfig(), func(_ context.Context, _, _ string, ev api.PreviewEvent) {
		events = append(events, ev)
	})
	configurePreviewProjection(c)
	c.pages[streamKey("s", "page-1")] = &stream{
		projectID:          "project",
		sessionID:          "s",
		pageID:             "page-1",
		assistantMessageID: "assistant-1",
		toolCallID:         "call-1",
		held:               &browser.HeldPage{TargetURL: "https://example.test"},
	}

	c.Claim(context.Background(), "s", "page-1", "assistant-2", "call-2")

	if len(events) != 1 {
		t.Fatalf("events=%d want 1", len(events))
	}
	if events[0].AssistantMessageID != "assistant-2" || events[0].ToolCallID != "call-2" {
		t.Fatalf("event association=%+v", events[0])
	}
	attachments := c.SnapshotForSession(context.Background(), "s")
	if len(attachments) != 1 || attachments[0].AssistantMessageID != "assistant-2" || attachments[0].ToolCallID != "call-2" {
		t.Fatalf("attachments=%+v", attachments)
	}
}

func TestSnapshotProjectsMetadataInTheAttachmentProject(t *testing.T) {
	var primedProject string
	c := NewController(DefaultConfig(), nil)
	c.SetCaptureProjector(captureprojection.New(
		secretmatch.NewInertMatcher(),
		func(_ context.Context, projectID, _ string) error {
			primedProject = projectID
			return nil
		},
	))
	c.pages[streamKey("s", "page-1")] = &stream{
		projectID: "project-1", sessionID: "s", parentSessionID: "root-1", pageID: "page-1",
		held: &browser.HeldPage{TargetURL: "https://example.test"},
	}

	attachments := c.SnapshotForSession(context.Background(), "s")
	if len(attachments) != 1 {
		t.Fatalf("attachments=%d want 1", len(attachments))
	}
	if primedProject != "project-1" {
		t.Fatalf("primed project=%q want project-1", primedProject)
	}
}

func TestPublishDrivingEmitsStateAndMarksFramesBusy(t *testing.T) {
	var mu sync.Mutex
	var events []api.PreviewEvent
	c := NewController(DefaultConfig(), func(_ context.Context, _, _ string, ev api.PreviewEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	configurePreviewProjection(c)
	h := &castHandle{cancel: func() {}, done: make(chan struct{})}
	st := &stream{
		sessionID:   "s",
		pageID:      "page-1",
		cast:        h,
		minInterval: time.Millisecond,
		held:        &browser.HeldPage{Width: 100, Height: 80},
	}
	c.mu.Lock()
	c.pages[streamKey("s", "page-1")] = st
	c.mu.Unlock()

	c.PublishDriving(context.Background(), "s", "page-1", true)
	c.offerFrame(context.Background(), st, h, []byte{0xff, 0xd8, 0xff, 0xd9}, "", "", browser.PageRegions{})
	c.PublishDriving(context.Background(), "s", "page-1", false)

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 3 {
		t.Fatalf("events=%d want 3", len(events))
	}
	if events[0].Op != api.PreviewEventOpState || events[0].Idle == nil || *events[0].Idle {
		t.Fatalf("driving-start state=%+v want idle=false", events[0])
	}
	if events[1].Op != api.PreviewEventOpFrame || events[1].Idle == nil || *events[1].Idle {
		t.Fatalf("mid-drive frame=%+v want idle=false", events[1])
	}
	if events[2].Op != api.PreviewEventOpState || events[2].Idle == nil || !*events[2].Idle {
		t.Fatalf("driving-end state=%+v want idle=true", events[2])
	}
}

func TestStopCastClearsPendingFrame(t *testing.T) {
	c := NewController(DefaultConfig(), func(_ context.Context, _, _ string, _ api.PreviewEvent) {
		t.Fatal("no event expected after stop")
	})
	done := make(chan struct{})
	close(done)
	h := &castHandle{cancel: func() {}, done: done}
	st := &stream{
		sessionID:   "s",
		pageID:      "page-1",
		cast:        h,
		minInterval: time.Hour, // hold the frame pending
		held:        &browser.HeldPage{},
		lastPublish: time.Now(),
	}
	c.mu.Lock()
	c.pages[streamKey("s", "page-1")] = st
	c.mu.Unlock()

	c.offerFrame(context.Background(), st, h, []byte{0xff, 0xd8}, "", "", browser.PageRegions{})
	c.mu.Lock()
	stop := c.stopCastLocked(t.Context(), st)
	pendingCleared := st.pending == nil
	castCleared := st.cast == nil
	c.mu.Unlock()
	stop()
	if !pendingCleared || !castCleared {
		t.Fatalf("pendingCleared=%v castCleared=%v want both true", pendingCleared, castCleared)
	}
	c.flushPending(context.Background(), st, h)
}

func TestPendingFrameIsMaskedAgainstItsOwnPageState(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxFPS = 2
	c := NewController(cfg, func(context.Context, string, string, api.PreviewEvent) {})
	c.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	var mu sync.Mutex
	var seen []browser.PageRegions
	c.projectFrame = func(
		_ context.Context, _ *captureprojection.Projector, _ captureprojection.Scope,
		regions browser.PageRegions, _ string, raw []byte,
	) ([]byte, error) {
		mu.Lock()
		seen = append(seen, regions)
		mu.Unlock()
		return append([]byte(nil), raw...), nil
	}
	h := &castHandle{cancel: func() {}, done: make(chan struct{})}
	st := &stream{
		projectID: "p", sessionID: "s", pageID: "page-1", cast: h,
		minInterval: time.Second / time.Duration(cfg.MaxFPS),
		held:        &browser.HeldPage{Width: 100, Height: 80},
		lastPublish: time.Now(),
	}
	c.mu.Lock()
	c.pages[streamKey("s", "page-1")] = st
	c.mu.Unlock()

	captured := browser.PageRegions{
		Regions: []captureprojection.Region{{
			Text: "token", Spans: []captureprojection.TextSpan{{
				Start: 0, End: 1, Rects: []captureprojection.Rect{{X: 1, Y: 2, Width: 3, Height: 4}},
			}},
		}},
		Complete: true, Width: 100, Height: 80,
	}
	c.offerFrame(context.Background(), st, h, []byte{0xff, 0xd8}, "", "", captured)
	time.Sleep(st.minInterval + 20*time.Millisecond)
	c.flushPending(context.Background(), st, h)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("projected frames = %d, want 1", len(seen))
	}
	if len(seen[0].Regions) != 1 || seen[0].Regions[0].Text != "token" {
		t.Fatalf("frame was masked against %+v, want the geometry captured with it", seen[0])
	}
	if seen[0].Width != captured.Width || seen[0].Height != captured.Height {
		t.Fatalf("mask extent = %vx%v, want %vx%v", seen[0].Width, seen[0].Height, captured.Width, captured.Height)
	}
}

func TestAcceptFrameBoundsGeometryReads(t *testing.T) {
	c := NewController(DefaultConfig(), nil)
	h := &castHandle{cancel: func() {}, done: make(chan struct{})}
	st := &stream{sessionID: "s", pageID: "page-1", cast: h, minInterval: time.Hour}
	c.mu.Lock()
	c.pages[streamKey("s", "page-1")] = st
	c.mu.Unlock()
	if !c.acceptFrame(st, h) {
		t.Fatal("first frame was not accepted")
	}
	if c.acceptFrame(st, h) {
		t.Fatal("second frame inside the window was accepted")
	}
}
