package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
)

type versionSpec struct {
	FileID, ProjectID, ParentVersionID, DerivedFromVersionID string
	OperationID, RootID, Path, EntryKind, State, SHA256      string
	BranchID                                                 sourcebranch.ID
	Object                                                   *db.UpsertSourceBlobObjectParams
	Size                                                     int64
	CaptureQuality                                           string
	// Landing defaults to the working file.
	Landing string
	TS      time.Time
}

func entryState(kind, sha string) string {
	if kind == EntryKindDirectory {
		return "directory"
	}
	if sha == "" {
		return "unresolved"
	}
	if kind == EntryKindLink {
		return "link"
	}
	return "content"
}

func (s *Store) insertVersion(ctx context.Context, q *db.Queries, spec versionSpec) (string, error) {
	return s.insertVersionWithCache(ctx, q, spec, nil)
}

func (s *Store) insertVersionWithCache(ctx context.Context, q *db.Queries, spec versionSpec, cache *TxBatchCache) (string, error) {
	state := spec.State
	if state == "" {
		state = entryState(spec.EntryKind, spec.SHA256)
	}
	// Retained content and link text require a content identity.
	if (state == "content" || state == "link") && spec.SHA256 == "" {
		state = "unresolved"
	}
	captureState, captureReason := "not_applicable", ""
	switch state {
	case "content", "link":
		captureState = "metadata_only"
		captureReason = "content_not_captured"
		if spec.Size > MaxRevisionContentBytes {
			captureReason = "content_too_large"
		}
		if spec.Object != nil {
			if cache == nil || !cache.upsertedBlobs[spec.Object.Sha256] {
				if err := q.UpsertSourceBlobObject(ctx, *spec.Object); err != nil {
					return "", err
				}
				if cache != nil {
					cache.upsertedBlobs[spec.Object.Sha256] = true
				}
			}
			captureState, captureReason, spec.Size = "stored", "", spec.Object.Size
		} else if object, err := q.GetSourceBlobObject(ctx, spec.SHA256); err == nil {
			captureState, captureReason, spec.Size = "stored", "", object.Size
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	case "unresolved":
		captureState, captureReason = "metadata_only", "state_unresolved"
	}
	// Every retained state advances the source-history clock.
	seq, err := cache.advanceOrdinal(ctx, q, spec.ProjectID)
	if err != nil {
		return "", err
	}
	landing := spec.Landing
	if landing == "" {
		landing = LandingWorkingFile
	}
	id := newID()
	if err := q.InsertSourceVersion(ctx, db.InsertSourceVersionParams{
		Seq: seq,
		ID:  id, FileID: spec.FileID, ProjectID: spec.ProjectID,
		BranchID:             spec.BranchID.String(),
		ParentVersionID:      nullableString(spec.ParentVersionID),
		DerivedFromVersionID: nullableString(spec.DerivedFromVersionID),
		OperationID:          nullableString(spec.OperationID), RootID: spec.RootID, Path: spec.Path,
		State: state, ContentSha256: spec.SHA256, ByteSize: spec.Size,
		CaptureState: captureState, CaptureReason: captureReason,
		CaptureQuality: spec.CaptureQuality, CreatedTs: spec.TS.Format(time.RFC3339Nano),
		Landing: landing,
	}); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) preserveObservedPreimageWithCache(ctx context.Context, q *db.Queries, in preparedRecord, tracked TrackedFile, head *db.SourceBranchHeads, cache *TxBatchCache) (string, error) {
	// Preserve the observed pre-image when it differs from the tracked head.
	preSHA, preSize, preRoot, prePath := in.BeforeSHA256, in.BeforeSize, in.RootID, in.Path
	if in.Op == api.SourceChangeOpRename {
		preRoot, prePath = in.RootID, in.FromPath
		if preSHA == "" {
			preSHA, preSize = in.AfterSHA256, in.AfterSize
		}
	}
	needsPreimage := in.Op != api.SourceChangeOpCreate && tracked.VersionID == ""
	if head != nil && preSHA != "" && head.ContentSha256 != preSHA {
		needsPreimage = true
	}
	if head != nil && in.Op != api.SourceChangeOpCreate {
		previous, err := q.GetSourceVersion(ctx, tracked.VersionID)
		if err != nil {
			return "", err
		}
		if previous.RootID != head.RootID || previous.Path != head.Path || previous.State != head.State {
			needsPreimage = true
			if preSHA == "" && head.State != "absent" {
				preSHA, preSize = head.ContentSha256, previous.ByteSize
			}
		}
	}
	if needsPreimage {
		return s.insertVersionWithCache(ctx, q, versionSpec{
			FileID: tracked.FileID, ProjectID: in.ProjectID, BranchID: in.BranchID,
			ParentVersionID:      tracked.VersionID,
			DerivedFromVersionID: in.DerivedFromVersionID,
			RootID:               preRoot, Path: prePath, EntryKind: in.EntryKind,
			SHA256: preSHA, Object: in.objects[preSHA], Size: preSize,
			CaptureQuality: in.CaptureQuality, TS: in.TS,
		}, cache)
	}

	return tracked.VersionID, nil
}
