package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

// TopologyBatchStatus describes batch-level settlement for a topology run.
type TopologyBatchStatus string

const (
	TopologyBatchActive  TopologyBatchStatus = "active"
	TopologyBatchDone    TopologyBatchStatus = "done"
	TopologyBatchFailed  TopologyBatchStatus = "failed"
	TopologyBatchAborted TopologyBatchStatus = "aborted"
)

// TopologyBatch groups the planned worker units of one topology run.
type TopologyBatch struct {
	ID              string
	SessionID       string
	ProjectID       string
	WorkspacePath   string
	Task            string
	Pattern         TopologyPattern
	WorkflowID      string
	WorkflowVersion string
	WorkflowRunID   string
	BlueprintPath   string
	Status          TopologyBatchStatus
	Reason          string
}

// TopologyUnit is one worker-backed unit in a topology batch.
type TopologyUnit struct {
	ID            string
	WorkID        string
	AgentType     string
	ParentID      string
	Prompt        string
	Phase         string
	WorkspaceID   string
	WorkspaceRoot string
	WorkerID      string
	JobID         string
	Status        api.LegStatus
	Summary       string
}

// TopologyWorkers plans, dispatches, observes, and cancels worker-backed topology batches.
type TopologyWorkers interface {
	CreateBatch(ctx context.Context, batch TopologyBatch, units []TopologyUnit) (*TopologyBatch, []TopologyUnit, error)
	BatchBySession(ctx context.Context, sessionID string) (string, bool, error)
	BatchByWorkflowRun(ctx context.Context, workflowRunID string) (string, bool, error)
	GetBatch(ctx context.Context, batchID string) (*TopologyBatch, error)
	GetUnit(ctx context.Context, batchID, unitID string) (*TopologyUnit, error)
	ListUnits(ctx context.Context, batchID string) ([]TopologyUnit, error)
	UpdateUnit(ctx context.Context, batchID string, unit TopologyUnit) error
	DispatchUnit(ctx context.Context, batchID, unitID string) (*TopologyUnit, error)
	AbortBatch(ctx context.Context, batchID, reason string) error
}

// TopologyDirectSettler settles a topology unit synchronously for harness fixtures.
type TopologyDirectSettler interface {
	DispatchSettled(ctx context.Context, batchID, unitID string, result api.WorkerResult) (*TopologyUnit, error)
}

// RunWorkerStore reads the statuses of workers bound to a workflow run.
type RunWorkerStore interface {
	WorkflowWorkStatuses(ctx context.Context, workflowRunID string) (map[string]api.LegStatus, error)
}

// MemoryTopologyWorkers implements TopologyWorkers, TopologyDirectSettler, and RunWorkerStore in memory.
type MemoryTopologyWorkers struct {
	mu            sync.Mutex
	batches       map[string]*TopologyBatch
	units         map[string][]TopologyUnit
	bySession     map[string]string
	byWorkflowRun map[string]string
}

// NewMemoryTopologyWorkers constructs an in-memory topology worker store.
func NewMemoryTopologyWorkers() *MemoryTopologyWorkers {
	return &MemoryTopologyWorkers{
		batches:       make(map[string]*TopologyBatch),
		units:         make(map[string][]TopologyUnit),
		bySession:     make(map[string]string),
		byWorkflowRun: make(map[string]string),
	}
}

func (m *MemoryTopologyWorkers) CreateBatch(
	ctx context.Context,
	batch TopologyBatch,
	units []TopologyUnit,
) (*TopologyBatch, []TopologyUnit, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(batch.ID) == "" {
		batch.ID = uuid.NewString()
	}
	if batch.Status == "" {
		batch.Status = TopologyBatchActive
	}
	stored := batch
	copied := make([]TopologyUnit, len(units))
	for i, u := range units {
		if strings.TrimSpace(u.ID) == "" {
			u.ID = uuid.NewString()
		}
		if u.Status == "" {
			u.Status = api.LegStatusPending
		}
		copied[i] = u
	}
	m.batches[stored.ID] = &stored
	m.units[stored.ID] = copied
	if s := strings.TrimSpace(stored.SessionID); s != "" {
		m.bySession[s] = stored.ID
	}
	if r := strings.TrimSpace(stored.WorkflowRunID); r != "" {
		m.byWorkflowRun[r] = stored.ID
	}
	outBatch := stored
	outUnits := append([]TopologyUnit(nil), copied...)
	return &outBatch, outUnits, nil
}

func (m *MemoryTopologyWorkers) BatchBySession(_ context.Context, sessionID string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.bySession[strings.TrimSpace(sessionID)]
	return id, ok, nil
}

func (m *MemoryTopologyWorkers) BatchByWorkflowRun(_ context.Context, workflowRunID string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byWorkflowRun[strings.TrimSpace(workflowRunID)]
	return id, ok, nil
}

func (m *MemoryTopologyWorkers) GetBatch(_ context.Context, batchID string) (*TopologyBatch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[strings.TrimSpace(batchID)]
	if !ok {
		return nil, fmt.Errorf("topology batch %q not found", batchID)
	}
	out := *b
	return &out, nil
}

