package confine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRefusalSource echoes host markers unless muted.
type fakeRefusalSource struct {
	mu                sync.Mutex
	writer            *io.PipeWriter
	opened            chan struct{}
	opens             atomic.Int32
	muted             atomic.Bool
	failing           atomic.Bool
	blocked           atomic.Bool
	probing           chan struct{}
	probeCheck        func(context.Context)
	reportBeforeBlock bool
}

func newFakeRefusalSource() *fakeRefusalSource {
	return &fakeRefusalSource{opened: make(chan struct{}, 8)}
}

func (s *fakeRefusalSource) open(ctx context.Context, _ int) (io.ReadCloser, func() error, error) {
	s.opens.Add(1)
	if s.failing.Load() {
		reader, writer := io.Pipe()
		_ = writer.Close()
		return reader, func() error { return errors.New("log: Must be admin to run 'stream' command") }, nil
	}
	reader, writer := io.Pipe()
	s.mu.Lock()
	s.writer = writer
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		_ = writer.Close()
	}()
	s.opened <- struct{}{}
	return reader, func() error { return nil }, nil
}

func (s *fakeRefusalSource) mark(message string) {
	if s.muted.Load() {
		return
	}
	s.emit(streamEvent{EventType: "logEvent", EventMessage: message, ProcessID: os.Getpid()})
}

// probe appends a refusal after preceding stream events.
func (s *fakeRefusalSource) probe(ctx context.Context, tag string) error {
	if s.probeCheck != nil {
		s.probeCheck(ctx)
	}
	if s.blocked.Load() {
		if s.reportBeforeBlock {
			s.refuse(tag, "Sandbox: lycaon(1) deny(1) file-write-create /tmp/pwc1-settle")
			s.refuse(tag, "Sandbox: lycaon(1) deny(1) file-write-create /tmp/pwc1-settle")
		}
		if s.probing != nil {
			close(s.probing)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	if !s.muted.Load() {
		s.refuse(tag, "Sandbox: lycaon(1) deny(1) file-write-create /tmp/pwc1-settle")
	}
	return nil
}

func TestRefusalSettleBoundsTheProbeItself(t *testing.T) {
	w, source := liveWatch(t)
	feed := w.register(testTag, FilesystemRules{})
	source.blocked.Store(true)
	done := make(chan struct{})
	go func() {
		w.settle(t.Context(), feed)
		close(done)
	}()
	select {
	case <-done:
		if feed.snapshot().Witness != WitnessIncomplete {
			t.Fatal("a timed out probe retained a kernel witness")
		}
	case <-time.After(2 * refusalSettleTimeout):
		t.Fatal("settle deadline did not bound the probe")
	}
}

func TestRefusalSettlementRetainsCanceledActionContext(t *testing.T) {
	type actionKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), actionKey{}, "action-owner"))
	cancel()
	w, source := liveWatch(t)
	feed := w.register(testTag, FilesystemRules{})
	checked := false
	source.probeCheck = func(probeCtx context.Context) {
		checked = true
		if probeCtx.Value(actionKey{}) != "action-owner" || probeCtx.Err() != nil {
			t.Errorf("settlement lost action values or inherited cancellation: %v", probeCtx)
		}
		if _, ok := probeCtx.Deadline(); !ok {
			t.Error("settlement probe has no deadline")
		}
	}
	w.settle(ctx, feed)
	if !checked || feed.snapshot().Witness != WitnessKernel {
		t.Fatalf("canceled action failed to settle its kernel reports: checked=%v witness=%q", checked, feed.snapshot().Witness)
	}
}

func TestRefusalWatchLossCancelsPendingSettlement(t *testing.T) {
	for _, tc := range []struct {
		name           string
		stop, reported bool
	}{
		{"watch stopped before report", true, false},
		{"watch stopped after report", true, true},
		{"stream ended before report", false, false},
		{"stream ended after report", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, source := liveWatch(t)
			feed := w.register(testTag, FilesystemRules{})
			source.blocked.Store(true)
			source.reportBeforeBlock = tc.reported
			source.probing = make(chan struct{})
			done := make(chan struct{})
			go func() {
				w.settle(t.Context(), feed)
				close(done)
			}()
			select {
			case <-source.probing:
			case <-time.After(refusalSettleTimeout):
				t.Fatal("settlement probe did not start")
			}
			if tc.stop {
				w.stop()
			} else {
				source.endStream()
			}
			select {
			case <-done:
			case <-time.After(refusalSettleTimeout / 2):
				t.Fatal("stream loss did not cancel its settlement probe")
			}
			if got := feed.snapshot().Witness; got != WitnessIncomplete {
				t.Fatalf("stream-loss witness = %q, want incomplete", got)
			}
			w.mu.Lock()
			pending := len(w.probes)
			w.mu.Unlock()
			if pending != 0 {
				t.Fatalf("stream loss retained %d settlement probes", pending)
			}
		})
	}
}

func (s *fakeRefusalSource) emit(ev streamEvent) {
	s.mu.Lock()
	writer := s.writer
	s.mu.Unlock()
	if writer == nil {
		return
	}
	line, _ := json.Marshal(ev)
	_, _ = writer.Write(append(line, '\n'))
}

func (s *fakeRefusalSource) refuse(tag, report string) {
	s.emit(streamEvent{
		EventType: "logEvent", ProcessImagePath: "/kernel",
		SenderImagePath: "/System/Library/Extensions" + sandboxSenderSuffix,
		EventMessage:    report + "\n" + refusalTagPrefix + tag,
	})
}

