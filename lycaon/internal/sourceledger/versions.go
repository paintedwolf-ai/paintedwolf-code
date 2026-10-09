package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
	"time"
)

// Version is one immutable state of a logical file.
type Version struct {
	Contributors                                             []Contributor
	ID, FileID, ProjectID, ParentVersionID                   string
	DerivedFromVersionID, OperationID, EffectID              string
	BranchID                                                 sourcebranch.ID
	RootID, Path, State, ContentSHA256                       string
	ByteSize                                                 int64
	CaptureState, CaptureReason, CaptureQuality              string
	Op                                                       api.SourceChangeOp
	Ordinal                                                  int64
	Origin                                                   api.SourceChangeOrigin
	Cause, ActorLabel, SessionID, JobID, ToolCallID, BatchID string
	// GitTransitionID links the state to its recorded ref movement.
	GitTransitionID string
	// CommandWindowID links the state to the command observation window the
	// recording pass ran under.
	CommandWindowID string
	// Landing says whether these bytes ever reached the working file.
	Landing   string
	Turn      int
	CreatedTS time.Time
}

type FileVersionsResult struct {
	FileID            string
	Versions          []Version
	NextBeforeOrdinal int64
	// GitTransitions resolves each Version.GitTransitionID on this page.
	GitTransitions map[string]GitTransition
	// CommandWindows resolves each Version.CommandWindowID on this page.
	CommandWindows map[string]CommandWindow
}

// BranchHead is the tracked current state of one logical file on a branch.
type BranchHead struct {
	FileID, VersionID, RootID, Path, State, SHA256 string
}

// ErrVersionUnavailable reports missing exact bytes for a retained state.
var ErrVersionUnavailable = errors.New("source version unavailable")

// RestorableVersion holds one immutable state and its exact on-disk bytes.
type RestorableVersion struct {
	ID, FileID, ProjectID, RootID, Path, State, SHA256 string
	Content                                            []byte
}

// Text decodes a retained text state.
func (v RestorableVersion) Text() (string, bool) {
	if v.State != "content" {
		return "", false
	}
	return decodeTextSnapshot(v.Content)
}

// ReadRestorableVersion resolves one retained state and its exact bytes.
func (s *History) ReadRestorableVersion(
	ctx context.Context,
	projectID, versionID string,
) (RestorableVersion, error) {
	if s == nil {
		return RestorableVersion{}, fmt.Errorf("ledger not configured")
	}
	row, err := s.queries.GetSourceVersion(ctx, versionID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.ProjectID != projectID) {
		return RestorableVersion{}, ErrHistoryNotFound
	}
	if err != nil {
		return RestorableVersion{}, err
	}
	out := RestorableVersion{
		ID: row.ID, FileID: row.FileID, ProjectID: row.ProjectID,
		RootID: row.RootID, Path: row.Path, State: row.State,
		SHA256: row.ContentSha256,
	}
	if row.State == "absent" {
		return out, nil
	}
	if row.State != "content" || row.CaptureState != "stored" || row.ContentSha256 == "" {
		return RestorableVersion{}, ErrVersionUnavailable
	}
	raw, found, err := s.retention.readVerifiedBlob(ctx, row.ContentSha256)
	if err != nil {
		return RestorableVersion{}, err
	}
	if !found {
		return RestorableVersion{}, ErrVersionUnavailable
	}
	out.Content = raw
	return out, nil
}

// VersionGitSource names a version's repository object.
type VersionGitSource struct {
	VersionID, FileID, RootID, Path, State, SHA256 string
	ByteSize                                       int64
	Commit                                         string
}

// ReadVersionGitSource resolves one retained state's repository source.
func (s *History) ReadVersionGitSource(
	ctx context.Context,
	projectID, versionID string,
) (VersionGitSource, error) {
	if s == nil {
		return VersionGitSource{}, fmt.Errorf("ledger not configured")
	}
	row, err := s.queries.GetSourceVersionGitSource(ctx, versionID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.ProjectID != projectID) {
		return VersionGitSource{}, ErrHistoryNotFound
	}
	if err != nil {
		return VersionGitSource{}, err
	}
	if row.ToCommit == "" {
		return VersionGitSource{}, ErrHistoryNotFound
	}
	return VersionGitSource{
		VersionID: row.ID, FileID: row.FileID, RootID: row.RootID,
		Path: row.Path, State: row.State, SHA256: row.ContentSha256,
		ByteSize: row.ByteSize, Commit: row.ToCommit,
	}, nil
}

// ResolveFile identifies the live logical file at a location on one branch.
func (s *History) ResolveFile(
	ctx context.Context,
	projectID string, branch sourcebranch.ID, rootID, path string,
) (fileID, versionID string, err error) {
	head, err := s.ResolveHead(ctx, projectID, branch, rootID, path)
	return head.FileID, head.VersionID, err
}

// ResolveHead identifies the tracked state at a location on one branch.
func (s *History) ResolveHead(
	ctx context.Context,
	projectID string, branch sourcebranch.ID, rootID, path string,
) (BranchHead, error) {
	if s == nil {
		return BranchHead{}, fmt.Errorf("ledger not configured")
	}
	head, err := s.queries.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{
		ProjectID: projectID, BranchID: branch.String(), RootID: rootID, Path: path,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return BranchHead{}, ErrHistoryNotFound
	}
	if err != nil {
		return BranchHead{}, err
	}
	return BranchHead{
		FileID: head.FileID, VersionID: head.VersionID, RootID: head.RootID,
		Path: head.Path, State: head.State, SHA256: head.ContentSha256,
	}, nil
}

