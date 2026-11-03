package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// ValidateWorkspaceSelection validates an explicit WorkspaceSelection against attached RootGrants.
// Selection narrows the workspace and never confers authority. TaskPathHints are never used here.
func ValidateWorkspaceSelection(grants []RootGrant, sel WorkspaceSelection) ([]RootGrant, error) {
	if len(grants) == 0 {
		return nil, &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: "at least one attached RootGrant is required"}
	}
	byID := make(map[string]RootGrant, len(grants))
	for _, g := range grants {
		if g.RootID == "" || g.SourcePath == "" {
			return nil, &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: "grant must have non-empty RootID and SourcePath"}
		}
		byID[g.RootID] = g
	}

	selectedIDs, err := resolveSelectedRootIDs(grants, byID, sel)
	if err != nil {
		return nil, err
	}

	narrowed := make([]RootGrant, 0, len(selectedIDs))
	hasPrimary := false
	for _, id := range selectedIDs {
		g, err := narrowGrantSubroot(byID[id], sel.Subroots[id])
		if err != nil {
			return nil, err
		}
		if g.IsPrimary {
			hasPrimary = true
		}
		narrowed = append(narrowed, g)
	}
	if !hasPrimary && len(narrowed) > 0 {
		narrowed[0].IsPrimary = true
	}
	return narrowed, nil
}

func resolveSelectedRootIDs(grants []RootGrant, byID map[string]RootGrant, sel WorkspaceSelection) ([]string, error) {
	for subRootID := range sel.Subroots {
		if _, ok := byID[subRootID]; !ok {
			return nil, &AdapterError{Op: "validate_selection", ReasonCode: ReasonUnauthorizedRoot, Diagnostic: fmt.Sprintf("subroot requested for unauthorized root %q", subRootID)}
		}
	}
	if len(sel.RootIDs) == 0 {
		ids := make([]string, 0, len(grants))
		for _, g := range grants {
			ids = append(ids, g.RootID)
		}
		return ids, nil
	}

	seen := make(map[string]bool, len(sel.RootIDs))
	ids := make([]string, 0, len(sel.RootIDs))
	for _, id := range sel.RootIDs {
		if _, ok := byID[id]; !ok {
			return nil, &AdapterError{Op: "validate_selection", ReasonCode: ReasonUnauthorizedRoot, Diagnostic: fmt.Sprintf("selected root %q is not in attached RootGrants", id)}
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for subRootID := range sel.Subroots {
		if !seen[subRootID] {
			return nil, &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: fmt.Sprintf("subroot %q specified for unselected root", subRootID)}
		}
	}
	return ids, nil
}

func narrowGrantSubroot(g RootGrant, reqSubroot string) (RootGrant, error) {
	baseSource := filepath.Clean(g.SourcePath)
	combinedSub := cleanSubrootRel(g.Subroot)
	if reqSubroot != "" {
		cleanReq, err := validateRelativeSubroot(reqSubroot)
		if err != nil {
			return RootGrant{}, err
		}
		if combinedSub == "" {
			combinedSub = cleanReq
		} else {
			combinedSub = filepath.ToSlash(filepath.Join(combinedSub, cleanReq))
		}
		g.Estimate = nil
	}
	if combinedSub != "" {
		targetPath := filepath.Join(baseSource, filepath.FromSlash(combinedSub))
		relCheck, relErr := filepath.Rel(baseSource, targetPath)
		if relErr != nil || strings.HasPrefix(relCheck, "..") {
			return RootGrant{}, &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: fmt.Sprintf("subroot %q escapes grant root %q", reqSubroot, g.RootID)}
		}
		st, err := os.Lstat(targetPath)
		if err != nil || !st.IsDir() {
			return RootGrant{}, &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: fmt.Sprintf("subroot %q in root %q is not an accessible directory", combinedSub, g.RootID)}
		}
		g.SourcePath = targetPath
		g.Subroot = combinedSub
	}
	return g, nil
}

func cleanSubrootRel(sub string) string {
	sub = strings.TrimSpace(sub)
	if sub == "" || sub == "." {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(sub))
}

