package confine

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/lineage"
)

const (
	// refusalTagBytes is the random tag length each action's deny rules carry.
	refusalTagBytes = 16
	// maxActionRefusals bounds the distinct refusals one action retains.
	maxActionRefusals = 32
	// maxOmittedRefusalKeys bounds the distinct refusals counted past the cap.
	maxOmittedRefusalKeys = 1024
	// refusalSettleTimeout bounds the wait for an action's last reports.
	refusalSettleTimeout = 2 * time.Second
	// refusalReadyInterval spaces the markers that prove the stream is live.
	refusalReadyInterval = 250 * time.Millisecond
	// refusalStreamFailures caps consecutive starts that fail to become live.
	refusalStreamFailures = 3
	// refusalRestartDelay spaces restarts of a stream that had been live.
	refusalRestartDelay = time.Second
)

// refusalSource is the platform's stream of kernel sandbox reports.
type refusalSource interface {
	// open starts a stream of ndjson log events. wait reaps the stream after
	// its reader ends and reports why it ended.
	open(ctx context.Context, selfPID int) (stream io.ReadCloser, wait func() error, err error)
	// mark writes message where the stream's predicate admits it; it proves
	// the stream is connected.
	mark(message string)
	// probe emits a tagged kernel refusal as a stream-ordering barrier.
	probe(ctx context.Context, tag string) error
}

type watchState int

const (
	watchOff watchState = iota
	watchStarting
	watchLive
	watchUnavailable
)

// refusalProbe remains owned by the stream until settlement returns.
type refusalProbe struct {
	arrived  chan struct{}
	cancel   context.CancelFunc
	reported bool
}

// refusalWatchT routes tagged kernel reports to their actions.
type refusalWatchT struct {
	mu     sync.Mutex
	source refusalSource
	state  watchState
	reason string
	feeds  map[string]*refusalFeed
	// lineages attribute reports whose tags were truncated.
	lineages map[string]*refusalFeed
	// probes includes reported barriers whose subprocess has not returned.
	probes map[string]*refusalProbe
	ready  string
	cancel context.CancelFunc
	done   chan struct{}
}

var refusalWatch = &refusalWatchT{
	feeds:    map[string]*refusalFeed{},
	lineages: map[string]*refusalFeed{},
	probes:   map[string]*refusalProbe{},
}

// StartRefusalWatch begins reading the kernel's sandbox reports. Actions bound
// before the stream is live report an incomplete witness; a host that cannot
// read the reports gives every action an unavailable one.
func StartRefusalWatch(stateRoot string) {
	refusalWatch.start(newRefusalSource(stateRoot))
}

// StopRefusalWatch preserves collected reports and degrades active witnesses.
func StopRefusalWatch() {
	refusalWatch.stop()
}

func (w *refusalWatchT) start(source refusalSource) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state == watchStarting || w.state == watchLive {
		return
	}
	if source == nil {
		w.state, w.reason = watchUnavailable, "this platform reports no sandbox refusals"
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.source, w.state, w.reason = source, watchStarting, ""
	w.cancel, w.done = cancel, make(chan struct{})
	go w.run(ctx, source, w.done)
}

