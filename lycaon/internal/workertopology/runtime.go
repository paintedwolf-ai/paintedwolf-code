// Package workertopology binds orchestrated workflow topologies onto durable
// worker records and the worker queue.
package workertopology

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// Runtime implements orchestration.TopologyWorkers, orchestration.TopologyDirectSettler,
// and orchestration.RunWorkerStore over durable worker records and the worker queue.
type Runtime struct {
	Records *workerrecords.Store
	Queue   worker.WorkerQueue

	settleMu      sync.Mutex
	mu            sync.Mutex
	batches       map[string]*orchestration.TopologyBatch
	units         map[string][]orchestration.TopologyUnit
	bySession     map[string]string
	byWorkflowRun map[string]string
}

// New constructs a durable worker-backed topology runtime.
func New(records *workerrecords.Store, queue worker.WorkerQueue) *Runtime {
	return &Runtime{
		Records:       records,
		Queue:         queue,
		batches:       make(map[string]*orchestration.TopologyBatch),
		units:         make(map[string][]orchestration.TopologyUnit),
		bySession:     make(map[string]string),
		byWorkflowRun: make(map[string]string),
	}
}

func (r *Runtime) CreateBatch(
	ctx context.Context,
	batch orchestration.TopologyBatch,
	units []orchestration.TopologyUnit,
) (*orchestration.TopologyBatch, []orchestration.TopologyUnit, error) {
	if r == nil || r.Records == nil {
		return nil, nil, fmt.Errorf("topology worker records not configured")
	}
	if strings.TrimSpace(batch.ID) == "" {
		batch.ID = uuid.NewString()
	}
	if batch.Status == "" {
		batch.Status = orchestration.TopologyBatchActive
	}
	actor := coordinatorActorForSession(batch.SessionID)
	createdUnits := make([]orchestration.TopologyUnit, len(units))
	idMap := make(map[string]string, len(units))
	for i, u := range units {
		mode := workerrecords.ScopeRead
		if strings.TrimSpace(u.AgentType) == orchestration.ProfileImplementer {
			mode = workerrecords.ScopeWrite
		}
		workflowPhase := ""
		workflowWorkID := ""
		if strings.TrimSpace(batch.WorkflowRunID) != "" {
			workflowPhase = u.Phase
			workflowWorkID = u.WorkID
		} else if strings.TrimSpace(batch.BlueprintPath) != "" {
			workflowWorkID = u.WorkID
		}
		op := workerrecords.Op{ID: "topology:" + batch.ID + ":worker:" + u.WorkID}
		receipt, err := r.Records.CreateWorker(ctx, actor, op, workerrecords.WorkerSpec{
			AgentType:      u.AgentType,
			ScopeMode:      mode,
			Brief:          u.Prompt,
			WorkflowRunID:  batch.WorkflowRunID,
			WorkflowPhase:  workflowPhase,
			WorkflowWorkID: workflowWorkID,
			BlueprintPath:  batch.BlueprintPath,
			Files:          []string{"**/*"},
		})
		if err != nil {
			return nil, nil, err
		}
		if u.ID != "" {
			idMap[u.ID] = receipt.WorkerID
		}
		u.ID = receipt.WorkerID
		u.WorkerID = receipt.WorkerID
		u.Status = api.LegStatusPending
		createdUnits[i] = u
	}
	for i := range createdUnits {
		if mapped, ok := idMap[createdUnits[i].ParentID]; ok {
			createdUnits[i].ParentID = mapped
		}
	}

	r.mu.Lock()
	stored := batch
	r.batches[stored.ID] = &stored
	r.units[stored.ID] = append([]orchestration.TopologyUnit(nil), createdUnits...)
	if s := strings.TrimSpace(stored.SessionID); s != "" {
		r.bySession[s] = stored.ID
	}
	if wfRun := strings.TrimSpace(stored.WorkflowRunID); wfRun != "" {
		r.byWorkflowRun[wfRun] = stored.ID
	}
	r.mu.Unlock()

	out := stored
	return &out, createdUnits, nil
}

