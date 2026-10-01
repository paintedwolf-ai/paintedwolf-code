package delegation

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	// ErrDelegationNotFound is returned when a delegation id is unknown.
	ErrDelegationNotFound = errors.New("delegation not found")
	// ErrDelegationSettled rejects changes to a closed delegation.
	ErrDelegationSettled = errors.New("delegation is settled")
	// ErrLegNotFound is returned when a leg id is unknown.
	ErrLegNotFound = errors.New("delegation leg not found")
	// ErrLegNotPending rejects dispatch of a leg that is no longer pending
	// (already dispatched, running, or terminal).
	ErrLegNotPending = errors.New("delegation leg is not pending")
)

type delegationRecord struct {
	delegation api.Delegation
	sessionID  string
	legs       map[string]*api.Leg
}

// MemoryStore holds in-memory delegations and legs.
type MemoryStore struct {
	mu          sync.RWMutex
	delegations map[string]*delegationRecord
	byProject   map[string][]string
	bySession   map[string]string          // sessionID → delegationID
	byWorkflow  map[string]string          // workflowRunID → delegationID
	operations  map[string]memoryOperation // operationID → receipt
}

type memoryOperation struct {
	inputDigest  string
	delegationID string
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore creates an empty delegation store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		delegations: make(map[string]*delegationRecord),
		byProject:   make(map[string][]string),
		bySession:   make(map[string]string),
		byWorkflow:  make(map[string]string),
		operations:  make(map[string]memoryOperation),
	}
}

// Create stores a complete delegation graph.
func (s *MemoryStore) Create(ctx context.Context, delegation api.Delegation, sessionID string, legs []api.Leg) (*api.Delegation, error) {
	return s.create(ctx, delegation, sessionID, legs, CreateReceipt{})
}

// CreateOnce stores the delegation with its receipt, or answers the delegation
// the receipt's operation already created.
func (s *MemoryStore) CreateOnce(ctx context.Context, delegation api.Delegation, sessionID string, legs []api.Leg, receipt CreateReceipt) (*api.Delegation, error) {
	return s.create(ctx, delegation, sessionID, legs, receipt)
}

// DelegationByOperation answers the delegation an operation created.
func (s *MemoryStore) DelegationByOperation(ctx context.Context, receipt CreateReceipt) (*api.Delegation, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, found, err := s.operationRecord(receipt)
	if err != nil || !found {
		return nil, false, err
	}
	out := rec.copyDelegation()
	return &out, true, nil
}

func (s *MemoryStore) operationRecord(receipt CreateReceipt) (*delegationRecord, bool, error) {
	stored, ok := s.operations[receipt.OperationID]
	if !ok {
		return nil, false, nil
	}
	if stored.inputDigest != receipt.InputDigest {
		return nil, false, ErrOperationConflict
	}
	rec, ok := s.delegations[stored.delegationID]
	return rec, ok, nil
}

