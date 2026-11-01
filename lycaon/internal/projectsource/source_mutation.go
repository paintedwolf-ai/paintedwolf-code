package projectsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/desktoptrash"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/keylock"
	"github.com/lycaon/lycaon/internal/people/peoplestore"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

var (
	ErrSourceMutationConflict = errors.New("source mutation operation conflict")
	ErrSourceMutationDiverged = errors.New("source mutation filesystem diverged")
)

// SourceMutationService coordinates admitted source operations.
type SourceMutationService struct {
	recorder       SourceMutationRecorder
	operationLocks keylock.Group
	Journal        *SourceMutationJournal
	Paths          *SourcePaths
	History        *SourceHistory
	Versions       *SourceVersions
	Effects        *SourceEffects
	recovery       *sourceRecovery
	settlement     *sourceSettlement
}

func NewSourceMutationService(database db.Handle, ledger *sourceledger.Store) *SourceMutationService {
	var recorder SourceMutationRecorder
	var heads SourceHeadReader
	var objects RecoveryStorage
	if ledger != nil {
		recorder, heads, objects = ledger, ledger.History, ledger.Retention
	}
	journal := &SourceMutationJournal{db: database, people: peoplestore.New(database), memory: make(map[string]*sourceMutationRow)}
	reclaim := &recoveryReclaimer{}
	reclaim.cond = sync.NewCond(&reclaim.mu)
	recovery := &sourceRecovery{db: database, objects: objects, recoveryBytes: make(map[string][]byte), recoveryEntries: make(map[string][]sourceledger.RecoveryEntry), reclaim: reclaim}
	history := &SourceHistory{db: database, recovery: recovery, history: make(map[string]*sourceHistoryEntry)}
	recovery.quota = history.recoveryQuotaBytes
	service := &SourceMutationService{
		recorder: recorder, Journal: journal, Paths: &SourcePaths{}, History: history, recovery: recovery,
		Effects:    &SourceEffects{Journal: journal, recovery: recovery, trash: desktoptrash.Move, sameFilesystem: fspath.SameFilesystem, relocate: fseffect.RelocateGuarded},
		settlement: &sourceSettlement{db: database, recorder: recorder, Journal: journal, History: history},
	}
	history.onQuotaChanged = func() { scheduleReclamation(service) }
	service.Versions = &SourceVersions{heads: heads, recorder: recorder, recovery: recovery, execute: service.execute}
	if ledger != nil {
		ledger.SetMutationScopeProvider(journal)
	}
	return service
}

// Close waits for any in-flight background recovery reclamation to finish.
func (s *SourceMutationService) Close() error {
	return closeRecoveryReclamation(s)
}

// WaitReclamation waits for pending background recovery reclamation passes to complete.
func (s *SourceMutationService) WaitReclamation(ctx context.Context) error {
	return waitRecoveryReclamation(ctx, s)
}

func closeRecoveryReclamation(s *SourceMutationService) error {
	if s == nil {
		return nil
	}
	_ = waitRecoveryReclamation(context.Background(), s)
	r := s.recovery.reclaim
	r.mu.Lock()
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	r.cond.Broadcast()
	for r.running {
		r.cond.Wait()
	}
	r.mu.Unlock()
	return nil
}

func waitRecoveryReclamation(ctx context.Context, s *SourceMutationService) error {
	if s == nil {
		return nil
	}
	r := s.recovery.reclaim
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	if r.reqSeq == 0 {
		r.reqSeq = 1
		if !r.running {
			r.running = true
			//nolint:gosec // Background reclamation worker outlives the initiating request and drains on service Close.
			go reclaimWorkerLoop(s)
		}
		r.cond.Broadcast()
	}
	target := r.reqSeq
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			r.mu.Lock()
			r.cond.Broadcast()
			r.mu.Unlock()
		case <-done:
		}
	}()
	defer close(done)
	for !r.closed && r.doneSeq < target && ctx.Err() == nil {
		r.cond.Wait()
	}
	err := ctx.Err()
	r.mu.Unlock()
	return err
}

func scheduleReclamation(s *SourceMutationService) {
	if s == nil {
		return
	}
	r := s.recovery.reclaim
	r.mu.Lock()
	if !r.closed {
		r.reqSeq++
		if !r.running {
			r.running = true
			go reclaimWorkerLoop(s)
		}
		r.cond.Broadcast()
	}
	r.mu.Unlock()
}

