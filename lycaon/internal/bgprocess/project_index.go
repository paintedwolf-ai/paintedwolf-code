package bgprocess

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrTeardownInProgress reports that the target project, session, or workspace root is being torn down.
var ErrTeardownInProgress = errors.New("workspace teardown in progress")

// ProjectBackgroundPublisher emits project-scoped background process lifecycle events.
type ProjectBackgroundPublisher func(ctx context.Context, ev api.ProjectBackgroundEvent)

// ProjectProcess is the registry's snapshot of one running or recently ended
// background command before session-store and workspace-root hydration.
type ProjectProcess struct {
	Handle            string
	ProjectID         string
	SessionID         string
	RootSessionID     string
	Command           string
	Cwd               string
	Ports             []string
	StartedAt         time.Time
	EndedAt           *time.Time
	Running           bool
	HasExit           bool
	ExitCode          int
	TerminationReason TerminationReason
}

func (r *processTable) checkAdmissionLocked(projectID, sessionID, rootSessionID, workDir string) (TerminationReason, bool) {
	if projectID != "" && r.deletingProjects[projectID] {
		return TerminationProjectDeleted, false
	}
	if sessionID != "" && r.deletingSessions[sessionID] {
		return TerminationChatDeleted, false
	}
	if rootSessionID != "" && r.deletingSessions[rootSessionID] {
		return TerminationChatDeleted, false
	}
	if projectID != "" {
		for _, rootPath := range r.detachingRoots[projectID] {
			if rootPath == "" {
				return TerminationProjectDeleted, false
			}
			if workDir != "" && projectroot.PathWithinRoot(rootPath, workDir) {
				return TerminationRootDetached, false
			}
		}
	}
	return "", true
}

func (r *processTable) recordStagePID(handle string, pid int) {
	if r == nil || pid <= 0 || handle == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.workDirs[handle]; !ok {
		return
	}
	if !slices.Contains(r.pids[handle], pid) {
		r.pids[handle] = append(r.pids[handle], pid)
	}
}

func (r *processTable) indexProjectProcessLocked(proc *Process) (api.ProjectBackgroundEvent, bool) {
	if proc == nil || proc.silent || proc.discarded {
		return api.ProjectBackgroundEvent{}, false
	}
	projectID := proc.ProjectID
	if r.projectProcs[projectID] == nil {
		r.projectProcs[projectID] = make(map[string]*Process)
	}
	r.projectProcs[projectID][proc.Handle] = proc
	r.pruneProjectCompletedLocked(projectID)
	if projectID == "" {
		return api.ProjectBackgroundEvent{}, false
	}
	r.projectRevisions[projectID]++
	rev := r.projectRevisions[projectID]
	ownerSession := proc.RootSessionID
	if ownerSession == "" {
		ownerSession = proc.SessionID
	}
	return api.ProjectBackgroundEvent{
		ProjectID: projectID,
		ProcessID: proc.Handle,
		SessionID: ownerSession,
		Action:    "started",
		Revision:  rev,
		Running:   true,
		Ports:     append([]string(nil), r.ports[proc.Handle]...),
	}, true
}

func (r *processTable) finishProjectProcessLocked(proc *Process) (api.ProjectBackgroundEvent, bool) {
	if proc == nil {
		return api.ProjectBackgroundEvent{}, false
	}
	delete(r.ports, proc.Handle)
	delete(r.pids, proc.Handle)
	if proc.silent || proc.discarded {
		return api.ProjectBackgroundEvent{}, false
	}
	projectID := proc.ProjectID
	if r.projectProcs[projectID] == nil {
		r.projectProcs[projectID] = make(map[string]*Process)
	}
	r.projectProcs[projectID][proc.Handle] = proc
	r.pruneProjectCompletedLocked(projectID)
	if projectID == "" {
		return api.ProjectBackgroundEvent{}, false
	}
	r.projectRevisions[projectID]++
	rev := r.projectRevisions[projectID]
	ownerSession := proc.RootSessionID
	if ownerSession == "" {
		ownerSession = proc.SessionID
	}
	var exitCode *int
	if proc.hasExit {
		code := proc.exitCode
		exitCode = &code
	}
	return api.ProjectBackgroundEvent{
		ProjectID:         projectID,
		ProcessID:         proc.Handle,
		SessionID:         ownerSession,
		Action:            "ended",
		Revision:          rev,
		Running:           false,
		ExitCode:          exitCode,
		TerminationReason: api.BackgroundProcessTerminationReason(proc.reason),
	}, true
}

