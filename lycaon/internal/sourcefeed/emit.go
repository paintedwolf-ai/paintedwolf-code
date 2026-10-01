// Package sourcefeed publishes durable source change events.
package sourcefeed

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceworkspace"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	selfWriteWindow    = time.Second
	maxChangesPerEvent = 512
)

// Change is one source mutation prepared for publication.
type Change struct {
	ProjectID     string
	WorkspaceID   string
	WorkspaceKind api.SourceWorkspaceKind
	RootID        string
	Path          string
	FromPath      string
	Op            api.SourceChangeOp
	Origin        api.SourceChangeOrigin
	IsDir         *bool
	SessionID     string
	JobID         string
	Turn          int
	ToolCallID    string
	AfterSHA256   string
	// AbsPath and FromAbsPath suppress watcher echoes.
	AbsPath     string
	FromAbsPath string
	TS          time.Time
}

// Sink delivers source events durably.
type Sink interface {
	SourceChanged(ctx context.Context, ev api.SourceChangesEvent) error
	SourceChangedTx(ctx context.Context, tx *sql.Tx, ev api.SourceChangesEvent) error
	// Deliver wakes committed transactional events.
	Deliver()
}

type sinkBinding struct {
	sink Sink
}

// StagedDelivery holds one transaction's source event.
type StagedDelivery struct {
	project, workspace string
	sink               Sink
	notice             Notice
	writePaths         []hostWriteTarget
	delivered          atomic.Bool
}

type preparedChanges struct {
	event      api.SourceChangesEvent
	writePaths []hostWriteTarget
}

var bound atomic.Pointer[sinkBinding]

// Bind installs the process-wide sink.
func Bind(s Sink) func() {
	if s == nil {
		panic("sourcefeed: bind nil sink")
	}
	binding := &sinkBinding{sink: s}
	bound.Store(binding)
	return func() {
		bound.CompareAndSwap(binding, nil)
	}
}

// Emit publishes one source change event in a transaction of its own.
func Emit(ctx context.Context, c Change) error {
	prepared, err := prepareChanges([]Change{c}, false)
	if err != nil {
		return err
	}
	binding := bound.Load()
	if binding == nil {
		notifySourceObservers(prepared.event.ProjectID, prepared.event.WorkspaceID, changeNotice(prepared.event))
		return nil
	}
	if err := binding.sink.SourceChanged(ctx, prepared.event); err != nil {
		return err
	}
	noteHostWritePaths(prepared.writePaths)
	notifySourceObservers(prepared.event.ProjectID, prepared.event.WorkspaceID, changeNotice(prepared.event))
	return nil
}

// EmitBatch publishes a converged set of changes as one bounded wire event.
func EmitBatch(ctx context.Context, changes []Change) error {
	return emitBatch(ctx, changes, false, false)
}

func emitBatch(ctx context.Context, changes []Change, resync, gitChanged bool) error {
	prepared, err := prepareChanges(changes, resync)
	if err != nil {
		return err
	}
	prepared.event.GitChanged = gitChanged
	if len(prepared.event.Changes) == 0 && !prepared.event.Resync && !gitChanged {
		return nil
	}
	binding := bound.Load()
	if binding == nil {
		notifySourceObservers(prepared.event.ProjectID, prepared.event.WorkspaceID, changeNotice(prepared.event))
		return nil
	}
	if err := binding.sink.SourceChanged(ctx, prepared.event); err != nil {
		return err
	}
	noteHostWritePaths(prepared.writePaths)
	notifySourceObservers(prepared.event.ProjectID, prepared.event.WorkspaceID, changeNotice(prepared.event))
	return nil
}

// EmitProjectSignal invalidates projections after ledger changes without a path batch.
func EmitProjectSignal(ctx context.Context, projectID string, roots []projectroot.RootRef) error {
	return emitProjectSignal(ctx, projectID, sourceworkspace.ID(projectID, roots), false, false)
}

// EmitGitSignal publishes a recorded ref movement without manufacturing file changes.
func EmitGitSignal(ctx context.Context, projectID string, roots []projectroot.RootRef) error {
	return emitProjectSignal(ctx, projectID, sourceworkspace.ID(projectID, roots), false, true)
}

func emitProjectSignal(ctx context.Context, projectID, workspaceID string, resync, gitChanged bool) error {
	projectID, workspaceID = strings.TrimSpace(projectID), strings.TrimSpace(workspaceID)
	if projectID == "" || workspaceID == "" {
		return fmt.Errorf("sourcefeed: project_id and workspace_id are required")
	}
	binding := bound.Load()
	if binding != nil {
		if err := binding.sink.SourceChanged(ctx, api.SourceChangesEvent{
			ProjectID: projectID, WorkspaceID: workspaceID, WorkspaceKind: api.SourceWorkspaceKindProject,
			Changes: []api.SourceChange{}, Resync: resync, GitChanged: gitChanged,
		}); err != nil {
			return err
		}
	}
	notifySourceObservers(projectID, workspaceID, Notice{})
	return nil
}

// EmitTx stages one source event in the caller's transaction.
func EmitTx(ctx context.Context, tx *sql.Tx, c Change) (*StagedDelivery, error) {
	return emitBatchTx(ctx, tx, []Change{c})
}

// EmitBatchTx stages one bounded batch in the caller's transaction.
func EmitBatchTx(ctx context.Context, tx *sql.Tx, changes []Change) (*StagedDelivery, error) {
	return emitBatchTx(ctx, tx, changes)
}