func beginForegroundExecution(s *SourceMutationService) {
	r := s.recovery.reclaim
	r.mu.Lock()
	r.fgCount++
	if r.cancel != nil {
		r.cancel()
	}
	r.cond.Broadcast()
	r.mu.Unlock()
	s.recovery.lifecycle.RLock()
}

func endForegroundExecution(s *SourceMutationService) {
	s.recovery.lifecycle.RUnlock()
	r := s.recovery.reclaim
	r.mu.Lock()
	r.fgCount--
	if !r.closed {
		r.reqSeq++
		if !r.running {
			r.running = true
			go reclaimWorkerLoop(s)
		}
	}
	r.cond.Broadcast()
	r.mu.Unlock()
}

func reclaimWorkerLoop(s *SourceMutationService) {
	r := s.recovery.reclaim
	for {
		r.mu.Lock()
		for !r.closed && r.fgCount > 0 {
			r.cond.Wait()
		}
		if r.closed || r.doneSeq >= r.reqSeq {
			r.running = false
			r.cancel = nil
			r.cond.Broadcast()
			r.mu.Unlock()
			return
		}
		seq := r.reqSeq
		ctx, cancel := context.WithCancel(context.Background())
		r.cancel = cancel
		r.mu.Unlock()

		reclaimEvictedRecovery(ctx, s)
		canceled := ctx.Err() != nil
		cancel()

		r.mu.Lock()
		r.cancel = nil
		if !canceled && seq > r.doneSeq {
			r.doneSeq = seq
		}
		r.cond.Broadcast()
		r.mu.Unlock()
	}
}

