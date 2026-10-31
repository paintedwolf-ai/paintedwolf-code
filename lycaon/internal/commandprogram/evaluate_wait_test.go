package commandprogram

import (
	"context"
	"errors"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

type waitObservations struct {
	mu      sync.Mutex
	states  []WaitState
	active  bool
	refused error
}

func (o *waitObservations) Block(_ context.Context, state WaitState) (func(), error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.states = append(o.states, state)
	o.active = true
	return func() { o.mu.Lock(); o.active = false; o.mu.Unlock() }, o.refused
}
func newWaitActivities(t *testing.T) (*executionWaits, *ExecutionWait, *waitObservations) {
	t.Helper()
	observed := &waitObservations{}
	ctx, cancel := context.WithCancelCause(t.Context())
	w := newExecutionWaits(ctx, newManualClock(), observed, cancel)
	root := w.start()
	w.foreground = root
	t.Cleanup(root.finish)
	return w, root, observed
}
func blockActivity(t *testing.T, a *ExecutionWait, state WaitState) func() {
	t.Helper()
	done, err := a.Block(t.Context(), state)
	testutil.FailErr(t, "block evaluator activity", err)
	return done
}
func TestExecutionWaitsRequireEveryActivityBlocked(t *testing.T) {
	w, root, observed := newWaitActivities(t)
	child := w.start()
	defer child.finish()
	until := root.Now().Add(120 * time.Second)
	unblock := blockActivity(t, root, WaitState{Statement: "wait %1", Jobs: []string{"job-b", "job-a", "job-b"}})
	defer unblock()
	if len(observed.states) != 0 {
		t.Fatal("runnable child published a wait")
	}
	childUnblock := blockActivity(t, child, WaitState{Statement: "sleep 120", Until: until})
	defer childUnblock()
	state := observed.states[len(observed.states)-1]
	if !state.Until.Equal(until) || state.Statement != "wait %1" || len(state.Jobs) != 2 || state.Jobs[0] != "job-a" || state.Jobs[1] != "job-b" {
		t.Fatalf("aggregate %+v", state)
	}
	childUnblock()
	if observed.active {
		t.Fatal("a runnable child left a wait armed")
	}
	child.finish()
	if !observed.active {
		t.Fatal("child completion did not expose remaining job wait")
	}
	root.finish()
	if observed.active {
		t.Fatal("foreground completion left background work parked")
	}
}
func TestExecutionWaitsHandleOverlappingBlocksAndOldUnblocks(t *testing.T) {
	_, root, observed := newWaitActivities(t)
	outer := blockActivity(t, root, WaitState{Statement: "sleep 120", Until: root.Now().Add(120 * time.Second)})
	inner := blockActivity(t, root, WaitState{Jobs: []string{"job-b"}})
	outer()
	if !observed.active {
		t.Fatal("old unblock cleared the active stream wait")
	}
	inner()
	if observed.active {
		t.Fatal("completed overlapping waits restored an ended wait")
	}
	outer()
	inner()
	if observed.active {
		t.Fatal("repeated unblock revived a wait")
	}
}
func TestExecutionWaitDeadlineScopesTheActualBlockingOperation(t *testing.T) {
	_, root, observed := newWaitActivities(t)
	restore := root.Deadline(root.Now().Add(60*time.Second), "timeout 60 cmd")
	unblock := blockActivity(t, root, WaitState{})
	if got := observed.states[0]; got.Statement != "timeout 60 cmd" || !got.Until.Equal(root.Now().Add(60*time.Second)) {
		t.Fatalf("deadline %+v", got)
	}
	unblock()
	restore()
	unblock = blockActivity(t, root, WaitState{})
	defer unblock()
	if observed.active {
		t.Fatal("ordinary supervised completion inherited a timeout deadline")
	}
}
func TestExecutionWaitCompletionPropagatesARefusedAggregate(t *testing.T) {
	w, root, observed := newWaitActivities(t)
	child := w.start()
	unblock := blockActivity(t, root, WaitState{Until: root.Now().Add(time.Hour)})
	defer unblock()
	observed.refused = errors.New("bounded wait refused")
	child.finish()
	if !errors.Is(w.err, observed.refused) {
		t.Fatalf("aggregate error %v", w.err)
	}
}

type firedOnRegistrationClock struct{ now time.Time }

func (c *firedOnRegistrationClock) Now() time.Time { return c.now }
func (c *firedOnRegistrationClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.now = c.now.Add(d)
	f()
	return func() bool { return false }
}
func TestExecutionWaitAlreadyReadyTimerNeverReportsBlocked(t *testing.T) {
	_, root, observed := newWaitActivities(t)
	root.set.clock = &firedOnRegistrationClock{now: root.Now()}
	testutil.FailErr(t, "wait for already ready timer", root.Until(t.Context(), root.Now().Add(time.Minute), "sleep 60"))
	if len(observed.states) != 0 {
		t.Fatalf("ready timer published %+v", observed.states)
	}
}

func awaitPipeState(t *testing.T, p *pipeRendezvous, ready func(*pipeRendezvous) bool) {
	t.Helper()
	for {
		p.mu.Lock()
		ok := ready(p)
		p.mu.Unlock()
		if ok {
			return
		}
		runtime.Gosched()
	}
}
func TestRendezvousPublicationMakesReaderRunnableBeforeWriterBlocks(t *testing.T) {
	w, reader, observed := newWaitActivities(t)
	writer := w.start()
	defer writer.finish()
	restore := writer.Deadline(writer.Now().Add(time.Minute), "timeout 60 producer")
	defer restore()
	p := newPipeRendezvous(t.Context())
	got := make(chan string, 1)
	go func() { b := make([]byte, 2); n, _ := p.read(t.Context(), b, reader); got <- string(b[:n]) }()
	awaitPipeState(t, p, func(p *pipeRendezvous) bool { return len(p.readers) == 1 })
	written := make(chan error, 1)
	go func() { _, err := p.write([]byte("ok"), writer); written <- err }()
	if value := <-got; value != "ok" {
		t.Fatalf("read %q", value)
	}
	testutil.FailErr(t, "write ready stream", <-written)
	if len(observed.states) != 0 {
		t.Fatalf("ready stream reported blocked %+v", observed.states)
	}
}
func TestInheritedPipeEndpointUsesTheCurrentActivity(t *testing.T) {
	w, parent, _ := newWaitActivities(t)
	child := w.start()
	defer child.finish()
	budget, err := NewByteBudget("stream_bytes", 20)
	testutil.FailErr(t, "pipe budget", err)
	pipe, err := NewBytePipe(t.Context(), budget)
	testutil.FailErr(t, "pipe", err)
	defer pipe.CloseConsumer()
	inherited := Streams{Input: pipeConsumer{pipe: pipe, wait: parent}}
	rebound := inherited.WithActivity(child)
	got := make(chan error, 1)
	go func() { _, err := rebound.Input.Read(make([]byte, 1)); got <- err }()
	awaitPipeState(t, pipe.stream, func(p *pipeRendezvous) bool { return len(p.readers) == 1 })
	w.mu.Lock()
	childBlocked, parentBlocked := w.live[child] != nil, w.live[parent] != nil
	w.mu.Unlock()
	if !childBlocked || parentBlocked {
		t.Fatalf("child blocked=%v parent blocked=%v", childBlocked, parentBlocked)
	}
	testutil.FailErr(t, "close inherited endpoint", pipe.CloseProducer(nil))
	if err := <-got; !errors.Is(err, io.EOF) {
		t.Fatalf("read close %v", err)
	}
}

func TestExecutionWaitsDiscardEndedTimersAndJobsFromOverlappingBlocks(t *testing.T) {
	_, root, observed := newWaitActivities(t)
	first := blockActivity(t, root, WaitState{Statement: "first", Until: root.Now().Add(5 * time.Second), Jobs: []string{"job-a"}})
	second := blockActivity(t, root, WaitState{Statement: "second", Until: root.Now().Add(120 * time.Second), Jobs: []string{"job-b"}})
	first()
	got := observed.states[len(observed.states)-1]
	if !got.Until.Equal(root.Now().Add(120*time.Second)) || len(got.Jobs) != 1 || got.Jobs[0] != "job-b" {
		t.Fatalf("ended blocker retained %+v", got)
	}
	second()
	if observed.active {
		t.Fatal("ended overlapping waits remain armed")
	}
}

func TestCanceledStreamReadKeepsItsEndpointUsable(t *testing.T) {
	_, activity, _ := newWaitActivities(t)
	budget, err := NewByteBudget("stream_bytes", 100)
	testutil.FailErr(t, "pipe budget", err)
	pipe, err := NewBytePipe(t.Context(), budget)
	testutil.FailErr(t, "pipe", err)
	defer pipe.CloseConsumer()
	input := pipeConsumer{pipe: pipe, wait: activity}
	ctx, cancel := context.WithCancel(t.Context())
	ended := make(chan error, 1)
	go func() { _, err := input.ReadContext(ctx, make([]byte, 1)); ended <- err }()
	awaitPipeState(t, pipe.stream, func(p *pipeRendezvous) bool { return len(p.readers) == 1 })
	cancel()
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read %v", err)
	}
	if pipe.Completion().Termination != StreamOpen {
		t.Fatalf("read cancellation closed pipe %+v", pipe.Completion())
	}
	wrote := make(chan error, 1)
	go func() { _, err := pipe.Write([]byte("ready")); wrote <- err }()
	got := make([]byte, 5)
	_, err = io.ReadFull(input, got)
	testutil.FailErr(t, "read after timeout", err)
	testutil.FailErr(t, "complete writer", <-wrote)
	if string(got) != "ready" {
		t.Fatalf("read %q", got)
	}
}
func TestZeroTimeReadinessIncludesEOFAndDoesNotConsumeBytes(t *testing.T) {
	if !inputReady(NullInput()) {
		t.Fatal("null input EOF is not ready")
	}
	budget, err := NewByteBudget("stream_bytes", 100)
	testutil.FailErr(t, "pipe budget", err)
	pipe, err := NewBytePipe(t.Context(), budget)
	testutil.FailErr(t, "pipe", err)
	input := pipe.ConsumerEnd()
	if inputReady(input) {
		t.Fatal("empty live pipe is ready")
	}
	wrote := make(chan error, 1)
	go func() { _, err := pipe.Write([]byte("ready")); wrote <- err }()
	awaitPipeState(t, pipe.stream, func(p *pipeRendezvous) bool { return len(p.data) > 0 })
	firstReady := inputReady(input)
	secondReady := inputReady(input)
	if !firstReady || !secondReady {
		t.Fatal("pending bytes are not ready")
	}
	got := make([]byte, 5)
	_, err = io.ReadFull(input, got)
	testutil.FailErr(t, "read after polling", err)
	testutil.FailErr(t, "complete writer", <-wrote)
	testutil.FailErr(t, "finish pipe", pipe.CloseProducer(nil))
	if string(got) != "ready" || !inputReady(input) {
		t.Fatalf("data %q EOF ready %v", got, inputReady(input))
	}
}

