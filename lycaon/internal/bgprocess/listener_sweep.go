package bgprocess

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// DefaultSweepInterval is the periodic listener refresh cadence while live background jobs exist.
	DefaultSweepInterval = 2 * time.Second
	// DefaultOutputCoalesceWindow coalesces output-chunk listener refreshes into one shared scan.
	DefaultOutputCoalesceWindow = 75 * time.Millisecond
)

// ObservedListener is one listening socket observed during a shared host sweep.
type ObservedListener struct {
	Addr      netip.AddrPort
	IPv6Only  bool
	PID       int
	LineageID string
	Handle    string
}

// ListenerObserver performs one shared socket-table observation per sweep.
type ListenerObserver func(ctx context.Context) ([]ObservedListener, error)

func defaultListenerObserver(ctx context.Context) ([]ObservedListener, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	table, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolTCP}})
	if err != nil {
		if errors.Is(err, lineage.ErrUnsupported) {
			return nil, nil
		}
		return nil, err
	}
	var out []ObservedListener
	for _, l := range table {
		for _, h := range l.Holders {
			out = append(out, ObservedListener{
				Addr:     l.Addr,
				IPv6Only: l.IPv6Only,
				PID:      h.PID,
			})
		}
	}
	return out, nil
}

type listenerSweeper struct {
	jobs           *processTable
	observer       ListenerObserver
	publishProject ProjectBackgroundPublisher
	sweepInterval  time.Duration
	coalesceWindow time.Duration

	sweepMu  sync.Mutex
	loopMu   sync.Mutex
	wakeCh   chan sweepSignal
	loopStop chan struct{}
	loopDone chan struct{}
	closed   bool
}

type sweepSignal int

const (
	sweepSignalStart sweepSignal = iota + 1
	sweepSignalOutput
	sweepSignalEnded
)

func newListenerSweeper(
	jobs *processTable,
	cfg Config,
	publishProject ProjectBackgroundPublisher,
) *listenerSweeper {
	observer := cfg.ListenerObserver
	if observer == nil {
		observer = defaultListenerObserver
	}
	sweepInterval := cfg.SweepInterval
	if sweepInterval <= 0 {
		sweepInterval = DefaultSweepInterval
	}
	coalesceWindow := cfg.OutputCoalesceWindow
	if coalesceWindow <= 0 {
		coalesceWindow = DefaultOutputCoalesceWindow
	}
	return &listenerSweeper{
		jobs:           jobs,
		observer:       observer,
		publishProject: publishProject,
		sweepInterval:  sweepInterval,
		coalesceWindow: coalesceWindow,
		wakeCh:         make(chan sweepSignal, 16),
	}
}

func (s *listenerSweeper) signal(sig sweepSignal) {
	if s == nil || s.jobs == nil {
		return
	}
	s.loopMu.Lock()
	if s.closed || !s.jobs.hasLiveBackground() {
		s.loopMu.Unlock()
		return
	}
	if s.loopDone == nil {
		stopCh := make(chan struct{})
		doneCh := make(chan struct{})
		s.loopStop = stopCh
		s.loopDone = doneCh
		go s.loop(stopCh, doneCh)
	}
	stopCh := s.loopStop
	s.loopMu.Unlock()

	select {
	case s.wakeCh <- sig:
	case <-stopCh:
	default:
		if sig == sweepSignalStart {
			select {
			case s.wakeCh <- sig:
			case <-stopCh:
			default:
			}
		}
	}
}

func (s *listenerSweeper) onJobStarted() {
	s.signal(sweepSignalStart)
}

func (s *listenerSweeper) onOutputChunk() {
	s.signal(sweepSignalOutput)
}