func (r *processTable) pruneProjectCompletedLocked(projectID string) {
	procs := r.projectProcs[projectID]
	if len(procs) == 0 {
		return
	}
	type completedJob struct {
		handle     string
		sessionID  string
		finishedAt time.Time
	}
	var completed []completedJob
	for handle, proc := range procs {
		if proc != nil && !proc.running && proc.hasExit {
			completed = append(completed, completedJob{
				handle:     handle,
				sessionID:  proc.SessionID,
				finishedAt: proc.finishedAt,
			})
		}
	}
	if len(completed) <= r.maxRecent {
		return
	}
	sort.Slice(completed, func(i, j int) bool {
		if completed[i].finishedAt.Equal(completed[j].finishedAt) {
			return completed[i].handle < completed[j].handle
		}
		return completed[i].finishedAt.Before(completed[j].finishedAt)
	})
	for _, job := range completed[:len(completed)-r.maxRecent] {
		delete(procs, job.handle)
		r.cleanupHandleAuxLocked(job.sessionID, projectID, job.handle)
	}
}

func (r *processTable) cleanupHandleAuxLocked(sessionID, projectID, handle string) {
	if sessionProcs := r.sessions[sessionID]; sessionProcs != nil {
		if _, ok := sessionProcs[handle]; ok {
			return
		}
	}
	if projProcs := r.projectProcs[projectID]; projProcs != nil {
		if _, ok := projProcs[handle]; ok {
			return
		}
	}
	delete(r.workDirs, handle)
	delete(r.ports, handle)
	delete(r.pids, handle)
}

func (r *processTable) unindexProjectProcessLocked(proc *Process) {
	if proc == nil {
		return
	}
	if projProcs := r.projectProcs[proc.ProjectID]; projProcs != nil {
		delete(projProcs, proc.Handle)
		if len(projProcs) == 0 {
			delete(r.projectProcs, proc.ProjectID)
		}
	}
	delete(r.ports, proc.Handle)
	delete(r.pids, proc.Handle)
}

func (r *processTable) hasLiveBackgroundLocked() bool {
	for _, procs := range r.projectProcs {
		for _, proc := range procs {
			if proc != nil && proc.running && !proc.stopped && !proc.hasExit && !proc.silent && !proc.discarded {
				return true
			}
		}
	}
	return false
}

func (r *processTable) hasLiveBackground() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hasLiveBackgroundLocked()
}

type pendingProjectRow struct {
	row    ProjectProcess
	stages []hostcmd.StageResult
	scope  captureprojection.Scope
}

func snapshotProjectRowLocked(r *processTable, proc *Process) pendingProjectRow {
	ownerSession := proc.RootSessionID
	if ownerSession == "" {
		ownerSession = proc.SessionID
	}
	var ports []string
	if proc.running && len(r.ports[proc.Handle]) > 0 {
		ports = append([]string(nil), r.ports[proc.Handle]...)
	}
	var endedAt *time.Time
	if proc.hasExit && !proc.finishedAt.IsZero() {
		finished := proc.finishedAt
		endedAt = &finished
	}
	var reason TerminationReason
	if proc.hasExit {
		reason = proc.reason
	}
	return pendingProjectRow{
		row: ProjectProcess{
			Handle:            proc.Handle,
			ProjectID:         proc.ProjectID,
			SessionID:         proc.SessionID,
			RootSessionID:     ownerSession,
			Cwd:               r.workDirs[proc.Handle],
			Ports:             ports,
			StartedAt:         proc.startedAt,
			EndedAt:           endedAt,
			Running:           proc.running,
			HasExit:           proc.hasExit,
			ExitCode:          proc.exitCode,
			TerminationReason: reason,
		},
		stages: append([]hostcmd.StageResult(nil), proc.Stages...),
		scope:  processCaptureScope(proc),
	}
}

