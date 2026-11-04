package sourceledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
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
	opener sync.Once
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
	// startSnapshotID anchors subsequent changes. It moves when the window
	// re-anchors and when a pass settles drift the window did not claim.
	startSnapshotID string
	// settled closes once settlement has finished, successfully or not.
	settled chan struct{}

	mu            sync.Mutex
	startChecked  bool
	startMissing  bool
	admissionMode string

	// The fields below are guarded by Commands.windowsMu.

	// Exited windows attribute trailing changes but yield to running commands.
	closing bool
	// active is whether the window may claim drift: a hold is live, or the last
	// one ended and its settle grace has not finished.
	active bool
	holds  int
	// waiters are holds waiting for the window to re-anchor.
	waiters int
	// anchoring is non-nil while the window re-anchors; anchorErr is its result.
	anchoring chan struct{}
	anchorErr error
	// quiet is non-nil during a settle grace, and closes when a hold or the
	// close ends that grace early.
	quiet         chan struct{}
	passesAtQuiet uint64
	// incomplete means a final pass failed. The window claims nothing, and the
	// next completed pass records the same drift as outside changes.
	incomplete bool
	finalized  bool
	// rowOwed means settlement could not end the row.
	rowOwed bool
	endedAt time.Time
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
		w.startMissing = false
		if _, err := snapshots.Get(ctx, w.startSnapshotID); err != nil {
			w.startMissing = true
			slog.WarnContext(ctx, "command window start snapshot unavailable; untracked files stay out of history",
				"window_id", w.id, "snapshot_id", w.startSnapshotID, "err", err)
		}
	}
	return w.startSnapshotID, !w.startMissing
}

// moveStart re-bases the window, so later admissions begin at snapshotID.
func (w *openCommandWindow) moveStart(snapshotID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.startSnapshotID != snapshotID {
		w.startSnapshotID, w.startChecked = snapshotID, false
	}
}

func (w *openCommandWindow) currentStart() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.startSnapshotID
}

