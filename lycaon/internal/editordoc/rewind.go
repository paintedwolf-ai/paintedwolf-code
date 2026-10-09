package editordoc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// WithSourceRewind excludes document commands from preflight through publication.
func (s *Service) WithSourceRewind(ctx context.Context, p *project.Project, files []sourceledger.RewindFile, apply func() error) error {
	return s.sourceRewindDocuments(ctx, p, files, "apply", apply)
}

// RecoverSourceRewind reconciles an already applied source transaction. Later
// document edits are preserved through the published replica when it rejoins.
func (s *Service) RecoverSourceRewind(ctx context.Context, p *project.Project, files []sourceledger.RewindFile, apply func() error) error {
	return s.sourceRewindDocuments(ctx, p, files, "recover", apply)
}

func (s *Service) RollbackSourceRewind(ctx context.Context, p *project.Project, files []sourceledger.RewindFile, apply func() error) error {
	return s.sourceRewindDocuments(ctx, p, files, "rollback", apply)
}

func (s *Service) sourceRewindDocuments(ctx context.Context, p *project.Project, files []sourceledger.RewindFile, mode string, apply func() error) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	docs, err := s.store.ListProject(ctx, p.ID, "")
	if err != nil {
		return err
	}
	selected := rewindDocuments(docs, files)
	if mode == "apply" {
		if issues := rewindDocumentIssues(docs, selected); len(issues) > 0 {
			return &sourceledger.RewindBlockedError{Issues: issues}
		}
	}
	applyErr := apply()
	var reconcileErr error
	for _, d := range docs {
		f, ok := selected[d.ID]
		if !ok {
			continue
		}
		destination := f.Target.Path
		if mode == "rollback" || applyErr != nil {
			destination = f.Expected.Path
		}
		if applyErr != nil {
			if ledger, ok := s.ledger.(interface {
				ResolveHeadByFile(context.Context, string, sourcebranch.ID, string) (sourceledger.BranchHead, error)
			}); ok {
				head, err := ledger.ResolveHeadByFile(ctx, p.ID, f.BranchID, f.FileID)
				if err != nil {
					reconcileErr = errors.Join(reconcileErr, err)
					continue
				}
				destination = head.Path
			}
		}
		if d.Path != destination {
			_, err := s.store.db.ExecContext(ctx, `UPDATE editor_documents SET path=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, destination, time.Now().UTC().Format(time.RFC3339Nano), d.ID, d.Revision)
			if err != nil {
				reconcileErr = errors.Join(reconcileErr, err)
				continue
			}
		}
		expectedSHA := f.Target.SHA256
		if mode == "rollback" || applyErr != nil {
			expectedSHA = f.Expected.SHA256
		}
		checkpoint := f.DocumentCheckpoint
		if mode == "rollback" || applyErr != nil {
			checkpoint = nil
		}
		if err := s.reconcileRewoundDocument(ctx, p, d.ID, expectedSHA, f.ActorPersonID, checkpoint); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("reconcile rewound document %s: %w", d.Path, err))
		}
	}
	return errors.Join(applyErr, reconcileErr)
}

func rewindDocuments(docs []*Document, files []sourceledger.RewindFile) map[string]sourceledger.RewindFile {
	selected := map[string]sourceledger.RewindFile{}
	for _, d := range docs {
		for _, f := range files {
			if d.FileID == f.FileID && d.BranchID == f.BranchID && d.RootID == f.Expected.RootID && (d.Path == f.Expected.Path || d.Path == f.Target.Path) {
				selected[d.ID] = f
			}
		}
	}
	return selected
}

func rewindDocumentIssues(docs []*Document, selected map[string]sourceledger.RewindFile) []sourceledger.RewindIssue {
	issues := []sourceledger.RewindIssue{}
	for _, d := range docs {
		f, ok := selected[d.ID]
		if !ok {
			continue
		}
		if d.Dirty || d.Diverged || (f.DocumentID != "" && (f.DocumentID != d.ID || f.DocumentRevision != d.Revision)) {
			issues = append(issues, sourceledger.RewindIssue{RootID: d.RootID, Path: d.Path, Code: "document_has_unsaved_changes"})
		}
	}
	return issues
}

func (s *Service) CheckSourceRewind(ctx context.Context, p *project.Project, files []sourceledger.RewindFile) ([]sourceledger.RewindIssue, error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	docs, err := s.store.ListProject(ctx, p.ID, "")
	if err != nil {
		return nil, err
	}
	return rewindDocumentIssues(docs, rewindDocuments(docs, files)), nil
}

func (s *Service) reconcileRewoundDocument(ctx context.Context, p *project.Project, id, expectedSHA, personID string, checkpoint []byte) error {
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	d, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	read, err := readDocumentSource(p, d)
	if err != nil {
		if errors.Is(err, projectsource.ErrSourceNotFound) || errors.Is(err, os.ErrNotExist) {
			return s.markAbsent(ctx, d)
		}
		return err
	}
	if d.BaseSHA256 == read.SHA256 {
		if d.Diverged || d.Absent {
			previous := d.Revision
			d.Diverged, d.Absent = false, false
			d.Revision++
			d.UpdatedAt = time.Now().UTC()
			if err := s.store.UpdateCAS(ctx, d, previous); err != nil {
				return err
			}
			s.changed(ctx, d, false)
		}
		return nil
	}
	actor := filesystemActor
	if read.SHA256 == expectedSHA {
		if personID != "" {
			actor = textActor{kind: actorRestore, personID: personID}
		} else {
			actor, err = s.personActor(ctx, actorRestore, "")
			if err != nil {
				return err
			}
		}
	}
	if read.SHA256 != expectedSHA {
		checkpoint = nil
	}
	if err := s.importPublishedReplica(ctx, d, read, actor, checkpoint); err != nil {
		return err
	}
	s.changed(ctx, d, true)
	return nil
}
