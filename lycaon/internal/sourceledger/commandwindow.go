package sourceledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// commandWindowSettle allows trailing watcher events.
const commandWindowSettle = 2 * time.Second

// CommandWindowInput opens one host-run command's observation window.
type CommandWindowInput struct {
	ProjectID   string
	Roots       []RootSpec
	SessionID   string
	Turn        int
	ToolCallID  string
	ToolName    string
	CommandLine string
}

type CommandWindow struct {
	ID            string
	ProjectID     string
	SessionID     string
	Turn          int
	ToolCallID    string
	ToolName      string
	CommandLine   string
	State         api.SourceCommandWindowState
	AdmissionMode string
	Ordinal       int64
	StartedTS     time.Time
	EndedTS       time.Time
}

type CommandWindowOpener interface {
	OpenCommandWindow(context.Context, CommandWindowInput) (*OpenCommandWindow, error)
}

type OpenCommandWindow struct {
	ID string

	store  *Commands
	window *openCommandWindow
	closed sync.Once
}

type openCommandWindow struct {
	id         string
	projectID  string
	roots      []RootSpec
	sessionID  string
	turn       int
	toolCallID string
	toolName   string
	startEpoch string
	// startSnapshotID anchors subsequent changes.
	startSnapshotID string
	// settled closes once the row is final.
	settled chan struct{}

	mu            sync.Mutex
	startChecked  bool
	startMissing  bool
	admissionMode string
	// Exited windows attribute trailing changes but yield to running commands.
	closing       bool
	passesAtClose uint64
}

// entryKey addresses one file across snapshot entries and branch heads.
func entryKey(rootID, path string) string {
	return rootID + "\x00" + path
}

// startSnapshot names the generation the window opened on. A start the
// store no longer holds excludes untracked files from admission.
func (w *openCommandWindow) startSnapshot(ctx context.Context, snapshots *sourcesnapshot.Store) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.startChecked {
		w.startChecked = true
		if _, err := snapshots.Get(ctx, w.startSnapshotID); err != nil {
			w.startMissing = true
			slog.WarnContext(ctx, "command window start snapshot unavailable; untracked files stay out of history",
				"window_id", w.id, "snapshot_id", w.startSnapshotID, "err", err)
		}
	}
	return w.startSnapshotID, !w.startMissing
}

// OpenCommandWindow reconciles the tree, remembers that inventory as the
// window's start, and records the window on the project's source clock.
func (s *Commands) OpenCommandWindow(ctx context.Context, in CommandWindowInput) (*OpenCommandWindow, error) {
	if s == nil || s.sqlDB == nil {
		return nil, fmt.Errorf("ledger not configured")
	}
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.ToolName = strings.TrimSpace(in.ToolName)
	if in.ProjectID == "" || in.ToolName == "" || len(in.Roots) == 0 {
		return nil, fmt.Errorf("command window requires project, tool name, and roots")
	}
	ctx, cancel := context.WithTimeout(ctx, s.observationBudget)
	defer cancel()
	s.recoverInterruptedWindows(ctx)
	req, err := s.windowInventoryRequest(ctx, in.ProjectID, in.Roots)
	if err != nil {
		return nil, err
	}
	// Reconcile first so drift from before the command is not attributed to
	// it; incomplete coverage cannot prove the tree unchanged, so it pays a pass.
	req.Force = !sourcesnapshot.ChangeTokenTrusted(snapshotRoots(in.Roots))
	req.Wait = true
	if err := s.inventory.EnsureInventory(ctx, req); err != nil {
		return nil, err
	}
	state, err := s.inventory.InventoryState(ctx, in.ProjectID, inventoryBranch(req.Roots), req.RootsGeneration)
	if err != nil {
		return nil, err
	}
	window := &openCommandWindow{
		id: newID(), projectID: in.ProjectID,
		roots: append([]RootSpec(nil), in.Roots...), sessionID: strings.TrimSpace(in.SessionID),
		turn: in.Turn, toolCallID: strings.TrimSpace(in.ToolCallID), toolName: in.ToolName,
		startEpoch:      sourcesnapshot.ChangeToken(snapshotRoots(in.Roots)),
		startSnapshotID: state.SnapshotID,
		settled:         make(chan struct{}),
	}
	if err := s.insertWindow(ctx, window, in.CommandLine); err != nil {
		return nil, err
	}
	if err := s.retainWindowStart(ctx, window); err != nil {
		return nil, err
	}
	s.windowsMu.Lock()
	s.openWindows[in.ProjectID] = append(s.openWindows[in.ProjectID], window)
	s.windowsMu.Unlock()
	return &OpenCommandWindow{ID: window.id, store: s, window: window}, nil
}