func (w *refusalWatchT) stop() {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.cancel, w.done = nil, nil
	if w.state != watchUnavailable {
		w.state = watchOff
	}
	w.degradeLocked()
	w.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (w *refusalWatchT) run(ctx context.Context, source refusalSource, done chan struct{}) {
	defer close(done)
	failures := 0
	for ctx.Err() == nil {
		wentLive, err := w.stream(ctx, source)
		if ctx.Err() != nil {
			return
		}
		w.mu.Lock()
		w.state = watchStarting
		w.degradeLocked()
		w.mu.Unlock()
		if wentLive {
			failures = 0
		} else if failures++; failures >= refusalStreamFailures {
			w.giveUp(err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(refusalRestartDelay):
		}
	}
}

func (w *refusalWatchT) stream(ctx context.Context, source refusalSource) (wentLive bool, err error) {
	selfPID := os.Getpid()
	reader, wait, err := source.open(ctx, selfPID)
	if err != nil {
		return false, err
	}
	streamCtx, stopReady := context.WithCancel(ctx)
	defer stopReady()
	ready := newRefusalNonce()
	w.mu.Lock()
	w.ready = ready
	w.mu.Unlock()
	go w.announceReady(streamCtx, source, ready)

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line, ok := parseStreamLine(scanner.Bytes(), selfPID)
		if !ok {
			continue
		}
		if w.deliver(line) {
			wentLive = true
			stopReady()
		}
	}
	_ = reader.Close()
	return wentLive, errors.Join(scanner.Err(), wait())
}

// announceReady repeats a marker until the stream echoes it.
func (w *refusalWatchT) announceReady(ctx context.Context, source refusalSource, nonce string) {
	ticker := time.NewTicker(refusalReadyInterval)
	defer ticker.Stop()
	for {
		source.mark(refusalMarkPrefix + nonce)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// deliver routes one stream line and reports whether it made the stream live.
func (w *refusalWatchT) deliver(line streamLine) (becameLive bool) {
	w.mu.Lock()
	switch {
	case line.lost:
		w.degradeLocked()
	case line.mark != "":
		if line.mark == w.ready {
			becameLive = w.state != watchLive
			w.state = watchLive
		}
	default:
		if probe, ok := w.probes[line.refusal.Tag]; ok {
			if !probe.reported {
				probe.reported = true
				close(probe.arrived)
			}
			break
		}
		feed := w.feeds[line.refusal.Tag]
		w.mu.Unlock()
		if line.refusal.Tag == "" {
			feed = w.feedOfProcess(line.refusal.PID)
		}
		if feed != nil {
			feed.record(line.refusal)
		}
		return false
	}
	w.mu.Unlock()
	return becameLive
}

func (w *refusalWatchT) giveUp(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state = watchUnavailable
	w.degradeLocked()
	w.reason = "the kernel's sandbox reports could not be read"
	if err != nil {
		w.reason += ": " + err.Error()
	}
	for _, feed := range w.feeds {
		feed.degrade(WitnessUnavailable)
	}
	slog.Warn("sandbox refusal watch unavailable", "component", "sandbox", "reason", w.reason)
}

// degradeLocked cancels barriers and marks active witnesses incomplete.
func (w *refusalWatchT) degradeLocked() {
	for tag, probe := range w.probes {
		probe.cancel()
		delete(w.probes, tag)
	}
	for _, feed := range w.feeds {
		feed.degrade(WitnessIncomplete)
	}
}

// feedOfProcess attributes a live descendant through its lineage.
func (w *refusalWatchT) feedOfProcess(pid int) *refusalFeed {
	line, ok := lineage.Of(pid)
	if !ok {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lineages[string(line.ID())]
}

// bindLineage lets reports that lost their tag reach the action by process.
func (w *refusalWatchT) bindLineage(tag, lineageID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if feed := w.feeds[tag]; feed != nil && lineageID != "" {
		feed.lineage = lineageID
		w.lineages[lineageID] = feed
	}
}

// register opens the feed an action's tagged refusals reach.
func (w *refusalWatchT) register(tag string, rules FilesystemRules) *refusalFeed {
	feed := &refusalFeed{rules: rules, witness: WitnessKernel, keys: map[string]int{}}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch w.state {
	case watchLive:
	case watchStarting:
		feed.witness = WitnessIncomplete
	default:
		feed.witness = WitnessUnavailable
	}
	w.feeds[tag] = feed
	return feed
}

func (w *refusalWatchT) unregister(tag string) {
	w.mu.Lock()
	if feed := w.feeds[tag]; feed != nil && feed.lineage != "" {
		delete(w.lineages, feed.lineage)
	}
	delete(w.feeds, tag)
	w.mu.Unlock()
}

// settle waits for a kernel refusal barrier; silent stream loss remains possible.
func (w *refusalWatchT) settle(ctx context.Context, feed *refusalFeed) {
	w.mu.Lock()
	if w.state != watchLive || feed.currentWitness() != WitnessKernel {
		w.mu.Unlock()
		return
	}
	// Final evidence survives request cancellation; stream loss cancels its probe.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refusalSettleTimeout)
	tag := newRefusalNonce()
	probe := &refusalProbe{arrived: make(chan struct{}), cancel: cancel}
	w.probes[tag] = probe
	source := w.source
	w.mu.Unlock()
	defer func() {
		cancel()
		w.mu.Lock()
		delete(w.probes, tag)
		w.mu.Unlock()
	}()
	if err := source.probe(ctx, tag); err != nil {
		feed.degrade(WitnessIncomplete)
		return
	}
	select {
	case <-probe.arrived:
	case <-ctx.Done():
		feed.degrade(WitnessIncomplete)
	}
}

func newRefusalNonce() string {
	b := make([]byte, refusalTagBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type refusalFeed struct {
	mu        sync.Mutex
	rules     FilesystemRules
	witness   RefusalWitness
	refusals  []SandboxRefusal
	keys      map[string]int
	omitted   int
	listeners []func()
	// lineage is the action's descendant lineage, once bound.
	lineage string
}

func refusalKey(operation, target string) string { return operation + "\x00" + target }

// record folds one report into the feed; listeners hear each new refusal.
func (f *refusalFeed) record(k kernelRefusal) {
	key := refusalKey(k.Operation, k.Target)
	f.mu.Lock()
	if i, seen := f.keys[key]; seen {
		if i >= 0 {
			f.refusals[i].Count += k.Count
		}
		f.mu.Unlock()
		return
	}
	if len(f.refusals) >= maxActionRefusals {
		if len(f.keys) < maxActionRefusals+maxOmittedRefusalKeys {
			f.keys[key] = -1
			f.omitted++
		}
		f.mu.Unlock()
		return
	}
	refusal := routeRefusal(f.rules, k.Operation, k.Target, k.Truncated)
	refusal.Process, refusal.Count = k.Process, k.Count
	f.keys[key] = len(f.refusals)
	f.refusals = append(f.refusals, refusal)
	listeners := append([]func(){}, f.listeners...)
	f.mu.Unlock()
	for _, listen := range listeners {
		listen()
	}
}

func (f *refusalFeed) snapshot() SandboxRefusals {
	f.mu.Lock()
	defer f.mu.Unlock()
	return SandboxRefusals{
		Witness:  f.witness,
		Refusals: append([]SandboxRefusal(nil), f.refusals...),
		Omitted:  f.omitted,
	}
}

func (f *refusalFeed) currentWitness() RefusalWitness {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.witness
}

// degrade lowers the witness; it never restores completeness.
func (f *refusalFeed) degrade(to RefusalWitness) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.witness == WitnessUnavailable {
		return
	}
	if to == WitnessUnavailable || f.witness == WitnessKernel {
		f.witness = to
	}
}

func (f *refusalFeed) listen(fn func()) {
	f.mu.Lock()
	f.listeners = append(f.listeners, fn)
	f.mu.Unlock()
}

// RefusalWatchLive reports whether kernel refusal reports currently reach
// this host.
func RefusalWatchLive() bool {
	return refusalWatch.currentState() == watchLive
}

func (w *refusalWatchT) currentState() watchState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}