// ResolveHeadByFile returns tombstones by logical file identity.
func (s *History) ResolveHeadByFile(
	ctx context.Context,
	projectID string, branch sourcebranch.ID, fileID string,
) (BranchHead, error) {
	if s == nil {
		return BranchHead{}, fmt.Errorf("ledger not configured")
	}
	head, err := s.queries.GetSourceBranchHeadByFile(ctx, db.GetSourceBranchHeadByFileParams{
		ProjectID: projectID, BranchID: branch.String(), FileID: fileID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return BranchHead{}, ErrHistoryNotFound
	}
	if err != nil {
		return BranchHead{}, err
	}
	return BranchHead{
		FileID: head.FileID, VersionID: head.VersionID, RootID: head.RootID,
		Path: head.Path, State: head.State, SHA256: head.ContentSha256,
	}, nil
}

// QueryFileVersions returns retained states across branches, newest first.
func (s *History) QueryFileVersions(
	ctx context.Context,
	projectID, fileID string,
	limit int,
	beforeOrdinal int64,
) (FileVersionsResult, error) {
	if s == nil {
		return FileVersionsResult{}, fmt.Errorf("ledger not configured")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if beforeOrdinal < 0 {
		beforeOrdinal = 0
	}
	rows, err := s.queries.ListSourceVersionsForFile(ctx, db.ListSourceVersionsForFileParams{
		ProjectID: projectID, FileID: fileID,
		BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit + 1),
	})
	if err != nil {
		return FileVersionsResult{}, err
	}
	out := FileVersionsResult{FileID: fileID, Versions: make([]Version, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextBeforeOrdinal = rows[len(rows)-1].Ordinal
	}
	for _, row := range rows {
		created, _ := time.Parse(time.RFC3339Nano, row.CreatedTs)
		out.Versions = append(out.Versions, Version{
			ID: row.VersionID, FileID: row.FileID, ProjectID: row.ProjectID,
			BranchID:        sourcebranch.ID(row.BranchID),
			ParentVersionID: row.ParentVersionID, DerivedFromVersionID: row.DerivedFromVersionID,
			OperationID: row.OperationID, EffectID: row.EffectID, RootID: row.RootID,
			Path: row.Path, State: row.State, ContentSHA256: row.ContentSha256,
			ByteSize: row.ByteSize, CaptureState: row.CaptureState,
			CaptureReason: row.CaptureReason, CaptureQuality: row.CaptureQuality,
			Op: api.SourceChangeOp(row.Op), Ordinal: row.Ordinal,
			Origin: api.SourceChangeOrigin(row.Origin), Cause: row.Cause,
			ActorLabel: row.ActorLabel, SessionID: row.SessionID, JobID: row.JobID,
			Turn: int(row.Turn), ToolCallID: row.ToolCallID, BatchID: row.BatchID,
			GitTransitionID: row.GitTransitionID, CommandWindowID: row.CommandWindowID,
			Landing: row.Landing, CreatedTS: created,
		})
	}
	if err := s.walk.hydrateVersionAuthors(ctx, projectID, out.Versions); err != nil {
		return FileVersionsResult{}, err
	}
	out.GitTransitions, err = s.gitTransitionsForVersions(ctx, out.Versions)
	if err != nil {
		return FileVersionsResult{}, err
	}
	windowIDs := make([]string, 0, 2)
	for _, version := range out.Versions {
		windowIDs = append(windowIDs, version.CommandWindowID)
	}
	out.CommandWindows, err = s.commands.commandWindowsFor(ctx, windowIDs)
	if err != nil {
		return FileVersionsResult{}, err
	}
	return out, nil
}

// gitTransitionsForVersions resolves transitions named by one page.
func (s *History) gitTransitionsForVersions(
	ctx context.Context,
	versions []Version,
) (map[string]GitTransition, error) {
	ids := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, version := range versions {
		id := version.GitTransitionID
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return s.git.GitTransitionsByIDs(ctx, ids)
}

// History reads retained file identities, states and provenance.
type History struct {
	queries     *db.Queries
	sqlDB       db.Handle
	commands    historyCommandsPort
	comparisons historyComparisonsPort
	git         historyGitPort
	retention   historyRetentionPort
	walk        historyWalkPort
}

type historyRetentionPort interface {
	readVerifiedBlob(ctx context.Context, sha256 string) ([]byte, bool, error)
}

type historyWalkPort interface {
	hydrateEffectAuthors(ctx context.Context, projectID string, effects []Effect) error
	hydrateVersionAuthors(ctx context.Context, projectID string, versions []Version) error
	queryEffects(
		ctx context.Context,
		projectID string,
		baseline Baseline,
		limit int,
		beforeOrdinal int64,
	) ([]Effect, error)
	walkCommandWindows(ctx context.Context, effects []Effect) ([]CommandWindow, error)
	walkFileStates(
		ctx context.Context,
		projectID string,
		rootBranches map[string]sourcebranch.ID,
		effects []Effect,
	) (map[string]walkFileState, error)
}

type historyCommandsPort interface {
	commandWindowsFor(ctx context.Context, ids []string) (map[string]CommandWindow, error)
}

type historyGitPort interface {
	GitTransitionsByIDs(ctx context.Context, ids []string) (map[string]GitTransition, error)
}

type historyComparisonsPort interface {
	comparisonSide(ctx context.Context, projectID, versionID string) (ComparisonSide, error)
	savedTextAttribution(ctx context.Context, projectID, versionID string, fallback AttributionResult) (AttributionResult, error)
}
