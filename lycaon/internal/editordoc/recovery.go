package editordoc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/textfile"
)

// Recover completes pending save intents.
func (s *Service) Recover(ctx context.Context) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	mutations, err := s.store.PendingMutations(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, mutation := range mutations {
		p, err := s.projectBranch(ctx, mutation.ProjectID, mutation.BranchID)
		if err != nil {
			failures = append(failures, fmt.Errorf("editor save %s: %w", mutation.ID, err))
			continue
		}
		if err := s.recoverMutation(ctx, p, mutation); err != nil {
			failures = append(failures, fmt.Errorf("editor save %s: %w", mutation.ID, err))
		}
	}
	if _, err := s.store.db.ExecContext(ctx, `DELETE FROM editor_retargets WHERE NOT EXISTS(SELECT 1 FROM source_mutations m WHERE m.id=editor_retargets.source_operation_id)`); err != nil {
		return err
	}
	retargets, err := s.pendingRetargets(ctx)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	for _, id := range retargets {
		if err := s.reconcileRetargetLocked(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) recoverMutation(ctx context.Context, p *project.Project, mutation *Mutation) error {
	document, err := s.store.Get(ctx, mutation.DocumentID)
	if err != nil {
		return err
	}
	if mutation.Status == "prepared" {
		// Recovery follows the root's current path.
		root, rootErr := s.liveRootPath(ctx, mutation.ProjectID, mutation.BranchID, mutation.RootID)
		if rootErr != nil {
			return s.abandonRecovery(ctx, document, mutation, rootErr)
		}
		raw, readErr := readPublicationPreimage(root, mutation.Path)
		// A publication that expected no file resumes while the path is still free.
		current := ""
		switch {
		case readErr == nil:
			current = textfile.SHA256(raw)
		case errors.Is(readErr, fs.ErrNotExist) && mutation.Creates():
		default:
			return s.abandonRecovery(ctx, document, mutation, readErr)
		}
		switch current {
		case mutation.AfterSHA256:
			mutation.Status = "file_applied"
			mutation.UpdatedAt = time.Now().UTC()
			if err := s.store.UpdateMutation(ctx, mutation); err != nil {
				return err
			}
		case mutation.ExpectedSHA256:
		default:
			return s.abandonRecovery(ctx, document, mutation, projectsource.ErrSourceWriteConflict)
		}
	}
	_, err = s.finishMutation(ctx, p, document, mutation)
	if err == nil {
		return nil
	}
	settled, readErr := s.store.Mutation(ctx, mutation.ID)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	if settled.Status == "failed" || settled.Status == "conflict" {
		return nil
	}
	return err
}

// An unavailable or changed target ends the intent, not the durable draft.
func (s *Service) abandonRecovery(ctx context.Context, d *Document, m *Mutation, cause error) error {
	return s.settlePublicationFailure(ctx, d, m, "conflict", cause)
}

func (s *Service) mutationMatchesDisk(ctx context.Context, m *Mutation) bool {
	root, err := s.liveRootPath(ctx, m.ProjectID, m.BranchID, m.RootID)
	if err != nil {
		return false
	}
	raw, err := readPublicationPreimage(root, m.Path)
	return err == nil && textfile.SHA256(raw) == m.AfterSHA256
}
