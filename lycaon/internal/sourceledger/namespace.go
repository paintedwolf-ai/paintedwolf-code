package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// RecordLocation identifies the entry changed by an operation.
type RecordLocation struct {
	RootID         string
	Path           string
	FromRootID     string
	FromPath       string
	EntryKind      string
	NativeRecovery *NativeRecoveryBinding `json:",omitempty"`
}

// NativeRecoveryBinding binds an entry to a verified native recovery operation.
type NativeRecoveryBinding struct {
	Key     string
	Restore bool
}

func directoryPath(ctx context.Context, q *db.Queries, project, branch, root, rel string) (db.SourceDirectories, error) {
	if path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") || strings.ContainsRune(rel, 0) {
		return db.SourceDirectories{}, fmt.Errorf("invalid source directory %q", rel)
	}
	dir, err := q.GetSourceDirectoryRoot(ctx, db.GetSourceDirectoryRootParams{ProjectID: project, BranchID: branch, RootID: root})
	if errors.Is(err, sql.ErrNoRows) {
		dir = db.SourceDirectories{ID: newID(), ProjectID: project, BranchID: branch, RootID: root, Present: 1}
		err = insertDirectory(ctx, q, dir)
	}
	if err != nil || rel == "." {
		return dir, err
	}
	for _, name := range strings.Split(rel, "/") {
		child, lookupErr := q.GetSourceDirectoryChild(ctx, db.GetSourceDirectoryChildParams{ParentID: db.NullString(dir.ID), Name: name})
		if errors.Is(lookupErr, sql.ErrNoRows) {
			child = db.SourceDirectories{ID: newID(), ProjectID: project, BranchID: branch, RootID: root, ParentID: db.NullString(dir.ID), Name: name, Present: 1}
			lookupErr = insertDirectory(ctx, q, child)
		}
		if lookupErr != nil {
			return db.SourceDirectories{}, lookupErr
		}
		dir = child
	}
	return dir, nil
}

func insertDirectory(ctx context.Context, q *db.Queries, dir db.SourceDirectories) error {
	return q.InsertSourceDirectory(ctx, db.InsertSourceDirectoryParams{ID: dir.ID, ProjectID: dir.ProjectID, BranchID: dir.BranchID, RootID: dir.RootID, ParentID: dir.ParentID, Name: dir.Name})
}

func upsertSourceHead(ctx context.Context, q *db.Queries, head db.SourceBranchHeads) error {
	parent, err := directoryPath(ctx, q, head.ProjectID, head.BranchID, head.RootID, path.Dir(head.Path))
	if err != nil {
		return err
	}
	if head.State != "absent" {
		if err := q.RetireSourceHeadAtLocation(ctx, db.RetireSourceHeadAtLocationParams{DirectoryID: parent.ID, Name: path.Base(head.Path), FileID: head.FileID, Ordinal: head.Ordinal, ObservedTs: head.ObservedTs}); err != nil {
			return err
		}
	}
	if head.State != "directory" && head.State != "absent" {
		occupied, err := q.GetSourceDirectoryChild(ctx, db.GetSourceDirectoryChildParams{ParentID: db.NullString(parent.ID), Name: path.Base(head.Path)})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			ts, err := time.Parse(time.RFC3339Nano, head.ObservedTs)
			if err != nil {
				return err
			}
			if err := setDirectoryState(ctx, q, occupied, occupied.ParentID, occupied.Name, 0, head.Ordinal, ts, ""); err != nil {
				return err
			}
		}
	}
	if head.State == "directory" {
		if _, err := directoryPath(ctx, q, head.ProjectID, head.BranchID, head.RootID, head.Path); err != nil {
			return err
		}
	}
	return q.UpsertSourceHeadEntry(ctx, db.UpsertSourceHeadEntryParams{
		ProjectID: head.ProjectID, BranchID: head.BranchID, RootID: head.RootID, FileID: head.FileID, VersionID: head.VersionID,
		DirectoryID: parent.ID, Name: path.Base(head.Path), State: head.State, ContentSha256: head.ContentSha256, Ordinal: head.Ordinal, ObservedTs: head.ObservedTs,
	})
}

