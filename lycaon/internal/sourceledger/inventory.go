package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// InventoryPhase is the project inventory lifecycle.
type InventoryPhase string

const (
	InventoryUninitialized InventoryPhase = "uninitialized"
	InventoryQueued        InventoryPhase = "queued"
	InventoryScanning      InventoryPhase = "scanning"
	InventoryReady         InventoryPhase = "ready"
	InventoryError         InventoryPhase = "error"
)

// InventoryRequest identifies one immutable project-root composition.
type InventoryRequest struct {
	ProjectID       string
	RootsGeneration int
	Roots           []RootSpec
	// Force runs a pass even when the recorded state looks current.
	Force bool
	// Wait blocks a request that joins an active job until a pass covering
	// it has completed.
	Wait           bool
	requestedEpoch string
	serial         uint64
}

// InventoryState is the read-side status for source views and diagnostics.
type InventoryState struct {
	ProjectID           string
	RequestedGeneration int
	CompletedGeneration int
	Phase               InventoryPhase
	Complete            bool
	FileCount           int64
	RequestedAt         time.Time
	StartedAt           time.Time
	CompletedAt         time.Time
	EpochToken          string
	SnapshotID          string
}

type inventoryJob struct {
	request InventoryRequest
	ctx     context.Context
	cancel  context.CancelFunc
	// done closes once a pass has run for the newest merged request; err is
	// that pass's result.
	done chan struct{}
	err  error
}

