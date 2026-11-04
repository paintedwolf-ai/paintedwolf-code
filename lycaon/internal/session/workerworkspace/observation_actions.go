package workerworkspace

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/tools"
)

// WorkspaceObservationOps provides read/write observation, verification binding,
// trunk change delivery, and next-turn notice draining on a worker workspace Service.
type WorkspaceObservationOps struct{ svc *Service }

// ObserveWorkerRead implements tools.WorkerObservationCoordinator for structured read facts.
func (o *WorkspaceObservationOps) ObserveWorkerRead(ctx context.Context, tctx tools.ToolContext, facts tools.WorkerFileObservationFacts) {
	if o == nil || o.svc == nil {
		return
	}
	jobID := firstNonEmpty(tctx.Identity.WorkerJobID, workercontext.Job(ctx))
	sessionID := strings.TrimSpace(tctx.Identity.SessionID)
	if jobID == "" && sessionID == "" && strings.TrimSpace(tctx.Source.WorkerBranchRoot) == "" {
		return
	}
	_, _ = o.ObserveReadWithFacts(
		jobID,
		sessionID,
		tctx.ActiveRootPath(),
		tctx.Source.WorkerBranchRoot,
		tctx.Source.Roots,
		facts,
	)
}

// ObserveWorkerWrite implements tools.WorkerObservationCoordinator for structured write facts.
func (o *WorkspaceObservationOps) ObserveWorkerWrite(ctx context.Context, tctx tools.ToolContext, facts tools.WorkerFileObservationFacts) {
	if o == nil || o.svc == nil {
		return
	}
	jobID := firstNonEmpty(tctx.Identity.WorkerJobID, workercontext.Job(ctx))
	sessionID := strings.TrimSpace(tctx.Identity.SessionID)
	if jobID == "" && sessionID == "" && strings.TrimSpace(tctx.Source.WorkerBranchRoot) == "" {
		return
	}
	o.RecordUpperWriteWithFacts(
		jobID,
		sessionID,
		tctx.ActiveRootPath(),
		tctx.Source.WorkerBranchRoot,
		tctx.Source.Roots,
		facts,
	)
}

// ObserveReadWithFacts records a file read observation using structured facts from the read boundary
// without performing a second file read. When digest facts are unavailable, it preserves explicit
// unknown/partial evidence.
func (o *WorkspaceObservationOps) ObserveReadWithFacts(
	jobID, childSessionID, workspacePath, branchRoot string,
	roots []projectroot.RootRef,
	facts tools.WorkerFileObservationFacts,
) (observation.SourceObservation, bool) {
	rawPath := firstNonEmpty(facts.RelPath, facts.AbsPath)
	if o == nil || o.svc == nil || rawPath == "" {
		return observation.SourceObservation{}, false
	}
	tr := o.svc.EnsureTracker(jobID, childSessionID, workspacePath, branchRoot, roots)
	if tr == nil {
		return observation.SourceObservation{}, false
	}
	qpath, diskPath := resolveFactsPath(workspacePath, branchRoot, roots, facts)
	layer := resolveObservationLayer(o.svc, tr, jobID, qpath, facts.Layer)
	sha := strings.TrimSpace(facts.SHA256)
	if !facts.Complete {
		sha = ""
	}
	ev := layeredfs.ObservationEvent{
		Path:      qpath,
		Layer:     layer,
		Kind:      layeredfs.ObserveRead,
		SHA256:    sha,
		Size:      facts.Size,
		ModTimeNS: facts.ModTimeNS,
		Ino:       facts.Ino,
		Dev:       facts.Dev,
	}
	if ev.ModTimeNS == 0 && ev.Ino == 0 && diskPath != "" {
		populateStatOnly(&ev, diskPath)
	}
	return tr.RecordServerFSObservation(ev), true
}

// ObserveReadPath records a path observation when structured read-time content digest facts
// are not provided, preserving explicit unknown digest / partial attribution evidence.
func (o *WorkspaceObservationOps) ObserveReadPath(
	jobID, childSessionID, workspacePath, branchRoot string,
	roots []projectroot.RootRef,
	rawPath string,
) (observation.SourceObservation, bool) {
	return o.ObserveReadWithFacts(jobID, childSessionID, workspacePath, branchRoot, roots, tools.WorkerFileObservationFacts{
		RelPath:  rawPath,
		Complete: false,
	})
}

// RecordUpperWriteWithFacts records a worker-initiated upper write using structured write facts,
// pinning the path as LayerPrivate without re-reading the file from disk.
func (o *WorkspaceObservationOps) RecordUpperWriteWithFacts(
	jobID, childSessionID, workspacePath, branchRoot string,
	roots []projectroot.RootRef,
	facts tools.WorkerFileObservationFacts,
) {
	rawPath := firstNonEmpty(facts.RelPath, facts.AbsPath)
	if o == nil || o.svc == nil || rawPath == "" {
		return
	}
	tr := o.svc.EnsureTracker(jobID, childSessionID, workspacePath, branchRoot, roots)
	if tr == nil {
		return
	}
	qpath, diskPath := resolveFactsPath(workspacePath, branchRoot, roots, facts)
	sha := strings.TrimSpace(facts.SHA256)
	if !facts.Complete {
		sha = ""
	}
	if sha == "" {
		if latest, ok := tr.LatestObservationForPath(qpath); ok && latest.Layer == layeredfs.LayerPrivate && latest.SHA256 != "" {
			tr.RecordWorkerMutation(qpath, observation.TrunkWriteInPlace)
			return
		}
	}
	modNS := facts.ModTimeNS
	if modNS == 0 {
		modNS = time.Now().UnixNano()
	}
	ev := layeredfs.ObservationEvent{
		Path:      qpath,
		Layer:     layeredfs.LayerPrivate,
		Kind:      layeredfs.ObserveRead,
		SHA256:    sha,
		Size:      facts.Size,
		ModTimeNS: modNS,
		Ino:       facts.Ino,
		Dev:       facts.Dev,
	}
	if ev.Ino == 0 && diskPath != "" {
		populateStatOnly(&ev, diskPath)
	}
	tr.RecordServerFSObservation(ev)
	tr.RecordWorkerMutation(qpath, observation.TrunkWriteInPlace)
}

