package db

import (
	"context"
	"errors"
)

// IntegrityAuditPending reports the durable cleanup obligation. Ordinary reads
// and writes remain available while verification is unfinished.
func (s *Store) IntegrityAuditPending(ctx context.Context) (bool, error) {
	if s == nil || s.reader == nil {
		return false, errors.New("inspect integrity obligation: reader is closed")
	}
	if err := s.Failure(); err != nil {
		return false, err
	}
	state, err := integrityAuditState(ctx, s.reader)
	if err != nil {
		return false, err
	}
	if err := s.Failure(); err != nil {
		return false, err
	}
	switch state {
	case "":
		return false, nil
	case integrityAuditPendingValue:
		return true, nil
	default:
		return false, storeIncompatible(RecoveryReasonIntegrityFailed, SchemaVersion, state)
	}
}

// WaitForIntegrityAudit delays irreversible cleanup, without holding a database
// connection or blocking checkpoints and foreground work.
func (s *Store) WaitForIntegrityAudit(ctx context.Context) error {
	if s == nil {
		return errors.New("wait for integrity audit: nil store")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.audit.mu.Lock()
		if s.audit.completed == nil {
			s.audit.completed = make(chan struct{})
		}
		completed := s.audit.completed
		s.audit.mu.Unlock()
		pending, err := s.IntegrityAuditPending(ctx)
		if err != nil || !pending {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.Failed():
			return s.Failure()
		case <-completed:
		}
	}
}

func (s *Store) auditCompleted() {
	s.audit.mu.Lock()
	if s.audit.completed != nil {
		close(s.audit.completed)
		s.audit.completed = nil
	}
	s.audit.mu.Unlock()
	s.signalMaintenance()
}

func pendingAuditResourceWork(ctx context.Context, database Handle, cfg RetentionConfig) (RetentionReport, error) {
	var report RetentionReport
	if cfg.IncrementalVacuumMinFreelistPages > 0 {
		vacuumed, err := runIncrementalVacuum(ctx, database, cfg.IncrementalVacuumMinFreelistPages)
		report.IncrementalVacuumed = vacuumed
		return report, err
	}
	return report, nil
}
