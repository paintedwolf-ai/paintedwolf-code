package bgprocess

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

const projectionSecret = "capture-secret-value"

func projectionProjector() *captureprojection.Projector {
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Secret: projectionSecret,
			RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
			Source: secretmatch.SourceRememberedMatch, NonDisclosable: true,
		}}
	})
	return captureprojection.New(m, nil)
}

// projectionHarness exposes deterministic process writes and publications.
type projectionHarness struct {
	reg  *Registry
	proc *Process

	mu     sync.Mutex
	events []api.BackgroundProcessEvent
}

func newProjectionHarness(t *testing.T, ringBytes int) *projectionHarness {
	t.Helper()
	h := &projectionHarness{}
	h.reg = newTestRegistry(t, Config{RingBufferBytes: ringBytes}, Hooks{
		Publish: func(_ context.Context, _, _ string, ev api.BackgroundProcessEvent) {
			h.mu.Lock()
			h.events = append(h.events, ev)
			h.mu.Unlock()
		},
	})
	h.reg.SetCaptureProjector(projectionProjector())
	h.proc = &Process{
		Handle: "h1", SessionID: "s1", ProjectID: "p1", RootSessionID: "s1",
		buffer: NewRingBuffer(ringBytes), running: true, done: make(chan struct{}),
	}
	h.reg.mu.Lock()
	h.reg.sessions["s1"] = map[string]*Process{"h1": h.proc}
	h.reg.mu.Unlock()
	// Close the synthetic process before registry cleanup.
	t.Cleanup(func() { close(h.proc.done) })
	return h
}

func (h *projectionHarness) write(stream, text string) {
	cursor := h.proc.buffer.Append(stream, []byte(text))
	h.reg.publishStream(context.Background(), h.proc, stream, cursor)
}

func (h *projectionHarness) published() []api.BackgroundProcessEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]api.BackgroundProcessEvent(nil), h.events...)
}

func TestEvictionNeverPublishesTheTailOfAStraddlingValue(t *testing.T) {
	h := newProjectionHarness(t, 28)
	h.write("stdout", strings.Repeat("A", 16))
	h.write("stdout", "capture-")
	h.write("stdout", "secret-value")
	h.write("stdout", strings.Repeat("B", 12))
	// Move the window head into the protected value.
	h.write("stdout", strings.Repeat("C", 12))

	if head := h.proc.buffer.Head(); head != 24 {
		t.Fatalf("window head = %d, want the fixture's mid-value boundary at 24", head)
	}
	for _, ev := range h.published() {
		if strings.Contains(ev.Text, "secret-value") {
			t.Fatalf("published the tail of an evicted value: %q", ev.Text)
		}
	}
}

func TestReadOutputWithholdsTheTailOfAnEvictedValue(t *testing.T) {
	h := newProjectionHarness(t, 28)
	h.write("stdout", strings.Repeat("A", 16))
	h.write("stdout", "capture-")
	h.write("stdout", "secret-value")
	h.write("stdout", strings.Repeat("B", 12))
	h.write("stdout", strings.Repeat("C", 12))

	snapshot, err := h.reg.ReadOutput(context.Background(), "s1", "h1")
	if err != nil {
		t.Fatalf("read projected output: %v", err)
	}
	var text strings.Builder
	for _, chunk := range snapshot.Chunks {
		text.WriteString(chunk.Text)
	}
	if strings.Contains(text.String(), "secret-value") {
		t.Fatalf("reload projection resurrected the evicted tail: %q", text.String())
	}
}

func TestConcurrentStreamWritersNeverRegressTheProjection(t *testing.T) {
	h := newProjectionHarness(t, DefaultRingBufferBytes)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.write("stdout", "capture-")
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.write("stderr", "secret-value")
		}()
	}
	wg.Wait()

	var live string
	for _, ev := range h.published() {
		if strings.Contains(ev.Text, projectionSecret) {
			t.Fatalf("concurrent publication exposed the value: %q", ev.Text)
		}
		if ev.Reset {
			live = ev.Text
		} else {
			live += ev.Text
		}
	}
	if strings.Contains(live, projectionSecret) {
		t.Fatalf("assembled live stream exposed the value: %q", live)
	}
	final, err := h.reg.ReadOutput(context.Background(), "s1", "h1")
	if err != nil {
		t.Fatalf("read projected output: %v", err)
	}
	for _, chunk := range final.Chunks {
		if strings.Contains(chunk.Text, projectionSecret) {
			t.Fatalf("final projection exposed the value: %q", chunk.Text)
		}
	}
}

