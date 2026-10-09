// Package sourceledger records file history and retained content.
package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/people/peoplestore"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// MaxRevisionContentBytes bounds content retained for one version.
	MaxRevisionContentBytes = sourceblob.MaxRevisionContentBytes
	CauseOverlayPromote     = "overlay_promote"
	CauseVersionRestore     = "version_restore"
	CauseVersionRestoreBase = "version_restore_base"
	CauseCommitRestore      = "git_commit_restore"
	// CauseFilesystemReconcile marks drift observed outside a command window.
	CauseFilesystemReconcile = "filesystem_reconcile"
	// CauseCommandWindow attributes observed drift to a command window.
	CauseCommandWindow = "command_window"
	// CauseGitOperation records paths changed by a managed Git invocation.
	CauseGitOperation = "git_operation"
	// CauseAgentEditHeld marks accepted agent text that has not reached disk.
	CauseAgentEditHeld = "agent_edit_held"
	// CaptureReconciled is the capture quality of every reconcile-pass record.
	CaptureReconciled = "reconciled"

	// Tracked links retain their target's bytes as files.
	EntryKindFile      = "file"
	EntryKindDirectory = "directory"

	// LandingWorkingFile marks a state the file itself held.
	LandingWorkingFile = "working_file"
	// LandingEditorDocument marks a retained, unsaved agent edit.
	LandingEditorDocument = "editor_document"
)

// Store records exact effects in the durable source history transaction.
type Store struct {
	sqlDB          db.Handle
	queries        *db.Queries
	people         *peoplestore.Store
	recordMu       sync.Mutex
	mutationScopes MutationScopeProvider
	Content        *sourceblob.Store
	Baselines      *workspacebaseline.Store
	Snapshots      *sourcesnapshot.Store
	Inventory      *Inventory
	Commands       *Commands
	Git            *Git
	History        *History
	Walk           *Walk
	Comparisons    *Comparisons
	Checkpoints    *Checkpoints
	Retention      *Retention
}

func New(sqlDB db.Handle, contentDir string) *Store {
	if sqlDB == nil {
		return nil
	}
	contentDir = filepath.Clean(contentDir)
	if contentDir == "." || contentDir == "" {
		contentDir, _ = os.MkdirTemp("", "painted-wolf-source-content-")
	}
	blobs := sourceblob.New(contentDir)
	queries := db.New(sqlDB)
	baselines := workspacebaseline.New(sqlDB, blobs, filepath.Join(filepath.Dir(contentDir), enginepaths.WorkerBaselinesDirName))
	snapshots := sourcesnapshot.New(sqlDB, blobs, filepath.Join(filepath.Dir(contentDir), enginepaths.SourceObservationsDBName), backgroundwork.Process())
	s := &Store{sqlDB: sqlDB, queries: queries, people: peoplestore.New(sqlDB), Content: blobs, Baselines: baselines, Snapshots: snapshots}
	s.Inventory = &Inventory{inventoryJobs: make(map[string]*inventoryJob), inventoryNow: time.Now, observationBudget: commandObservationBudget, queries: queries, recordMu: &s.recordMu, snapshots: snapshots, sqlDB: sqlDB, writer: s}
	s.Commands = &Commands{inventoryPasses: make(map[string]uint64), objects: blobs, observationBudget: commandObservationBudget, openWindows: make(map[string][]*openCommandWindow), passWake: make(chan struct{}), queries: queries, recordMu: &s.recordMu, snapshots: snapshots, sqlDB: sqlDB, windowSettle: commandWindowSettle}
	s.Git = &Git{queries: queries, recordMu: &s.recordMu, sqlDB: sqlDB}
	s.History = &History{queries: queries, sqlDB: sqlDB}
	s.Walk = &Walk{queries: queries, sqlDB: sqlDB}
	s.Comparisons = &Comparisons{queries: queries, recordMu: &s.recordMu, sqlDB: sqlDB}
	s.Checkpoints = &Checkpoints{queries: queries, recordMu: &s.recordMu, sqlDB: sqlDB}
	s.Retention = &Retention{baselines: baselines, objects: blobs, queries: queries, snapshots: snapshots, sqlDB: sqlDB}
	s.Inventory.commands = s.Commands
	s.Inventory.git = s.Git
	s.Inventory.retention = s.Retention
	s.Commands.git = s.Git
	s.Commands.inventory = s.Inventory
	s.Git.commands = s.Commands
	s.Git.inventory = s.Inventory
	s.History.commands = s.Commands
	s.History.comparisons = s.Comparisons
	s.History.git = s.Git
	s.History.retention = s.Retention
	s.History.walk = s.Walk
	s.Walk.commands = s.Commands
	s.Walk.git = s.Git
	s.Comparisons.history = s.History
	s.Comparisons.retention = s.Retention
	s.Comparisons.walk = s.Walk
	return s
}

