package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/pkg/api"
)

type preparedTrack struct {
	TrackInput
	object *db.UpsertSourceBlobObjectParams
}

// TrackFile establishes or refreshes a file's tracked baseline.
func (s *Store) TrackFile(ctx context.Context, raw TrackInput) (TrackedFile, error) {
	if s == nil || s.sqlDB == nil {
		return TrackedFile{}, fmt.Errorf("ledger not configured")
	}
	in := normalizeTrackInput(raw)
	if in.ProjectID == "" || in.RootID == "" || in.Path == "" {
		return TrackedFile{}, fmt.Errorf("source tracking requires project, root, and path")
	}
	release := s.objects.AcquireReferenceLease()
	defer release()
	object, err := prepareContent(ctx, s.objects, in.SHA256, in.Content, in.EntryKind)
	if err != nil {
		return TrackedFile{}, err
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return TrackedFile{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	tracked, err := s.trackFileTx(ctx, q, preparedTrack{TrackInput: in, object: object})
	if err != nil {
		return TrackedFile{}, err
	}
	if err := tx.Commit(); err != nil {
		return TrackedFile{}, err
	}
	return tracked, nil
}

// LookupFile answers a file's recorded identity and, when the recorded head
// holds exactly this content, its version. It records nothing: a path the
// ledger has not recorded answers an empty identity. An untouched worker path
// carries its trunk identity.
func (s *Store) LookupFile(ctx context.Context, raw TrackInput) (TrackedFile, error) {
	in := normalizeTrackInput(raw)
	head, err := s.queries.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(), RootID: in.RootID, Path: in.Path,
	})
	if err == nil {
		return recordedVersion(head.FileID, head.VersionID, head.ContentSha256, in.SHA256), nil
	}
	if !errors.Is(err, sql.ErrNoRows) || !in.BranchID.IsWorker() {
		return TrackedFile{}, ignoreNoRows(err)
	}
	trunk, err := s.queries.GetTrunkSourceHeadByPath(ctx, db.GetTrunkSourceHeadByPathParams{
		ProjectID: in.ProjectID, RootID: in.RootID, Path: in.Path,
	})
	if err != nil {
		return TrackedFile{}, ignoreNoRows(err)
	}
	return recordedVersion(trunk.FileID, trunk.VersionID, trunk.ContentSha256, in.SHA256), nil
}

func recordedVersion(fileID, versionID, recordedSHA, readSHA string) TrackedFile {
	out := TrackedFile{FileID: fileID}
	if readSHA != "" && recordedSHA == readSHA {
		out.VersionID = versionID
	}
	return out
}

func ignoreNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func normalizeTrackInput(in TrackInput) TrackInput {
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.RootID = strings.TrimSpace(in.RootID)
	in.Path = filepath.ToSlash(strings.TrimSpace(in.Path))
	if in.EntryKind == "" {
		in.EntryKind = EntryKindFile
	}
	if in.TS.IsZero() {
		in.TS = time.Now().UTC()
	}
	if in.SHA256 == "" && in.Content != nil {
		in.SHA256 = sourceblob.ContentSHA(in.Content)
	}
	if in.Size == 0 && in.Content != nil {
		in.Size = int64(len(in.Content))
	}
	return in
}

