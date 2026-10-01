package project

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
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/keylock"
	"github.com/lycaon/lycaon/internal/people/peoplestore"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

var (
	ErrSourceMutationConflict = errors.New("source mutation operation conflict")
	ErrSourceMutationDiverged = errors.New("source mutation filesystem diverged")
)

// SourceMutationService performs durable filesystem changes.
type SourceMutationService struct {
	db     db.Handle
	people *peoplestore.Store
	ledger *sourceledger.Store
	// stateMu guards in-memory state and affected-path reservations.
	stateMu         sync.Mutex
	memory          map[string]*sourceMutationRow
	history         map[string]*sourceHistoryEntry
	historySeq      int64
	operationLocks  keylock.Group
	recoveryBytes   map[string][]byte
	recoveryEntries map[string][]sourceledger.RecoveryEntry
	reservations    map[*sourceReservation]struct{}
	historyLocks    keylock.Group
	sameFilesystem  func(string, string) (bool, error)
	trash           func(context.Context, string) error
}

func NewSourceMutationService(database db.Handle, ledger *sourceledger.Store) *SourceMutationService {
	return &SourceMutationService{
		db: database, ledger: ledger, trash: desktoptrash.Move, sameFilesystem: fspath.SameFilesystem, recoveryBytes: make(map[string][]byte), recoveryEntries: make(map[string][]sourceledger.RecoveryEntry),
		people:  peoplestore.New(database),
		memory:  make(map[string]*sourceMutationRow),
		history: make(map[string]*sourceHistoryEntry),
	}
}

func (s *SourceMutationService) execute(ctx context.Context, operationID, projectID, inputDigest string, prepare func() (*sourceMutationPlan, error)) (json.RawMessage, error) {
	if s == nil || strings.TrimSpace(operationID) == "" {
		return nil, fmt.Errorf("source mutation operation id required")
	}
	unlock, err := s.operationLocks.Acquire(ctx, operationID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	row, err := s.prepareExecution(ctx, operationID, projectID, inputDigest, prepare)
	var release func()
	if err == nil && row.Status != sourceMutationCommitted {
		release, err = s.reserveSourcePlan(row.Plan)
	}
	if err != nil {
		if row != nil && errors.Is(err, ErrSourceBusy) {
			row.Status, row.Error = sourceMutationFailed, err.Error()
			_ = s.update(context.WithoutCancel(ctx), row)
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
	row, found, err := s.load(ctx, operationID)
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
			if err := s.update(ctx, row); err != nil {
				return nil, err
			}
		}
		if row.Status == sourceMutationDiverged {
			return nil, fmt.Errorf("%w: %s", ErrSourceMutationDiverged, row.Error)
		}
	} else {
		plan, prepareErr := prepare()
		if prepareErr != nil {
			return nil, errors.Join(prepareErr, s.pruneSourceRecovery(context.WithoutCancel(ctx), projectID))
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
		if err := s.insert(ctx, row); err != nil {
			return nil, errors.Join(err, s.pruneSourceRecovery(context.WithoutCancel(ctx), projectID))
		}
	}
	return row, nil
}

func (s *SourceMutationService) resume(ctx context.Context, row *sourceMutationRow) (json.RawMessage, error) {
	appliedNow := false
	if row.Status == sourceMutationPrepared {
		if err := s.prepareSourceContents(ctx, row); err != nil {
			err = s.finishSourcePreparationFailure(ctx, row, err)
			row.Status, row.Error = sourceMutationFailed, err.Error()
			_ = s.update(context.WithoutCancel(ctx), row)
			return nil, err
		}
	}
	if row.Status == sourceMutationPrepared {
		if err := s.applyMutation(ctx, row); err != nil {
			err = s.finishSourcePreparationFailure(ctx, row, err)
			if row.Plan.CrossVolume && row.Plan.HoldStarted {
				row.Status, row.Error = sourceMutationFailed, err.Error()
				_ = s.update(context.WithoutCancel(ctx), row)
				return nil, &SourceMoveIncompleteError{Cause: err, HeldPath: row.Plan.HoldAbs}
			}
			if isSourceDivergence(err) {
				row.Status, row.Error = sourceMutationDiverged, err.Error()
				_ = s.update(ctx, row)
				return nil, fmt.Errorf("%w: %w", ErrSourceMutationDiverged, err)
			}
			row.Error = err.Error()
			if row.Plan.Kind == "delete" {
				row.Status = sourceMutationFailed
			}
			_ = s.update(context.WithoutCancel(ctx), row)
			return nil, err
		}
		appliedNow = true
		row.Status, row.Error = sourceMutationFileApplied, ""
		if err := s.update(ctx, row); err != nil {
			return nil, err
		}
	}
	// Copy and restore carry their stage validation through native publication.
	needsVerification := !appliedNow || (row.Plan.Kind != "copy" && row.Plan.Kind != "restore")
	if row.Status == sourceMutationFileApplied && needsVerification {
		if err := verifySourceMutationApplied(ctx, &row.Plan); err != nil {
			if applyErr := s.applyMutation(ctx, row); applyErr != nil {
				if isSourceDivergence(applyErr) {
					row.Status, row.Error = sourceMutationDiverged, applyErr.Error()
					_ = s.update(ctx, row)
					return nil, fmt.Errorf("%w: %w", ErrSourceMutationDiverged, applyErr)
				}
				return nil, applyErr
			}
			if verifyErr := verifySourceMutationApplied(ctx, &row.Plan); verifyErr != nil {
				row.Status, row.Error = sourceMutationDiverged, verifyErr.Error()
				_ = s.update(ctx, row)
				return nil, fmt.Errorf("%w: %w", ErrSourceMutationDiverged, verifyErr)
			}
		}
	}
	if err := s.commit(ctx, row); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), row.Response...), nil
}