func (s *SourceMutationService) execute(ctx context.Context, operationID, projectID, inputDigest string, memory sourceeffect.Memory, prepare func() (*sourceMutationPlan, error)) (json.RawMessage, error) {
	if memory == nil {
		return nil, fmt.Errorf("source operation memory is required")
	}
	if s == nil || strings.TrimSpace(operationID) == "" {
		return nil, fmt.Errorf("source mutation operation id required")
	}
	s.History.policyMu.RLock()
	defer s.History.policyMu.RUnlock()
	unlock, err := s.operationLocks.Acquire(ctx, operationID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	beginForegroundExecution(s)
	defer endForegroundExecution(s)
	if s.recovery.objects != nil {
		defer func() {
			if err := s.recovery.objects.SweepBlobs(context.WithoutCancel(ctx)); err != nil && !errors.Is(err, sourceledger.ErrBlobMaintenanceDeferred) {
				// Best-effort sweep for legacy retained recovery objects.
				_ = err
			}
		}()
		release := s.recovery.objects.HoldRecoveryReferences()
		defer release()
	}
	row, err := s.prepareExecution(ctx, operationID, projectID, inputDigest, prepare)
	if row != nil {
		row.memory = memory
	}
	var release func()
	if err == nil && row.Status != sourceMutationCommitted {
		release, err = s.Paths.reserveSourcePlan(row.Plan)
	}
	if err != nil {
		if row != nil && errors.Is(err, ErrSourceBusy) {
			row.Status, row.Error = sourceMutationFailed, err.Error()
			_ = s.Journal.update(context.WithoutCancel(ctx), row)
		}
		return nil, err
	}
	if row.Status == sourceMutationCommitted {
		return append(json.RawMessage(nil), row.Response...), nil
	}
	defer release()
	return s.resume(ctx, row)
}

func (s *SourceMutationService) prepareExecution(ctx context.Context, operationID, projectID, inputDigest string, prepare func() (*sourceMutationPlan, error)) (*sourceMutationRow, error) {
	row, found, err := s.Journal.load(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if found {
		if row.InputDigest != inputDigest {
			return nil, fmt.Errorf("%w: operation %s", ErrSourceMutationConflict, operationID)
		}
		if row.Status == sourceMutationCommitted {
			return row, nil
		}
		if row.Status == sourceMutationFailed {
			row.Status, row.Error = sourceMutationPrepared, ""
			if err := s.Journal.update(ctx, row); err != nil {
				return nil, err
			}
		}
		if row.Status == sourceMutationDiverged {
			return nil, fmt.Errorf("%w: %s", ErrSourceMutationDiverged, row.Error)
		}
	} else {
		plan, prepareErr := prepare()
		if prepareErr != nil {
			return nil, prepareErr
		}
		if plan.Kind == "copy" || plan.Kind == "rename" || plan.Kind == "restore" {
			target := plan.ToAbs
			if plan.Kind == "restore" {
				target = plan.AbsPath
			}
			plan.StageAbs = filepath.Join(filepath.Dir(target), ".paintedwolf-copy-"+operationID)
			if plan.Kind == "rename" {
				plan.HoldAbs = filepath.Join(filepath.Dir(plan.FromAbs), ".paintedwolf-move-"+operationID)
			}
		}
		row = &sourceMutationRow{ID: operationID, ProjectID: plan.ProjectID, Kind: plan.Kind,
			InputDigest: inputDigest, Plan: *plan, Status: sourceMutationPrepared, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if err := s.Journal.insert(ctx, row); err != nil {
			return nil, err
		}
	}
	return row, nil
}

func (s *SourceMutationService) resume(ctx context.Context, row *sourceMutationRow) (json.RawMessage, error) {
	appliedNow := false
	if row.Status == sourceMutationPrepared {
		if err := s.Effects.prepareSourceContents(ctx, row); err != nil {
			err = s.finishSourcePreparationFailure(ctx, row, err)
			row.Status, row.Error = sourceMutationFailed, err.Error()
			_ = s.Journal.update(context.WithoutCancel(ctx), row)
			return nil, err
		}
	}
	if row.Status == sourceMutationPrepared {
		if err := s.Effects.applyMutation(ctx, row); err != nil {
			var applied *sourceeffect.AppliedError
			if errors.As(err, &applied) {
				settled := context.WithoutCancel(ctx)
				row.Status, row.Error = sourceMutationFileApplied, err.Error()
				if updateErr := s.Journal.update(settled, row); updateErr != nil {
					return nil, errors.Join(err, updateErr)
				}
				return nil, errors.Join(err, s.settlement.commit(settled, row))
			}
			err = s.finishSourcePreparationFailure(ctx, row, err)
			if row.Plan.CrossVolume && row.Plan.HoldStarted {
				row.Status, row.Error = sourceMutationFailed, err.Error()
				_ = s.Journal.update(context.WithoutCancel(ctx), row)
				return nil, &SourceMoveIncompleteError{Cause: err, HeldPath: row.Plan.HoldAbs}
			}
			if isSourceDivergence(err) {
				row.Status, row.Error = sourceMutationDiverged, err.Error()
				_ = s.Journal.update(ctx, row)
				return nil, fmt.Errorf("%w: %w", ErrSourceMutationDiverged, err)
			}
			row.Error = err.Error()
			if row.Plan.Kind == "delete" || sourceFailureLeavesInput(&row.Plan) {
				row.Status = sourceMutationFailed
			}
			_ = s.Journal.update(context.WithoutCancel(ctx), row)
			return nil, err
		}
		appliedNow = true
		row.Status, row.Error = sourceMutationFileApplied, ""
		if err := s.Journal.update(ctx, row); err != nil {
			return nil, err
		}
	}
	// Copy and restore carry their stage validation through native publication.
	needsVerification := !appliedNow || (row.Plan.Kind != "copy" && row.Plan.Kind != "restore")
	if row.Status == sourceMutationFileApplied && needsVerification {
		if err := verifySourceMutationApplied(ctx, &row.Plan, row.memory); err != nil {
			if applyErr := s.Effects.applyMutation(ctx, row); applyErr != nil {
				if isSourceDivergence(applyErr) {
					row.Status, row.Error = sourceMutationDiverged, applyErr.Error()
					_ = s.Journal.update(ctx, row)
					return nil, fmt.Errorf("%w: %w", ErrSourceMutationDiverged, applyErr)
				}
				return nil, applyErr
			}
			if verifyErr := verifySourceMutationApplied(ctx, &row.Plan, row.memory); verifyErr != nil {
				row.Status, row.Error = sourceMutationDiverged, verifyErr.Error()
				_ = s.Journal.update(ctx, row)
				return nil, fmt.Errorf("%w: %w", ErrSourceMutationDiverged, verifyErr)
			}
		}
	}
	if err := s.settlement.commit(ctx, row); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), row.Response...), nil
}
