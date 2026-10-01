package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"time"

	storedb "github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people/peoplestore"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store struct {
	db     storedb.Handle
	people *peoplestore.Store
}

func NewStore(database storedb.Handle) *Store {
	return &Store{db: database, people: peoplestore.New(database)}
}

func (s *Store) get(ctx context.Context, query string, args ...any) (*Document, error) {
	return scanDocument(s.db.QueryRowContext(ctx, query, args...))
}

const documentColumns = `id, project_id, branch_id, file_id, root_id, path,
draft, base_content, base_sha256, encoding, size_bytes, eol, base_eol,
mixed_eol, base_mixed_eol, revision, dirty, diverged, created_at, updated_at,
held_agent_version_id, absent`

func (s *Store) Get(ctx context.Context, id string) (*Document, error) {
	return s.get(ctx, `SELECT `+documentColumns+` FROM editor_documents WHERE id = ?`, id)
}

// GetByIdentity answers the one document a branch path has, present or absent.
func (s *Store) GetByIdentity(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (*Document, error) {
	return s.get(ctx, `SELECT `+documentColumns+` FROM editor_documents WHERE project_id = ? AND branch_id = ? AND root_id = ? AND path = ?`, projectID, branch, rootID, path)
}

func (s *Store) ListProject(ctx context.Context, projectID, rootID string) ([]*Document, error) {
	query := `SELECT ` + documentColumns + ` FROM editor_documents WHERE project_id = ?`
	args := []any{projectID}
	if rootID != "" {
		query += ` AND root_id = ?`
		args = append(args, rootID)
	}
	query += ` ORDER BY path, id`
	return s.queryDocuments(ctx, query, args...)
}

func (s *Store) queryDocuments(ctx context.Context, query string, args ...any) ([]*Document, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var documents []*Document
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		documents = append(documents, d)
	}
	return documents, rows.Err()
}

func scanDocument(row rowScanner) (*Document, error) {
	var d Document
	var mixed, baseMixed, dirty, diverged, absent int
	var created, updated string
	if err := row.Scan(&d.ID, &d.ProjectID, &d.BranchID, &d.FileID, &d.RootID, &d.Path,
		&d.Draft, &d.BaseContent, &d.BaseSHA256, &d.Encoding, &d.SizeBytes, &d.EOL, &d.BaseEOL,
		&mixed, &baseMixed, &d.Revision, &dirty, &diverged, &created, &updated,
		&d.HeldAgentVersionID, &absent); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	d.MixedEOL, d.BaseMixedEOL, d.Dirty, d.Diverged, d.Absent = mixed != 0, baseMixed != 0, dirty != 0, diverged != 0, absent != 0
	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &d, nil
}

func (s *Store) Insert(ctx context.Context, d *Document) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO editor_documents (`+documentColumns+`, opened_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.ProjectID, d.BranchID, d.FileID, d.RootID, d.Path, d.Draft, d.BaseContent,
		d.BaseSHA256, d.Encoding, d.SizeBytes, d.EOL, d.BaseEOL, d.MixedEOL, d.BaseMixedEOL,
		d.Revision, d.Dirty, d.Diverged, d.CreatedAt.Format(time.RFC3339Nano), d.UpdatedAt.Format(time.RFC3339Nano),
		d.HeldAgentVersionID, d.Absent, d.CreatedAt.Format(time.RFC3339Nano))
	return err
}

// MarkOpened stamps the document's open time without changing its revision.
func (s *Store) MarkOpened(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE editor_documents SET opened_at=? WHERE id=?`, at.UTC().Format(time.RFC3339Nano), id)
	return err
}

// execer accepts direct and transactional writes.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) UpdateCAS(ctx context.Context, d *Document, expected int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error { return s.UpdateCASTx(ctx, tx, d, expected) })
}

// UpdateCASTx advances the document inside the caller's transaction.
func (s *Store) UpdateCASTx(ctx context.Context, tx *sql.Tx, d *Document, expected int64) error {
	if err := updateCAS(ctx, tx, d, expected); err != nil {
		return err
	}
	if d.PublishedRevision > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE editor_replica_heads SET published_revision=? WHERE document_id=?`, d.PublishedRevision, d.ID); err != nil {
			return err
		}
	}
	if len(d.publishedCheckpoint) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE editor_replica_heads SET published_checkpoint=? WHERE document_id=?`, d.publishedCheckpoint, d.ID); err != nil {
			return err
		}
	}
	if d.filesystemClient != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE editor_replica_heads SET filesystem_client=? WHERE document_id=?`, d.filesystemClient, d.ID); err != nil {
			return err
		}
	}
	return commitReplica(ctx, tx, d)
}