func (s *Commands) insertWindow(ctx context.Context, w *openCommandWindow, commandLine string) error {
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	ordinal, err := q.AdvanceSourceOrdinal(ctx, w.projectID)
	if err != nil {
		return err
	}
	if err := q.InsertSourceCommandWindow(ctx, db.InsertSourceCommandWindowParams{
		ID: w.id, ProjectID: w.projectID,
		SessionID: w.sessionID, Turn: int64(w.turn), ToolCallID: w.toolCallID,
		ToolName: w.toolName, CommandLine: commandLine, Ordinal: ordinal,
		StartedTs: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// Retain before-images unavailable from repository objects or history until the window settles.
const (
	windowRetainFiles = 8192
	windowRetainBytes = 256 << 20
)

// retainWindowStart keeps the before-images the window's admissions may
// need, bounded by windowRetainFiles and windowRetainBytes.
func (s *Commands) retainWindowStart(ctx context.Context, w *openCommandWindow) error {
	if w.startSnapshotID == "" || s.snapshots == nil {
		return nil
	}
	entries, err := s.snapshots.HashedEntries(ctx, w.startSnapshotID, MaxRevisionContentBytes, windowRetainFiles+1)
	if err != nil {
		return err
	}
	if len(entries) > windowRetainFiles {
		slog.InfoContext(ctx, "command window start retention bounded",
			"window_id", w.id, "unretained", len(entries)-windowRetainFiles, "limit", windowRetainFiles)
		entries = entries[:windowRetainFiles]
	}
	release := s.objects.AcquireReferenceLease()
	defer release()
	var retained int64
	pins := make([]string, 0, len(entries))
	for _, entry := range entries {
		if retained+entry.Size > windowRetainBytes {
			slog.InfoContext(ctx, "command window start retention bounded by size",
				"window_id", w.id, "retained_bytes", retained, "limit", windowRetainBytes)
			break
		}
		if _, err := s.queries.GetSourceBlobObject(ctx, entry.SHA256); err == nil {
			pins = append(pins, entry.SHA256)
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		raw, err := s.snapshots.Bytes(ctx, entry)
		if errors.Is(err, sourcesnapshot.ErrContentUnavailable) {
			// Already moved on; the pass that admits it records what it can.
			continue
		}
		if err != nil {
			return err
		}
		rel, stored, oids, err := s.objects.Put(entry.SHA256, raw)
		if err != nil {
			return err
		}
		if err := s.queries.UpsertSourceBlobObject(ctx, db.UpsertSourceBlobObjectParams{
			Sha256: entry.SHA256, Size: int64(len(raw)), StoredSize: stored, StorageRelpath: rel,
			GitOidSha1: oids.SHA1, GitOidSha256: oids.SHA256,
		}); err != nil {
			return err
		}
		retained += int64(len(raw))
		pins = append(pins, entry.SHA256)
	}
	if len(pins) == 0 {
		return nil
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	for _, sha := range pins {
		if err := q.InsertSourceCommandWindowObject(ctx, db.InsertSourceCommandWindowObjectParams{WindowID: w.id, Sha256: sha}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// windowInventoryRequest reads the project's current root generation so the
// window's passes join the same inventory lineage as the watcher's.
func (s *Commands) windowInventoryRequest(ctx context.Context, projectID string, roots []RootSpec) (InventoryRequest, error) {
	project, err := s.queries.GetProjectByID(ctx, projectID)
	if err != nil {
		return InventoryRequest{}, err
	}
	return InventoryRequest{
		ProjectID: projectID, RootsGeneration: int(project.RootsGeneration),
		Roots: append([]RootSpec(nil), roots...),
	}, nil
}

// Close starts settlement after process exit. Without complete watcher coverage,
// it runs the final pass synchronously; Settled signals completion.
func (o *OpenCommandWindow) Close(ctx context.Context) error {
	if o == nil || o.store == nil {
		return nil
	}
	var err error
	o.closed.Do(func() { err = o.store.closeCommandWindow(ctx, o.window) })
	return err
}

// Settled closes once the window's row is ended or removed.
func (o *OpenCommandWindow) Settled() <-chan struct{} {
	if o == nil || o.window == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return o.window.settled
}

func (s *Commands) closeCommandWindow(ctx context.Context, w *openCommandWindow) error {
	ended := time.Now().UTC()
	s.windowsMu.Lock()
	w.closing = true
	w.passesAtClose = s.inventoryPasses[w.projectID]
	s.windowsMu.Unlock()
	if !sourcesnapshot.ChangeTokenTrusted(snapshotRoots(w.roots)) {
		return s.settleCommandWindow(ctx, w, ended, true)
	}
	background := context.WithoutCancel(ctx)
	go func() {
		s.awaitWindowPass(background, w)
		if err := s.settleCommandWindow(background, w, ended, false); err != nil {
			slog.WarnContext(background, "command window settle", "window_id", w.id, "err", err)
		}
	}()
	return nil
}

// awaitWindowPass returns once an inventory pass has completed since the
// window closed, or the settle period elapsed.
func (s *Commands) awaitWindowPass(ctx context.Context, w *openCommandWindow) {
	deadline := time.NewTimer(s.windowSettle)
	defer deadline.Stop()
	for {
		s.windowsMu.Lock()
		passed := s.inventoryPasses[w.projectID] > w.passesAtClose
		wake := s.passWake
		s.windowsMu.Unlock()
		if passed {
			return
		}
		select {
		case <-wake:
		case <-deadline.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// settleCommandWindow runs the last pass when one is still owed, stops
// attributing, and makes the row final.
func (s *Commands) settleCommandWindow(ctx context.Context, w *openCommandWindow, ended time.Time, force bool) error {
	defer close(w.settled)
	roots := snapshotRoots(w.roots)
	s.windowsMu.Lock()
	passed := s.inventoryPasses[w.projectID] > w.passesAtClose
	s.windowsMu.Unlock()
	var passErr error
	if force || (!passed && sourcesnapshot.ChangeToken(roots) != w.startEpoch) {
		req, err := s.windowInventoryRequest(ctx, w.projectID, w.roots)
		if err != nil {
			passErr = err
		} else {
			req.Force, req.Wait = true, true
			passErr = s.inventory.EnsureInventory(ctx, req)
		}
	}
	// Pending observers commit their references before window collection.
	release, err := s.git.lockObservations(ctx, w.projectID, w.roots)
	if release != nil {
		defer release()
		// HEAD may move without invalidating any working-file snapshots.
		_, gitErr := s.git.observeGitState(ctx, w.projectID, w.roots)
		passErr = errors.Join(passErr, gitErr)
	}
	s.windowsMu.Lock()
	held := s.openWindows[w.projectID]
	for i, candidate := range held {
		if candidate == w {
			s.openWindows[w.projectID] = append(held[:i:i], held[i+1:]...)
			break
		}
	}
	if len(s.openWindows[w.projectID]) == 0 {
		delete(s.openWindows, w.projectID)
	}
	s.windowsMu.Unlock()
	if err != nil {
		// Stop future attribution, preserving the row for in-flight observers
		// and interrupted-window recovery.
		return errors.Join(passErr, err)
	}
	w.mu.Lock()
	admission := w.admissionMode
	w.mu.Unlock()
	if err := s.finishWindow(ctx, w.id, admission, ended); err != nil {
		return err
	}
	return passErr
}

// notePassCompleted counts the pass and wakes windows waiting on one.
func (s *Commands) notePassCompleted(projectID string) {
	s.windowsMu.Lock()
	s.inventoryPasses[projectID]++
	close(s.passWake)
	s.passWake = make(chan struct{})
	s.windowsMu.Unlock()
}

// finishWindow ends the row and keeps it while file or Git history names it.
func (s *Commands) finishWindow(ctx context.Context, id, admissionMode string, ended time.Time) error {
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if err := q.EndSourceCommandWindow(ctx, db.EndSourceCommandWindowParams{
		AdmissionMode: admissionMode, EndedTs: ended.Format(time.RFC3339Nano), ID: id,
	}); err != nil {
		return err
	}
	if err := q.DeleteSourceCommandWindowObjects(ctx, id); err != nil {
		return err
	}
	if _, err := q.DeleteSourceCommandWindowIfUnreferenced(ctx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// recoverInterruptedWindows settles rows an earlier process left running:
// memory is the only record of an open window, so all of them are interrupted.
func (s *Commands) recoverInterruptedWindows(ctx context.Context) {
	s.windowRecovery.Do(func() {
		s.recordMu.Lock()
		defer s.recordMu.Unlock()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.queries.InterruptRunningSourceCommandWindows(ctx, now); err != nil {
			slog.WarnContext(ctx, "command window recovery", "err", err)
			return
		}
		if _, err := s.queries.DeleteSettledSourceCommandWindowObjects(ctx); err != nil {
			slog.WarnContext(ctx, "command window recovery", "err", err)
		}
		if _, err := s.queries.DeleteUnreferencedEndedSourceCommandWindows(ctx); err != nil {
			slog.WarnContext(ctx, "command window recovery", "err", err)
		}
	})
}

// Prefer the newest running window, then the newest settling window,
// to minimize attribution of preceding changes.
func (s *Commands) attributionWindow(projectID string, roots []RootSpec) *openCommandWindow {
	s.windowsMu.Lock()
	defer s.windowsMu.Unlock()
	held := make([]*openCommandWindow, 0)
	for _, window := range s.openWindows[projectID] {
		if inventoryBranch(window.roots) == inventoryBranch(roots) {
			held = append(held, window)
		}
	}
	for i := len(held) - 1; i >= 0; i-- {
		if !held[i].closing {
			return held[i]
		}
	}
	if len(held) == 0 {
		return nil
	}
	return held[len(held)-1]
}

func commandWindowFromRow(row db.SourceCommandWindows) CommandWindow {
	started, _ := time.Parse(time.RFC3339Nano, row.StartedTs)
	ended, _ := time.Parse(time.RFC3339Nano, row.EndedTs)
	return CommandWindow{
		ID: row.ID, ProjectID: row.ProjectID,
		SessionID: row.SessionID, Turn: int(row.Turn), ToolCallID: row.ToolCallID,
		ToolName: row.ToolName, CommandLine: row.CommandLine,
		State: api.SourceCommandWindowState(row.State), AdmissionMode: row.AdmissionMode,
		Ordinal: row.Ordinal, StartedTS: started, EndedTS: ended,
	}
}

// commandWindowsFor resolves the windows a page of effects or versions names.
func (s *Commands) commandWindowsFor(ctx context.Context, ids []string) (map[string]CommandWindow, error) {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(unique)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSourceCommandWindowsByIDs(ctx, string(raw))
	if err != nil {
		return nil, err
	}
	out := make(map[string]CommandWindow, len(rows))
	for _, row := range rows {
		out[row.ID] = commandWindowFromRow(row)
	}
	return out, nil
}

// Commands attributes source observations to host command windows.
type Commands struct {
	inventoryPasses   map[string]uint64
	objects           *sourceblob.Store
	observationBudget time.Duration
	openWindows       map[string][]*openCommandWindow
	passWake          chan struct{}
	queries           *db.Queries
	recordMu          *sync.Mutex
	snapshots         *sourcesnapshot.Store
	sqlDB             db.Handle
	windowRecovery    sync.Once
	windowSettle      time.Duration
	windowsMu         sync.Mutex
	git               commandsGitPort
	inventory         commandsInventoryPort
}

type commandsInventoryPort interface {
	EnsureInventory(ctx context.Context, req InventoryRequest) error
	InventoryState(ctx context.Context, projectID string, branch sourcebranch.ID, rootsGeneration int) (InventoryState, error)
}

type commandsGitPort interface {
	lockObservations(ctx context.Context, projectID string, roots []RootSpec) (func(), error)
	observeGitState(ctx context.Context, projectID string, roots []RootSpec) (map[string]string, error)
}