func (m *MemoryTopologyWorkers) GetUnit(_ context.Context, batchID, unitID string) (*TopologyUnit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list, ok := m.units[strings.TrimSpace(batchID)]
	if !ok {
		return nil, fmt.Errorf("topology batch %q not found", batchID)
	}
	for i := range list {
		if list[i].ID == unitID {
			out := list[i]
			return &out, nil
		}
	}
	return nil, fmt.Errorf("topology unit %q not found in batch %q", unitID, batchID)
}

func (m *MemoryTopologyWorkers) ListUnits(_ context.Context, batchID string) ([]TopologyUnit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list, ok := m.units[strings.TrimSpace(batchID)]
	if !ok {
		return nil, fmt.Errorf("topology batch %q not found", batchID)
	}
	return append([]TopologyUnit(nil), list...), nil
}

func (m *MemoryTopologyWorkers) UpdateUnit(_ context.Context, batchID string, unit TopologyUnit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list, ok := m.units[strings.TrimSpace(batchID)]
	if !ok {
		return fmt.Errorf("topology batch %q not found", batchID)
	}
	for i := range list {
		if list[i].ID == unit.ID {
			list[i] = unit
			m.recomputeBatchLocked(batchID)
			return nil
		}
	}
	return fmt.Errorf("topology unit %q not found in batch %q", unit.ID, batchID)
}

func (m *MemoryTopologyWorkers) DispatchUnit(ctx context.Context, batchID, unitID string) (*TopologyUnit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[strings.TrimSpace(batchID)]
	if !ok {
		return nil, fmt.Errorf("topology batch %q not found", batchID)
	}
	if b.Status == TopologyBatchAborted {
		return nil, fmt.Errorf("topology batch %q is aborted", batchID)
	}
	list := m.units[batchID]
	for i := range list {
		if list[i].ID == unitID {
			if list[i].Status == api.LegStatusPending {
				list[i].Status = api.LegStatusRunning
				if list[i].WorkerID == "" {
					list[i].WorkerID = "worker-" + list[i].ID
				}
				if list[i].JobID == "" {
					list[i].JobID = "job-" + list[i].ID
				}
			}
			out := list[i]
			return &out, nil
		}
	}
	return nil, fmt.Errorf("topology unit %q not found in batch %q", unitID, batchID)
}

func (m *MemoryTopologyWorkers) DispatchSettled(
	ctx context.Context,
	batchID, unitID string,
	result api.WorkerResult,
) (*TopologyUnit, error) {
	if _, err := m.DispatchUnit(ctx, batchID, unitID); err != nil {
		return nil, err
	}
	return m.SettleUnit(batchID, unitID, result)
}

// SettleUnit records a terminal worker outcome on an in-memory topology unit.
func (m *MemoryTopologyWorkers) SettleUnit(batchID, unitID string, result api.WorkerResult) (*TopologyUnit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[strings.TrimSpace(batchID)]
	if !ok {
		return nil, fmt.Errorf("topology batch %q not found", batchID)
	}
	list := m.units[batchID]
	for i := range list {
		if list[i].ID != unitID {
			continue
		}
		if b.Status == TopologyBatchAborted || list[i].Status == api.LegStatusCanceled {
			out := list[i]
			return &out, nil
		}
		if strings.TrimSpace(result.Status) == "failed" {
			list[i].Status = api.LegStatusFailed
		} else {
			list[i].Status = api.LegStatusComplete
		}
		list[i].Summary = strings.TrimSpace(result.Summary)
		m.recomputeBatchLocked(batchID)
		out := list[i]
		return &out, nil
	}
	return nil, fmt.Errorf("topology unit %q not found in batch %q", unitID, batchID)
}

func (m *MemoryTopologyWorkers) AbortBatch(_ context.Context, batchID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[strings.TrimSpace(batchID)]
	if !ok {
		return fmt.Errorf("topology batch %q not found", batchID)
	}
	if b.Status != TopologyBatchActive {
		return nil
	}
	list := m.units[batchID]
	for i := range list {
		if !list[i].Status.IsTerminal() {
			list[i].Status = api.LegStatusCanceled
		}
	}
	b.Status = TopologyBatchAborted
	b.Reason = strings.TrimSpace(reason)
	return nil
}

func (m *MemoryTopologyWorkers) WorkflowWorkStatuses(
	_ context.Context,
	workflowRunID string,
) (map[string]api.LegStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	batchID, ok := m.byWorkflowRun[strings.TrimSpace(workflowRunID)]
	if !ok {
		return nil, nil
	}
	list := m.units[batchID]
	out := make(map[string]api.LegStatus, len(list))
	for _, u := range list {
		out[strings.TrimSpace(u.WorkID)] = u.Status
	}
	return out, nil
}

func (m *MemoryTopologyWorkers) recomputeBatchLocked(batchID string) {
	b, ok := m.batches[batchID]
	if !ok || b.Status == TopologyBatchAborted {
		return
	}
	list := m.units[batchID]
	if len(list) == 0 {
		return
	}
	anyFailed := false
	for _, u := range list {
		if !u.Status.IsTerminal() {
			return
		}
		if u.Status == api.LegStatusFailed || u.Status == api.LegStatusCanceled {
			anyFailed = true
		}
	}
	if anyFailed {
		b.Status = TopologyBatchFailed
		return
	}
	b.Status = TopologyBatchDone
}