func (s *MemoryStore) create(ctx context.Context, delegation api.Delegation, sessionID string, legs []api.Leg, receipt CreateReceipt) (*api.Delegation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(legs) == 0 {
		return nil, ErrLegNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if receipt.OperationID != "" {
		prior, found, err := s.operationRecord(receipt)
		if err != nil {
			return nil, err
		}
		if found {
			out := prior.copyDelegation()
			return &out, nil
		}
	}

	if delegation.ID == "" {
		delegation.ID = uuid.NewString()
	}
	if delegation.CreatedAt.IsZero() {
		delegation.CreatedAt = time.Now().UTC()
	}
	if delegation.Status == "" {
		delegation.Status = api.DelegationStatusActive
	}
	if delegation.Phase == "" {
		delegation.Phase = api.DelegationPhaseSetup
	}
	legRecords := make(map[string]*api.Leg, len(legs))
	for i := range legs {
		if legs[i].ID == "" {
			legs[i].ID = uuid.NewString()
		}
		legs[i].DelegationID = delegation.ID
		if legs[i].CreatedAt.IsZero() {
			legs[i].CreatedAt = time.Now().UTC()
		}
		if legs[i].Status == "" {
			legs[i].Status = api.LegStatusPending
		}
		leg := legs[i]
		legRecords[leg.ID] = &leg
	}
	if err := ValidateLegDependencies(legs); err != nil {
		return nil, err
	}

	rec := &delegationRecord{
		delegation: delegation,
		sessionID:  sessionID,
		legs:       legRecords,
	}
	s.delegations[delegation.ID] = rec
	s.byProject[delegation.ProjectID] = append(s.byProject[delegation.ProjectID], delegation.ID)
	if sessionID != "" {
		s.bySession[sessionID] = delegation.ID
	}
	if delegation.WorkflowRunID != "" {
		s.byWorkflow[delegation.WorkflowRunID] = delegation.ID
	}
	if receipt.OperationID != "" {
		s.operations[receipt.OperationID] = memoryOperation{inputDigest: receipt.InputDigest, delegationID: delegation.ID}
	}
	delegation.Legs = append([]api.Leg(nil), legs...)
	delegation.CoordinatorSessionID = sessionID
	out := delegation
	return &out, nil
}

// AddLeg appends a leg to an existing delegation.
func (s *MemoryStore) AddLeg(ctx context.Context, delegationID string, leg api.Leg) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.delegations[delegationID]
	if !ok {
		return ErrDelegationNotFound
	}
	if delegationSettled(&rec.delegation) {
		return ErrDelegationSettled
	}

	if leg.ID == "" {
		leg.ID = uuid.NewString()
	}
	leg.DelegationID = delegationID
	if leg.CreatedAt.IsZero() {
		leg.CreatedAt = time.Now().UTC()
	}
	if leg.Status == "" {
		leg.Status = api.LegStatusPending
	}
	existing := make([]api.Leg, 0, len(rec.legs)+1)
	for _, current := range rec.legs {
		existing = append(existing, *current)
	}
	if err := ValidateLegDependencies(append(existing, leg)); err != nil {
		return err
	}
	cp := leg
	rec.legs[leg.ID] = &cp
	return nil
}

// Get returns a delegation by id.
func (s *MemoryStore) Get(ctx context.Context, delegationID string) (*api.Delegation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.delegations[delegationID]
	if !ok {
		return nil, ErrDelegationNotFound
	}
	delegation := rec.copyDelegation()
	return &delegation, nil
}

// DelegationBySessionID returns the delegation linked to a coordinator session.
func (s *MemoryStore) DelegationBySessionID(sessionID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.bySession[sessionID]
	return id, ok
}

// DelegationByWorkflowRunID returns the delegation bound to one workflow run.
func (s *MemoryStore) DelegationByWorkflowRunID(ctx context.Context, workflowRunID string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byWorkflow[workflowRunID]
	return id, ok, nil
}

// SessionID returns the coordinator session for a delegation.
func (s *MemoryStore) SessionID(delegationID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.delegations[delegationID]
	if !ok {
		return "", false
	}
	return rec.sessionID, true
}

// GetLeg returns a leg by id.
func (s *MemoryStore) GetLeg(ctx context.Context, delegationID, legID string) (*api.Leg, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.delegations[delegationID]
	if !ok {
		return nil, ErrDelegationNotFound
	}
	leg, ok := rec.legs[legID]
	if !ok {
		return nil, ErrLegNotFound
	}
	out := *leg
	return &out, nil
}

// UpdateLeg updates a leg record.
func (s *MemoryStore) UpdateLeg(ctx context.Context, leg api.Leg) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.delegations[leg.DelegationID]
	if !ok {
		return ErrDelegationNotFound
	}
	if _, ok := rec.legs[leg.ID]; !ok {
		return ErrLegNotFound
	}
	current := rec.legs[leg.ID]
	if delegationSettled(&rec.delegation) || current.Status.IsTerminal() || current.WorkerID != leg.WorkerID {
		return nil
	}

	existing := make([]api.Leg, 0, len(rec.legs))
	for id, current := range rec.legs {
		if id == leg.ID {
			existing = append(existing, leg)
			continue
		}
		existing = append(existing, *current)
	}
	if err := ValidateLegDependencies(existing); err != nil {
		return err
	}
	cp := leg
	rec.legs[leg.ID] = &cp
	return nil
}

