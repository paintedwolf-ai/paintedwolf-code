package extensionstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/extpacks"
)

const (
	journalPrepared     = "prepared"
	journalFilesApplied = "files_applied"
)

// SQLJournal persists extension operations in extension_operations.
type SQLJournal struct {
	db db.Handle
	mu sync.Mutex
}

// NewSQLJournal wraps the store handle.
func NewSQLJournal(database db.Handle) *SQLJournal {
	return &SQLJournal{db: database}
}

type journalRow struct {
	ID     string
	Status string
	Op     Operation
}

// Begin records the operation before any file is touched.
func (j *SQLJournal) Begin(ctx context.Context, op Operation) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	planJSON, err := json.Marshal(op)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var projectDir, projectID any
	if op.ProjectDir != "" {
		projectDir = op.ProjectDir
	}
	if op.ProjectID != "" {
		projectID = op.ProjectID
	}
	_, err = j.db.ExecContext(ctx,
		`INSERT INTO extension_operations(id, scope, project_dir, project_id, status, plan_json, created_at) VALUES(?,?,?,?,?,?,?)`,
		id, op.Scope, projectDir, projectID, journalPrepared, string(planJSON), now)
	if err != nil {
		return "", err
	}
	return id, nil
}

// FilesApplied marks both state files published.
func (j *SQLJournal) FilesApplied(ctx context.Context, id string) error {
	return j.setStatus(ctx, id, journalFilesApplied)
}

// Finish removes a settled recovery record.
func (j *SQLJournal) Finish(ctx context.Context, id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, err := j.db.ExecContext(ctx, `DELETE FROM extension_operations WHERE id=?`, id)
	return err
}

func (j *SQLJournal) setStatus(ctx context.Context, id, status string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, err := j.db.ExecContext(ctx,
		`UPDATE extension_operations SET status=? WHERE id=?`, status, id)
	return err
}

func (j *SQLJournal) pending(ctx context.Context) ([]journalRow, error) {
	rows, err := j.db.QueryContext(ctx,
		`SELECT id, status, plan_json FROM extension_operations WHERE status IN ('prepared','files_applied') ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []journalRow
	for rows.Next() {
		var row journalRow
		var planJSON string
		if err := rows.Scan(&row.ID, &row.Status, &planJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(planJSON), &row.Op); err != nil {
			return nil, fmt.Errorf("extension op %s plan: %w", row.ID, err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Recover settles interrupted operations before mutations are served.
func (j *SQLJournal) Recover(ctx context.Context, publisher Publisher, events Emitter) error {
	if j == nil || j.db == nil {
		return nil
	}
	rows, err := j.pending(ctx)
	if err != nil {
		return err
	}
	var recoveryErr error
	for _, row := range rows {
		if err := j.resume(ctx, row, publisher, events); err != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover extension op %s: %w", row.ID, err))
		}
	}
	return recoveryErr
}

func (j *SQLJournal) resume(ctx context.Context, row journalRow, publisher Publisher, events Emitter) error {
	op := row.Op
	contextDir := ""
	if op.Scope == "project" {
		contextDir = op.ProjectDir
	}
	release, err := extpacks.AcquireIntentLocks([]string{contextDir})
	if err != nil {
		return err
	}
	defer release()
	published, err := filesMatch(op)
	if err != nil {
		return err
	}
	if row.Status == journalPrepared && !published {
		// Restore the recorded source state.
		desiredErr := restoreStateFile(op.DesiredPath, stateFile{Bytes: op.PrevDesired, Missing: op.PrevDesiredGone})
		var lockErr error
		if strings.TrimSpace(op.LockPath) != "" {
			lockErr = restoreStateFile(op.LockPath, stateFile{Bytes: op.PrevLock, Missing: op.PrevLockGone})
		}
		if err := errors.Join(desiredErr, lockErr); err != nil {
			return err
		}
		return j.Finish(ctx, row.ID)
	}
	// Complete the published transition.
	if err := j.Finish(ctx, row.ID); err != nil {
		return err
	}
	// A project operation with no registry id — a CLI mutation on a working copy
	// — has no project for the app to refresh.
	if publisher != nil && (op.Scope != "project" || op.ProjectID != "") {
		publisher.InvalidateProjects(ctx, op.ProjectID)
	}
	if events != nil {
		events.ExtensionsChanged(ctx, Scope{Kind: op.Scope, ProjectID: op.ProjectID, ProjectDir: op.ProjectDir})
	}
	return nil
}

// filesMatch reports whether both state files hold the staged replacements.
func filesMatch(op Operation) (bool, error) {
	desired, err := readCommitted(op.DesiredPath)
	if err != nil {
		return false, err
	}
	lock := []byte(nil)
	if strings.TrimSpace(op.LockPath) != "" {
		lock, err = readCommitted(op.LockPath)
		if err != nil {
			return false, err
		}
	}
	return bytes.Equal(desired, op.NextDesired) && bytes.Equal(lock, op.NextLock), nil
}

func readCommitted(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}
