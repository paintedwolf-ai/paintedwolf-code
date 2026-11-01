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
	RootID         string                 `json:",omitempty"`
	Path           string                 `json:",omitempty"`
	FromRootID     string                 `json:",omitempty"`
	FromPath       string                 `json:",omitempty"`
	EntryKind      string                 `json:",omitempty"`
	NativeRecovery *NativeRecoveryBinding `json:",omitempty"`
}

// NativeRecoveryBinding binds an entry to a verified native recovery operation.
type NativeRecoveryBinding struct {
	Key     string
	Restore bool
}

func directoryPath(ctx context.Context, q *db.Queries, project, branch, root, rel string) (db.SourceDirectories, error) {
	return directoryPathWithCache(ctx, q, project, branch, root, rel, nil)
}

func directoryPathWithCache(ctx context.Context, q *db.Queries, project, branch, root, rel string, cache *TxBatchCache) (db.SourceDirectories, error) {
	if path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") || strings.ContainsRune(rel, 0) {
		return db.SourceDirectories{}, fmt.Errorf("invalid source directory %q", rel)
	}
	if cache != nil {
		if cached, ok := cache.directories[dirCacheKey{project: project, branch: branch, root: root, rel: rel}]; ok {
			return cached, nil
		}
	}
	var dir db.SourceDirectories
	var err error
	if cache != nil {
		if cachedRoot, ok := cache.directories[dirCacheKey{project: project, branch: branch, root: root, rel: "."}]; ok {
			dir = cachedRoot
		}
	}
	if dir.ID == "" {
		dir, err = q.GetSourceDirectoryRoot(ctx, db.GetSourceDirectoryRootParams{ProjectID: project, BranchID: branch, RootID: root})
		if errors.Is(err, sql.ErrNoRows) {
			dir = db.SourceDirectories{ID: newID(), ProjectID: project, BranchID: branch, RootID: root, Present: 1}
			err = insertDirectory(ctx, q, dir)
		}
		if err != nil {
			return dir, err
		}
		if cache != nil {
			cache.directories[dirCacheKey{project: project, branch: branch, root: root, rel: "."}] = dir
		}
	}
	if rel == "." {
		return dir, nil
	}
	curRel := ""
	for _, name := range strings.Split(rel, "/") {
		if curRel == "" {
			curRel = name
		} else {
			curRel = curRel + "/" + name
		}
		if cache != nil {
			if cachedChild, ok := cache.directories[dirCacheKey{project: project, branch: branch, root: root, rel: curRel}]; ok {
				dir = cachedChild
				continue
			}
		}
		child, lookupErr := q.GetSourceDirectoryChild(ctx, db.GetSourceDirectoryChildParams{ParentID: db.NullString(dir.ID), Name: name})
		if errors.Is(lookupErr, sql.ErrNoRows) {
			child = db.SourceDirectories{ID: newID(), ProjectID: project, BranchID: branch, RootID: root, ParentID: db.NullString(dir.ID), Name: name, Present: 1}
			lookupErr = insertDirectory(ctx, q, child)
			if lookupErr == nil && cache != nil {
				cache.hasChildSubdirs[dir.ID] = true
				cache.seenDirEntries[dirEntryKey{dirID: dir.ID, name: name}] = true
				cache.hasChildSubdirs[child.ID] = false
				cache.dirHasActiveHeads[child.ID] = false
				cache.loadedDirs[child.ID] = true
			}
		}
		if lookupErr != nil {
			return db.SourceDirectories{}, lookupErr
		}
		if cache != nil {
			cache.directories[dirCacheKey{project: project, branch: branch, root: root, rel: curRel}] = child
		}
		dir = child
	}
	return dir, nil
}

func insertDirectory(ctx context.Context, q *db.Queries, dir db.SourceDirectories) error {
	return q.InsertSourceDirectory(ctx, db.InsertSourceDirectoryParams{ID: dir.ID, ProjectID: dir.ProjectID, BranchID: dir.BranchID, RootID: dir.RootID, ParentID: dir.ParentID, Name: dir.Name})
}

func upsertSourceHead(ctx context.Context, q *db.Queries, head db.SourceBranchHeads) error {
	return upsertSourceHeadWithCache(ctx, q, head, nil)
}