func (s *listenerSweeper) onJobEnded() {
	if s == nil || s.jobs == nil {
		return
	}
	s.loopMu.Lock()
	if s.closed {
		s.loopMu.Unlock()
		return
	}
	if s.jobs.hasLiveBackground() {
		stopCh := s.loopStop
		s.loopMu.Unlock()
		if stopCh != nil {
			select {
			case s.wakeCh <- sweepSignalEnded:
			case <-stopCh:
			default:
			}
		}
		return
	}
	stopCh := s.loopStop
	doneCh := s.loopDone
	s.loopStop = nil
	s.loopDone = nil
	s.loopMu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		<-doneCh
	}
}

func (s *listenerSweeper) close() {
	if s == nil {
		return
	}
	s.loopMu.Lock()
	s.closed = true
	stopCh := s.loopStop
	doneCh := s.loopDone
	s.loopStop = nil
	s.loopDone = nil
	s.loopMu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		<-doneCh
	}
}

func (s *listenerSweeper) loop(stopCh <-chan struct{}, doneCh chan struct{}) {
	defer close(doneCh)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		periodicTicker *time.Ticker
		periodicC      <-chan time.Time
		coalesceTimer  *time.Timer
		coalesceC      <-chan time.Time
	)
	stopPeriodic := func() {
		if periodicTicker != nil {
			periodicTicker.Stop()
			periodicTicker = nil
			periodicC = nil
		}
	}
	stopCoalesce := func() {
		if coalesceTimer != nil {
			if !coalesceTimer.Stop() {
				select {
				case <-coalesceTimer.C:
				default:
				}
			}
			coalesceTimer = nil
			coalesceC = nil
		}
	}
	defer func() {
		stopPeriodic()
		stopCoalesce()
	}()

	ensurePeriodic := func() {
		if periodicTicker == nil && s.sweepInterval > 0 {
			periodicTicker = time.NewTicker(s.sweepInterval)
			periodicC = periodicTicker.C
		}
	}

	tryExitWhenIdle := func() bool {
		s.loopMu.Lock()
		defer s.loopMu.Unlock()
		if s.jobs.hasLiveBackground() {
			return false
		}
		if s.loopDone == doneCh {
			s.loopStop = nil
			s.loopDone = nil
		}
		return true
	}

	runSweep := func() bool {
		stopCoalesce()
		hasLive := s.sweepOnce(ctx)
		if hasLive {
			ensurePeriodic()
			return false
		}
		stopPeriodic()
		return tryExitWhenIdle()
	}

	for {
		select {
		case <-stopCh:
			return
		case sig := <-s.wakeCh:
			switch sig {
			case sweepSignalStart:
				if runSweep() {
					return
				}
			case sweepSignalOutput:
				if coalesceTimer == nil {
					coalesceTimer = time.NewTimer(s.coalesceWindow)
					coalesceC = coalesceTimer.C
				}
			case sweepSignalEnded:
				if tryExitWhenIdle() {
					return
				}
			}
		case <-coalesceC:
			coalesceTimer = nil
			coalesceC = nil
			if runSweep() {
				return
			}
		case <-periodicC:
			if runSweep() {
				return
			}
		}
	}
}

type liveJobTarget struct {
	projectID     string
	rootSessionID string
}