func (r *Runtime) BatchBySession(_ context.Context, sessionID string) (string, bool, error) {
	if r == nil {
		return "", false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.bySession[strings.TrimSpace(sessionID)]
	return id, ok, nil
}

func (r *Runtime) BatchByWorkflowRun(ctx context.Context, workflowRunID string) (string, bool, error) {
	workflowRunID = strings.TrimSpace(workflowRunID)
	if r == nil || r.Records == nil || workflowRunID == "" {
		return "", false, nil
	}
	r.mu.Lock()
	if id, ok := r.byWorkflowRun[workflowRunID]; ok {
		r.mu.Unlock()
		return id, true, nil
	}
	r.mu.Unlock()

	workers, err := r.Records.ByWorkflowRun(ctx, workflowRunID)
	if err != nil {
		return "", false, err
	}
	var bound []workerrecords.Worker
	for _, w := range workers {
		if w.WorkflowWorkID != "" && w.ClosedReason != "superseded" {
			bound = append(bound, w)
		}
	}
	if len(bound) == 0 {
		return "", false, nil
	}
	batchID := "wf:" + workflowRunID
	units := make([]orchestration.TopologyUnit, len(bound))
	for i, w := range bound {
		status, summary, jobID, wsID, wsRoot, pErr := r.projectWorkerUnit(ctx, w)
		if pErr != nil {
			return "", false, pErr
		}
		units[i] = orchestration.TopologyUnit{
			ID:            w.ID,
			WorkID:        w.WorkflowWorkID,
			AgentType:     w.AgentType,
			Prompt:        w.Brief,
			Phase:         w.WorkflowPhase,
			WorkspaceID:   wsID,
			WorkspaceRoot: wsRoot,
			WorkerID:      w.ID,
			JobID:         jobID,
			Status:        status,
			Summary:       summary,
		}
	}
	batch := orchestration.TopologyBatch{
		ID:            batchID,
		SessionID:     bound[0].SessionID,
		ProjectID:     bound[0].ProjectID,
		WorkflowRunID: workflowRunID,
		BlueprintPath: bound[0].BlueprintPath,
		Status:        computeBatchStatus(units, orchestration.TopologyBatchActive),
	}

	r.mu.Lock()
	if existing, ok := r.byWorkflowRun[workflowRunID]; ok {
		r.mu.Unlock()
		return existing, true, nil
	}
	r.batches[batchID] = &batch
	r.units[batchID] = units
	r.byWorkflowRun[workflowRunID] = batchID
	if batch.SessionID != "" {
		r.bySession[batch.SessionID] = batchID
	}
	r.mu.Unlock()
	return batchID, true, nil
}

func (r *Runtime) GetBatch(ctx context.Context, batchID string) (*orchestration.TopologyBatch, error) {
	units, err := r.ListUnits(ctx, batchID)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.batches[strings.TrimSpace(batchID)]
	if !ok {
		return nil, fmt.Errorf("topology batch %q not found", batchID)
	}
	b.Status = computeBatchStatus(units, b.Status)
	out := *b
	return &out, nil
}

func (r *Runtime) GetUnit(ctx context.Context, batchID, unitID string) (*orchestration.TopologyUnit, error) {
	units, err := r.ListUnits(ctx, batchID)
	if err != nil {
		return nil, err
	}
	for i := range units {
		if units[i].ID == unitID {
			out := units[i]
			return &out, nil
		}
	}
	return nil, fmt.Errorf("topology unit %q not found in batch %q", unitID, batchID)
}

func (r *Runtime) ListUnits(ctx context.Context, batchID string) ([]orchestration.TopologyUnit, error) {
	r.mu.Lock()
	b, ok := r.batches[strings.TrimSpace(batchID)]
	var snapshot []orchestration.TopologyUnit
	if ok {
		snapshot = append([]orchestration.TopologyUnit(nil), r.units[batchID]...)
	}
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("topology batch %q not found", batchID)
	}
	actor := coordinatorActorForSession(b.SessionID)
	for i := range snapshot {
		if snapshot[i].WorkerID == "" {
			continue
		}
		w, err := r.Records.Worker(ctx, actor, snapshot[i].WorkerID)
		if err != nil {
			return nil, err
		}
		status, summary, jobID, wsID, wsRoot, err := r.projectWorkerUnit(ctx, w)
		if err != nil {
			return nil, err
		}
		snapshot[i].Status = status
		snapshot[i].Summary = summary
		if jobID != "" {
			snapshot[i].JobID = jobID
		}
		if wsID != "" {
			snapshot[i].WorkspaceID = wsID
		}
		if wsRoot != "" {
			snapshot[i].WorkspaceRoot = wsRoot
		}
	}
	r.mu.Lock()
	if cur, exists := r.batches[batchID]; exists {
		r.units[batchID] = append([]orchestration.TopologyUnit(nil), snapshot...)
		cur.Status = computeBatchStatus(snapshot, cur.Status)
	}
	r.mu.Unlock()
	return snapshot, nil
}

func (r *Runtime) UpdateUnit(ctx context.Context, batchID string, unit orchestration.TopologyUnit) error {
	r.mu.Lock()
	b, ok := r.batches[strings.TrimSpace(batchID)]
	list := r.units[batchID]
	idx := -1
	for i := range list {
		if list[i].ID == unit.ID {
			idx = i
			break
		}
	}
	var prevPrompt string
	var sessionID string
	if ok && idx >= 0 {
		prevPrompt = list[idx].Prompt
		sessionID = b.SessionID
	}
	r.mu.Unlock()
	if !ok || idx < 0 {
		return fmt.Errorf("topology unit %q not found in batch %q", unit.ID, batchID)
	}
	if strings.TrimSpace(unit.Prompt) != "" && unit.Prompt != prevPrompt && unit.WorkerID != "" {
		actor := coordinatorActorForSession(sessionID)
		op := workerrecords.Op{ID: "topology:" + batchID + ":brief:" + unit.ID + ":" + uuid.NewString()}
		if _, err := r.Records.SetBrief(ctx, actor, op, unit.WorkerID, unit.Prompt); err != nil {
			return err
		}
	}
	r.mu.Lock()
	if cur := r.units[batchID]; idx < len(cur) && cur[idx].ID == unit.ID {
		cur[idx] = unit
	}
	r.mu.Unlock()
	return nil
}