func upsertSourceHeadWithCache(ctx context.Context, q *db.Queries, head db.SourceBranchHeads, cache *TxBatchCache) error {
	parent, err := directoryPathWithCache(ctx, q, head.ProjectID, head.BranchID, head.RootID, path.Dir(head.Path), cache)
	if err != nil {
		return err
	}
	name := path.Base(head.Path)
	if head.State != "absent" {
		needRetire := true
		if cache != nil && cache.tx != nil {
			if err := cache.ensureDirectoryEntriesLoaded(ctx, parent.ID); err != nil {
				return err
			}
			entryKey := dirEntryKey{dirID: parent.ID, name: name}
			if !cache.seenDirEntries[entryKey] {
				needRetire = false
			}
			cache.seenDirEntries[entryKey] = true
		}
		if needRetire {
			if err := q.RetireSourceHeadAtLocation(ctx, db.RetireSourceHeadAtLocationParams{DirectoryID: parent.ID, Name: name, FileID: head.FileID, Ordinal: head.Ordinal, ObservedTs: head.ObservedTs}); err != nil {
				return err
			}
		}
	}
	if head.State != "directory" && head.State != "absent" {
		checkOccupied := true
		if cache != nil {
			hasSubdirs, err := cache.directoryHasChildSubdirs(ctx, parent.ID)
			if err != nil {
				return err
			}
			if !hasSubdirs {
				checkOccupied = false
			}
		}
		if checkOccupied {
			occupied, err := q.GetSourceDirectoryChild(ctx, db.GetSourceDirectoryChildParams{ParentID: db.NullString(parent.ID), Name: name})
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
				if cache != nil {
					clear(cache.directories)
					clear(cache.loadedDirs)
				}
			}
		}
	}
	if head.State == "directory" {
		if _, err := directoryPathWithCache(ctx, q, head.ProjectID, head.BranchID, head.RootID, head.Path, cache); err != nil {
			return err
		}
	}
	return q.UpsertSourceHeadEntry(ctx, db.UpsertSourceHeadEntryParams{
		ProjectID: head.ProjectID, BranchID: head.BranchID, RootID: head.RootID, FileID: head.FileID, VersionID: head.VersionID,
		DirectoryID: parent.ID, Name: name, State: head.State, ContentSha256: head.ContentSha256, Ordinal: head.Ordinal, ObservedTs: head.ObservedTs,
	})
}

// A directory transition changes one edge; descendants retain their file and content identities.
func transitionDirectoryWithCache(ctx context.Context, q *db.Queries, in RecordInput, ordinal int64, cache *TxBatchCache) error {
	if in.EntryKind != EntryKindDirectory {
		return nil
	}
	recovery := in.NativeRecovery
	resetCache := func() {
		if cache != nil {
			clear(cache.directories)
			clear(cache.hasChildSubdirs)
			clear(cache.dirHasActiveHeads)
			clear(cache.loadedDirs)
			clear(cache.seenDirEntries)
		}
	}
	if in.Op != api.SourceChangeOpCreate || (recovery != nil && recovery.Restore) {
		resetCache()
	}
	rel := in.Path
	if in.Op == api.SourceChangeOpRename {
		rel = in.FromPath
	}
	if rel == "." {
		return fmt.Errorf("cannot transition the source root")
	}
	parent, err := directoryPathWithCache(ctx, q, in.ProjectID, in.BranchID.String(), in.RootID, path.Dir(in.Path), cache)
	if err != nil {
		return err
	}
	var dir db.SourceDirectories
	if recovery != nil && recovery.Restore && recovery.Key != "" {
		dir, err = q.GetSourceDirectoryRecovery(ctx, db.GetSourceDirectoryRecoveryParams{ProjectID: in.ProjectID, BranchID: in.BranchID.String(), RootID: in.RootID, RecoveryKey: recovery.Key})
	}
	if err != nil {
		return err
	}
	if dir.ID == "" && in.Op == api.SourceChangeOpCreate {
		baseName := path.Base(in.Path)
		checkOccupied := true
		if cache != nil {
			if hasSubdirs, subErr := cache.directoryHasChildSubdirs(ctx, parent.ID); subErr == nil && !hasSubdirs {
				checkOccupied = false
			}
		}
		if checkOccupied {
			occupied, lookupErr := q.GetSourceDirectoryChild(ctx, db.GetSourceDirectoryChildParams{ParentID: db.NullString(parent.ID), Name: baseName})
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				return lookupErr
			}
			if lookupErr == nil {
				resetCache()
				if err := setDirectoryState(ctx, q, occupied, occupied.ParentID, occupied.Name, 0, ordinal, in.TS, ""); err != nil {
					return err
				}
			}
		}
		dir = db.SourceDirectories{ID: newID(), ProjectID: in.ProjectID, BranchID: in.BranchID.String(), RootID: in.RootID, ParentID: db.NullString(parent.ID), Name: baseName, Present: 1}
		if err := insertDirectory(ctx, q, dir); err != nil {
			return err
		}
		if cache != nil {
			cache.directories[dirCacheKey{project: in.ProjectID, branch: in.BranchID.String(), root: in.RootID, rel: in.Path}] = dir
			cache.hasChildSubdirs[parent.ID] = true
			cache.seenDirEntries[dirEntryKey{dirID: parent.ID, name: baseName}] = true
			cache.hasChildSubdirs[dir.ID] = false
			cache.dirHasActiveHeads[dir.ID] = false
			cache.loadedDirs[dir.ID] = true
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