// EnsureInventory waits for passes it starts. Joining requests return after
// merging unless Wait requires completion of the merged rerun.
func (s *Inventory) EnsureInventory(ctx context.Context, req InventoryRequest) error {
	if s == nil || strings.TrimSpace(req.ProjectID) == "" || req.RootsGeneration < 0 {
		return nil
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.Roots = append([]RootSpec(nil), req.Roots...)
	// Change tokens avoid redundant inventory passes.
	req.requestedEpoch = sourcesnapshot.ChangeToken(snapshotRoots(req.Roots))

admit:
	if err := ctx.Err(); err != nil {
		return err
	}
	s.inventoryMu.Lock()
	if s.inventorySuspended[req.ProjectID] > 0 {
		s.inventoryMu.Unlock()
		return context.Canceled
	}
	s.inventorySerial++
	req.serial = s.inventorySerial
	if active := s.inventoryJobs[req.scopeKey()]; active != nil {
		if active.ctx.Err() != nil {
			done := active.done
			s.inventoryMu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-done:
				goto admit
			}
		}
		if req.Force || req.RootsGeneration > active.request.RootsGeneration ||
			(req.RootsGeneration == active.request.RootsGeneration && req.requestedEpoch != active.request.requestedEpoch) {
			active.request = req
		}
		done := active.done
		s.inventoryMu.Unlock()
		if !req.Wait {
			return nil
		}
		select {
		case <-done:
			return active.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if !req.Force {
		state, err := s.InventoryState(ctx, req.ProjectID, inventoryBranch(req.Roots), req.RootsGeneration)
		if err != nil {
			s.inventoryMu.Unlock()
			return err
		}
		// Incomplete watcher coverage expires ready state.
		if state.CompletedGeneration == req.RootsGeneration &&
			state.EpochToken == req.requestedEpoch &&
			s.inventoryStateReusable(state, req.Roots) {
			s.inventoryMu.Unlock()
			return nil
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	job := &inventoryJob{request: req, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	s.inventoryJobs[req.scopeKey()] = job
	s.inventoryMu.Unlock()

	defer s.releaseInventoryJob(req.scopeKey(), job)

	for {
		if err := ctx.Err(); err != nil {
			job.err = err
			return err
		}
		s.inventoryMu.Lock()
		current := job.request
		s.inventoryMu.Unlock()
		runErr := s.runInventoryGeneration(ctx, current)
		if s.settleInventoryJob(req.scopeKey(), job, current, runErr) {
			return runErr
		}
	}
}

// SuspendInventory cancels and drains every branch's discovery before a project
// changes roots or is removed. Admission stays closed until resume is called.
func (s *Inventory) SuspendInventory(ctx context.Context, projectID string) (resume func(), err error) {
	if s == nil {
		return func() {}, nil
	}
	s.inventoryMu.Lock()
	if s.inventorySuspended == nil {
		s.inventorySuspended = make(map[string]int)
	}
	s.inventorySuspended[projectID]++
	var jobs []*inventoryJob
	for _, job := range s.inventoryJobs {
		if job.request.ProjectID == projectID {
			job.cancel()
			jobs = append(jobs, job)
		}
	}
	s.inventoryMu.Unlock()
	resume = sync.OnceFunc(func() {
		s.inventoryMu.Lock()
		defer s.inventoryMu.Unlock()
		if s.inventorySuspended[projectID] <= 1 {
			delete(s.inventorySuspended, projectID)
		} else {
			s.inventorySuspended[projectID]--
		}
	})
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			return resume, ctx.Err()
		case <-job.done:
		}
	}
	return resume, nil
}

func (s *Inventory) inventoryStateReusable(state InventoryState, roots []RootSpec) bool {
	if sourcesnapshot.ChangeTokenTrusted(snapshotRoots(roots)) {
		return true
	}
	if state.CompletedAt.IsZero() {
		return false
	}
	now := time.Now()
	if s.inventoryNow != nil {
		now = s.inventoryNow()
	}
	age := now.UTC().Sub(state.CompletedAt.UTC())
	return age >= 0 && age < repochange.CoverageRevalidationInterval
}

// settleInventoryJob retires the job once the pass that just ran was for the
// newest merged request; a request merged meanwhile makes the caller loop.
func (s *Inventory) settleInventoryJob(projectID string, job *inventoryJob, completed InventoryRequest, runErr error) bool {
	s.inventoryMu.Lock()
	defer s.inventoryMu.Unlock()
	if s.inventoryJobs[projectID] != job || job.request.serial != completed.serial {
		return false
	}
	delete(s.inventoryJobs, projectID)
	job.err = runErr
	close(job.done)
	return true
}

func (s *Inventory) releaseInventoryJob(projectID string, job *inventoryJob) {
	s.inventoryMu.Lock()
	if s.inventoryJobs[projectID] == job {
		delete(s.inventoryJobs, projectID)
		close(job.done)
	}
	s.inventoryMu.Unlock()
}

func (s *Inventory) runInventoryGeneration(ctx context.Context, req InventoryRequest) error {
	now := db.FormatTime(time.Now().UTC())
	if err := s.queries.QueueSourceInventory(ctx, db.QueueSourceInventoryParams{
		ProjectID: req.ProjectID, BranchID: inventoryBranch(req.Roots).String(), RequestedGeneration: int64(req.RootsGeneration),
		RequestedEpoch: req.requestedEpoch, RequestedTs: now,
	}); err != nil {
		return err
	}
	updated, err := s.queries.MarkSourceInventoryScanning(ctx, db.MarkSourceInventoryScanningParams{
		StartedTs: now, ProjectID: req.ProjectID, BranchID: inventoryBranch(req.Roots).String(), RequestedGeneration: int64(req.RootsGeneration),
	})
	if err != nil {
		return err
	}
	if updated == 0 {
		return nil
	}
	started := time.Now()
	slog.InfoContext(ctx, "source inventory scanning",
		"project_id", req.ProjectID, "roots_generation", req.RootsGeneration, "roots", len(req.Roots))
	outcome, snapshotID, completedEpoch, scanErr := s.reconcileInventoryRequest(ctx, req)
	completed := db.FormatTime(time.Now().UTC())
	if scanErr != nil {
		_, markErr := s.queries.MarkSourceInventoryError(ctx, db.MarkSourceInventoryErrorParams{
			CompletedTs: completed, LastError: inventoryErrorText(scanErr),
			ProjectID: req.ProjectID, BranchID: inventoryBranch(req.Roots).String(), RequestedGeneration: int64(req.RootsGeneration),
		})
		slog.WarnContext(ctx, "source inventory failed",
			"project_id", req.ProjectID, "duration_ms", time.Since(started).Milliseconds(), "err", scanErr)
		s.signalInventoryMoved(ctx, req)
		if markErr != nil {
			return errors.Join(scanErr, markErr)
		}
		return scanErr
	}
	_, err = s.queries.MarkSourceInventoryReady(ctx, db.MarkSourceInventoryReadyParams{
		CompletedGeneration: int64(req.RootsGeneration), CompletedEpoch: completedEpoch,
		SnapshotID: snapshotID, FileCount: int64(outcome.files), CompletedTs: completed,
		ProjectID: req.ProjectID, BranchID: inventoryBranch(req.Roots).String(), RequestedGeneration: int64(req.RootsGeneration),
	})
	if err != nil {
		return err
	}
	s.commands.notePassCompleted(req.ProjectID)
	slog.InfoContext(ctx, "source inventory ready",
		"project_id", req.ProjectID, "duration_ms", time.Since(started).Milliseconds(),
		"files", outcome.files, "recorded", outcome.recorded)
	s.signalInventoryMoved(ctx, req)
	return nil
}

// signalInventoryMoved tells projections the inventory phase changed. The
// phase is a fact consumers present, so every completion announces itself.
func (s *Inventory) signalInventoryMoved(ctx context.Context, req InventoryRequest) {
	if err := sourcefeed.EmitProjectSignal(ctx, req.ProjectID, rootRefsOf(req.Roots)); err != nil {
		slog.WarnContext(ctx, "source inventory signal", "project_id", req.ProjectID, "err", err)
	}
}

func (s *Inventory) reconcileInventoryRequest(
	ctx context.Context,
	req InventoryRequest,
) (outcome reconcileOutcome, snapshotID, epoch string, err error) {
	if s.inventoryReconcile != nil {
		count, err := s.inventoryReconcile(ctx, req.ProjectID, req.Roots)
		return reconcileOutcome{files: count}, "test-inventory", req.requestedEpoch, err
	}
	if s.snapshots == nil {
		return reconcileOutcome{}, "", "", errors.New("source snapshot store is not configured")
	}
	release, err := s.git.lockObservations(ctx, req.ProjectID, req.Roots)
	if err != nil {
		return reconcileOutcome{}, "", "", err
	}
	defer release()
	snapshot, err := s.snapshots.Ensure(ctx, sourcesnapshot.Request{Roots: snapshotRoots(req.Roots)})
	if err != nil {
		return reconcileOutcome{}, "", "", err
	}
	// Preserve a changed token for the next inventory signal.
	if snapshot.Quality != sourcesnapshot.CaptureExact {
		now := db.FormatTime(time.Now().UTC())
		if err := s.queries.QueueSourceInventory(ctx, db.QueueSourceInventoryParams{
			ProjectID: req.ProjectID, BranchID: inventoryBranch(req.Roots).String(), RequestedGeneration: int64(req.RootsGeneration),
			RequestedEpoch: sourcesnapshot.ChangeToken(snapshotRoots(req.Roots)), RequestedTs: now,
		}); err != nil {
			return reconcileOutcome{}, "", "", err
		}
	}
	// Observe ref movements before attributing file changes. Failed observations
	// leave external changes unattributed.
	transitionByRoot, gitErr := s.git.observeGitState(ctx, req.ProjectID, req.Roots)
	if gitErr != nil {
		transitionByRoot = nil
	}
	outcome, err = s.reconcileSnapshot(
		ctx, req.ProjectID,
		snapshot, req.Roots, transitionByRoot, s.commands.attributionWindow(req.ProjectID, req.Roots),
	)
	if err != nil {
		return outcome, snapshot.ID, req.requestedEpoch, err
	}
	// A pass repairs a little on completion; one deferred behind another
	// capture is not this inventory's failure.
	if err := s.retention.MaintainBlobs(ctx); err != nil && !errors.Is(err, ErrBlobMaintenanceDeferred) {
		return outcome, snapshot.ID, req.requestedEpoch, err
	}
	return outcome, snapshot.ID, req.requestedEpoch, nil
}

func snapshotRoots(roots []RootSpec) []sourcesnapshot.Root {
	out := make([]sourcesnapshot.Root, 0, len(roots))
	for _, root := range roots {
		out = append(out, sourcesnapshot.Root{Path: cleanRootPath(root.Path)})
	}
	return out
}

// InventoryState returns a generation-aware lifecycle without starting work.
func (s *Inventory) InventoryState(ctx context.Context, projectID string, branch sourcebranch.ID, rootsGeneration int) (InventoryState, error) {
	out := InventoryState{
		ProjectID: projectID, RequestedGeneration: rootsGeneration,
		CompletedGeneration: -1, Phase: InventoryUninitialized,
	}
	if s == nil || strings.TrimSpace(projectID) == "" {
		return out, nil
	}
	row, err := s.queries.GetSourceInventoryState(ctx, db.GetSourceInventoryStateParams{ProjectID: projectID, BranchID: branch.String()})
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.RequestedGeneration = int(row.RequestedGeneration)
	out.CompletedGeneration = int(row.CompletedGeneration)
	out.FileCount = row.FileCount
	out.EpochToken = row.CompletedEpoch
	out.SnapshotID = row.SnapshotID
	out.Complete = row.CompletedGeneration == int64(rootsGeneration) &&
		row.CompletedEpoch != "" && row.CompletedEpoch == row.RequestedEpoch
	if out.Complete {
		out.Phase = InventoryReady
	} else {
		out.Phase = InventoryPhase(row.Phase)
	}
	out.RequestedAt = parseInventoryTime(row.RequestedTs)
	out.StartedAt = parseInventoryTime(row.StartedTs)
	out.CompletedAt = parseInventoryTime(row.CompletedTs)
	return out, nil
}

func parseInventoryTime(raw string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, raw)
	return t
}

func inventoryErrorText(err error) string {
	if err == nil {
		return ""
	}
	const max = 500
	text := strings.TrimSpace(err.Error())
	if len(text) > max {
		text = text[:max]
	}
	return fmt.Sprintf("inventory: %s", text)
}

// A session binds at most one checkout; other roots retain their base branch.
func inventoryBranch(roots []RootSpec) sourcebranch.ID {
	for _, root := range roots {
		if root.BranchID != sourcebranch.Trunk {
			return root.BranchID
		}
	}
	return sourcebranch.Trunk
}
func (r InventoryRequest) scopeKey() string {
	return r.ProjectID + "\x00" + inventoryBranch(r.Roots).String()
}

// Inventory schedules source observations and reconciles current heads.
type Inventory struct {
	inventoryJobs      map[string]*inventoryJob
	inventoryMu        sync.Mutex
	inventoryNow       func() time.Time
	inventoryReconcile func(context.Context, string, []RootSpec) (int, error)
	inventorySerial    uint64
	inventorySuspended map[string]int
	observationBudget  time.Duration
	queries            *db.Queries
	recordMu           *sync.Mutex
	snapshots          *sourcesnapshot.Store
	sqlDB              db.Handle
	commands           inventoryCommandsPort
	git                inventoryGitPort
	retention          inventoryRetentionPort
	writer             observationRecorder
}

type inventoryCommandsPort interface {
	attributionWindow(projectID string, roots []RootSpec) *openCommandWindow
	notePassCompleted(projectID string)
}

type inventoryGitPort interface {
	lockObservations(ctx context.Context, projectID string, roots []RootSpec) (func(), error)
	observeGitState(ctx context.Context, projectID string, roots []RootSpec) (map[string]string, error)
}

type inventoryRetentionPort interface {
	MaintainBlobs(ctx context.Context) error
}

// Observation recording shares mutation admission and the writer's transaction.
type observationRecorder interface {
	RecordBatchTx(context.Context, *sql.Tx, []RecordInput) error
	recordBatchTx(context.Context, *db.Queries, []RecordInput) error
	mutationObservationScope(context.Context, *sql.Tx, string) (MutationObservationScope, error)
}