// A directory transition changes one edge; descendants retain their file and content identities.
func transitionDirectory(ctx context.Context, q *db.Queries, in RecordInput, ordinal int64) error {
	if in.EntryKind != EntryKindDirectory {
		return nil
	}
	rel := in.Path
	if in.Op == api.SourceChangeOpRename {
		rel = in.FromPath
	}
	if rel == "." {
		return fmt.Errorf("cannot transition the source root")
	}
	parent, err := directoryPath(ctx, q, in.ProjectID, in.BranchID.String(), in.RootID, path.Dir(in.Path))
	if err != nil {
		return err
	}
	var dir db.SourceDirectories
	recovery := in.NativeRecovery
	if recovery != nil && recovery.Restore && recovery.Key != "" {
		dir, err = q.GetSourceDirectoryRecovery(ctx, db.GetSourceDirectoryRecoveryParams{ProjectID: in.ProjectID, BranchID: in.BranchID.String(), RootID: in.RootID, RecoveryKey: recovery.Key})
	}
	if err != nil {
		return err
	}
	if dir.ID == "" && in.Op == api.SourceChangeOpCreate {
		occupied, lookupErr := q.GetSourceDirectoryChild(ctx, db.GetSourceDirectoryChildParams{ParentID: db.NullString(parent.ID), Name: path.Base(in.Path)})
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}
		if lookupErr == nil {
			if err := setDirectoryState(ctx, q, occupied, occupied.ParentID, occupied.Name, 0, ordinal, in.TS, ""); err != nil {
				return err
			}
		}
		dir = db.SourceDirectories{ID: newID(), ProjectID: in.ProjectID, BranchID: in.BranchID.String(), RootID: in.RootID, ParentID: db.NullString(parent.ID), Name: path.Base(in.Path), Present: 1}
		if err := insertDirectory(ctx, q, dir); err != nil {
			return err
		}
	} else if dir.ID == "" {
		dir, err = directoryPath(ctx, q, in.ProjectID, in.BranchID.String(), in.RootID, rel)
		if err != nil {
			return err
		}
	}
	if in.Op == api.SourceChangeOpRename || (recovery != nil && recovery.Restore) {
		if err := requireNonDescendant(ctx, q, parent, dir.ID); err != nil {
			return err
		}
		occupied, lookupErr := q.GetSourceDirectoryChild(ctx, db.GetSourceDirectoryChildParams{ParentID: db.NullString(parent.ID), Name: path.Base(in.Path)})
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}
		if lookupErr == nil && occupied.ID != dir.ID {
			// Filesystem publication proved the target was free; old observations are tombstoned.
			if err := setDirectoryState(ctx, q, occupied, occupied.ParentID, occupied.Name, 0, ordinal, in.TS, ""); err != nil {
				return err
			}
		}
	}
	present := int64(1)
	identity := ""
	if in.Op == api.SourceChangeOpDelete {
		present = 0
	}
	if recovery != nil {
		identity = recovery.Key
	}
	return setDirectoryState(ctx, q, dir, db.NullString(parent.ID), path.Base(in.Path), present, ordinal, in.TS, identity)
}

func requireNonDescendant(ctx context.Context, q *db.Queries, parent db.SourceDirectories, movedID string) error {
	for {
		if parent.ID == movedID {
			return fmt.Errorf("directory cannot become its own descendant")
		}
		if !parent.ParentID.Valid {
			return nil
		}
		var err error
		parent, err = q.GetSourceDirectory(ctx, parent.ParentID.String)
		if err != nil {
			return err
		}
	}
}

func setDirectoryState(ctx context.Context, q *db.Queries, dir db.SourceDirectories, parent sql.NullString, name string, present, ordinal int64, ts time.Time, identity string) error {
	return q.TransitionSourceDirectory(ctx, db.TransitionSourceDirectoryParams{ID: dir.ID, ParentID: parent, Name: name, Present: present, Ordinal: ordinal, ObservedTs: ts.Format(time.RFC3339Nano), RecoveryKey: identity})
}