// RecordUpperWrite pins rawPath as LayerPrivate on the workspace's scoped tracker.
func (o *WorkspaceObservationOps) RecordUpperWrite(
	jobID, childSessionID, workspacePath, branchRoot string,
	roots []projectroot.RootRef,
	rawPath string,
) {
	o.RecordUpperWriteWithFacts(jobID, childSessionID, workspacePath, branchRoot, roots, tools.WorkerFileObservationFacts{
		RelPath:  rawPath,
		Layer:    "private",
		Complete: false,
	})
}

// BindVerificationReceipt snapshots and binds the current observed sources and scope generation
// on the worker workspace's scoped tracker.
func (o *WorkspaceObservationOps) BindVerificationReceipt(
	jobID, childSessionID, workspacePath, branchRoot, checkID, checkpointID string,
	scopeRoots []string,
) (observation.VerificationBinding, bool) {
	if o == nil || o.svc == nil {
		return observation.VerificationBinding{}, false
	}
	tr := o.svc.EnsureTracker(jobID, childSessionID, workspacePath, branchRoot, nil)
	if tr == nil {
		return observation.VerificationBinding{}, false
	}
	checkID = strings.TrimSpace(checkID)
	checkpointID = strings.TrimSpace(checkpointID)
	binding := tr.BindVerificationReceipt(checkID, checkpointID, scopeRoots)

	o.svc.obsMu.Lock()
	defer o.svc.obsMu.Unlock()
	if st := lookupStateLocked(&o.svc.WorkspaceTrackerRegistry, jobID, childSessionID, branchRoot, workspacePath); st != nil {
		if checkpointID != "" {
			st.CheckpointID = checkpointID
		}
		st.LatestBinding = binding
		st.HasBinding = true
		if checkID != "" {
			st.Bindings[checkID] = binding
		}
	}
	return binding, true
}

// BindCheck binds a verification check receipt on the workspace tracker matching key.
func (o *WorkspaceObservationOps) BindCheck(
	key, checkID, checkpointID string,
	scopeRoots []string,
) (observation.VerificationBinding, bool) {
	if o == nil || o.svc == nil {
		return observation.VerificationBinding{}, false
	}
	o.svc.obsMu.Lock()
	defer o.svc.obsMu.Unlock()
	st := lookupStateLocked(&o.svc.WorkspaceTrackerRegistry, key)
	if st == nil || st.Tracker == nil {
		return observation.VerificationBinding{}, false
	}
	checkID = strings.TrimSpace(checkID)
	checkpointID = strings.TrimSpace(checkpointID)
	binding := st.Tracker.BindVerificationReceipt(checkID, checkpointID, scopeRoots)
	if checkpointID != "" {
		st.CheckpointID = checkpointID
	}
	st.LatestBinding = binding
	st.HasBinding = true
	if checkID != "" {
		st.Bindings[checkID] = binding
	}
	return binding, true
}

// RecordLowerTrunkChange dispatches a lower trunk mutation to all active workspace trackers
// (or the tracker matching key when key is non-empty).
func (o *WorkspaceObservationOps) RecordLowerTrunkChange(key string, ev observation.TrunkChangeEvent) []observation.InvalidationEvent {
	if o == nil || o.svc == nil {
		return nil
	}
	targets := collectTargetTrackers(&o.svc.WorkspaceTrackerRegistry, key)
	var out []observation.InvalidationEvent
	for _, tr := range targets {
		out = append(out, tr.RecordTrunkChange(ev)...)
	}
	return out
}

// RecordWatcherOverflow marks trunk observations as uncertain on matching workspace trackers.
func (o *WorkspaceObservationOps) RecordWatcherOverflow(key, rootID, reason string) {
	if o == nil || o.svc == nil {
		return
	}
	for _, tr := range collectTargetTrackers(&o.svc.WorkspaceTrackerRegistry, key) {
		tr.RecordWatcherOverflow(rootID, reason)
	}
}

// DrainTrunkChangedNotice consumes and formats any pending trunk.changed notice for the next model turn.
func (o *WorkspaceObservationOps) DrainTrunkChangedNotice(keys ...string) (*observation.TrunkChangedNotice, string) {
	if o == nil || o.svc == nil {
		return nil, ""
	}
	tr := o.svc.TrackerFor(keys...)
	if tr == nil {
		return nil, ""
	}
	notice, ok := tr.ConsumeTrunkChangedNotice()
	if !ok || notice == nil {
		return nil, ""
	}
	raw, err := json.Marshal(notice)
	if err != nil {
		return notice, observation.TrunkChangedEventType
	}
	return notice, observation.TrunkChangedEventType + ": " + string(raw)
}

func populateStatOnly(ev *layeredfs.ObservationEvent, diskPath string) {
	info, err := os.Lstat(diskPath)
	if err != nil {
		return
	}
	if ev.Size == 0 {
		ev.Size = info.Size()
	}
	if ev.ModTimeNS == 0 {
		ev.ModTimeNS = info.ModTime().UnixNano()
	}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok && sys != nil {
		if ev.Ino == 0 {
			ev.Ino = uint64(sys.Ino)
		}
		if ev.Dev == 0 {
			ev.Dev = uint64(sys.Dev)
		}
	}
}
