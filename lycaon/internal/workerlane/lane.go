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

// Workspaces is the Milestone 14 branch service a lane prepares through.
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

// Service keeps lanes. Admission, landing, rejection and binding switches for
// one worker run one at a time.
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
	// SourceResultID names the result a refresh or reset follows.
	SourceResultID string
	// PromotionRef names the landing a refresh follows.
	PromotionRef string
}

// Admit selects and readies the workspace for a worker's next round.
func (s *Service) Admit(ctx context.Context, req Request) (Binding, error) {
	unlock := s.lock(req.WorkerID)
	defer unlock()
	sel := Select(req.Facts)
	switch sel.Action {
	case ActionHold:
		return Binding{}, &HoldError{Reason: sel.Hold}
	case ActionSource:
		return Binding{Action: sel.Action}, nil
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
	}
	return s.switchLane(ctx, req, sel)
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
	if hadLane && req.SourceResultID != "" && latest.SourceResultID == req.SourceResultID {
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
	if _, err := s.records.RecordPreparation(ctx, req.Actor, req.Op, req.WorkerID, workerrecords.Preparation{
		SourceResultID: req.SourceResultID, PromotionRef: req.PromotionRef,
		PreparationRef: binding.Root, BindingGeneration: generation,
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
