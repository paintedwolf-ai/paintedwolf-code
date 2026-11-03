package workerworkspace

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/projectroot"
)

type serviceContextKey struct{}

// WithService attaches a scoped *Service to ctx so callers without a direct Host receiver
// can resolve workspace-owned observation trackers without process-global state.
func WithService(ctx context.Context, svc *Service) context.Context {
	if ctx == nil || svc == nil {
		return ctx
	}
	return context.WithValue(ctx, serviceContextKey{}, svc)
}

// FromContext retrieves a scoped *Service from ctx if present.
func FromContext(ctx context.Context) *Service {
	if ctx == nil {
		return nil
	}
	svc, _ := ctx.Value(serviceContextKey{}).(*Service)
	return svc
}

// WorkspaceObservationState holds the scoped observation.Tracker and verification bindings.
type WorkspaceObservationState struct {
	JobID, ChildSessionID, WorkspacePath, BranchRoot, CheckpointID string
	Roots                                                          []projectroot.RootRef
	Tracker                                                        *observation.Tracker
	Bindings                                                       map[string]observation.VerificationBinding
	LatestBinding                                                  observation.VerificationBinding
	HasBinding                                                     bool
}

// WorkspaceTrackerRegistry owns scoped *observation.Tracker instances per worker workspace.
type WorkspaceTrackerRegistry struct {
	svc      *Service
	obsMu    sync.RWMutex
	trackers map[string]*WorkspaceObservationState
}

// EnsureTracker provisions or updates a scoped *observation.Tracker for the given worker identity keys.
func (r *WorkspaceTrackerRegistry) EnsureTracker(
	jobID, childSessionID, workspacePath, branchRoot string,
	roots []projectroot.RootRef,
) *observation.Tracker {
	if r == nil {
		return nil
	}
	jobID = strings.TrimSpace(jobID)
	childSessionID = strings.TrimSpace(childSessionID)
	workspacePath = cleanOptionalPath(workspacePath)
	branchRoot = cleanOptionalPath(branchRoot)

	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	if r.trackers == nil {
		r.trackers = make(map[string]*WorkspaceObservationState)
	}

	st := lookupStateLocked(r, jobID, childSessionID, branchRoot)
	if st == nil && jobID == "" && childSessionID == "" && branchRoot == "" {
		st = lookupStateLocked(r, workspacePath)
	}
	if st == nil {
		viewID := firstNonEmpty(jobID, childSessionID, branchRoot, workspacePath, "workspace-view")
		facts := observation.LiveMountVisibilityFacts("workspace-overlay", true, true, true, observation.CompletenessComplete)
		rootSpecs := buildRootSpecs(workspacePath, roots)
		tr, err := observation.NewTracker(observation.TrackerConfig{
			ViewID: viewID,
			Facts:  facts,
			Roots:  rootSpecs,
		})
		if err != nil {
			return nil
		}
		st = &WorkspaceObservationState{
			Tracker:  tr,
			Bindings: make(map[string]observation.VerificationBinding),
		}
	} else if st.Tracker != nil {
		rootSpecs := buildRootSpecs(firstNonEmpty(workspacePath, st.WorkspacePath), roots)
		if len(rootSpecs) > 0 {
			st.Tracker.BindCore(nil, rootSpecs...)
		}
	}

	if jobID != "" {
		st.JobID = jobID
	}
	if childSessionID != "" {
		st.ChildSessionID = childSessionID
	}
	if workspacePath != "" {
		st.WorkspacePath = workspacePath
	}
	if branchRoot != "" {
		st.BranchRoot = branchRoot
	}
	if len(roots) > 0 {
		st.Roots = append([]projectroot.RootRef(nil), roots...)
	}
	indexStateLocked(r, st)
	return st.Tracker
}

// AttachTracker binds an existing *observation.Tracker (and optional VerificationBinding) to scoped keys.
func (r *WorkspaceTrackerRegistry) AttachTracker(tracker *observation.Tracker, binding observation.VerificationBinding, checkpointID string, keys ...string) {
	if r == nil || tracker == nil {
		return
	}
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	if r.trackers == nil {
		r.trackers = make(map[string]*WorkspaceObservationState)
	}
	st := lookupStateLocked(r, keys...)
	if st == nil {
		st = &WorkspaceObservationState{Bindings: make(map[string]observation.VerificationBinding)}
	}
	st.Tracker = tracker
	if strings.TrimSpace(checkpointID) != "" {
		st.CheckpointID = strings.TrimSpace(checkpointID)
	}
	if binding.ReceiptID != "" || binding.ViewID != "" || binding.ScopeSourceGeneration > 0 {
		st.LatestBinding, st.HasBinding = binding, true
		if binding.ReceiptID != "" {
			st.Bindings[binding.ReceiptID] = binding
		}
	}
	indexStateLocked(r, st, keys...)
}