// Abort atomically settles every active leg and the delegation.
func (s *MemoryStore) Abort(ctx context.Context, delegationID string, completedAt time.Time, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.delegations[delegationID]
	if !ok {
		return ErrDelegationNotFound
	}
	if delegationSettled(&rec.delegation) {
		return nil
	}

	for _, leg := range rec.legs {
		switch leg.Status {
		case api.LegStatusPending, api.LegStatusDispatched, api.LegStatusRunning, api.LegStatusRetryPending, api.LegStatusHeld:
			leg.Status = api.LegStatusCanceled
			completed := completedAt
			leg.CompletedAt = &completed
		case api.LegStatusComplete, api.LegStatusFailed, api.LegStatusCanceled:
		}
	}
	rec.delegation.Status = api.DelegationStatusAborted
	rec.delegation.Phase = api.DelegationPhaseDone
	rec.delegation.Reason = reason
	return nil
}

// DispatchLegWithJob commits dispatch state together.
func (s *MemoryStore) DispatchLegWithJob(ctx context.Context, leg api.Leg, delegation api.Delegation, task api.WorkerTask) error {
	return s.dispatchLegWithJob(ctx, leg, delegation, task, api.LegStatusPending)
}

// RedispatchLegWithJob commits a continuation from retry_pending.
func (s *MemoryStore) RedispatchLegWithJob(ctx context.Context, leg api.Leg, delegation api.Delegation, task api.WorkerTask) error {
	return s.dispatchLegWithJob(ctx, leg, delegation, task, api.LegStatusRetryPending)
}

func (s *MemoryStore) dispatchLegWithJob(ctx context.Context, leg api.Leg, delegation api.Delegation, task api.WorkerTask, expected api.LegStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if leg.WorkerID == "" || task.ID == "" || leg.WorkerID != task.ID {
		return ErrLegNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.delegations[delegation.ID]
	if !ok {
		return ErrDelegationNotFound
	}
	if delegationSettled(&rec.delegation) {
		return ErrLegNotPending
	}

	current, ok := rec.legs[leg.ID]
	if !ok {
		return ErrLegNotFound
	}
	// pending-to-dispatched CAS: a leg that already dispatched must not
	// enqueue a second worker.
	if current.Status != expected {
		return ErrLegNotPending
	}
	legCopy := leg
	legCopy.Status = api.LegStatusDispatched
	rec.legs[leg.ID] = &legCopy
	rec.delegation.Status = delegation.Status
	rec.delegation.Phase = delegation.Phase
	rec.delegation.Task = delegation.Task
	return nil
}

// ListLegs returns all legs for a delegation.
func (s *MemoryStore) ListLegs(ctx context.Context, delegationID string) ([]api.Leg, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.delegations[delegationID]
	if !ok {
		return nil, ErrDelegationNotFound
	}
	out := make([]api.Leg, 0, len(rec.legs))
	for _, leg := range rec.legs {
		out = append(out, *leg)
	}
	return out, nil
}

// ListByProject returns delegations for a project directory.
func (s *MemoryStore) ListByProject(ctx context.Context, projectDir, sessionID string) ([]api.Delegation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.byProject[projectDir]
	out := make([]api.Delegation, 0, len(ids))
	for _, id := range ids {
		if rec, ok := s.delegations[id]; ok && (sessionID == "" || rec.sessionID == sessionID) {
			out = append(out, rec.copyDelegation())
		}
	}
	return out, nil
}

func (r *delegationRecord) copyDelegation() api.Delegation {
	delegation := r.delegation
	delegation.CoordinatorSessionID = r.sessionID
	delegation.Legs = make([]api.Leg, 0, len(r.legs))
	for _, leg := range r.legs {
		delegation.Legs = append(delegation.Legs, *leg)
	}
	return delegation
}