// RecordInput is one exact consequence within a causal operation.
type RecordInput struct {
	RecordLocation
	TextBefore, TextAfter *TextState
	ProjectID             string
	// BranchID is the line of history this lands on; the zero value is the trunk.
	BranchID             sourcebranch.ID
	FileID               string
	DerivedFromVersionID string
	Op                   api.SourceChangeOp
	Origin               api.SourceChangeOrigin
	// An empty PersonID uses the acting person for user-origin operations.
	PersonID       string
	SessionID      string
	JobID          string
	Turn           int
	ToolCallID     string
	ToolName       string
	BatchID        string
	BeforeSHA256   string
	AfterSHA256    string
	Before         []byte
	After          []byte
	BeforeSize     int64
	AfterSize      int64
	OperationID    string
	Cause          string
	CaptureQuality string
	ActorLabel     string
	// Reconciliation binds these effects to the observed ref transition.
	GitTransitionID string
	// Reconciliation attributes effects to the active command window.
	CommandWindowID string
	TS              time.Time
}

type Recorder interface {
	Record(context.Context, RecordInput) error
	RecordTx(context.Context, *sql.Tx, RecordInput) error
}

// TrackInput identifies a file entering sparse history.
type TrackInput struct {
	ProjectID, RootID, Path string
	BranchID                sourcebranch.ID
	EntryKind               string
	SHA256                  string
	Content                 []byte
	Size                    int64
	TS                      time.Time
}

type TrackedFile struct {
	FileID, VersionID string
}

type FileTracker interface {
	TrackFile(context.Context, TrackInput) (TrackedFile, error)
}

type BatchRecorder interface {
	Recorder
	RecordBatch(context.Context, []RecordInput) error
	RecordBatchTx(context.Context, *sql.Tx, []RecordInput) error
}

type PromoteRecorder interface {
	BatchRecorder
}

type JobHistory interface {
	JobPathFirstWriteOrder(context.Context, string, string) ([]string, error)
	JobVersionForPath(context.Context, string, string, string, string) (string, string, error)
}

type JobVersionResolver interface {
	JobVersionForPath(context.Context, string, string, string, string) (string, string, error)
}

type AuthorshipReader interface {
	SessionAuthoredPaths(context.Context, string, string, string) ([]string, error)
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
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return TrackedFile{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	tracked, err := s.trackFileTx(ctx, q, in)
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

func (s *Store) trackFileTx(ctx context.Context, q *db.Queries, in TrackInput) (TrackedFile, error) {
	head, err := q.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(), RootID: in.RootID, Path: in.Path,
	})
	if err == nil {
		if in.SHA256 != "" && head.ContentSha256 != "" && head.ContentSha256 != in.SHA256 {
			if err := s.recordBatchTx(ctx, q, []RecordInput{{
				RecordLocation: RecordLocation{RootID: in.RootID, Path: in.Path, EntryKind: in.EntryKind},
				ProjectID:      in.ProjectID, BranchID: in.BranchID, FileID: head.FileID,
				Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
				AfterSHA256: in.SHA256, After: in.Content, AfterSize: in.Size,
				Cause: "open_observation", CaptureQuality: "observed", TS: in.TS}}); err != nil {
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
			SHA256: in.SHA256, Content: in.Content, Size: in.Size,
			CaptureQuality: "exact", TS: in.TS,
		})
		if err != nil {
			return TrackedFile{}, err
		}
		if err := upsertSourceHead(ctx, q, db.SourceBranchHeads{
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
		EntryKind: in.EntryKind, State: state, SHA256: in.SHA256, Content: in.Content,
		Size: in.Size, CaptureQuality: "exact", TS: in.TS,
	})
	if err != nil {
		return TrackedFile{}, err
	}
	ordinal, err := q.LatestSourceOrdinal(ctx, in.ProjectID)
	if err != nil {
		return TrackedFile{}, err
	}
	if err := upsertSourceHead(ctx, q, db.SourceBranchHeads{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(),
		FileID: fileID, VersionID: versionID, RootID: in.RootID, Path: in.Path,
		State: state, ContentSha256: in.SHA256, Ordinal: ordinal,
		ObservedTs: in.TS.Format(time.RFC3339Nano),
	}); err != nil {
		return TrackedFile{}, err
	}
	return TrackedFile{FileID: fileID, VersionID: versionID}, nil
}

func (s *Store) LedgerDB() db.Handle {
	if s == nil {
		return nil
	}
	return s.sqlDB
}

// Record persists a single-effect operation.
func nullableString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func newID() string {
	return uuid.NewString()
}