func emitBatchTx(ctx context.Context, tx *sql.Tx, changes []Change) (*StagedDelivery, error) {
	prepared, err := prepareChanges(changes, false)
	if err != nil {
		return nil, err
	}
	if len(prepared.event.Changes) == 0 {
		return nil, nil
	}
	delivery := &StagedDelivery{project: prepared.event.ProjectID, workspace: prepared.event.WorkspaceID, writePaths: prepared.writePaths, notice: changeNotice(prepared.event)}
	binding := bound.Load()
	if binding != nil {
		if err := binding.sink.SourceChangedTx(ctx, tx, prepared.event); err != nil {
			return nil, err
		}
		delivery.sink = binding.sink
	}
	return delivery, nil
}

// DeliverCommitted records and wakes the committed events once.
func (d *StagedDelivery) DeliverCommitted() {
	if d == nil || !d.delivered.CompareAndSwap(false, true) {
		return
	}
	noteHostWritePaths(d.writePaths)
	if d.sink != nil {
		d.sink.Deliver()
	}
	notifySourceObservers(d.project, d.workspace, d.notice)
}

func (c Change) validate() error {
	if strings.TrimSpace(c.RootID) == "" || strings.TrimSpace(c.Path) == "" {
		return fmt.Errorf("sourcefeed: root_id and path are required")
	}
	switch c.Op {
	case api.SourceChangeOpWrite, api.SourceChangeOpCreate, api.SourceChangeOpDelete:
	case api.SourceChangeOpRename:
		if strings.TrimSpace(c.FromPath) == "" {
			return fmt.Errorf("sourcefeed: from_path is required for rename")
		}
	default:
		return fmt.Errorf("sourcefeed: invalid op %q", c.Op)
	}
	switch c.Origin {
	case api.SourceChangeOriginAgent, api.SourceChangeOriginUser, api.SourceChangeOriginExternal:
	default:
		return fmt.Errorf("sourcefeed: invalid origin %q", c.Origin)
	}
	return nil
}

func (c Change) event() api.SourceChange {
	ts := c.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	isDir := c.IsDir
	if isDir == nil && c.Op != api.SourceChangeOpDelete && strings.TrimSpace(c.AbsPath) != "" {
		if info, err := os.Lstat(c.AbsPath); err == nil {
			value := info.IsDir()
			isDir = &value
		}
	}
	return api.SourceChange{
		RootID:      strings.TrimSpace(c.RootID),
		Path:        filepath.ToSlash(strings.TrimSpace(c.Path)),
		FromPath:    filepath.ToSlash(strings.TrimSpace(c.FromPath)),
		Op:          c.Op,
		Origin:      c.Origin,
		IsDir:       isDir,
		SessionID:   strings.TrimSpace(c.SessionID),
		WorkerID:    strings.TrimSpace(c.JobID),
		Turn:        c.Turn,
		ToolCallID:  strings.TrimSpace(c.ToolCallID),
		AfterSHA256: strings.TrimSpace(c.AfterSHA256),
		ChangedAt:   ts,
	}
}

func prepareChanges(changes []Change, resync bool) (preparedChanges, error) {
	if len(changes) == 0 {
		return preparedChanges{}, nil
	}
	projectID := strings.TrimSpace(changes[0].ProjectID)
	workspaceID := strings.TrimSpace(changes[0].WorkspaceID)
	workspaceKind := changes[0].WorkspaceKind
	if projectID == "" || workspaceID == "" {
		return preparedChanges{}, fmt.Errorf("sourcefeed: project_id and workspace_id are required")
	}
	if !validWorkspaceKind(workspaceKind) {
		return preparedChanges{}, fmt.Errorf("sourcefeed: invalid workspace_kind %q", workspaceKind)
	}
	limit := min(len(changes), maxChangesPerEvent)
	var writePaths []hostWriteTarget
	for _, change := range changes {
		if strings.TrimSpace(change.ProjectID) != projectID ||
			strings.TrimSpace(change.WorkspaceID) != workspaceID || change.WorkspaceKind != workspaceKind {
			return preparedChanges{}, fmt.Errorf("sourcefeed: batch spans source workspaces")
		}
		if err := change.validate(); err != nil {
			return preparedChanges{}, err
		}
		if change.Origin == api.SourceChangeOriginExternal {
			continue
		}
		// The watcher reconciles rename echoes without foreground content hashing.
		if change.Op == api.SourceChangeOpRename && change.AfterSHA256 == "" {
			continue
		}
		if change.AbsPath == "" && change.FromAbsPath == "" {
			continue
		}
		if writePaths == nil {
			writePaths = make([]hostWriteTarget, 0, len(changes))
		}
		if change.AbsPath != "" {
			writePaths = append(writePaths, hostWriteTarget{path: change.AbsPath, digest: change.AfterSHA256})
		}
		if change.FromAbsPath != "" {
			writePaths = append(writePaths, hostWriteTarget{path: change.FromAbsPath})
		}
	}
	out := api.SourceChangesEvent{
		ProjectID: projectID, WorkspaceID: workspaceID, WorkspaceKind: workspaceKind,
		Changes: make([]api.SourceChange, 0, limit), Resync: resync || len(changes) > limit,
	}
	for _, change := range changes[:limit] {
		out.Changes = append(out.Changes, change.event())
	}
	return preparedChanges{event: out, writePaths: writePaths}, nil
}

func validWorkspaceKind(kind api.SourceWorkspaceKind) bool {
	switch kind {
	case api.SourceWorkspaceKindProject, api.SourceWorkspaceKindWorker:
		return true
	default:
		return false
	}
}