// CleanupTracker removes tracker registrations for the given keys.
func (r *WorkspaceTrackerRegistry) CleanupTracker(keys ...string) {
	if r == nil {
		return
	}
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	st := lookupStateLocked(r, keys...)
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k != "" {
			delete(r.trackers, k)
		}
	}
	if st != nil {
		for _, k := range []string{st.JobID, st.ChildSessionID, st.BranchRoot} {
			if k != "" {
				delete(r.trackers, k)
			}
		}
	}
}

// TrackerFor returns the *observation.Tracker matching the first known key.
func (r *WorkspaceTrackerRegistry) TrackerFor(keys ...string) *observation.Tracker {
	if r == nil {
		return nil
	}
	r.obsMu.RLock()
	defer r.obsMu.RUnlock()
	if st := lookupStateLocked(r, keys...); st != nil {
		return st.Tracker
	}
	return nil
}

// TrackerForSession implements verification.WorkspaceTrackerProvider.
func (r *WorkspaceTrackerRegistry) TrackerForSession(jobID, sessionID, workspaceRoot, workspacePath string) (*observation.Tracker, string) {
	if r == nil {
		return nil, ""
	}
	r.obsMu.RLock()
	defer r.obsMu.RUnlock()
	st := lookupStateLocked(r, jobID, sessionID, cleanOptionalPath(workspaceRoot), cleanOptionalPath(workspacePath))
	if st == nil || st.Tracker == nil {
		return nil, ""
	}
	return st.Tracker, st.CheckpointID
}

// BindingForCheck returns a recorded VerificationBinding for checkID or the workspace's latest binding.
func (r *WorkspaceTrackerRegistry) BindingForCheck(jobID, sessionID, workspaceRoot, checkID string) (observation.VerificationBinding, bool) {
	if r == nil {
		return observation.VerificationBinding{}, false
	}
	r.obsMu.RLock()
	defer r.obsMu.RUnlock()
	st := lookupStateLocked(r, jobID, sessionID, cleanOptionalPath(workspaceRoot))
	if st == nil {
		return observation.VerificationBinding{}, false
	}
	checkID = strings.TrimSpace(checkID)
	if checkID != "" {
		if b, ok := st.Bindings[checkID]; ok {
			return b, true
		}
	}
	if st.HasBinding {
		return st.LatestBinding, true
	}
	return observation.VerificationBinding{}, false
}

// LatestBindingFor returns the tracker, latest verification binding, and checkpoint ID for a task/session.
func (r *WorkspaceTrackerRegistry) LatestBindingFor(
	jobID, sessionID, workspaceRoot, workspacePath string,
) (*observation.Tracker, observation.VerificationBinding, string, bool) {
	if r == nil {
		return nil, observation.VerificationBinding{}, "", false
	}
	r.obsMu.RLock()
	defer r.obsMu.RUnlock()
	st := lookupStateLocked(r, jobID, sessionID, cleanOptionalPath(workspaceRoot), cleanOptionalPath(workspacePath))
	if st == nil || st.Tracker == nil {
		return nil, observation.VerificationBinding{}, "", false
	}
	binding := st.LatestBinding
	if !st.HasBinding {
		binding = st.Tracker.BindVerificationReceipt("", st.CheckpointID, nil)
	}
	return st.Tracker, binding, st.CheckpointID, true
}

func lookupStateLocked(r *WorkspaceTrackerRegistry, keys ...string) *WorkspaceObservationState {
	if r == nil || len(r.trackers) == 0 {
		return nil
	}
	for _, raw := range keys {
		k := strings.TrimSpace(raw)
		if k == "" {
			continue
		}
		if st := r.trackers[k]; st != nil {
			return st
		}
		if cleaned := cleanOptionalPath(k); cleaned != "" {
			if st := r.trackers[cleaned]; st != nil {
				return st
			}
		}
	}
	return nil
}

func indexStateLocked(r *WorkspaceTrackerRegistry, st *WorkspaceObservationState, keys ...string) {
	all := append([]string{st.JobID, st.ChildSessionID, st.BranchRoot}, keys...)
	for _, raw := range all {
		k := strings.TrimSpace(raw)
		if k == "" {
			continue
		}
		r.trackers[k] = st
	}
	if wp := strings.TrimSpace(st.WorkspacePath); wp != "" && r.trackers[wp] == nil {
		r.trackers[wp] = st
	}
}

func buildRootSpecs(primaryPath string, roots []projectroot.RootRef) []layeredfs.RootSpec {
	var specs []layeredfs.RootSpec
	seen := make(map[string]bool)
	for _, r := range roots {
		id := strings.TrimSpace(r.ID)
		p := cleanOptionalPath(r.Path)
		if id == "" || p == "" || seen[id] {
			continue
		}
		seen[id] = true
		specs = append(specs, layeredfs.RootSpec{ID: id, TrunkPath: p})
	}
	if primaryPath != "" && !seen["primary"] {
		specs = append(specs, layeredfs.RootSpec{ID: "primary", TrunkPath: primaryPath})
	}
	return specs
}

func cleanOptionalPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