func projectCommandLine(
	ctx context.Context,
	projector *captureprojection.Projector,
	scope captureprojection.Scope,
	stages []hostcmd.StageResult,
) string {
	cmd := hostcmd.CommandLine(stages)
	if cmd == "" || projector == nil {
		return cmd
	}
	projected, err := projector.Text(ctx, scope, "capture.process.command", cmd)
	if err != nil {
		return captureUnavailableText
	}
	return projected.Value
}

func finalizeProjectRows(
	ctx context.Context,
	projector *captureprojection.Projector,
	pending []pendingProjectRow,
) []ProjectProcess {
	if len(pending) == 0 {
		return nil
	}
	out := make([]ProjectProcess, len(pending))
	for i, item := range pending {
		row := item.row
		row.Command = projectCommandLine(ctx, projector, item.scope, item.stages)
		out[i] = row
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Running != out[j].Running {
			return out[i].Running
		}
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].Handle < out[j].Handle
	})
	return out
}

// ListProjectBackground returns running and recently ended background commands for projectID.
func (r *Registry) ListProjectBackground(ctx context.Context, projectID string) ([]ProjectProcess, int64) {
	if r == nil || r.jobs == nil {
		return nil, 0
	}
	projectID = trim(projectID)
	r.jobs.mu.Lock()
	rev := r.jobs.projectRevisions[projectID]
	procs := r.jobs.projectProcs[projectID]
	pending := make([]pendingProjectRow, 0, len(procs))
	for _, proc := range procs {
		if proc == nil || proc.silent || proc.discarded {
			continue
		}
		pending = append(pending, snapshotProjectRowLocked(r.jobs, proc))
	}
	projector := r.Output.projector
	r.jobs.mu.Unlock()
	return finalizeProjectRows(ctx, projector, pending), rev
}

// AllRunningBackgroundProcesses returns every live background command across all projects.
func (r *Registry) AllRunningBackgroundProcesses(ctx context.Context) []ProjectProcess {
	if r == nil || r.jobs == nil {
		return nil
	}
	r.jobs.mu.Lock()
	var pending []pendingProjectRow
	for _, procs := range r.jobs.projectProcs {
		for _, proc := range procs {
			if proc == nil || !proc.running || proc.silent || proc.discarded {
				continue
			}
			pending = append(pending, snapshotProjectRowLocked(r.jobs, proc))
		}
	}
	projector := r.Output.projector
	r.jobs.mu.Unlock()
	return finalizeProjectRows(ctx, projector, pending)
}