func validateRelativeSubroot(sub string) (string, error) {
	if filepath.IsAbs(sub) || strings.HasPrefix(sub, "/") || strings.HasPrefix(sub, "\\") {
		return "", &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: fmt.Sprintf("subroot %q must be relative", sub)}
	}
	cleaned := filepath.ToSlash(filepath.Clean(sub))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: fmt.Sprintf("subroot %q escapes grant boundary", sub)}
	}
	if cleaned == "." {
		return "", nil
	}
	return cleaned, nil
}

// Selector chooses one workspace adapter from structured probe capabilities and manages fallback.
type Selector struct {
	TargetOS        string
	LinuxAdapter    Adapter
	DarwinAdapter   Adapter
	FallbackAdapter Adapter
	Registry        *ActiveViewRegistry
}

// NewSelector constructs a capability-based Selector with an ActiveViewRegistry.
func NewSelector(targetOS string, linuxAdp, darwinAdp, fallbackAdp Adapter) *Selector {
	if targetOS == "" {
		targetOS = runtime.GOOS
	}
	if fallbackAdp == nil {
		fallbackAdp = NewEagerCopyAdapter(ReasonNone)
	}
	return &Selector{
		TargetOS:        targetOS,
		LinuxAdapter:    linuxAdp,
		DarwinAdapter:   darwinAdp,
		FallbackAdapter: fallbackAdp,
		Registry:        NewActiveViewRegistry(),
	}
}

// Select probes the platform primary adapter (retrying transient failure once) or selects fallback.
func (s *Selector) Select(ctx context.Context, probeCfg ProbeConfig) (Adapter, ProbeResult, error) {
	primary := s.primaryForOS()
	if primary == nil {
		fb := s.withFallbackReason(ReasonUnsupportedPlatform)
		return fb, ProbeResult{
			AdapterID:  fb.ID(),
			Supported:  false,
			ReasonCode: ReasonUnsupportedPlatform,
			Diagnostic: fmt.Sprintf("no mounted adapter for OS %q; using %s", s.TargetOS, fb.ID()),
		}, nil
	}

	res, err := primary.Probe(ctx, probeCfg)
	attempts := 1
	if (!res.Supported || err != nil) && shouldRetryTransient(res, err) {
		res, err = primary.Probe(ctx, probeCfg)
		attempts = 2
	}
	res.Attempts = attempts
	if err == nil && res.Supported {
		return primary, res, nil
	}

	reason := res.ReasonCode
	if reason == ReasonNone {
		var aErr *AdapterError
		if errors.As(err, &aErr) && aErr.ReasonCode != ReasonNone {
			reason = aErr.ReasonCode
		} else {
			reason = ReasonUnsupportedKernel
		}
	}
	res.ReasonCode = reason
	res.Supported = false
	return s.withFallbackReason(reason), res, nil
}

// SelectAndPrepare selects an adapter (or reuses the fixed adapter for an existing active view) and prepares it.
func (s *Selector) SelectAndPrepare(ctx context.Context, probeCfg ProbeConfig, req PrepareRequest) (Adapter, *PreparedView, ProbeResult, error) {
	if existingAdp, existingView, ok := s.Registry.AdapterForView(req.ViewID); ok {
		return existingAdp, existingView, ProbeResult{AdapterID: existingAdp.ID(), Supported: true}, nil
	}
	adp, probeRes, err := s.Select(ctx, probeCfg)
	if err != nil {
		return nil, nil, probeRes, err
	}
	view, err := adp.Prepare(ctx, req)
	if err != nil {
		return adp, nil, probeRes, err
	}
	s.Registry.Register(view, adp)
	return adp, view, probeRes, nil
}

func (s *Selector) primaryForOS() Adapter {
	switch s.TargetOS {
	case "linux":
		return s.LinuxAdapter
	case "darwin":
		return s.DarwinAdapter
	default:
		return nil
	}
}

func (s *Selector) withFallbackReason(reason ReasonCode) Adapter {
	if ec, ok := s.FallbackAdapter.(*EagerCopyAdapter); ok {
		return ec.WithFallbackReason(reason)
	}
	return s.FallbackAdapter
}