func TestProjectScreenMasksAValueThatWrapsATerminalRow(t *testing.T) {
	reg := newTestRegistry(t, DefaultConfig(), Hooks{})
	reg.SetCaptureProjector(projectionProjector())
	screen := ScreenSnapshot{
		Cols: 12, Rows: 3, CursorCol: 8, CursorRow: 2,
		Lines: []string{
			"ready       ",
			projectionSecret[:12],
			projectionSecret[12:] + "    ",
		},
	}
	safe, err := reg.ProjectScreen(context.Background(), captureprojection.Scope{}, screen)
	if err != nil {
		t.Fatalf("project screen: %v", err)
	}
	for _, half := range []string{projectionSecret[:12], projectionSecret[12:]} {
		if strings.Contains(strings.Join(safe.Lines, ""), half) {
			t.Fatalf("wrapped value survived the grid projection: %q", safe.Lines)
		}
	}
	if safe.Cols != screen.Cols || safe.Rows != screen.Rows ||
		safe.CursorCol != screen.CursorCol || safe.CursorRow != screen.CursorRow {
		t.Fatalf("grid geometry changed: %+v", safe)
	}
	for i, line := range safe.Lines {
		if len([]rune(line)) != screen.Cols {
			t.Fatalf("row %d width = %d, want %d columns", i, len([]rune(line)), screen.Cols)
		}
	}
}

// TestForegroundSnapshotTailIsScreenedBeforeItIsCut covers split values.
func TestForegroundSnapshotTailIsScreenedBeforeItIsCut(t *testing.T) {
	h := newProjectionHarness(t, DefaultRingBufferBytes)
	h.write("stdout", strings.Repeat("x", 100))
	// Split the value across writes and the output cap.
	h.write("stdout", "capture-")
	h.write("stdout", "secret-value")

	snap, err := h.reg.Snapshot(context.Background(), "s1", "h1", 16)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if strings.Contains(snap.Tail, "secret-value") {
		t.Fatalf("foreground tail carried the value: %q", snap.Tail)
	}
	if !strings.Contains(snap.Tail, "[REDACTED]") {
		t.Fatalf("foreground tail was not screened: %q", snap.Tail)
	}
}

// TestUnwiredProjectorIsAWiringStateNotAFailedScreen covers absent projection.
func TestUnwiredProjectorIsAWiringStateNotAFailedScreen(t *testing.T) {
	h := newProjectionHarness(t, DefaultRingBufferBytes)
	h.reg.SetCaptureProjector(nil)
	h.write("stdout", "plain output")

	snap, err := h.reg.Snapshot(context.Background(), "s1", "h1", 64)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !strings.Contains(snap.Tail, "plain output") {
		t.Fatalf("unwired host lost its tail: %q", snap.Tail)
	}
}

// TestFailedScreeningSuppressesTheTail covers failed projection.
func TestFailedScreeningSuppressesTheTail(t *testing.T) {
	h := newProjectionHarness(t, DefaultRingBufferBytes)
	failing := captureprojection.New(
		secretmatch.NewInertMatcher(),
		func(context.Context, string, string) error { return errUnavailablePrimer },
	)
	h.reg.SetCaptureProjector(failing)
	h.proc.ProjectID = "p1"
	h.proc.RootSessionID = "s1"
	h.write("stdout", "plain output")

	snap, err := h.reg.Snapshot(context.Background(), "s1", "h1", 64)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Tail != captureUnavailableText {
		t.Fatalf("failed screening published %q, want the suppressed marker", snap.Tail)
	}
}

var errUnavailablePrimer = errors.New("managed secret priming failed")