// ProjectProcessesUnderRoot returns live background commands under rootPath with command and port metadata.
func (r *Registry) ProjectProcessesUnderRoot(
	ctx context.Context,
	projectID string,
	sessionIDs map[string]struct{},
	rootPath string,
) []ProjectProcess {
	if r == nil || r.jobs == nil {
		return nil
	}
	projectID = trim(projectID)
	rootPath = trim(rootPath)
	r.jobs.mu.Lock()
	var pending []pendingProjectRow
	for sessionID, procs := range r.jobs.sessions {
		for handle, proc := range procs {
			if proc == nil || !proc.running || proc.silent || proc.discarded {
				continue
			}
			if !matchesProjectOrSessions(proc, sessionID, projectID, sessionIDs) {
				continue
			}
			workDir := r.jobs.workDirs[handle]
			if rootPath != "" && !projectroot.PathWithinRoot(rootPath, workDir) {
				continue
			}
			pending = append(pending, snapshotProjectRowLocked(r.jobs, proc))
		}
	}
	projector := r.Output.projector
	r.jobs.mu.Unlock()
	out := finalizeProjectRows(ctx, projector, pending)
	sort.Slice(out, func(i, j int) bool {
		if out[i].SessionID == out[j].SessionID {
			return out[i].Handle < out[j].Handle
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

// SessionBackgroundRunningCounts returns live background command counts keyed by session ID for projectID.
func (r *Registry) SessionBackgroundRunningCounts(projectID string) map[string]int {
	if r == nil || r.jobs == nil {
		return nil
	}
	projectID = trim(projectID)
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	procs := r.jobs.projectProcs[projectID]
	if len(procs) == 0 {
		return nil
	}
	counts := make(map[string]int)
	for _, proc := range procs {
		if proc == nil || !proc.running || proc.silent || proc.discarded || proc.kind != processKindPipeline {
			continue
		}
		owner := proc.RootSessionID
		if owner == "" {
			owner = proc.SessionID
		}
		if owner != "" {
			counts[owner]++
		}
		if proc.SessionID != "" && proc.SessionID != owner {
			counts[proc.SessionID]++
		}
	}
	return counts
}

// SessionBackgroundRunningCount returns the number of live background commands attributed to sessionID.
func (r *Registry) SessionBackgroundRunningCount(sessionID string) int {
	if r == nil || r.jobs == nil {
		return 0
	}
	sessionID = trim(sessionID)
	if sessionID == "" {
		return 0
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	n := 0
	for _, procs := range r.jobs.projectProcs {
		for _, proc := range procs {
			if proc == nil || !proc.running || proc.silent || proc.discarded || proc.kind != processKindPipeline {
				continue
			}
			if proc.RootSessionID == sessionID || proc.SessionID == sessionID {
				n++
			}
		}
	}
	return n
}

// StopProjectProcess stops a background command in projectID by handle.
func (r *ProcessLifecycle) StopProjectProcess(
	projectID, handle string,
	reason TerminationReason,
) (*api.BackgroundProcessStopResult, error) {
	if r == nil || r.jobs == nil {
		return nil, ErrProcessNotFound
	}
	projectID = trim(projectID)
	handle = trim(handle)
	if projectID == "" || handle == "" {
		return nil, ErrProcessNotFound
	}
	r.jobs.mu.Lock()
	var proc *Process
	if projProcs := r.jobs.projectProcs[projectID]; projProcs != nil {
		proc = projProcs[handle]
	}
	if proc == nil || proc.silent || proc.discarded || proc.ProjectID != projectID {
		r.jobs.mu.Unlock()
		return nil, ErrProcessNotFound
	}
	r.jobs.mu.Unlock()
	r.killProcess(proc, reason)
	running, hasExit, exitCode, settledReason := r.jobs.exitState(proc)
	out := &api.BackgroundProcessStopResult{
		ProcessID:         handle,
		StopRequested:     true,
		Running:           running,
		TerminationReason: api.BackgroundProcessTerminationReason(settledReason),
	}
	if hasExit {
		out.ExitCode = &exitCode
	}
	return out, nil
}

// DisposeProject stops and removes every process in projectID and prevents new admissions during teardown.
func (r *ProcessLifecycle) DisposeProject(ctx context.Context, projectID string, reason TerminationReason) error {
	if r == nil || r.jobs == nil {
		return nil
	}
	projectID = trim(projectID)
	if projectID == "" {
		return nil
	}
	if reason == "" {
		reason = TerminationReasonFromContext(ctx, TerminationProjectDeleted)
	}
	r.jobs.mu.Lock()
	r.jobs.deletingProjects[projectID] = true
	defer func() {
		r.jobs.mu.Lock()
		delete(r.jobs.deletingProjects, projectID)
		r.jobs.mu.Unlock()
	}()
	seen := make(map[string]*Process)
	for handle, proc := range r.jobs.projectProcs[projectID] {
		if proc != nil {
			seen[handle] = proc
		}
		delete(r.jobs.workDirs, handle)
		delete(r.jobs.ports, handle)
		delete(r.jobs.pids, handle)
	}
	delete(r.jobs.projectProcs, projectID)
	for sessionID, sessionProcs := range r.jobs.sessions {
		for handle, proc := range sessionProcs {
			if proc == nil || proc.ProjectID != projectID {
				continue
			}
			seen[handle] = proc
			delete(sessionProcs, handle)
			delete(r.jobs.workDirs, handle)
			delete(r.jobs.ports, handle)
			delete(r.jobs.pids, handle)
		}
		if len(sessionProcs) == 0 {
			delete(r.jobs.sessions, sessionID)
		}
	}
	r.jobs.mu.Unlock()
	for _, proc := range seen {
		r.killProcess(proc, reason)
	}
	for _, proc := range seen {
		if err := r.awaitSettled(ctx, proc); err != nil {
			return err
		}
		r.releaseIndexWatch(proc)
	}
	return nil
}
