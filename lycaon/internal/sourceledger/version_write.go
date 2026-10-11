package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
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

func (s *Store) insertVersion(ctx context.Context, q *db.Queries, spec versionSpec) (string, error) {
	state := spec.State
	if state == "" {
		if spec.EntryKind == EntryKindDirectory {
			state = "directory"
		} else if spec.SHA256 == "" {
			state = "unresolved"
		} else {
			state = "content"
		}
	}
	// Content state requires a content identity.
	if state == "content" && spec.SHA256 == "" {
		state = "unresolved"
	}
	captureState, captureReason := "not_applicable", ""
	switch state {
	case "content":
		captureState = "metadata_only"
		captureReason = "content_not_captured"
		if spec.Size > MaxRevisionContentBytes {
			captureReason = "content_too_large"
		}
		if spec.Object != nil {
			if err := q.UpsertSourceBlobObject(ctx, *spec.Object); err != nil {
				return "", err
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
	seq, err := q.AdvanceSourceOrdinal(ctx, spec.ProjectID)
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