func shouldRetryTransient(res ProbeResult, err error) bool {
	if res.ReasonCode == ReasonTransientStartupFailure {
		return true
	}
	var aErr *AdapterError
	return errors.As(err, &aErr) && aErr.ReasonCode == ReasonTransientStartupFailure
}

// ActiveViewRecovery describes the preserved state of an active view after adapter failure.
type ActiveViewRecovery struct {
	ViewID                           string                    `json:"view_id"`
	OriginalAdapterID                AdapterID                 `json:"original_adapter_id"`
	Mutated                          bool                      `json:"mutated"`
	RequiresCheckpointReconstruction bool                      `json:"requires_checkpoint_reconstruction"`
	PreservedSnapshot                layeredfs.JournalSnapshot `json:"preserved_snapshot"`
	PreservedChanges                 []layeredfs.ChangeEntry   `json:"preserved_changes"`
	TransitionedView                 *PreparedView             `json:"transitioned_view,omitempty"`
}

type activeViewEntry struct {
	view    *PreparedView
	adapter Adapter
	failed  bool
	reason  ReasonCode
}

// ActiveViewRegistry enforces selection immutability for active views and preserves mutated state on failure.
type ActiveViewRegistry struct {
	mu    sync.RWMutex
	views map[string]*activeViewEntry
}

// NewActiveViewRegistry creates an ActiveViewRegistry.
func NewActiveViewRegistry() *ActiveViewRegistry {
	return &ActiveViewRegistry{views: make(map[string]*activeViewEntry)}
}

// Register binds a PreparedView to its selected Adapter.
func (r *ActiveViewRegistry) Register(view *PreparedView, adp Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.views[view.ViewID] = &activeViewEntry{view: view, adapter: adp}
}

// AdapterForView returns the fixed adapter and view for viewID if registered.
func (r *ActiveViewRegistry) AdapterForView(viewID string) (Adapter, *PreparedView, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.views[viewID]
	if !ok {
		return nil, nil, false
	}
	return e.adapter, e.view, true
}

// RecoverOrTransition handles adapter failure on an active view without erasing mutated worker data (#1658, #1666).
func (r *ActiveViewRegistry) RecoverOrTransition(
	ctx context.Context,
	viewID string,
	failureReason ReasonCode,
	replacement Adapter,
	allowCheckpointReconstruction bool,
) (*ActiveViewRecovery, error) {
	r.mu.Lock()
	entry, ok := r.views[viewID]
	if !ok {
		r.mu.Unlock()
		return nil, &AdapterError{Op: "recover_or_transition", ReasonCode: ReasonViewNotFound, Diagnostic: fmt.Sprintf("active view %q not found", viewID)}
	}
	entry.failed = true
	entry.reason = failureReason
	view, adp := entry.view, entry.adapter
	r.mu.Unlock()

	snap, err := view.Core.SnapshotJournal()
	if err != nil {
		return nil, err
	}
	changes, err := view.Core.EnumerateChanges(true)
	if err != nil {
		return nil, err
	}
	mutated := len(changes) > 0 || len(snap.Operations) > 0
	recovery := &ActiveViewRecovery{
		ViewID:                           viewID,
		OriginalAdapterID:                adp.ID(),
		Mutated:                          mutated,
		RequiresCheckpointReconstruction: mutated,
		PreservedSnapshot:                snap,
		PreservedChanges:                 changes,
	}
	if mutated && !allowCheckpointReconstruction {
		return recovery, &AdapterError{
			AdapterID:  adp.ID(),
			Op:         "recover_or_transition",
			ReasonCode: ReasonActiveViewMutated,
			Diagnostic: "refusing to switch mutated worker view to a fresh workspace without #1666 checkpoint reconstruction",
		}
	}
	if replacement != nil {
		probeRes, pErr := replacement.Probe(ctx, ProbeConfig{RuntimeDir: filepath.Dir(view.RuntimeViewDir)})
		if pErr != nil || !probeRes.Supported {
			return recovery, &AdapterError{
				AdapterID:  replacement.ID(),
				Op:         "recover_or_transition",
				ReasonCode: probeRes.ReasonCode,
				Diagnostic: "replacement adapter is unsupported; active worker data remains preserved in Core",
				Cause:      pErr,
			}
		}
	}
	return recovery, nil
}