func (s *Store) trackFileTx(ctx context.Context, q *db.Queries, in preparedTrack) (TrackedFile, error) {
	head, err := q.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(), RootID: in.RootID, Path: in.Path,
	})
	if err == nil {
		if in.SHA256 != "" && head.ContentSha256 != "" && head.ContentSha256 != in.SHA256 {
			if err := s.recordBatchTx(ctx, q, []preparedRecord{{RecordInput: normalizedInput(RecordInput{
				ProjectID: in.ProjectID, BranchID: in.BranchID,
				RootID: in.RootID, Path: in.Path, FileID: head.FileID, EntryKind: in.EntryKind,
				Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
				AfterSHA256: in.SHA256, After: in.Content, AfterSize: in.Size,
				Cause: "open_observation", CaptureQuality: "observed", TS: in.TS,
			}), objects: map[string]*db.UpsertSourceBlobObjectParams{in.SHA256: in.object}}}); err != nil {
				return TrackedFile{}, err
			}
			updated, err := q.GetSourceBranchHeadByFile(ctx, db.GetSourceBranchHeadByFileParams{
				ProjectID: in.ProjectID, BranchID: in.BranchID.String(), FileID: head.FileID,
			})
			if err != nil {
				return TrackedFile{}, err
			}
			return TrackedFile{FileID: updated.FileID, VersionID: updated.VersionID}, nil
		}
		if in.SHA256 == "" || head.ContentSha256 == in.SHA256 {
			return TrackedFile{FileID: head.FileID, VersionID: head.VersionID}, nil
		}
		// Enrich a metadata-only head without recording an effect.
		versionID, err := s.insertVersion(ctx, q, versionSpec{
			FileID: head.FileID, ProjectID: in.ProjectID, BranchID: in.BranchID,
			ParentVersionID: head.VersionID,
			RootID:          in.RootID, Path: in.Path, EntryKind: in.EntryKind,
			SHA256: in.SHA256, Object: in.object, Size: in.Size,
			CaptureQuality: "exact", TS: in.TS,
		})
		if err != nil {
			return TrackedFile{}, err
		}
		if err := q.UpsertSourceBranchHead(ctx, db.UpsertSourceBranchHeadParams{
			ProjectID: in.ProjectID, BranchID: in.BranchID.String(),
			FileID: head.FileID, VersionID: versionID, RootID: in.RootID, Path: in.Path,
			State: "content", ContentSha256: in.SHA256, Ordinal: head.Ordinal,
			ObservedTs: in.TS.Format(time.RFC3339Nano),
		}); err != nil {
			return TrackedFile{}, err
		}
		return TrackedFile{FileID: head.FileID, VersionID: versionID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TrackedFile{}, err
	}
	// Untouched worker paths inherit trunk file identity.
	fileID, derivedFromVersionID := "", ""
	if in.BranchID.IsWorker() {
		trunk, trunkErr := q.GetTrunkSourceHeadByPath(ctx, db.GetTrunkSourceHeadByPathParams{
			ProjectID: in.ProjectID, RootID: in.RootID, Path: in.Path,
		})
		if trunkErr == nil {
			fileID, derivedFromVersionID = trunk.FileID, trunk.VersionID
		} else if !errors.Is(trunkErr, sql.ErrNoRows) {
			return TrackedFile{}, trunkErr
		}
	}
	if fileID == "" {
		fileID = newID()
		if err := q.InsertSourceFile(ctx, db.InsertSourceFileParams{
			ID: fileID, ProjectID: in.ProjectID, EntryKind: in.EntryKind,
			CreatedTs: in.TS.Format(time.RFC3339Nano),
		}); err != nil {
			return TrackedFile{}, err
		}
	}
	state := "unresolved"
	if in.EntryKind == EntryKindDirectory {
		state = "directory"
	} else if in.SHA256 != "" {
		state = "content"
	}
	versionID, err := s.insertVersion(ctx, q, versionSpec{
		FileID: fileID, ProjectID: in.ProjectID, BranchID: in.BranchID,
		DerivedFromVersionID: derivedFromVersionID,
		RootID:               in.RootID, Path: in.Path,
		EntryKind: in.EntryKind, State: state, SHA256: in.SHA256, Object: in.object,
		Size: in.Size, CaptureQuality: "exact", TS: in.TS,
	})
	if err != nil {
		return TrackedFile{}, err
	}
	ordinal, err := q.LatestSourceOrdinal(ctx, in.ProjectID)
	if err != nil {
		return TrackedFile{}, err
	}
	if err := q.UpsertSourceBranchHead(ctx, db.UpsertSourceBranchHeadParams{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(),
		FileID: fileID, VersionID: versionID, RootID: in.RootID, Path: in.Path,
		State: state, ContentSha256: in.SHA256, Ordinal: ordinal,
		ObservedTs: in.TS.Format(time.RFC3339Nano),
	}); err != nil {
		return TrackedFile{}, err
	}
	return TrackedFile{FileID: fileID, VersionID: versionID}, nil
}