// OpenCommandWindow reconciles the tree, remembers that inventory as the
// window's start, and records the window on the project's source clock. It
// opens active, held by its opener until ReleaseOpener or Close.
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
	// Recovery interrupts every running row, so no row may be written before it
	// has succeeded: a later retry would end a live window.
	if err := s.RecoverInterruptedWindows(ctx); err != nil {
		return nil, err
	}
	snapshotID, epoch, err := s.anchorStart(ctx, in.ProjectID, in.Roots)
	if err != nil {
		return nil, err
	}
	window := &openCommandWindow{
		id: newID(), projectID: in.ProjectID,
		roots: append([]RootSpec(nil), in.Roots...), sessionID: strings.TrimSpace(in.SessionID),
		turn: in.Turn, toolCallID: strings.TrimSpace(in.ToolCallID), toolName: in.ToolName,
		startEpoch:      epoch,
		startSnapshotID: snapshotID,
		settled:         make(chan struct{}),
		active:          true,
		holds:           1,
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

// anchorStart reconciles the tree so drift from before this point is not
// attributed, and names the inventory that results; incomplete coverage cannot
// prove the tree unchanged, so it pays a pass.
func (s *Commands) anchorStart(ctx context.Context, projectID string, roots []RootSpec) (snapshotID, epoch string, err error) {
	req, err := s.windowInventoryRequest(ctx, projectID, roots)
	if err != nil {
		return "", "", err
	}
	req.Force = !sourcesnapshot.ChangeTokenTrusted(snapshotRoots(roots))
	req.Wait = true
	if err := s.awaitStartInventory(ctx, req); err != nil {
		return "", "", err
	}
	state, err := s.inventory.InventoryState(ctx, projectID, inventoryBranch(req.Roots), req.RootsGeneration)
	if err != nil {
		return "", "", err
	}
	return state.SnapshotID, sourcesnapshot.ChangeToken(snapshotRoots(roots)), nil
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

// ErrWindowStartPending refuses a window while a pass that outlasted an earlier
// open is still running: its start inventory is unknown, and waiting again would
// charge another launch the whole budget.
var ErrWindowStartPending = errors.New("command window start inventory is still running")

// ErrWindowClosed refuses a hold on a window whose program has settled.
var ErrWindowClosed = errors.New("command window is closed")

// slowPassReuseFloor keeps a slow pass usable across a model turn; bounding reuse
// by the pass alone would leave a pass just over budget unused at agent pace.
const slowPassReuseFloor = 30 * time.Second

// slowPass is the last pass of a scope that outlasted the observation budget.
type slowPass struct {
	generation int
	finished   time.Time
	took       time.Duration
}

// noteSlowPass remembers a completed pass that took longer than an open can wait.
// Caller holds windowsMu.
func (s *Commands) noteSlowPass(req InventoryRequest, started time.Time) {
	finished := s.now()
	s.slowPasses[req.scopeKey()] = slowPass{generation: req.RootsGeneration, finished: finished, took: finished.Sub(started)}
}

// awaitStartInventory waits for the window's start inventory within ctx. The pass
// is detached, so one that outlasts the budget finishes and a later open finds it
// done; thrown away, a pass longer than the budget would never converge. Forcing
// another after it would not converge either, so an open reuses a slow pass that
// just finished: drift since then may reach the window, bounded by one pass.
func (s *Commands) awaitStartInventory(ctx context.Context, req InventoryRequest) error {
	key := req.scopeKey()
	s.windowsMu.Lock()
	overdue := s.overduePasses[key] > 0
	slow, finishedSlow := s.slowPasses[key]
	s.windowsMu.Unlock()
	if overdue {
		return ErrWindowStartPending
	}
	started := s.now()
	if req.Force && finishedSlow && slow.generation == req.RootsGeneration &&
		started.Sub(slow.finished) <= max(slow.took, slowPassReuseFloor) {
		return nil
	}
	finished := make(chan error, 1)
	go func() { finished <- s.inventory.EnsureInventory(context.WithoutCancel(ctx), req) }()
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
	}
	s.windowsMu.Lock()
	s.overduePasses[key]++
	s.windowsMu.Unlock()
	go func() {
		err := <-finished
		s.windowsMu.Lock()
		defer s.windowsMu.Unlock()
		if s.overduePasses[key]--; s.overduePasses[key] == 0 {
			delete(s.overduePasses, key)
		}
		if err == nil {
			s.noteSlowPass(req, started)
		}
	}()
	return ctx.Err()
}

// Retain before-images unavailable from repository objects or history until the window settles.
const (
	windowRetainFiles = 8192
	windowRetainBytes = 256 << 20
)

// retainWindowStart keeps the before-images the window's admissions may need,
// bounded by windowRetainFiles, windowRetainBytes and retainBudget. A file left
// unretained loses its before-image, not its attribution, so running out of time
// keeps the window; stored objects carry over, so later opens resume cheaply.
func (s *Commands) retainWindowStart(ctx context.Context, w *openCommandWindow) error {
	if w.currentStart() == "" || s.snapshots == nil {
		return nil
	}
	budget, cancel := context.WithTimeout(ctx, s.retainBudget)
	defer cancel()
	// New objects are reclaimable until pinned, so the lease spans the pin.
	release := s.objects.AcquireReferenceLease()
	defer release()
	pins, err := s.retainStartObjects(budget, w)
	if err != nil && budget.Err() == nil {
		return err
	}
	if err != nil {
		slog.InfoContext(ctx, "command window start retention bounded by time",
			"window_id", w.id, "retained_files", len(pins), "limit", s.retainBudget)
	}
	return s.pinWindowObjects(context.WithoutCancel(ctx), w, pins)
}

// retainStartObjects stores what it can and returns what it stored, including
// when it stops early with an error.
func (s *Commands) retainStartObjects(ctx context.Context, w *openCommandWindow) ([]string, error) {
	entries, err := s.snapshots.HashedEntries(ctx, w.currentStart(), MaxRevisionContentBytes, windowRetainFiles+1)
	if err != nil {
		return nil, err
	}
	if len(entries) > windowRetainFiles {
		slog.InfoContext(ctx, "command window start retention bounded",
			"window_id", w.id, "unretained", len(entries)-windowRetainFiles, "limit", windowRetainFiles)
		entries = entries[:windowRetainFiles]
	}
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
			return pins, err
		}
		raw, err := s.snapshots.Bytes(ctx, entry)
		if errors.Is(err, sourcesnapshot.ErrContentUnavailable) {
			// Already moved on, or out of time; the pass that admits it records what it can.
			if ctx.Err() != nil {
				return pins, ctx.Err()
			}
			continue
		}
		if err != nil {
			return pins, err
		}
		rel, stored, oids, err := s.objects.Put(ctx, entry.SHA256, raw)
		if err != nil {
			return pins, err
		}
		if err := s.queries.UpsertSourceBlobObject(ctx, db.UpsertSourceBlobObjectParams{
			Sha256: entry.SHA256, Size: int64(len(raw)), StoredSize: stored, StorageRelpath: rel,
			GitOidSha1: oids.SHA1, GitOidSha256: oids.SHA256,
		}); err != nil {
			return pins, err
		}
		retained += int64(len(raw))
		pins = append(pins, entry.SHA256)
	}
	return pins, nil
}