func TestExecutionWaitForkInheritsDeadlinesAndKeepsTheParentRunnable(t *testing.T) {
	_, root, observed := newWaitActivities(t)
	restore := root.Deadline(root.Now().Add(time.Minute), "timeout 60 xargs")
	defer restore()
	child, finish := root.Fork()
	defer finish()
	blocked := blockActivity(t, child, WaitState{})
	defer blocked()
	if observed.active {
		t.Fatal("waiting child parked its runnable parent")
	}
	parentBlocked := blockActivity(t, root, WaitState{})
	defer parentBlocked()
	got := observed.states[len(observed.states)-1]
	if got.Statement != "timeout 60 xargs" || !got.Until.Equal(root.Now().Add(time.Minute)) {
		t.Fatalf("inherited deadline %+v", got)
	}
}

func TestExecutionWaitProcessNeedsAnExplicitDeadlineOrAwaitedJob(t *testing.T) {
	w, root, observed := newWaitActivities(t)
	background, finish := root.Fork()
	defer finish()
	sleeping := blockActivity(t, background, WaitState{Statement: "sleep 120", Until: root.Now().Add(120 * time.Second)})
	defer sleeping()
	process, err := root.Process(t.Context())
	testutil.FailErr(t, "observe foreground process", err)
	if observed.active {
		t.Fatal("ordinary process became waiting beside an unrelated timer")
	}
	process()
	sleeping()
	background.Job("exact-job")
	waiting, err := background.Process(t.Context())
	testutil.FailErr(t, "observe background process", err)
	defer waiting()
	awaited := blockActivity(t, root, WaitState{Statement: "wait %1", Jobs: []string{"exact-job"}})
	defer awaited()
	if !observed.active {
		t.Fatal("exact supervised job wait did not publish")
	}
	got := observed.states[len(observed.states)-1]
	if len(got.Jobs) != 1 || got.Jobs[0] != "exact-job" {
		t.Fatalf("wait %+v", got)
	}
	child, release := background.Fork()
	defer release()
	w.mu.Lock()
	job := child.job
	w.mu.Unlock()
	if job != "exact-job" {
		t.Fatalf("nested activity job=%q", job)
	}
}
