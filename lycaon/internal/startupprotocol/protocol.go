// Package startupprotocol reports engine boot to its desktop parent.
package startupprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	environment       = "LYCAON_STARTUP_PROTOCOL"
	version           = 1
	heartbeatInterval = time.Second
)

// Phase is a closed semantic boot stage shared with the desktop shell.
type Phase string

const (
	PhaseLaunch            Phase = "launch"
	PhaseObservability     Phase = "observability"
	PhaseStore             Phase = "store"
	PhaseUpgradeSnapshot   Phase = "upgrade_snapshot"
	PhaseSchemaUpgrade     Phase = "schema_upgrade"
	PhaseUpgradeValidation Phase = "upgrade_validation"
	PhaseConfiguration     Phase = "configuration"
	PhaseUserPath          Phase = "user_path"
	PhaseCredentials       Phase = "credentials"
	PhaseHostResources     Phase = "host_resources"
	PhaseProviders         Phase = "providers"
	PhasePricing           Phase = "pricing"
	PhaseTools             Phase = "tools"
	PhaseAgents            Phase = "agents"
	PhaseSessions          Phase = "sessions"
	PhasePolicy            Phase = "policy"
	PhaseEvents            Phase = "events"
	PhaseWorkflows         Phase = "workflows"
	PhaseWorkers           Phase = "workers"
	PhaseScan              Phase = "scan"
	PhaseResearch          Phase = "research"
	PhaseGrounding         Phase = "grounding"
	PhaseCoordinator       Phase = "coordinator"
	PhaseServer            Phase = "server"
	PhaseServices          Phase = "services"
	PhaseRecovery          Phase = "recovery"
	PhaseBackgroundWork    Phase = "background_work"
	PhaseBinding           Phase = "binding"
	PhaseReady             Phase = "ready"
)

var phases = []Phase{
	PhaseLaunch, PhaseObservability, PhaseStore, PhaseUpgradeSnapshot, PhaseSchemaUpgrade, PhaseUpgradeValidation, PhaseConfiguration, PhaseUserPath,
	PhaseCredentials, PhaseHostResources, PhaseProviders, PhasePricing, PhaseTools,
	PhaseAgents, PhaseSessions, PhasePolicy, PhaseEvents, PhaseWorkflows, PhaseWorkers,
	PhaseScan, PhaseResearch, PhaseGrounding, PhaseCoordinator, PhaseServer, PhaseServices,
	PhaseRecovery, PhaseBackgroundWork, PhaseBinding, PhaseReady,
}

var phaseSet = func() map[Phase]struct{} {
	set := make(map[Phase]struct{}, len(phases))
	for _, phase := range phases {
		set[phase] = struct{}{}
	}
	return set
}()

// Phases returns the complete protocol vocabulary in lifecycle order.
func Phases() []Phase {
	return append([]Phase(nil), phases...)
}

type kind string

const (
	kindPhase     kind = "phase"
	kindHeartbeat kind = "heartbeat"
	kindReady     kind = "ready"
	kindFailed    kind = "failed"
)