// endStream closes the current stream as if the log process exited.
func (s *fakeRefusalSource) endStream() {
	s.mu.Lock()
	writer := s.writer
	s.writer = nil
	s.mu.Unlock()
	if writer != nil {
		_ = writer.Close()
	}
}

func newTestWatch(t *testing.T, source *fakeRefusalSource) *refusalWatchT {
	t.Helper()
	w := &refusalWatchT{feeds: map[string]*refusalFeed{}, probes: map[string]*refusalProbe{}}
	w.start(t.Context(), source)
	t.Cleanup(w.stop)
	return w
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func liveWatch(t *testing.T) (*refusalWatchT, *fakeRefusalSource) {
	t.Helper()
	source := newFakeRefusalSource()
	w := newTestWatch(t, source)
	waitFor(t, "stream live", func() bool { return w.currentState() == watchLive })
	return w, source
}

func TestRefusalWatchRoutesReportsToTheirAction(t *testing.T) {
	w, source := liveWatch(t)
	rules, _ := projectRules(t)
	feed := w.register(testTag, rules)
	other := w.register("fedcba9876543210fedcba9876543210", rules)
	heard := atomic.Int32{}
	feed.listen(func() { heard.Add(1) })

	source.refuse(testTag, "Sandbox: limactl(9) deny(1) network-bind /tmp/u.sock")
	source.refuse(testTag, "2 duplicate reports for Sandbox: limactl(9) deny(1) network-bind /tmp/u.sock")
	w.settle(t.Context(), feed)

	got := feed.snapshot()
	if got.Witness != WitnessKernel || len(got.Refusals) != 1 {
		t.Fatalf("feed = %+v, want one kernel-witnessed refusal", got)
	}
	if r := got.Refusals[0]; r.Count != 3 || r.Process != "limactl" || r.Recovery != RecoverHostExecution {
		t.Fatalf("refusal = %+v", r)
	}
	if heard.Load() != 1 {
		t.Fatalf("listener heard %d times, want once for one distinct refusal", heard.Load())
	}
	if len(other.snapshot().Refusals) != 0 {
		t.Fatal("another action's feed received the report")
	}
}

func TestRefusalWitnessDegradesWhenReportsMayBeMissing(t *testing.T) {
	source := newFakeRefusalSource()
	source.muted.Store(true)
	w := newTestWatch(t, source)
	<-source.opened
	early := w.register(testTag, FilesystemRules{})
	if early.snapshot().Witness != WitnessIncomplete {
		t.Fatalf("an action bound before the stream was live claimed %s", early.snapshot().Witness)
	}
	source.muted.Store(false)
	waitFor(t, "stream live", func() bool { return w.currentState() == watchLive })

	late := w.register("fedcba9876543210fedcba9876543210", FilesystemRules{})
	source.muted.Store(true)
	start := time.Now()
	w.settle(t.Context(), late)
	if late.snapshot().Witness != WitnessIncomplete || time.Since(start) < refusalSettleTimeout {
		t.Fatalf("a settle whose probe never streamed left witness %s", late.snapshot().Witness)
	}
}

func TestRefusalWitnessDegradesOnLossAndRestart(t *testing.T) {
	w, source := liveWatch(t)
	lossy := w.register(testTag, FilesystemRules{})
	source.emit(streamEvent{EventType: "lossEvent"})
	waitFor(t, "loss applied", func() bool { return lossy.snapshot().Witness == WitnessIncomplete })

	survivor := w.register("fedcba9876543210fedcba9876543210", FilesystemRules{})
	source.endStream()
	waitFor(t, "restart degrades", func() bool { return survivor.snapshot().Witness == WitnessIncomplete })
	waitFor(t, "stream reopened", func() bool { return source.opens.Load() >= 2 && w.currentState() == watchLive })
}

func TestRefusalWatchGivesUpWhenTheStreamNeverGoesLive(t *testing.T) {
	source := newFakeRefusalSource()
	source.failing.Store(true)
	w := newTestWatch(t, source)
	waitFor(t, "unavailable", func() bool { return w.currentState() == watchUnavailable })
	if got := w.register(testTag, FilesystemRules{}).snapshot().Witness; got != WitnessUnavailable {
		t.Fatalf("witness = %s, want unavailable", got)
	}
	if source.opens.Load() != refusalStreamFailures {
		t.Fatalf("opened %d streams, want %d", source.opens.Load(), refusalStreamFailures)
	}
}

func TestRefusalFeedBoundsDistinctRefusals(t *testing.T) {
	feed := &refusalFeed{witness: WitnessKernel, keys: map[string]int{}}
	for i := range maxActionRefusals + 5 {
		feed.record(kernelRefusal{Operation: "file-write-create", Target: "/tmp/f" + strconv.Itoa(i), Count: 1})
	}
	feed.record(kernelRefusal{Operation: "file-write-create", Target: "/tmp/f0", Count: 1})
	got := feed.snapshot()
	if len(got.Refusals) != maxActionRefusals || got.Omitted != 5 || got.Refusals[0].Count != 2 {
		t.Fatalf("feed kept %d refusals, omitted %d, first count %d", len(got.Refusals), got.Omitted, got.Refusals[0].Count)
	}
}
