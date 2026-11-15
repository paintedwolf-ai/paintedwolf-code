package workerlane

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
)

// Workspaces prepares and evicts workspace branches for a lane.
type Workspaces interface {
	CreateWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, viewID string) (*workspace.Binding, workspace.SandboxLayout, error)
	CreateStackedWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, viewID, parentCheckpointID string) (*workspace.Binding, workspace.SandboxLayout, error)
	EvictWorkerBranch(ctx context.Context, branchRoot string) error
}

var (
	// ErrHeld reports a lane that admits nothing until its hold clears.
	ErrHeld = errors.New("lane is held")
	// ErrNotPrepared reports a write round on a worker with no lane.
	ErrNotPrepared = errors.New("worker has no prepared lane")
	// ErrClosed reports a worker that was closed before its lane switched.
	ErrClosed = errors.New("worker is closed")
)

// HoldError names the hold that refused admission.
type HoldError struct{ Reason HoldReason }

func (e *HoldError) Error() string { return "lane is held: " + string(e.Reason) }
func (e *HoldError) Unwrap() error { return ErrHeld }

// Binding is the workspace a round runs in. Root is empty for a read round.
type Binding struct {
	Root       string `json:"root,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	Action     Action `json:"action"`
}

// Service keeps lanes. One worker's admissions run one at a time.
type Service struct {
	workspaces Workspaces
	records    *workerrecords.Store
	locks      sync.Map
}

// New returns a lane service.
func New(workspaces Workspaces, records *workerrecords.Store) *Service {
	return &Service{workspaces: workspaces, records: records}
}

// lock serializes one worker's lane operations.
func (s *Service) lock(workerID string) func() {
	mu, _ := s.locks.LoadOrStore(workerID, &sync.Mutex{})
	m := mu.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// Request is one admission.
type Request struct {
	Actor        workerrecords.Actor
	Op           workerrecords.Op
	WorkerID     string
	Facts        Facts
	Roots        []projectroot.RootRef
	ActiveRootID string
}

// Admit selects and readies the workspace for a worker's next round.
func (s *Service) Admit(ctx context.Context, req Request) (Binding, error) {
	unlock := s.lock(req.WorkerID)
	defer unlock()
	if closed, err := s.isClosed(ctx, req.Actor, req.WorkerID); err != nil {
		return Binding{}, err
	} else if closed {
		return Binding{}, ErrClosed
	}
	sel := Select(req.Facts)
	switch sel.Action {
	case ActionHold:
		return Binding{}, &HoldError{Reason: sel.Hold}
	case ActionSource:
		return Binding{Action: sel.Action}, nil
	case ActionSubject:
		// The subject is sealed: a reviewer's view, once prepared, is kept.
		current, ok, err := s.current(ctx, req.Actor, req.WorkerID)
		if err != nil {
			return Binding{}, err
		}
		if ok {
			current.Action = ActionSubject
			return current, nil
		}
		// The view follows the subject, never one of the reviewer's results.
		req.Facts.LastResultID = ""
		return s.switchLane(ctx, req, sel)
	case ActionKeep:
		current, ok, err := s.current(ctx, req.Actor, req.WorkerID)
		if err != nil {
			return Binding{}, err
		}
		if !ok {
			return Binding{}, ErrNotPrepared
		}
		current.Action = ActionKeep
		return current, nil
	case ActionPrepare, ActionRefresh, ActionReset:
		return s.switchLane(ctx, req, sel)
	}
	return s.switchLane(ctx, req, sel)
}

// RefreshIdle prepares a fresh lane view after a landing while the worker has
// no unsettled round. Closing the worker before or during preparation leaves
// its recorded binding untouched.
func (s *Service) RefreshIdle(ctx context.Context, req Request) (Binding, bool, error) {
	unlock := s.lock(req.WorkerID)
	defer unlock()
	if closed, err := s.isClosed(ctx, req.Actor, req.WorkerID); err != nil || closed {
		return Binding{}, false, err
	}
	rounds, err := s.records.Rounds(ctx, req.Actor, req.WorkerID)
	if err != nil {
		return Binding{}, false, err
	}
	for _, r := range rounds {
		if r.SettledAt == "" {
			return Binding{}, false, nil
		}
	}
	sel := Select(req.Facts)
	if sel.Action != ActionRefresh {
		return Binding{}, false, nil
	}
	b, err := s.switchLane(ctx, req, sel)
	if errors.Is(err, ErrClosed) {
		return Binding{}, false, nil
	}
	return b, err == nil, err
}

func (s *Service) isClosed(ctx context.Context, actor workerrecords.Actor, workerID string) (bool, error) {
	w, err := s.records.Worker(ctx, actor, workerID)
	if err != nil {
		return false, err
	}
	return w.ClosedAt != "", nil
}

// switchLane prepares the next generation and records it only when
// preparation succeeds. The earlier generation is then offered to retention,
// which keeps any checkpoint still referenced.
func (s *Service) switchLane(ctx context.Context, req Request, sel Selection) (Binding, error) {
	latest, hadLane, err := s.records.LatestPreparation(ctx, req.Actor, req.WorkerID)
	if err != nil {
		return Binding{}, err
	}
	previous := Binding{Root: latest.PreparationRef, Generation: latest.BindingGeneration}
	// A replay after the switch committed returns that switch instead of
	// preparing an orphan view.
	source := req.Facts.LastResultID
	if hadLane && source != "" && latest.SourceResultID == source {
		previous.Action = sel.Action
		return previous, nil
	}
	generation := previous.Generation + 1
	viewID := laneViewID(req.WorkerID, generation)
	var binding *workspace.Binding
	if sel.FromCheckpoint != "" {
		binding, _, err = s.workspaces.CreateStackedWorkerBranch(ctx, req.Roots, req.ActiveRootID, viewID, sel.FromCheckpoint)
	} else {
		binding, _, err = s.workspaces.CreateWorkerBranch(ctx, req.Roots, req.ActiveRootID, viewID)
	}
	if err != nil {
		return Binding{}, fmt.Errorf("prepare lane generation %d: %w", generation, err)
	}
	if binding == nil || strings.TrimSpace(binding.Root) == "" {
		return Binding{}, fmt.Errorf("prepare lane generation %d: no branch root", generation)
	}
	if closed, err := s.isClosed(ctx, req.Actor, req.WorkerID); err != nil {
		_ = s.workspaces.EvictWorkerBranch(context.WithoutCancel(ctx), binding.Root)
		return Binding{}, err
	} else if closed {
		_ = s.workspaces.EvictWorkerBranch(context.WithoutCancel(ctx), binding.Root)
		return Binding{}, ErrClosed
	}
	if _, err := s.records.RecordPreparation(ctx, req.Actor, req.Op, req.WorkerID, workerrecords.Preparation{
		SourceResultID: source, PreparationRef: binding.Root, BindingGeneration: generation,
	}); err != nil {
		return Binding{}, err
	}
	if hadLane && previous.Root != "" && previous.Root != binding.Root {
		// Retention refuses a branch that holds the only copy of a result
		// or unsettled work; the lane has already moved on either way.
		_ = s.workspaces.EvictWorkerBranch(context.WithoutCancel(ctx), previous.Root)
	}
	return Binding{Root: binding.Root, Generation: generation, Action: sel.Action}, nil
}

// current reads the worker's recorded lane binding.
func (s *Service) current(ctx context.Context, actor workerrecords.Actor, workerID string) (Binding, bool, error) {
	prep, ok, err := s.records.LatestPreparation(ctx, actor, workerID)
	if err != nil || !ok {
		return Binding{}, false, err
	}
	return Binding{Root: prep.PreparationRef, Generation: prep.BindingGeneration}, true, nil
}

func laneViewID(workerID string, generation int64) string {
	return strings.TrimSpace(workerID) + "." + strconv.FormatInt(generation, 10)
}
