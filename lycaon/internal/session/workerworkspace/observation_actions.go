package workerworkspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// WorkspaceObservationOps provides read/write observation, verification binding,
// trunk change delivery, and next-turn notice draining on a worker workspace Service.
type WorkspaceObservationOps struct{ svc *Service }

// ObserveReadPath records a file read observation on the worker workspace's scoped tracker.
func (o *WorkspaceObservationOps) ObserveReadPath(
	jobID, childSessionID, workspacePath, branchRoot string,
	roots []projectroot.RootRef,
	rawPath string,
) (observation.SourceObservation, bool) {
	if o == nil || o.svc == nil || strings.TrimSpace(rawPath) == "" {
		return observation.SourceObservation{}, false
	}
	tr := o.svc.EnsureTracker(jobID, childSessionID, workspacePath, branchRoot, roots)
	if tr == nil {
		return observation.SourceObservation{}, false
	}
	qpath, diskPath := resolveQualifiedAndDiskPath(workspacePath, branchRoot, roots, rawPath)
	layer := layeredfs.LayerTrunk
	if isTouchedForJob(o.svc, jobID, qpath.Rel) {
		layer = layeredfs.LayerPrivate
	}
	ev := layeredfs.ObservationEvent{
		Path:  qpath,
		Layer: layer,
		Kind:  layeredfs.ObserveRead,
	}
	if diskPath != "" {
		if info, err := os.Lstat(diskPath); err == nil {
			ev.Size = info.Size()
			ev.ModTimeNS = info.ModTime().UnixNano()
			if sys, ok := info.Sys().(*syscall.Stat_t); ok && sys != nil {
				ev.Ino = uint64(sys.Ino)
				ev.Dev = uint64(sys.Dev)
			}
			if info.Mode().IsRegular() {
				if data, rErr := os.ReadFile(diskPath); rErr == nil {
					sum := sha256.Sum256(data)
					ev.SHA256 = hex.EncodeToString(sum[:])
					ev.Size = int64(len(data))
				}
			}
		}
	}
	return tr.RecordServerFSObservation(ev), true
}

// RecordUpperWrite records a worker-initiated upper write on the workspace's scoped tracker,
// pinning the path as LayerPrivate so lower trunk changes cannot clobber or invalidate it.
func (o *WorkspaceObservationOps) RecordUpperWrite(
	jobID, childSessionID, workspacePath, branchRoot string,
	roots []projectroot.RootRef,
	rawPath string,
) {
	if o == nil || o.svc == nil || strings.TrimSpace(rawPath) == "" {
		return
	}
	tr := o.svc.EnsureTracker(jobID, childSessionID, workspacePath, branchRoot, roots)
	if tr == nil {
		return
	}
	qpath, diskPath := resolveQualifiedAndDiskPath(workspacePath, branchRoot, roots, rawPath)
	ev := layeredfs.ObservationEvent{
		Path:      qpath,
		Layer:     layeredfs.LayerPrivate,
		Kind:      layeredfs.ObserveRead,
		ModTimeNS: time.Now().UnixNano(),
	}
	if diskPath != "" {
		if data, err := os.ReadFile(diskPath); err == nil {
			sum := sha256.Sum256(data)
			ev.SHA256 = hex.EncodeToString(sum[:])
			ev.Size = int64(len(data))
		}
	}
	tr.RecordServerFSObservation(ev)
	tr.RecordWorkerMutation(qpath, observation.TrunkWriteInPlace)
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

func collectTargetTrackers(r *WorkspaceTrackerRegistry, key string) []*observation.Tracker {
	if r == nil {
		return nil
	}
	r.obsMu.RLock()
	defer r.obsMu.RUnlock()
	var targets []*observation.Tracker
	if strings.TrimSpace(key) != "" {
		if st := lookupStateLocked(r, key); st != nil && st.Tracker != nil {
			targets = append(targets, st.Tracker)
		}
		return targets
	}
	seen := make(map[*observation.Tracker]bool)
	for _, st := range r.trackers {
		if st != nil && st.Tracker != nil && !seen[st.Tracker] {
			seen[st.Tracker] = true
			targets = append(targets, st.Tracker)
		}
	}
	return targets
}

func isTouchedForJob(m *Service, jobID, relPath string) bool {
	if m == nil || m.Touches == nil {
		return false
	}
	jobID = strings.TrimSpace(jobID)
	relPath = filepath.ToSlash(filepath.Clean(strings.TrimSpace(relPath)))
	if jobID == "" || relPath == "" || relPath == "." {
		return false
	}
	for _, p := range m.Touches.Paths(jobID) {
		if p == relPath {
			return true
		}
	}
	return false
}

func resolveQualifiedAndDiskPath(
	workspacePath, branchRoot string,
	roots []projectroot.RootRef,
	rawPath string,
) (layeredfs.QualifiedPath, string) {
	rawPath = strings.TrimSpace(rawPath)
	if colon := strings.IndexByte(rawPath, ':'); colon > 0 && !filepath.IsAbs(rawPath) {
		rootID := strings.TrimSpace(rawPath[:colon])
		rel := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(strings.TrimSpace(rawPath[colon+1:]), "/")))
		for _, r := range roots {
			if r.ID == rootID {
				return layeredfs.QualifiedPath{RootID: rootID, Rel: rel}, filepath.Join(r.Path, filepath.FromSlash(rel))
			}
		}
	}
	cleanRel := filepath.ToSlash(filepath.Clean(rawPath))
	baseDir := firstNonEmpty(cleanOptionalPath(branchRoot), cleanOptionalPath(workspacePath))
	if filepath.IsAbs(rawPath) && baseDir != "" {
		if rel, err := filepath.Rel(baseDir, rawPath); err == nil && !strings.HasPrefix(rel, "..") {
			cleanRel = filepath.ToSlash(filepath.Clean(rel))
		}
	}
	cleanRel = strings.TrimPrefix(cleanRel, "/")
	diskPath := ""
	if baseDir != "" {
		diskPath = filepath.Join(baseDir, filepath.FromSlash(cleanRel))
	}
	return layeredfs.QualifiedPath{RootID: "primary", Rel: cleanRel}, diskPath
}