func updateCAS(ctx context.Context, x execer, d *Document, expected int64) error {
	res, err := x.ExecContext(ctx, `UPDATE editor_documents SET file_id=?, draft=?, base_content=?, base_sha256=?, encoding=?, size_bytes=?, eol=?, base_eol=?, mixed_eol=?, base_mixed_eol=?, revision=?, dirty=?, diverged=?, updated_at=?, held_agent_version_id=?, absent=? WHERE id=? AND revision=?`,
		d.FileID, d.Draft, d.BaseContent, d.BaseSHA256, d.Encoding, d.SizeBytes, d.EOL, d.BaseEOL, d.MixedEOL,
		d.BaseMixedEOL, d.Revision, d.Dirty, d.Diverged, d.UpdatedAt.Format(time.RFC3339Nano),
		d.HeldAgentVersionID, d.Absent, d.ID, expected)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrRevisionConflict
	}
	return nil
}

// Tx runs fn in one transaction, rolling back everything it wrote if fn fails.
func (s *Store) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) InsertMutation(ctx context.Context, m *Mutation) error {
	return s.Tx(ctx, func(tx *sql.Tx) error { return s.InsertMutationTx(ctx, tx, m) })
}

func (s *Store) InsertMutationTx(ctx context.Context, tx *sql.Tx, m *Mutation) error {
	if err := insertMutation(ctx, tx, m); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM editor_save_pins WHERE operation_id=? AND document_id=?`, m.ID, m.DocumentID); err != nil {
		return err
	}
	return trimSnapshots(ctx, tx, "", 0)
}

func insertMutation(ctx context.Context, x execer, m *Mutation) error {
	_, err := x.ExecContext(ctx, `INSERT INTO editor_mutations (id, input_digest, document_id, project_id, branch_id, file_id, root_id, path, expected_sha256, after_sha256, encoding, content, before_bytes, after_bytes, status, session_id, turn, error, response_json, created_at, updated_at, draft_revision, eol, origin, person_id, tool_call_id, tool_name, checkpoint, before_checkpoint, client_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.InputDigest, m.DocumentID, m.ProjectID, m.BranchID, m.FileID, m.RootID, m.Path,
		m.ExpectedSHA256, m.AfterSHA256, m.Encoding, m.Content, m.BeforeBytes,
		m.AfterBytes, m.Status, m.SessionID, m.Turn, m.Error, nullableString(m.ResponseJSON),
		m.CreatedAt.Format(time.RFC3339Nano), m.UpdatedAt.Format(time.RFC3339Nano), m.DraftRevision, m.EOL,
		string(m.Origin), nullableString(m.PersonID), m.ToolCallID, m.ToolName, m.Checkpoint, m.BeforeCheckpoint, m.ClientID)
	return err
}

func (s *Store) Mutation(ctx context.Context, id string) (*Mutation, error) {
	return scanMutation(s.db.QueryRowContext(ctx, `SELECT `+mutationColumns+` FROM editor_mutations WHERE id=?`, id))
}

const mutationColumns = `id, input_digest, document_id, project_id, branch_id, file_id, root_id, path, expected_sha256, after_sha256, encoding, content, before_bytes, after_bytes, status, session_id, turn, error, COALESCE(response_json, ''), created_at, updated_at, draft_revision, eol, origin, COALESCE(person_id, ''), tool_call_id, tool_name, checkpoint, before_checkpoint, replay_compacted, client_id`

type rowScanner interface{ Scan(...any) error }

func scanMutation(row rowScanner) (*Mutation, error) {
	var m Mutation
	var created, updated, origin string
	err := row.Scan(&m.ID, &m.InputDigest, &m.DocumentID, &m.ProjectID, &m.BranchID, &m.FileID, &m.RootID, &m.Path, &m.ExpectedSHA256, &m.AfterSHA256, &m.Encoding, &m.Content, &m.BeforeBytes, &m.AfterBytes, &m.Status, &m.SessionID, &m.Turn, &m.Error, &m.ResponseJSON, &created, &updated, &m.DraftRevision, &m.EOL, &origin, &m.PersonID, &m.ToolCallID, &m.ToolName, &m.Checkpoint, &m.BeforeCheckpoint, &m.ReplayCompacted, &m.ClientID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Origin = api.SourceChangeOrigin(origin)
	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &m, nil
}

func (s *Store) UpdateMutation(ctx context.Context, m *Mutation) error {
	return updateMutation(ctx, s.db, m)
}

// UpdateMutationTx advances the mutation inside the caller's transaction.
func (s *Store) UpdateMutationTx(ctx context.Context, tx *sql.Tx, m *Mutation) error {
	return updateMutation(ctx, tx, m)
}

func updateMutation(ctx context.Context, x execer, m *Mutation) error {
	_, err := x.ExecContext(ctx, `UPDATE editor_mutations SET status=?, error=?, before_bytes=?, after_bytes=?, content=?, response_json=?, checkpoint=?, before_checkpoint=?, updated_at=? WHERE id=?`, m.Status, m.Error, m.BeforeBytes, m.AfterBytes, m.Content, nullableString(m.ResponseJSON), m.Checkpoint, m.BeforeCheckpoint, m.UpdatedAt.Format(time.RFC3339Nano), m.ID)
	return err
}

func (s *Store) PendingMutations(ctx context.Context) ([]*Mutation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mutationColumns+` FROM editor_mutations WHERE status IN ('prepared','file_applied') ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Mutation
	for rows.Next() {
		m, err := scanMutation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
