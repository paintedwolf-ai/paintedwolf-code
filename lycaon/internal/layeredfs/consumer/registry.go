package consumer

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

var branchEngineRegistry sync.Map // map[string]*Engine

// RegisterBranchEngine associates a worker branch root or mount directory with its consumer Engine.
func RegisterBranchEngine(branchRoot string, eng *Engine) {
	cleaned := filepath.Clean(strings.TrimSpace(branchRoot))
	if cleaned == "" || cleaned == "." || eng == nil {
		return
	}
	branchEngineRegistry.Store(cleaned, eng)
}

// UnregisterBranchEngine removes a branch root association from the active Engine registry.
func UnregisterBranchEngine(branchRoot string) {
	cleaned := filepath.Clean(strings.TrimSpace(branchRoot))
	if cleaned == "" || cleaned == "." {
		return
	}
	branchEngineRegistry.Delete(cleaned)
}

// UnregisterEngine removes every association of eng, so nothing can reach a settled view.
func UnregisterEngine(eng *Engine) {
	if eng == nil {
		return
	}
	branchEngineRegistry.Range(func(k, v any) bool {
		if v.(*Engine) == eng {
			branchEngineRegistry.Delete(k)
		}
		return true
	})
}

// RestoreEngine re-registers eng for its view's paths and branchRoot after a settlement failed, so a
// view that is still live stays reachable.
func RestoreEngine(eng *Engine, branchRoot string) {
	if eng == nil {
		return
	}
	eng.registerViewPaths()
	RegisterBranchEngine(branchRoot, eng)
}

// LookupBranchEngine finds the registered consumer Engine for branchRoot or a path below it.
func LookupBranchEngine(branchRoot string) (*Engine, bool) {
	cleaned := filepath.Clean(strings.TrimSpace(branchRoot))
	if cleaned == "" || cleaned == "." {
		return nil, false
	}
	if v, ok := branchEngineRegistry.Load(cleaned); ok {
		return v.(*Engine), true
	}
	var matched *Engine
	branchEngineRegistry.Range(func(k, v any) bool {
		if hasPathPrefix(cleaned, k.(string)) {
			matched = v.(*Engine)
			return false
		}
		return true
	})
	return matched, matched != nil
}

// ResolveBranchEnginePath maps a branchRoot and relative path to a registered (Engine, rootID, rootRel).
func ResolveBranchEnginePath(branchRoot, rel string) (*Engine, string, string, bool) {
	eng, ok := LookupBranchEngine(branchRoot)
	if !ok || eng == nil {
		return nil, "", "", false
	}
	targetAbs := filepath.Clean(strings.TrimSpace(branchRoot))
	cleanRel := strings.Trim(filepath.ToSlash(strings.TrimSpace(rel)), "/")
	if cleanRel != "" && cleanRel != "." {
		targetAbs = filepath.Join(targetAbs, filepath.FromSlash(cleanRel))
	}
	for _, rootID := range eng.rootOrder {
		mnt, hasMnt := eng.mountedPaths[rootID]
		if hasMnt && hasPathPrefix(targetAbs, mnt) {
			sub, err := filepath.Rel(mnt, targetAbs)
			if err == nil {
				sub = filepath.ToSlash(sub)
				if sub == "" {
					sub = "."
				}
				return eng, rootID, sub, true
			}
		}
	}
	rootID := eng.PrimaryRootID()
	if rootID == "" {
		return nil, "", "", false
	}
	if cleanRel == "" {
		cleanRel = "."
	}
	if len(eng.rootOrder) > 1 && cleanRel != "." {
		parts := strings.SplitN(cleanRel, "/", 2)
		if _, exists := eng.rootsByID[parts[0]]; exists {
			if len(parts) == 1 {
				return eng, parts[0], ".", true
			}
			return eng, parts[0], parts[1], true
		}
	}
	return eng, rootID, cleanRel, true
}

// HasIndex reports whether a SourceIndexProvider is attached to the Engine.
func (e *Engine) HasIndex() bool { return e != nil && e.index != nil }

// PrimaryRootID returns the primary root ID (or first root ID) configured on the Engine.
func (e *Engine) PrimaryRootID() string {
	for _, id := range e.rootOrder {
		if e.rootsByID[id].IsPrimary {
			return id
		}
	}
	if len(e.rootOrder) > 0 {
		return e.rootOrder[0]
	}
	return ""
}

// MountedRootSpecs returns the Engine's roots with TrunkPath set to each root's mounted workspace path.
func (e *Engine) MountedRootSpecs() []layeredfs.RootSpec {
	out := make([]layeredfs.RootSpec, 0, len(e.rootOrder))
	for _, id := range e.rootOrder {
		spec := e.rootsByID[id]
		if mnt, ok := e.mountedPaths[id]; ok && mnt != "" {
			spec.TrunkPath = mnt
		}
		out = append(out, spec)
	}
	return out
}

// DivergenceRelativePaths returns sorted relative (or root-prefixed when multi-root) divergence
// paths for promotion preview and completion checks in O(changed) time (#1664).
func (e *Engine) DivergenceRelativePaths(ctx context.Context, checkpointID string) ([]string, error) {
	sum, err := e.ComputeDivergence(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	multiRoot := len(e.rootOrder) > 1
	seen := make(map[string]struct{}, len(sum.Entries)*2)
	out := make([]string, 0, len(sum.Entries))
	addQP := func(qp layeredfs.QualifiedPath) {
		if qp.Rel == "" || qp.Rel == "." || IsInternalStoragePath(qp.Rel) {
			return
		}
		p := qp.Rel
		if multiRoot && qp.RootID != "" {
			p = qp.RootID + "/" + qp.Rel
		}
		if _, exists := seen[p]; !exists {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	for _, d := range sum.Entries {
		if d.PreviousPath != nil {
			addQP(*d.PreviousPath)
		}
		addQP(d.Path)
	}
	sort.Strings(out)
	return out, nil
}