type event struct {
	Protocol  int    `json:"protocol"`
	Sequence  uint64 `json:"sequence"`
	PID       int    `json:"pid"`
	Kind      kind   `json:"kind"`
	Phase     Phase  `json:"phase"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Port      int    `json:"port,omitempty"`
	Code      string `json:"code,omitempty"`
}

// Sink is the app-facing startup lifecycle contract.
type Sink interface {
	Phase(Phase) error
	Ready(int) error
	Failed(string) error
}

// Reporter owns one process's protocol stream and heartbeat loop.
type Reporter struct {
	mu       sync.Mutex
	encoder  *json.Encoder
	trace    io.Writer
	started  time.Time
	phase    Phase
	sequence uint64
	err      error
	terminal bool
	done     chan struct{}
	doneOnce sync.Once
	wg       sync.WaitGroup
	interval time.Duration
}

// FromEnvironment creates the reporter requested by the desktop parent.
// A missing variable keeps ordinary CLI serve stdout unchanged.
func FromEnvironment(protocol, trace io.Writer) (*Reporter, error) {
	raw := os.Getenv(environment)
	if raw == "" {
		return nil, nil
	}
	if raw != fmt.Sprint(version) {
		return nil, fmt.Errorf("unsupported startup protocol version %q", raw)
	}
	return newReporter(protocol, trace, heartbeatInterval)
}

func newReporter(protocol, trace io.Writer, interval time.Duration) (*Reporter, error) {
	if protocol == nil {
		return nil, errors.New("startup protocol writer is required")
	}
	if interval <= 0 {
		return nil, errors.New("startup heartbeat interval must be positive")
	}
	r := &Reporter{
		encoder: json.NewEncoder(protocol), trace: trace, started: time.Now().UTC(),
		phase: PhaseLaunch, done: make(chan struct{}), interval: interval,
	}
	if err := r.emitLocked(kindPhase, PhaseLaunch, 0, ""); err != nil {
		return nil, err
	}
	r.wg.Add(1)
	go r.heartbeatLoop()
	return r, nil
}

// Phase reports the next semantic boot phase.
func (r *Reporter) Phase(phase Phase) error {
	if r == nil {
		return nil
	}
	if _, ok := phaseSet[phase]; !ok || phase == PhaseReady {
		return fmt.Errorf("startup progress phase %q is invalid", phase)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.activeErrorLocked(); err != nil {
		return err
	}
	r.phase = phase
	return r.emitLocked(kindPhase, phase, 0, "")
}

// Ready publishes the bound port and closes the startup lifecycle.
func (r *Reporter) Ready(port int) error {
	if r == nil {
		return nil
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("startup ready port %d is invalid", port)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.activeErrorLocked(); err != nil {
		return err
	}
	r.phase = PhaseReady
	err := r.emitLocked(kindReady, PhaseReady, port, "")
	r.finishLocked()
	return err
}

// Failed publishes a machine-readable terminal startup failure.
func (r *Reporter) Failed(code string) error {
	if r == nil {
		return nil
	}
	if code == "" {
		return errors.New("startup failure code is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.activeErrorLocked(); err != nil {
		return err
	}
	err := r.emitLocked(kindFailed, r.phase, 0, code)
	r.finishLocked()
	return err
}

// Close stops heartbeat emission.
func (r *Reporter) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.finishLocked()
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Reporter) heartbeatLoop() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			r.mu.Lock()
			if !r.terminal {
				_ = r.emitLocked(kindHeartbeat, r.phase, 0, "")
			}
			r.mu.Unlock()
		}
	}
}

func (r *Reporter) activeErrorLocked() error {
	if !r.terminal {
		return nil
	}
	if r.err != nil {
		return r.err
	}
	return errors.New("startup protocol already finished")
}

func (r *Reporter) emitLocked(eventKind kind, phase Phase, port int, code string) error {
	if r.err != nil {
		return r.err
	}
	r.sequence++
	record := event{
		Protocol: version, Sequence: r.sequence, PID: os.Getpid(), Kind: eventKind,
		Phase: phase, ElapsedMS: time.Since(r.started).Milliseconds(), Port: port,
		Code: code,
	}
	if err := r.encoder.Encode(record); err != nil {
		r.err = fmt.Errorf("write startup protocol: %w", err)
		r.finishLocked()
		return r.err
	}
	if eventKind != kindHeartbeat && r.trace != nil {
		_, _ = fmt.Fprintf(r.trace, "startup kind=%s phase=%s sequence=%d elapsed_ms=%d\n",
			eventKind, phase, record.Sequence, record.ElapsedMS)
	}
	return nil
}

func (r *Reporter) finishLocked() {
	r.terminal = true
	r.doneOnce.Do(func() { close(r.done) })
}