func (s *Commands) pinWindowObjects(ctx context.Context, w *openCommandWindow, pins []string) error {
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

// Settled closes once settlement has finished, whether or not it succeeded.
func (o *OpenCommandWindow) Settled() <-chan struct{} {
	if o == nil || o.window == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return o.window.settled
}

// IsSettled reports, without waiting, whether settlement succeeded, so the
// window's record is complete.
func (o *OpenCommandWindow) IsSettled() bool {
	select {
	case <-o.Settled():
	default:
		return false
	}
	if o == nil || o.window == nil {
		return true
	}
	o.store.windowsMu.Lock()
	defer o.store.windowsMu.Unlock()
	return !o.window.incomplete
}

// SessionWindow is one of a session's windows that has not settled.
type SessionWindow struct {
	ID string
	// Closing means its process ended and only settlement remains.
	Closing bool
	Settled <-chan struct{}
	// Incomplete means its final pass failed, so what is recorded is a floor.
	// It stays listed until the next completed inventory pass.
	Incomplete bool
}

// OpenSessionWindows lists the session's unsettled and incomplete windows. Memory is the only
// record of an open window; rows from an earlier process were interrupted.
func (s *Commands) OpenSessionWindows(projectID, sessionID string) []SessionWindow {
	if s == nil {
		return nil
	}
	projectID, sessionID = strings.TrimSpace(projectID), strings.TrimSpace(sessionID)
	s.windowsMu.Lock()
	defer s.windowsMu.Unlock()
	var out []SessionWindow
	for _, window := range s.openWindows[projectID] {
		if window.sessionID == sessionID {
			out = append(out, SessionWindow{ID: window.id, Closing: window.closing, Settled: window.settled, Incomplete: window.incomplete})
		}
	}
	return out
}

func (s *Commands) closeCommandWindow(ctx context.Context, w *openCommandWindow) error {
	ended := time.Now().UTC()
	s.windowsMu.Lock()
	w.closing = true
	endGrace(w)
	w.passesAtQuiet = s.inventoryPasses[w.projectID]
	idle := !w.active
	anchoring := w.anchoring
	s.windowsMu.Unlock()
	if anchoring != nil {
		select {
		case <-anchoring:
		case <-ctx.Done():
		}
	}
	// An idle window owes no pass: it has claimed nothing since its grace ended.
	if !sourcesnapshot.ChangeTokenTrusted(snapshotRoots(w.roots)) {
		return s.settleCommandWindow(ctx, w, ended, !idle)
	}
	background := context.WithoutCancel(ctx)
	go func() {
		if !idle {
			s.awaitWindowPass(background, w, nil)
		}
		if err := s.settleCommandWindow(background, w, ended, false); err != nil {
			slog.WarnContext(background, "command window settle", "window_id", w.id, "err", err)
		}
	}()
	return nil
}

// awaitWindowPass returns once an inventory pass has completed since the
// window went quiet, the settle period elapsed, or stop closed.
func (s *Commands) awaitWindowPass(ctx context.Context, w *openCommandWindow, stop <-chan struct{}) {
	deadline := time.NewTimer(s.windowSettle)
	defer deadline.Stop()
	for {
		s.windowsMu.Lock()
		passed := s.inventoryPasses[w.projectID] > w.passesAtQuiet
		wake := s.passWake
		s.windowsMu.Unlock()
		if passed {
			return
		}
		select {
		case <-wake:
		case <-deadline.C:
			return
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// settleWindowObservations runs the last pass when one is still owed and
// observes Git. Pending observers commit their references before the caller
// collects the window, so it holds the returned release through that.
func (s *Commands) settleWindowObservations(ctx context.Context, w *openCommandWindow, force bool) (release func(), passErr, lockErr error) {
	roots := snapshotRoots(w.roots)
	s.windowsMu.Lock()
	owed := force || w.incomplete || (w.active && s.inventoryPasses[w.projectID] <= w.passesAtQuiet && sourcesnapshot.ChangeToken(roots) != w.startEpoch)
	s.windowsMu.Unlock()
	if owed {
		req, err := s.windowInventoryRequest(ctx, w.projectID, w.roots)
		if err != nil {
			passErr = err
		} else {
			req.Force, req.Wait = true, true
			started := s.now()
			passErr = s.inventory.EnsureInventory(ctx, req)
			if passErr == nil && s.now().Sub(started) > s.observationBudget {
				s.windowsMu.Lock()
				s.noteSlowPass(req, started)
				s.windowsMu.Unlock()
			}
		}
	}
	release, lockErr = s.git.lockObservations(ctx, w.projectID, w.roots)
	if release != nil {
		// HEAD may move without invalidating any working-file snapshots.
		_, gitErr := s.git.observeGitState(ctx, w.projectID, w.roots)
		passErr = errors.Join(passErr, gitErr)
	}
	return release, passErr, lockErr
}

// settleCommandWindow runs the last pass when one is still owed, stops
// attributing, and makes the row final. A failure anywhere leaves the window
// listed as incomplete until the next completed pass.
func (s *Commands) settleCommandWindow(ctx context.Context, w *openCommandWindow, ended time.Time, force bool) error {
	defer close(w.settled)
	release, passErr, lockErr := s.settleWindowObservations(ctx, w, force)
	if release != nil {
		defer release()
	}
	s.windowsMu.Lock()
	w.active = false
	w.endedAt = ended
	s.windowsMu.Unlock()
	var finishErr error
	if lockErr == nil {
		w.mu.Lock()
		admission := w.admissionMode
		w.mu.Unlock()
		finishErr = s.finishWindow(ctx, w.id, admission, ended)
	}
	s.windowsMu.Lock()
	defer s.windowsMu.Unlock()
	w.finalized = true
	// Without its lock or its final write the row stays for in-flight observers
	// and interrupted-window recovery.
	w.rowOwed = lockErr != nil || finishErr != nil
	err := errors.Join(passErr, lockErr, finishErr)
	if err != nil {
		w.incomplete = true
	}
	if !w.incomplete {
		s.forgetWindow(w)
	}
	return err
}

// forgetWindow removes a settled window. Caller holds windowsMu.
func (s *Commands) forgetWindow(w *openCommandWindow) {
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
}

// notePassCompleted counts the pass, wakes windows waiting on one, and clears
// windows left incomplete on this branch: the pass recorded their drift as
// outside changes, so nothing further is owed.
func (s *Commands) notePassCompleted(ctx context.Context, projectID string, roots []RootSpec) {
	s.windowsMu.Lock()
	s.inventoryPasses[projectID]++
	close(s.passWake)
	s.passWake = make(chan struct{})
	var owed []*openCommandWindow
	for _, window := range append([]*openCommandWindow(nil), s.openWindows[projectID]...) {
		if !window.incomplete || inventoryBranch(window.roots) != inventoryBranch(roots) {
			continue
		}
		window.incomplete = false
		if !window.finalized {
			continue
		}
		if window.rowOwed {
			owed = append(owed, window)
		}
		s.forgetWindow(window)
	}
	s.windowsMu.Unlock()
	for _, window := range owed {
		if err := s.finishWindow(context.WithoutCancel(ctx), window.id, "", window.endedAt); err != nil {
			slog.WarnContext(ctx, "command window finish after failed settlement", "window_id", window.id, "err", err)
		}
	}
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

// RecoverInterruptedWindows settles rows an earlier process left running:
// memory is the only record of an open window, so all of them are interrupted.
// A failure leaves it to be retried; no window opens until it has succeeded.
func (s *Commands) RecoverInterruptedWindows(ctx context.Context) error {
	s.windowRecoveryMu.Lock()
	defer s.windowRecoveryMu.Unlock()
	if s.windowsRecovered {
		return nil
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.queries.InterruptRunningSourceCommandWindows(ctx, now); err != nil {
		return err
	}
	if _, err := s.queries.DeleteSettledSourceCommandWindowObjects(ctx); err != nil {
		return err
	}
	if _, err := s.queries.DeleteUnreferencedEndedSourceCommandWindows(ctx); err != nil {
		return err
	}
	s.windowsRecovered = true
	return nil
}

// attributionWindow names the window that may claim drift on these roots: one
// with a live process or still in its settle grace. Active windows of more than
// one session attribute to nobody, because nothing says whose the drift is; a
// file missing from a bulk commit is visible, and another's work staged is not.
// Within one session the newest running window wins, then the newest settling.
// An incomplete window claims nothing but still counts as a session in doubt.
func (s *Commands) attributionWindow(projectID string, roots []RootSpec) *openCommandWindow {
	s.windowsMu.Lock()
	defer s.windowsMu.Unlock()
	branch := inventoryBranch(roots)
	held := make([]*openCommandWindow, 0)
	session, seen := "", false
	for _, window := range s.openWindows[projectID] {
		if inventoryBranch(window.roots) != branch || (!window.active && !window.incomplete) {
			continue
		}
		if seen && window.sessionID != session {
			return nil
		}
		session, seen = window.sessionID, true
		if window.active {
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

// unclaimedWindows lists every registered window on these roots, for a pass that
// has no window to attribute to.
func (s *Commands) unclaimedWindows(projectID string, roots []RootSpec) []*openCommandWindow {
	s.windowsMu.Lock()
	defer s.windowsMu.Unlock()
	branch := inventoryBranch(roots)
	var out []*openCommandWindow
	for _, window := range s.openWindows[projectID] {
		if inventoryBranch(window.roots) == branch {
			out = append(out, window)
		}
	}
	return out
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

// SetWindowSettleForTest replaces the settle period, so a test decides whether
// a closed window on trusted coverage settles on a pass rather than on time.
func (s *Commands) SetWindowSettleForTest(period time.Duration) {
	s.windowSettle = period
}

// SetWindowIdledForTest observes each window whose settle grace has ended.
func (s *Commands) SetWindowIdledForTest(observe func(id string)) {
	s.windowsMu.Lock()
	defer s.windowsMu.Unlock()
	s.windowIdled = observe
}

// Commands attributes source observations to host command windows.
type Commands struct {
	inventoryPasses   map[string]uint64
	now               func() time.Time
	objects           *sourceblob.Store
	observationBudget time.Duration
	openWindows       map[string][]*openCommandWindow
	// overduePasses counts passes still running after an open stopped waiting.
	overduePasses map[string]int
	passWake      chan struct{}
	queries       *db.Queries
	recordMu      *sync.Mutex
	// retainBudget is the share of an open spent keeping before-images.
	retainBudget time.Duration
	// slowPasses remembers, per scope, the last pass that outlasted the budget.
	slowPasses       map[string]slowPass
	snapshots        *sourcesnapshot.Store
	sqlDB            db.Handle
	windowRecoveryMu sync.Mutex
	windowsRecovered bool
	windowSettle     time.Duration
	windowsMu        sync.Mutex
	git              commandsGitPort
	inventory        commandsInventoryPort
	// windowIdled, when set, observes each window whose grace ended.
	windowIdled func(id string)
}

type commandsInventoryPort interface {
	EnsureInventory(ctx context.Context, req InventoryRequest) error
	InventoryState(ctx context.Context, projectID string, branch sourcebranch.ID, rootsGeneration int) (InventoryState, error)
}

type commandsGitPort interface {
	lockObservations(ctx context.Context, projectID string, roots []RootSpec) (func(), error)
	observeGitState(ctx context.Context, projectID string, roots []RootSpec) (map[string]string, error)
}
