package sourceledger

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
)

// RecoveryEntry is one ordered entry in a complete filesystem snapshot.
type RecoveryEntry struct {
	Path string
	Mode uint32
	SHA  string
	Link string
}

const recoveryPageSize = 256

// RecoveryWriter bounds memory and keeps compression outside database transactions.
type RecoveryWriter struct {
	store                 *Retention
	projectID, recoveryID string
	entries               []RecoveryEntry
	objects               []sourceblob.Capture
	ordinal               int64
	release               func()
}

func (s *Retention) BeginRecovery(ctx context.Context, projectID, recoveryID string) (*RecoveryWriter, error) {
	if _, err := s.sqlDB.ExecContext(ctx, `DELETE FROM source_recovery_entries WHERE project_id=? AND recovery_id=?`, projectID, recoveryID); err != nil {
		return nil, err
	}
	return &RecoveryWriter{store: s, projectID: projectID, recoveryID: recoveryID, release: s.objects.AcquireReferenceLease()}, nil
}

func (w *RecoveryWriter) Close() {
	if w.release != nil {
		w.release()
		w.release = nil
	}
}

// Append records an entry and optionally copies its bytes into a private destination.
func (w *RecoveryWriter) Append(ctx context.Context, entry RecoveryEntry, root *os.Root, path string, destination io.Writer) (RecoveryEntry, error) {
	if os.FileMode(entry.Mode).IsRegular() {
		object, err := w.store.objects.CopyRootFile(ctx, root, path, destination)
		if err != nil {
			return entry, err
		}
		if object.Mode != entry.Mode {
			return entry, fmt.Errorf("%w: entry mode changed", sourceblob.ErrFileChanged)
		}
		entry.SHA = object.SHA256
		w.objects = append(w.objects, object)
	}
	w.entries = append(w.entries, entry)
	if len(w.entries) >= recoveryPageSize {
		return entry, w.Flush(ctx)
	}
	return entry, nil
}

func (w *RecoveryWriter) Flush(ctx context.Context) error {
	if len(w.entries) == 0 {
		return ctx.Err()
	}
	tx, err := w.store.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	for _, object := range w.objects {
		if err := queries.UpsertSourceBlobObject(ctx, db.UpsertSourceBlobObjectParams{Sha256: object.SHA256, Size: object.Size, StoredSize: object.Stored, StorageRelpath: object.Rel, GitOidSha1: object.GitOIDs.SHA1, GitOidSha256: object.GitOIDs.SHA256}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO source_recovery_objects(project_id,recovery_id,sha256) VALUES(?,?,?)`, w.projectID, w.recoveryID, object.SHA256); err != nil {
			return err
		}
	}
	for i, entry := range w.entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO source_recovery_entries(project_id,recovery_id,ordinal,path,mode,sha256,link) VALUES(?,?,?,?,?,?,?)`, w.projectID, w.recoveryID, w.ordinal+int64(i), entry.Path, entry.Mode, entry.SHA, entry.Link); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	w.ordinal += int64(len(w.entries))
	w.entries = w.entries[:0]
	w.objects = w.objects[:0]
	return nil
}

// WalkRecovery releases each read cursor before invoking callbacks that may write.
func (s *Retention) WalkRecovery(ctx context.Context, projectID, recoveryID string, count int64, reverse bool, visit func(RecoveryEntry) error) error {
	for offset := int64(0); offset < count; offset += recoveryPageSize {
		start, end := offset, min(offset+recoveryPageSize, count)
		order := "ASC"
		if reverse {
			start, end = count-end, count-start
			order = "DESC"
		}
		entries, err := s.readRecoveryPage(ctx, projectID, recoveryID, start, end, order)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(entry); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Retention) readRecoveryPage(ctx context.Context, projectID, recoveryID string, start, end int64, order string) ([]RecoveryEntry, error) {
	rows, err := s.sqlDB.QueryContext(ctx, `SELECT path,mode,sha256,link FROM source_recovery_entries WHERE project_id=? AND recovery_id=? AND ordinal>=? AND ordinal<? ORDER BY ordinal `+order, projectID, recoveryID, start, end)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := make([]RecoveryEntry, 0, end-start)
	for rows.Next() {
		var entry RecoveryEntry
		if err := rows.Scan(&entry.Path, &entry.Mode, &entry.SHA, &entry.Link); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if int64(len(entries)) != end-start {
		return nil, os.ErrNotExist
	}
	return entries, nil
}
