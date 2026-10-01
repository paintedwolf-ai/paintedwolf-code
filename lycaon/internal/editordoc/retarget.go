package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

type retargetRecord struct {
	ID, ProjectID, RootID, FromPath, ToPath, SourceOperationID string
	BranchID                                                   sourcebranch.ID
}

func (s *Service) PrepareRetarget(ctx context.Context, p *project.Project, plan project.SourceRenamePlan) (string, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	if plan.OperationID == "" {
		return "", fmt.Errorf("source operation identity required")
	}
	now := time.Now().UTC()
	intent := &retargetRecord{
		ID: uuid.NewString(), SourceOperationID: plan.OperationID, ProjectID: p.ID, BranchID: p.BranchForRoot(plan.RootID), RootID: plan.RootID,
		FromPath: filepath.ToSlash(strings.TrimSpace(plan.From)),
		ToPath:   filepath.ToSlash(strings.TrimSpace(plan.To)),
	}
	if rootPath(p, plan.RootID) == "" {
		return "", ErrRootDetached
	}
	_, err := s.store.db.ExecContext(ctx, `INSERT INTO editor_retargets (id, project_id, branch_id, root_id, from_path, to_path, created_at, source_operation_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		intent.ID, intent.ProjectID, intent.BranchID, intent.RootID,
		intent.FromPath, intent.ToPath, now.Format(time.RFC3339Nano), intent.SourceOperationID)
	if err != nil {
		return "", err
	}
	return intent.ID, nil
}

func (s *Service) ReconcileRetarget(ctx context.Context, id string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	return s.reconcileRetargetLocked(ctx, id)
}

// ReconcilePendingRetargets resolves durable rename intents.
func (s *Service) ReconcilePendingRetargets(ctx context.Context) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	ids, err := s.pendingRetargets(ctx)
	if err != nil {
		return err
	}
	var reconcileErr error
	for _, id := range ids {
		if err := s.reconcileRetargetLocked(ctx, id); err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		}
	}
	return reconcileErr
}

func (s *Service) reconcileRetargetLocked(ctx context.Context, id string) error {
	intent, err := s.retargetIntent(ctx, id)
	if err != nil {
		return err
	}
	var status string
	err = s.store.db.QueryRowContext(ctx, `SELECT status FROM source_mutations WHERE id=? AND project_id=? AND json_extract(plan_json,'$.root_id')=? AND json_extract(plan_json,'$.from_path')=? AND json_extract(plan_json,'$.to_path')=?`, intent.SourceOperationID, intent.ProjectID, intent.RootID, intent.FromPath, intent.ToPath).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "committed" {
		return nil
	}
	if err := s.retargetDocumentsLocked(ctx, intent); err != nil {
		return err
	}
	return s.finishRetarget(ctx, intent)
}

func (s *Service) retargetIntent(ctx context.Context, id string) (*retargetRecord, error) {
	var intent retargetRecord
	err := s.store.db.QueryRowContext(ctx, `SELECT id, project_id, branch_id, root_id, from_path, to_path, source_operation_id FROM editor_retargets WHERE id=?`, strings.TrimSpace(id)).Scan(
		&intent.ID, &intent.ProjectID, &intent.BranchID, &intent.RootID,
		&intent.FromPath, &intent.ToPath, &intent.SourceOperationID)
	if err != nil {
		return nil, err
	}
	return &intent, nil
}

func (s *Service) retargetDocumentsLocked(ctx context.Context, intent *retargetRecord) error {
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, path, revision FROM editor_documents WHERE project_id=? AND branch_id=? AND root_id=? AND (path=? OR substr(path, 1, ?) = ?) ORDER BY length(path) DESC`,
		intent.ProjectID, intent.BranchID, intent.RootID,
		intent.FromPath, len(intent.FromPath)+1, intent.FromPath+"/")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type update struct {
		id, path string
		revision int64
	}
	var updates []update
	for rows.Next() {
		var row update
		if err := rows.Scan(&row.id, &row.path, &row.revision); err != nil {
			return err
		}
		row.path = intent.ToPath + strings.TrimPrefix(row.path, intent.FromPath)
		updates = append(updates, row)
	}
	// An incomplete row scan cannot commit.
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var superseded []string
	for _, row := range updates {
		// The rename replaced the destination's file, so its document ends too.
		occupied, err := supersedeDocumentAt(ctx, tx, intent, row.path, row.id)
		if err != nil {
			return err
		}
		superseded = append(superseded, occupied...)
		result, err := tx.ExecContext(ctx, `UPDATE editor_documents SET path=?, revision=revision+1, updated_at=? WHERE id=? AND revision=?`, row.path, now, row.id, row.revision)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return ErrRevisionConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.forgetDocuments(ctx, superseded)
	for _, row := range updates {
		document, err := s.store.Get(ctx, row.id)
		if err != nil {
			return err
		}
		s.changed(ctx, document, false)
	}
	return nil
}

func scanDocumentIDs(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// supersedeDocumentAt removes any documents already at the path another
// document is moving onto and returns their ids.
func supersedeDocumentAt(ctx context.Context, tx *sql.Tx, intent *retargetRecord, path, mover string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM editor_documents WHERE project_id=? AND branch_id=? AND root_id=? AND path=? AND id<>?`,
		intent.ProjectID, intent.BranchID, intent.RootID, path, mover)
	if err != nil {
		return nil, err
	}
	ids, err := scanDocumentIDs(rows)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM source_text_contributions WHERE document_id=?`, id); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM editor_documents WHERE id=?`, id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// forgetDocuments drops the runtime state of documents whose rows are gone.
func (s *Service) forgetDocuments(ctx context.Context, ids []string) {
	if len(ids) == 0 {
		return
	}
	s.ForgetRemoved(ids)
	s.replicas.mu.Lock()
	for _, id := range ids {
		s.replicas.evict(ctx, id)
	}
	s.replicas.mu.Unlock()
	s.agentReads.mu.Lock()
	for key := range s.agentReads.bases {
		for _, id := range ids {
			if key.document == id {
				delete(s.agentReads.bases, key)
			}
		}
	}
	s.agentReads.mu.Unlock()
}

func (s *Service) finishRetarget(ctx context.Context, intent *retargetRecord) error {
	_, err := s.store.db.ExecContext(ctx, `DELETE FROM editor_retargets WHERE id=?`, intent.ID)
	return err
}

func (s *Service) pendingRetargets(ctx context.Context) ([]string, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT id FROM editor_retargets ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