// sweepOnce performs one shared socket-table observation and attributes listeners to live background jobs.
// It returns true if at least one live background job remains in the registry.
func (s *listenerSweeper) sweepOnce(ctx context.Context) bool {
	if s == nil || s.jobs == nil || s.observer == nil {
		return false
	}
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()

	s.jobs.mu.Lock()
	if s.jobs.closed {
		s.jobs.mu.Unlock()
		return false
	}
	liveHandles := make(map[string]liveJobTarget)
	lineageToHandle := make(map[string]string)
	pidToHandle := make(map[int]string)
	for _, procs := range s.jobs.projectProcs {
		for handle, proc := range procs {
			if proc == nil || !proc.running || proc.silent || proc.discarded {
				continue
			}
			owner := proc.RootSessionID
			if owner == "" {
				owner = proc.SessionID
			}
			liveHandles[handle] = liveJobTarget{
				projectID:     proc.ProjectID,
				rootSessionID: owner,
			}
			if proc.facts.Action != nil {
				if lid := trim(proc.facts.Action.LineageID()); lid != "" {
					lineageToHandle[lid] = handle
				}
			}
			for _, pid := range s.jobs.pids[handle] {
				if pid > 0 {
					pidToHandle[pid] = handle
				}
			}
			if proc.pty != nil {
				if pid := proc.pty.Pid(); pid > 0 {
					pidToHandle[pid] = handle
				}
			}
		}
	}
	s.jobs.mu.Unlock()

	if len(liveHandles) == 0 {
		return false
	}

	observations, err := s.observer(ctx)
	if err != nil {
		return true
	}

	newPorts := make(map[string][]string, len(liveHandles))
	pidLineageCache := make(map[int]string)
	for _, obs := range observations {
		if !obs.Addr.IsValid() || obs.Addr.Port() == 0 {
			continue
		}
		handle := trim(obs.Handle)
		if handle == "" && obs.LineageID != "" {
			handle = lineageToHandle[trim(obs.LineageID)]
		}
		if handle == "" && obs.PID > 0 {
			handle = pidToHandle[obs.PID]
		}
		if handle == "" && obs.PID > 0 && len(lineageToHandle) > 0 {
			lid, cached := pidLineageCache[obs.PID]
			if !cached {
				if lin, ok := lineage.Of(obs.PID); ok && lin != nil {
					lid = string(lin.ID())
				}
				pidLineageCache[obs.PID] = lid
			}
			if lid != "" {
				handle = lineageToHandle[lid]
			}
		}
		if handle == "" {
			continue
		}
		if _, ok := liveHandles[handle]; !ok {
			continue
		}
		ep := formatListenerEndpoint(obs.Addr)
		newPorts[handle] = append(newPorts[handle], ep)
	}

	for handle := range liveHandles {
		ports := newPorts[handle]
		if len(ports) > 0 {
			slices.Sort(ports)
			ports = slices.Compact(ports)
			newPorts[handle] = ports
		}
	}

	var events []api.ProjectBackgroundEvent
	hasRemainingLive := false
	s.jobs.mu.Lock()
	for handle, target := range liveHandles {
		var proc *Process
		if projProcs := s.jobs.projectProcs[target.projectID]; projProcs != nil {
			proc = projProcs[handle]
		}
		if proc == nil || !proc.running || proc.silent || proc.discarded {
			continue
		}
		hasRemainingLive = true
		updated := newPorts[handle]
		current := s.jobs.ports[handle]
		if slices.Equal(current, updated) {
			continue
		}
		if len(updated) == 0 {
			delete(s.jobs.ports, handle)
		} else {
			s.jobs.ports[handle] = updated
		}
		if target.projectID != "" {
			s.jobs.projectRevisions[target.projectID]++
			rev := s.jobs.projectRevisions[target.projectID]
			events = append(events, api.ProjectBackgroundEvent{
				ProjectID: target.projectID,
				ProcessID: handle,
				SessionID: target.rootSessionID,
				Action:    "ports_changed",
				Revision:  rev,
				Running:   true,
				Ports:     append([]string(nil), updated...),
			})
		}
	}
	if !hasRemainingLive {
		hasRemainingLive = s.jobs.hasLiveBackgroundLocked()
	}
	s.jobs.mu.Unlock()

	if s.publishProject != nil {
		for _, ev := range events {
			s.publishProject(ctx, ev)
		}
	}
	return hasRemainingLive
}

func formatListenerEndpoint(addr netip.AddrPort) string {
	return netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port()).String()
}

// RefreshListeners runs one immediate shared listener sweep across live background jobs.
func (r *Registry) RefreshListeners(ctx context.Context) {
	if r == nil || r.sweeper == nil {
		return
	}
	r.sweeper.sweepOnce(ctx)
}
